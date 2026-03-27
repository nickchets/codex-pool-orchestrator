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

const (
	testGeminiOAuthCLIClientID     = "test-gemini-cli-client-id"
	testGeminiOAuthCLIClientSecret = "test-gemini-cli-client-secret"
	testGeminiOAuthGCloudClientID  = "test-gcloud-client-id"
	testGeminiOAuthGCloudSecret    = "test-gcloud-client-secret"
)

func setGeminiOAuthTestProfiles(t *testing.T) {
	t.Helper()
	t.Setenv(geminiOAuthCLIClientIDVar, testGeminiOAuthCLIClientID)
	t.Setenv(geminiOAuthCLIClientSecretVar, testGeminiOAuthCLIClientSecret)
	t.Setenv(geminiOAuthGCloudClientIDVar, testGeminiOAuthGCloudClientID)
	t.Setenv(geminiOAuthGCloudClientSecretVar, testGeminiOAuthGCloudSecret)
}

func TestGeminiProviderLoadAccountLoadsPersistedState(t *testing.T) {
	rateLimitUntil := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	healthCheckedAt := time.Date(2026, 3, 23, 11, 45, 0, 0, time.UTC)
	lastHealthyAt := time.Date(2026, 3, 23, 10, 30, 0, 0, time.UTC)
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"client_id": "client-id",
		"client_secret": "client-secret",
		"operator_source": "manual_import",
		"expiry_date": 1774353600000,
		"plan_type": "gemini",
		"last_refresh": "2026-03-23T10:00:00Z",
		"rate_limit_until": "2026-03-24T12:00:00Z",
		"health_status": "quota_exceeded",
		"health_error": "quota",
		"health_checked_at": "2026-03-23T11:45:00Z",
		"last_healthy_at": "2026-03-23T10:30:00Z",
		"disabled": true,
		"dead": true
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_test.json", "/tmp/gemini_test.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if !acc.Disabled {
		t.Fatal("expected Disabled to load")
	}
	if !acc.Dead {
		t.Fatal("expected Dead to load")
	}
	if acc.RateLimitUntil != rateLimitUntil {
		t.Fatalf("RateLimitUntil = %v, want %v", acc.RateLimitUntil, rateLimitUntil)
	}
	if acc.HealthStatus != "quota_exceeded" {
		t.Fatalf("HealthStatus = %q", acc.HealthStatus)
	}
	if acc.HealthError != "quota" {
		t.Fatalf("HealthError = %q", acc.HealthError)
	}
	if acc.OAuthClientID != "client-id" {
		t.Fatalf("OAuthClientID = %q", acc.OAuthClientID)
	}
	if acc.OAuthClientSecret != "client-secret" {
		t.Fatalf("OAuthClientSecret = %q", acc.OAuthClientSecret)
	}
	if acc.OperatorSource != geminiOperatorSourceManualImport {
		t.Fatalf("OperatorSource = %q", acc.OperatorSource)
	}
	if acc.HealthCheckedAt != healthCheckedAt {
		t.Fatalf("HealthCheckedAt = %v, want %v", acc.HealthCheckedAt, healthCheckedAt)
	}
	if acc.LastHealthyAt != lastHealthyAt {
		t.Fatalf("LastHealthyAt = %v, want %v", acc.LastHealthyAt, lastHealthyAt)
	}
}

func TestGeminiProviderLoadAccountLoadsOAuthProfileID(t *testing.T) {
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"oauth_profile_id": "gcloud",
		"expiry_date": 1774353600000
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_profile.json", "/tmp/gemini_profile.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID = %q", acc.OAuthProfileID)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to stay empty, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}
}

func TestGeminiProviderLoadAccountLeavesLegacyOperatorSourceUnsetWithoutProfileID(t *testing.T) {
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"client_id": "legacy-client",
		"client_secret": "legacy-secret",
		"expiry_date": 1774353600000
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_legacy.json", "/tmp/gemini_legacy.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if acc.OperatorSource != "" {
		t.Fatalf("OperatorSource = %q, want empty for legacy seat without explicit provenance", acc.OperatorSource)
	}
}

func TestGeminiProviderLoadAccountInfersManagedOAuthFromOperatorEmail(t *testing.T) {
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"operator_email": "seat@example.com"
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_managed_legacy.json", "/tmp/gemini_managed_legacy.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if acc.OperatorSource != geminiOperatorSourceManagedOAuth {
		t.Fatalf("OperatorSource = %q", acc.OperatorSource)
	}
}

func TestGeminiProviderLoadAccountLoadsAntigravityFields(t *testing.T) {
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"antigravity_source": "antigravity_tools",
		"antigravity_account_id": "ag-1",
		"antigravity_email": "ag@example.com",
		"antigravity_name": "AG User",
		"antigravity_project_id": "project-1",
		"antigravity_proxy_disabled": true,
		"antigravity_validation_blocked": true,
		"gemini_subscription_tier_id": "standard-tier",
		"gemini_subscription_tier_name": "Standard",
		"gemini_validation_reason_code": "ACCOUNT_NEEDS_WORKSPACE",
		"gemini_validation_message": "Workspace validation required",
		"gemini_validation_url": "https://example.com/validate",
		"gemini_provider_checked_at": "2026-03-24T12:00:00Z",
		"gemini_protected_models": ["gemini-3.1-pro-high"],
		"antigravity_quota": {
			"is_forbidden": true,
			"forbidden_reason": "quota exhausted",
			"last_updated": 1774353900,
			"model_forwarding_rules": {
				"gemini-1.5-pro": "gemini-2.5-pro"
			},
			"models": [
				{
					"name": "gemini-3.1-pro-high",
					"percentage": 67,
					"reset_time": "2026-03-24T15:00:00Z",
					"display_name": "Gemini 3.1 Pro High",
					"supports_images": true,
					"supports_thinking": true,
					"thinking_budget": 24576,
					"recommended": true,
					"max_tokens": 1048576,
					"max_output_tokens": 65535
				}
			]
		}
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_ag.json", "/tmp/gemini_ag.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if acc.OperatorSource != geminiOperatorSourceAntigravityImport {
		t.Fatalf("OperatorSource = %q", acc.OperatorSource)
	}
	if acc.OAuthProfileID != geminiOAuthAntigravityProfileID {
		t.Fatalf("OAuthProfileID = %q", acc.OAuthProfileID)
	}
	if acc.OperatorEmail != "ag@example.com" {
		t.Fatalf("OperatorEmail = %q", acc.OperatorEmail)
	}
	if acc.AntigravityAccountID != "ag-1" {
		t.Fatalf("AntigravityAccountID = %q", acc.AntigravityAccountID)
	}
	if !acc.AntigravityProxyDisabled {
		t.Fatal("expected AntigravityProxyDisabled to load")
	}
	if !acc.AntigravityValidationBlocked {
		t.Fatal("expected AntigravityValidationBlocked to load")
	}
	if !acc.AntigravityQuotaForbidden {
		t.Fatal("expected AntigravityQuotaForbidden to load")
	}
	if acc.AntigravityQuotaForbiddenReason != "quota exhausted" {
		t.Fatalf("AntigravityQuotaForbiddenReason = %q", acc.AntigravityQuotaForbiddenReason)
	}
	if acc.GeminiSubscriptionTierID != "standard-tier" {
		t.Fatalf("GeminiSubscriptionTierID = %q", acc.GeminiSubscriptionTierID)
	}
	if acc.GeminiSubscriptionTierName != "Standard" {
		t.Fatalf("GeminiSubscriptionTierName = %q", acc.GeminiSubscriptionTierName)
	}
	if acc.GeminiValidationReasonCode != "ACCOUNT_NEEDS_WORKSPACE" {
		t.Fatalf("GeminiValidationReasonCode = %q", acc.GeminiValidationReasonCode)
	}
	if acc.GeminiValidationMessage != "Workspace validation required" {
		t.Fatalf("GeminiValidationMessage = %q", acc.GeminiValidationMessage)
	}
	if acc.GeminiValidationURL != "https://example.com/validate" {
		t.Fatalf("GeminiValidationURL = %q", acc.GeminiValidationURL)
	}
	if got := acc.GeminiProviderCheckedAt.UTC().Format(time.RFC3339); got != "2026-03-24T12:00:00Z" {
		t.Fatalf("GeminiProviderCheckedAt = %q", got)
	}
	if acc.GeminiProviderTruthReady {
		t.Fatal("expected GeminiProviderTruthReady to stay false for blocked seat")
	}
	if acc.GeminiProviderTruthState != geminiProviderTruthStateProxyDisabled {
		t.Fatalf("GeminiProviderTruthState = %q", acc.GeminiProviderTruthState)
	}
	if acc.GeminiProviderTruthReason != "provider marked seat proxy_disabled" {
		t.Fatalf("GeminiProviderTruthReason = %q", acc.GeminiProviderTruthReason)
	}
	if len(acc.GeminiProtectedModels) != 1 || acc.GeminiProtectedModels[0] != "gemini-3.1-pro-high" {
		t.Fatalf("GeminiProtectedModels = %#v", acc.GeminiProtectedModels)
	}
	if got := acc.GeminiQuotaUpdatedAt.UTC().Format(time.RFC3339); got != "2026-03-24T12:05:00Z" {
		t.Fatalf("GeminiQuotaUpdatedAt = %q", got)
	}
	if len(acc.GeminiQuotaModels) != 1 {
		t.Fatalf("GeminiQuotaModels = %#v", acc.GeminiQuotaModels)
	}
	if acc.GeminiQuotaModels[0].Name != "gemini-3.1-pro-high" || acc.GeminiQuotaModels[0].MaxOutputTokens != 65535 {
		t.Fatalf("GeminiQuotaModels[0] = %#v", acc.GeminiQuotaModels[0])
	}
	if acc.GeminiQuotaModels[0].ThinkingBudget != 24576 {
		t.Fatalf("GeminiQuotaModels[0].ThinkingBudget = %d", acc.GeminiQuotaModels[0].ThinkingBudget)
	}
	if got := acc.GeminiModelForwardingRules["gemini-1.5-pro"]; got != "gemini-2.5-pro" {
		t.Fatalf("GeminiModelForwardingRules = %#v", acc.GeminiModelForwardingRules)
	}
}

func TestGeminiProviderLoadAccountFiltersPersistedCodexQuotaModels(t *testing.T) {
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"oauth_profile_id": "antigravity_public",
		"antigravity_source": "browser_oauth",
		"gemini_quota_models": [
			{
				"name": "claude-sonnet-4-6",
				"percentage": 62
			},
			{
				"name": "gpt-oss-120b-medium",
				"percentage": 91
			},
			{
				"name": "gemini-2.5-flash",
				"percentage": 73
			}
		]
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_mixed_quota.json", "/tmp/gemini_mixed_quota.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if len(acc.GeminiQuotaModels) != 2 {
		t.Fatalf("GeminiQuotaModels = %#v", acc.GeminiQuotaModels)
	}
	if acc.GeminiQuotaModels[0].Name != "gemini-2.5-flash" || acc.GeminiQuotaModels[0].RouteProvider != "gemini" {
		t.Fatalf("GeminiQuotaModels[0] = %#v", acc.GeminiQuotaModels[0])
	}
	if acc.GeminiQuotaModels[1].Name != "claude-sonnet-4-6" || acc.GeminiQuotaModels[1].RouteProvider != "claude" {
		t.Fatalf("GeminiQuotaModels[1] = %#v", acc.GeminiQuotaModels[1])
	}
	for _, model := range acc.GeminiQuotaModels {
		if model.Name == "gpt-oss-120b-medium" {
			t.Fatalf("unexpected codex quota model: %#v", acc.GeminiQuotaModels)
		}
	}
}

func TestGeminiProviderLoadAccountNormalizesAllowlistedRestrictedHealthStatus(t *testing.T) {
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"antigravity_source": "browser_oauth",
		"antigravity_validation_blocked": true,
		"gemini_validation_reason_code": "UNSUPPORTED_LOCATION",
		"gemini_validation_message": "region blocked",
		"gemini_provider_checked_at": "2026-03-27T10:00:00Z",
		"health_status": "validation_blocked",
		"health_error": "managed gemini seat provider truth not ready: validation_blocked: region blocked"
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_restricted.json", "/tmp/gemini_restricted.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if acc.GeminiProviderTruthState != geminiProviderTruthStateRestricted {
		t.Fatalf("GeminiProviderTruthState = %q", acc.GeminiProviderTruthState)
	}
	if acc.HealthStatus != "restricted" {
		t.Fatalf("HealthStatus = %q", acc.HealthStatus)
	}
	if acc.HealthError != "" {
		t.Fatalf("HealthError = %q", acc.HealthError)
	}
}

func TestSaveGeminiAccountPersistsStateFields(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_state.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token": "old-access",
		"refresh_token": "old-refresh",
		"scope": "scope",
		"extra": "keep-me"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:                "gemini_state",
		Type:              AccountTypeGemini,
		File:              accFile,
		AccessToken:       "new-access",
		RefreshToken:      "new-refresh",
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		ExpiresAt:         time.Date(2026, 3, 25, 8, 0, 0, 0, time.UTC),
		LastRefresh:       time.Date(2026, 3, 23, 9, 30, 0, 0, time.UTC),
		RateLimitUntil:    time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC),
		HealthStatus:      "quota_exceeded",
		HealthError:       "quota",
		HealthCheckedAt:   time.Date(2026, 3, 23, 11, 0, 0, 0, time.UTC),
		LastHealthyAt:     time.Date(2026, 3, 23, 8, 45, 0, 0, time.UTC),
		Disabled:          true,
		Dead:              true,
	}

	if err := saveGeminiAccount(acc); err != nil {
		t.Fatalf("saveGeminiAccount error: %v", err)
	}

	var root map[string]any
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}

	if root["extra"] != "keep-me" {
		t.Fatalf("expected custom field to be preserved, got %#v", root["extra"])
	}
	if root["client_id"] != "client-id" {
		t.Fatalf("client_id = %#v", root["client_id"])
	}
	if root["client_secret"] != "client-secret" {
		t.Fatalf("client_secret = %#v", root["client_secret"])
	}
	if root["health_status"] != "quota_exceeded" {
		t.Fatalf("health_status = %#v", root["health_status"])
	}
	if root["health_error"] != "quota" {
		t.Fatalf("health_error = %#v", root["health_error"])
	}
	if root["disabled"] != true {
		t.Fatalf("disabled = %#v", root["disabled"])
	}
	if root["dead"] != true {
		t.Fatalf("dead = %#v", root["dead"])
	}
	if root["rate_limit_until"] != "2026-03-23T12:00:00Z" {
		t.Fatalf("rate_limit_until = %#v", root["rate_limit_until"])
	}
	if root["health_checked_at"] != "2026-03-23T11:00:00Z" {
		t.Fatalf("health_checked_at = %#v", root["health_checked_at"])
	}
	if root["last_healthy_at"] != "2026-03-23T08:45:00Z" {
		t.Fatalf("last_healthy_at = %#v", root["last_healthy_at"])
	}
}

func TestSaveGeminiAccountPersistsOAuthProfileID(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_profile_state.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token": "old-access",
		"refresh_token": "old-refresh",
		"client_id": "legacy-client",
		"client_secret": "legacy-secret"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:             "gemini_profile_state",
		Type:           AccountTypeGemini,
		File:           accFile,
		AccessToken:    "new-access",
		RefreshToken:   "new-refresh",
		OAuthProfileID: "gcloud",
		OperatorSource: geminiOperatorSourceManagedOAuth,
	}

	if err := saveGeminiAccount(acc); err != nil {
		t.Fatalf("saveGeminiAccount error: %v", err)
	}

	var root map[string]any
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}

	if root["oauth_profile_id"] != "gcloud" {
		t.Fatalf("oauth_profile_id = %#v", root["oauth_profile_id"])
	}
	if root["operator_source"] != geminiOperatorSourceManagedOAuth {
		t.Fatalf("operator_source = %#v", root["operator_source"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected client_id to be dropped: %s", string(saved))
	}
	if _, ok := root["client_secret"]; ok {
		t.Fatalf("expected client_secret to be dropped: %s", string(saved))
	}
}

func TestSaveGeminiAccountPersistsAntigravityFields(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_antigravity_state.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token": "old-access",
		"refresh_token": "old-refresh"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:                           "gemini_antigravity",
		Type:                         AccountTypeGemini,
		File:                         accFile,
		AccessToken:                  "new-access",
		RefreshToken:                 "new-refresh",
		OperatorSource:               geminiOperatorSourceAntigravityImport,
		OperatorEmail:                "ag@example.com",
		AntigravitySource:            "antigravity_tools",
		AntigravityAccountID:         "ag-1",
		AntigravityEmail:             "ag@example.com",
		AntigravityName:              "AG User",
		AntigravityProjectID:         "project-1",
		AntigravityCurrent:           true,
		AntigravityProxyDisabled:     true,
		AntigravityValidationBlocked: true,
		GeminiSubscriptionTierID:     "standard-tier",
		GeminiSubscriptionTierName:   "Standard",
		GeminiValidationReasonCode:   "ACCOUNT_NEEDS_WORKSPACE",
		GeminiValidationMessage:      "Workspace validation required",
		GeminiValidationURL:          "https://example.com/validate",
		GeminiProviderCheckedAt:      time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
		GeminiProtectedModels:        []string{"gemini-3.1-pro-high"},
		GeminiQuotaUpdatedAt:         time.Date(2026, 3, 24, 12, 5, 0, 0, time.UTC),
		GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
			Name:             "gemini-3.1-pro-high",
			Percentage:       67,
			ResetTime:        "2026-03-24T15:00:00Z",
			DisplayName:      "Gemini 3.1 Pro High",
			SupportsImages:   true,
			SupportsThinking: true,
			ThinkingBudget:   24576,
			Recommended:      true,
			MaxTokens:        1048576,
			MaxOutputTokens:  65535,
		}},
		GeminiModelForwardingRules: map[string]string{
			"gemini-1.5-pro": "gemini-2.5-pro",
		},
		AntigravityQuota: map[string]any{
			"is_forbidden":     true,
			"forbidden_reason": "quota exhausted",
		},
	}

	if err := saveGeminiAccount(acc); err != nil {
		t.Fatalf("saveGeminiAccount error: %v", err)
	}

	var root map[string]any
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if root["operator_source"] != geminiOperatorSourceAntigravityImport {
		t.Fatalf("operator_source = %#v", root["operator_source"])
	}
	if root["operator_email"] != "ag@example.com" {
		t.Fatalf("operator_email = %#v", root["operator_email"])
	}
	if root["antigravity_account_id"] != "ag-1" {
		t.Fatalf("antigravity_account_id = %#v", root["antigravity_account_id"])
	}
	if root["antigravity_proxy_disabled"] != true {
		t.Fatalf("antigravity_proxy_disabled = %#v", root["antigravity_proxy_disabled"])
	}
	if root["antigravity_validation_blocked"] != true {
		t.Fatalf("antigravity_validation_blocked = %#v", root["antigravity_validation_blocked"])
	}
	if root["gemini_subscription_tier_id"] != "standard-tier" {
		t.Fatalf("gemini_subscription_tier_id = %#v", root["gemini_subscription_tier_id"])
	}
	if root["gemini_subscription_tier_name"] != "Standard" {
		t.Fatalf("gemini_subscription_tier_name = %#v", root["gemini_subscription_tier_name"])
	}
	if root["gemini_validation_reason_code"] != "ACCOUNT_NEEDS_WORKSPACE" {
		t.Fatalf("gemini_validation_reason_code = %#v", root["gemini_validation_reason_code"])
	}
	if root["gemini_validation_message"] != "Workspace validation required" {
		t.Fatalf("gemini_validation_message = %#v", root["gemini_validation_message"])
	}
	if root["gemini_validation_url"] != "https://example.com/validate" {
		t.Fatalf("gemini_validation_url = %#v", root["gemini_validation_url"])
	}
	if root["gemini_provider_checked_at"] != "2026-03-24T12:00:00Z" {
		t.Fatalf("gemini_provider_checked_at = %#v", root["gemini_provider_checked_at"])
	}
	if root["gemini_provider_truth_ready"] != false {
		t.Fatalf("gemini_provider_truth_ready = %#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateProxyDisabled {
		t.Fatalf("gemini_provider_truth_state = %#v", root["gemini_provider_truth_state"])
	}
	if root["gemini_provider_truth_reason"] != "provider marked seat proxy_disabled" {
		t.Fatalf("gemini_provider_truth_reason = %#v", root["gemini_provider_truth_reason"])
	}
	if root["gemini_quota_updated_at"] != "2026-03-24T12:05:00Z" {
		t.Fatalf("gemini_quota_updated_at = %#v", root["gemini_quota_updated_at"])
	}
	protectedModels, _ := root["gemini_protected_models"].([]any)
	if len(protectedModels) != 1 || protectedModels[0] != "gemini-3.1-pro-high" {
		t.Fatalf("gemini_protected_models = %#v", root["gemini_protected_models"])
	}
	quotaModels, _ := root["gemini_quota_models"].([]any)
	if len(quotaModels) != 1 {
		t.Fatalf("gemini_quota_models = %#v", root["gemini_quota_models"])
	}
	quotaModel, _ := quotaModels[0].(map[string]any)
	if quotaModel["name"] != "gemini-3.1-pro-high" || quotaModel["max_output_tokens"] != float64(65535) {
		t.Fatalf("gemini_quota_models[0] = %#v", quotaModel)
	}
	forwardingRules, _ := root["gemini_model_forwarding_rules"].(map[string]any)
	if forwardingRules["gemini-1.5-pro"] != "gemini-2.5-pro" {
		t.Fatalf("gemini_model_forwarding_rules = %#v", root["gemini_model_forwarding_rules"])
	}
	quota, _ := root["antigravity_quota"].(map[string]any)
	if quota["forbidden_reason"] != "quota exhausted" {
		t.Fatalf("antigravity quota = %#v", root["antigravity_quota"])
	}
}

func TestSaveGeminiAccountPersistsProviderTruthReadyState(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_provider_truth_ready.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token": "old-access",
		"refresh_token": "old-refresh"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:                       "gemini_provider_truth_ready",
		Type:                     AccountTypeGemini,
		File:                     accFile,
		AccessToken:              "new-access",
		RefreshToken:             "new-refresh",
		AntigravityProjectID:     "project-1",
		GeminiProviderCheckedAt:  time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
		GeminiSubscriptionTierID: "standard-tier",
	}

	if err := saveGeminiAccount(acc); err != nil {
		t.Fatalf("saveGeminiAccount error: %v", err)
	}

	var root map[string]any
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}

	if root["gemini_provider_truth_ready"] != true {
		t.Fatalf("gemini_provider_truth_ready = %#v", root["gemini_provider_truth_ready"])
	}
	if root["gemini_provider_truth_state"] != geminiProviderTruthStateReady {
		t.Fatalf("gemini_provider_truth_state = %#v", root["gemini_provider_truth_state"])
	}
	if _, ok := root["gemini_provider_truth_reason"]; ok {
		t.Fatalf("expected gemini_provider_truth_reason to be omitted for ready state: %#v", root["gemini_provider_truth_reason"])
	}
}

func TestFinalizeProxyResponsePersistsHealthyGeminiRecovery(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_dead.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token",
		"rate_limit_until":"2026-03-23T12:00:00Z",
		"health_status":"quota_exceeded",
		"health_error":"quota",
		"dead":true
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:             "gemini_dead",
		Type:           AccountTypeGemini,
		File:           accFile,
		AccessToken:    "access-token",
		RefreshToken:   "refresh-token",
		Dead:           true,
		RateLimitUntil: time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC),
		HealthStatus:   "quota_exceeded",
		HealthError:    "quota",
	}

	h.finalizeProxyResponse("req-test", &GeminiProvider{}, acc, "pool-user", http.StatusOK, true, false, "", 0, 0, nil)

	if acc.Dead {
		t.Fatal("expected gemini account to be resurrected")
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", acc.RateLimitUntil)
	}
	if acc.HealthStatus != "auth_only" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != "provider truth not hydrated" {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if acc.HealthCheckedAt.IsZero() {
		t.Fatal("expected health_checked_at to be updated")
	}
	if !acc.LastHealthyAt.IsZero() {
		t.Fatalf("expected last_healthy_at to stay zero, got %v", acc.LastHealthyAt)
	}
	if acc.GeminiOperationalState != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("gemini_operational_state=%q", acc.GeminiOperationalState)
	}
	if acc.GeminiOperationalCheckedAt.IsZero() || acc.GeminiOperationalLastSuccessAt.IsZero() {
		t.Fatal("expected operational timestamps to be updated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if _, ok := root["rate_limit_until"]; ok {
		t.Fatalf("expected saved file to clear rate_limit_until: %s", string(saved))
	}
	if _, ok := root["dead"]; ok {
		t.Fatalf("expected saved file to clear dead flag: %s", string(saved))
	}
	if root["health_status"] != "auth_only" {
		t.Fatalf("saved health_status = %#v", root["health_status"])
	}
	if root["gemini_operational_state"] != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("saved gemini_operational_state = %#v", root["gemini_operational_state"])
	}
}

func TestFinalizeProxyResponsePersistsHealthyGeminiStateFromUnknown(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_unknown.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:           "gemini_unknown",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	}

	h.finalizeProxyResponse("req-test", &GeminiProvider{}, acc, "pool-user", http.StatusOK, true, false, "", 0, 0, nil)

	if acc.HealthStatus != "auth_only" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthCheckedAt.IsZero() {
		t.Fatal("expected health_checked_at to be populated")
	}
	if !acc.LastHealthyAt.IsZero() {
		t.Fatalf("expected last_healthy_at to stay zero, got %v", acc.LastHealthyAt)
	}
	if acc.GeminiOperationalState != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("gemini_operational_state=%q", acc.GeminiOperationalState)
	}
	if acc.GeminiOperationalCheckedAt.IsZero() || acc.GeminiOperationalLastSuccessAt.IsZero() {
		t.Fatal("expected operational timestamps to be populated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if root["health_status"] != "auth_only" {
		t.Fatalf("saved health_status = %#v", root["health_status"])
	}
	if _, ok := root["health_checked_at"]; !ok {
		t.Fatalf("expected health_checked_at to be persisted: %s", string(saved))
	}
	if _, ok := root["last_healthy_at"]; ok {
		t.Fatalf("expected last_healthy_at to stay absent: %s", string(saved))
	}
	if root["gemini_operational_state"] != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("saved gemini_operational_state = %#v", root["gemini_operational_state"])
	}
	if _, ok := root["gemini_operational_checked_at"]; !ok {
		t.Fatalf("expected gemini_operational_checked_at to be persisted: %s", string(saved))
	}
	if _, ok := root["gemini_operational_last_success_at"]; !ok {
		t.Fatalf("expected gemini_operational_last_success_at to be persisted: %s", string(saved))
	}
}

func TestFinalizeProxyResponsePersistsHealthyGeminiTimestampsWhenAlreadyHealthy(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_healthy_missing_timestamps.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token",
		"health_status":"healthy"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:           "gemini_healthy",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		HealthStatus: "healthy",
	}

	h.finalizeProxyResponse("req-test", &GeminiProvider{}, acc, "pool-user", http.StatusOK, true, false, "", 0, 0, nil)

	if acc.HealthStatus != "auth_only" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthCheckedAt.IsZero() {
		t.Fatal("expected health_checked_at to be populated")
	}
	if !acc.LastHealthyAt.IsZero() {
		t.Fatalf("expected last_healthy_at to stay zero, got %v", acc.LastHealthyAt)
	}
	if acc.GeminiOperationalState != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("gemini_operational_state=%q", acc.GeminiOperationalState)
	}
	if acc.GeminiOperationalCheckedAt.IsZero() || acc.GeminiOperationalLastSuccessAt.IsZero() {
		t.Fatal("expected operational timestamps to be populated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if _, ok := root["health_checked_at"]; !ok {
		t.Fatalf("expected health_checked_at to be persisted: %s", string(saved))
	}
	if _, ok := root["last_healthy_at"]; ok {
		t.Fatalf("expected last_healthy_at to stay absent: %s", string(saved))
	}
	if root["health_status"] != "auth_only" {
		t.Fatalf("saved health_status = %#v", root["health_status"])
	}
	if root["gemini_operational_state"] != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("saved gemini_operational_state = %#v", root["gemini_operational_state"])
	}
}

func TestFinalizeWebSocketSuccessStatePersistsHealthyGeminiState(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_ws_unknown.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:           "gemini_ws_unknown",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	}

	h.finalizeWebSocketSuccessState(acc, "", http.StatusSwitchingProtocols)

	if acc.HealthStatus != "auth_only" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthCheckedAt.IsZero() {
		t.Fatal("expected health_checked_at to be populated")
	}
	if !acc.LastHealthyAt.IsZero() {
		t.Fatalf("expected last_healthy_at to stay zero, got %v", acc.LastHealthyAt)
	}
	if acc.GeminiOperationalState != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("gemini_operational_state=%q", acc.GeminiOperationalState)
	}
	if acc.GeminiOperationalCheckedAt.IsZero() || acc.GeminiOperationalLastSuccessAt.IsZero() {
		t.Fatal("expected operational timestamps to be populated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if root["health_status"] != "auth_only" {
		t.Fatalf("saved health_status = %#v", root["health_status"])
	}
	if _, ok := root["health_checked_at"]; !ok {
		t.Fatalf("expected health_checked_at to be persisted: %s", string(saved))
	}
	if _, ok := root["last_healthy_at"]; ok {
		t.Fatalf("expected last_healthy_at to stay absent: %s", string(saved))
	}
	if root["gemini_operational_state"] != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("saved gemini_operational_state = %#v", root["gemini_operational_state"])
	}
}

func TestGeminiProviderRefreshTokenFallsBackToGCloudClient(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"seed-access",
		"refresh_token":"seed-refresh"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:           "gemini_refresh",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "seed-access",
		RefreshToken: "seed-refresh",
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch calls {
		case 1:
			if values.Get("client_id") != testGeminiOAuthCLIClientID {
				t.Fatalf("first client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusUnauthorized, `{"error":"unauthorized_client","error_description":"Unauthorized"}`), nil
		case 2:
			if values.Get("client_id") != testGeminiOAuthGCloudClientID {
				t.Fatalf("second client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		default:
			t.Fatalf("unexpected refresh call #%d", calls)
		}
		return nil, nil
	})

	if err := (&GeminiProvider{}).RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if acc.AccessToken != "fresh-access" {
		t.Fatalf("AccessToken=%q", acc.AccessToken)
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID=%q", acc.OAuthProfileID)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to be cleared, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if root["oauth_profile_id"] != "gcloud" {
		t.Fatalf("saved oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected saved client_id to be dropped: %s", string(saved))
	}
}

func TestGeminiProviderRefreshTokenKeepsAntigravityProfile(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh_antigravity.json")
	raw := []byte(`{
		"access_token":"seed-access",
		"refresh_token":"seed-refresh",
		"oauth_profile_id":"antigravity_public",
		"operator_source":"antigravity_import",
		"antigravity_project_id":"project-1",
		"antigravity_source":"browser_oauth"
	}`)
	if err := os.WriteFile(accFile, raw, 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_refresh_antigravity.json", accFile, raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if values.Get("client_id") != geminiOAuthAntigravityClientID {
			t.Fatalf("client_id=%q", values.Get("client_id"))
		}
		if values.Get("client_secret") != "" {
			t.Fatalf("client_secret=%q", values.Get("client_secret"))
		}
		return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
	})

	if err := (&GeminiProvider{}).RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if acc.AccessToken != "fresh-access" {
		t.Fatalf("AccessToken=%q", acc.AccessToken)
	}
	if acc.OAuthProfileID != geminiOAuthAntigravityProfileID {
		t.Fatalf("OAuthProfileID=%q", acc.OAuthProfileID)
	}
	if acc.OperatorSource != geminiOperatorSourceAntigravityImport {
		t.Fatalf("OperatorSource=%q", acc.OperatorSource)
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if root["oauth_profile_id"] != geminiOAuthAntigravityProfileID {
		t.Fatalf("saved oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if root["operator_source"] != geminiOperatorSourceAntigravityImport {
		t.Fatalf("saved operator_source=%#v", root["operator_source"])
	}
}

func TestGeminiProviderRefreshTokenPrefersManagedProfileBeforeLegacyRawClient(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh_managed_legacy.json")
	raw := []byte(`{
		"access_token":"seed-access",
		"refresh_token":"seed-refresh",
		"client_id":"legacy-client-id",
		"client_secret":"legacy-client-secret",
		"operator_email":"seat@example.com"
	}`)
	if err := os.WriteFile(accFile, raw, 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_refresh_managed_legacy.json", accFile, raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if values.Get("client_id") != testGeminiOAuthGCloudClientID {
			t.Fatalf("client_id=%q", values.Get("client_id"))
		}
		return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
	})

	if err := (&GeminiProvider{}).RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID=%q", acc.OAuthProfileID)
	}
	if acc.OperatorSource != geminiOperatorSourceManagedOAuth {
		t.Fatalf("OperatorSource=%q", acc.OperatorSource)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to be cleared, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if root["oauth_profile_id"] != "gcloud" {
		t.Fatalf("saved oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if root["operator_source"] != geminiOperatorSourceManagedOAuth {
		t.Fatalf("saved operator_source=%#v", root["operator_source"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected saved client_id to be dropped: %s", string(saved))
	}
}

func TestGeminiProviderRefreshTokenFallsBackOn400InvalidGrant(t *testing.T) {
	testGeminiProviderRefreshTokenFallsBackOnRetryableBadRequest(t, "invalid_grant")
}

func TestGeminiProviderRefreshTokenFallsBackOn400InvalidClient(t *testing.T) {
	testGeminiProviderRefreshTokenFallsBackOnRetryableBadRequest(t, "invalid_client")
}

func TestGeminiProviderRefreshTokenMigratesLegacySeatToManagedProfile(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh_legacy.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"seed-access",
		"refresh_token":"seed-refresh",
		"client_id":"legacy-client",
		"client_secret":"legacy-secret"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	raw, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	acc, err := (&GeminiProvider{}).LoadAccount(filepath.Base(accFile), accFile, raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if acc.OperatorSource != "" {
		t.Fatalf("OperatorSource = %q, want empty before migration", acc.OperatorSource)
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch calls {
		case 1:
			if values.Get("client_id") != "legacy-client" {
				t.Fatalf("first client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusBadRequest, `{"error":"invalid_client","error_description":"legacy client is no longer allowed"}`), nil
		case 2:
			if values.Get("client_id") != testGeminiOAuthCLIClientID {
				t.Fatalf("second client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusUnauthorized, `{"error":"unauthorized_client","error_description":"retry another client"}`), nil
		case 3:
			if values.Get("client_id") != testGeminiOAuthGCloudClientID {
				t.Fatalf("third client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		default:
			t.Fatalf("unexpected refresh call #%d", calls)
		}
		return nil, nil
	})

	if err := (&GeminiProvider{}).RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
	if acc.AccessToken != "fresh-access" {
		t.Fatalf("AccessToken=%q", acc.AccessToken)
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID=%q", acc.OAuthProfileID)
	}
	if acc.OperatorSource != geminiOperatorSourceManagedOAuth {
		t.Fatalf("OperatorSource=%q", acc.OperatorSource)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to be cleared, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if root["oauth_profile_id"] != "gcloud" {
		t.Fatalf("saved oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if root["operator_source"] != geminiOperatorSourceManagedOAuth {
		t.Fatalf("saved operator_source=%#v", root["operator_source"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected saved client_id to be dropped: %s", string(saved))
	}
	if _, ok := root["client_secret"]; ok {
		t.Fatalf("expected saved client_secret to be dropped: %s", string(saved))
	}
}

func testGeminiProviderRefreshTokenFallsBackOnRetryableBadRequest(t *testing.T, oauthError string) {
	t.Helper()
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh_bad_request.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"seed-access",
		"refresh_token":"seed-refresh"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:           "gemini_refresh_bad_request",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "seed-access",
		RefreshToken: "seed-refresh",
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch calls {
		case 1:
			if values.Get("client_id") != testGeminiOAuthCLIClientID {
				t.Fatalf("first client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusBadRequest, `{"error":"`+oauthError+`","error_description":"retry another client"}`), nil
		case 2:
			if values.Get("client_id") != testGeminiOAuthGCloudClientID {
				t.Fatalf("second client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		default:
			t.Fatalf("unexpected refresh call #%d", calls)
		}
		return nil, nil
	})

	if err := (&GeminiProvider{}).RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if acc.AccessToken != "fresh-access" {
		t.Fatalf("AccessToken=%q", acc.AccessToken)
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID=%q", acc.OAuthProfileID)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to be cleared, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}
}

func TestGeminiProviderRefreshTokenLogsFallbackTrace(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh_trace.json")
	if err := os.WriteFile(accFile, []byte(`{"access_token":"seed-access","refresh_token":"seed-refresh"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	acc := &Account{
		ID:           "gemini_refresh_trace",
		Type:         AccountTypeGemini,
		File:         accFile,
		AuthMode:     accountAuthModeOAuth,
		AccessToken:  "seed-access",
		RefreshToken: "seed-refresh",
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"retry another client"}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		default:
			t.Fatalf("unexpected refresh call #%d", calls)
		}
		return nil, nil
	})

	logs := captureLogs(t, func() {
		if err := (&GeminiProvider{}).RefreshToken(testTraceContext("req-gemini-refresh"), acc, transport); err != nil {
			t.Fatalf("RefreshToken error: %v", err)
		}
	})

	if !strings.Contains(logs, "[req-gemini-refresh] trace auth_fallback") {
		t.Fatalf("missing auth_fallback log: %s", logs)
	}
	if !strings.Contains(logs, "[req-gemini-refresh] trace token_refresh") {
		t.Fatalf("missing token_refresh log: %s", logs)
	}
	if !strings.Contains(logs, `provider=gemini`) || !strings.Contains(logs, `result=fallback`) || !strings.Contains(logs, `result=ok`) {
		t.Fatalf("unexpected refresh trace logs: %s", logs)
	}
}
