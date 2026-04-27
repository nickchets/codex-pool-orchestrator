package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newPhase6PolicyRequest(rawKey, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func phase6OKResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestPhase6VirtualKeyOverRPMRejectsBeforeUpstream(t *testing.T) {
	var upstreamCalls int64
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{MaxRPM: 1}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt64(&upstreamCalls, 1)
		return phase6OKResponse(req, `{"id":"ok","usage":{"input_tokens":1,"output_tokens":1}}`), nil
	}))
	body := `{"model":"gpt-5-codex","input":"hi","max_output_tokens":1}`

	first := httptest.NewRecorder()
	h.proxyRequest(first, newPhase6PolicyRequest(rawKey, body), "req-phase6-rpm-1")
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	h.proxyRequest(second, newPhase6PolicyRequest(rawKey, body), "req-phase6-rpm-2")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	if got := atomic.LoadInt64(&upstreamCalls); got != 1 {
		t.Fatalf("upstream calls=%d", got)
	}
}

func TestPhase6VirtualKeyOverConcurrencyRejectsBeforeUpstreamAndReleases(t *testing.T) {
	var upstreamCalls int64
	entered := make(chan struct{})
	release := make(chan struct{})
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{MaxConcurrency: 1}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		call := atomic.AddInt64(&upstreamCalls, 1)
		if call == 1 {
			close(entered)
			<-release
		}
		return phase6OKResponse(req, `{"id":"ok","usage":{"input_tokens":1,"output_tokens":1}}`), nil
	}))
	body := `{"model":"gpt-5-codex","input":"hi","max_output_tokens":1}`

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.proxyRequest(rr, newPhase6PolicyRequest(rawKey, body), "req-phase6-concurrency-1")
		firstDone <- rr
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first request to reach upstream")
	}

	second := httptest.NewRecorder()
	h.proxyRequest(second, newPhase6PolicyRequest(rawKey, body), "req-phase6-concurrency-2")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	if got := atomic.LoadInt64(&upstreamCalls); got != 1 {
		t.Fatalf("upstream calls while slot held=%d", got)
	}

	close(release)
	select {
	case rr := <-firstDone:
		if rr.Code != http.StatusOK {
			t.Fatalf("first status=%d body=%s", rr.Code, rr.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first request to finish")
	}

	third := httptest.NewRecorder()
	h.proxyRequest(third, newPhase6PolicyRequest(rawKey, body), "req-phase6-concurrency-3")
	if third.Code != http.StatusOK {
		t.Fatalf("third status=%d body=%s", third.Code, third.Body.String())
	}
	if got := atomic.LoadInt64(&upstreamCalls); got != 2 {
		t.Fatalf("upstream calls after release=%d", got)
	}
}

func TestPhase6VirtualKeyOverTPMEstimatedReservationRejectsBeforeUpstream(t *testing.T) {
	var upstreamCalls int64
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{MaxTPM: 10}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt64(&upstreamCalls, 1)
		return phase6OKResponse(req, `{"id":"should-not-happen"}`), nil
	}))
	body := `{"model":"gpt-5-codex","input":"hi","max_output_tokens":100}`

	rr := httptest.NewRecorder()
	h.proxyRequest(rr, newPhase6PolicyRequest(rawKey, body), "req-phase6-tpm")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt64(&upstreamCalls); got != 0 {
		t.Fatalf("upstream calls=%d", got)
	}
}

func TestPhase6VirtualKeyTPMEstimateHonorsOutputTokenFieldsAndDefault(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		defaultOutput int
	}{
		{name: "max_tokens", body: `{"model":"gpt-5-codex","input":"hi","max_tokens":100}`},
		{name: "max_completion_tokens", body: `{"model":"gpt-5-codex","input":"hi","max_completion_tokens":100}`},
		{name: "max_output_tokens", body: `{"model":"gpt-5-codex","input":"hi","max_output_tokens":100}`},
		{name: "configured default", body: `{"model":"gpt-5-codex","input":"hi"}`, defaultOutput: 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls int64
			h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{MaxTPM: 50}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt64(&upstreamCalls, 1)
				return phase6OKResponse(req, `{"id":"should-not-happen"}`), nil
			}))
			if tc.defaultOutput > 0 {
				h.cfg.poolAPIDefaultMaxOutputTokens = tc.defaultOutput
			}

			rr := httptest.NewRecorder()
			h.proxyRequest(rr, newPhase6PolicyRequest(rawKey, tc.body), "req-phase6-tpm-fields")
			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if got := atomic.LoadInt64(&upstreamCalls); got != 0 {
				t.Fatalf("upstream calls=%d", got)
			}
		})
	}
}

func TestPhase6VirtualKeyDailyAndMonthlyBudgetsRejectFromStoredUsage(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name      string
		policy    PoolAPITokenPolicy
		timestamp time.Time
		used      int64
	}{
		{
			name:      "daily budget exceeded",
			policy:    PoolAPITokenPolicy{DailyBudget: 5},
			timestamp: now,
			used:      6,
		},
		{
			name:      "monthly budget exceeded",
			policy:    PoolAPITokenPolicy{MonthlyBudget: 50},
			timestamp: now,
			used:      51,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls int64
			h, rawKey := newPhase4VirtualKeyHandler(t, tc.policy, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt64(&upstreamCalls, 1)
				return phase6OKResponse(req, `{"id":"should-not-happen"}`), nil
			}))
			usage, err := newUsageStore(t.TempDir()+"/usage.db", 30)
			if err != nil {
				t.Fatalf("new usage store: %v", err)
			}
			t.Cleanup(func() { _ = usage.Close() })
			h.store = usage
			tokenID, _, err := parsePoolAPIKey(rawKey)
			if err != nil {
				t.Fatalf("parse raw key: %v", err)
			}
			if err := usage.record(RequestUsage{
				Timestamp:      tc.timestamp,
				AccountID:      "codex-seat",
				AccountType:    AccountTypeCodex,
				UserID:         "user1234567890abcdef",
				TokenID:        tokenID,
				TokenName:      "phase4",
				CredentialKind: CredentialKindOpenAICompatiblePoolKey,
				InputTokens:    tc.used,
				BillableTokens: tc.used,
			}); err != nil {
				t.Fatalf("record usage: %v", err)
			}

			rr := httptest.NewRecorder()
			h.proxyRequest(rr, newPhase6PolicyRequest(rawKey, `{"model":"gpt-5-codex","input":"hi","max_output_tokens":1}`), "req-phase6-budget")
			if rr.Code != http.StatusForbidden && rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if got := atomic.LoadInt64(&upstreamCalls); got != 0 {
				t.Fatalf("upstream calls=%d", got)
			}
		})
	}
}

func TestPhase6VirtualKeyActualUsageAboveReservationDoesNotFailCompletedRequest(t *testing.T) {
	var upstreamCalls int64
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{MaxTPM: 40}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt64(&upstreamCalls, 1)
		return phase6OKResponse(req, `{"id":"ok","usage":{"input_tokens":100,"output_tokens":100}}`), nil
	}))
	body := `{"model":"gpt-5-codex","input":"hi","max_output_tokens":1}`

	rr := httptest.NewRecorder()
	h.proxyRequest(rr, newPhase6PolicyRequest(rawKey, body), "req-phase6-actual-usage")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt64(&upstreamCalls); got != 1 {
		t.Fatalf("upstream calls=%d", got)
	}
}

func TestPhase6RealProviderPassthroughUnaffectedByVirtualKeyPolicy(t *testing.T) {
	var upstreamCalls int64
	h, _ := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{MaxRPM: 1, MaxConcurrency: 1, MaxTPM: 1}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt64(&upstreamCalls, 1)
		return phase6OKResponse(req, `{"id":"ok"}`), nil
	}))
	h.cfg.maxInMemoryBodyBytes = 16
	body := `{"model":"gpt-5-codex","input":"` + strings.Repeat("x", 128) + `"}`
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(body))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Authorization", "Bearer sk-real-provider-key")

	rr := httptest.NewRecorder()
	h.proxyRequest(rr, req, "req-phase6-passthrough")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt64(&upstreamCalls); got != 1 {
		t.Fatalf("upstream calls=%d", got)
	}
}
