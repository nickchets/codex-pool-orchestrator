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
	"testing"
	"time"
)

func TestCodexProviderLoadAccountGitLab(t *testing.T) {
	provider := NewCodexProvider(nil, nil, nil, nil)
	acc, err := provider.LoadAccount("codex_gitlab_deadbeef.json", "/tmp/codex_gitlab_deadbeef.json", []byte(`{
		"plan_type":"gitlab_duo",
		"auth_mode":"gitlab_duo",
		"gitlab_token":"glpat-source",
		"gitlab_instance_url":"https://gitlab.example.com",
		"gitlab_gateway_token":"gateway-token",
		"gitlab_gateway_headers":{"x-gitlab-user-id":"42"},
		"gitlab_gateway_base_url":"https://cloud.gitlab.com/ai/v1/proxy/openai",
		"health_status":"healthy"
	}`))
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account")
	}
	if !isGitLabCodexAccount(acc) {
		t.Fatalf("expected gitlab codex account, got auth_mode=%q", acc.AuthMode)
	}
	if acc.UpstreamBaseURL != defaultGitLabCodexGatewayURL {
		t.Fatalf("upstream_base_url=%q", acc.UpstreamBaseURL)
	}
	if acc.ExtraHeaders["x-gitlab-user-id"] != "42" {
		t.Fatalf("extra_headers=%v", acc.ExtraHeaders)
	}
}

func TestCodexProviderRefreshTokenGitLab(t *testing.T) {
	baseURL, _ := url.Parse("https://chatgpt.example.com/backend-api/codex")
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{
		ID:            "codex_gitlab_deadbeef",
		Type:          AccountTypeCodex,
		File:          filepath.Join(t.TempDir(), "codex_gitlab_deadbeef.json"),
		PlanType:      "gitlab_duo",
		AuthMode:      accountAuthModeGitLab,
		RefreshToken:  "glpat-source",
		SourceBaseURL: "https://gitlab.example.com",
	}
	if err := os.WriteFile(acc.File, []byte(`{"plan_type":"gitlab_duo","auth_mode":"gitlab_duo","gitlab_token":"glpat-source"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://gitlab.example.com/api/v4/ai/third_party_agents/direct_access" {
			t.Fatalf("unexpected url: %s", req.URL.String())
		}
		return jsonResponse(http.StatusCreated, `{
			"token":"gateway-token-fresh",
			"expires_at":1911111111,
			"headers":{"x-gitlab-user-id":"42"}
		}`), nil
	})

	if err := provider.RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if acc.AccessToken != "gateway-token-fresh" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
	if acc.UpstreamBaseURL != defaultGitLabCodexGatewayURL {
		t.Fatalf("upstream_base_url=%q", acc.UpstreamBaseURL)
	}
	if acc.ExtraHeaders["x-gitlab-user-id"] != "42" {
		t.Fatalf("extra_headers=%v", acc.ExtraHeaders)
	}
}

func TestClassifyManagedGitLabCodexErrorQuotaExceededGatewayRequest(t *testing.T) {
	disposition := classifyManagedGitLabCodexError(
		managedGitLabCodexErrorSourceGatewayRequest,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"error":"insufficient_credits","error_code":"USAGE_QUOTA_EXCEEDED","message":"Consumer does not have sufficient credits for this request"}`),
	)
	if disposition.MarkDead {
		t.Fatal("expected quota-exceeded gateway request to avoid dead disposition")
	}
	if !disposition.RateLimit {
		t.Fatal("expected quota-exceeded gateway request to activate cooldown")
	}
	if disposition.HealthStatus != "quota_exceeded" {
		t.Fatalf("health_status=%q", disposition.HealthStatus)
	}
}

func TestApplyManagedGitLabCodexDispositionQuotaExceededTracksCooldown(t *testing.T) {
	acc := &Account{
		ID:          "codex_gitlab_quota",
		Type:        AccountTypeCodex,
		PlanType:    "gitlab_duo",
		AuthMode:    accountAuthModeGitLab,
		AccessToken: "gateway-token",
	}
	now := time.Now().UTC()
	applyManagedGitLabCodexDisposition(acc, managedGitLabCodexErrorDisposition{
		RateLimit:    true,
		HealthStatus: "quota_exceeded",
		Reason:       "Consumer does not have sufficient credits for this request",
	}, http.Header{}, now)

	if acc.Dead {
		t.Fatal("expected quota-exceeded GitLab Codex account to stay non-dead")
	}
	if acc.HealthStatus != "quota_exceeded" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.GitLabQuotaExceededCount != 1 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", acc.GitLabQuotaExceededCount)
	}
	if !acc.RateLimitUntil.After(now) {
		t.Fatalf("rate_limit_until=%v now=%v", acc.RateLimitUntil, now)
	}
}

func TestClassifyManagedGitLabCodexErrorGatewayRejected403(t *testing.T) {
	disposition := classifyManagedGitLabCodexError(
		managedGitLabCodexErrorSourceGatewayRequest,
		http.StatusForbidden,
		http.Header{},
		[]byte("error code: 1010"),
	)
	if disposition.MarkDead {
		t.Fatal("expected gateway-rejected 403 to avoid dead disposition")
	}
	if !disposition.RateLimit {
		t.Fatal("expected gateway-rejected 403 to activate cooldown")
	}
	if disposition.HealthStatus != "gateway_rejected" {
		t.Fatalf("health_status=%q", disposition.HealthStatus)
	}
	if disposition.Cooldown != managedGitLabCodexGatewayRejectWait {
		t.Fatalf("cooldown=%v", disposition.Cooldown)
	}
}

func TestPlanRouteGitLabCodexAliasRewritesModel(t *testing.T) {
	base, _ := url.Parse("https://chatgpt.example.com/backend-api/codex")
	h := &proxyHandler{
		registry: NewProviderRegistry(
			NewCodexProvider(base, base, base, base),
			NewClaudeProvider(base),
			NewGeminiProvider(base, base),
		),
	}

	body := []byte(`{"model":"gitlab/gpt-5-codex","input":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(string(body)))

	plan, rewritten, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser}, req, buildBufferedRequestShape(req, body, body), body)
	if err != nil {
		t.Fatalf("planRoute: %v", err)
	}
	if plan.RequiredPlan != accountAuthModeGitLab {
		t.Fatalf("required_plan=%q", plan.RequiredPlan)
	}
	if strings.Contains(string(rewritten), "gitlab/gpt-5-codex") {
		t.Fatalf("expected rewritten body, got %s", string(rewritten))
	}
	if !strings.Contains(string(rewritten), `"model":"gpt-5-codex"`) {
		t.Fatalf("unexpected rewritten body: %s", string(rewritten))
	}
}

func TestCoerceGitLabCodexRequestBodyForcesMediumVerbosity(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","text":{"verbosity":"low"},"input":"hi"}`)

	rewritten, changed := coerceGitLabCodexRequestBody(body)
	if !changed {
		t.Fatal("expected body rewrite")
	}
	if strings.Contains(string(rewritten), `"verbosity":"low"`) {
		t.Fatalf("expected low verbosity to be removed: %s", string(rewritten))
	}
	if !strings.Contains(string(rewritten), `"verbosity":"medium"`) {
		t.Fatalf("expected medium verbosity in rewritten body: %s", string(rewritten))
	}
}

func TestPoolCandidateDefaultsExcludeGitLabCodex(t *testing.T) {
	now := time.Now().UTC()
	gitlab := &Account{
		ID:          "gitlab",
		Type:        AccountTypeCodex,
		PlanType:    "gitlab_duo",
		AuthMode:    accountAuthModeGitLab,
		AccessToken: "token-gitlab",
		ExtraHeaders: map[string]string{
			"x-gitlab-user-id": "42",
		},
		Usage: UsageSnapshot{RetrievedAt: now},
	}
	regular := &Account{
		ID:          "regular",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-regular",
		Usage:       UsageSnapshot{RetrievedAt: now},
	}
	pool := newPoolState([]*Account{gitlab, regular}, false)

	got := pool.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "regular" {
		t.Fatalf("expected regular codex seat, got %+v", got)
	}
}

func TestPoolCandidateGitLabRequiredPlanSelectsGitLabCodex(t *testing.T) {
	now := time.Now().UTC()
	gitlab := &Account{
		ID:          "gitlab",
		Type:        AccountTypeCodex,
		PlanType:    "gitlab_duo",
		AuthMode:    accountAuthModeGitLab,
		AccessToken: "token-gitlab",
		ExtraHeaders: map[string]string{
			"x-gitlab-user-id": "42",
		},
		Usage: UsageSnapshot{RetrievedAt: now},
	}
	regular := &Account{
		ID:          "regular",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-regular",
		Usage:       UsageSnapshot{RetrievedAt: now},
	}
	pool := newPoolState([]*Account{gitlab, regular}, false)

	got := pool.candidate("", nil, AccountTypeCodex, accountAuthModeGitLab)
	if got == nil || got.ID != "gitlab" {
		t.Fatalf("expected gitlab codex seat, got %+v", got)
	}
}

func TestPoolCandidateGitLabRequiredPlanAllowsBootstrapWithoutGatewayState(t *testing.T) {
	now := time.Now().UTC()
	gitlab := &Account{
		ID:           "gitlab",
		Type:         AccountTypeCodex,
		PlanType:     "gitlab_duo",
		AuthMode:     accountAuthModeGitLab,
		RefreshToken: "glpat-source",
		Usage:        UsageSnapshot{RetrievedAt: now},
	}
	regular := &Account{
		ID:          "regular",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-regular",
		Usage:       UsageSnapshot{RetrievedAt: now},
	}
	pool := newPoolState([]*Account{gitlab, regular}, false)

	got := pool.candidate("", nil, AccountTypeCodex, accountAuthModeGitLab)
	if got == nil || got.ID != "gitlab" {
		t.Fatalf("expected bootstrap gitlab codex seat, got %+v", got)
	}
}

func TestFetchCodexModelsSkipsGitLabCodexSeatsAndAddsAlias(t *testing.T) {
	now := time.Now().UTC()
	gitlab := &Account{
		ID:          "gitlab",
		Type:        AccountTypeCodex,
		PlanType:    "gitlab_duo",
		AuthMode:    accountAuthModeGitLab,
		AccessToken: "token-gitlab",
		ExtraHeaders: map[string]string{
			"x-gitlab-user-id": "42",
		},
		Usage: UsageSnapshot{RetrievedAt: now},
	}
	regular := &Account{
		ID:          "regular",
		Type:        AccountTypeCodex,
		PlanType:    "pro",
		AuthMode:    accountAuthModeOAuth,
		AccessToken: "token-regular",
		Usage:       UsageSnapshot{RetrievedAt: now},
	}

	var seenAuth []string
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		if r.URL.Path != "/backend-api/codex/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5-codex"}]}`)),
		}, nil
	})

	responsesBase, _ := url.Parse("https://chatgpt.example.com/backend-api/codex")
	whamBase, _ := url.Parse("https://chatgpt.example.com/backend-api")
	h := &proxyHandler{
		startTime: time.Now().Add(-time.Minute),
		transport: transport,
		pool:      newPoolState([]*Account{gitlab, regular}, false),
		registry: NewProviderRegistry(
			NewCodexProvider(responsesBase, whamBase, nil, responsesBase),
			NewClaudeProvider(responsesBase),
			NewGeminiProvider(responsesBase, responsesBase),
		),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/backend-api/codex/models?client_version=0.106.0", nil)
	entry, err := h.fetchCodexModels(req, "req-models", RoutePlan{
		AccountType: AccountTypeCodex,
		Provider:    h.registry.ForType(AccountTypeCodex),
	})
	if err != nil {
		t.Fatalf("fetchCodexModels: %v", err)
	}
	if len(seenAuth) != 1 || seenAuth[0] != "Bearer token-regular" {
		t.Fatalf("unexpected auth headers: %v", seenAuth)
	}
	if !strings.Contains(string(entry.Body), gitLabCodexAliasModel()) {
		t.Fatalf("expected gitlab alias in models body: %s", string(entry.Body))
	}
}

func TestWithGitLabCodexModelAliasSupportsModelsSlugPayload(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5.4"},{"slug":"gpt-5-codex","display_name":"GPT-5 Codex"}]}`)
	got := withGitLabCodexModelAlias(body)
	if !strings.Contains(string(got), `"slug":"gitlab/gpt-5-codex"`) {
		t.Fatalf("expected gitlab slug alias in payload: %s", string(got))
	}
	if strings.Count(string(got), `"slug":"gitlab/gpt-5-codex"`) != 1 {
		t.Fatalf("expected single gitlab alias, got %s", string(got))
	}
}
