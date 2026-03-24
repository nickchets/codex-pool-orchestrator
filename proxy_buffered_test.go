package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newBufferedCodexProxyHandlerForTest(t *testing.T, upstreamURL string, accounts []*Account) *proxyHandler {
	t.Helper()

	baseURL, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)

	return &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
			maxInMemoryBodyBytes: 1024,
		},
		transport: http.DefaultTransport,
		pool:      newPoolState(accounts, false),
		registry:  NewProviderRegistry(codex, claude, gemini),
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
	}
}

func newBufferedGitLabClaudeAccountForTest(t *testing.T, dir, id, sourceToken, gatewayToken, upstreamBaseURL string) *Account {
	t.Helper()

	file := filepath.Join(dir, id+".json")
	payload := fmt.Sprintf(`{
		"plan_type":"gitlab_duo",
		"auth_mode":"gitlab_duo",
		"gitlab_token":"%s",
		"gitlab_gateway_token":"%s",
		"gitlab_gateway_headers":{"X-Gitlab-Instance-Id":"inst-1"},
		"gitlab_gateway_base_url":"%s"
	}`, sourceToken, gatewayToken, upstreamBaseURL)
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write gitlab account file %s: %v", file, err)
	}

	return &Account{
		ID:              id,
		Type:            AccountTypeClaude,
		File:            file,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    sourceToken,
		AccessToken:     gatewayToken,
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: upstreamBaseURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
	}
}

func waitForBufferedProxySuccessAccountState(t *testing.T, acc *Account, reason string) proxyTestAccountSnapshot {
	t.Helper()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		snapshot := snapshotProxyTestAccount(acc)
		if !snapshot.LastUsed.IsZero() {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}

	snapshot := snapshotProxyTestAccount(acc)
	t.Fatalf("expected %s; LastUsed=%v", reason, snapshot.LastUsed)
	return proxyTestAccountSnapshot{}
}

func TestProxyBufferedManagedAPI429RetriesNextSeatAfterQuotaFallback(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	type authCall struct {
		count int
	}
	calls := map[string]*authCall{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		call := calls[auth]
		if call == nil {
			call = &authCall{}
			calls[auth] = call
		}
		call.count++

		w.Header().Set("Content-Type", "application/json")
		switch auth {
		case "Bearer sk-proj-dead":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-dead","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`))
		case "Bearer sk-proj-live":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-live","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", auth)
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadFile := filepath.Join(tmp, "openai_api_dead.json")
	if err := os.WriteFile(deadFile, []byte(`{"OPENAI_API_KEY":"sk-proj-dead","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write dead key file: %v", err)
	}
	liveFile := filepath.Join(tmp, "openai_api_live.json")
	if err := os.WriteFile(liveFile, []byte(`{"OPENAI_API_KEY":"sk-proj-live","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write live key file: %v", err)
	}

	deadAcc := &Account{
		ID:          "openai_api_dead",
		Type:        AccountTypeCodex,
		File:        deadFile,
		AccessToken: "sk-proj-dead",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}
	liveAcc := &Account{
		ID:          "openai_api_live",
		Type:        AccountTypeCodex,
		File:        liveFile,
		AccessToken: "sk-proj-live",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-4.1-mini","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-managed-api-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected first managed api key to be marked dead")
	}
	if deadState.HealthStatus != "dead" {
		t.Fatalf("dead health status = %q", deadState.HealthStatus)
	}
	liveState := snapshotProxyTestAccount(liveAcc)
	if liveState.Dead {
		t.Fatal("expected second managed api key to stay live")
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "second managed api key to be used")
	if calls["Bearer sk-proj-dead"] == nil || calls["Bearer sk-proj-dead"].count != 2 {
		t.Fatalf("dead account calls = %+v", calls["Bearer sk-proj-dead"])
	}
	if calls["Bearer sk-proj-live"] == nil || calls["Bearer sk-proj-live"].count != 2 {
		t.Fatalf("live account calls = %+v", calls["Bearer sk-proj-live"])
	}
}

func TestProxyBufferedManagedAPI402RetriesNextSeatAfterPaymentRequired(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	type authCall struct {
		count int
	}
	calls := map[string]*authCall{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		call := calls[auth]
		if call == nil {
			call = &authCall{}
			calls[auth] = call
		}
		call.count++

		w.Header().Set("Content-Type", "application/json")
		switch auth {
		case "Bearer sk-proj-dead":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-dead","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"billing hard limit","code":"billing_hard_limit_reached"}}`))
		case "Bearer sk-proj-live":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-live","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", auth)
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadFile := filepath.Join(tmp, "openai_api_dead.json")
	if err := os.WriteFile(deadFile, []byte(`{"OPENAI_API_KEY":"sk-proj-dead","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write dead key file: %v", err)
	}
	liveFile := filepath.Join(tmp, "openai_api_live.json")
	if err := os.WriteFile(liveFile, []byte(`{"OPENAI_API_KEY":"sk-proj-live","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write live key file: %v", err)
	}

	deadAcc := &Account{
		ID:          "openai_api_dead",
		Type:        AccountTypeCodex,
		File:        deadFile,
		AccessToken: "sk-proj-dead",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}
	liveAcc := &Account{
		ID:          "openai_api_live",
		Type:        AccountTypeCodex,
		File:        liveFile,
		AccessToken: "sk-proj-live",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-4.1-mini","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-managed-api-402-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected first managed api key to be marked dead after 402")
	}
	liveState := snapshotProxyTestAccount(liveAcc)
	if liveState.Dead {
		t.Fatal("expected second managed api key to stay live")
	}
	if calls["Bearer sk-proj-dead"] == nil || calls["Bearer sk-proj-dead"].count != 2 {
		t.Fatalf("dead account calls = %+v", calls["Bearer sk-proj-dead"])
	}
	if calls["Bearer sk-proj-live"] == nil || calls["Bearer sk-proj-live"].count != 2 {
		t.Fatalf("live account calls = %+v", calls["Bearer sk-proj-live"])
	}
}

func TestProxyBufferedPaymentRequiredDeactivatedWorkspaceRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var deadCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer dead-seat-token":
			deadCalls++
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"deactivated_workspace"}`))
		case "Bearer live-seat-token":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	deadAcc := &Account{
		ID:          "codex_dead",
		Type:        AccountTypeCodex,
		AccessToken: "dead-seat-token",
		AccountID:   "acct-dead",
		PlanType:    "pro",
	}
	liveAcc := &Account{
		ID:          "codex_live",
		Type:        AccountTypeCodex,
		AccessToken: "live-seat-token",
		AccountID:   "acct-live",
		PlanType:    "pro",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.4","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-402-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected deactivated workspace account to be marked dead")
	}
	liveState := snapshotProxyTestAccount(liveAcc)
	if liveState.Dead {
		t.Fatal("expected fallback account to stay live")
	}
	if deadCalls != 1 || liveCalls != 1 {
		t.Fatalf("deadCalls=%d liveCalls=%d", deadCalls, liveCalls)
	}
}

func TestProxyBufferedRetryable5xxRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var deadCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer flaky-seat-token":
			deadCalls++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"server boom"}}`))
		case "Bearer live-seat-token":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	flakyAcc := &Account{
		ID:          "codex_flaky",
		Type:        AccountTypeCodex,
		AccessToken: "flaky-seat-token",
		AccountID:   "acct-flaky",
		PlanType:    "pro",
	}
	liveAcc := &Account{
		ID:          "codex_live",
		Type:        AccountTypeCodex,
		AccessToken: "live-seat-token",
		AccountID:   "acct-live",
		PlanType:    "pro",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{flakyAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.4","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-5xx-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	flakyState := snapshotProxyTestAccount(flakyAcc)
	if flakyState.Dead {
		t.Fatal("expected 5xx account to remain non-dead")
	}
	if flakyState.Penalty == 0 {
		t.Fatal("expected 5xx account penalty to increase")
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "fallback account to be used")
	if deadCalls != 1 || liveCalls != 1 {
		t.Fatalf("flakyCalls=%d liveCalls=%d", deadCalls, liveCalls)
	}
	recent := h.recent.snapshot()
	if len(recent) == 0 || !strings.Contains(recent[0], "502 Bad Gateway") {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestProxyBufferedTransientAuthFailureRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var deniedCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer denied-seat-token":
			deniedCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer live-seat-token":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	deniedAcc := &Account{
		ID:          "codex_denied",
		Type:        AccountTypeCodex,
		AccessToken: "denied-seat-token",
		AccountID:   "acct-denied",
		PlanType:    "pro",
	}
	liveAcc := &Account{
		ID:          "codex_live",
		Type:        AccountTypeCodex,
		AccessToken: "live-seat-token",
		AccountID:   "acct-live",
		PlanType:    "pro",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deniedAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.4","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-403-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deniedState := snapshotProxyTestAccount(deniedAcc)
	if deniedState.Dead {
		t.Fatal("expected transient auth failure account to remain non-dead")
	}
	if deniedState.Penalty == 0 {
		t.Fatal("expected transient auth failure to add penalty")
	}
	if deniedCalls != 1 || liveCalls != 1 {
		t.Fatalf("deniedCalls=%d liveCalls=%d", deniedCalls, liveCalls)
	}
}

func TestProxyBufferedGitLabClaude402QuotaExceededMarksDeadAndRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var quotaCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-quota":
			quotaCalls++
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"insufficient_credits","error_code":"USAGE_QUOTA_EXCEEDED"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	quotaAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_quota", "glpat-quota", "gateway-quota", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{quotaAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-402-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	quotaState := snapshotProxyTestAccount(quotaAcc)
	if !quotaState.Dead {
		t.Fatal("expected quota account to be marked dead")
	}
	if quotaState.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", quotaState.HealthStatus)
	}
	if !quotaState.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", quotaState.RateLimitUntil)
	}
	if quotaState.GitLabQuotaExceededCount != 0 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", quotaState.GitLabQuotaExceededCount)
	}
	if quotaCalls != 1 || liveCalls != 1 {
		t.Fatalf("quotaCalls=%d liveCalls=%d", quotaCalls, liveCalls)
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "live gitlab account to be used")
}

func TestProxyBufferedGitLabClaude403GatewayRejectedRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var staleCalls, freshCalls, liveCalls, refreshCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-stale":
			staleCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer gateway-fresh":
			freshCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	rejectedAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_rejected", "glpat-rejected", "gateway-stale", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{rejectedAcc, liveAcc})
	h.refreshTransport = gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		refreshCalls++
		return gitlabClaudeJSONResponse(http.StatusOK, `{
			"token":"gateway-fresh",
			"base_url":"https://cloud.gitlab.com/ai/v1/proxy/anthropic",
			"expires_at":1911111111,
			"headers":{"X-Gitlab-Instance-Id":"inst-1"}
		}`), nil
	})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-403-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	rejectedState := snapshotProxyTestAccount(rejectedAcc)
	if rejectedState.Dead {
		t.Fatal("expected gateway-rejected account to remain live")
	}
	if rejectedState.HealthStatus != "gateway_rejected" {
		t.Fatalf("health_status=%q", rejectedState.HealthStatus)
	}
	if rejectedState.RateLimitUntil.IsZero() {
		t.Fatal("expected gateway rejection cooldown to be set")
	}
	if rejectedState.AccessToken != "gateway-fresh" {
		t.Fatalf("access_token=%q", rejectedState.AccessToken)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if staleCalls != 1 || freshCalls != 1 || liveCalls != 1 {
		t.Fatalf("staleCalls=%d freshCalls=%d liveCalls=%d", staleCalls, freshCalls, liveCalls)
	}
}

func TestProxyBufferedGitLabClaude401RefreshInvalidGrantMarksDead(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var staleCalls, liveCalls, refreshCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-stale":
			staleCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"stale gateway token"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_dead", "glpat-dead", "gateway-stale", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	h.refreshTransport = gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		refreshCalls++
		return gitlabClaudeJSONResponse(http.StatusUnauthorized, `{"error":"invalid_grant"}`), nil
	})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-401-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected invalid_grant account to end dead")
	}
	if deadState.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", deadState.HealthStatus)
	}
	if !deadState.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", deadState.RateLimitUntil)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if staleCalls != 1 || liveCalls != 1 {
		t.Fatalf("staleCalls=%d liveCalls=%d", staleCalls, liveCalls)
	}
	saved, err := os.ReadFile(deadAcc.File)
	if err != nil {
		t.Fatalf("read saved dead account: %v", err)
	}
	if !strings.Contains(string(saved), `"dead": true`) {
		t.Fatalf("expected persisted dead flag, got %s", string(saved))
	}
}

func TestProxyBufferedGitLabClaude403DirectAccessForbiddenMarksDead(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var staleCalls, liveCalls, refreshCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-stale":
			staleCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_forbidden", "glpat-dead", "gateway-stale", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	h.refreshTransport = gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		refreshCalls++
		return gitlabClaudeJSONResponse(http.StatusForbidden, `{"message":"forbidden"}`), nil
	})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-403-direct-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected direct_access forbidden account to end dead")
	}
	if deadState.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", deadState.HealthStatus)
	}
	if !deadState.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", deadState.RateLimitUntil)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if staleCalls != 1 || liveCalls != 1 {
		t.Fatalf("staleCalls=%d liveCalls=%d", staleCalls, liveCalls)
	}
}
