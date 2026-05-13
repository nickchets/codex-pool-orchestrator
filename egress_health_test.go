package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamErrorTrackerSummarizesRecentChallengeAndPolicyEvents(t *testing.T) {
	now := time.Date(2026, 5, 10, 20, 45, 0, 0, time.UTC)
	tracker := newUpstreamErrorTracker(16)

	challengeResp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header: http.Header{
			"Cf-Mitigated": []string{"challenge"},
			"Cf-Ray":       []string{"raw-cf-ray-secret-WAW"},
		},
	}
	policyResp := &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{"Content-Type": []string{"application/json"}}}

	tracker.record(classifyUpstreamResponse(challengeResp, []byte("<html>Just a moment...</html>")), challengeResp, now.Add(-9*time.Minute))
	tracker.record(classifyUpstreamResponse(challengeResp, []byte("<html>Just a moment...</html>")), challengeResp, now.Add(-time.Minute))
	tracker.record(classifyUpstreamResponse(policyResp, []byte(`{"error":"This content was flagged for possible cybersecurity risk. Trusted Access for Cyber"}`)), policyResp, now.Add(-30*time.Second))
	tracker.record(classifyUpstreamResponse(challengeResp, []byte("<html>Just a moment...</html>")), challengeResp, now.Add(-11*time.Minute))

	summary := tracker.summary(now)
	if summary == nil {
		t.Fatal("expected summary")
	}
	if summary.LastErrorClass != upstreamProviderPolicyBlock.String() {
		t.Fatalf("last_error_class=%q", summary.LastErrorClass)
	}
	if summary.LastErrorAt != now.Add(-30*time.Second).Format(time.RFC3339) {
		t.Fatalf("last_error_at=%q", summary.LastErrorAt)
	}
	if summary.CloudflareChallengeCount10m != 2 {
		t.Fatalf("cloudflare count=%d", summary.CloudflareChallengeCount10m)
	}
	if summary.ProviderPolicyBlockCount10m != 1 {
		t.Fatalf("policy count=%d", summary.ProviderPolicyBlockCount10m)
	}
	if summary.LastCFRayHash == "" || !strings.HasPrefix(summary.LastCFRayHash, "sha256:") {
		t.Fatalf("bad cf ray hash=%q", summary.LastCFRayHash)
	}
	if strings.Contains(summary.LastCFRayHash, "raw-cf-ray-secret") {
		t.Fatalf("raw cf-ray leaked in hash field: %q", summary.LastCFRayHash)
	}
}

func TestUpstreamErrorTrackerEmptySummaryIsStillVisible(t *testing.T) {
	summary := newUpstreamErrorTracker(16).summary(time.Date(2026, 5, 10, 20, 45, 0, 0, time.UTC))
	if summary == nil {
		t.Fatal("expected zero summary, got nil")
	}
	if summary.CloudflareChallengeCount10m != 0 || summary.ProviderPolicyBlockCount10m != 0 || summary.LastErrorClass != "" {
		t.Fatalf("empty summary = %+v, want zero summary", summary)
	}
}

func TestBuildPoolDashboardDataIncludesUpstreamErrorSummary(t *testing.T) {
	now := time.Date(2026, 5, 10, 20, 45, 0, 0, time.UTC)
	tracker := newUpstreamErrorTracker(16)
	resp := &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{"Cf-Mitigated": []string{"challenge"}, "Cf-Ray": []string{"raw-ray-123"}}}
	tracker.record(classifyUpstreamResponse(resp, []byte("<html>Just a moment...</html>")), resp, now.Add(-time.Minute))

	h := &proxyHandler{
		cfg:            config{codexChatCompletionsResponsesAdapter: true},
		pool:           newPoolState(nil, false),
		startTime:      now.Add(-time.Hour),
		upstreamErrors: tracker,
	}

	data := h.buildPoolDashboardData(now)
	if data.UpstreamErrorSummary == nil {
		t.Fatal("expected upstream_error_summary")
	}
	if data.UpstreamErrorSummary.LastErrorClass != upstreamCloudflareChallenge.String() {
		t.Fatalf("last_error_class=%q", data.UpstreamErrorSummary.LastErrorClass)
	}
	if data.UpstreamErrorSummary.CloudflareChallengeCount10m != 1 {
		t.Fatalf("cloudflare count=%d", data.UpstreamErrorSummary.CloudflareChallengeCount10m)
	}
	if !data.ProtocolAdapters.CodexChatCompletionsResponsesAdapter {
		t.Fatal("expected codex chat/completions responses adapter status enabled")
	}
}
