package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestClaudeProviderLoadsGitLabManagedAccount(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)

	payload := []byte(`{
	  "plan_type": "gitlab_duo",
	  "auth_mode": "gitlab_duo",
	  "gitlab_token": "glpat-source",
	  "gitlab_instance_url": "https://gitlab.example.com",
	  "gitlab_gateway_token": "gateway-token",
	  "gitlab_gateway_base_url": "https://cloud.gitlab.com/ai/v1/proxy/anthropic",
	  "gitlab_gateway_headers": {
	    "X-Gitlab-Instance-Id": "inst-1",
	    "X-Gitlab-Realm": "saas"
	  },
	  "gitlab_gateway_expires_at": "2026-03-22T20:00:00Z",
	  "health_status": "healthy"
	}`)

	acc, err := provider.LoadAccount("claude_gitlab_deadbeef.json", "/tmp/claude_gitlab_deadbeef.json", payload)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account")
	}
	if acc.AuthMode != accountAuthModeGitLab {
		t.Fatalf("auth_mode=%q", acc.AuthMode)
	}
	if acc.RefreshToken != "glpat-source" {
		t.Fatalf("refresh_token=%q", acc.RefreshToken)
	}
	if acc.AccessToken != "gateway-token" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
	if acc.SourceBaseURL != "https://gitlab.example.com" {
		t.Fatalf("source_base_url=%q", acc.SourceBaseURL)
	}
	if acc.UpstreamBaseURL != "https://cloud.gitlab.com/ai/v1/proxy/anthropic" {
		t.Fatalf("upstream_base_url=%q", acc.UpstreamBaseURL)
	}
	if acc.ExtraHeaders["X-Gitlab-Instance-Id"] != "inst-1" {
		t.Fatalf("extra_headers=%v", acc.ExtraHeaders)
	}
	if acc.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.ExpiresAt.IsZero() {
		t.Fatal("expected gateway expiry to be loaded")
	}
}

func TestClaudeProviderSetAuthHeadersForGitLabManagedAccount(t *testing.T) {
	provider := &ClaudeProvider{}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	acc := &Account{
		Type:         AccountTypeClaude,
		AuthMode:     accountAuthModeGitLab,
		AccessToken:  "gateway-token",
		ExtraHeaders: map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
	}
	provider.SetAuthHeaders(req, acc)

	if got := req.Header.Get("Authorization"); got != "Bearer gateway-token" {
		t.Fatalf("authorization=%q", got)
	}
	if got := req.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
		t.Fatalf("x-gitlab-instance-id=%q", got)
	}
}

func TestClaudeProviderRefreshGitLabManagedAccount(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)

	var gotPath string
	var gotAuth string
	transport := gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotPath = req.URL.String()
		gotAuth = req.Header.Get("Authorization")
		return gitlabClaudeJSONResponse(http.StatusOK, `{
			"token": "gateway-token",
			"base_url": "https://cloud.gitlab.com/ai/v1/proxy/anthropic",
			"expires_at": 1911111111,
			"headers": {
				"X-Gitlab-Instance-Id": "inst-1",
				"X-Gitlab-Realm": "saas"
			}
		}`), nil
	})

	acc := &Account{
		Type:            AccountTypeClaude,
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    "glpat-source",
		SourceBaseURL:   "https://gitlab.example.com",
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
	}

	if err := provider.RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if gotPath != "https://gitlab.example.com/api/v4/ai/third_party_agents/direct_access" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Bearer glpat-source" {
		t.Fatalf("authorization=%q", gotAuth)
	}
	if acc.AccessToken != "gateway-token" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
	if acc.UpstreamBaseURL != "https://cloud.gitlab.com/ai/v1/proxy/anthropic" {
		t.Fatalf("upstream_base_url=%q", acc.UpstreamBaseURL)
	}
	if acc.ExtraHeaders["X-Gitlab-Realm"] != "saas" {
		t.Fatalf("extra_headers=%v", acc.ExtraHeaders)
	}
	if acc.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.ExpiresAt.IsZero() {
		t.Fatal("expected expires_at to be set")
	}
}

func TestProviderUpstreamURLForGitLabClaudeAccount(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)
	acc := &Account{
		Type:            AccountTypeClaude,
		AuthMode:        accountAuthModeGitLab,
		UpstreamBaseURL: "https://cloud.gitlab.com/ai/v1/proxy/anthropic",
	}

	got := providerUpstreamURLForAccount(provider, "/v1/messages", acc)
	if got == nil || got.String() != "https://cloud.gitlab.com/ai/v1/proxy/anthropic" {
		t.Fatalf("upstream=%v", got)
	}
}

func TestNeedsRefreshWhenGitLabClaudeGatewayStateMissing(t *testing.T) {
	h := &proxyHandler{}
	acc := &Account{
		Type:         AccountTypeClaude,
		AuthMode:     accountAuthModeGitLab,
		RefreshToken: "glpat-source",
	}

	if !h.needsRefresh(acc) {
		t.Fatal("expected gitlab claude account with missing gateway token to require refresh")
	}
}

func TestClassifyManagedGitLabClaudeErrorQuotaExceeded(t *testing.T) {
	disposition := classifyManagedGitLabClaudeError(http.StatusForbidden, http.Header{}, []byte(`{"message":"USAGE_QUOTA_EXCEEDED"}`))
	if !disposition.RateLimit {
		t.Fatalf("expected rate limit classification, got %+v", disposition)
	}
	if disposition.MarkDead {
		t.Fatalf("did not expect dead classification, got %+v", disposition)
	}
}

type gitlabClaudeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f gitlabClaudeRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func gitlabClaudeJSONResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
