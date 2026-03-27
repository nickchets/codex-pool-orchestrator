package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeOpenCodeBaseURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8989":    "http://127.0.0.1:8989/v1",
		"http://127.0.0.1:8989/":   "http://127.0.0.1:8989/v1",
		"http://127.0.0.1:8989/v1": "http://127.0.0.1:8989/v1",
	}
	for input, want := range cases {
		if got := normalizeOpenCodeBaseURL(input); got != want {
			t.Fatalf("normalizeOpenCodeBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildOpenCodeConfigBundle(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                      "gemini-seat-1",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-1",
				OperatorEmail:           "seat@example.com",
				AntigravityProjectID:    "project-1",
				AntigravityCurrent:      true,
				LastUsed:                now,
				GeminiProviderCheckedAt: now,
				GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
					Name:             "claude-3-7-sonnet",
					RouteProvider:    "gemini",
					DisplayName:      "Claude via Gemini",
					MaxTokens:        200000,
					MaxOutputTokens:  65535,
					SupportsImages:   true,
					SupportsThinking: true,
					ThinkingBudget:   4096,
				}},
				GeminiProtectedModels: []string{"claude-3-7-sonnet"},
				AntigravityQuota: map[string]any{
					"is_forbidden": false,
				},
				GeminiQuotaUpdatedAt: now,
			},
		}, false),
	}
	user := &PoolUser{
		ID:       "pool-user-1234",
		Token:    "download-token",
		Email:    "pool@example.com",
		PlanType: "pro",
	}
	req := httptest.NewRequest("GET", "http://pool.local/config/opencode/download-token", nil)

	bundle, err := h.buildOpenCodeConfigBundle(req, user, getPoolJWTSecret())
	if err != nil {
		t.Fatalf("buildOpenCodeConfigBundle: %v", err)
	}
	if bundle.ProviderID != openCodeAntigravityProviderID {
		t.Fatalf("provider_id = %q", bundle.ProviderID)
	}
	if bundle.BaseURL != "http://pool.local/v1" {
		t.Fatalf("base_url = %q", bundle.BaseURL)
	}
	if !strings.HasPrefix(bundle.APIKey, ClaudePoolTokenPrefix) {
		t.Fatalf("api_key = %q, want %q prefix", bundle.APIKey, ClaudePoolTokenPrefix)
	}
	provider := bundle.OpenCodeConfig["provider"].(map[string]any)[openCodeAntigravityProviderID].(map[string]any)
	if provider["npm"] != "@ai-sdk/anthropic" {
		t.Fatalf("npm = %#v", provider["npm"])
	}
	options := provider["options"].(map[string]any)
	if options["baseURL"] != "http://pool.local/v1" {
		t.Fatalf("baseURL = %#v", options["baseURL"])
	}
	if options["apiKey"] != bundle.APIKey {
		t.Fatalf("apiKey mismatch")
	}
	models := provider["models"].(map[string]any)
	model := models["claude-3-7-sonnet"].(map[string]any)
	if model["name"] != "Claude via Gemini" {
		t.Fatalf("model.name = %#v", model["name"])
	}
	if model["maxTokens"] != 200000 {
		t.Fatalf("model.maxTokens = %#v", model["maxTokens"])
	}
	if model["maxOutputTokens"] != 65535 {
		t.Fatalf("model.maxOutputTokens = %#v", model["maxOutputTokens"])
	}
	if model["supportsImages"] != true || model["supportsThinking"] != true {
		t.Fatalf("model capabilities = %#v", model)
	}
	if model["thinkingBudget"] != 4096 {
		t.Fatalf("model.thinkingBudget = %#v", model["thinkingBudget"])
	}
	if model["protected"] != true {
		t.Fatalf("model.protected = %#v", model["protected"])
	}
	if len(bundle.AntigravityAccounts.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(bundle.AntigravityAccounts.Accounts))
	}
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.RefreshToken != "refresh-1" {
		t.Fatalf("refresh_token = %q", account.RefreshToken)
	}
	if account.ProjectID != "project-1" {
		t.Fatalf("project_id = %q", account.ProjectID)
	}
	if account.Enabled == nil {
		t.Fatalf("enabled = %#v", account.Enabled)
	}
	if account.CachedQuotaUpdated != now.UnixMilli() {
		t.Fatalf("cached_quota_updated_at = %d", account.CachedQuotaUpdated)
	}
	cachedModels := account.CachedQuota["models"].([]map[string]any)
	if len(cachedModels) != 1 {
		t.Fatalf("cached_quota.models = %#v", cachedModels)
	}
	if cachedModels[0]["route_provider"] != "gemini" || cachedModels[0]["routable"] != true {
		t.Fatalf("cached model = %#v", cachedModels[0])
	}
	if cachedModels[0]["compatibility_lane"] != geminiQuotaCompatibilityLaneGeminiFacade {
		t.Fatalf("cached model = %#v", cachedModels[0])
	}
	if cachedModels[0]["protected"] != true {
		t.Fatalf("cached model = %#v", cachedModels[0])
	}
}

func TestBuildOpenCodeConfigBundleMarksBlockedGeminiSeatDisabled(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                           "gemini-seat-blocked",
				Type:                         AccountTypeGemini,
				RefreshToken:                 "refresh-blocked",
				OperatorEmail:                "blocked@example.com",
				OAuthProfileID:               geminiOAuthAntigravityProfileID,
				AntigravitySource:            "browser_oauth",
				AntigravityProjectID:         "project-blocked",
				AntigravityValidationBlocked: true,
				GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
				GeminiProviderTruthState:     geminiProviderTruthStateRestricted,
				GeminiProviderTruthReason:    "UNSUPPORTED_LOCATION",
				GeminiQuotaUpdatedAt:         now,
			},
		}, false),
	}
	user := &PoolUser{
		ID:       "pool-user-1234",
		Token:    "download-token",
		Email:    "pool@example.com",
		PlanType: "pro",
	}
	req := httptest.NewRequest("GET", "http://pool.local/config/opencode/download-token", nil)

	bundle, err := h.buildOpenCodeConfigBundle(req, user, getPoolJWTSecret())
	if err != nil {
		t.Fatalf("buildOpenCodeConfigBundle: %v", err)
	}
	if len(bundle.AntigravityAccounts.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(bundle.AntigravityAccounts.Accounts))
	}
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.Enabled == nil || *account.Enabled {
		t.Fatalf("enabled = %#v", account.Enabled)
	}
	if account.LastSwitchReason != "not_warmed" {
		t.Fatalf("last_switch_reason = %q", account.LastSwitchReason)
	}
	if account.CooldownReason != "not_warmed" {
		t.Fatalf("cooldown_reason = %q", account.CooldownReason)
	}
	if account.CachedQuota["provider_truth_state"] != geminiProviderTruthStateRestricted {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if account.CachedQuota["provider_truth_ready"] != false {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
}

func TestBuildOpenCodeConfigBundlePrefersEnabledSeatForActiveIndex(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                       "gemini-seat-blocked",
				Type:                     AccountTypeGemini,
				RefreshToken:             "refresh-blocked",
				OperatorEmail:            "a-blocked@example.com",
				GeminiProviderTruthState: geminiProviderTruthStateMissingProjectID,
				GeminiProviderCheckedAt:  now,
			},
			{
				ID:                      "gemini-seat-ready",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-ready",
				OperatorEmail:           "z-ready@example.com",
				AntigravityProjectID:    "project-ready",
				AntigravityCurrent:      true,
				GeminiProviderCheckedAt: now,
				GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
			},
		}, false),
	}
	user := &PoolUser{
		ID:       "pool-user-1234",
		Token:    "download-token",
		Email:    "pool@example.com",
		PlanType: "pro",
	}
	req := httptest.NewRequest("GET", "http://pool.local/config/opencode/download-token", nil)

	bundle, err := h.buildOpenCodeConfigBundle(req, user, getPoolJWTSecret())
	if err != nil {
		t.Fatalf("buildOpenCodeConfigBundle: %v", err)
	}
	if bundle.AntigravityAccounts.ActiveIndex != 0 {
		t.Fatalf("active_index = %d", bundle.AntigravityAccounts.ActiveIndex)
	}
	if got := bundle.AntigravityAccounts.ActiveIndexByFamily["gemini"]; got != 0 {
		t.Fatalf("active_index_by_family = %#v", bundle.AntigravityAccounts.ActiveIndexByFamily)
	}
	if len(bundle.AntigravityAccounts.Accounts) != 2 {
		t.Fatalf("accounts = %d", len(bundle.AntigravityAccounts.Accounts))
	}
	first := bundle.AntigravityAccounts.Accounts[0]
	second := bundle.AntigravityAccounts.Accounts[1]
	if first.RefreshToken != "refresh-ready" {
		t.Fatalf("first account = %#v", first)
	}
	if first.Enabled == nil || !*first.Enabled {
		t.Fatalf("first account = %#v", first)
	}
	if second.Enabled == nil || *second.Enabled {
		t.Fatalf("second account = %#v", second)
	}
}
