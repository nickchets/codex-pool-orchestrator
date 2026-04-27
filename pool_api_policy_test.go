package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func requirePositiveRetryAfter(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	value := strings.TrimSpace(rr.Header().Get("Retry-After"))
	if value == "" {
		t.Fatalf("Retry-After header missing; body=%s", rr.Body.String())
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		t.Fatalf("Retry-After header = %q, want positive integer seconds", value)
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
	requirePositiveRetryAfter(t, second)
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
	requirePositiveRetryAfter(t, second)
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
	requirePositiveRetryAfter(t, rr)
	if got := atomic.LoadInt64(&upstreamCalls); got != 0 {
		t.Fatalf("upstream calls=%d", got)
	}
}

func TestPhase6VirtualKeyAllowedAccountTypesRejectsModelRoutedProviderBeforeUpstream(t *testing.T) {
	var upstreamCalls int64
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{AllowedAccountTypes: []AccountType{AccountTypeCodex}}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt64(&upstreamCalls, 1)
		return phase6OKResponse(req, `{"id":"should-not-happen"}`), nil
	}), &Account{
		ID:          "gemini-seat",
		Type:        AccountTypeGemini,
		AccessToken: "gemini-access",
		Usage: UsageSnapshot{
			RetrievedAt: time.Now().UTC(),
		},
	})
	body := `{"model":"gemini-3.1-pro","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	h.proxyRequest(rr, req, "req-phase6-account-type")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "account_type_not_allowed") {
		t.Fatalf("body missing account_type_not_allowed: %s", rr.Body.String())
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

func TestPhase6VirtualKeyConcurrentBudgetReservationsRejectSecondBeforeUpstream(t *testing.T) {
	body := `{"model":"gpt-5-codex","input":"hi","max_output_tokens":1}`
	estimated := estimateOpenAICompatiblePoolAPITokens("/v1/responses", []byte(body), defaultPoolAPIMaxOutputReservation)
	if estimated <= 0 {
		t.Fatalf("estimated tokens=%d", estimated)
	}
	budget := float64(estimated*2 - 1)
	cases := []struct {
		name   string
		policy PoolAPITokenPolicy
	}{
		{name: "daily", policy: PoolAPITokenPolicy{DailyBudget: budget}},
		{name: "monthly", policy: PoolAPITokenPolicy{MonthlyBudget: budget}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls int64
			entered := make(chan struct{})
			release := make(chan struct{})
			h, rawKey := newPhase4VirtualKeyHandler(t, tc.policy, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				call := atomic.AddInt64(&upstreamCalls, 1)
				if call == 1 {
					close(entered)
					<-release
				}
				return phase6OKResponse(req, `{"id":"ok","usage":{"input_tokens":0,"output_tokens":0}}`), nil
			}))
			usage, err := newUsageStore(t.TempDir()+"/usage.db", 30)
			if err != nil {
				t.Fatalf("new usage store: %v", err)
			}
			t.Cleanup(func() { _ = usage.Close() })
			h.store = usage

			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rr := httptest.NewRecorder()
				h.proxyRequest(rr, newPhase6PolicyRequest(rawKey, body), "req-phase6-budget-race-1")
				firstDone <- rr
			}()

			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for first request to reach upstream")
			}

			second := httptest.NewRecorder()
			h.proxyRequest(second, newPhase6PolicyRequest(rawKey, body), "req-phase6-budget-race-2")
			if second.Code != http.StatusForbidden {
				t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
			}
			if got := atomic.LoadInt64(&upstreamCalls); got != 1 {
				t.Fatalf("upstream calls while budget reservation held=%d", got)
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
			h.proxyRequest(third, newPhase6PolicyRequest(rawKey, body), "req-phase6-budget-race-3")
			if third.Code != http.StatusOK {
				t.Fatalf("third status=%d body=%s", third.Code, third.Body.String())
			}
			if got := atomic.LoadInt64(&upstreamCalls); got != 2 {
				t.Fatalf("upstream calls after budget release=%d", got)
			}
		})
	}
}

func TestPhase6PolicyManagerPrunesStaleStates(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	m := newPoolAPIPolicyManager()
	m.now = func() time.Time { return now }

	res, err := m.Reserve("tok-empty", PoolAPITokenPolicy{}, 10, nil)
	if err != nil {
		t.Fatalf("reserve without limits: %v", err)
	}
	if res != nil {
		t.Fatalf("reservation without limits = %#v, want nil", res)
	}
	m.mu.Lock()
	if got := len(m.states); got != 0 {
		m.mu.Unlock()
		t.Fatalf("states after no-limit reservation=%d", got)
	}
	m.mu.Unlock()

	res, err = m.Reserve("tok-budget", PoolAPITokenPolicy{DailyBudget: 100}, 10, func(time.Time) (poolAPIBudgetUsage, *poolAPIPolicyError) {
		return poolAPIBudgetUsage{}, nil
	})
	if err != nil {
		t.Fatalf("reserve budget: %v", err)
	}
	if res == nil {
		t.Fatal("budget reservation is nil")
	}
	res.Release()
	m.mu.Lock()
	if _, ok := m.states["tok-budget"]; ok {
		m.mu.Unlock()
		t.Fatal("budget-only state was not removed after release")
	}
	m.mu.Unlock()

	res, err = m.Reserve("tok-window", PoolAPITokenPolicy{MaxConcurrency: 1, MaxRPM: 1, MaxTPM: 100}, 10, nil)
	if err != nil {
		t.Fatalf("reserve windowed: %v", err)
	}
	now = now.Add(poolAPIPolicyWindow + time.Second)
	res.Release()
	m.mu.Lock()
	if _, ok := m.states["tok-window"]; ok {
		m.mu.Unlock()
		t.Fatal("expired window state was not removed after release")
	}
	m.mu.Unlock()
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
