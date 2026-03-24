package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func stubCodexLoopbackEnsure(t *testing.T) {
	t.Helper()
	previous := ensureCodexLoopbackCallbackServersForOperator
	ensureCodexLoopbackCallbackServersForOperator = func(h *proxyHandler) error { return nil }
	t.Cleanup(func() {
		ensureCodexLoopbackCallbackServersForOperator = previous
	})
}

func resetManagedGeminiOAuthSessions() {
	managedGeminiOAuthSessions.Lock()
	managedGeminiOAuthSessions.sessions = make(map[string]*managedGeminiOAuthSession)
	managedGeminiOAuthSessions.Unlock()
}

func testCodexIDToken(t *testing.T, userID, accountID, email, subject string, exp time.Time) string {
	t.Helper()
	payload := map[string]any{
		"exp": exp.Unix(),
		"sub": subject,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_user_id":    userID,
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  "team",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": email,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal id token payload: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func TestBuildPoolDashboardDataGroupsWorkspaceSeats(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	blocked := &Account{
		ID:        "blocked",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.95,
			SecondaryResetAt:     now.Add(90 * time.Minute),
		},
	}
	healthySibling := &Account{
		ID:        "healthy-sibling",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-b", "workspace-a", "b@example.com", "sub-b", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.12,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthyOther := &Account{
		ID:        "healthy-other",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-b",
		IDToken:   testCodexIDToken(t, "user-c", "workspace-b", "c@example.com", "sub-c", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.30,
			SecondaryUsedPercent: 0.25,
			SecondaryResetAt:     now.Add(48 * time.Hour),
		},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{blocked, healthySibling, healthyOther}, false),
		startTime: now.Add(-2 * time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.PoolSummary.TotalAccounts != 3 {
		t.Fatalf("total_accounts=%d", data.PoolSummary.TotalAccounts)
	}
	if data.PoolSummary.EligibleAccounts != 2 {
		t.Fatalf("eligible_accounts=%d", data.PoolSummary.EligibleAccounts)
	}
	if data.PoolSummary.WorkspaceCount != 2 {
		t.Fatalf("workspace_count=%d", data.PoolSummary.WorkspaceCount)
	}
	if data.PoolSummary.NextRecoveryAt == "" {
		t.Fatal("expected next_recovery_at to be populated")
	}

	var workspaceA *PoolDashboardWorkspaceGroup
	for i := range data.WorkspaceGroups {
		if data.WorkspaceGroups[i].WorkspaceID == "workspace-a" {
			workspaceA = &data.WorkspaceGroups[i]
			break
		}
	}
	if workspaceA == nil {
		t.Fatal("workspace-a group missing")
	}
	if workspaceA.SeatCount != 2 || workspaceA.EligibleSeatCount != 1 || workspaceA.BlockedSeatCount != 1 {
		t.Fatalf("workspace-a counts=%+v", *workspaceA)
	}
	if len(workspaceA.SeatKeys) != 2 {
		t.Fatalf("expected 2 seat keys, got %v", workspaceA.SeatKeys)
	}

	blockedAccount := data.Accounts[0]
	if blockedAccount.ID != "blocked" {
		t.Fatalf("expected blocked account to sort first, got %s", blockedAccount.ID)
	}
	if blockedAccount.Routing.BlockReason != "secondary_headroom_lt_10" {
		t.Fatalf("block_reason=%q", blockedAccount.Routing.BlockReason)
	}
	if blockedAccount.WorkspaceID != "workspace-a" {
		t.Fatalf("workspace_id=%q", blockedAccount.WorkspaceID)
	}
	if !strings.Contains(blockedAccount.SeatKey, "workspace-a") {
		t.Fatalf("seat_key=%q", blockedAccount.SeatKey)
	}
}

func TestBuildPoolDashboardDataTracksOpenAIAPIPool(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	seat := &Account{
		ID:        "healthy-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	apiKey := &Account{
		ID:              "openai_api_deadbeef",
		Type:            AccountTypeCodex,
		PlanType:        "api",
		AuthMode:        accountAuthModeAPIKey,
		HealthStatus:    "healthy",
		HealthCheckedAt: now.Add(-2 * time.Minute),
		LastHealthyAt:   now.Add(-2 * time.Minute),
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat, apiKey}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.CodexSeatCount != 1 {
		t.Fatalf("codex_seat_count=%d", data.CodexSeatCount)
	}
	if data.OpenAIAPIPool.TotalKeys != 1 {
		t.Fatalf("api total=%d", data.OpenAIAPIPool.TotalKeys)
	}
	if data.OpenAIAPIPool.HealthyKeys != 1 {
		t.Fatalf("api healthy=%d", data.OpenAIAPIPool.HealthyKeys)
	}
	if data.OpenAIAPIPool.EligibleKeys != 1 {
		t.Fatalf("api eligible=%d", data.OpenAIAPIPool.EligibleKeys)
	}
	if data.OpenAIAPIPool.NextKeyID != "openai_api_deadbeef" {
		t.Fatalf("next api key=%q", data.OpenAIAPIPool.NextKeyID)
	}
	if len(data.WorkspaceGroups) != 1 {
		t.Fatalf("workspace groups should exclude api keys, got %d", len(data.WorkspaceGroups))
	}
	if !data.Accounts[1].FallbackOnly {
		t.Fatalf("expected fallback_only account status, got %+v", data.Accounts[1])
	}
}

func TestBuildPoolDashboardDataShowsGitLabDirectAccessSignals(t *testing.T) {
	now := time.Date(2026, 3, 23, 6, 45, 0, 0, time.UTC)
	gitlabClaude := &Account{
		ID:                       "claude_gitlab_deadbeef",
		Type:                     AccountTypeClaude,
		PlanType:                 "gitlab_duo",
		AuthMode:                 accountAuthModeGitLab,
		HealthStatus:             "quota_exceeded",
		HealthError:              "Consumer does not have sufficient credits",
		LastRefresh:              now.Add(-2 * time.Minute),
		ExpiresAt:                now.Add(18 * time.Minute),
		GitLabRateLimitName:      "throttle_authenticated_api",
		GitLabRateLimitLimit:     2000,
		GitLabRateLimitRemaining: 1999,
		GitLabRateLimitResetAt:   now.Add(20 * time.Minute),
		GitLabQuotaExceededCount: 3,
		RateLimitUntil:           now.Add(4 * time.Hour),
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{gitlabClaude}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	status := data.Accounts[0]
	if status.GitLabRateLimitName != "throttle_authenticated_api" {
		t.Fatalf("gitlab_rate_limit_name=%q", status.GitLabRateLimitName)
	}
	if status.GitLabRateLimitLimit != 2000 || status.GitLabRateLimitRemaining != 1999 {
		t.Fatalf("gitlab rate limit=%d/%d", status.GitLabRateLimitRemaining, status.GitLabRateLimitLimit)
	}
	if status.GitLabRateLimitResetIn == "" {
		t.Fatalf("gitlab_rate_limit_reset_in=%q", status.GitLabRateLimitResetIn)
	}
	if status.UsageObserved != "local totals only · GitLab quota hidden" {
		t.Fatalf("usage_observed=%q", status.UsageObserved)
	}
	if status.GitLabQuotaExceededCount != 3 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", status.GitLabQuotaExceededCount)
	}
	if status.GitLabQuotaProbeIn == "" {
		t.Fatalf("gitlab_quota_probe_in=%q", status.GitLabQuotaProbeIn)
	}
	if status.HealthStatus != "quota_exceeded" {
		t.Fatalf("health_status=%q", status.HealthStatus)
	}
}

func TestBuildPoolDashboardDataSeparatesGeminiOperatorLanes(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	now := time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC)
	managed := &Account{
		ID:             "gemini_managed",
		Type:           AccountTypeGemini,
		PlanType:       "gemini",
		AuthMode:       accountAuthModeOAuth,
		OAuthProfileID: "gcloud",
		OperatorSource: geminiOperatorSourceManagedOAuth,
	}
	imported := &Account{
		ID:             "gemini_imported",
		Type:           AccountTypeGemini,
		PlanType:       "gemini",
		AuthMode:       accountAuthModeOAuth,
		OperatorSource: geminiOperatorSourceManualImport,
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{managed, imported}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.GeminiOperator.ManagedSeatCount != 1 {
		t.Fatalf("managed_seat_count=%d", data.GeminiOperator.ManagedSeatCount)
	}
	if data.GeminiOperator.ImportedSeatCount != 1 {
		t.Fatalf("imported_seat_count=%d", data.GeminiOperator.ImportedSeatCount)
	}
	if !data.GeminiOperator.ManagedOAuthAvailable {
		t.Fatalf("expected managed_oauth_available=true")
	}
	if data.GeminiOperator.ManagedOAuthProfile != "gcloud" {
		t.Fatalf("managed_oauth_profile=%q", data.GeminiOperator.ManagedOAuthProfile)
	}
	if len(data.Accounts) != 2 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].OperatorSource == "" || data.Accounts[1].OperatorSource == "" {
		t.Fatalf("operator sources missing: %+v", data.Accounts)
	}
}

func TestBuildPoolDashboardDataBlocksGitLabTokensMissingGatewayState(t *testing.T) {
	now := time.Date(2026, 3, 23, 6, 45, 0, 0, time.UTC)
	gitlabClaude := &Account{
		ID:           "claude_gitlab_deadbeef",
		Type:         AccountTypeClaude,
		PlanType:     "gitlab_duo",
		AuthMode:     accountAuthModeGitLab,
		HealthStatus: "unknown",
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{gitlabClaude}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.GitLabClaudePool.EligibleTokens != 0 {
		t.Fatalf("eligible_tokens=%d", data.GitLabClaudePool.EligibleTokens)
	}
	if data.GitLabClaudePool.NextTokenID != "" {
		t.Fatalf("next_token_id=%q", data.GitLabClaudePool.NextTokenID)
	}
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].Routing.Eligible {
		t.Fatal("expected token to be blocked")
	}
	if data.Accounts[0].Routing.BlockReason != "missing_gateway_state" {
		t.Fatalf("block_reason=%q", data.Accounts[0].Routing.BlockReason)
	}
}

func TestBuildPoolDashboardDataSelectsCurrentSeatFromInflightAndLastUsed(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	current := &Account{
		ID:        "current-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Inflight:  2,
		LastUsed:  now.Add(-15 * time.Second),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.30,
			SecondaryUsedPercent: 0.20,
		},
	}
	older := &Account{
		ID:        "older-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-b",
		IDToken:   testCodexIDToken(t, "user-b", "workspace-b", "b@example.com", "sub-b", now.Add(4*time.Hour)),
		LastUsed:  now.Add(-2 * time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.10,
		},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{current, older}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.CurrentSeat == nil {
		t.Fatal("expected current_seat to be populated")
	}
	if data.CurrentSeat.ID != "current-seat" {
		t.Fatalf("current seat=%+v", data.CurrentSeat)
	}
	if data.ActiveSeat == nil || data.ActiveSeat.ID != "current-seat" {
		t.Fatalf("active_seat=%+v", data.ActiveSeat)
	}
	if data.ActiveSeat.Inflight != 2 {
		t.Fatalf("expected inflight=2, got %+v", data.ActiveSeat)
	}
	if data.ActiveSeat.ActiveSeatCount != 1 {
		t.Fatalf("expected active_seat_count=1, got %+v", data.ActiveSeat)
	}
	if !strings.Contains(data.ActiveSeat.Basis, "Live requests") {
		t.Fatalf("expected live-request basis, got %+v", data.ActiveSeat)
	}
	if data.LastUsedSeat != nil {
		t.Fatalf("expected last_used_seat to be omitted when it matches active_seat, got %+v", data.LastUsedSeat)
	}
	if data.BestEligibleSeat != nil {
		t.Fatalf("expected best_eligible_seat to be omitted when it matches active_seat, got %+v", data.BestEligibleSeat)
	}
}

func TestBuildPoolDashboardDataSeparatesLastUsedAndBestEligibleWhenIdle(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	lastUsedBlocked := &Account{
		ID:        "blocked-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		LastUsed:  now.Add(-15 * time.Second),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.91,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	healthy := &Account{
		ID:        "healthy-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-b",
		IDToken:   testCodexIDToken(t, "user-b", "workspace-b", "b@example.com", "sub-b", now.Add(4*time.Hour)),
		LastUsed:  now.Add(-2 * time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
		},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{lastUsedBlocked, healthy}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.ActiveSeat != nil {
		t.Fatalf("expected no active_seat, got %+v", data.ActiveSeat)
	}
	if data.LastUsedSeat == nil || data.LastUsedSeat.ID != "blocked-seat" {
		t.Fatalf("last_used_seat=%+v", data.LastUsedSeat)
	}
	if data.LastUsedSeat.RoutingStatus != "secondary_headroom_lt_10" {
		t.Fatalf("last_used routing=%+v", data.LastUsedSeat)
	}
	if data.BestEligibleSeat == nil || data.BestEligibleSeat.ID != "healthy-seat" {
		t.Fatalf("best_eligible_seat=%+v", data.BestEligibleSeat)
	}
	if data.CurrentSeat == nil || data.CurrentSeat.ID != "healthy-seat" {
		t.Fatalf("current_seat=%+v", data.CurrentSeat)
	}
}

func TestBuildPoolDashboardDataPrefersCodexSeatPreviewBeforeFallbackAPIKey(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	codexSeat := &Account{
		ID:        "healthy-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
		},
	}
	fallbackKey := &Account{
		ID:          "openai_api_deadbeef",
		Type:        AccountTypeCodex,
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
		AccessToken: "sk-proj-test",
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{codexSeat, fallbackKey}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.BestEligibleSeat == nil || data.BestEligibleSeat.ID != "healthy-seat" {
		t.Fatalf("best_eligible_seat=%+v", data.BestEligibleSeat)
	}
	if data.CurrentSeat == nil || data.CurrentSeat.ID != "healthy-seat" {
		t.Fatalf("current_seat=%+v", data.CurrentSeat)
	}
	if data.OpenAIAPIPool.NextKeyID != "openai_api_deadbeef" {
		t.Fatalf("next api key=%q", data.OpenAIAPIPool.NextKeyID)
	}
}

func TestServePoolDashboardRouteReturnsJSONContract(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID:        "blocked",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.91,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		cfg:       config{adminToken: "secret"},
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/admin/pool/dashboard", nil)
	req.Header.Set("X-Admin-Token", "secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.PoolSummary.TotalAccounts != 1 {
		t.Fatalf("total_accounts=%d", payload.PoolSummary.TotalAccounts)
	}
	if len(payload.WorkspaceGroups) != 1 || payload.WorkspaceGroups[0].WorkspaceID != "workspace-a" {
		t.Fatalf("workspace_groups=%+v", payload.WorkspaceGroups)
	}
	if len(payload.Accounts) != 1 || payload.Accounts[0].Routing.BlockReason != "secondary_headroom_lt_10" {
		t.Fatalf("accounts=%+v", payload.Accounts)
	}
}

func TestServeStatusPageClarifiesQuotaVsLocalFields(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID:        "blocked",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.91,
			SecondaryResetAt:     now.Add(2 * time.Hour),
			RetrievedAt:          now.Add(-3 * time.Minute),
			Source:               "wham",
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status", nil)
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Current Active Seat",
		"No live request is active right now.",
		"Remaining (5h)",
		"Remaining (7d)",
		"healthy seats routable",
		"Auth TTL",
		"Local Last Used",
		"Local Tokens",
		"usage wham",
		"remaining 85%",
		"remaining 9%",
		"used 91%",
		"used 15%",
		"Remaining columns show remaining headroom, not used quota.",
		"Primary/Secondary usage and recovery come from the latest observed quota snapshot.",
		"leave rotation once headroom reaches 10% remaining",
		"Status JSON",
		"Health check",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
		}
	}
	for _, forbidden := range []string{
		`href="/admin/accounts"`,
		`href="/admin/tokens"`,
		`href="/metrics"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in body", forbidden)
		}
	}
}

func TestServeStatusPageReturnsJSONForExplicitJSONClients(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.25,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status", nil)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.CodexCount != 1 {
		t.Fatalf("codex_count=%d", payload.CodexCount)
	}
	if payload.PoolSummary.EligibleAccounts != 1 {
		t.Fatalf("eligible_accounts=%d", payload.PoolSummary.EligibleAccounts)
	}
	if payload.ActiveSeat != nil {
		t.Fatalf("active_seat=%+v", payload.ActiveSeat)
	}
	if payload.BestEligibleSeat == nil || payload.BestEligibleSeat.ID != "healthy" {
		t.Fatalf("best_eligible_seat=%+v", payload.BestEligibleSeat)
	}
	if payload.CurrentSeat == nil || payload.CurrentSeat.ID != "healthy" {
		t.Fatalf("current_seat=%+v", payload.CurrentSeat)
	}
	if payload.Accounts[0].AuthExpiresAt == "" {
		t.Fatalf("auth_expires_at missing: %+v", payload.Accounts[0])
	}
}

func TestLocalOperatorCodexOAuthStartAllowsLoopbackWithoutAdminHeader(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if _, ok := payload["oauth_url"].(string); !ok {
		t.Fatalf("payload missing oauth_url: %+v", payload)
	}
	if _, ok := payload["state"].(string); !ok {
		t.Fatalf("payload missing state: %+v", payload)
	}
}

func TestLocalOperatorCodexOAuthStartRejectsNonLoopback(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://example.com/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.RemoteAddr = "198.51.100.10:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestLocalOperatorCodexOAuthStartRejectsForwardedRequests(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestLocalOperatorCodexAPIKeyAddStoresManagedKey(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_probe","status":"completed"}`))
	}))
	defer apiServer.Close()

	baseURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:       config{poolDir: poolDir},
		pool:      newPoolState(nil, false),
		registry:  NewProviderRegistry(codex, claude, gemini),
		transport: http.DefaultTransport,
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/api-key-add", strings.NewReader(`{"api_key":"sk-proj-test"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	accountID, _ := payload["account_id"].(string)
	if accountID == "" {
		t.Fatalf("missing account_id: %+v", payload)
	}
	if payload["health_status"] != "healthy" {
		t.Fatalf("unexpected health_status: %+v", payload)
	}
	if h.pool.count() != 1 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
	keyPath := filepath.Join(poolDir, managedOpenAIAPISubdir, accountID+".json")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("expected stored key file at %s: %v", keyPath, err)
	}
}

func TestLocalOperatorCodexAPIKeyAddMarksQuotaKeyDead(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`))
	}))
	defer apiServer.Close()

	baseURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:       config{poolDir: poolDir},
		pool:      newPoolState(nil, false),
		registry:  NewProviderRegistry(codex, claude, gemini),
		transport: http.DefaultTransport,
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/api-key-add", strings.NewReader(`{"api_key":"sk-proj-test"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload["health_status"] != "dead" {
		t.Fatalf("unexpected health_status: %+v", payload)
	}
	if payload["dead"] != true {
		t.Fatalf("expected dead=true, got %+v", payload)
	}
}

func TestLocalOperatorGeminiSeatAddStoresManagedSeat(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != geminiOAuthTokenURL {
				t.Fatalf("unexpected refresh URL: %s", req.URL.String())
			}
			return gitlabClaudeJSONResponse(http.StatusOK, `{"access_token":"fresh-token","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		}),
	}

	authJSON := `{"access_token":"seed-token","refresh_token":"refresh-token","expiry_date":1774353600000}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/import-oauth-creds", strings.NewReader(`{"auth_json":`+strconv.Quote(authJSON)+`}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	accountID, _ := payload["account_id"].(string)
	if accountID == "" {
		t.Fatalf("missing account_id: %+v", payload)
	}
	if payload["health_status"] != "healthy" {
		t.Fatalf("unexpected health_status: %+v", payload)
	}
	if h.pool.count() != 1 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
	seatPath := filepath.Join(poolDir, managedGeminiSubdir, accountID+".json")
	if _, err := os.Stat(seatPath); err != nil {
		t.Fatalf("expected stored gemini seat file at %s: %v", seatPath, err)
	}
	saved, err := os.ReadFile(seatPath)
	if err != nil {
		t.Fatalf("read seat file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal seat file: %v", err)
	}
	if root["operator_source"] != geminiOperatorSourceManualImport {
		t.Fatalf("operator_source=%#v", root["operator_source"])
	}
}

func TestLocalOperatorGeminiSeatAddMarksUnauthorizedSeatDead(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return gitlabClaudeJSONResponse(http.StatusUnauthorized, `{"error":"invalid_grant"}`), nil
		}),
	}

	authJSON := `{"access_token":"seed-token","refresh_token":"refresh-token","expiry_date":1774353600000}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/account-add", strings.NewReader(`{"auth_json":`+strconv.Quote(authJSON)+`}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload["health_status"] != "dead" {
		t.Fatalf("unexpected health_status: %+v", payload)
	}
	if payload["dead"] != true {
		t.Fatalf("expected dead=true, got %+v", payload)
	}
}

func TestLocalOperatorGeminiSeatAddIgnoresProvidedRuntimeState(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return gitlabClaudeJSONResponse(http.StatusTooManyRequests, `{"error":"rate limited"}`), nil
		}),
	}

	authJSON := `{
		"access_token":"seed-token",
		"refresh_token":"refresh-token",
		"expiry_date":1774353600000,
		"dead":true,
		"disabled":true,
		"health_status":"dead",
		"health_error":"stale external state",
		"rate_limit_until":"2026-03-29T12:00:00Z"
	}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/account-add", strings.NewReader(`{"auth_json":`+strconv.Quote(authJSON)+`}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload["health_status"] != "rate_limited" {
		t.Fatalf("unexpected health_status: %+v", payload)
	}
	if payload["dead"] != false {
		t.Fatalf("expected dead=false after sanitizing provided seat state, got %+v", payload)
	}

	accountID, _ := payload["account_id"].(string)
	if accountID == "" {
		t.Fatalf("missing account_id: %+v", payload)
	}
	seatPath := filepath.Join(poolDir, managedGeminiSubdir, accountID+".json")
	saved, err := os.ReadFile(seatPath)
	if err != nil {
		t.Fatalf("read seat file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal seat file: %v", err)
	}
	if _, ok := root["disabled"]; ok {
		t.Fatalf("expected provided disabled flag to be cleared: %s", string(saved))
	}
	if _, ok := root["dead"]; ok {
		t.Fatalf("expected provided dead flag to be cleared for rate-limited seat: %s", string(saved))
	}
	if root["health_status"] != "rate_limited" {
		t.Fatalf("saved health_status=%#v", root["health_status"])
	}
}

func TestLocalOperatorGeminiSeatAddRejectsNullAuthJSON(t *testing.T) {
	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	h := &proxyHandler{
		cfg:      config{poolDir: t.TempDir()},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/import-oauth-creds", strings.NewReader(`{"auth_json":"null"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "auth_json must be a JSON object") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestLocalOperatorGeminiOAuthStartAllowsLoopbackWithoutAdminHeader(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/oauth-start", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	oauthURL, _ := payload["oauth_url"].(string)
	if oauthURL == "" {
		t.Fatalf("payload missing oauth_url: %+v", payload)
	}
	if !strings.Contains(oauthURL, "accounts.google.com") {
		t.Fatalf("unexpected oauth_url=%q", oauthURL)
	}
	if _, ok := payload["state"].(string); !ok {
		t.Fatalf("payload missing state: %+v", payload)
	}
}

func TestManagedGeminiOAuthCallbackRejectsExpiredState(t *testing.T) {
	resetManagedGeminiOAuthSessions()
	t.Cleanup(resetManagedGeminiOAuthSessions)

	managedGeminiOAuthSessions.Lock()
	managedGeminiOAuthSessions.sessions["expired-state"] = &managedGeminiOAuthSession{
		State:       "expired-state",
		RedirectURI: "http://127.0.0.1:8989/operator/gemini/oauth-callback",
		CreatedAt:   time.Now().Add(-managedGeminiOAuthSessionTTL - time.Minute).UTC(),
	}
	managedGeminiOAuthSessions.Unlock()

	h := &proxyHandler{}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/operator/gemini/oauth-callback?code=test-code&state=expired-state", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "missing or expired") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
	managedGeminiOAuthSessions.Lock()
	_, ok := managedGeminiOAuthSessions.sessions["expired-state"]
	managedGeminiOAuthSessions.Unlock()
	if ok {
		t.Fatalf("expected expired state to be removed after callback attempt")
	}
}

func TestManagedGeminiRedirectURIPreservesLoopbackFamily(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "ipv4", host: "127.0.0.1:8989", want: "http://127.0.0.1:8989/operator/gemini/oauth-callback"},
		{name: "localhost", host: "localhost:8989", want: "http://localhost:8989/operator/gemini/oauth-callback"},
		{name: "ipv6", host: "[::1]:8989", want: "http://[::1]:8989/operator/gemini/oauth-callback"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com/operator/gemini/oauth-start", nil)
			req.Host = tc.host
			got, err := managedGeminiRedirectURI(req)
			if err != nil {
				t.Fatalf("managedGeminiRedirectURI() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("managedGeminiRedirectURI() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocalOperatorGeminiOAuthCallbackStoresManagedSeat(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	redirectURI := "http://127.0.0.1:8989/operator/gemini/oauth-callback"
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				values, err := url.ParseQuery(string(body))
				if err != nil {
					t.Fatalf("parse form: %v", err)
				}
				switch values.Get("grant_type") {
				case "authorization_code":
					if values.Get("redirect_uri") != redirectURI {
						t.Fatalf("redirect_uri=%q", values.Get("redirect_uri"))
					}
					if values.Get("client_id") != testGeminiOAuthGCloudClientID {
						t.Fatalf("client_id=%q", values.Get("client_id"))
					}
					return jsonResponse(http.StatusOK, `{"access_token":"oauth-access","refresh_token":"oauth-refresh","token_type":"Bearer","scope":"scope","expires_in":3600}`), nil
				case "refresh_token":
					if values.Get("client_id") != testGeminiOAuthGCloudClientID {
						t.Fatalf("refresh client_id=%q", values.Get("client_id"))
					}
					return jsonResponse(http.StatusOK, `{"access_token":"probe-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
				default:
					t.Fatalf("unexpected grant_type=%q", values.Get("grant_type"))
				}
			case managedGeminiOAuthUserInfoURL:
				return jsonResponse(http.StatusOK, `{"email":"seat@example.com","name":"Seat Example"}`), nil
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	managedGeminiOAuthSessions.Lock()
	managedGeminiOAuthSessions.sessions = map[string]*managedGeminiOAuthSession{
		"state-1": {
			State:        "state-1",
			CodeVerifier: "verifier-1",
			RedirectURI:  redirectURI,
			ProfileID:    "gcloud",
			ClientID:     testGeminiOAuthGCloudClientID,
			ClientSecret: testGeminiOAuthGCloudSecret,
			CreatedAt:    time.Now().UTC(),
		},
	}
	managedGeminiOAuthSessions.Unlock()
	t.Cleanup(func() {
		managedGeminiOAuthSessions.Lock()
		managedGeminiOAuthSessions.sessions = make(map[string]*managedGeminiOAuthSession)
		managedGeminiOAuthSessions.Unlock()
	})

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/operator/gemini/oauth-callback?code=test-code&state=state-1", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Gemini seat added") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
	if h.pool.count() != 1 {
		t.Fatalf("pool count=%d", h.pool.count())
	}

	entries, err := os.ReadDir(filepath.Join(poolDir, managedGeminiSubdir))
	if err != nil {
		t.Fatalf("read gemini dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("unexpected gemini files: %+v", entries)
	}
	saved, err := os.ReadFile(filepath.Join(poolDir, managedGeminiSubdir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read saved seat: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode saved seat: %v", err)
	}
	if root["oauth_profile_id"] != "gcloud" {
		t.Fatalf("saved oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if root["operator_source"] != geminiOperatorSourceManagedOAuth {
		t.Fatalf("saved operator_source=%#v", root["operator_source"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected saved seat to omit raw client_id: %#v", root["client_id"])
	}
	if root["operator_email"] != "seat@example.com" {
		t.Fatalf("saved operator_email=%#v", root["operator_email"])
	}
}

func TestLocalOperatorAccountDeleteRemovesManagedAPIKeyAndReloadsPool(t *testing.T) {
	apiBase, err := url.Parse("https://api.openai.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	acc, _, err := saveManagedOpenAIAPIKey(poolDir, "sk-proj-test-delete")
	if err != nil {
		t.Fatalf("save managed key: %v", err)
	}

	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState([]*Account{acc}, false),
		registry: NewProviderRegistry(codex, claude, gemini),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/account-delete", strings.NewReader(`{"account_id":"`+acc.ID+`"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(acc.File); !os.IsNotExist(err) {
		t.Fatalf("expected key file to be removed, stat err=%v", err)
	}
	if h.pool.count() != 0 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
}

func TestLocalOperatorAccountDeleteRejectsInflightAccount(t *testing.T) {
	apiBase, err := url.Parse("https://api.openai.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	codexDir := filepath.Join(poolDir, "codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	authPath := filepath.Join(codexDir, "seat-a.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"access","refresh_token":"refresh"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{
		cfg: config{poolDir: poolDir},
		pool: newPoolState([]*Account{{
			ID:          "seat-a",
			Type:        AccountTypeCodex,
			File:        authPath,
			AccessToken: "access",
			Inflight:    1,
		}}, false),
		registry: NewProviderRegistry(codex, claude, gemini),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/account-delete", strings.NewReader(`{"account_id":"seat-a"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("expected auth file to remain: %v", err)
	}
	if h.pool.count() != 1 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
}

func TestLocalOperatorCodexOAuthStartDisabledInFriendMode(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{friendCode: "friend-code", adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestServeStatusPageReturnsJSONForFormatQuery(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status?format=json", nil)
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.CodexCount != 1 {
		t.Fatalf("codex_count=%d", payload.CodexCount)
	}
}

func TestServeStatusPageIncludesQuarantineStatus(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	poolDir := t.TempDir()
	quarantineDir := filepath.Join(poolDir, quarantineSubdir, "gemini")
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		t.Fatalf("mkdir quarantine dir: %v", err)
	}

	quarantinedPath := filepath.Join(quarantineDir, "seat-a.json")
	if err := os.WriteFile(quarantinedPath, []byte(`{"dead":true}`), 0o600); err != nil {
		t.Fatalf("write quarantined file: %v", err)
	}
	quarantinedAt := now.Add(-2 * time.Hour)
	if err := os.Chtimes(quarantinedPath, quarantinedAt, quarantinedAt); err != nil {
		t.Fatalf("chtimes quarantined file: %v", err)
	}

	h := &proxyHandler{
		cfg:       config{poolDir: poolDir},
		pool:      newPoolState(nil, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/status?format=json", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.Quarantine.Total != 1 {
		t.Fatalf("quarantine total=%d", payload.Quarantine.Total)
	}
	if got := payload.Quarantine.Providers["gemini"]; got != 1 {
		t.Fatalf("quarantine gemini count=%d", got)
	}
	if len(payload.Quarantine.Recent) != 1 {
		t.Fatalf("recent quarantine entries=%d", len(payload.Quarantine.Recent))
	}
	if payload.Quarantine.Recent[0].ID != "seat-a" {
		t.Fatalf("unexpected quarantine entry id=%q", payload.Quarantine.Recent[0].ID)
	}
	if payload.Quarantine.Recent[0].Provider != "gemini" {
		t.Fatalf("unexpected quarantine provider=%q", payload.Quarantine.Recent[0].Provider)
	}

	htmlReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/status", nil)
	htmlReq.Host = "127.0.0.1:8989"
	htmlReq.RemoteAddr = "127.0.0.1:4242"
	htmlRR := httptest.NewRecorder()
	h.serveStatusPage(htmlRR, htmlReq)
	if htmlRR.Code != http.StatusOK {
		t.Fatalf("html status=%d body=%s", htmlRR.Code, htmlRR.Body.String())
	}
	body := htmlRR.Body.String()
	for _, fragment := range []string{
		"Quarantine",
		"Quarantined files:",
		"seat-a",
		"Accounts that stay dead for more than 72 hours are moved out of the active pool automatically",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in html body", fragment)
		}
	}
}

func TestServeStatusPageIncludesOperatorActionForLocalLoopback(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/status", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Start Codex OAuth",
		"Fallback API Pool",
		"Managed Gemini OAuth",
		"Start Managed Gemini OAuth",
		"Manual Gemini Import",
		"Add API Key",
		"Import oauth_creds.json",
		"openai-api-key-input",
		"gemini-seat-json-input",
		"/operator/codex/api-key-add",
		"/operator/gemini/import-oauth-creds",
		"/operator/gemini/oauth-start",
		"/operator/account-delete",
		"deleteAccountFromStatus",
		"account-action-status",
		"/v1/responses",
		"codex_pool_manager.py codex-oauth-start",
		"/operator/codex/oauth-start",
		"Open OAuth Page",
		"keeps the popup opener attached",
		"refreshes this page automatically when pool seat state changes",
		"Waiting for pool seat state to change...",
		"Waiting for pool seat state to change.",
		"Waiting for the Gemini seat state to change...",
		"Timed out waiting for the Gemini seat state to change.",
		"codex-oauth-result",
		"gemini_oauth_result",
		"auth_expires_at",
		"last_refresh_at",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
		}
	}
	for _, forbidden := range []string{
		"noopener noreferrer",
		"auth_expires_in || ''",
		"local_last_used || ''",
		"local_tokens || ''",
		"Waiting for the OAuth callback.",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in status body", forbidden)
		}
	}
}

func TestServeStatusPageHidesOperatorActionOutsideLoopback(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status", nil)
	req.RemoteAddr = "198.51.100.10:4242"
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, forbidden := range []string{
		"Import Gemini",
		"Start Codex OAuth",
		"codex_pool_manager.py codex-oauth-start",
		"/operator/codex/oauth-start",
		"Fallback API Pool",
		"Managed Gemini OAuth",
		"Start Managed Gemini OAuth",
		"Manual Gemini Import",
		"/operator/codex/api-key-add",
		"/operator/gemini/import-oauth-creds",
		"/operator/gemini/oauth-start",
		"/operator/account-delete",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in body", forbidden)
		}
	}
}
