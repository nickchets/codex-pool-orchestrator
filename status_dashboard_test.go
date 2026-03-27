package main

import (
	"context"
	"encoding/base64"
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

func resetAntigravityGeminiOAuthSessions() {
	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions = make(map[string]*antigravityGeminiOAuthSession)
	antigravityGeminiOAuthSessions.Unlock()
}

func addGeminiSeatFromAuthJSONForTest(t *testing.T, h *proxyHandler, authJSON string) *geminiSeatAddOutcome {
	t.Helper()
	outcome, err := h.addGeminiSeatFromAuthJSON(context.Background(), authJSON)
	if err != nil {
		t.Fatalf("add gemini seat: %v", err)
	}
	return outcome
}

func readGeminiSeatRootForTest(t *testing.T, poolDir, accountID string) map[string]any {
	t.Helper()
	seatPath := filepath.Join(poolDir, managedGeminiSubdir, accountID+".json")
	saved, err := os.ReadFile(seatPath)
	if err != nil {
		t.Fatalf("read seat file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal seat file: %v", err)
	}
	return root
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

func TestBuildPoolDashboardDataTracksEligibleUnhealthyOpenAIAPIFallback(t *testing.T) {
	now := time.Date(2026, 3, 25, 14, 0, 0, 0, time.UTC)
	apiKey := &Account{
		ID:              "openai_api_fallback",
		Type:            AccountTypeCodex,
		PlanType:        "api",
		AuthMode:        accountAuthModeAPIKey,
		HealthStatus:    "error",
		HealthError:     "context deadline exceeded",
		HealthCheckedAt: now.Add(-3 * time.Minute),
		LastHealthyAt:   now.Add(-2 * time.Hour),
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{apiKey}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.OpenAIAPIPool.TotalKeys != 1 {
		t.Fatalf("api total=%d", data.OpenAIAPIPool.TotalKeys)
	}
	if data.OpenAIAPIPool.HealthyKeys != 0 {
		t.Fatalf("api healthy=%d", data.OpenAIAPIPool.HealthyKeys)
	}
	if data.OpenAIAPIPool.EligibleKeys != 1 {
		t.Fatalf("api eligible=%d", data.OpenAIAPIPool.EligibleKeys)
	}
	if data.OpenAIAPIPool.EligibleUnhealthyKeys != 1 {
		t.Fatalf("api eligible_unhealthy=%d", data.OpenAIAPIPool.EligibleUnhealthyKeys)
	}
	if data.OpenAIAPIPool.NextKeyID != "openai_api_fallback" {
		t.Fatalf("next api key=%q", data.OpenAIAPIPool.NextKeyID)
	}
	if !strings.Contains(data.OpenAIAPIPool.StatusNote, "Eligible now follows selector routing") {
		t.Fatalf("status_note=%q", data.OpenAIAPIPool.StatusNote)
	}
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}

	status := data.Accounts[0]
	if !status.FallbackOnly {
		t.Fatalf("expected fallback_only account status, got %+v", status)
	}
	if !status.Routing.Eligible {
		t.Fatalf("expected eligible fallback key despite unhealthy probe, got %+v", status.Routing)
	}
	if status.HealthStatus != "error" {
		t.Fatalf("health_status=%q", status.HealthStatus)
	}
	if status.HealthError != "context deadline exceeded" {
		t.Fatalf("health_error=%q", status.HealthError)
	}
	if status.ProbeState != "error" {
		t.Fatalf("probe_state=%q", status.ProbeState)
	}
	if !strings.Contains(status.ProbeSummary, "timed out") {
		t.Fatalf("probe_summary=%q", status.ProbeSummary)
	}
	if status.HealthCheckedAt != "2026-03-25T13:57:00Z" {
		t.Fatalf("health_checked_at=%q", status.HealthCheckedAt)
	}
	if status.LastHealthyAt != "2026-03-25T12:00:00Z" {
		t.Fatalf("last_healthy_at=%q", status.LastHealthyAt)
	}

	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal account status: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode account status: %v", err)
	}
	if got := payload["fallback_only"]; got != true {
		t.Fatalf("fallback_only payload=%#v", got)
	}
	if got := payload["health_status"]; got != "error" {
		t.Fatalf("health_status payload=%#v", got)
	}
	if got := payload["probe_state"]; got != "error" {
		t.Fatalf("probe_state payload=%#v", got)
	}
	if got := payload["health_checked_at"]; got != "2026-03-25T13:57:00Z" {
		t.Fatalf("health_checked_at payload=%#v", got)
	}
	if got := payload["last_healthy_at"]; got != "2026-03-25T12:00:00Z" {
		t.Fatalf("last_healthy_at payload=%#v", got)
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
	antigravity := &Account{
		ID:             "gemini_antigravity",
		Type:           AccountTypeGemini,
		PlanType:       "gemini",
		AuthMode:       accountAuthModeOAuth,
		OperatorSource: geminiOperatorSourceAntigravityImport,
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{managed, imported, antigravity}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.GeminiOperator.ManagedSeatCount != 1 {
		t.Fatalf("managed_seat_count=%d", data.GeminiOperator.ManagedSeatCount)
	}
	if data.GeminiOperator.ImportedSeatCount != 2 {
		t.Fatalf("imported_seat_count=%d", data.GeminiOperator.ImportedSeatCount)
	}
	if data.GeminiOperator.AntigravitySeatCount != 1 {
		t.Fatalf("antigravity_seat_count=%d", data.GeminiOperator.AntigravitySeatCount)
	}
	if data.GeminiOperator.LegacySeatCount != 1 {
		t.Fatalf("legacy_seat_count=%d", data.GeminiOperator.LegacySeatCount)
	}
	if !data.GeminiOperator.ManagedOAuthAvailable {
		t.Fatalf("expected managed_oauth_available=true")
	}
	if data.GeminiOperator.ManagedOAuthProfile != "gcloud" {
		t.Fatalf("managed_oauth_profile=%q", data.GeminiOperator.ManagedOAuthProfile)
	}
	if !strings.Contains(data.GeminiOperator.Note, "only supported Gemini seat onboarding flow") {
		t.Fatalf("note=%q", data.GeminiOperator.Note)
	}
	if len(data.Accounts) != 3 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].OperatorSource == "" || data.Accounts[1].OperatorSource == "" || data.Accounts[2].OperatorSource == "" {
		t.Fatalf("operator sources missing: %+v", data.Accounts)
	}
}

func TestBuildPoolDashboardDataShowsGeminiImportedSeatsWithoutManagedOAuth(t *testing.T) {
	t.Setenv(geminiOAuthEnvClientIDVar, "")
	t.Setenv(geminiOAuthEnvClientSecretVar, "")
	t.Setenv(geminiOAuthCLIClientIDVar, "")
	t.Setenv(geminiOAuthCLIClientSecretVar, "")
	t.Setenv(geminiOAuthGCloudClientIDVar, "")
	t.Setenv(geminiOAuthGCloudClientSecretVar, "")
	t.Setenv(geminiOAuthAntigravitySecretVar, "")

	now := time.Date(2026, 3, 25, 14, 5, 0, 0, time.UTC)
	imported := &Account{
		ID:             "gemini_imported",
		Type:           AccountTypeGemini,
		PlanType:       "gemini",
		AuthMode:       accountAuthModeOAuth,
		OperatorSource: geminiOperatorSourceManualImport,
		OperatorEmail:  "seat@example.com",
		HealthStatus:   "imported",
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{imported}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.GeminiOperator.ManagedOAuthAvailable {
		t.Fatalf("expected managed_oauth_available=false")
	}
	if data.GeminiOperator.ManagedSeatCount != 0 {
		t.Fatalf("managed_seat_count=%d", data.GeminiOperator.ManagedSeatCount)
	}
	if data.GeminiOperator.ImportedSeatCount != 1 {
		t.Fatalf("imported_seat_count=%d", data.GeminiOperator.ImportedSeatCount)
	}
	if data.GeminiOperator.AntigravitySeatCount != 0 {
		t.Fatalf("antigravity_seat_count=%d", data.GeminiOperator.AntigravitySeatCount)
	}
	if data.GeminiOperator.LegacySeatCount != 1 {
		t.Fatalf("legacy_seat_count=%d", data.GeminiOperator.LegacySeatCount)
	}
	if data.GeminiOperator.Note == "" {
		t.Fatal("expected note to explain browser-only onboarding truth")
	}
	if !strings.Contains(data.GeminiOperator.Note, "legacy local Gemini seat") {
		t.Fatalf("note=%q", data.GeminiOperator.Note)
	}
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].OperatorSource != "legacy local import" {
		t.Fatalf("operator_source=%q", data.Accounts[0].OperatorSource)
	}
	if data.Accounts[0].HealthStatus != "imported" {
		t.Fatalf("health_status=%q", data.Accounts[0].HealthStatus)
	}

	raw, err := json.Marshal(data.GeminiOperator)
	if err != nil {
		t.Fatalf("marshal gemini operator: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode gemini operator payload: %v", err)
	}
	if got := payload["managed_oauth_available"]; got != false {
		t.Fatalf("managed_oauth_available payload=%#v", got)
	}
	if got := payload["note"]; got == "" {
		t.Fatalf("note payload=%#v", got)
	}
	if got := payload["imported_seat_count"]; got != float64(1) {
		t.Fatalf("imported_seat_count payload=%#v", got)
	}
	if got := payload["legacy_seat_count"]; got != float64(1) {
		t.Fatalf("legacy_seat_count payload=%#v", got)
	}
}

func TestBuildPoolDashboardDataLeavesLegacyGeminiOperatorSourceUnsetWithoutProvenance(t *testing.T) {
	now := time.Date(2026, 3, 24, 10, 5, 0, 0, time.UTC)
	legacy := &Account{
		ID:       "gemini_legacy",
		Type:     AccountTypeGemini,
		PlanType: "gemini",
		AuthMode: accountAuthModeOAuth,
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{legacy}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].OperatorSource != "" {
		t.Fatalf("operator_source=%q, want empty for legacy Gemini seat without explicit provenance", data.Accounts[0].OperatorSource)
	}
	if data.GeminiOperator.ManagedSeatCount != 0 || data.GeminiOperator.ImportedSeatCount != 0 || data.GeminiOperator.AntigravitySeatCount != 0 || data.GeminiOperator.LegacySeatCount != 0 {
		t.Fatalf("unexpected Gemini operator counts: %+v", data.GeminiOperator)
	}
}

func TestBuildPoolDashboardDataCountsAntigravityGeminiImports(t *testing.T) {
	now := time.Date(2026, 3, 24, 10, 6, 0, 0, time.UTC)
	imported := &Account{
		ID:               "gemini_antigravity",
		Type:             AccountTypeGemini,
		PlanType:         "gemini",
		AuthMode:         accountAuthModeOAuth,
		OperatorSource:   geminiOperatorSourceAntigravityImport,
		OperatorEmail:    "ag@example.com",
		AntigravityEmail: "ag@example.com",
		HealthStatus:     "imported",
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{imported}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.GeminiOperator.ImportedSeatCount != 1 {
		t.Fatalf("imported_seat_count=%d", data.GeminiOperator.ImportedSeatCount)
	}
	if data.GeminiOperator.AntigravitySeatCount != 1 {
		t.Fatalf("antigravity_seat_count=%d", data.GeminiOperator.AntigravitySeatCount)
	}
	if data.GeminiOperator.LegacySeatCount != 0 {
		t.Fatalf("legacy_seat_count=%d", data.GeminiOperator.LegacySeatCount)
	}
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].OperatorSource != "antigravity browser auth" {
		t.Fatalf("operator_source=%q", data.Accounts[0].OperatorSource)
	}
	if data.Accounts[0].Email != "ag@example.com" {
		t.Fatalf("email=%q", data.Accounts[0].Email)
	}
}

func TestBuildPoolDashboardDataIncludesGeminiProviderTruth(t *testing.T) {
	now := time.Date(2026, 3, 24, 10, 7, 0, 0, time.UTC)
	seat := &Account{
		ID:                           "gemini_antigravity",
		Type:                         AccountTypeGemini,
		PlanType:                     "gemini",
		AuthMode:                     accountAuthModeOAuth,
		OperatorSource:               geminiOperatorSourceAntigravityImport,
		AntigravityProjectID:         "project-1",
		AntigravityValidationBlocked: true,
		GeminiSubscriptionTierID:     "standard-tier",
		GeminiSubscriptionTierName:   "Standard",
		GeminiValidationReasonCode:   "ACCOUNT_NEEDS_WORKSPACE",
		GeminiValidationMessage:      "Workspace validation required",
		GeminiValidationURL:          "https://example.com/validate",
		GeminiProviderCheckedAt:      time.Date(2026, 3, 24, 10, 5, 0, 0, time.UTC),
		GeminiProtectedModels:        []string{"gemini-3.1-pro-high"},
		GeminiQuotaUpdatedAt:         time.Date(2026, 3, 24, 10, 6, 0, 0, time.UTC),
		GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
			Name:             "gemini-3.1-pro-high",
			RouteProvider:    "gemini",
			Percentage:       81,
			ResetTime:        "2026-03-24T16:00:00Z",
			DisplayName:      "Gemini 3.1 Pro High",
			SupportsImages:   true,
			SupportsThinking: true,
			ThinkingBudget:   24576,
			MaxOutputTokens:  65535,
		}, {
			Name:          "claude-sonnet-4-6",
			RouteProvider: "claude",
			Percentage:    62,
			ResetTime:     "2026-03-24T16:30:00Z",
			DisplayName:   "Claude Sonnet 4.6",
		}},
		GeminiModelForwardingRules: map[string]string{
			"gemini-1.5-pro": "gemini-2.5-pro",
		},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].ProviderSubscriptionTier != "standard-tier" {
		t.Fatalf("provider_subscription_tier=%q", data.Accounts[0].ProviderSubscriptionTier)
	}
	if data.Accounts[0].ProviderSubscriptionName != "Standard" {
		t.Fatalf("provider_subscription_name=%q", data.Accounts[0].ProviderSubscriptionName)
	}
	if data.Accounts[0].ProviderValidationCode != "ACCOUNT_NEEDS_WORKSPACE" {
		t.Fatalf("provider_validation_code=%q", data.Accounts[0].ProviderValidationCode)
	}
	if data.Accounts[0].ProviderValidationMessage != "Workspace validation required" {
		t.Fatalf("provider_validation_message=%q", data.Accounts[0].ProviderValidationMessage)
	}
	if data.Accounts[0].ProviderValidationURL != "https://example.com/validate" {
		t.Fatalf("provider_validation_url=%q", data.Accounts[0].ProviderValidationURL)
	}
	if data.Accounts[0].ProviderCheckedAt != "2026-03-24T10:05:00Z" {
		t.Fatalf("provider_checked_at=%q", data.Accounts[0].ProviderCheckedAt)
	}
	if data.Accounts[0].ProviderTruth == nil {
		t.Fatal("expected provider_truth object")
	}
	if data.Accounts[0].ProviderTruth.ProjectID != "project-1" {
		t.Fatalf("provider_truth.project_id=%q", data.Accounts[0].ProviderTruth.ProjectID)
	}
	if data.Accounts[0].ProviderTruth.State != geminiProviderTruthStateValidationBlocked {
		t.Fatalf("provider_truth.state=%q", data.Accounts[0].ProviderTruth.State)
	}
	if data.Accounts[0].ProviderTruth.Ready {
		t.Fatal("expected provider_truth.ready=false for validation blocked seat")
	}
	if !data.Accounts[0].ProviderTruth.ValidationBlocked {
		t.Fatal("expected provider_truth.validation_blocked=true")
	}
	if data.Accounts[0].ProviderTruth.CheckedAt != "2026-03-24T10:05:00Z" {
		t.Fatalf("provider_truth.checked_at=%q", data.Accounts[0].ProviderTruth.CheckedAt)
	}
	if len(data.Accounts[0].ProviderTruth.ProtectedModels) != 1 || data.Accounts[0].ProviderTruth.ProtectedModels[0] != "gemini-3.1-pro-high" {
		t.Fatalf("provider_truth.protected_models=%#v", data.Accounts[0].ProviderTruth.ProtectedModels)
	}
	if data.Accounts[0].ProviderTruth.Quota == nil {
		t.Fatal("expected provider_truth.quota object")
	}
	if data.Accounts[0].ProviderTruth.Quota.UpdatedAt != "2026-03-24T10:06:00Z" {
		t.Fatalf("provider_truth.quota.updated_at=%q", data.Accounts[0].ProviderTruth.Quota.UpdatedAt)
	}
	if got := data.Accounts[0].ProviderTruth.Quota.ModelForwardingRules["gemini-1.5-pro"]; got != "gemini-2.5-pro" {
		t.Fatalf("provider_truth.quota.model_forwarding_rules=%#v", data.Accounts[0].ProviderTruth.Quota.ModelForwardingRules)
	}
	if len(data.Accounts[0].ProviderTruth.Quota.Models) != 2 {
		t.Fatalf("provider_truth.quota.models=%#v", data.Accounts[0].ProviderTruth.Quota.Models)
	}
	if !data.Accounts[0].ProviderTruth.Quota.Models[0].Protected {
		t.Fatalf("expected protected model flag, got %#v", data.Accounts[0].ProviderTruth.Quota.Models[0])
	}
	geminiModel := data.Accounts[0].ProviderTruth.Quota.Models[0]
	if geminiModel.RouteProvider != "gemini" {
		t.Fatalf("provider_truth.quota.models[0].route_provider=%q", geminiModel.RouteProvider)
	}
	if geminiModel.Routable {
		t.Fatalf("expected blocked gemini model to stay unroutable, got %#v", geminiModel)
	}
	if geminiModel.CompatibilityLane != geminiQuotaCompatibilityLaneGeminiFacade {
		t.Fatalf("provider_truth.quota.models[0].compatibility_lane=%q", geminiModel.CompatibilityLane)
	}
	if !strings.Contains(geminiModel.CompatibilityReason, "validation") {
		t.Fatalf("provider_truth.quota.models[0].compatibility_reason=%q", geminiModel.CompatibilityReason)
	}
	if geminiModel.MaxOutputTokens != 65535 {
		t.Fatalf("provider_truth.quota.models[0].max_output_tokens=%d", geminiModel.MaxOutputTokens)
	}
	claudeModel := data.Accounts[0].ProviderTruth.Quota.Models[1]
	if claudeModel.RouteProvider != "claude" {
		t.Fatalf("provider_truth.quota.models[1].route_provider=%q", claudeModel.RouteProvider)
	}
	if claudeModel.Routable {
		t.Fatalf("expected claude quota model to stay catalog-only, got %#v", claudeModel)
	}
	if claudeModel.CompatibilityLane != geminiQuotaCompatibilityLaneAnthropicAdapterRequired {
		t.Fatalf("provider_truth.quota.models[1].compatibility_lane=%q", claudeModel.CompatibilityLane)
	}
	if claudeModel.CompatibilityReason != "quota catalog only; Anthropic-compatible adapter is not implemented" {
		t.Fatalf("provider_truth.quota.models[1].compatibility_reason=%q", claudeModel.CompatibilityReason)
	}
	if data.Accounts[0].ProviderQuotaSummary != "2 models · gemini 1 seat-blocked · claude 1 catalog-only" {
		t.Fatalf("provider_quota_summary=%q", data.Accounts[0].ProviderQuotaSummary)
	}
}

func TestBuildPoolDashboardDataMarksAllowlistedValidationBlockedGeminiQuotaModelsRoutable(t *testing.T) {
	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	seat := &Account{
		ID:                           "gemini_antigravity_blocked",
		Type:                         AccountTypeGemini,
		PlanType:                     "gemini",
		AuthMode:                     accountAuthModeOAuth,
		OperatorSource:               geminiOperatorSourceAntigravityImport,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
		GeminiProviderTruthState:     geminiProviderTruthStateRestricted,
		GeminiProviderTruthReason:    "UNSUPPORTED_LOCATION",
		GeminiOperationalState:       geminiOperationalTruthStateDegradedOK,
		GeminiOperationalReason:      "UNSUPPORTED_LOCATION",
		GeminiQuotaUpdatedAt:         now,
		GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
			Name:          "gemini-2.5-flash",
			RouteProvider: "gemini",
			Percentage:    71,
			ResetTime:     "2026-03-26T18:00:00Z",
			DisplayName:   "Gemini 2.5 Flash",
		}},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.PoolSummary.EligibleAccounts != 1 {
		t.Fatalf("eligible_accounts=%d", data.PoolSummary.EligibleAccounts)
	}
	if !data.Accounts[0].Routing.Eligible {
		t.Fatalf("routing=%+v", data.Accounts[0].Routing)
	}
	if data.Accounts[0].Routing.BlockReason != "" {
		t.Fatalf("block_reason=%q", data.Accounts[0].Routing.BlockReason)
	}
	if data.Accounts[0].Routing.PrimaryHeadroomKnown || data.Accounts[0].Routing.SecondaryHeadroomKnown {
		t.Fatalf("expected Gemini headroom to stay unknown without observed usage, got %+v", data.Accounts[0].Routing)
	}
	if data.Accounts[0].ProviderTruth == nil || data.Accounts[0].ProviderTruth.Quota == nil {
		t.Fatalf("provider_truth=%+v", data.Accounts[0].ProviderTruth)
	}
	models := data.Accounts[0].ProviderTruth.Quota.Models
	if len(models) != 1 {
		t.Fatalf("provider_truth.quota.models=%#v", models)
	}
	if !models[0].Routable {
		t.Fatalf("expected allowlisted blocked Gemini model to be routable, got %#v", models[0])
	}
	if models[0].CompatibilityLane != geminiQuotaCompatibilityLaneGeminiFacade {
		t.Fatalf("provider_truth.quota.models[0].compatibility_lane=%q", models[0].CompatibilityLane)
	}
	if models[0].CompatibilityReason != "" {
		t.Fatalf("provider_truth.quota.models[0].compatibility_reason=%q", models[0].CompatibilityReason)
	}
	if data.Accounts[0].ProviderQuotaSummary != "1 models · gemini 1 routable" {
		t.Fatalf("provider_quota_summary=%q", data.Accounts[0].ProviderQuotaSummary)
	}
}

func TestBuildPoolDashboardDataSummarizesGeminiQuotaSnapshotWithoutModelRows(t *testing.T) {
	now := time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)
	seat := &Account{
		ID:                      "gemini_ready_empty_quota",
		Type:                    AccountTypeGemini,
		PlanType:                "gemini",
		AuthMode:                accountAuthModeOAuth,
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		AntigravityProjectID:    "project-1",
		GeminiProviderCheckedAt: now.Add(-2 * time.Minute),
		GeminiQuotaUpdatedAt:    now.Add(-time.Minute),
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].ProviderQuotaSummary != "0 models captured" {
		t.Fatalf("provider_quota_summary=%q", data.Accounts[0].ProviderQuotaSummary)
	}
	if data.GeminiPool == nil {
		t.Fatal("expected gemini_pool aggregate")
	}
	if data.GeminiPool.TotalSeats != 1 || data.GeminiPool.EligibleSeats != 1 || data.GeminiPool.ReadySeats != 1 {
		t.Fatalf("gemini_pool=%+v", *data.GeminiPool)
	}
	if data.GeminiPool.QuotaTrackedSeats != 1 || data.GeminiPool.QuotaEmptySeats != 1 {
		t.Fatalf("gemini_pool=%+v", *data.GeminiPool)
	}
	if !strings.Contains(data.GeminiPool.Note, "quota snapshots without model rows") {
		t.Fatalf("gemini_pool.note=%q", data.GeminiPool.Note)
	}
}

func TestBuildPoolDashboardDataMarksGeminiProviderTruthFreshWhenReady(t *testing.T) {
	now := time.Date(2026, 3, 24, 10, 7, 0, 0, time.UTC)
	seat := &Account{
		ID:                      "gemini_ready_fresh",
		Type:                    AccountTypeGemini,
		PlanType:                "gemini",
		AuthMode:                accountAuthModeOAuth,
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		AntigravityProjectID:    "project-1",
		GeminiProviderCheckedAt: time.Date(2026, 3, 24, 10, 5, 0, 0, time.UTC),
		GeminiQuotaUpdatedAt:    time.Date(2026, 3, 24, 10, 6, 0, 0, time.UTC),
		GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
			Name:       "gemini-3.1-pro-high",
			Percentage: 81,
		}},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].ProviderTruth == nil {
		t.Fatal("expected provider_truth object")
	}
	if data.Accounts[0].ProviderTruth.State != geminiProviderTruthStateReady {
		t.Fatalf("provider_truth.state=%q", data.Accounts[0].ProviderTruth.State)
	}
	if data.Accounts[0].ProviderTruth.FreshnessState != geminiProviderTruthFreshnessStateFresh {
		t.Fatalf("provider_truth.freshness_state=%q", data.Accounts[0].ProviderTruth.FreshnessState)
	}
	if data.Accounts[0].ProviderTruth.FreshUntil != "2026-03-24T10:35:00Z" {
		t.Fatalf("provider_truth.fresh_until=%q", data.Accounts[0].ProviderTruth.FreshUntil)
	}
	if data.Accounts[0].ProviderTruth.Stale {
		t.Fatal("expected provider_truth.stale=false for fresh provider snapshot")
	}
}

func TestBuildPoolDashboardDataMarksGeminiProviderTruthStaleFromQuotaAge(t *testing.T) {
	now := time.Date(2026, 3, 24, 10, 40, 0, 0, time.UTC)
	seat := &Account{
		ID:                      "gemini_ready_stale_quota",
		Type:                    AccountTypeGemini,
		PlanType:                "gemini",
		AuthMode:                accountAuthModeOAuth,
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		AntigravityProjectID:    "project-1",
		GeminiProviderCheckedAt: time.Date(2026, 3, 24, 10, 25, 0, 0, time.UTC),
		GeminiQuotaUpdatedAt:    time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC),
		GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
			Name:       "gemini-3.1-pro-high",
			Percentage: 81,
		}},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].ProviderTruth == nil {
		t.Fatal("expected provider_truth object")
	}
	if data.Accounts[0].ProviderTruth.State != geminiProviderTruthStateReady {
		t.Fatalf("provider_truth.state=%q", data.Accounts[0].ProviderTruth.State)
	}
	if !data.Accounts[0].ProviderTruth.Ready {
		t.Fatal("expected provider_truth.ready=true for stale-but-known seat")
	}
	if data.Accounts[0].ProviderTruth.FreshnessState != geminiProviderTruthFreshnessStateStale {
		t.Fatalf("provider_truth.freshness_state=%q", data.Accounts[0].ProviderTruth.FreshnessState)
	}
	if !data.Accounts[0].ProviderTruth.Stale {
		t.Fatal("expected provider_truth.stale=true")
	}
	if data.Accounts[0].ProviderTruth.StaleReason != "quota snapshot is older than the freshness window" {
		t.Fatalf("provider_truth.stale_reason=%q", data.Accounts[0].ProviderTruth.StaleReason)
	}
	if data.Accounts[0].ProviderTruth.FreshUntil != "2026-03-24T10:30:00Z" {
		t.Fatalf("provider_truth.fresh_until=%q", data.Accounts[0].ProviderTruth.FreshUntil)
	}
	if data.Accounts[0].Routing.BlockReason != "stale_quota_snapshot" {
		t.Fatalf("routing.block_reason=%q", data.Accounts[0].Routing.BlockReason)
	}
	if data.GeminiPool == nil {
		t.Fatal("expected gemini_pool aggregate")
	}
	if data.GeminiPool.StaleTruthSeats != 1 || data.GeminiPool.StaleQuotaSeats != 1 {
		t.Fatalf("gemini_pool=%+v", *data.GeminiPool)
	}
	if !strings.Contains(data.GeminiPool.Note, "stale quota snapshot") {
		t.Fatalf("gemini_pool.note=%q", data.GeminiPool.Note)
	}
}

func TestBuildPoolDashboardDataCountsEligibleGeminiCooldownSeats(t *testing.T) {
	now := time.Date(2026, 3, 24, 10, 40, 0, 0, time.UTC)
	seat := &Account{
		ID:                      "gemini_ready_cooldown",
		Type:                    AccountTypeGemini,
		PlanType:                "gemini",
		AuthMode:                accountAuthModeOAuth,
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		AntigravityProjectID:    "project-1",
		GeminiProviderCheckedAt: now.Add(-5 * time.Minute),
		GeminiOperationalState:  geminiOperationalTruthStateCooldown,
		GeminiOperationalReason: "quota resets in 4s",
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.GeminiPool == nil {
		t.Fatal("expected gemini_pool aggregate")
	}
	if data.GeminiPool.CooldownSeats != 1 {
		t.Fatalf("gemini_pool=%+v", *data.GeminiPool)
	}
	if data.GeminiPool.EligibleSeats != 1 || data.GeminiPool.CleanEligibleSeats != 0 || data.GeminiPool.DegradedEligibleSeats != 1 {
		t.Fatalf("gemini_pool=%+v", *data.GeminiPool)
	}
	if data.Accounts[0].Routing.State != routingDisplayStateDegradedEnabled {
		t.Fatalf("routing.state=%q", data.Accounts[0].Routing.State)
	}
}

func TestBuildPoolDashboardDataCountsCleanEligibleGeminiSeatsSeparately(t *testing.T) {
	now := time.Date(2026, 3, 24, 10, 40, 0, 0, time.UTC)
	seat := &Account{
		ID:                      "gemini_ready_clean",
		Type:                    AccountTypeGemini,
		PlanType:                "gemini",
		AuthMode:                accountAuthModeOAuth,
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		AntigravityProjectID:    "project-1",
		GeminiProviderCheckedAt: now.Add(-5 * time.Minute),
		GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.GeminiPool == nil {
		t.Fatal("expected gemini_pool aggregate")
	}
	if data.GeminiPool.EligibleSeats != 1 || data.GeminiPool.CleanEligibleSeats != 1 || data.GeminiPool.DegradedEligibleSeats != 0 {
		t.Fatalf("gemini_pool=%+v", *data.GeminiPool)
	}
	if data.Accounts[0].Routing.State != routingDisplayStateEnabled {
		t.Fatalf("routing.state=%q", data.Accounts[0].Routing.State)
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
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return gitlabClaudeJSONResponse(http.StatusOK, `{"access_token":"fresh-token","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{"done":true,"response":{"cloudaicompanionProject":{"id":"project-1"}}}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{"cloudaicompanionProject":"project-1","currentTier":{"id":"standard-tier","name":"Standard"}}`), nil
			default:
				t.Fatalf("unexpected refresh URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	authJSON := `{"access_token":"seed-token","refresh_token":"refresh-token","expiry_date":1774353600000}`
	outcome := addGeminiSeatFromAuthJSONForTest(t, h, authJSON)
	accountID := outcome.AccountID
	if accountID == "" {
		t.Fatalf("missing account_id: %+v", outcome)
	}
	if outcome.HealthStatus != "healthy" {
		t.Fatalf("unexpected health_status: %+v", outcome)
	}
	if !outcome.ProviderTruthReady {
		t.Fatalf("unexpected provider_truth_ready: %+v", outcome)
	}
	if outcome.ProviderTruthState != geminiProviderTruthStateReady {
		t.Fatalf("unexpected provider_truth_state: %+v", outcome)
	}
	if outcome.ProviderProjectID != "project-1" {
		t.Fatalf("unexpected provider_project_id: %+v", outcome)
	}
	if h.pool.count() != 1 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
	seatPath := filepath.Join(poolDir, managedGeminiSubdir, accountID+".json")
	if _, err := os.Stat(seatPath); err != nil {
		t.Fatalf("expected stored gemini seat file at %s: %v", seatPath, err)
	}
	root := readGeminiSeatRootForTest(t, poolDir, accountID)
	if root["operator_source"] != geminiOperatorSourceManualImport {
		t.Fatalf("operator_source=%#v", root["operator_source"])
	}
	if root["gemini_provider_truth_ready"] != true {
		t.Fatalf("gemini_provider_truth_ready=%#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateReady {
		t.Fatalf("gemini_provider_truth_state=%#v", root["gemini_provider_truth_state"])
	}
	if root["antigravity_project_id"] != "project-1" {
		t.Fatalf("antigravity_project_id=%#v", root["antigravity_project_id"])
	}
}

func TestLocalOperatorGeminiSeatAddAcceptsAntigravityAccountWrapper(t *testing.T) {
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
			t.Fatalf("antigravity import should not refresh during add")
			return nil, nil
		}),
	}

	authJSON := `{
		"id":"ag-1",
		"email":"ag@example.com",
		"name":"AG User",
		"proxy_disabled":false,
		"validation_blocked":false,
		"protected_models":["gemini-3.1-pro-high"],
		"quota":{
			"is_forbidden":false,
			"last_updated":1774353900,
			"model_forwarding_rules":{"gemini-1.5-pro":"gemini-2.5-pro"},
			"models":[
				{
					"name":"gemini-3.1-pro-high",
					"percentage":72,
					"reset_time":"2026-03-24T15:00:00Z",
					"display_name":"Gemini 3.1 Pro High",
					"supports_images":true,
					"supports_thinking":true,
					"thinking_budget":24576,
					"max_output_tokens":65535
				}
			]
		},
		"token":{
			"access_token":"seed-token",
			"refresh_token":"refresh-token",
			"expiry_timestamp":1774396800,
			"token_type":"Bearer",
			"project_id":"project-1"
		}
	}`
	outcome := addGeminiSeatFromAuthJSONForTest(t, h, authJSON)
	if outcome.HealthStatus != "project_only_unverified" {
		t.Fatalf("unexpected health_status: %+v", outcome)
	}
	if outcome.ProviderTruthState != geminiProviderTruthStateProjectOnlyUnverified {
		t.Fatalf("unexpected provider_truth_state: %+v", outcome)
	}
	if outcome.ProviderProjectID != "project-1" {
		t.Fatalf("unexpected provider_project_id: %+v", outcome)
	}
	root := readGeminiSeatRootForTest(t, poolDir, outcome.AccountID)
	if root["operator_source"] != geminiOperatorSourceAntigravityImport {
		t.Fatalf("operator_source=%#v", root["operator_source"])
	}
	if root["oauth_profile_id"] != geminiOAuthAntigravityProfileID {
		t.Fatalf("oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if root["antigravity_account_id"] != "ag-1" {
		t.Fatalf("antigravity_account_id=%#v", root["antigravity_account_id"])
	}
	if root["operator_email"] != "ag@example.com" {
		t.Fatalf("operator_email=%#v", root["operator_email"])
	}
	protectedModels, _ := root["gemini_protected_models"].([]any)
	if len(protectedModels) != 1 || protectedModels[0] != "gemini-3.1-pro-high" {
		t.Fatalf("gemini_protected_models=%#v", root["gemini_protected_models"])
	}
	if root["gemini_quota_updated_at"] != "2026-03-24T12:05:00Z" {
		t.Fatalf("gemini_quota_updated_at=%#v", root["gemini_quota_updated_at"])
	}
	quotaModels, _ := root["gemini_quota_models"].([]any)
	if len(quotaModels) != 1 {
		t.Fatalf("gemini_quota_models=%#v", root["gemini_quota_models"])
	}
	quotaModel, _ := quotaModels[0].(map[string]any)
	if quotaModel["name"] != "gemini-3.1-pro-high" || quotaModel["max_output_tokens"] != float64(65535) {
		t.Fatalf("gemini_quota_models[0]=%#v", quotaModel)
	}
	forwardingRules, _ := root["gemini_model_forwarding_rules"].(map[string]any)
	if forwardingRules["gemini-1.5-pro"] != "gemini-2.5-pro" {
		t.Fatalf("gemini_model_forwarding_rules=%#v", root["gemini_model_forwarding_rules"])
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
	outcome := addGeminiSeatFromAuthJSONForTest(t, h, authJSON)
	if outcome.HealthStatus != "dead" {
		t.Fatalf("unexpected health_status: %+v", outcome)
	}
	if !outcome.Dead {
		t.Fatalf("expected dead=true, got %+v", outcome)
	}
}

func TestLocalOperatorGeminiSeatAddMarksMissingProjectIDAsProbeFailure(t *testing.T) {
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
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return gitlabClaudeJSONResponse(http.StatusOK, `{"access_token":"fresh-token","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{"done":true,"response":{}}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{}`), nil
			default:
				t.Fatalf("unexpected refresh URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	authJSON := `{"access_token":"seed-token","refresh_token":"refresh-token","expiry_date":1774353600000}`
	outcome := addGeminiSeatFromAuthJSONForTest(t, h, authJSON)
	if outcome.ProbeOK {
		t.Fatalf("expected probe_ok=false, got %+v", outcome)
	}
	if outcome.HealthStatus != "missing_project_id" {
		t.Fatalf("unexpected health_status: %+v", outcome)
	}
	if outcome.ProviderTruthReady {
		t.Fatalf("unexpected provider_truth_ready: %+v", outcome)
	}
	if outcome.ProviderTruthState != geminiProviderTruthStateMissingProjectID {
		t.Fatalf("unexpected provider_truth_state: %+v", outcome)
	}
	if outcome.Dead {
		t.Fatalf("expected dead=false, got %+v", outcome)
	}
	if !strings.Contains(outcome.ProbeError, "missing_project_id") {
		t.Fatalf("expected missing_project_id probe_error, got %+v", outcome)
	}

	root := readGeminiSeatRootForTest(t, poolDir, outcome.AccountID)
	if root["health_status"] != "missing_project_id" {
		t.Fatalf("saved health_status=%#v", root["health_status"])
	}
	if root["gemini_provider_truth_ready"] != false {
		t.Fatalf("saved gemini_provider_truth_ready=%#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateMissingProjectID {
		t.Fatalf("saved gemini_provider_truth_state=%#v", root["gemini_provider_truth_state"])
	}
	if _, ok := root["gemini_provider_checked_at"]; !ok {
		t.Fatalf("expected gemini_provider_checked_at to be persisted: %#v", root)
	}
}

func TestLocalOperatorGeminiSeatAddMarksValidationBlockedSeatNonHealthy(t *testing.T) {
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
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return gitlabClaudeJSONResponse(http.StatusOK, `{"access_token":"fresh-token","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusBadRequest, `{"error":"validation required"}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{
					"ineligibleTiers":[{
						"reasonCode":"ACCOUNT_VALIDATION_REQUIRED",
						"reasonMessage":"Validate your account",
						"validationUrl":"https://example.com/verify"
					}]
				}`), nil
			default:
				t.Fatalf("unexpected refresh URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	authJSON := `{"access_token":"seed-token","refresh_token":"refresh-token","expiry_date":1774353600000}`
	outcome := addGeminiSeatFromAuthJSONForTest(t, h, authJSON)
	if outcome.ProbeOK {
		t.Fatalf("expected probe_ok=false, got %+v", outcome)
	}
	if outcome.HealthStatus != "validation_blocked" {
		t.Fatalf("unexpected health_status: %+v", outcome)
	}
	if outcome.ProviderTruthState != geminiProviderTruthStateValidationBlocked {
		t.Fatalf("unexpected provider_truth_state: %+v", outcome)
	}
	if outcome.ProviderTruthReady {
		t.Fatalf("unexpected provider_truth_ready: %+v", outcome)
	}
	if outcome.Dead {
		t.Fatalf("expected dead=false, got %+v", outcome)
	}
	if !strings.Contains(outcome.ProviderTruthReason, "Validate your account") {
		t.Fatalf("unexpected provider_truth_reason: %+v", outcome)
	}

	root := readGeminiSeatRootForTest(t, poolDir, outcome.AccountID)
	if root["health_status"] != "validation_blocked" {
		t.Fatalf("saved health_status=%#v", root["health_status"])
	}
	if root["antigravity_validation_blocked"] != true {
		t.Fatalf("saved antigravity_validation_blocked=%#v", root["antigravity_validation_blocked"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateValidationBlocked {
		t.Fatalf("saved gemini_provider_truth_state=%#v", root["gemini_provider_truth_state"])
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
	outcome := addGeminiSeatFromAuthJSONForTest(t, h, authJSON)
	if outcome.HealthStatus != "rate_limited" {
		t.Fatalf("unexpected health_status: %+v", outcome)
	}
	if outcome.Dead {
		t.Fatalf("expected dead=false after sanitizing provided seat state, got %+v", outcome)
	}
	if outcome.AccountID == "" {
		t.Fatalf("missing account_id: %+v", outcome)
	}
	root := readGeminiSeatRootForTest(t, poolDir, outcome.AccountID)
	if _, ok := root["disabled"]; ok {
		t.Fatalf("expected provided disabled flag to be cleared: %#v", root)
	}
	if _, ok := root["dead"]; ok {
		t.Fatalf("expected provided dead flag to be cleared for rate-limited seat: %#v", root)
	}
	if root["health_status"] != "rate_limited" {
		t.Fatalf("saved health_status=%#v", root["health_status"])
	}
}

func TestAddGeminiSeatFromAuthJSONRejectsNullAuthJSON(t *testing.T) {
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

	_, err = h.addGeminiSeatFromAuthJSON(context.Background(), "null")
	if err == nil {
		t.Fatal("expected null auth_json to be rejected")
	}
	if !strings.Contains(err.Error(), "auth_json must be a JSON object") {
		t.Fatalf("unexpected error=%v", err)
	}
}

func TestLocalOperatorGeminiLegacyImportRoutesRemoved(t *testing.T) {
	h := &proxyHandler{}
	for _, path := range []string{
		"http://127.0.0.1/operator/gemini/account-add",
		"http://127.0.0.1/operator/gemini/import-oauth-creds",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Host = "127.0.0.1:8989"
		req.RemoteAddr = "127.0.0.1:4242"
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
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

func TestLocalOperatorGeminiAntigravityOAuthStartAllowsLoopbackWithoutAdminHeader(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/antigravity/oauth-start", strings.NewReader(`{}`))
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
	parsed, err := url.Parse(oauthURL)
	if err != nil {
		t.Fatalf("parse oauth_url: %v", err)
	}
	query := parsed.Query()
	if got := query.Get("client_id"); got != geminiOAuthAntigravityClientID {
		t.Fatalf("client_id=%q", got)
	}
	if got := query.Get("redirect_uri"); got != "http://localhost:8989/oauth-callback" {
		t.Fatalf("redirect_uri=%q", got)
	}
	if got := query.Get("response_type"); got != "code" {
		t.Fatalf("response_type=%q", got)
	}
	if got := query.Get("access_type"); got != "offline" {
		t.Fatalf("access_type=%q", got)
	}
	if got := query.Get("include_granted_scopes"); got != "true" {
		t.Fatalf("include_granted_scopes=%q", got)
	}
	if got := query.Get("prompt"); got != "consent" {
		t.Fatalf("prompt=%q", got)
	}
	scopes := map[string]struct{}{}
	for _, scope := range strings.Fields(query.Get("scope")) {
		scopes[scope] = struct{}{}
	}
	for _, scope := range []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog",
		"https://www.googleapis.com/auth/experimentsandconfigs",
	} {
		if _, ok := scopes[scope]; !ok {
			t.Fatalf("missing scope %q in %q", scope, query.Get("scope"))
		}
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

func TestAntigravityGeminiOAuthCallbackRejectsExpiredState(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions["expired-state"] = &antigravityGeminiOAuthSession{
		State:       "expired-state",
		RedirectURI: "http://localhost:8989/oauth-callback",
		CreatedAt:   time.Now().Add(-antigravityOAuthSessionTTL - time.Minute).UTC(),
	}
	antigravityGeminiOAuthSessions.Unlock()

	h := &proxyHandler{}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth-callback?code=test-code&state=expired-state", nil)
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
	antigravityGeminiOAuthSessions.Lock()
	_, ok := antigravityGeminiOAuthSessions.sessions["expired-state"]
	antigravityGeminiOAuthSessions.Unlock()
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
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{"done":true,"response":{"cloudaicompanionProject":{"id":"project-1"}}}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{"cloudaicompanionProject":"project-1","currentTier":{"id":"standard-tier","name":"Standard"}}`), nil
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
	if root["gemini_provider_truth_ready"] != true {
		t.Fatalf("saved gemini_provider_truth_ready=%#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateReady {
		t.Fatalf("saved gemini_provider_truth_state=%#v", root["gemini_provider_truth_state"])
	}
}

func TestLocalOperatorGeminiAntigravityOAuthCallbackStoresImportedSeat(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	redirectURI := "http://localhost:8989/oauth-callback"
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
				if values.Get("grant_type") != "authorization_code" {
					t.Fatalf("grant_type=%q", values.Get("grant_type"))
				}
				if values.Get("redirect_uri") != redirectURI {
					t.Fatalf("redirect_uri=%q", values.Get("redirect_uri"))
				}
				if values.Get("client_id") != geminiOAuthAntigravityClientID {
					t.Fatalf("client_id=%q", values.Get("client_id"))
				}
				return jsonResponse(http.StatusOK, `{"access_token":"oauth-access","refresh_token":"oauth-refresh","token_type":"Bearer","scope":"scope","expires_in":3600}`), nil
			case managedGeminiOAuthUserInfoURL:
				return jsonResponse(http.StatusOK, `{"email":"seat@example.com","name":"Seat Example"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{"done":true,"response":{"cloudaicompanionProject":{"id":"psyched-sphere-vj8c5"}}}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{"cloudaicompanionProject":"psyched-sphere-vj8c5","currentTier":{"id":"standard-tier","name":"Standard"}}`), nil
			case "https://api.example.com/v1internal:fetchAvailableModels":
				return jsonResponse(http.StatusOK, `{"last_updated":1774353900}`), nil
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions = map[string]*antigravityGeminiOAuthSession{
		"state-1": {
			State:       "state-1",
			RedirectURI: redirectURI,
			CreatedAt:   time.Now().UTC(),
		},
	}
	antigravityGeminiOAuthSessions.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth-callback?code=test-code&state=state-1", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Antigravity Gemini seat added") {
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
	if root["operator_source"] != geminiOperatorSourceAntigravityImport {
		t.Fatalf("saved operator_source=%#v", root["operator_source"])
	}
	if root["oauth_profile_id"] != geminiOAuthAntigravityProfileID {
		t.Fatalf("saved oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected saved client_id to be dropped once oauth_profile_id is persisted: %#v", root["client_id"])
	}
	if root["antigravity_project_id"] != "psyched-sphere-vj8c5" {
		t.Fatalf("saved antigravity_project_id=%#v", root["antigravity_project_id"])
	}
	if root["antigravity_source"] != "browser_oauth" {
		t.Fatalf("saved antigravity_source=%#v", root["antigravity_source"])
	}
	if root["operator_email"] != "seat@example.com" {
		t.Fatalf("saved operator_email=%#v", root["operator_email"])
	}
	if root["gemini_subscription_tier_id"] != "standard-tier" {
		t.Fatalf("saved gemini_subscription_tier_id=%#v", root["gemini_subscription_tier_id"])
	}
	if root["gemini_subscription_tier_name"] != "Standard" {
		t.Fatalf("saved gemini_subscription_tier_name=%#v", root["gemini_subscription_tier_name"])
	}
	if _, ok := root["gemini_provider_checked_at"]; !ok {
		t.Fatalf("expected gemini_provider_checked_at to be persisted: %#v", root)
	}
	if root["gemini_provider_truth_ready"] != true {
		t.Fatalf("saved gemini_provider_truth_ready=%#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateReady {
		t.Fatalf("saved gemini_provider_truth_state=%#v", root["gemini_provider_truth_state"])
	}
}

func TestLocalOperatorGeminiAntigravityOAuthCallbackBootstrapsOnboardBeforeLoadCodeAssist(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	redirectURI := "http://localhost:8989/oauth-callback"
	var calls []string
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return jsonResponse(http.StatusOK, `{"access_token":"oauth-access","refresh_token":"oauth-refresh","token_type":"Bearer","scope":"scope","expires_in":3600}`), nil
			case managedGeminiOAuthUserInfoURL:
				return jsonResponse(http.StatusOK, `{"email":"seat@example.com","name":"Seat Example"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				calls = append(calls, "onboard")
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read onboard body: %v", err)
				}
				if !strings.Contains(string(body), `"tierId":"standard-tier"`) {
					t.Fatalf("unexpected onboard payload=%s", string(body))
				}
				return jsonResponse(http.StatusOK, `{"done":true,"response":{"cloudaicompanionProject":{"id":"psyched-sphere-vj8c5"}}}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				calls = append(calls, "load")
				return jsonResponse(http.StatusOK, `{"cloudaicompanionProject":"psyched-sphere-vj8c5"}`), nil
			case "https://api.example.com/v1internal:fetchAvailableModels":
				return jsonResponse(http.StatusOK, `{"models":{}}`), nil
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions = map[string]*antigravityGeminiOAuthSession{
		"state-1": {
			State:       "state-1",
			RedirectURI: redirectURI,
			CreatedAt:   time.Now().UTC(),
		},
	}
	antigravityGeminiOAuthSessions.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth-callback?code=test-code&state=state-1", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(calls) == 0 || calls[0] != "onboard" {
		t.Fatalf("call order=%v, want onboard first", calls)
	}
}

func TestLocalOperatorGeminiAntigravityOAuthCallbackStoresValidationBlockedSeat(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	redirectURI := "http://localhost:8989/oauth-callback"
	var fetchPayloads []map[string]any
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return jsonResponse(http.StatusOK, `{"access_token":"oauth-access","refresh_token":"oauth-refresh","token_type":"Bearer","scope":"scope","expires_in":3600}`), nil
			case managedGeminiOAuthUserInfoURL:
				return jsonResponse(http.StatusOK, `{"email":"seat@example.com","name":"Seat Example"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read onboard body: %v", err)
				}
				if !strings.Contains(string(body), `"tierId":"standard-tier"`) {
					t.Fatalf("unexpected onboard payload=%s", string(body))
				}
				return jsonResponse(http.StatusOK, `{"done":true}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{
					"ineligibleTiers": [{
						"reasonCode": "UNSUPPORTED_LOCATION",
						"reasonMessage": "Your current account is not eligible for Gemini Code Assist for individuals because it is not currently available in your location.",
						"validationUrl": "https://developers.google.com/gemini-code-assist/ui/faqs"
					}]
				}`), nil
			case "https://api.example.com/v1internal:fetchAvailableModels":
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read fetchAvailableModels body: %v", err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode fetchAvailableModels payload: %v body=%s", err, string(body))
				}
				fetchPayloads = append(fetchPayloads, payload)
				return jsonResponse(http.StatusOK, `{
					"models": {
						"gemini-3.1-pro-high": {
							"quotaInfo": {
								"remainingFraction": 0.61,
								"resetTime": "2026-03-24T15:00:00Z"
							},
							"displayName": "Gemini 3.1 Pro High",
							"supportsImages": true,
							"supportsThinking": true,
							"thinkingBudget": 24576,
							"recommended": true,
							"maxTokens": 1048576,
							"maxOutputTokens": 65535
						}
					}
				}`), nil
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions = map[string]*antigravityGeminiOAuthSession{
		"state-1": {
			State:       "state-1",
			RedirectURI: redirectURI,
			CreatedAt:   time.Now().UTC(),
		},
	}
	antigravityGeminiOAuthSessions.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth-callback?code=test-code&state=state-1", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Antigravity Gemini seat saved with provider block") {
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
	accountID := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	saved, err := os.ReadFile(filepath.Join(poolDir, managedGeminiSubdir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read saved seat: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode saved seat: %v", err)
	}
	if root["operator_source"] != geminiOperatorSourceAntigravityImport {
		t.Fatalf("saved operator_source=%#v", root["operator_source"])
	}
	if root["antigravity_source"] != "browser_oauth" {
		t.Fatalf("saved antigravity_source=%#v", root["antigravity_source"])
	}
	if root["antigravity_validation_blocked"] != true {
		t.Fatalf("saved antigravity_validation_blocked=%#v", root["antigravity_validation_blocked"])
	}
	if root["gemini_validation_reason_code"] != "UNSUPPORTED_LOCATION" {
		t.Fatalf("saved gemini_validation_reason_code=%#v", root["gemini_validation_reason_code"])
	}
	if _, ok := root["antigravity_project_id"]; ok {
		t.Fatalf("expected antigravity_project_id to stay absent: %#v", root["antigravity_project_id"])
	}
	if _, ok := root["gemini_provider_checked_at"]; !ok {
		t.Fatalf("expected gemini_provider_checked_at to be persisted: %#v", root)
	}
	if root["gemini_provider_truth_ready"] != false {
		t.Fatalf("saved gemini_provider_truth_ready=%#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateRestricted {
		t.Fatalf("saved gemini_provider_truth_state=%#v", root["gemini_provider_truth_state"])
	}
	if len(fetchPayloads) != 1 {
		t.Fatalf("fetchAvailableModels payloads=%v", fetchPayloads)
	}
	if len(fetchPayloads[0]) != 0 {
		t.Fatalf("expected empty fetchAvailableModels payload for validation-blocked seat, got %#v", fetchPayloads[0])
	}
	quotaModels, _ := root["gemini_quota_models"].([]any)
	if len(quotaModels) != 1 {
		t.Fatalf("saved gemini_quota_models=%#v", root["gemini_quota_models"])
	}

	snapshot, ok := h.snapshotAccountByID(accountID, time.Now())
	if !ok {
		t.Fatalf("snapshotAccountByID(%q) missing", accountID)
	}
	if snapshot.HealthStatus != "restricted" {
		t.Fatalf("health_status=%q", snapshot.HealthStatus)
	}
	if snapshot.GeminiProviderTruthState != geminiProviderTruthStateRestricted {
		t.Fatalf("provider_truth_state=%q", snapshot.GeminiProviderTruthState)
	}
}

func TestLocalOperatorGeminiAntigravityOAuthCallbackStoresMissingProjectSeat(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	redirectURI := "http://localhost:8989/oauth-callback"
	var fetchPayloads []map[string]any
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return jsonResponse(http.StatusOK, `{"access_token":"oauth-access","refresh_token":"oauth-refresh","token_type":"Bearer","scope":"scope","expires_in":3600}`), nil
			case managedGeminiOAuthUserInfoURL:
				return jsonResponse(http.StatusOK, `{"email":"seat@example.com","name":"Seat Example"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{"done":true}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{}`), nil
			case "https://api.example.com/v1internal:fetchAvailableModels":
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read fetchAvailableModels body: %v", err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode fetchAvailableModels payload: %v body=%s", err, string(body))
				}
				fetchPayloads = append(fetchPayloads, payload)
				return jsonResponse(http.StatusOK, `{
					"models": {
						"gemini-3.1-pro-high": {
							"quotaInfo": {
								"remainingFraction": 0.72,
								"resetTime": "2026-03-24T15:00:00Z"
							},
							"displayName": "Gemini 3.1 Pro High",
							"supportsImages": true,
							"supportsThinking": true,
							"thinkingBudget": 24576,
							"recommended": true,
							"maxTokens": 1048576,
							"maxOutputTokens": 65535
						}
					}
				}`), nil
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions = map[string]*antigravityGeminiOAuthSession{
		"state-1": {
			State:       "state-1",
			RedirectURI: redirectURI,
			CreatedAt:   time.Now().UTC(),
		},
	}
	antigravityGeminiOAuthSessions.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth-callback?code=test-code&state=state-1", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Antigravity Gemini seat saved with provider block") {
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
	accountID := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	saved, err := os.ReadFile(filepath.Join(poolDir, managedGeminiSubdir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read saved seat: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode saved seat: %v", err)
	}
	if root["health_status"] != "missing_project_id" {
		t.Fatalf("saved health_status=%#v", root["health_status"])
	}
	if root["gemini_provider_truth_ready"] != false {
		t.Fatalf("saved gemini_provider_truth_ready=%#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateMissingProjectID {
		t.Fatalf("saved gemini_provider_truth_state=%#v", root["gemini_provider_truth_state"])
	}
	if _, ok := root["gemini_provider_checked_at"]; !ok {
		t.Fatalf("expected gemini_provider_checked_at to be persisted: %#v", root)
	}
	if _, ok := root["antigravity_project_id"]; ok {
		t.Fatalf("expected antigravity_project_id to stay absent: %#v", root["antigravity_project_id"])
	}
	if len(fetchPayloads) != 1 {
		t.Fatalf("fetchAvailableModels payloads=%v", fetchPayloads)
	}
	if len(fetchPayloads[0]) != 0 {
		t.Fatalf("expected empty fetchAvailableModels payload for missing-project seat, got %#v", fetchPayloads[0])
	}
	quotaModels, _ := root["gemini_quota_models"].([]any)
	if len(quotaModels) != 1 {
		t.Fatalf("saved gemini_quota_models=%#v", root["gemini_quota_models"])
	}

	snapshot, ok := h.snapshotAccountByID(accountID, time.Now())
	if !ok {
		t.Fatalf("snapshotAccountByID(%q) missing", accountID)
	}
	if snapshot.HealthStatus != "missing_project_id" {
		t.Fatalf("health_status=%q", snapshot.HealthStatus)
	}
	if snapshot.GeminiProviderTruthState != geminiProviderTruthStateMissingProjectID {
		t.Fatalf("provider_truth_state=%q", snapshot.GeminiProviderTruthState)
	}
}

func TestLocalOperatorGeminiAntigravityOAuthCallbackPersistsFetchedQuotaWithFallback(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	redirectURI := "http://localhost:8989/oauth-callback"
	var fetchCalls []string
	var fetchPayloads []map[string]any
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return jsonResponse(http.StatusOK, `{"access_token":"oauth-access","refresh_token":"oauth-refresh","token_type":"Bearer","scope":"scope","expires_in":3600}`), nil
			case managedGeminiOAuthUserInfoURL:
				return jsonResponse(http.StatusOK, `{"email":"seat@example.com","name":"Seat Example"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{"done":true,"response":{"cloudaicompanionProject":{"id":"psyched-sphere-vj8c5"}}}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{"cloudaicompanionProject":"psyched-sphere-vj8c5","currentTier":{"id":"standard-tier","name":"Standard"}}`), nil
			case "https://api.example.com/v1internal:fetchAvailableModels":
				fetchCalls = append(fetchCalls, req.URL.String())
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read fetchAvailableModels body: %v", err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode fetchAvailableModels payload: %v body=%s", err, string(body))
				}
				fetchPayloads = append(fetchPayloads, payload)
				return jsonResponse(http.StatusBadGateway, `{"error":"upstream down"}`), nil
			case "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels":
				fetchCalls = append(fetchCalls, req.URL.String())
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read sandbox fetchAvailableModels body: %v", err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode sandbox fetchAvailableModels payload: %v body=%s", err, string(body))
				}
				fetchPayloads = append(fetchPayloads, payload)
				return jsonResponse(http.StatusTooManyRequests, `{"error":"sandbox throttled"}`), nil
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels":
				fetchCalls = append(fetchCalls, req.URL.String())
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read daily fetchAvailableModels body: %v", err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode daily fetchAvailableModels payload: %v body=%s", err, string(body))
				}
				fetchPayloads = append(fetchPayloads, payload)
				return jsonResponse(http.StatusOK, `{
					"models": {
						"gemini-3.1-pro-high": {
							"quotaInfo": {
								"remainingFraction": 0.67,
								"resetTime": "2026-03-24T15:00:00Z"
							},
							"displayName": "Gemini 3.1 Pro High",
							"supportsImages": true,
							"supportsThinking": true,
							"thinkingBudget": 24576,
							"recommended": true,
							"maxTokens": 1048576,
							"maxOutputTokens": 65535,
							"supportedMimeTypes": {
								"image/png": true
							}
						}
					},
					"deprecatedModelIds": {
						"gemini-1.5-pro": {
							"newModelId": "gemini-2.5-pro"
						}
					}
				}`), nil
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions = map[string]*antigravityGeminiOAuthSession{
		"state-1": {
			State:       "state-1",
			RedirectURI: redirectURI,
			CreatedAt:   time.Now().UTC(),
		},
	}
	antigravityGeminiOAuthSessions.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth-callback?code=test-code&state=state-1", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(fetchCalls) < 3 {
		t.Fatalf("fetchAvailableModels calls=%v", fetchCalls)
	}
	if fetchCalls[0] != "https://api.example.com/v1internal:fetchAvailableModels" {
		t.Fatalf("unexpected first fetchAvailableModels call=%q", fetchCalls[0])
	}
	if fetchCalls[len(fetchCalls)-1] != "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels" {
		t.Fatalf("unexpected final fetchAvailableModels call=%q", fetchCalls[len(fetchCalls)-1])
	}
	if len(fetchPayloads) != len(fetchCalls) {
		t.Fatalf("fetchAvailableModels payloads=%v", fetchPayloads)
	}
	sawProjectPayload := false
	for idx, payload := range fetchPayloads {
		if project, ok := payload["project"]; ok {
			if project != "psyched-sphere-vj8c5" {
				t.Fatalf("fetchAvailableModels payload[%d]=%#v", idx, payload)
			}
			sawProjectPayload = true
		}
		if _, ok := payload["cloudaicompanionProject"]; ok {
			t.Fatalf("unexpected legacy fetchAvailableModels payload[%d]=%#v", idx, payload)
		}
	}
	if !sawProjectPayload {
		t.Fatalf("expected at least one project-aware fetchAvailableModels payload, got %v", fetchPayloads)
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
	quotaUpdatedAt, _ := root["gemini_quota_updated_at"].(string)
	if quotaUpdatedAt == "" {
		t.Fatalf("saved gemini_quota_updated_at=%#v", root["gemini_quota_updated_at"])
	}
	if _, err := time.Parse(time.RFC3339, quotaUpdatedAt); err != nil {
		t.Fatalf("parse saved gemini_quota_updated_at=%q: %v", quotaUpdatedAt, err)
	}
	quotaModels, _ := root["gemini_quota_models"].([]any)
	if len(quotaModels) != 1 {
		t.Fatalf("saved gemini_quota_models=%#v", root["gemini_quota_models"])
	}
	quotaModel, _ := quotaModels[0].(map[string]any)
	if quotaModel["name"] != "gemini-3.1-pro-high" || quotaModel["percentage"] != float64(67) || quotaModel["display_name"] != "Gemini 3.1 Pro High" {
		t.Fatalf("saved gemini_quota_models[0]=%#v", quotaModel)
	}
	forwardingRules, _ := root["gemini_model_forwarding_rules"].(map[string]any)
	if forwardingRules["gemini-1.5-pro"] != "gemini-2.5-pro" {
		t.Fatalf("saved gemini_model_forwarding_rules=%#v", root["gemini_model_forwarding_rules"])
	}

	accountID := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	snapshot, ok := h.snapshotAccountByID(accountID, time.Now())
	if !ok {
		t.Fatalf("snapshotAccountByID(%q) missing", accountID)
	}
	if snapshot.GeminiQuotaUpdatedAt.IsZero() {
		t.Fatal("GeminiQuotaUpdatedAt is zero")
	}
	if len(snapshot.GeminiQuotaModels) != 1 || snapshot.GeminiQuotaModels[0].Name != "gemini-3.1-pro-high" {
		t.Fatalf("GeminiQuotaModels=%#v", snapshot.GeminiQuotaModels)
	}
}

func TestLocalOperatorGeminiAntigravityOAuthCallbackMarksQuotaForbiddenWithoutFallback(t *testing.T) {
	resetAntigravityGeminiOAuthSessions()
	t.Cleanup(resetAntigravityGeminiOAuthSessions)

	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	redirectURI := "http://localhost:8989/oauth-callback"
	var fetchCalls []string
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState(nil, false),
		registry: NewProviderRegistry(codex, claude, gemini),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case geminiOAuthTokenURL:
				return jsonResponse(http.StatusOK, `{"access_token":"oauth-access","refresh_token":"oauth-refresh","token_type":"Bearer","scope":"scope","expires_in":3600}`), nil
			case managedGeminiOAuthUserInfoURL:
				return jsonResponse(http.StatusOK, `{"email":"seat@example.com","name":"Seat Example"}`), nil
			case "https://api.example.com/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{"done":true,"response":{"cloudaicompanionProject":{"id":"psyched-sphere-vj8c5"}}}`), nil
			case "https://api.example.com/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{"cloudaicompanionProject":"psyched-sphere-vj8c5","currentTier":{"id":"standard-tier","name":"Standard"}}`), nil
			case "https://api.example.com/v1internal:fetchAvailableModels":
				fetchCalls = append(fetchCalls, req.URL.String())
				return jsonResponse(http.StatusForbidden, `{"error":"quota blocked"}`), nil
			case "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels":
				t.Fatal("sandbox quota fallback should not run after 403")
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels":
				t.Fatal("daily quota fallback should not run after 403")
			case "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels":
				t.Fatal("prod quota fallback should not run after 403")
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}
			return nil, nil
		}),
	}

	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions = map[string]*antigravityGeminiOAuthSession{
		"state-1": {
			State:       "state-1",
			RedirectURI: redirectURI,
			CreatedAt:   time.Now().UTC(),
		},
	}
	antigravityGeminiOAuthSessions.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth-callback?code=test-code&state=state-1", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(fetchCalls) == 0 {
		t.Fatalf("fetchAvailableModels calls=%v", fetchCalls)
	}
	for _, call := range fetchCalls {
		if call != "https://api.example.com/v1internal:fetchAvailableModels" {
			t.Fatalf("unexpected fetchAvailableModels fallback call=%q", call)
		}
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
	quota, _ := root["antigravity_quota"].(map[string]any)
	if quota["is_forbidden"] != true {
		t.Fatalf("saved antigravity_quota=%#v", root["antigravity_quota"])
	}
	if _, ok := quota["last_updated"]; !ok {
		t.Fatalf("saved antigravity_quota missing last_updated=%#v", quota)
	}

	accountID := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	snapshot, ok := h.snapshotAccountByID(accountID, time.Now())
	if !ok {
		t.Fatalf("snapshotAccountByID(%q) missing", accountID)
	}
	if !snapshot.AntigravityQuotaForbidden {
		t.Fatal("expected AntigravityQuotaForbidden")
	}
	if snapshot.HealthStatus != "quota_forbidden" {
		t.Fatalf("health_status=%q", snapshot.HealthStatus)
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

func TestServeStatusPageJSONKeepsAllowlistedValidationBlockedGeminiTruth(t *testing.T) {
	now := time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)
	account := &Account{
		ID:                           "gemini-blocked",
		Type:                         AccountTypeGemini,
		PlanType:                     "gemini",
		AuthMode:                     accountAuthModeOAuth,
		OperatorSource:               geminiOperatorSourceAntigravityImport,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
		GeminiProviderTruthState:     geminiProviderTruthStateRestricted,
		GeminiProviderTruthReason:    "UNSUPPORTED_LOCATION",
		HealthStatus:                 "restricted",
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

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(payload.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(payload.Accounts))
	}
	accountStatus := payload.Accounts[0]
	if accountStatus.HealthStatus != "restricted" {
		t.Fatalf("health_status=%q", accountStatus.HealthStatus)
	}
	if accountStatus.Routing.Eligible {
		t.Fatalf("routing=%+v", accountStatus.Routing)
	}
	if accountStatus.Routing.BlockReason != "not_warmed" {
		t.Fatalf("block_reason=%q", accountStatus.Routing.BlockReason)
	}
	if accountStatus.Routing.State != routingDisplayStateBlocked {
		t.Fatalf("routing.state=%q", accountStatus.Routing.State)
	}
	if accountStatus.Routing.PrimaryHeadroomKnown || accountStatus.Routing.SecondaryHeadroomKnown {
		t.Fatalf("expected unknown Gemini headroom flags, got %+v", accountStatus.Routing)
	}
	if accountStatus.ProviderTruth == nil || !accountStatus.ProviderTruth.Restricted || !accountStatus.ProviderTruth.ValidationBlocked {
		t.Fatalf("provider_truth=%+v", accountStatus.ProviderTruth)
	}
	if payload.GeminiPool == nil {
		t.Fatal("expected gemini_pool aggregate")
	}
	if payload.GeminiPool.TotalSeats != 1 || payload.GeminiPool.NotWarmedSeats != 1 || payload.GeminiPool.ValidationFlaggedSeats != 1 {
		t.Fatalf("gemini_pool=%+v", payload.GeminiPool)
	}
	if payload.BestEligibleSeat != nil {
		t.Fatalf("best_eligible_seat=%+v", payload.BestEligibleSeat)
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
		"Antigravity Gemini Auth",
		"Start Antigravity Gemini Auth",
		"Add API Key",
		"openai-api-key-input",
		"/operator/codex/api-key-add",
		"/operator/gemini/antigravity/oauth-start",
		"/operator/account-delete",
		"deleteAccountFromStatus",
		"account-action-status",
		"/v1/responses",
		"POST /operator/codex/oauth-start",
		"/operator/codex/oauth-start",
		"Open OAuth Page",
		"keeps the popup opener attached",
		"refreshes this page automatically when pool seat state changes",
		"Waiting for pool seat state to change...",
		"Waiting for pool seat state to change.",
		"Waiting for the Antigravity Gemini seat state to change...",
		"Timed out waiting for the Antigravity Gemini seat state to change.",
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
		"Manual Gemini Import",
		"Import oauth_creds.json",
		"gemini-seat-json-input",
		"/operator/gemini/import-oauth-creds",
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
		"POST /operator/codex/oauth-start",
		"/operator/codex/oauth-start",
		"Fallback API Pool",
		"Antigravity Gemini Auth",
		"Start Antigravity Gemini Auth",
		"/operator/codex/api-key-add",
		"/operator/gemini/antigravity/oauth-start",
		"/operator/account-delete",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in body", forbidden)
		}
	}
}
