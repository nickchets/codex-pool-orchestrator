package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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

func TestBuildWhamUsageURLKeepsBackendAPI(t *testing.T) {
	base, _ := url.Parse("https://chatgpt.com/backend-api")
	got := buildWhamUsageURL(base)
	expected := "https://chatgpt.com/backend-api/wham/usage"
	if got != expected {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestCodexProviderUpstreamURLBackendAPIPathUsesWhamBase(t *testing.T) {
	responsesBase, _ := url.Parse("https://chatgpt.com/backend-api/codex")
	whamBase, _ := url.Parse("https://chatgpt.com/backend-api")
	provider := NewCodexProvider(responsesBase, whamBase, nil, responsesBase)

	got := provider.UpstreamURL("/backend-api/codex/models")
	if got.String() != whamBase.String() {
		t.Fatalf("expected wham base %s, got %s", whamBase, got)
	}
}

func TestCodexProviderNormalizePathBackendAPIPathStripsPrefix(t *testing.T) {
	provider := &CodexProvider{}

	normalized := provider.NormalizePath("/backend-api/codex/models")
	got := singleJoin("/backend-api", normalized)
	expected := "/backend-api/codex/models"
	if got != expected {
		t.Fatalf("expected %s, got %s (normalized=%s)", expected, got, normalized)
	}
}

func TestCodexProviderParseUsageHeaders(t *testing.T) {
	acc := &Account{Type: AccountTypeCodex}
	provider := &CodexProvider{}
	provider.ParseUsageHeaders(acc, mapToHeader(map[string]string{
		"X-Codex-Primary-Used-Percent":   "25",
		"X-Codex-Secondary-Used-Percent": "50",
		"X-Codex-Primary-Window-Minutes": "300",
	}))

	if acc.Usage.PrimaryUsedPercent != 0.25 {
		t.Fatalf("primary percent = %v", acc.Usage.PrimaryUsedPercent)
	}
	if acc.Usage.SecondaryUsedPercent != 0.50 {
		t.Fatalf("secondary percent = %v", acc.Usage.SecondaryUsedPercent)
	}
	if acc.Usage.PrimaryWindowMinutes != 300 {
		t.Fatalf("primary window = %d", acc.Usage.PrimaryWindowMinutes)
	}
}

func TestParseRequestUsageFromSSE(t *testing.T) {
	line := []byte(`{"type":"response.completed","prompt_cache_key":"pc","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10,"billable_tokens":70}}`)
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ru := parseRequestUsage(obj)
	if ru == nil {
		t.Fatalf("expected usage parsed")
	}
	if ru.InputTokens != 100 || ru.CachedInputTokens != 40 || ru.OutputTokens != 10 || ru.BillableTokens != 70 {
		t.Fatalf("unexpected values: %+v", ru)
	}
	if ru.PromptCacheKey != "pc" {
		t.Fatalf("prompt_cache_key=%s", ru.PromptCacheKey)
	}
}

func TestExtractRequestedModelFromJSON(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex-spark","input":"hi"}`)
	got := extractRequestedModelFromJSON(body)
	if got != "gpt-5.3-codex-spark" {
		t.Fatalf("model=%q", got)
	}
	if !modelRequiresCodexPro(got) {
		t.Fatalf("expected model to require codex pro")
	}
}

func TestClaudeProviderParseUsageHeadersIgnored(t *testing.T) {
	acc := &Account{Type: AccountTypeClaude}
	provider := &ClaudeProvider{}
	retrievedAt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	primaryReset := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	secondaryReset := time.Now().UTC().Add(3 * 24 * time.Hour).Truncate(time.Second)
	acc.Usage = UsageSnapshot{
		PrimaryUsedPercent:   0.25,
		SecondaryUsedPercent: 0.33,
		PrimaryUsed:          0.25,
		SecondaryUsed:        0.33,
		PrimaryResetAt:       primaryReset,
		SecondaryResetAt:     secondaryReset,
		RetrievedAt:          retrievedAt,
		Source:               "claude-api",
	}

	provider.ParseUsageHeaders(acc, mapToHeader(map[string]string{
		"anthropic-ratelimit-unified-tokens-utilization":   "99.9",
		"anthropic-ratelimit-unified-requests-utilization": "88.8",
		"anthropic-ratelimit-unified-tokens-reset":         "9999999999",
		"anthropic-ratelimit-unified-requests-reset":       "9999999999",
	}))

	if acc.Usage.PrimaryUsedPercent != 0.25 {
		t.Fatalf("primary percent = %v", acc.Usage.PrimaryUsedPercent)
	}
	if acc.Usage.SecondaryUsedPercent != 0.33 {
		t.Fatalf("secondary percent = %v", acc.Usage.SecondaryUsedPercent)
	}
	if acc.Usage.PrimaryResetAt.UTC().Unix() != primaryReset.Unix() {
		t.Fatalf("primary reset = %v want %v", acc.Usage.PrimaryResetAt.UTC(), primaryReset)
	}
	if acc.Usage.SecondaryResetAt.UTC().Unix() != secondaryReset.Unix() {
		t.Fatalf("secondary reset = %v want %v", acc.Usage.SecondaryResetAt.UTC(), secondaryReset)
	}
	if acc.Usage.RetrievedAt.UTC().Unix() != retrievedAt.Unix() {
		t.Fatalf("retrieved_at = %v want %v", acc.Usage.RetrievedAt.UTC(), retrievedAt)
	}
}

func TestMergeUsageClaudeAPIAllowsPerWindowResetToZero(t *testing.T) {
	prev := UsageSnapshot{
		PrimaryUsedPercent:   0.5,
		SecondaryUsedPercent: 0.25,
		PrimaryUsed:          0.5,
		SecondaryUsed:        0.25,
		RetrievedAt:          time.Now().UTC().Add(-10 * time.Minute),
		Source:               "claude-api",
	}
	next := UsageSnapshot{
		PrimaryUsedPercent:   0,
		SecondaryUsedPercent: 0.25,
		PrimaryUsed:          0,
		SecondaryUsed:        0.25,
		RetrievedAt:          time.Now().UTC(),
		Source:               "claude-api",
	}

	got := mergeUsage(prev, next)
	if got.PrimaryUsedPercent != 0 {
		t.Fatalf("primary percent = %v", got.PrimaryUsedPercent)
	}
	if got.PrimaryUsed != 0 {
		t.Fatalf("primary used = %v", got.PrimaryUsed)
	}
	if got.SecondaryUsedPercent != 0.25 {
		t.Fatalf("secondary percent = %v", got.SecondaryUsedPercent)
	}
}

func TestParseClaudeResetAt(t *testing.T) {
	resetAt := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)

	if _, ok := parseClaudeResetAt(nil); ok {
		t.Fatalf("expected nil reset value to be ignored")
	}
	if _, ok := parseClaudeResetAt(""); ok {
		t.Fatalf("expected empty reset value to be ignored")
	}

	fromString, ok := parseClaudeResetAt(resetAt.Format(time.RFC3339))
	if !ok {
		t.Fatalf("expected RFC3339 reset to parse")
	}
	if fromString.UTC().Unix() != resetAt.Unix() {
		t.Fatalf("string reset = %v want %v", fromString.UTC(), resetAt)
	}

	fromUnix, ok := parseClaudeResetAt(float64(resetAt.Unix()))
	if !ok {
		t.Fatalf("expected unix reset to parse")
	}
	if fromUnix.UTC().Unix() != resetAt.Unix() {
		t.Fatalf("unix reset = %v want %v", fromUnix.UTC(), resetAt)
	}
}

func TestCodexProviderLoadsManagedOpenAIAPIKeyAccount(t *testing.T) {
	apiBase, _ := url.Parse("https://api.openai.com")
	provider := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)

	payload := []byte(`{
	  "OPENAI_API_KEY": "sk-proj-test",
	  "auth_mode": "api_key",
	  "plan_type": "api",
	  "health_status": "healthy",
	  "health_error": "",
	  "dead": false
	}`)

	acc, err := provider.LoadAccount("openai_api_deadbeef.json", "/tmp/openai_api_deadbeef.json", payload)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account")
	}
	if acc.AuthMode != accountAuthModeAPIKey {
		t.Fatalf("auth_mode=%q", acc.AuthMode)
	}
	if acc.PlanType != "api" {
		t.Fatalf("plan_type=%q", acc.PlanType)
	}
	if acc.AccessToken != "sk-proj-test" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
}

func TestSaveCodexAccountPersistsOAuthHealthState(t *testing.T) {
	apiBase, _ := url.Parse("https://chatgpt.com")
	provider := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)

	accFile := filepath.Join(t.TempDir(), "codex_oauth.json")
	if err := os.WriteFile(accFile, []byte(`{"tokens":{"access_token":"seed-access","refresh_token":"seed-refresh"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	healthCheckedAt := time.Date(2026, 3, 28, 14, 0, 0, 0, time.UTC)
	lastHealthyAt := healthCheckedAt.Add(-30 * time.Minute)
	acc := &Account{
		ID:              "seat-oauth",
		Type:            AccountTypeCodex,
		File:            accFile,
		AccessToken:     "next-access",
		RefreshToken:    "next-refresh",
		HealthStatus:    codexRefreshInvalidHealthStatus,
		HealthError:     codexRefreshInvalidHealthError,
		HealthCheckedAt: healthCheckedAt,
		LastHealthyAt:   lastHealthyAt,
	}

	if err := saveCodexAccount(acc); err != nil {
		t.Fatalf("save codex account: %v", err)
	}

	raw, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	loaded, err := provider.LoadAccount(filepath.Base(accFile), accFile, raw)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected account")
	}
	if loaded.HealthStatus != codexRefreshInvalidHealthStatus {
		t.Fatalf("health_status=%q", loaded.HealthStatus)
	}
	if loaded.HealthError != codexRefreshInvalidHealthError {
		t.Fatalf("health_error=%q", loaded.HealthError)
	}
	if !loaded.HealthCheckedAt.Equal(healthCheckedAt) {
		t.Fatalf("health_checked_at=%v", loaded.HealthCheckedAt)
	}
	if !loaded.LastHealthyAt.Equal(lastHealthyAt) {
		t.Fatalf("last_healthy_at=%v", loaded.LastHealthyAt)
	}
	if loaded.Dead {
		t.Fatal("expected seat to stay non-dead")
	}
	if !loaded.DeadSince.IsZero() {
		t.Fatalf("dead_since=%v", loaded.DeadSince)
	}
}

func TestCodexProviderLoadsLegacyDeadOAuthAccountAsDeadHealth(t *testing.T) {
	apiBase, _ := url.Parse("https://chatgpt.com")
	provider := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)

	payload := []byte(`{
	  "tokens": {
	    "access_token": "seed-access",
	    "refresh_token": "seed-refresh"
	  },
	  "dead": true
	}`)

	acc, err := provider.LoadAccount("legacy-dead.json", "/tmp/legacy-dead.json", payload)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account")
	}
	if !acc.Dead {
		t.Fatal("expected dead flag")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
}

func TestCodexProviderRefreshTokenLogsTrace(t *testing.T) {
	refreshBase, _ := url.Parse("https://auth.openai.com")
	provider := NewCodexProvider(refreshBase, refreshBase, refreshBase, refreshBase)
	accFile := filepath.Join(t.TempDir(), "codex_refresh_trace.json")
	if err := os.WriteFile(accFile, []byte(`{"tokens":{"access_token":"seed-access","refresh_token":"seed-refresh"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	acc := &Account{
		ID:           "codex_refresh_trace",
		Type:         AccountTypeCodex,
		File:         accFile,
		AuthMode:     accountAuthModeOAuth,
		AccessToken:  "seed-access",
		RefreshToken: "seed-refresh",
	}

	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","refresh_token":"fresh-refresh"}`), nil
	})

	logs := captureLogs(t, func() {
		if err := provider.RefreshToken(testTraceContext("req-codex-refresh"), acc, transport); err != nil {
			t.Fatalf("RefreshToken error: %v", err)
		}
	})

	if !strings.Contains(logs, "[req-codex-refresh] trace token_refresh") {
		t.Fatalf("missing token_refresh log: %s", logs)
	}
	if !strings.Contains(logs, `provider=codex`) || !strings.Contains(logs, `result=ok`) {
		t.Fatalf("unexpected codex trace logs: %s", logs)
	}
}

func TestTryOnceManagedOpenAIAPIKeyUsesAPIBase(t *testing.T) {
	var seenPaths []string
	var seenAuth []string
	var seenBodies []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPaths = append(seenPaths, r.URL.Path)
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		seenBodies = append(seenBodies, string(body))
		if r.Header.Get("ChatGPT-Account-ID") != "" {
			t.Fatalf("expected no ChatGPT-Account-ID for managed api key")
		}
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_123","status":"completed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	apiBase, _ := url.Parse(apiServer.URL)
	provider := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)
	registry := NewProviderRegistry(provider, claude, gemini)

	tmp := t.TempDir()
	keyPath := filepath.Join(tmp, "openai_api", "openai_api_deadbeef.json")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(`{"OPENAI_API_KEY":"sk-proj-test","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	acc := &Account{
		ID:          "openai_api_deadbeef",
		Type:        AccountTypeCodex,
		File:        keyPath,
		AccessToken: "sk-proj-test",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}
	h := &proxyHandler{
		cfg:       config{},
		transport: http.DefaultTransport,
		registry:  registry,
	}

	body := []byte(`{"model":"gpt-4.1-mini","input":"hi"}`)
	req, err := http.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, _, _, err := h.tryOnce(context.Background(), req, body, RoutePlan{
		Provider:     provider,
		TargetBase:   apiBase,
		UpstreamPath: "/v1/responses",
	}, acc, "req-test")
	if err != nil {
		t.Fatalf("tryOnce: %v", err)
	}
	defer resp.Body.Close()

	if len(seenPaths) < 2 {
		t.Fatalf("expected probe and request, saw %v", seenPaths)
	}
	if seenPaths[0] != "/v1/responses" || seenPaths[1] != "/v1/responses" {
		t.Fatalf("unexpected paths: %v", seenPaths)
	}
	if !strings.Contains(seenBodies[0], `"model":"gpt-5.4"`) {
		t.Fatalf("expected responses probe body, got %q", seenBodies[0])
	}
	if strings.TrimSpace(seenBodies[1]) != string(body) {
		t.Fatalf("unexpected forwarded request body: %q", seenBodies[1])
	}
	for _, auth := range seenAuth {
		if auth != "Bearer sk-proj-test" {
			t.Fatalf("unexpected auth header %q", auth)
		}
	}
}

func TestTryOnceGeminiFacadeForcesRefreshAfter401DespiteRecentLastRefresh(t *testing.T) {
	t.Setenv(geminiOAuthAntigravitySecretVar, "antigravity-secret")

	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	codex := NewCodexProvider(geminiBase, geminiBase, geminiBase, geminiBase)
	claude := NewClaudeProvider(geminiBase)
	gemini := NewGeminiProvider(geminiBase, geminiAPIBase)

	accFile := filepath.Join(t.TempDir(), "gemini_antigravity.json")
	expiredAt := time.Now().Add(-time.Hour).UTC().UnixMilli()
	lastRefresh := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)
	raw := fmt.Sprintf(`{
		"access_token":"stale-access",
		"refresh_token":"seed-refresh",
		"oauth_profile_id":"%s",
		"operator_source":"%s",
		"antigravity_source":"browser_oauth",
		"antigravity_project_id":"project-1",
		"expiry_date":%d,
		"last_refresh":"%s"
	}`, geminiOAuthAntigravityProfileID, geminiOperatorSourceAntigravityImport, expiredAt, lastRefresh)
	if err := os.WriteFile(accFile, []byte(raw), 0o600); err != nil {
		t.Fatalf("write account: %v", err)
	}

	acc, err := gemini.LoadAccount(filepath.Base(accFile), accFile, []byte(raw))
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if (&proxyHandler{}).needsRefresh(acc) {
		t.Fatal("expected recent LastRefresh to suppress proactive refresh")
	}

	var refreshCalls int
	var upstreamCalls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.String() == geminiOAuthTokenURL:
			refreshCalls++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read refresh body: %v", err)
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatalf("parse refresh body: %v", err)
			}
			if values.Get("client_id") != geminiOAuthAntigravityClientID {
				t.Fatalf("client_id=%q", values.Get("client_id"))
			}
			if values.Get("client_secret") != "antigravity-secret" {
				t.Fatalf("client_secret=%q", values.Get("client_secret"))
			}
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		case r.URL.Host == geminiBase.Host && r.URL.Path == "/v1internal:generateContent":
			upstreamCalls++
			switch upstreamCalls {
			case 1:
				if got := r.Header.Get("Authorization"); got != "Bearer stale-access" {
					t.Fatalf("first auth=%q", got)
				}
				return jsonResponse(http.StatusUnauthorized, `{"error":"invalid_token"}`), nil
			case 2:
				if got := r.Header.Get("Authorization"); got != "Bearer fresh-access" {
					t.Fatalf("second auth=%q", got)
				}
				return jsonResponse(http.StatusOK, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"AG_POOL_OK."}]}}]}}`), nil
			default:
				t.Fatalf("unexpected upstream call #%d", upstreamCalls)
			}
		default:
			t.Fatalf("unexpected request to %s", r.URL.String())
		}
		return nil, nil
	})

	h := &proxyHandler{
		cfg:              config{},
		transport:        transport,
		refreshTransport: transport,
		registry:         NewProviderRegistry(codex, claude, gemini),
	}

	body := []byte(`{"contents":[{"parts":[{"text":"Reply with exactly AG_POOL_OK."}]}]}`)
	req, err := http.NewRequest(http.MethodPost, "http://pool.local/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, _, _, err := h.tryOnce(context.Background(), req, body, RoutePlan{
		Provider:     gemini,
		TargetBase:   geminiAPIBase,
		UpstreamPath: "/v1beta/models/gemini-2.5-flash:generateContent",
		Shape:        RequestShape{RequestedModel: "gemini-2.5-flash"},
	}, acc, "req-gemini")
	if err != nil {
		t.Fatalf("tryOnce: %v", err)
	}
	defer resp.Body.Close()

	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if upstreamCalls != 2 {
		t.Fatalf("upstreamCalls=%d", upstreamCalls)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(gotBody), "AG_POOL_OK.") {
		t.Fatalf("body=%q", string(gotBody))
	}
	if acc.AccessToken != "fresh-access" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
}

func TestTryOnceOpenAIChatCompletionsGeminiUnwrapsCodeAssistEnvelope(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	codexBase, _ := url.Parse("https://chatgpt.com/backend-api/codex")
	whamBase, _ := url.Parse("https://chatgpt.com/backend-api")
	refreshBase, _ := url.Parse("https://auth.openai.com")

	codex := NewCodexProvider(codexBase, whamBase, refreshBase, codexBase)
	claude := NewClaudeProvider(codexBase)
	gemini := NewGeminiProvider(geminiBase, geminiAPIBase)

	acc := &Account{
		ID:                   "gemini-antigravity-seat",
		Type:                 AccountTypeGemini,
		AccessToken:          "gemini-access",
		RefreshToken:         "gemini-refresh",
		OAuthProfileID:       geminiOAuthAntigravityProfileID,
		AntigravitySource:    "browser_oauth",
		AntigravityProjectID: "project-1",
	}

	upstreamCalls := 0
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		upstreamCalls++
		if r.URL.Host != geminiBase.Host {
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		if r.URL.Path != "/v1internal:generateContent" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != antigravityCodeAssistUA {
			t.Fatalf("user-agent = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"response":{"candidates":[{"content":{"parts":[{"text":"AG_POOL_OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}},"traceId":"trace-opencode-1"}`,
			)),
		}, nil
	})

	h := &proxyHandler{
		cfg:              config{},
		transport:        transport,
		refreshTransport: transport,
		registry:         NewProviderRegistry(codex, claude, gemini),
	}

	body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"system","content":"sys"},{"role":"user","content":"Reply with exactly AG_POOL_OK."}]}`)
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/chat/completions", strings.NewReader(string(body)))
	shape := buildBufferedRequestShape(req, body, body)

	routePlan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("planRoute: %v", err)
	}

	resp, _, _, err := h.tryOnce(context.Background(), req, rewrittenBody, routePlan, acc, "req-opencode")
	if err != nil {
		t.Fatalf("tryOnce: %v", err)
	}
	defer resp.Body.Close()

	if upstreamCalls != 1 {
		t.Fatalf("upstreamCalls=%d", upstreamCalls)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"object":"chat.completion"`, `"model":"gemini-2.5-flash"`, `"content":"AG_POOL_OK"`, `"finish_reason":"stop"`, `"total_tokens":7`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %q: %s", want, text)
		}
	}
}

func TestTryOnceGeminiCodeAssistFallsBackAcrossAntigravityHosts(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	codexBase, _ := url.Parse("https://chatgpt.com/backend-api/codex")
	whamBase, _ := url.Parse("https://chatgpt.com/backend-api")
	refreshBase, _ := url.Parse("https://auth.openai.com")

	codex := NewCodexProvider(codexBase, whamBase, refreshBase, codexBase)
	claude := NewClaudeProvider(codexBase)
	gemini := NewGeminiProvider(geminiBase, geminiAPIBase)

	acc := &Account{
		ID:                   "gemini-antigravity-seat",
		Type:                 AccountTypeGemini,
		AccessToken:          "gemini-access",
		RefreshToken:         "gemini-refresh",
		OAuthProfileID:       geminiOAuthAntigravityProfileID,
		AntigravitySource:    "browser_oauth",
		AntigravityProjectID: "project-1",
	}

	var seenHosts []string
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seenHosts = append(seenHosts, r.URL.Host)
		if r.URL.Path != "/v1internal:generateContent" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		switch r.URL.Host {
		case "daily-cloudcode-pa.sandbox.googleapis.com", "daily-cloudcode-pa.googleapis.com":
			return jsonResponse(http.StatusInternalServerError, `{"error":{"code":500,"message":"Unknown Error.","status":"UNKNOWN"}}`), nil
		case "cloudcode-pa.googleapis.com":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"response":{"candidates":[{"content":{"parts":[{"text":"AG_HOST_FALLBACK_OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}},"traceId":"trace-host-fallback-1"}`,
				)),
			}, nil
		default:
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		return nil, nil
	})

	h := &proxyHandler{
		cfg:              config{},
		transport:        transport,
		refreshTransport: transport,
		registry:         NewProviderRegistry(codex, claude, gemini),
	}

	body := []byte(`{"model":"gemini-3.1-pro","messages":[{"role":"user","content":"Reply with exactly AG_HOST_FALLBACK_OK."}]}`)
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/chat/completions", strings.NewReader(string(body)))
	shape := buildBufferedRequestShape(req, body, body)

	routePlan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("planRoute: %v", err)
	}

	resp, _, _, err := h.tryOnce(context.Background(), req, rewrittenBody, routePlan, acc, "req-gemini-host-fallback")
	if err != nil {
		t.Fatalf("tryOnce: %v", err)
	}
	defer resp.Body.Close()

	wantHosts := []string{
		"daily-cloudcode-pa.sandbox.googleapis.com",
		"daily-cloudcode-pa.googleapis.com",
		"cloudcode-pa.googleapis.com",
	}
	if strings.Join(seenHosts, ",") != strings.Join(wantHosts, ",") {
		t.Fatalf("seenHosts = %v, want %v", seenHosts, wantHosts)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"content":"AG_HOST_FALLBACK_OK"`, `"total_tokens":7`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %q: %s", want, text)
		}
	}
}

// mapToHeader is a tiny helper to build http.Header in tests without importing net/http everywhere.
func mapToHeader(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

func TestFinalizeProxyResponsePinsInitialConversationAndDecaysPenalty(t *testing.T) {
	acc := &Account{ID: "acc-1", Type: AccountTypeCodex, Penalty: 0.4}
	pool := newPoolState([]*Account{acc}, false)
	h := &proxyHandler{pool: pool}

	h.finalizeProxyResponse(
		"req-test",
		nil,
		acc,
		"user-1",
		http.StatusOK,
		true,
		false,
		"conv-initial",
		0,
		0,
		[]byte("data: {\"conversation_id\":\"conv-from-response\"}\n"),
	)

	if got := pool.convPin["conv-initial"]; got != "acc-1" {
		t.Fatalf("initial conversation pin = %q", got)
	}
	if _, ok := pool.convPin["conv-from-response"]; ok {
		t.Fatalf("expected response conversation to stay ignored when initial conversation is already known")
	}
	if acc.LastUsed.IsZero() {
		t.Fatal("expected LastUsed to be updated")
	}
	if acc.Penalty != 0.2 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
}

func TestCandidateSupportingPathAllowsAntigravityFallbackProjectForGeminiSeats(t *testing.T) {
	unsupported := &Account{
		ID:       "gemini_seat_manual",
		Type:     AccountTypeGemini,
		PlanType: "gemini",
		LastUsed: time.Now().UTC(),
	}
	supported := &Account{
		ID:                "gemini_seat_antigravity",
		Type:              AccountTypeGemini,
		PlanType:          "gemini",
		OAuthProfileID:    geminiOAuthAntigravityProfileID,
		AntigravitySource: "browser_oauth",
	}
	pool := newPoolState([]*Account{unsupported, supported}, false)
	h := &proxyHandler{pool: pool}
	provider := &GeminiProvider{}
	path := "/v1beta/models/gemini-2.5-flash:streamGenerateContent"

	if got := pool.candidate("", map[string]bool{}, AccountTypeGemini, ""); got == nil || got.ID != unsupported.ID {
		t.Fatalf("baseline candidate = %+v, want unsupported seat first", got)
	}

	got, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeGemini, "", provider, path, "gemini-2.5-flash", "")
	if err != nil {
		t.Fatalf("candidate supporting path: %v", err)
	}
	if got == nil {
		t.Fatal("expected supporting candidate")
	}
	if got.ID != supported.ID {
		t.Fatalf("candidate = %s, want %s", got.ID, supported.ID)
	}
}

func TestCandidateSupportingPathSkipsGeminiSeatWhenRequestedModelCoolingDown(t *testing.T) {
	now := time.Now().UTC()
	coolingUntil := now.Add(2 * time.Minute)
	cooling := &Account{
		ID:                       "gemini_seat_cooling",
		Type:                     AccountTypeGemini,
		PlanType:                 "gemini",
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		AntigravitySource:        "browser_oauth",
		AntigravityProjectID:     "project-1",
		GeminiProviderCheckedAt:  now,
		GeminiProviderTruthReady: true,
		GeminiProviderTruthState: geminiProviderTruthStateReady,
		GeminiModelRateLimitResetTimes: map[string]time.Time{
			"gemini-3.1-pro-high": coolingUntil,
		},
	}
	healthy := &Account{
		ID:                       "gemini_seat_healthy",
		Type:                     AccountTypeGemini,
		PlanType:                 "gemini",
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		AntigravitySource:        "browser_oauth",
		AntigravityProjectID:     "project-2",
		GeminiProviderCheckedAt:  now,
		GeminiProviderTruthReady: true,
		GeminiProviderTruthState: geminiProviderTruthStateReady,
	}
	h := &proxyHandler{pool: newPoolState([]*Account{cooling, healthy}, false)}
	provider := &GeminiProvider{}

	got, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeGemini, "", provider, "/v1beta/models/gemini-3.1-pro-high:generateContent", "gemini-3.1-pro", "")
	if err != nil {
		t.Fatalf("candidate supporting path: %v", err)
	}
	if got == nil || got.ID != healthy.ID {
		t.Fatalf("candidate = %+v, want %s", got, healthy.ID)
	}

	got, err = h.candidateSupportingPath("", map[string]bool{}, AccountTypeGemini, "", provider, "/v1beta/models/gemini-2.5-flash:generateContent", "gemini-2.5-flash", "")
	if err != nil {
		t.Fatalf("flash candidate supporting path: %v", err)
	}
	if got == nil || got.ID != cooling.ID {
		t.Fatalf("flash candidate = %+v, want %s", got, cooling.ID)
	}
}

func TestCandidateSupportingPathAllowsForcedDebugGeminiSeatForV1Internal(t *testing.T) {
	blocked := &Account{
		ID:                           "gemini_seat_blocked",
		Type:                         AccountTypeGemini,
		PlanType:                     "gemini",
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
	}
	healthy := &Account{
		ID:                "gemini_seat_healthy",
		Type:              AccountTypeGemini,
		PlanType:          "gemini",
		OAuthProfileID:    geminiOAuthAntigravityProfileID,
		AntigravitySource: "browser_oauth",
	}
	pool := newPoolState([]*Account{healthy, blocked}, false)
	h := &proxyHandler{pool: pool}
	provider := &GeminiProvider{}

	got, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeGemini, "", provider, "/v1internal:generateContent", "", blocked.ID)
	if err != nil {
		t.Fatalf("candidate supporting path: %v", err)
	}
	if got == nil || got.ID != blocked.ID {
		t.Fatalf("candidate = %+v, want %s", got, blocked.ID)
	}
}

func TestCandidateSupportingPathAllowsForcedDebugGeminiSeatForAllowlistedBlockedV1BetaPath(t *testing.T) {
	blocked := &Account{
		ID:                           "gemini_seat_blocked",
		Type:                         AccountTypeGemini,
		PlanType:                     "gemini",
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "INELIGIBLE_ACCOUNT",
	}
	pool := newPoolState([]*Account{blocked}, false)
	h := &proxyHandler{pool: pool}
	provider := &GeminiProvider{}

	got, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeGemini, "", provider, "/v1beta/models/gemini-2.5-flash:generateContent", "gemini-2.5-flash", blocked.ID)
	if err != nil {
		t.Fatalf("candidate supporting path: %v", err)
	}
	if got == nil || got.ID != blocked.ID {
		t.Fatalf("candidate = %+v, want %s", got, blocked.ID)
	}
}

func TestCandidateSupportingPathReturnsGitLabSharedTPMRateLimitError(t *testing.T) {
	now := time.Now().UTC()
	until := now.Add(45 * time.Second)
	gitlabOne := &Account{
		ID:              "claude_gitlab_one",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-1",
		RefreshToken:    "glpat-1",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		RateLimitUntil:  until,
		HealthStatus:    "rate_limited",
		HealthError:     managedGitLabClaudeSharedOrgTPMHealthError("This request would exceed your organization's rate limit of 18,000,000 input tokens per minute"),
	}
	gitlabTwo := &Account{
		ID:              "claude_gitlab_two",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-2",
		RefreshToken:    "glpat-2",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		RateLimitUntil:  until,
		HealthStatus:    "rate_limited",
		HealthError:     managedGitLabClaudeSharedOrgTPMHealthError("This request would exceed your organization's rate limit of 18,000,000 input tokens per minute"),
	}
	baseURL, _ := url.Parse(defaultGitLabClaudeGatewayURL)
	h := &proxyHandler{pool: newPoolState([]*Account{gitlabOne, gitlabTwo}, false)}
	provider := NewClaudeProvider(baseURL)

	got, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeClaude, "gitlab_duo", provider, "/v1/messages", "claude-opus-4-6", "")
	if got != nil {
		t.Fatalf("candidate = %+v, want nil", got)
	}
	rateLimitErr, ok := asRateLimitResponseError(err)
	if !ok {
		t.Fatalf("expected rate-limit error, got %v", err)
	}
	if !strings.Contains(rateLimitErr.Error(), "organization's rate limit") {
		t.Fatalf("error=%q", rateLimitErr.Error())
	}
	if rateLimitErr.retryAfter <= 0 {
		t.Fatalf("retry_after=%v", rateLimitErr.retryAfter)
	}
}

func TestGitLabClaudeSharedTPMCooldownErrorIgnoresLaneWhenLiveSeatStillExists(t *testing.T) {
	now := time.Now().UTC()
	baseURL, _ := url.Parse(defaultGitLabClaudeGatewayURL)
	provider := NewClaudeProvider(baseURL)

	cooled := &Account{
		ID:              "claude_gitlab_cooling",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-1",
		RefreshToken:    "glpat-1",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		RateLimitUntil:  now.Add(45 * time.Second),
		HealthStatus:    "rate_limited",
		HealthError:     managedGitLabClaudeSharedOrgTPMHealthError("This request would exceed your organization's rate limit of 18,000,000 input tokens per minute"),
	}
	live := &Account{
		ID:              "claude_gitlab_live",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-2",
		RefreshToken:    "glpat-2",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		HealthStatus:    "healthy",
	}
	h := &proxyHandler{pool: newPoolState([]*Account{cooled, live}, false)}

	if err := h.gitLabClaudeSharedTPMCooldownError(now, AccountTypeClaude, "gitlab_duo", provider, "/v1/messages"); err != nil {
		t.Fatalf("expected no shared cooldown error while a live seat remains, got %v", err)
	}
}

func TestCandidateSupportingPathStillUsesDirectClaudeWhenGitLabSharedTPMActive(t *testing.T) {
	now := time.Now().UTC()
	gitlab := &Account{
		ID:              "claude_gitlab_rate_limited",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-1",
		RefreshToken:    "glpat-1",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		RateLimitUntil:  now.Add(45 * time.Second),
		HealthStatus:    "rate_limited",
		HealthError:     managedGitLabClaudeSharedOrgTPMHealthError("This request would exceed your organization's rate limit of 18,000,000 input tokens per minute"),
	}
	direct := &Account{
		ID:          "claude_direct_live",
		Type:        AccountTypeClaude,
		PlanType:    "claude",
		AccessToken: "sk-ant-api-live",
	}
	baseURL, _ := url.Parse(defaultGitLabClaudeGatewayURL)
	h := &proxyHandler{pool: newPoolState([]*Account{gitlab, direct}, false)}
	provider := NewClaudeProvider(baseURL)

	got, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeClaude, "", provider, "/v1/messages", "claude-opus-4-6", "")
	if err != nil {
		t.Fatalf("candidate supporting path: %v", err)
	}
	if got == nil || got.ID != direct.ID {
		t.Fatalf("candidate = %+v, want %s", got, direct.ID)
	}
}

func TestCandidateSupportingPathClaimsCodexSeatBeforeCallerStartsWork(t *testing.T) {
	baseURL, _ := url.Parse("https://example.com")
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)

	first := &Account{
		ID:       "a-seat",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.10,
			PrimaryResetAt:       time.Now().Add(2 * time.Hour),
			SecondaryResetAt:     time.Now().Add(24 * time.Hour),
		},
	}
	second := &Account{
		ID:       "b-seat",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.10,
			PrimaryResetAt:       time.Now().Add(2 * time.Hour),
			SecondaryResetAt:     time.Now().Add(24 * time.Hour),
		},
	}
	h := &proxyHandler{pool: newPoolState([]*Account{first, second}, false)}

	claimedOne, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeCodex, "", provider, "/v1/responses", "", "")
	if err != nil {
		t.Fatalf("first claim err = %v", err)
	}
	if claimedOne == nil || claimedOne.ID != "a-seat" {
		t.Fatalf("expected first claimed codex seat a-seat, got %+v", claimedOne)
	}
	if claimedOne.Inflight != 1 {
		t.Fatalf("expected first claimed seat inflight=1, got %d", claimedOne.Inflight)
	}

	claimedTwo, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeCodex, "", provider, "/v1/responses", "", "")
	if err != nil {
		t.Fatalf("second claim err = %v", err)
	}
	if claimedTwo == nil || claimedTwo.ID != "b-seat" {
		t.Fatalf("expected second claim to avoid already-claimed seat, got %+v", claimedTwo)
	}
	if claimedTwo.Inflight != 1 {
		t.Fatalf("expected second claimed seat inflight=1, got %d", claimedTwo.Inflight)
	}

	claimedOne.Inflight--
	claimedTwo.Inflight--
}

func TestPropagateManagedGitLabClaudeSharedTPMCooldownOnlyTouchesMatchingEntitlementScope(t *testing.T) {
	now := time.Now().UTC()
	trigger := &Account{
		ID:              "claude_gitlab_trigger",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-trigger",
		RefreshToken:    "glpat-trigger",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders: map[string]string{
			"X-Gitlab-Instance-Id":                      "inst-1",
			"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "100,200",
			"X-Gitlab-User-Id":                          "42",
		},
	}
	sameScope := &Account{
		ID:              "claude_gitlab_same_scope",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-same",
		RefreshToken:    "glpat-same",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders: map[string]string{
			"X-Gitlab-Instance-Id":                      "inst-1",
			"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "100,200",
			"X-Gitlab-User-Id":                          "77",
		},
	}
	otherScope := &Account{
		ID:              "claude_gitlab_other_scope",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-other",
		RefreshToken:    "glpat-other",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders: map[string]string{
			"X-Gitlab-Instance-Id":                      "inst-1",
			"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "300",
			"X-Gitlab-User-Id":                          "42",
		},
	}
	h := &proxyHandler{pool: newPoolState([]*Account{trigger, sameScope, otherScope}, false)}
	disposition := managedGitLabClaudeErrorDisposition{
		RateLimit:    true,
		SharedOrgTPM: true,
		HealthStatus: "rate_limited",
		Cooldown:     managedGitLabClaudeOrgTPMRateLimitWait,
		Reason:       "This request would exceed your organization's rate limit of 18,000,000 input tokens per minute",
	}

	if !h.propagateManagedGitLabClaudeSharedTPMCooldown("req-test", trigger, disposition, http.Header{}, "claude-opus-4-6", now) {
		t.Fatal("expected propagation to affect matching entitlement scope")
	}
	if trigger.RateLimitUntil.IsZero() || sameScope.RateLimitUntil.IsZero() {
		t.Fatal("expected matching entitlement scope seats to receive cooldown")
	}
	if !otherScope.RateLimitUntil.IsZero() {
		t.Fatalf("expected other entitlement scope to stay clear, got %v", otherScope.RateLimitUntil)
	}
}

func TestCandidateSupportingPathRejectsForcedDebugGeminiSeatForUnsupportedBlockedV1BetaPath(t *testing.T) {
	blocked := &Account{
		ID:                           "gemini_seat_blocked",
		Type:                         AccountTypeGemini,
		PlanType:                     "gemini",
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "ACCOUNT_NEEDS_WORKSPACE",
	}
	pool := newPoolState([]*Account{blocked}, false)
	h := &proxyHandler{pool: pool}
	provider := &GeminiProvider{}

	got, err := h.candidateSupportingPath("", map[string]bool{}, AccountTypeGemini, "", provider, "/v1beta/models/gemini-2.5-flash:generateContent", "gemini-2.5-flash", blocked.ID)
	if err == nil {
		t.Fatal("expected error")
	}
	if got != nil {
		t.Fatalf("candidate = %+v, want nil", got)
	}
}

func TestSkipPreemptiveRefreshForAllowlistedValidationBlockedGeminiSeat(t *testing.T) {
	if !skipPreemptiveRefreshForAccount(&Account{
		Type:                         AccountTypeGemini,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
	}) {
		t.Fatal("expected allowlisted validation-blocked Gemini seat to skip preemptive refresh")
	}
	if skipPreemptiveRefreshForAccount(&Account{
		Type:                         AccountTypeGemini,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "ACCOUNT_NEEDS_WORKSPACE",
	}) {
		t.Fatal("expected non-allowlisted validation-blocked Gemini seat to keep normal refresh policy")
	}
}

func TestFinalizeProxyResponseRecoversManagedAPIAccountFromResponseConversation(t *testing.T) {
	acc := &Account{
		ID:             "openai_api_deadbeef",
		Type:           AccountTypeCodex,
		AuthMode:       accountAuthModeAPIKey,
		Dead:           true,
		HealthStatus:   "dead",
		HealthError:    "quota_exhausted",
		Penalty:        0.8,
		RateLimitUntil: time.Now().Add(5 * time.Minute),
	}
	pool := newPoolState([]*Account{acc}, false)
	h := &proxyHandler{pool: pool}

	h.finalizeProxyResponse(
		"req-test",
		nil,
		acc,
		"user-1",
		http.StatusOK,
		true,
		false,
		"",
		0,
		0,
		[]byte("data: {\"conversation_id\":\"conv-sse\"}\n"),
	)

	if got := pool.convPin["conv-sse"]; got != acc.ID {
		t.Fatalf("response conversation pin = %q", got)
	}
	if acc.Dead {
		t.Fatal("expected managed api account to recover on success")
	}
	if acc.HealthStatus != "healthy" {
		t.Fatalf("health status = %q", acc.HealthStatus)
	}
	if acc.HealthError != "" {
		t.Fatalf("health error = %q", acc.HealthError)
	}
	if acc.HealthCheckedAt.IsZero() || acc.LastHealthyAt.IsZero() {
		t.Fatal("expected health timestamps to be updated")
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate limit until = %v", acc.RateLimitUntil)
	}
	if acc.LastUsed.IsZero() {
		t.Fatal("expected LastUsed to be updated")
	}
	if acc.Penalty != 0.4 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
}

func TestFinalizeProxyResponseSkipsSuccessRecoveryAfterManagedStreamFailure(t *testing.T) {
	acc := &Account{
		ID:             "openai_api_deadbeef",
		Type:           AccountTypeCodex,
		AuthMode:       accountAuthModeAPIKey,
		Dead:           true,
		HealthStatus:   "dead",
		HealthError:    "stream_failure",
		Penalty:        0.8,
		RateLimitUntil: time.Now().Add(5 * time.Minute),
	}
	pool := newPoolState([]*Account{acc}, false)
	h := &proxyHandler{pool: pool}

	h.finalizeProxyResponse(
		"req-test",
		nil,
		acc,
		"user-1",
		http.StatusOK,
		true,
		true,
		"",
		0,
		0,
		[]byte("data: {\"conversation_id\":\"conv-sse\"}\n"),
	)

	if len(pool.convPin) != 0 {
		t.Fatalf("unexpected pin map: %+v", pool.convPin)
	}
	if !acc.Dead {
		t.Fatal("expected managed api account to stay dead after managed stream failure")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health status = %q", acc.HealthStatus)
	}
	if acc.HealthCheckedAt != (time.Time{}) || acc.LastHealthyAt != (time.Time{}) {
		t.Fatalf("unexpected health timestamps: checked=%v healthy=%v", acc.HealthCheckedAt, acc.LastHealthyAt)
	}
	if acc.LastUsed != (time.Time{}) {
		t.Fatalf("last used = %v", acc.LastUsed)
	}
	if acc.Penalty != 0.8 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
	if acc.RateLimitUntil.IsZero() {
		t.Fatal("expected rate limit state to remain unchanged")
	}
}

func TestFinalizeWebSocketSuccessStateRecoversManagedAPIAccountOnNonSwitching2xx(t *testing.T) {
	acc := &Account{
		ID:             "openai_api_deadbeef",
		Type:           AccountTypeCodex,
		AuthMode:       accountAuthModeAPIKey,
		Dead:           true,
		HealthStatus:   "dead",
		HealthError:    "quota_exhausted",
		Penalty:        0.8,
		RateLimitUntil: time.Now().Add(5 * time.Minute),
	}
	pool := newPoolState([]*Account{acc}, false)
	h := &proxyHandler{pool: pool}

	h.finalizeWebSocketSuccessState(acc, "thread-ws-200", http.StatusOK)

	if got := pool.convPin["thread-ws-200"]; got != acc.ID {
		t.Fatalf("conversation pin = %q", got)
	}
	if acc.Dead {
		t.Fatal("expected managed api account to recover on websocket success")
	}
	if acc.HealthStatus != "healthy" {
		t.Fatalf("health status = %q", acc.HealthStatus)
	}
	if acc.HealthError != "" {
		t.Fatalf("health error = %q", acc.HealthError)
	}
	if acc.HealthCheckedAt.IsZero() || acc.LastHealthyAt.IsZero() {
		t.Fatal("expected health timestamps to be updated")
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate limit until = %v", acc.RateLimitUntil)
	}
	if acc.LastUsed.IsZero() {
		t.Fatal("expected LastUsed to be updated")
	}
	if acc.Penalty != 0.4 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
}

func TestFinalizeWebSocketSuccessStatePersistsRuntimeLastUsed(t *testing.T) {
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 7)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	acc := &Account{
		ID:       "seat-ws",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.12,
			SecondaryUsedPercent: 0.18,
			PrimaryResetAt:       time.Now().Add(time.Hour),
			SecondaryResetAt:     time.Now().Add(12 * time.Hour),
		},
	}
	h := &proxyHandler{store: store}

	h.finalizeWebSocketSuccessState(acc, "", http.StatusSwitchingProtocols)

	restored := &Account{ID: "seat-ws", Type: AccountTypeCodex}
	_, _, _, restoredRuntime := restorePersistedUsageState([]*Account{restored}, store)
	if restoredRuntime != 1 {
		t.Fatalf("restoredRuntime=%d", restoredRuntime)
	}
	if restored.LastUsed.IsZero() {
		t.Fatal("expected LastUsed to be restored from runtime store")
	}
}

func TestFinalizeWebSocketSuccessStateSkipsFailedHandshake(t *testing.T) {
	acc := &Account{
		ID:             "openai_api_deadbeef",
		Type:           AccountTypeCodex,
		AuthMode:       accountAuthModeAPIKey,
		Dead:           true,
		HealthStatus:   "dead",
		HealthError:    "quota_exhausted",
		Penalty:        0.8,
		RateLimitUntil: time.Now().Add(5 * time.Minute),
	}
	pool := newPoolState([]*Account{acc}, false)
	h := &proxyHandler{pool: pool}

	h.finalizeWebSocketSuccessState(acc, "thread-ws-failed", http.StatusTooManyRequests)

	if len(pool.convPin) != 0 {
		t.Fatalf("unexpected pin map: %+v", pool.convPin)
	}
	if !acc.Dead {
		t.Fatal("expected managed api account to stay dead after failed websocket handshake")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health status = %q", acc.HealthStatus)
	}
	if acc.HealthError != "quota_exhausted" {
		t.Fatalf("health error = %q", acc.HealthError)
	}
	if acc.HealthCheckedAt != (time.Time{}) || acc.LastHealthyAt != (time.Time{}) {
		t.Fatalf("unexpected health timestamps: checked=%v healthy=%v", acc.HealthCheckedAt, acc.LastHealthyAt)
	}
	if acc.LastUsed != (time.Time{}) {
		t.Fatalf("last used = %v", acc.LastUsed)
	}
	if acc.Penalty != 0.8 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
	if acc.RateLimitUntil.IsZero() {
		t.Fatal("expected rate limit state to remain unchanged")
	}
}

func TestFinalizeProxyResponsePersistsRuntimeLastUsed(t *testing.T) {
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 7)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	acc := &Account{
		ID:       "seat-buffered",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.11,
			SecondaryUsedPercent: 0.17,
			PrimaryResetAt:       time.Now().Add(time.Hour),
			SecondaryResetAt:     time.Now().Add(12 * time.Hour),
		},
	}
	h := &proxyHandler{store: store}

	h.finalizeProxyResponse(
		"req-test",
		nil,
		acc,
		"user-1",
		http.StatusOK,
		true,
		false,
		"",
		0,
		0,
		nil,
	)

	restored := &Account{ID: "seat-buffered", Type: AccountTypeCodex}
	_, _, _, restoredRuntime := restorePersistedUsageState([]*Account{restored}, store)
	if restoredRuntime != 1 {
		t.Fatalf("restoredRuntime=%d", restoredRuntime)
	}
	if restored.LastUsed.IsZero() {
		t.Fatal("expected LastUsed to be restored from runtime store")
	}
}

func TestFinalizeCopiedProxyResponseRecordsCopyError(t *testing.T) {
	acc := &Account{ID: "acc-1", Type: AccountTypeCodex}
	h := &proxyHandler{
		metrics: newMetrics(),
		recent:  newRecentErrors(5),
	}

	ok := h.finalizeCopiedProxyResponse(
		"req-test",
		nil,
		nil,
		acc,
		"user-1",
		http.StatusOK,
		true,
		false,
		"",
		0,
		0,
		nil,
		errors.New("copy failed"),
		false,
		time.Now(),
		"done",
	)

	if ok {
		t.Fatal("expected copy error path to return false")
	}
	recent := h.recent.snapshot()
	if len(recent) != 1 || recent[0] != "copy failed" {
		t.Fatalf("recent = %+v", recent)
	}
	if got := h.metrics.requests["error"]; got != 1 {
		t.Fatalf("error metric = %d", got)
	}
	if got := h.metrics.accStatus[acc.ID]["error"]; got != 1 {
		t.Fatalf("account error metric = %d", got)
	}
	if acc.LastUsed != (time.Time{}) {
		t.Fatalf("last used = %v", acc.LastUsed)
	}
}

func TestFinalizeCopiedProxyResponseRecordsStatusMetricAndFinalizesSuccess(t *testing.T) {
	acc := &Account{ID: "acc-1", Type: AccountTypeCodex, Penalty: 0.4}
	pool := newPoolState([]*Account{acc}, false)
	h := &proxyHandler{
		pool:    pool,
		metrics: newMetrics(),
		recent:  newRecentErrors(5),
	}

	ok := h.finalizeCopiedProxyResponse(
		"req-test",
		nil,
		nil,
		acc,
		"user-1",
		http.StatusOK,
		true,
		false,
		"conv-initial",
		0,
		0,
		[]byte("data: {\"conversation_id\":\"conv-sse\"}\n"),
		nil,
		false,
		time.Now(),
		"done",
	)

	if !ok {
		t.Fatal("expected success path to return true")
	}
	if got := pool.convPin["conv-initial"]; got != acc.ID {
		t.Fatalf("conversation pin = %q", got)
	}
	if got := h.metrics.requests["200"]; got != 1 {
		t.Fatalf("status metric = %d", got)
	}
	if got := h.metrics.accStatus[acc.ID]["200"]; got != 1 {
		t.Fatalf("account status metric = %d", got)
	}
	if recent := h.recent.snapshot(); len(recent) != 0 {
		t.Fatalf("recent = %+v", recent)
	}
	if acc.LastUsed.IsZero() {
		t.Fatal("expected LastUsed to be updated")
	}
	if acc.Penalty != 0.2 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionManaged5xxRecordsFallbackAndHealth(t *testing.T) {
	tmp := t.TempDir()
	accFile := filepath.Join(tmp, "openai_api_deadbeef.json")
	if err := os.WriteFile(accFile, []byte(`{"OPENAI_API_KEY":"sk-proj-test","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	acc := &Account{
		ID:          "openai_api_deadbeef",
		Type:        AccountTypeCodex,
		File:        accFile,
		AccessToken: "sk-proj-test",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}
	h := &proxyHandler{recent: newRecentErrors(5)}
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Header:     http.Header{},
	}

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		false,
		[]byte(`{"error":{"message":"server boom"}}`),
		"",
		"",
	)

	if err == nil || err.Error() != "managed api fallback 502 Bad Gateway: server boom" {
		t.Fatalf("err = %v", err)
	}
	recent := h.recent.snapshot()
	if len(recent) != 1 || recent[0] != err.Error() {
		t.Fatalf("recent = %+v", recent)
	}
	if acc.Dead {
		t.Fatal("expected managed api key to stay non-dead on transient 5xx")
	}
	if acc.HealthStatus != "error" {
		t.Fatalf("health status = %q", acc.HealthStatus)
	}
	if acc.HealthError != "server boom" {
		t.Fatalf("health error = %q", acc.HealthError)
	}
	if acc.HealthCheckedAt.IsZero() {
		t.Fatal("expected HealthCheckedAt to be updated")
	}
	if acc.Penalty != 0.5 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionMarksPermanentCodexAuthFailureDead(t *testing.T) {
	tmp := t.TempDir()
	accFile := filepath.Join(tmp, "codex-1.json")
	if err := os.WriteFile(accFile, []byte(`{"tokens":{"access_token":"tok"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	acc := &Account{ID: "codex-1", Type: AccountTypeCodex, File: accFile}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Header:     http.Header{"X-Openai-Ide-Error-Code": []string{"account_deactivated"}},
	}

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		false,
		[]byte(`{"error":"account_deactivated"}`),
		"",
		"",
	)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !acc.Dead {
		t.Fatal("expected account to be marked dead")
	}
	if acc.Penalty != 100.0 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != "codex upstream account_deactivated" {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if acc.HealthCheckedAt.IsZero() {
		t.Fatal("expected health_checked_at to be set")
	}
}

func TestApplyPreCopyUpstreamStatusDispositionRefreshFailedMarksNonCodexDead(t *testing.T) {
	tmp := t.TempDir()
	accFile := filepath.Join(tmp, "claude-1.json")
	acc := &Account{ID: "claude-1", Type: AccountTypeClaude, File: accFile}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Header:     http.Header{},
	}

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		true,
		[]byte(`{"error":"refresh failed"}`),
		"",
		"",
	)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !acc.Dead {
		t.Fatal("expected account to be marked dead")
	}
	if acc.Penalty != 1.0 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
	if _, err := os.Stat(accFile); err != nil {
		t.Fatalf("expected account state file to be written: %v", err)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionNonManaged429AppliesRateLimitPenalty(t *testing.T) {
	acc := &Account{ID: "claude-1", Type: AccountTypeClaude}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     http.Header{"Retry-After": []string{"2"}},
	}

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		false,
		nil,
		"",
		"",
	)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if acc.Penalty != 1.0 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
	if acc.RateLimitUntil.IsZero() {
		t.Fatal("expected RateLimitUntil to be set")
	}
}

func TestApplyPreCopyUpstreamStatusDispositionTracksGeminiModelCooldownWithoutSeatWideCooldown(t *testing.T) {
	acc := &Account{
		ID:                       "gemini-1",
		Type:                     AccountTypeGemini,
		PlanType:                 "gemini",
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		AntigravitySource:        "browser_oauth",
		AntigravityProjectID:     "project-1",
		GeminiProviderCheckedAt:  time.Now().UTC(),
		GeminiProviderTruthReady: true,
		GeminiProviderTruthState: geminiProviderTruthStateReady,
	}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     http.Header{},
	}
	resetAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		false,
		[]byte(`{"error":{"message":"model quota exhausted","details":[{"metadata":{"quotaResetTimeStamp":"`+resetAt.Format(time.RFC3339)+`"}}]}}`),
		"gemini-3.1-pro",
		"/v1beta/models/gemini-3.1-pro-high:generateContent",
	)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("expected seat-wide cooldown to stay clear, got %v", acc.RateLimitUntil)
	}
	if acc.Penalty != 1.0 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
	if acc.GeminiOperationalState != geminiOperationalTruthStateCooldown {
		t.Fatalf("operational_state = %q", acc.GeminiOperationalState)
	}
	if acc.GeminiOperationalSource != "proxy" {
		t.Fatalf("operational_source = %q", acc.GeminiOperationalSource)
	}
	if got := acc.GeminiModelRateLimitResetTimes["gemini-3.1-pro-high"]; !got.Equal(resetAt) {
		t.Fatalf("model reset time = %v", got)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionUsesManagedGeminiFallbackCooldownForBare429(t *testing.T) {
	acc := &Account{
		ID:                       "gemini-fallback-429",
		Type:                     AccountTypeGemini,
		PlanType:                 "gemini",
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		AntigravitySource:        "browser_oauth",
		AntigravityProjectID:     "project-1",
		GeminiProviderCheckedAt:  time.Now().UTC(),
		GeminiProviderTruthReady: true,
		GeminiProviderTruthState: geminiProviderTruthStateReady,
	}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     http.Header{},
	}
	before := time.Now().UTC()

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		false,
		nil,
		"gemini-3.1-pro-high",
		"/v1beta/models/gemini-3.1-pro-high:generateContent",
	)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	until := acc.GeminiModelRateLimitResetTimes["gemini-3.1-pro-high"]
	if until.IsZero() {
		t.Fatalf("GeminiModelRateLimitResetTimes = %#v", acc.GeminiModelRateLimitResetTimes)
	}
	wait := until.Sub(before)
	if wait < managedGeminiRateLimitWait-2*time.Second || wait > managedGeminiRateLimitWait+2*time.Second {
		t.Fatalf("wait = %v, want about %v", wait, managedGeminiRateLimitWait)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionTransientCodexAuthAddsPenalty(t *testing.T) {
	acc := &Account{ID: "codex-1", Type: AccountTypeCodex}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{},
	}

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		false,
		[]byte(`{"error":"temporary denied"}`),
		"",
		"",
	)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if acc.Dead {
		t.Fatal("expected account to remain live")
	}
	if acc.Penalty != 10.0 {
		t.Fatalf("penalty = %v", acc.Penalty)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionGitLabQuotaExceededMarksDead(t *testing.T) {
	tmp := t.TempDir()
	accFile := filepath.Join(tmp, "claude_gitlab_deadbeef.json")
	if err := os.WriteFile(accFile, []byte(`{
		"plan_type":"gitlab_duo",
		"auth_mode":"gitlab_duo",
		"gitlab_token":"glpat-source",
		"gitlab_gateway_token":"gateway-token",
		"gitlab_gateway_headers":{"X-Gitlab-Instance-Id":"inst-1"},
		"gitlab_gateway_base_url":"https://cloud.gitlab.com/ai/v1/proxy/anthropic",
		"last_refresh":"2026-03-23T09:00:00Z"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	acc := &Account{
		ID:              "claude_gitlab_deadbeef",
		Type:            AccountTypeClaude,
		File:            accFile,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    "glpat-source",
		AccessToken:     "gateway-token",
		SourceBaseURL:   "https://gitlab.com",
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		LastRefresh:     time.Date(2026, 3, 23, 9, 0, 0, 0, time.UTC),
	}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusPaymentRequired,
		Status:     "402 Payment Required",
		Header:     http.Header{},
	}

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		false,
		[]byte(`{"error":"insufficient_credits","error_code":"USAGE_QUOTA_EXCEEDED"}`),
		"",
		"",
	)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !acc.Dead {
		t.Fatal("expected gitlab account to be marked dead")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("expected RateLimitUntil to stay clear, got %v", acc.RateLimitUntil)
	}
	if acc.GitLabQuotaExceededCount != 0 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", acc.GitLabQuotaExceededCount)
	}
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if !strings.Contains(string(saved), "\"dead\": true") {
		t.Fatalf("expected dead flag in saved file: %s", string(saved))
	}
	if strings.Contains(string(saved), "\"rate_limit_until\"") {
		t.Fatalf("did not expect rate_limit_until in saved file: %s", string(saved))
	}
	if strings.Contains(string(saved), "\"gitlab_quota_exceeded_count\"") {
		t.Fatalf("did not expect gitlab_quota_exceeded_count in saved file: %s", string(saved))
	}
}

func TestApplyManagedGitLabClaudeDispositionGitLabQuotaExceededMarksDeadWithoutCooldown(t *testing.T) {
	acc := &Account{
		ID:              "claude_gitlab_deadbeef",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    "glpat-source",
		AccessToken:     "gateway-token",
		SourceBaseURL:   "https://gitlab.com",
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
	}
	disposition := classifyManagedGitLabClaudeError(
		managedGitLabClaudeErrorSourceGatewayRequest,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"error":"insufficient_credits","error_code":"USAGE_QUOTA_EXCEEDED"}`),
	)

	now := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)
	applyManagedGitLabClaudeDisposition(acc, disposition, http.Header{}, now)
	if !acc.Dead {
		t.Fatal("expected gitlab quota failure to mark account dead")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", acc.RateLimitUntil)
	}
	if acc.GitLabQuotaExceededCount != 0 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", acc.GitLabQuotaExceededCount)
	}
}

func TestApplyPreCopyUpstreamStatusDispositionPreservesDeadGitLabAccount(t *testing.T) {
	acc := &Account{
		ID:              "claude_gitlab_deadbeef",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    "glpat-source",
		AccessToken:     "gateway-token",
		SourceBaseURL:   "https://gitlab.com",
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		Dead:            true,
		HealthStatus:    "dead",
	}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{},
	}

	err := h.applyPreCopyUpstreamStatusDisposition(
		"req-test",
		acc,
		resp,
		true,
		[]byte(`{"error":"temporary denied"}`),
		"",
		"",
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !acc.Dead {
		t.Fatal("expected dead gitlab account to stay dead")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", acc.RateLimitUntil)
	}
}

func TestApplyUpstreamAuthFailureDispositionPreservesDeadGitLabAccount(t *testing.T) {
	acc := &Account{
		ID:              "claude_gitlab_deadbeef",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    "glpat-source",
		AccessToken:     "gateway-token",
		SourceBaseURL:   "https://gitlab.com",
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		Dead:            true,
		HealthStatus:    "dead",
	}
	h := &proxyHandler{}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Header:     http.Header{},
	}

	h.applyUpstreamAuthFailureDisposition(
		"req-test",
		acc,
		resp,
		true,
		[]byte(`{"error":"temporary denied"}`),
	)

	if !acc.Dead {
		t.Fatal("expected dead gitlab account to stay dead")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", acc.RateLimitUntil)
	}
}

func TestRefreshAccountOnceGitLabBypassesPerAccountThrottle(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)
	h := &proxyHandler{
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeClaude: provider,
			},
		},
		refreshTransport: gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return gitlabClaudeJSONResponse(http.StatusOK, `{
				"token": "gateway-token-fresh",
				"base_url": "https://cloud.gitlab.com/ai/v1/proxy/anthropic",
				"expires_at": 1911111111,
				"headers": {
					"X-Gitlab-Instance-Id": "inst-1",
					"X-Gitlab-Realm": "saas"
				}
			}`), nil
		}),
	}

	acc := &Account{
		ID:            "claude_gitlab_deadbeef",
		Type:          AccountTypeClaude,
		File:          filepath.Join(t.TempDir(), "claude_gitlab_deadbeef.json"),
		PlanType:      "gitlab_duo",
		AuthMode:      accountAuthModeGitLab,
		RefreshToken:  "glpat-source",
		SourceBaseURL: "https://gitlab.example.com",
		LastRefresh:   time.Now().UTC(),
	}
	if err := os.WriteFile(acc.File, []byte(`{"plan_type":"gitlab_duo","auth_mode":"gitlab_duo","gitlab_token":"glpat-source"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	if err := h.refreshAccountOnce(context.Background(), acc, false); err != nil {
		t.Fatalf("refreshAccountOnce: %v", err)
	}
	if acc.AccessToken != "gateway-token-fresh" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
}

func TestRefreshAccountOnceKeepsLastRefreshForGeminiOAuthConfigErrorUntouched(t *testing.T) {
	t.Setenv(geminiOAuthEnvClientIDVar, "")
	t.Setenv(geminiOAuthEnvClientSecretVar, "")
	t.Setenv(geminiOAuthCLIClientIDVar, "")
	t.Setenv(geminiOAuthCLIClientSecretVar, "")
	t.Setenv(geminiOAuthGCloudClientIDVar, "")
	t.Setenv(geminiOAuthGCloudClientSecretVar, "")
	t.Setenv(geminiOAuthAntigravitySecretVar, "")

	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	lastRefresh := time.Date(2026, 3, 23, 9, 0, 0, 0, time.UTC)

	h := &proxyHandler{
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeGemini: NewGeminiProvider(geminiBase, geminiAPIBase),
			},
		},
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("unexpected outbound refresh request: %s", req.URL.String())
			return nil, nil
		}),
	}

	acc := &Account{
		ID:             "gemini_cfg_error",
		Type:           AccountTypeGemini,
		File:           filepath.Join(t.TempDir(), "gemini_cfg_error.json"),
		AccessToken:    "access-token",
		RefreshToken:   "refresh-token",
		OperatorSource: geminiOperatorSourceManagedOAuth,
		LastRefresh:    lastRefresh,
	}
	if err := os.WriteFile(acc.File, []byte(`{"access_token":"access-token","refresh_token":"refresh-token","operator_source":"managed_oauth","last_refresh":"2026-03-23T09:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	err := h.refreshAccountOnce(context.Background(), acc, false)
	if err == nil || !strings.Contains(err.Error(), "no configured Gemini OAuth client") {
		t.Fatalf("err = %v, want config error", err)
	}
	if !acc.LastRefresh.Equal(lastRefresh) {
		t.Fatalf("LastRefresh = %v, want unchanged %v", acc.LastRefresh, lastRefresh)
	}
}

func TestFinalizeProxyResponseResetsGitLabQuotaBackoffAfterSuccess(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "claude_gitlab_deadbeef.json")
	if err := os.WriteFile(accFile, []byte(`{
		"plan_type":"gitlab_duo",
		"auth_mode":"gitlab_duo",
		"gitlab_token":"glpat-source",
		"gitlab_gateway_token":"gateway-token",
		"gitlab_gateway_headers":{"X-Gitlab-Instance-Id":"inst-1"},
		"gitlab_gateway_base_url":"https://cloud.gitlab.com/ai/v1/proxy/anthropic",
		"gitlab_quota_exceeded_count":2,
		"rate_limit_until":"2026-03-23T12:00:00Z",
		"health_status":"quota_exceeded",
		"health_error":"quota"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	h := &proxyHandler{}
	acc := &Account{
		ID:                       "claude_gitlab_deadbeef",
		Type:                     AccountTypeClaude,
		File:                     accFile,
		PlanType:                 "gitlab_duo",
		AuthMode:                 accountAuthModeGitLab,
		RefreshToken:             "glpat-source",
		AccessToken:              "gateway-token",
		SourceBaseURL:            "https://gitlab.com",
		UpstreamBaseURL:          defaultGitLabClaudeGatewayURL,
		ExtraHeaders:             map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		GitLabQuotaExceededCount: 2,
		RateLimitUntil:           time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC),
		HealthStatus:             "quota_exceeded",
		HealthError:              "quota",
	}

	h.finalizeProxyResponse("req-test", &ClaudeProvider{}, acc, "pool-user", http.StatusOK, false, false, "", 0, 0, []byte(`{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`))

	if acc.GitLabQuotaExceededCount != 0 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", acc.GitLabQuotaExceededCount)
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", acc.RateLimitUntil)
	}
	if acc.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if strings.Contains(string(saved), "\"gitlab_quota_exceeded_count\"") {
		t.Fatalf("expected saved file to clear quota count: %s", string(saved))
	}
}

func TestInspectResponseBodyForClassificationPlaintext(t *testing.T) {
	body := []byte(`{"error":{"code":"insufficient_quota"}}`)
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(bytes.NewReader(body)),
	}
	result := inspectResponseBodyForClassification(resp, preCopyStatusInspectionLimit)
	if !bytes.Equal(result.Inspected, body) {
		t.Fatalf("Inspected = %q, want %q", result.Inspected, body)
	}
	if !bytes.Equal(result.RawPrefix, body) {
		t.Fatalf("RawPrefix = %q, want %q", result.RawPrefix, body)
	}

	remaining, _ := io.ReadAll(resp.Body)
	if len(remaining) != 0 {
		t.Fatalf("body not drained: %d bytes remaining", len(remaining))
	}
}

func TestInspectResponseBodyForClassificationGzip(t *testing.T) {
	plain := []byte(`{"error":{"code":"insufficient_quota"}}`)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(plain); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	raw := buf.Bytes()

	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"application/json"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(raw)),
	}
	result := inspectResponseBodyForClassification(resp, preCopyStatusInspectionLimit)
	if !bytes.Equal(result.Inspected, plain) {
		t.Fatalf("Inspected = %q, want decoded %q", result.Inspected, plain)
	}
	if len(result.RawPrefix) == 0 {
		t.Fatal("RawPrefix should contain raw gzip bytes")
	}
	if bytes.Equal(result.RawPrefix, plain) {
		t.Fatal("RawPrefix should be raw gzip bytes, not decoded")
	}
}

func TestInspectResponseBodyForClassificationDoesNotReplayAutomatically(t *testing.T) {
	plain := []byte(`{"error":{"code":"insufficient_quota"}}`)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(plain); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	raw := buf.Bytes()

	tail := []byte("TAIL-DATA")
	combined := append(append([]byte(nil), raw...), tail...)

	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"application/json"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(combined)),
	}
	result := inspectResponseBodyForClassification(resp, preCopyStatusInspectionLimit)
	if !bytes.Equal(result.Inspected, plain) {
		t.Fatalf("Inspected = %q, want %q", result.Inspected, plain)
	}

	resp.Body = replayResponseBody(result.RawPrefix, resp.Body)
	full, _ := io.ReadAll(resp.Body)
	if !bytes.HasPrefix(full, result.RawPrefix) {
		t.Fatalf("replayed body should start with raw prefix")
	}
	if !bytes.HasSuffix(full, tail) {
		t.Fatalf("replayed body should end with tail data")
	}
}

func TestInspectBufferedRetryBodyDecodesGzip(t *testing.T) {
	plain := []byte(`{"error":{"code":"insufficient_quota"}}`)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(plain); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	raw := buf.Bytes()

	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"application/json"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(raw)),
	}
	inspected := inspectBufferedRetryBody(resp.Body, preCopyStatusInspectionLimit)
	if !bytes.Equal(inspected, plain) {
		t.Fatalf("inspected = %q, want %q", inspected, plain)
	}
}
