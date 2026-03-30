package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	  "gitlab_rate_limit_name": "throttle_authenticated_api",
	  "gitlab_rate_limit_limit": 2000,
	  "gitlab_rate_limit_remaining": 1999,
	  "gitlab_rate_limit_reset_at": "2026-03-22T20:05:00Z",
	  "gitlab_quota_exceeded_count": 2,
	  "gitlab_last_quota_exceeded_at": "2026-03-22T19:00:00Z",
	  "rate_limit_until": "2026-03-22T20:10:00Z",
	  "last_refresh": "2026-03-22T20:09:00Z",
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
	if acc.GitLabRateLimitName != "throttle_authenticated_api" {
		t.Fatalf("gitlab_rate_limit_name=%q", acc.GitLabRateLimitName)
	}
	if acc.GitLabRateLimitLimit != 2000 || acc.GitLabRateLimitRemaining != 1999 {
		t.Fatalf("gitlab rate limit=%d/%d", acc.GitLabRateLimitRemaining, acc.GitLabRateLimitLimit)
	}
	if acc.GitLabRateLimitResetAt.IsZero() {
		t.Fatal("expected gitlab rate limit reset to be loaded")
	}
	if acc.GitLabQuotaExceededCount != 2 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", acc.GitLabQuotaExceededCount)
	}
	if acc.GitLabLastQuotaExceededAt.IsZero() {
		t.Fatal("expected gitlab_last_quota_exceeded_at to be loaded")
	}
	if acc.RateLimitUntil.IsZero() {
		t.Fatal("expected rate_limit_until to be loaded")
	}
	if acc.LastRefresh.IsZero() {
		t.Fatal("expected last_refresh to be loaded")
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
		return gitlabClaudeJSONResponseWithHeaders(http.StatusOK, `{
			"token": "gateway-token",
			"base_url": "https://cloud.gitlab.com/ai/v1/proxy/anthropic",
			"expires_at": 1911111111,
			"headers": {
				"X-Gitlab-Instance-Id": "inst-1",
				"X-Gitlab-Realm": "saas"
			}
		}`, http.Header{
			"RateLimit-Name":      []string{"throttle_authenticated_api"},
			"RateLimit-Limit":     []string{"2000"},
			"RateLimit-Remaining": []string{"1999"},
			"RateLimit-Reset":     []string{"1911112222"},
		}), nil
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
	if acc.GitLabRateLimitName != "throttle_authenticated_api" {
		t.Fatalf("gitlab_rate_limit_name=%q", acc.GitLabRateLimitName)
	}
	if acc.GitLabRateLimitLimit != 2000 || acc.GitLabRateLimitRemaining != 1999 {
		t.Fatalf("gitlab rate limit=%d/%d", acc.GitLabRateLimitRemaining, acc.GitLabRateLimitLimit)
	}
	if acc.GitLabRateLimitResetAt.Unix() != 1911112222 {
		t.Fatalf("gitlab_rate_limit_reset_at=%v", acc.GitLabRateLimitResetAt)
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
	disposition := classifyManagedGitLabClaudeError(managedGitLabClaudeErrorSourceGatewayRequest, http.StatusPaymentRequired, http.Header{}, []byte(`{"message":"USAGE_QUOTA_EXCEEDED"}`))
	if disposition.RateLimit {
		t.Fatalf("did not expect rate limit classification, got %+v", disposition)
	}
	if !disposition.MarkDead {
		t.Fatalf("expected dead classification, got %+v", disposition)
	}
	if disposition.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", disposition.HealthStatus)
	}
}

func TestClassifyManagedGitLabClaudeGatewayForbiddenDoesNotMarkDead(t *testing.T) {
	disposition := classifyManagedGitLabClaudeError(managedGitLabClaudeErrorSourceGatewayRequest, http.StatusForbidden, http.Header{}, []byte(`<html>forbidden</html>`))
	if !disposition.RateLimit {
		t.Fatalf("expected temporary cooldown classification, got %+v", disposition)
	}
	if disposition.MarkDead {
		t.Fatalf("did not expect dead classification, got %+v", disposition)
	}
	if disposition.HealthStatus != "gateway_rejected" {
		t.Fatalf("health_status=%q", disposition.HealthStatus)
	}
}

func TestClassifyManagedGitLabClaudeDirectAccessForbiddenMarksDead(t *testing.T) {
	disposition := classifyManagedGitLabClaudeError(managedGitLabClaudeErrorSourceDirectAccess, http.StatusForbidden, http.Header{}, []byte(`{"message":"forbidden"}`))
	if disposition.RateLimit {
		t.Fatalf("did not expect rate limit classification, got %+v", disposition)
	}
	if !disposition.MarkDead {
		t.Fatalf("expected dead classification, got %+v", disposition)
	}
}

func TestClassifyManagedGitLabClaudeErrorOrgTPMUsesSharedShortCooldown(t *testing.T) {
	disposition := classifyManagedGitLabClaudeError(
		managedGitLabClaudeErrorSourceGatewayRequest,
		http.StatusTooManyRequests,
		http.Header{},
		[]byte(`{"error":{"message":"This request would exceed your organization's rate limit of 18,000,000 input tokens per minute"}}`),
	)
	if !disposition.RateLimit {
		t.Fatalf("expected rate limit classification, got %+v", disposition)
	}
	if disposition.MarkDead {
		t.Fatalf("did not expect dead classification, got %+v", disposition)
	}
	if !disposition.SharedOrgTPM {
		t.Fatalf("expected shared org TPM classification, got %+v", disposition)
	}
	if disposition.Cooldown != managedGitLabClaudeOrgTPMRateLimitWait {
		t.Fatalf("cooldown=%v", disposition.Cooldown)
	}
}

func TestGitLabClaudeQuotaExceededCooldownExpandsExponentially(t *testing.T) {
	if got := gitLabClaudeQuotaExceededCooldown(1); got != 30*time.Minute {
		t.Fatalf("count1=%v", got)
	}
	if got := gitLabClaudeQuotaExceededCooldown(2); got != time.Hour {
		t.Fatalf("count2=%v", got)
	}
	if got := gitLabClaudeQuotaExceededCooldown(3); got != 2*time.Hour {
		t.Fatalf("count3=%v", got)
	}
	if got := gitLabClaudeQuotaExceededCooldown(10); got != 24*time.Hour {
		t.Fatalf("count10=%v", got)
	}
}

func TestApplyManagedGitLabClaudeDispositionParsesRetryAfterHTTPDate(t *testing.T) {
	acc := &Account{
		ID:           "claude_gitlab_retry_after",
		Type:         AccountTypeClaude,
		AuthMode:     accountAuthModeGitLab,
		AccessToken:  "gateway-token",
		ExtraHeaders: map[string]string{"X-Test": "retry-after"},
	}
	now := time.Now().UTC()
	headers := http.Header{
		"Retry-After": []string{now.Add(90 * time.Second).Format(http.TimeFormat)},
	}
	disposition := managedGitLabClaudeErrorDisposition{
		RateLimit:    true,
		HealthStatus: "rate_limited",
		Cooldown:     managedGitLabClaudeRateLimitWait,
		Reason:       "rate limit",
	}

	applyManagedGitLabClaudeDisposition(acc, disposition, headers, now)

	if acc.RateLimitUntil.IsZero() {
		t.Fatal("expected rate limit until")
	}
	wait := acc.RateLimitUntil.Sub(now)
	if wait < 89*time.Second || wait > 91*time.Second {
		t.Fatalf("rate_limit_until=%v wait=%v", acc.RateLimitUntil, wait)
	}
}

func TestApplyManagedGitLabClaudeDispositionOrgTPMUsesShortFallbackWithoutRetryAfter(t *testing.T) {
	acc := &Account{
		ID:           "claude_gitlab_org_tpm",
		Type:         AccountTypeClaude,
		AuthMode:     accountAuthModeGitLab,
		AccessToken:  "gateway-token",
		ExtraHeaders: map[string]string{"X-Test": "org-tpm"},
	}
	now := time.Now().UTC()
	disposition := managedGitLabClaudeErrorDisposition{
		RateLimit:    true,
		SharedOrgTPM: true,
		HealthStatus: "rate_limited",
		Cooldown:     managedGitLabClaudeOrgTPMRateLimitWait,
		Reason:       "This request would exceed your organization's rate limit of 18,000,000 input tokens per minute",
	}

	applyManagedGitLabClaudeDisposition(acc, disposition, http.Header{}, now)

	if acc.RateLimitUntil.IsZero() {
		t.Fatal("expected rate limit until")
	}
	wait := acc.RateLimitUntil.Sub(now)
	if wait < 74*time.Second || wait > 76*time.Second {
		t.Fatalf("rate_limit_until=%v wait=%v", acc.RateLimitUntil, wait)
	}
}

func TestGitLabClaudeScopeKeyPrefersEntitlementHeadersOverInstanceOnly(t *testing.T) {
	base := &Account{
		Type:            AccountTypeClaude,
		AuthMode:        accountAuthModeGitLab,
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders: map[string]string{
			"X-Gitlab-Instance-Id":                  "inst-1",
			"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "100,200",
			"X-Gitlab-User-Id":                      "42",
		},
	}
	sameEntitlement := &Account{
		Type:            AccountTypeClaude,
		AuthMode:        accountAuthModeGitLab,
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders: map[string]string{
			"X-Gitlab-Instance-Id":                  "inst-1",
			"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "100,200",
			"X-Gitlab-User-Id":                      "77",
		},
	}
	differentEntitlement := &Account{
		Type:            AccountTypeClaude,
		AuthMode:        accountAuthModeGitLab,
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders: map[string]string{
			"X-Gitlab-Instance-Id":                  "inst-1",
			"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "300",
			"X-Gitlab-User-Id":                      "42",
		},
	}

	if got := gitLabClaudeScopeKey(base); got == "" {
		t.Fatal("expected non-empty scope key")
	}
	if got, want := gitLabClaudeScopeKey(sameEntitlement), gitLabClaudeScopeKey(base); got != want {
		t.Fatalf("same entitlement scope mismatch: got %q want %q", got, want)
	}
	if got, want := gitLabClaudeScopeKey(differentEntitlement), gitLabClaudeScopeKey(base); got == want {
		t.Fatalf("expected different entitlement scope, got %q", got)
	}
}

func TestClaudeProviderLoadsLegacyQuotaExceededAccountWithDefaultBackoffCount(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)

	payload := []byte(`{
	  "plan_type": "gitlab_duo",
	  "auth_mode": "gitlab_duo",
	  "gitlab_token": "glpat-source",
	  "gitlab_gateway_token": "gateway-token",
	  "gitlab_gateway_headers": {
	    "X-Gitlab-Instance-Id": "inst-1"
	  },
	  "rate_limit_until": "2026-03-22T20:10:00Z",
	  "health_status": "quota_exceeded"
	}`)

	acc, err := provider.LoadAccount("claude_gitlab_deadbeef.json", "/tmp/claude_gitlab_deadbeef.json", payload)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account")
	}
	if acc.GitLabQuotaExceededCount != 1 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", acc.GitLabQuotaExceededCount)
	}
	if !acc.Dead {
		t.Fatal("expected legacy quota-exceeded account to load dead")
	}
	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", acc.RateLimitUntil)
	}
}

func TestSaveGitLabClaudeAccountFailsClosedOnMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_gitlab_deadbeef.json")
	if err := os.WriteFile(path, []byte(`{"plan_type":`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	acc := &Account{
		ID:              "claude_gitlab_deadbeef",
		Type:            AccountTypeClaude,
		File:            path,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    "glpat-source",
		SourceBaseURL:   "https://gitlab.example.com",
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		HealthStatus:    "unknown",
	}

	if err := saveGitLabClaudeAccount(acc); err == nil {
		t.Fatal("expected parse error")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(raw) != `{"plan_type":` {
		t.Fatalf("expected malformed file to remain unchanged, got %q", string(raw))
	}
}

func TestSaveGitLabClaudeAccountRoundTripsGitLabFields(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)

	path := filepath.Join(t.TempDir(), "claude_gitlab_deadbeef.json")
	original := map[string]any{
		"plan_type":           "gitlab_duo",
		"auth_mode":           accountAuthModeGitLab,
		"gitlab_token":        "glpat-source",
		"gitlab_instance_url": "https://gitlab.example.com",
		"extra_top":           "preserve-me",
	}
	buf, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal original: %v", err)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	checkedAt := time.Date(2026, 3, 23, 11, 0, 0, 0, time.UTC)
	lastHealthyAt := checkedAt.Add(-5 * time.Minute)
	lastRefresh := checkedAt.Add(-2 * time.Minute)
	acc := &Account{
		ID:                        "claude_gitlab_deadbeef",
		Type:                      AccountTypeClaude,
		File:                      path,
		PlanType:                  "gitlab_duo",
		AuthMode:                  accountAuthModeGitLab,
		RefreshToken:              "glpat-source",
		AccessToken:               "gateway-token",
		SourceBaseURL:             "https://gitlab.example.com",
		UpstreamBaseURL:           defaultGitLabClaudeGatewayURL,
		ExtraHeaders:              map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		ExpiresAt:                 checkedAt.Add(20 * time.Minute),
		LastRefresh:               lastRefresh,
		RateLimitUntil:            checkedAt.Add(40 * time.Minute),
		HealthStatus:              "quota_exceeded",
		HealthError:               "quota",
		HealthCheckedAt:           checkedAt,
		LastHealthyAt:             lastHealthyAt,
		GitLabRateLimitName:       "throttle_authenticated_api",
		GitLabRateLimitLimit:      2000,
		GitLabRateLimitRemaining:  0,
		GitLabRateLimitResetAt:    checkedAt.Add(15 * time.Minute),
		GitLabQuotaExceededCount:  3,
		GitLabLastQuotaExceededAt: checkedAt.Add(-30 * time.Minute),
	}

	if err := saveGitLabClaudeAccount(acc); err != nil {
		t.Fatalf("saveGitLabClaudeAccount: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("unmarshal saved: %v", err)
	}
	if saved["extra_top"] != "preserve-me" {
		t.Fatalf("expected unknown field preserved, got %#v", saved["extra_top"])
	}
	if saved["gitlab_quota_exceeded_count"] != float64(3) {
		t.Fatalf("gitlab_quota_exceeded_count=%v", saved["gitlab_quota_exceeded_count"])
	}
	if saved["gitlab_rate_limit_remaining"] != float64(0) {
		t.Fatalf("gitlab_rate_limit_remaining=%v", saved["gitlab_rate_limit_remaining"])
	}
	if _, ok := saved["last_refresh"]; !ok {
		t.Fatal("expected last_refresh to be persisted")
	}

	loaded, err := provider.LoadAccount("claude_gitlab_deadbeef.json", path, raw)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if loaded.GitLabQuotaExceededCount != 3 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", loaded.GitLabQuotaExceededCount)
	}
	if loaded.GitLabRateLimitLimit != 2000 || loaded.GitLabRateLimitRemaining != 0 {
		t.Fatalf("gitlab rate limit=%d/%d", loaded.GitLabRateLimitRemaining, loaded.GitLabRateLimitLimit)
	}
	if loaded.LastRefresh.IsZero() {
		t.Fatal("expected last_refresh round-trip")
	}
}

func TestRefreshGitLabClaudeAccessMalformed2xxMarksErrorAndClearsGatewayState(t *testing.T) {
	acc := &Account{
		Type:            AccountTypeClaude,
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    "glpat-source",
		AccessToken:     "stale-token",
		SourceBaseURL:   "https://gitlab.example.com",
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		ExpiresAt:       time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC),
		HealthStatus:    "healthy",
	}

	transport := gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return gitlabClaudeJSONResponse(http.StatusOK, `{"token":"gateway-token"}`), nil
	})

	err := refreshGitLabClaudeAccess(context.Background(), acc, transport)
	if err == nil {
		t.Fatal("expected error")
	}
	if acc.HealthStatus != "error" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError == "" {
		t.Fatal("expected health_error")
	}
	if acc.AccessToken != "" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
	if len(acc.ExtraHeaders) != 0 {
		t.Fatalf("extra_headers=%v", acc.ExtraHeaders)
	}
	if !acc.ExpiresAt.IsZero() {
		t.Fatalf("expires_at=%v", acc.ExpiresAt)
	}
	if acc.HealthCheckedAt.IsZero() {
		t.Fatal("expected health_checked_at")
	}
}

type gitlabClaudeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f gitlabClaudeRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func gitlabClaudeJSONResponse(statusCode int, body string) *http.Response {
	return gitlabClaudeJSONResponseWithHeaders(statusCode, body, nil)
}

func gitlabClaudeJSONResponseWithHeaders(statusCode int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
