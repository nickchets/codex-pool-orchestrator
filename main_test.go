package main

import (
	"context"
	"encoding/json"
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
	resp, _, _, err := h.tryOnce(context.Background(), req, body, apiBase, provider, acc, "req-test")
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

// mapToHeader is a tiny helper to build http.Header in tests without importing net/http everywhere.
func mapToHeader(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}
