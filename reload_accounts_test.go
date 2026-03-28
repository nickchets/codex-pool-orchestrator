package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReloadAccountsPreservesRuntimeState(t *testing.T) {
	tmp := t.TempDir()
	poolDir := filepath.Join(tmp, "codex")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatalf("mkdir pool dir: %v", err)
	}

	authPath := filepath.Join(poolDir, "seat-a.json")
	auth := map[string]any{
		"tokens": map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"account_id":    "workspace-a",
		},
	}
	buf, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(authPath, buf, 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	handler := &proxyHandler{
		cfg: config{poolDir: tmp},
		pool: newPoolState([]*Account{{
			ID:           "seat-a",
			Type:         AccountTypeCodex,
			File:         authPath,
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			Usage: UsageSnapshot{
				PrimaryUsedPercent:   0.42,
				SecondaryUsedPercent: 0.84,
				PrimaryUsed:          0.42,
				SecondaryUsed:        0.84,
				PrimaryResetAt:       now.Add(90 * time.Minute),
				SecondaryResetAt:     now.Add(12 * time.Hour),
				RetrievedAt:          now.Add(-2 * time.Minute),
				Source:               "wham",
			},
			Penalty:        1.5,
			LastPenalty:    now.Add(-5 * time.Minute),
			LastUsed:       now.Add(-30 * time.Second),
			RateLimitUntil: now.Add(10 * time.Minute),
			Totals: AccountUsage{
				TotalBillableTokens: 1234,
				RequestCount:        7,
				LastPrimaryPct:      0.42,
				LastSecondaryPct:    0.84,
				LastUpdated:         now.Add(-30 * time.Second),
			},
		}}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeCodex: &CodexProvider{},
			},
		},
	}

	handler.reloadAccounts()

	if handler.pool.count() != 1 {
		t.Fatalf("reloaded accounts=%d", handler.pool.count())
	}

	reloaded := handler.pool.allAccounts()[0]
	if reloaded.AccessToken != "new-access" {
		t.Fatalf("access token not refreshed from disk: %q", reloaded.AccessToken)
	}
	if reloaded.RefreshToken != "new-refresh" {
		t.Fatalf("refresh token not refreshed from disk: %q", reloaded.RefreshToken)
	}
	if reloaded.Usage.PrimaryUsedPercent != 0.42 || reloaded.Usage.SecondaryUsedPercent != 0.84 {
		t.Fatalf("usage lost across reload: %+v", reloaded.Usage)
	}
	if reloaded.Usage.Source != "wham" {
		t.Fatalf("usage source=%q", reloaded.Usage.Source)
	}
	if reloaded.Penalty != 1.5 {
		t.Fatalf("penalty=%v", reloaded.Penalty)
	}
	if !reloaded.LastPenalty.Equal(now.Add(-5 * time.Minute)) {
		t.Fatalf("last penalty=%v", reloaded.LastPenalty)
	}
	if !reloaded.LastUsed.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("last used=%v", reloaded.LastUsed)
	}
	if !reloaded.RateLimitUntil.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("rate limit until=%v", reloaded.RateLimitUntil)
	}
	if reloaded.Totals.TotalBillableTokens != 1234 || reloaded.Totals.RequestCount != 7 {
		t.Fatalf("totals lost across reload: %+v", reloaded.Totals)
	}
}

func TestReloadAccountsKeepsCodexOAuthPersistedHealthState(t *testing.T) {
	tmp := t.TempDir()
	poolDir := filepath.Join(tmp, "codex")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatalf("mkdir pool dir: %v", err)
	}

	authPath := filepath.Join(poolDir, "seat-health.json")
	healthCheckedAt := time.Date(2026, 3, 28, 14, 0, 0, 0, time.UTC)
	lastHealthyAt := time.Date(2026, 3, 28, 13, 30, 0, 0, time.UTC)
	auth := map[string]any{
		"tokens": map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"account_id":    "workspace-a",
		},
		"health_status":     codexRefreshInvalidHealthStatus,
		"health_error":      codexRefreshInvalidHealthError,
		"health_checked_at": healthCheckedAt.Format(time.RFC3339),
		"last_healthy_at":   lastHealthyAt.Format(time.RFC3339),
	}
	buf, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(authPath, buf, 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	handler := &proxyHandler{
		cfg: config{poolDir: tmp},
		pool: newPoolState([]*Account{{
			ID:           "seat-health",
			Type:         AccountTypeCodex,
			File:         authPath,
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			Usage: UsageSnapshot{
				PrimaryUsedPercent:   0.18,
				SecondaryUsedPercent: 0.29,
				RetrievedAt:          now.Add(-time.Minute),
				Source:               "runtime",
			},
			Penalty:        1.75,
			LastPenalty:    now.Add(-5 * time.Minute),
			LastUsed:       now.Add(-30 * time.Second),
			RateLimitUntil: now.Add(10 * time.Minute),
			Totals: AccountUsage{
				TotalBillableTokens: 321,
				RequestCount:        4,
				LastPrimaryPct:      0.18,
				LastSecondaryPct:    0.29,
				LastUpdated:         now.Add(-30 * time.Second),
			},
		}}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeCodex: &CodexProvider{},
			},
		},
	}

	handler.reloadAccounts()

	if handler.pool.count() != 1 {
		t.Fatalf("reloaded accounts=%d", handler.pool.count())
	}

	reloaded := handler.pool.allAccounts()[0]
	if reloaded.HealthStatus != codexRefreshInvalidHealthStatus {
		t.Fatalf("health_status=%q", reloaded.HealthStatus)
	}
	if reloaded.HealthError != codexRefreshInvalidHealthError {
		t.Fatalf("health_error=%q", reloaded.HealthError)
	}
	if !reloaded.HealthCheckedAt.Equal(healthCheckedAt) {
		t.Fatalf("health_checked_at=%v", reloaded.HealthCheckedAt)
	}
	if !reloaded.LastHealthyAt.Equal(lastHealthyAt) {
		t.Fatalf("last_healthy_at=%v", reloaded.LastHealthyAt)
	}
	if reloaded.Dead {
		t.Fatal("expected codex seat to reload as non-dead")
	}
	if !reloaded.DeadSince.IsZero() {
		t.Fatalf("dead_since=%v", reloaded.DeadSince)
	}
	if reloaded.Usage.PrimaryUsedPercent != 0.18 || reloaded.Usage.SecondaryUsedPercent != 0.29 {
		t.Fatalf("usage lost across reload: %+v", reloaded.Usage)
	}
	if !reloaded.LastUsed.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("last used=%v", reloaded.LastUsed)
	}
	if reloaded.Totals.TotalBillableTokens != 321 || reloaded.Totals.RequestCount != 4 {
		t.Fatalf("totals lost across reload: %+v", reloaded.Totals)
	}
}

func TestReloadAccountsKeepsGeminiPersistedStateWhilePreservingRuntimeUsage(t *testing.T) {
	tmp := t.TempDir()
	poolDir := filepath.Join(tmp, "gemini")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatalf("mkdir pool dir: %v", err)
	}

	authPath := filepath.Join(poolDir, "seat-a.json")
	healthCheckedAt := time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC)
	lastHealthyAt := time.Date(2026, 3, 24, 8, 30, 0, 0, time.UTC)
	rateLimitUntil := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	auth := map[string]any{
		"access_token":                "new-access",
		"refresh_token":               "new-refresh",
		"oauth_profile_id":            "gcloud",
		"rate_limit_until":            rateLimitUntil.Format(time.RFC3339),
		"health_status":               "rate_limited",
		"health_error":                "quota",
		"health_checked_at":           healthCheckedAt.Format(time.RFC3339),
		"last_healthy_at":             lastHealthyAt.Format(time.RFC3339),
		"antigravity_project_id":      "project-1",
		"gemini_provider_checked_at":  "2026-03-24T10:05:00Z",
		"gemini_subscription_tier_id": "standard-tier",
		"gemini_protected_models":     []string{"gemini-3.1-pro-high"},
		"gemini_quota_updated_at":     "2026-03-24T10:06:00Z",
		"gemini_quota_models": []map[string]any{{
			"name":              "gemini-3.1-pro-high",
			"percentage":        81,
			"reset_time":        "2026-03-24T16:00:00Z",
			"thinking_budget":   24576,
			"max_output_tokens": 65535,
			"supports_thinking": true,
			"supports_images":   true,
			"display_name":      "Gemini 3.1 Pro High",
		}},
		"gemini_model_forwarding_rules": map[string]string{
			"gemini-1.5-pro": "gemini-2.5-pro",
		},
	}
	buf, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(authPath, buf, 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	handler := &proxyHandler{
		cfg: config{poolDir: tmp},
		pool: newPoolState([]*Account{{
			ID:           "seat-a",
			Type:         AccountTypeGemini,
			File:         authPath,
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			Usage: UsageSnapshot{
				PrimaryUsedPercent:   0.12,
				SecondaryUsedPercent: 0.34,
				PrimaryUsed:          0.12,
				SecondaryUsed:        0.34,
				RetrievedAt:          now.Add(-time.Minute),
				Source:               "runtime",
			},
			Penalty:     1.25,
			LastPenalty: now.Add(-5 * time.Minute),
			LastUsed:    now.Add(-30 * time.Second),
			Totals: AccountUsage{
				TotalBillableTokens: 4321,
				RequestCount:        9,
				LastPrimaryPct:      0.12,
				LastSecondaryPct:    0.34,
				LastUpdated:         now.Add(-30 * time.Second),
			},
		}}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeGemini: &GeminiProvider{},
			},
		},
	}

	handler.reloadAccounts()

	if handler.pool.count() != 1 {
		t.Fatalf("reloaded accounts=%d", handler.pool.count())
	}

	reloaded := handler.pool.allAccounts()[0]
	if reloaded.AccessToken != "new-access" {
		t.Fatalf("access token not refreshed from disk: %q", reloaded.AccessToken)
	}
	if reloaded.RefreshToken != "new-refresh" {
		t.Fatalf("refresh token not refreshed from disk: %q", reloaded.RefreshToken)
	}
	if reloaded.OAuthProfileID != "gcloud" {
		t.Fatalf("oauth_profile_id=%q", reloaded.OAuthProfileID)
	}
	if reloaded.HealthStatus != "rate_limited" {
		t.Fatalf("health_status=%q", reloaded.HealthStatus)
	}
	if reloaded.HealthError != "quota" {
		t.Fatalf("health_error=%q", reloaded.HealthError)
	}
	if !reloaded.HealthCheckedAt.Equal(healthCheckedAt) {
		t.Fatalf("health_checked_at=%v", reloaded.HealthCheckedAt)
	}
	if !reloaded.LastHealthyAt.Equal(lastHealthyAt) {
		t.Fatalf("last_healthy_at=%v", reloaded.LastHealthyAt)
	}
	if !reloaded.RateLimitUntil.Equal(rateLimitUntil) {
		t.Fatalf("rate_limit_until=%v", reloaded.RateLimitUntil)
	}
	if reloaded.AntigravityProjectID != "project-1" {
		t.Fatalf("antigravity_project_id=%q", reloaded.AntigravityProjectID)
	}
	if reloaded.GeminiProviderTruthState != geminiProviderTruthStateReady {
		t.Fatalf("provider_truth_state=%q", reloaded.GeminiProviderTruthState)
	}
	if !reloaded.GeminiProviderTruthReady {
		t.Fatal("expected provider truth to stay ready across reload")
	}
	if len(reloaded.GeminiProtectedModels) != 1 || reloaded.GeminiProtectedModels[0] != "gemini-3.1-pro-high" {
		t.Fatalf("gemini_protected_models=%#v", reloaded.GeminiProtectedModels)
	}
	if got := reloaded.GeminiQuotaUpdatedAt.UTC().Format(time.RFC3339); got != "2026-03-24T10:06:00Z" {
		t.Fatalf("gemini_quota_updated_at=%q", got)
	}
	if len(reloaded.GeminiQuotaModels) != 1 || reloaded.GeminiQuotaModels[0].Name != "gemini-3.1-pro-high" {
		t.Fatalf("gemini_quota_models=%#v", reloaded.GeminiQuotaModels)
	}
	if got := reloaded.GeminiModelForwardingRules["gemini-1.5-pro"]; got != "gemini-2.5-pro" {
		t.Fatalf("gemini_model_forwarding_rules=%#v", reloaded.GeminiModelForwardingRules)
	}
	if reloaded.Usage.PrimaryUsedPercent != 0.12 || reloaded.Usage.SecondaryUsedPercent != 0.34 {
		t.Fatalf("usage lost across reload: %+v", reloaded.Usage)
	}
	if reloaded.Penalty != 1.25 {
		t.Fatalf("penalty=%v", reloaded.Penalty)
	}
	if !reloaded.LastUsed.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("last used=%v", reloaded.LastUsed)
	}
	if reloaded.Totals.TotalBillableTokens != 4321 || reloaded.Totals.RequestCount != 9 {
		t.Fatalf("totals lost across reload: %+v", reloaded.Totals)
	}
}

func TestReloadAccountsKeepsGeminiPersistedProfileAndHealthState(t *testing.T) {
	tmp := t.TempDir()
	poolDir := filepath.Join(tmp, "gemini")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatalf("mkdir pool dir: %v", err)
	}

	authPath := filepath.Join(poolDir, "seat-a.json")
	auth := map[string]any{
		"access_token":      "new-access",
		"refresh_token":     "new-refresh",
		"oauth_profile_id":  "gcloud",
		"health_status":     "quota_exceeded",
		"health_error":      "quota",
		"health_checked_at": "2026-03-23T11:45:00Z",
		"last_healthy_at":   "2026-03-23T10:30:00Z",
	}
	buf, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(authPath, buf, 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	handler := &proxyHandler{
		cfg: config{poolDir: tmp},
		pool: newPoolState([]*Account{{
			ID:             "seat-a",
			Type:           AccountTypeGemini,
			File:           authPath,
			AccessToken:    "old-access",
			RefreshToken:   "old-refresh",
			OAuthProfileID: "",
			Usage: UsageSnapshot{
				PrimaryUsedPercent:   0.11,
				SecondaryUsedPercent: 0.22,
				RetrievedAt:          now.Add(-2 * time.Minute),
				Source:               "runtime",
			},
			Penalty:        2.5,
			LastPenalty:    now.Add(-4 * time.Minute),
			LastUsed:       now.Add(-20 * time.Second),
			RateLimitUntil: now.Add(8 * time.Minute),
			Totals: AccountUsage{
				TotalBillableTokens: 444,
				RequestCount:        5,
				LastPrimaryPct:      0.11,
				LastSecondaryPct:    0.22,
				LastUpdated:         now.Add(-20 * time.Second),
			},
		}}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeGemini: &GeminiProvider{},
			},
		},
	}

	handler.reloadAccounts()

	if handler.pool.count() != 1 {
		t.Fatalf("reloaded accounts=%d", handler.pool.count())
	}

	reloaded := handler.pool.allAccounts()[0]
	if reloaded.AccessToken != "new-access" {
		t.Fatalf("access token not refreshed from disk: %q", reloaded.AccessToken)
	}
	if reloaded.RefreshToken != "new-refresh" {
		t.Fatalf("refresh token not refreshed from disk: %q", reloaded.RefreshToken)
	}
	if reloaded.OAuthProfileID != "gcloud" {
		t.Fatalf("oauth profile id = %q", reloaded.OAuthProfileID)
	}
	if reloaded.HealthStatus != "quota_exceeded" {
		t.Fatalf("health status = %q", reloaded.HealthStatus)
	}
	if reloaded.HealthError != "quota" {
		t.Fatalf("health error = %q", reloaded.HealthError)
	}
	if reloaded.HealthCheckedAt.Format(time.RFC3339) != "2026-03-23T11:45:00Z" {
		t.Fatalf("health checked at = %v", reloaded.HealthCheckedAt)
	}
	if reloaded.LastHealthyAt.Format(time.RFC3339) != "2026-03-23T10:30:00Z" {
		t.Fatalf("last healthy at = %v", reloaded.LastHealthyAt)
	}
	if reloaded.Usage.PrimaryUsedPercent != 0.11 || reloaded.Usage.SecondaryUsedPercent != 0.22 {
		t.Fatalf("usage lost across reload: %+v", reloaded.Usage)
	}
	if reloaded.Penalty != 2.5 {
		t.Fatalf("penalty=%v", reloaded.Penalty)
	}
	if !reloaded.LastPenalty.Equal(now.Add(-4 * time.Minute)) {
		t.Fatalf("last penalty=%v", reloaded.LastPenalty)
	}
	if !reloaded.LastUsed.Equal(now.Add(-20 * time.Second)) {
		t.Fatalf("last used=%v", reloaded.LastUsed)
	}
	if !reloaded.RateLimitUntil.Equal(now.Add(8 * time.Minute)) {
		t.Fatalf("rate limit until=%v", reloaded.RateLimitUntil)
	}
	if reloaded.Totals.TotalBillableTokens != 444 || reloaded.Totals.RequestCount != 5 {
		t.Fatalf("totals lost across reload: %+v", reloaded.Totals)
	}
}

func TestReloadAccountsNormalizesAllowlistedGeminiRestrictedHealthStatus(t *testing.T) {
	tmp := t.TempDir()
	poolDir := filepath.Join(tmp, "gemini")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatalf("mkdir pool dir: %v", err)
	}

	authPath := filepath.Join(poolDir, "seat-restricted.json")
	auth := map[string]any{
		"access_token":                   "access-token",
		"refresh_token":                  "refresh-token",
		"oauth_profile_id":               geminiOAuthAntigravityProfileID,
		"operator_source":                geminiOperatorSourceAntigravityImport,
		"antigravity_source":             "browser_oauth",
		"antigravity_validation_blocked": true,
		"gemini_validation_reason_code":  "UNSUPPORTED_LOCATION",
		"gemini_validation_message":      "region blocked",
		"gemini_provider_checked_at":     "2026-03-27T10:00:00Z",
		"health_status":                  "validation_blocked",
		"health_error":                   "managed gemini seat provider truth not ready: validation_blocked: region blocked",
	}
	buf, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(authPath, buf, 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	handler := &proxyHandler{
		cfg: config{poolDir: tmp},
		pool: newPoolState([]*Account{{
			ID:           "seat-restricted",
			Type:         AccountTypeGemini,
			File:         authPath,
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
		}}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeGemini: &GeminiProvider{},
			},
		},
	}

	handler.reloadAccounts()

	if handler.pool.count() != 1 {
		t.Fatalf("reloaded accounts=%d", handler.pool.count())
	}

	reloaded := handler.pool.allAccounts()[0]
	if reloaded.GeminiProviderTruthState != geminiProviderTruthStateRestricted {
		t.Fatalf("provider truth state = %q", reloaded.GeminiProviderTruthState)
	}
	if reloaded.HealthStatus != "restricted" {
		t.Fatalf("health status = %q", reloaded.HealthStatus)
	}
	if reloaded.HealthError != "" {
		t.Fatalf("health error = %q", reloaded.HealthError)
	}
}
