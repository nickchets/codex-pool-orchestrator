package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testCodexModelsHandler(t *testing.T, transport http.RoundTripper, startTime time.Time, accounts ...*Account) *proxyHandler {
	t.Helper()

	responsesBase, _ := url.Parse("https://chatgpt.example.com/backend-api/codex")
	whamBase, _ := url.Parse("https://chatgpt.example.com/backend-api")
	claudeBase, _ := url.Parse("https://claude.example.com")
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewCodexProvider(responsesBase, whamBase, nil, responsesBase)
	claude := NewClaudeProvider(claudeBase)
	gemini := NewGeminiProvider(geminiBase, geminiAPIBase)
	return &proxyHandler{
		startTime:        startTime,
		transport:        transport,
		refreshTransport: transport,
		pool:             newPoolState(accounts, false),
		registry:         NewProviderRegistry(provider, claude, gemini),
	}
}

func newGitLabCodexTestAccount(id, gatewayToken string, lastHealthy time.Time) *Account {
	return &Account{
		ID:              id,
		Type:            AccountTypeCodex,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     gatewayToken,
		RefreshToken:    "gitlab-source-" + id,
		UpstreamBaseURL: "https://gitlab.example.com/ai/v1/proxy/openai",
		ExtraHeaders: map[string]string{
			"x-gitlab-user-id": "42",
		},
		HealthStatus:  "healthy",
		LastHealthyAt: lastHealthy,
		Usage: UsageSnapshot{
			RetrievedAt: time.Now().UTC(),
		},
	}
}

func TestProbeGitLabCodexModelQuotaExceededAppliesCooldown(t *testing.T) {
	acc := newGitLabCodexTestAccount("gitlab-a", "gateway-a", time.Now().UTC())
	acc.File = filepath.Join(t.TempDir(), "gitlab-a.json")
	if err := os.WriteFile(acc.File, []byte(`{"plan_type":"gitlab_duo","auth_mode":"gitlab_duo","gitlab_token":"source-a","gitlab_gateway_token":"gateway-a","gitlab_gateway_headers":{"x-gitlab-user-id":"42"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPaymentRequired,
			Status:     "402 Payment Required",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"error":"insufficient_credits",
				"error_code":"USAGE_QUOTA_EXCEEDED",
				"message":"Consumer does not have sufficient credits for this request"
			}`)),
		}, nil
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)
	provider, _ := h.registry.ForType(AccountTypeCodex).(*CodexProvider)

	_, err := h.probeGitLabCodexModel(context.Background(), provider, acc, gitLabCodexModelSpec{Slug: "gpt-5-codex"})
	if err == nil {
		t.Fatal("expected quota-exceeded probe to fail")
	}
	if acc.Dead {
		t.Fatal("expected quota-exceeded probe to keep GitLab Codex seat non-dead")
	}
	if acc.HealthStatus != "quota_exceeded" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.GitLabQuotaExceededCount != 1 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", acc.GitLabQuotaExceededCount)
	}
	if acc.RateLimitUntil.IsZero() {
		t.Fatal("expected quota-exceeded probe to set cooldown")
	}
}

func TestCodexWarmStateBlocksRecentStartupWithoutUsageSnapshots(t *testing.T) {
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AccessToken: "token-a",
	}
	h := testCodexModelsHandler(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be called during warm-up gate")
		return nil, nil
	}), time.Now(), acc)

	ready, missing, total := h.codexWarmState(time.Now())
	if ready {
		t.Fatal("expected codex warm state to block when usage snapshots are still missing")
	}
	if missing != 1 || total != 1 {
		t.Fatalf("warm state counts = (%d missing, %d total)", missing, total)
	}
}

func TestCodexWarmStateAllowsAfterTimeout(t *testing.T) {
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AccessToken: "token-a",
	}
	h := testCodexModelsHandler(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, nil
	}), time.Now().Add(-codexStartupWarmTimeout-time.Second), acc)

	ready, missing, total := h.codexWarmState(time.Now())
	if !ready {
		t.Fatal("expected codex warm state to allow traffic after timeout")
	}
	if missing != 1 || total != 1 {
		t.Fatalf("warm state counts = (%d missing, %d total)", missing, total)
	}
}

func TestServeCodexModelsCachesSuccessfulUpstreamResponse(t *testing.T) {
	now := time.Now().UTC()
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AccessToken: "token-a",
		Usage: UsageSnapshot{
			RetrievedAt: now,
		},
	}

	var calls atomic.Int32
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer token-a" {
			t.Fatalf("authorization header = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5.4"}]}`)),
		}, nil
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models?client_version=0.106.0", nil)
	routePlan := RoutePlan{
		AccountType: AccountTypeCodex,
		Provider:    h.registry.ForType(AccountTypeCodex),
	}

	rr1 := httptest.NewRecorder()
	h.serveCodexModelsForTest(rr1, req, "req-1", routePlan)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", rr1.Code, rr1.Body.String())
	}
	if got := rr1.Header().Get("X-Codex-Models-Cache"); got != "refresh" {
		t.Fatalf("first cache header = %q", got)
	}

	rr2 := httptest.NewRecorder()
	h.serveCodexModelsForTest(rr2, req, "req-2", routePlan)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", rr2.Code, rr2.Body.String())
	}
	if got := rr2.Header().Get("X-Codex-Models-Cache"); got != "hit" {
		t.Fatalf("second cache header = %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one upstream call, got %d", calls.Load())
	}
}

func TestServeCodexModelsServesStaleCacheOnRefreshFailure(t *testing.T) {
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AccessToken: "token-a",
		Usage: UsageSnapshot{
			RetrievedAt: time.Now().UTC(),
		},
	}
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, io.EOF
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)
	h.codexModels.store(codexModelsCacheEntry{
		Body:        []byte(`{"data":[{"id":"cached"}]}`),
		ContentType: "application/json",
		FetchedAt:   time.Now().Add(-2 * time.Hour),
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models?client_version=0.106.0", nil)
	routePlan := RoutePlan{
		AccountType: AccountTypeCodex,
		Provider:    h.registry.ForType(AccountTypeCodex),
	}

	rr := httptest.NewRecorder()
	h.serveCodexModelsForTest(rr, req, "req-1", routePlan)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Codex-Models-Cache"); got != "stale" {
		t.Fatalf("cache header = %q", got)
	}
	if body := strings.TrimSpace(rr.Body.String()); body != `{"data":[{"id":"cached"}]}` {
		t.Fatalf("body = %s", body)
	}
}

func TestFetchCodexModelsLogsTraceRefresh(t *testing.T) {
	now := time.Now().UTC()
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-a",
		Usage: UsageSnapshot{
			RetrievedAt: now,
		},
	}

	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5.4"}]}`)),
		}, nil
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models?client_version=0.106.0", nil)
	req = req.WithContext(testTraceContext("req-models"))
	routePlan := RoutePlan{
		AccountType: AccountTypeCodex,
		Provider:    h.registry.ForType(AccountTypeCodex),
	}

	logs := captureLogs(t, func() {
		if _, err := h.fetchCodexModels(req, "req-models", routePlan); err != nil {
			t.Fatalf("fetchCodexModels: %v", err)
		}
	})

	if !strings.Contains(logs, "[req-models] trace models_cache") {
		t.Fatalf("missing models_cache trace log: %s", logs)
	}
	if !strings.Contains(logs, `provider=codex`) || !strings.Contains(logs, `state=refresh`) {
		t.Fatalf("unexpected models_cache trace log: %s", logs)
	}
}

func TestFetchCodexModelsDefaultsClientVersion(t *testing.T) {
	now := time.Now().UTC()
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-a",
		Usage: UsageSnapshot{
			RetrievedAt: now,
		},
	}

	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.Query().Get("client_version"); got != codexClientVersion {
			t.Fatalf("client_version=%q want %q", got, codexClientVersion)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5.4"}]}`)),
		}, nil
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models", nil)
	req = req.WithContext(testTraceContext("req-models-default-version"))
	routePlan := RoutePlan{
		AccountType: AccountTypeCodex,
		Provider:    h.registry.ForType(AccountTypeCodex),
	}

	if _, err := h.fetchCodexModels(req, "req-models-default-version", routePlan); err != nil {
		t.Fatalf("fetchCodexModels: %v", err)
	}
}

func TestMaybeServeCachedCodexModelsLogsSingleRefreshError(t *testing.T) {
	now := time.Now().UTC()
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-a",
		Usage: UsageSnapshot{
			RetrievedAt: now,
		},
	}

	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, io.EOF
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models", nil)
	req = req.WithContext(testTraceContext("req-models-refresh-error"))
	rr := httptest.NewRecorder()

	logs := captureLogs(t, func() {
		handled := h.maybeServeCachedCodexModels(rr, req, "req-models-refresh-error", AdmissionResult{
			Kind:         AdmissionKindPoolUser,
			UserID:       "pool-user",
			ProviderType: AccountTypeCodex,
		})
		if !handled {
			t.Fatal("expected codex models request to be handled")
		}
	})

	if got := strings.Count(logs, "trace models_cache"); got != 1 {
		t.Fatalf("models_cache trace count=%d logs=%s", got, logs)
	}
	if !strings.Contains(logs, `state=refresh_error`) {
		t.Fatalf("missing refresh_error trace log: %s", logs)
	}
}

func TestMaybeServeCachedCodexModelsServesDiscoveredGitLabCatalog(t *testing.T) {
	acc := newGitLabCodexTestAccount("gitlab-a", "gateway-a", time.Now().UTC())
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/ai/v1/proxy/openai/v1/responses" {
			t.Fatalf("unexpected probe path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gateway-a" {
			t.Fatalf("authorization header = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		text := string(body)
		switch {
		case strings.Contains(text, `"model":"gpt-5-codex"`):
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"model":"gpt-5-codex","output":[{"content":[{"type":"output_text","text":"OK"}]}]}`)),
			}, nil
		case strings.Contains(text, `"model":"gpt-5.4-mini"`):
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"model":"gpt-5.4-mini","output":[{"content":[{"type":"output_text","text":"OK"}]}]}`)),
			}, nil
		case strings.Contains(text, `"model":"gpt-5.4"`):
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Status:     "400 Bad Request",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"detail":"Unsupported model"}`)),
			}, nil
		default:
			t.Fatalf("unexpected probe body = %s", text)
			return nil, nil
		}
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab
	h.cfg.gitLabCodexDiscoveryModels = []string{"gpt-5-codex", "gpt-5.4-mini", "gpt-5.4"}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models?client_version=0.106.0", nil)
	req = req.WithContext(testTraceContext("req-models-gitlab"))
	rr := httptest.NewRecorder()

	handled := h.maybeServeCachedCodexModels(rr, req, "req-models-gitlab", AdmissionResult{
		Kind:   AdmissionKindPoolUser,
		UserID: "pool-user",
	})
	if !handled {
		t.Fatal("expected codex models request to be handled")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Codex-Models-Cache"); got != "gitlab-refresh" {
		t.Fatalf("cache header=%q", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"slug":"gpt-5-codex"`) {
		t.Fatalf("expected gpt-5-codex in body: %s", body)
	}
	if !strings.Contains(body, `"slug":"gpt-5.4-mini"`) {
		t.Fatalf("expected gpt-5.4-mini in body: %s", body)
	}
	if strings.Contains(body, `"slug":"gpt-5.4"`) {
		t.Fatalf("did not expect unsupported gpt-5.4 in body: %s", body)
	}
	if !strings.Contains(body, `"supported_reasoning_levels"`) {
		t.Fatalf("expected full-support discovered catalog shape in body: %s", body)
	}
	if strings.Contains(body, `"effort":"xhigh"`) {
		t.Fatalf("did not expect xhigh reasoning level in discovered catalog: %s", body)
	}
	if !strings.Contains(body, `"default_verbosity":"medium"`) {
		t.Fatalf("expected medium default verbosity in discovered catalog: %s", body)
	}
	if strings.Contains(body, gitLabCodexAliasModel()) {
		t.Fatalf("did not expect alias model in discovered catalog: %s", body)
	}
}

func TestMaybeServeCachedCodexModelsIntersectsGitLabModelsAcrossSeats(t *testing.T) {
	accA := newGitLabCodexTestAccount("gitlab-a", "gateway-a", time.Now().UTC())
	accB := newGitLabCodexTestAccount("gitlab-b", "gateway-b", time.Now().UTC().Add(-time.Minute))
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		token := r.Header.Get("Authorization")
		modelBody := string(body)
		supported := map[string]map[string]bool{
			"Bearer gateway-a": {
				`"model":"gpt-5-codex"`:  true,
				`"model":"gpt-5.4-mini"`: true,
				`"model":"gpt-5.4-nano"`: true,
			},
			"Bearer gateway-b": {
				`"model":"gpt-5-codex"`:  true,
				`"model":"gpt-5.4-mini"`: true,
			},
		}
		for probe, ok := range supported[token] {
			if strings.Contains(modelBody, probe) && ok {
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"output":[{"content":[{"type":"output_text","text":"OK"}]}]}`)),
				}, nil
			}
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"detail":"Unsupported model"}`)),
		}, nil
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), accA, accB)
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab
	h.cfg.gitLabCodexDiscoveryModels = []string{"gpt-5-codex", "gpt-5.4-mini", "gpt-5.4-nano"}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models?client_version=0.106.0", nil)
	req = req.WithContext(testTraceContext("req-models-gitlab-intersection"))
	rr := httptest.NewRecorder()

	handled := h.maybeServeCachedCodexModels(rr, req, "req-models-gitlab-intersection", AdmissionResult{
		Kind:   AdmissionKindPoolUser,
		UserID: "pool-user",
	})
	if !handled {
		t.Fatal("expected codex models request to be handled")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"slug":"gpt-5-codex"`) || !strings.Contains(body, `"slug":"gpt-5.4-mini"`) {
		t.Fatalf("expected common supported models in body: %s", body)
	}
	if strings.Contains(body, `"slug":"gpt-5.4-nano"`) {
		t.Fatalf("did not expect non-common model in body: %s", body)
	}
}

func TestMaybeServeCachedCodexModelsFallsBackWhenGitLabDiscoveryFails(t *testing.T) {
	acc := newGitLabCodexTestAccount("gitlab-a", "gateway-a", time.Now().UTC())
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, io.EOF
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab
	h.cfg.gitLabCodexDiscoveryModels = []string{"gpt-5-codex", "gpt-5.4-mini"}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models?client_version=0.106.0", nil)
	req = req.WithContext(testTraceContext("req-models-gitlab-fallback"))
	rr := httptest.NewRecorder()

	handled := h.maybeServeCachedCodexModels(rr, req, "req-models-gitlab-fallback", AdmissionResult{
		Kind:   AdmissionKindPoolUser,
		UserID: "pool-user",
	})
	if !handled {
		t.Fatal("expected codex models request to be handled")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Codex-Models-Cache"); got != "gitlab-fallback" {
		t.Fatalf("cache header=%q", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"slug":"gpt-5-codex"`) {
		t.Fatalf("expected fallback gpt-5-codex in body: %s", body)
	}
	if strings.Contains(body, `"slug":"gpt-5.4-mini"`) {
		t.Fatalf("did not expect undiscovered model in fallback body: %s", body)
	}
}

func TestMaybeServeCachedCodexModelsHandlesV1ModelsAliasForGitLab(t *testing.T) {
	acc := newGitLabCodexTestAccount("gitlab-a", "gateway-a", time.Now().UTC())
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be called when gitlab catalog cache is fresh")
		return nil, nil
	})
	h := testCodexModelsHandler(t, transport, time.Now().Add(-time.Minute), acc)
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab
	h.gitlabCodexModels.store(buildGitLabCodexModelsEntry([]gitLabCodexModelSpec{
		{Slug: "gpt-5-codex", DisplayName: "GPT-5 Codex", Priority: 1},
		{Slug: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", Priority: 2},
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/v1/models", nil)
	req = req.WithContext(testTraceContext("req-models-gitlab-v1-alias"))
	rr := httptest.NewRecorder()

	handled := h.maybeServeCachedCodexModels(rr, req, "req-models-gitlab-v1-alias", AdmissionResult{
		Kind:   AdmissionKindPoolUser,
		UserID: "pool-user",
	})
	if !handled {
		t.Fatal("expected /v1/models to be handled as a codex models request")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Codex-Models-Cache"); got != "gitlab-hit" {
		t.Fatalf("cache header=%q", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"slug":"gpt-5-codex"`) || !strings.Contains(body, `"slug":"gpt-5.4-mini"`) {
		t.Fatalf("expected cached gitlab catalog in body: %s", body)
	}
}

func TestEnsureCodexRouteReadyLogsWarmupBlock(t *testing.T) {
	acc := &Account{
		ID:          "seat-a",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-a",
	}
	h := testCodexModelsHandler(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be called during warm-up gate")
		return nil, nil
	}), time.Now(), acc)

	trace := &requestTrace{
		cfg:       requestTraceConfig{requests: true},
		reqID:     "req-warm",
		startedAt: time.Now(),
	}
	rr := httptest.NewRecorder()
	logs := captureLogs(t, func() {
		if h.ensureCodexRouteReady(rr, "req-warm", RoutePlan{AccountType: AccountTypeCodex}, trace) {
			t.Fatal("expected warm-up block")
		}
	})

	if !strings.Contains(logs, "[req-warm] trace route_gate") {
		t.Fatalf("missing route_gate trace log: %s", logs)
	}
	if !strings.Contains(logs, `reason=warmup`) {
		t.Fatalf("unexpected route_gate trace log: %s", logs)
	}
}

func (h *proxyHandler) serveCodexModelsForTest(w http.ResponseWriter, r *http.Request, reqID string, routePlan RoutePlan) {
	now := time.Now()
	if cached, ok := h.codexModels.load(); ok {
		age := now.Sub(cached.FetchedAt)
		if age < codexModelsFreshTTL {
			writeCodexModelsCacheResponse(w, cached, "hit")
			return
		}
	}

	refreshed, refreshErr := h.fetchCodexModels(r, reqID, routePlan)
	if refreshErr == nil {
		h.codexModels.store(refreshed)
		writeCodexModelsCacheResponse(w, refreshed, "refresh")
		return
	}

	if cached, ok := h.codexModels.load(); ok && now.Sub(cached.FetchedAt) < codexModelsMaxStaleTTL {
		writeCodexModelsCacheResponse(w, cached, "stale")
		return
	}

	http.Error(w, refreshErr.Error(), http.StatusBadGateway)
}
