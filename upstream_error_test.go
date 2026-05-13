package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSanitizeHeaderForLogRedactsSecretBearingHeaders(t *testing.T) {
	headers := http.Header{
		"Set-Cookie":          []string{"__cf_bm=secret-cookie; Path=/; HttpOnly", "sessionid=secret-session"},
		"Authorization":       []string{"Bearer secret-token"},
		"Proxy-Authorization": []string{"Basic secret-basic"},
		"Cookie":              []string{"a=secret"},
		"Content-Type":        []string{"text/html; charset=UTF-8"},
		"Cf-Mitigated":        []string{"challenge"},
	}

	got := sanitizeHeaderForLog(headers)
	text := got.String()

	for _, forbidden := range []string{"secret-cookie", "secret-session", "secret-token", "secret-basic", "a=secret", "__cf_bm="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized headers leaked %q in %s", forbidden, text)
		}
	}
	for _, want := range []string{"Set-Cookie=[REDACTED]", "Authorization=[REDACTED]", "Proxy-Authorization=[REDACTED]", "Cookie=[REDACTED]", "Content-Type=[text/html; charset=UTF-8]", "Cf-Mitigated=[challenge]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("sanitized headers missing %q in %s", want, text)
		}
	}
}

func TestClassifyUpstreamResponseCloudflareChallengeHeaders(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header: http.Header{
			"Server":        []string{"cloudflare"},
			"Cf-Mitigated":  []string{"challenge"},
			"Server-Timing": []string{"chlray;desc=\"abc\""},
			"Set-Cookie":    []string{"__cf_bm=redacted; Path=/; HttpOnly"},
		},
	}

	info := classifyUpstreamResponse(resp, []byte("<html><title>Just a moment...</title></html>"))

	if info.Class != upstreamCloudflareChallenge {
		t.Fatalf("class = %q", info.Class)
	}
	if !info.Retryable {
		t.Fatal("expected Cloudflare challenge to be retryable across egress/seat")
	}
	if info.SeatPoison {
		t.Fatal("Cloudflare challenge must not poison the OAuth/API seat")
	}
	if !info.EgressChallenge {
		t.Fatal("expected egress challenge marker")
	}
	if info.SafeMessage == "" || info.SafeMessage == "__cf_bm=redacted" {
		t.Fatalf("unsafe/empty safe message: %q", info.SafeMessage)
	}
}

func TestClassifyUpstreamResponseCloudflareChallengeBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{"Server": []string{"cloudflare"}, "Content-Type": []string{"text/html; charset=UTF-8"}},
	}

	info := classifyUpstreamResponse(resp, []byte("<html><body>Checking if the site connection is secure. Enable JavaScript and cookies to continue.</body></html>"))

	if info.Class != upstreamCloudflareChallenge {
		t.Fatalf("class = %q", info.Class)
	}
	if info.SeatPoison {
		t.Fatal("body-detected Cloudflare challenge must not poison seat")
	}
}

func TestClassifyUpstreamResponseProviderPolicyBlock(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{"Content-Type": []string{"application/json"}}}
	body := []byte(`{"error":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program."}`)

	info := classifyUpstreamResponse(resp, body)

	if info.Class != upstreamProviderPolicyBlock {
		t.Fatalf("class = %q", info.Class)
	}
	if !info.Retryable {
		t.Fatal("provider policy block should rotate to another seat without poisoning the current seat")
	}
	if info.SeatPoison || info.EgressChallenge {
		t.Fatalf("policy block flags: seatPoison=%v egressChallenge=%v", info.SeatPoison, info.EgressChallenge)
	}
}

func TestClassifyUpstreamResponseAuthRateLimitAndTransient(t *testing.T) {
	tests := []struct {
		name string
		resp *http.Response
		body []byte
		want upstreamErrorClass
	}{
		{
			name: "gemini unauthenticated",
			resp: &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: http.Header{"Content-Type": []string{"application/json"}}},
			body: []byte(`{"error":{"status":"UNAUTHENTICATED","message":"401 UNAUTHENTICATED"}}`),
			want: upstreamAuthFailure,
		},
		{
			name: "rate limit",
			resp: &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests", Header: http.Header{}},
			body: []byte(`{"error":"quota exhausted"}`),
			want: upstreamRateLimit,
		},
		{
			name: "transient 503",
			resp: &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Header: http.Header{}},
			body: []byte(`upstream unavailable`),
			want: upstreamTransient,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := classifyUpstreamResponse(tt.resp, tt.body)
			if info.Class != tt.want {
				t.Fatalf("class = %q, want %q", info.Class, tt.want)
			}
		})
	}
}

func TestCloudflareChallengeDoesNotLookLikePermanentCodexAuthFailure(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header: http.Header{
			"Server":       []string{"cloudflare"},
			"Cf-Mitigated": []string{"challenge"},
		},
	}
	if isPermanentCodexAuthFailure(resp, []byte("<html>Just a moment...</html>")) {
		t.Fatal("Cloudflare challenge must not be permanent Codex auth failure")
	}
}

func TestApplyPreCopyUpstreamStatusDispositionCloudflareChallengeDoesNotPoisonCodexSeat(t *testing.T) {
	acc := &Account{ID: "codex-1", Type: AccountTypeCodex}
	tracker := newUpstreamErrorTracker(16)
	h := &proxyHandler{upstreamErrors: tracker}
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header: http.Header{
			"Server":        []string{"cloudflare"},
			"Cf-Mitigated":  []string{"challenge"},
			"Server-Timing": []string{"chlray;desc=\"abc\""},
		},
	}

	err := h.applyPreCopyUpstreamStatusDisposition("req-test", acc, resp, false, []byte("<html>Just a moment...</html>"), "", "")

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if acc.Dead {
		t.Fatal("Cloudflare challenge must not mark Codex seat dead")
	}
	if acc.Penalty != 0 {
		t.Fatalf("penalty = %v, want 0", acc.Penalty)
	}
	summary := tracker.summary(time.Now().UTC())
	if summary == nil || summary.CloudflareChallengeCount10m != 1 || summary.LastErrorClass != upstreamCloudflareChallenge.String() {
		t.Fatalf("upstream summary = %+v", summary)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionProviderPolicyBlockDoesNotPoisonCodexSeat(t *testing.T) {
	acc := &Account{ID: "codex-1", Type: AccountTypeCodex}
	h := &proxyHandler{recent: newRecentErrors(5)}
	resp := &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{"Content-Type": []string{"application/json"}}}
	body := []byte(`{"error":"This content was flagged for possible cybersecurity risk. Trusted Access for Cyber"}`)

	err := h.applyPreCopyUpstreamStatusDisposition("req-test", acc, resp, false, body, "", "")

	if err == nil {
		t.Fatal("expected structured provider-policy error")
	}
	if got := err.Error(); got != "upstream_provider_policy_block: 403 Forbidden" {
		t.Fatalf("err = %q", got)
	}
	if acc.Dead {
		t.Fatal("provider policy block must not mark Codex seat dead")
	}
	if acc.Penalty != 0 {
		t.Fatalf("penalty = %v, want 0", acc.Penalty)
	}
}

func TestFormatBufferedRetryStatusErrorUsesStructuredPolicyAndChallengeClasses(t *testing.T) {
	policyResp := &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{"Content-Type": []string{"application/json"}}}
	policyErr := formatBufferedRetryStatusError(policyResp, `{"error":"This content was flagged for possible cybersecurity risk. Trusted Access for Cyber"}`)
	if policyErr == nil || policyErr.Error() != "upstream_provider_policy_block: 403 Forbidden" {
		t.Fatalf("policyErr = %v", policyErr)
	}
	if isNonRetryableUpstreamError(policyErr) {
		t.Fatal("provider policy block should rotate across seats instead of stopping on the first seat")
	}

	challengeResp := &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{"Cf-Mitigated": []string{"challenge"}}}
	challengeErr := formatBufferedRetryStatusError(challengeResp, `<html>Just a moment...</html>`)
	if challengeErr == nil || challengeErr.Error() != "upstream_cloudflare_challenge: 403 Forbidden" {
		t.Fatalf("challengeErr = %v", challengeErr)
	}
	if isNonRetryableUpstreamError(challengeErr) {
		t.Fatal("Cloudflare challenge should remain retryable across alternate route/seat")
	}
}
