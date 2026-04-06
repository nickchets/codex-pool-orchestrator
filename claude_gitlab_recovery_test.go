package main

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func newGitLabClaudeRecoveryTestAccount(t *testing.T, dir, id, sourceToken, gatewayToken string, extraHeaders map[string]string) *Account {
	t.Helper()

	acc := &Account{
		ID:              id,
		Type:            AccountTypeClaude,
		File:            filepath.Join(dir, id+".json"),
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    sourceToken,
		AccessToken:     gatewayToken,
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    copyStringMap(extraHeaders),
		ExpiresAt:       time.Now().UTC().Add(time.Hour),
		LastRefresh:     time.Now().UTC(),
		HealthStatus:    "healthy",
	}
	if err := saveGitLabClaudeAccountFile(acc, true); err != nil {
		t.Fatalf("save test gitlab claude account: %v", err)
	}
	return acc
}

func TestPropagateManagedGitLabClaudeSharedTPMCooldownSchedulesCanary(t *testing.T) {
	now := time.Now().UTC()
	headers := map[string]string{
		"X-Gitlab-Instance-Id":                      "inst-1",
		"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "100,200",
	}
	trigger := &Account{
		ID:              "claude_gitlab_trigger",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-trigger",
		RefreshToken:    "glpat-trigger",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    copyStringMap(headers),
	}
	sibling := &Account{
		ID:              "claude_gitlab_sibling",
		Type:            AccountTypeClaude,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		AccessToken:     "gateway-sibling",
		RefreshToken:    "glpat-sibling",
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
		ExtraHeaders:    copyStringMap(headers),
	}
	h := &proxyHandler{pool: newPoolState([]*Account{trigger, sibling}, false)}
	disposition := managedGitLabClaudeErrorDisposition{
		RateLimit:    true,
		SharedOrgTPM: true,
		HealthStatus: "rate_limited",
		Cooldown:     managedGitLabClaudeOrgTPMRateLimitWait,
		Reason:       "This request would exceed your organization's rate limit of 18,000,000 input tokens per minute",
	}

	if !h.propagateManagedGitLabClaudeSharedTPMCooldown("req-test", trigger, disposition, http.Header{}, "claude-opus-4-6", now) {
		t.Fatal("expected propagation to schedule shared cooldown canaries")
	}

	for _, acc := range []*Account{trigger, sibling} {
		acc.mu.Lock()
		if acc.GitLabCanaryModel != "claude-opus-4-6" {
			t.Fatalf("%s canary model = %q", acc.ID, acc.GitLabCanaryModel)
		}
		if acc.GitLabCanaryNextProbeAt.IsZero() {
			t.Fatalf("%s expected canary probe schedule", acc.ID)
		}
		if acc.GitLabCanaryNextProbeAt.After(acc.RateLimitUntil) {
			t.Fatalf("%s next probe %v after cooldown %v", acc.ID, acc.GitLabCanaryNextProbeAt, acc.RateLimitUntil)
		}
		if acc.GitLabCanaryLastResult != "scheduled" {
			t.Fatalf("%s last result = %q", acc.ID, acc.GitLabCanaryLastResult)
		}
		acc.mu.Unlock()
	}
}

func TestRecoverDueManagedGitLabClaudeSharedTPMScopesClearsScopeOnSuccess(t *testing.T) {
	now := time.Now().UTC()
	tmp := t.TempDir()
	headers := map[string]string{
		"X-Gitlab-Instance-Id":                      "inst-1",
		"X-Gitlab-Feature-Enabled-By-Namespace-Ids": "100,200",
	}
	first := newGitLabClaudeRecoveryTestAccount(t, tmp, "claude_gitlab_scope_one", "glpat-1", "gateway-1", headers)
	second := newGitLabClaudeRecoveryTestAccount(t, tmp, "claude_gitlab_scope_two", "glpat-2", "gateway-2", headers)

	for _, acc := range []*Account{first, second} {
		acc.mu.Lock()
		acc.HealthStatus = "rate_limited"
		acc.HealthError = managedGitLabClaudeSharedOrgTPMHealthError("This request would exceed your organization's rate limit of 18,000,000 input tokens per minute")
		acc.RateLimitUntil = now.Add(45 * time.Second)
		acc.GitLabCanaryModel = "claude-opus-4-6"
		acc.GitLabCanaryNextProbeAt = now.Add(-time.Second)
		acc.mu.Unlock()
	}

	var seenRequests int
	h := &proxyHandler{
		pool: newPoolState([]*Account{first, second}, false),
		transport: gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seenRequests++
			if req.URL.Path != "/ai/v1/proxy/anthropic/v1/messages" && req.URL.Path != "/v1/messages" {
				t.Fatalf("unexpected canary path %q", req.URL.Path)
			}
			return gitlabClaudeJSONResponse(http.StatusOK, `{"id":"msg_test","type":"message","role":"assistant","content":[]}`), nil
		}),
	}

	h.recoverDueManagedGitLabClaudeSharedTPMScopes("test")
	if seenRequests != 1 {
		t.Fatalf("seenRequests=%d", seenRequests)
	}

	for _, acc := range []*Account{first, second} {
		acc.mu.Lock()
		if !acc.RateLimitUntil.IsZero() {
			t.Fatalf("%s rate_limit_until=%v", acc.ID, acc.RateLimitUntil)
		}
		if acc.HealthStatus != "healthy" {
			t.Fatalf("%s health_status=%q", acc.ID, acc.HealthStatus)
		}
		if acc.HealthError != "" {
			t.Fatalf("%s health_error=%q", acc.ID, acc.HealthError)
		}
		if !acc.GitLabCanaryNextProbeAt.IsZero() {
			t.Fatalf("%s next_probe=%v", acc.ID, acc.GitLabCanaryNextProbeAt)
		}
		if acc.GitLabCanaryLastResult != "success" {
			t.Fatalf("%s last_result=%q", acc.ID, acc.GitLabCanaryLastResult)
		}
		if acc.GitLabCanaryLastSuccessAt.IsZero() {
			t.Fatalf("%s expected canary success timestamp", acc.ID)
		}
		acc.mu.Unlock()
	}
}

func TestBuildPoolDashboardDataShowsGitLabClaudeCanaryRecoveryState(t *testing.T) {
	now := time.Date(2026, 3, 30, 18, 0, 0, 0, time.UTC)
	acc := &Account{
		ID:                      "claude_gitlab_canary_status",
		Type:                    AccountTypeClaude,
		PlanType:                "gitlab_duo",
		AuthMode:                accountAuthModeGitLab,
		AccessToken:             "gateway-status",
		RefreshToken:            "glpat-status",
		SourceBaseURL:           defaultGitLabInstanceURL,
		UpstreamBaseURL:         defaultGitLabClaudeGatewayURL,
		ExtraHeaders:            map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		HealthStatus:            "rate_limited",
		HealthError:             managedGitLabClaudeSharedOrgTPMHealthError("This request would exceed your organization's rate limit of 18,000,000 input tokens per minute"),
		RateLimitUntil:          now.Add(40 * time.Second),
		GitLabCanaryModel:       "claude-opus-4-6",
		GitLabCanaryNextProbeAt: now.Add(5 * time.Second),
		GitLabCanaryLastResult:  "scheduled",
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{acc}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	status := data.Accounts[0]
	if status.GitLabCanaryModel != "claude-opus-4-6" {
		t.Fatalf("gitlab_canary_model=%q", status.GitLabCanaryModel)
	}
	if status.GitLabCanaryProbeIn == "" {
		t.Fatalf("gitlab_canary_probe_in=%q", status.GitLabCanaryProbeIn)
	}
	if status.GitLabCanaryLastResult != "scheduled" {
		t.Fatalf("gitlab_canary_last_result=%q", status.GitLabCanaryLastResult)
	}
}
