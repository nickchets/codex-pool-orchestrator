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
	if got, _ := bundle.OpenCodeConfig["model"].(string); got != "codex-pool/gemini-3.1-flash-lite" {
		t.Fatalf("model = %q", got)
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
	for _, want := range []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-2.5-flash-thinking",
		"gemini-2.5-pro",
		"gemini-3-flash",
		"gemini-3-flash-agent",
		"gemini-3-pro-high",
		"gemini-3-pro-low",
		"gemini-3-pro-preview",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-lite",
		"gemini-3.1-pro",
		"gemini-3.1-pro-high",
		"gemini-3.1-pro-low",
		"gemini-3.1-pro-preview",
	} {
		if _, ok := models[want]; !ok {
			t.Fatalf("models missing %q: %#v", want, models)
		}
	}
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
	if account.AddedAt != now.UnixMilli() {
		t.Fatalf("added_at = %d", account.AddedAt)
	}
	if account.LastUsed != now.UnixMilli() {
		t.Fatalf("last_used = %d", account.LastUsed)
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
	if got := account.CachedQuota["provider_checked_at"]; got != now.Unix() {
		t.Fatalf("cached_quota.provider_checked_at = %#v", got)
	}
}

func TestBuildOpenCodeConfigBundleKeepsCanonicalNamesForKnownGeminiModels(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Date(2026, time.March, 29, 12, 0, 0, 0, time.UTC)
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                      "gemini-seat-1",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-1",
				OperatorEmail:           "seat@example.com",
				AntigravityProjectID:    "project-1",
				GeminiProviderCheckedAt: now,
				GeminiQuotaModels: []GeminiModelQuotaSnapshot{
					{
						Name:        "gemini-2.5-flash",
						DisplayName: "Gemini 3.1 Flash Lite",
					},
					{
						Name:        "gemini-3-flash-agent",
						DisplayName: "Gemini 3 Flash",
					},
				},
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
	provider := bundle.OpenCodeConfig["provider"].(map[string]any)[openCodeAntigravityProviderID].(map[string]any)
	models := provider["models"].(map[string]any)
	if got := models["gemini-2.5-flash"].(map[string]any)["name"]; got != "Gemini 2.5 Flash" {
		t.Fatalf("gemini-2.5-flash name = %#v", got)
	}
	if got := models["gemini-3-flash-agent"].(map[string]any)["name"]; got != "Gemini 3 Flash Agent" {
		t.Fatalf("gemini-3-flash-agent name = %#v", got)
	}
}

func TestBuildOpenCodeConfigBundleKeepsLastUsedEmptyWhenSeatWasOnlyRefreshed(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Date(2026, time.March, 27, 12, 34, 56, 0, time.UTC)
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                      "gemini-seat-refreshed",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-1",
				OperatorEmail:           "seat@example.com",
				AntigravityProjectID:    "project-1",
				LastRefresh:             now,
				GeminiProviderCheckedAt: now.Add(-time.Minute),
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
	if len(bundle.AntigravityAccounts.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(bundle.AntigravityAccounts.Accounts))
	}
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.AddedAt != now.UnixMilli() {
		t.Fatalf("added_at = %d", account.AddedAt)
	}
	if account.LastUsed != 0 {
		t.Fatalf("last_used = %d", account.LastUsed)
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
	if account.CooldownReason != "UNSUPPORTED_LOCATION" {
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

func TestBuildOpenCodeConfigBundlePrefersReadyCleanSeatOverDegradedEnabledSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                           "gemini-seat-degraded",
				Type:                         AccountTypeGemini,
				RefreshToken:                 "refresh-degraded",
				OperatorEmail:                "a-degraded@example.com",
				OperatorSource:               geminiOperatorSourceAntigravityImport,
				OAuthProfileID:               geminiOAuthAntigravityProfileID,
				AntigravityProjectID:         "project-degraded",
				AntigravityCurrent:           true,
				AntigravityValidationBlocked: true,
				GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
				GeminiProviderTruthState:     geminiProviderTruthStateRestricted,
				GeminiProviderTruthReason:    "UNSUPPORTED_LOCATION",
				GeminiProviderCheckedAt:      now,
				GeminiOperationalState:       geminiOperationalTruthStateDegradedOK,
				GeminiOperationalReason:      "provider restriction detected",
				LastUsed:                     now.Add(2 * time.Minute),
			},
			{
				ID:                      "gemini-seat-ready",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-ready",
				OperatorEmail:           "z-ready@example.com",
				AntigravityProjectID:    "project-ready",
				GeminiProviderCheckedAt: now,
				GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
				LastUsed:                now,
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
	if len(bundle.AntigravityAccounts.Accounts) != 2 {
		t.Fatalf("accounts = %d", len(bundle.AntigravityAccounts.Accounts))
	}
	first := bundle.AntigravityAccounts.Accounts[0]
	second := bundle.AntigravityAccounts.Accounts[1]
	if first.RefreshToken != "refresh-ready" || first.Enabled == nil || !*first.Enabled {
		t.Fatalf("first account = %#v", first)
	}
	if second.RefreshToken != "refresh-degraded" || second.Enabled == nil || !*second.Enabled {
		t.Fatalf("second account = %#v", second)
	}
	if second.LastSwitchReason != routingDisplayStateDegradedEnabled {
		t.Fatalf("last_switch_reason = %q", second.LastSwitchReason)
	}
}

func TestBuildOpenCodeConfigBundleExportsGeminiCooldownState(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	cooldownUntil := now.Add(5 * time.Second)
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                      "gemini-seat-cooldown",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-cooldown",
				OperatorEmail:           "cooldown@example.com",
				AntigravityProjectID:    "project-cooldown",
				GeminiProviderCheckedAt: now,
				GeminiOperationalState:  geminiOperationalTruthStateCooldown,
				GeminiOperationalReason: "You have exhausted your capacity on this model.",
				RateLimitUntil:          cooldownUntil,
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
		t.Fatalf("accounts = %d", len(bundle.AntigravityAccounts.Accounts))
	}
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.Enabled == nil || *account.Enabled {
		t.Fatalf("account = %#v", account)
	}
	if account.CoolingDownUntil != cooldownUntil.UnixMilli() {
		t.Fatalf("cooling_down_until = %d", account.CoolingDownUntil)
	}
	if account.LastSwitchReason != "rate_limited" {
		t.Fatalf("last_switch_reason = %q", account.LastSwitchReason)
	}
	if account.CooldownReason == "" || !strings.Contains(account.CooldownReason, "capacity") {
		t.Fatalf("cooldown_reason = %q", account.CooldownReason)
	}
	if account.CachedQuota["operational_truth_state"] != geminiOperationalTruthStateCooldown {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if account.CachedQuota["routing_state"] != routingDisplayStateCooldown {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if account.CachedQuota["routing_block_reason"] != "rate_limited" {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
}

func TestBuildOpenCodeConfigBundleExportsGeminiModelRateLimitResetTimes(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	resetAt := now.Add(3 * time.Minute).Truncate(time.Second)
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                       "gemini-seat-model-cooldown",
				Type:                     AccountTypeGemini,
				RefreshToken:             "refresh-model-cooldown",
				OperatorEmail:            "model-cooldown@example.com",
				AntigravityProjectID:     "project-model-cooldown",
				GeminiProviderCheckedAt:  now,
				GeminiProviderTruthReady: true,
				GeminiProviderTruthState: geminiProviderTruthStateReady,
				GeminiOperationalState:   geminiOperationalTruthStateCooldown,
				GeminiModelRateLimitResetTimes: map[string]time.Time{
					"gemini-3.1-pro-high": resetAt,
				},
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
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.Enabled == nil || !*account.Enabled {
		t.Fatalf("enabled = %#v", account.Enabled)
	}
	if account.CoolingDownUntil != 0 {
		t.Fatalf("cooling_down_until = %d", account.CoolingDownUntil)
	}
	if account.LastSwitchReason != routingDisplayStateDegradedEnabled {
		t.Fatalf("last_switch_reason = %q", account.LastSwitchReason)
	}
	if !strings.Contains(account.CooldownReason, "gemini-3.1-pro-high") {
		t.Fatalf("cooldown_reason = %q", account.CooldownReason)
	}
	if account.RateLimitResetTimes["gemini-3.1-pro-high"] != resetAt.UnixMilli() {
		t.Fatalf("rate_limit_reset_times = %#v", account.RateLimitResetTimes)
	}
	resetTimes, _ := account.CachedQuota["rate_limit_reset_times"].(map[string]int64)
	if resetTimes["gemini-3.1-pro-high"] != resetAt.UnixMilli() {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	cachedModels := account.CachedQuota["models"].([]map[string]any)
	if len(cachedModels) != 1 {
		t.Fatalf("cached_quota.models = %#v", cachedModels)
	}
	if cachedModels[0]["name"] != "gemini-3.1-pro-high" || cachedModels[0]["percentage"] != 100 || cachedModels[0]["reset_time"] != resetAt.Format(time.RFC3339) {
		t.Fatalf("cached model = %#v", cachedModels[0])
	}
	if account.CachedQuota["routing_state"] != routingDisplayStateDegradedEnabled {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if got := account.CachedQuota["routing_reason"]; got == nil || !strings.Contains(got.(string), "gemini-3.1-pro-high") {
		t.Fatalf("routing_reason = %#v", got)
	}
}

func TestBuildOpenCodeConfigBundleExportsWarmedMissingProjectGeminiAsRoutable(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                      "gemini-seat-missing-project",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-missing-project",
				OperatorEmail:           "missing-project@example.com",
				OperatorSource:          geminiOperatorSourceAntigravityImport,
				OAuthProfileID:          geminiOAuthAntigravityProfileID,
				GeminiProviderCheckedAt: now,
				GeminiQuotaUpdatedAt:    now,
				GeminiOperationalState:  geminiOperationalTruthStateDegradedOK,
				GeminiOperationalReason: "operator smoke succeeded via fallback project",
				GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
					Name:          "gemini-3.1-pro-high",
					RouteProvider: "gemini",
					Percentage:    81,
				}},
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
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.Enabled == nil || !*account.Enabled {
		t.Fatalf("enabled = %#v", account.Enabled)
	}
	if account.ProjectID != "" || account.ManagedProjectID != "" {
		t.Fatalf("project fields = %#v", account)
	}
	if _, ok := account.CachedQuota["project_id"]; ok {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if account.LastSwitchReason != routingDisplayStateDegradedEnabled {
		t.Fatalf("last_switch_reason = %q", account.LastSwitchReason)
	}
	if !strings.Contains(account.CooldownReason, "fallback project") {
		t.Fatalf("cooldown_reason = %q", account.CooldownReason)
	}
	cachedModels := account.CachedQuota["models"].([]map[string]any)
	if len(cachedModels) != 1 {
		t.Fatalf("cached_quota.models = %#v", cachedModels)
	}
	if cachedModels[0]["routable"] != true {
		t.Fatalf("cached model = %#v", cachedModels[0])
	}
	if got := cachedModels[0]["compatibility_reason"]; got != nil && got != "" {
		t.Fatalf("cached model = %#v", cachedModels[0])
	}
	if account.CachedQuota["provider_truth_state"] != geminiProviderTruthStateMissingProjectID {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if account.CachedQuota["routing_state"] != routingDisplayStateDegradedEnabled {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if got := account.CachedQuota["routing_reason"]; got == nil || !strings.Contains(got.(string), "fallback project") {
		t.Fatalf("routing_reason = %#v", got)
	}
}

func TestBuildOpenCodeConfigBundleOmitsExpiredGeminiCooldownMetadata(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                      "gemini-seat-ready",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-ready",
				OperatorEmail:           "ready@example.com",
				AntigravityProjectID:    "project-ready",
				GeminiProviderCheckedAt: now,
				GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
				RateLimitUntil:          now.Add(-time.Minute),
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
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.CoolingDownUntil != 0 {
		t.Fatalf("cooling_down_until = %d", account.CoolingDownUntil)
	}
	if account.CooldownReason != "" {
		t.Fatalf("cooldown_reason = %q", account.CooldownReason)
	}
}

func TestBuildOpenCodeConfigBundleExportsStaleQuotaRoutingMetadata(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	now := time.Now().UTC()
	freshUntil := now.Add(-2 * time.Minute)
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:                      "gemini-seat-stale-quota",
				Type:                    AccountTypeGemini,
				RefreshToken:            "refresh-stale-quota",
				OperatorEmail:           "stale-quota@example.com",
				AntigravityProjectID:    "project-stale-quota",
				GeminiProviderCheckedAt: now,
				GeminiQuotaUpdatedAt:    freshUntil.Add(-geminiProviderTruthFreshnessWindow),
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
	account := bundle.AntigravityAccounts.Accounts[0]
	if account.Enabled == nil || *account.Enabled {
		t.Fatalf("account = %#v", account)
	}
	if account.LastSwitchReason != "stale_quota_snapshot" {
		t.Fatalf("last_switch_reason = %q", account.LastSwitchReason)
	}
	if account.CachedQuota["routing_block_reason"] != "stale_quota_snapshot" {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
	if got := account.CachedQuota["routing_reason"]; got != "quota snapshot is older than the freshness window" {
		t.Fatalf("routing_reason = %#v", got)
	}
	if account.CachedQuota["routing_recovery_at"] == "" {
		t.Fatalf("cached_quota = %#v", account.CachedQuota)
	}
}
