package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	openCodeAntigravityProviderID   = "codex-pool"
	openCodeConfigFileName          = "opencode.json"
	openCodeAntigravityAccountsFile = "pool-gemini-accounts.json"
)

type openCodePluginAccount struct {
	Email               string           `json:"email,omitempty"`
	RefreshToken        string           `json:"refreshToken"`
	ProjectID           string           `json:"projectId,omitempty"`
	AddedAt             int64            `json:"addedAt"`
	LastUsed            int64            `json:"lastUsed"`
	CachedQuota         map[string]any   `json:"cachedQuota,omitempty"`
	CachedQuotaUpdated  int64            `json:"cachedQuotaUpdatedAt,omitempty"`
	Enabled             *bool            `json:"enabled,omitempty"`
	LastSwitchReason    string           `json:"lastSwitchReason,omitempty"`
	CoolingDownUntil    int64            `json:"coolingDownUntil,omitempty"`
	CooldownReason      string           `json:"cooldownReason,omitempty"`
	ManagedProjectID    string           `json:"managedProjectId,omitempty"`
	RateLimitResetTimes map[string]int64 `json:"rateLimitResetTimes,omitempty"`
}

type openCodePluginAccountsFile struct {
	Version             int                     `json:"version"`
	Accounts            []openCodePluginAccount `json:"accounts"`
	ActiveIndex         int                     `json:"activeIndex"`
	ActiveIndexByFamily map[string]int          `json:"activeIndexByFamily"`
}

type openCodeConfigBundle struct {
	ProviderID          string                     `json:"provider_id"`
	BaseURL             string                     `json:"base_url"`
	APIKey              string                     `json:"api_key"`
	ConfigFile          string                     `json:"config_file"`
	AccountsFile        string                     `json:"accounts_file"`
	OpenCodeConfig      map[string]any             `json:"opencode_config"`
	AntigravityAccounts openCodePluginAccountsFile `json:"pool_gemini_accounts"`
}

func normalizeOpenCodeBaseURL(input string) string {
	trimmed := strings.TrimSpace(strings.TrimRight(input, "/"))
	if trimmed == "" {
		return "/v1"
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

func defaultOpenCodeProviderModels() map[string]any {
	return map[string]any{
		"gemini-2.5-flash": map[string]any{
			"name": "Gemini 2.5 Flash",
		},
		"gemini-2.5-flash-lite": map[string]any{
			"name": "Gemini 2.5 Flash Lite",
		},
		"gemini-2.5-flash-thinking": map[string]any{
			"name": "Gemini 2.5 Flash Thinking",
		},
		"gemini-2.5-pro": map[string]any{
			"name": "Gemini 2.5 Pro",
		},
		"gemini-3-flash": map[string]any{
			"name": "Gemini 3 Flash",
		},
		"gemini-3-flash-agent": map[string]any{
			"name": "Gemini 3 Flash Agent",
		},
		"gemini-3-pro-high": map[string]any{
			"name": "Gemini 3 Pro High",
		},
		"gemini-3-pro-low": map[string]any{
			"name": "Gemini 3 Pro Low",
		},
		"gemini-3-pro-preview": map[string]any{
			"name": "Gemini 3 Pro Preview",
		},
		"gemini-3.1-flash-image": map[string]any{
			"name": "Gemini 3.1 Flash Image",
		},
		"gemini-3.1-flash-lite": map[string]any{
			"name": "Gemini 3.1 Flash Lite",
		},
		"gemini-3.1-pro": map[string]any{
			"name": "Gemini 3.1 Pro",
		},
		"gemini-3.1-pro-high": map[string]any{
			"name": "Gemini 3.1 Pro High",
		},
		"gemini-3.1-pro-low": map[string]any{
			"name": "Gemini 3.1 Pro Low",
		},
		"gemini-3.1-pro-preview": map[string]any{
			"name": "Gemini 3.1 Pro Preview",
		},
	}
}

func mergeOpenCodeProviderModelMetadata(models map[string]any, model GeminiModelQuotaSnapshot, protected bool) {
	name := strings.TrimSpace(model.Name)
	if name == "" {
		return
	}
	_, knownModel := models[name]
	existing, _ := models[name].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
		models[name] = existing
	}
	if strings.TrimSpace(model.DisplayName) != "" && !knownModel {
		existing["name"] = strings.TrimSpace(model.DisplayName)
	} else if _, ok := existing["name"]; !ok {
		existing["name"] = name
	}
	if model.MaxTokens > 0 {
		existing["maxTokens"] = model.MaxTokens
	}
	if model.MaxOutputTokens > 0 {
		existing["maxOutputTokens"] = model.MaxOutputTokens
	}
	if model.SupportsImages {
		existing["supportsImages"] = true
	}
	if model.SupportsThinking {
		existing["supportsThinking"] = true
	}
	if model.ThinkingBudget > 0 {
		existing["thinkingBudget"] = model.ThinkingBudget
	}
	if protected {
		existing["protected"] = true
	}
}

func buildOpenCodeProviderModels(h *proxyHandler) map[string]any {
	models := defaultOpenCodeProviderModels()
	if h == nil || h.pool == nil {
		return models
	}

	now := time.Now().UTC()
	h.pool.mu.RLock()
	accs := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()

	for _, acc := range accs {
		snapshot := snapshotAccountState(acc, now, "", "")
		if snapshot.Type != AccountTypeGemini || snapshot.Disabled || snapshot.Dead {
			continue
		}
		protected := make(map[string]struct{}, len(snapshot.GeminiProtectedModels))
		for _, modelID := range snapshot.GeminiProtectedModels {
			protected[strings.TrimSpace(modelID)] = struct{}{}
		}
		for _, model := range snapshot.GeminiQuotaModels {
			routeProvider := firstNonEmpty(strings.TrimSpace(model.RouteProvider), geminiQuotaModelRouteProvider(model.Name))
			if routeProvider != "gemini" {
				continue
			}
			_, isProtected := protected[strings.TrimSpace(model.Name)]
			mergeOpenCodeProviderModelMetadata(models, model, isProtected)
		}
	}
	return models
}

func buildOpenCodeConfigDocument(baseURL, apiKey string, models map[string]any) map[string]any {
	if len(models) == 0 {
		models = defaultOpenCodeProviderModels()
	}
	return map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"model":   openCodeAntigravityProviderID + "/gemini-3.1-flash-lite",
		"provider": map[string]any{
			openCodeAntigravityProviderID: map[string]any{
				// OpenCode reaches this local Gemini pool over its Anthropic-compatible transport.
				"npm":  "@ai-sdk/anthropic",
				"name": "Codex Pool Gemini",
				"options": map[string]any{
					"baseURL": normalizeOpenCodeBaseURL(baseURL),
					"apiKey":  apiKey,
				},
				"models": models,
			},
		},
	}
}

func openCodeAccountAddedAtMillis(snapshot accountSnapshot) int64 {
	for _, ts := range []time.Time{snapshot.LastUsed, snapshot.LastRefresh, snapshot.GeminiProviderCheckedAt} {
		if !ts.IsZero() {
			return ts.UTC().UnixMilli()
		}
	}
	return 0
}

func openCodeAccountLastUsedMillis(snapshot accountSnapshot) int64 {
	if snapshot.LastUsed.IsZero() {
		return 0
	}
	return snapshot.LastUsed.UTC().UnixMilli()
}

func buildOpenCodeCachedQuota(snapshot accountSnapshot) map[string]any {
	quota := make(map[string]any)
	if !snapshot.GeminiQuotaUpdatedAt.IsZero() {
		quota["last_updated"] = snapshot.GeminiQuotaUpdatedAt.UTC().Unix()
	}
	if !snapshot.GeminiProviderCheckedAt.IsZero() {
		quota["provider_checked_at"] = snapshot.GeminiProviderCheckedAt.UTC().Unix()
	}
	if tier := firstNonEmpty(strings.TrimSpace(snapshot.GeminiSubscriptionTierID), strings.TrimSpace(snapshot.GeminiSubscriptionTierName)); tier != "" {
		quota["subscription_tier"] = tier
	}
	if len(snapshot.GeminiModelForwardingRules) > 0 {
		quota["model_forwarding_rules"] = cloneStringMap(snapshot.GeminiModelForwardingRules)
	}
	if len(snapshot.GeminiProtectedModels) > 0 {
		quota["protected_models"] = normalizeStringSlice(snapshot.GeminiProtectedModels)
	}
	if len(snapshot.GeminiModelRateLimitResetTimes) > 0 {
		resetTimes := make(map[string]int64, len(snapshot.GeminiModelRateLimitResetTimes))
		for model, resetAt := range snapshot.GeminiModelRateLimitResetTimes {
			if resetAt.IsZero() {
				continue
			}
			resetTimes[model] = resetAt.UTC().UnixMilli()
		}
		if len(resetTimes) > 0 {
			quota["rate_limit_reset_times"] = resetTimes
		}
	}
	if len(snapshot.GeminiQuotaModels) > 0 {
		models := make([]map[string]any, 0, len(snapshot.GeminiQuotaModels))
		protected := make(map[string]struct{}, len(snapshot.GeminiProtectedModels))
		for _, modelID := range snapshot.GeminiProtectedModels {
			protected[strings.TrimSpace(modelID)] = struct{}{}
		}
		for _, model := range snapshot.GeminiQuotaModels {
			routeProvider := firstNonEmpty(strings.TrimSpace(model.RouteProvider), geminiQuotaModelRouteProvider(model.Name))
			runtimeSupport := geminiQuotaModelRuntimeSupportForSnapshot(snapshot, routeProvider)
			entry := map[string]any{
				"name":               strings.TrimSpace(model.Name),
				"route_provider":     routeProvider,
				"routable":           runtimeSupport.Routable,
				"compatibility_lane": runtimeSupport.CompatibilityLane,
			}
			if runtimeSupport.CompatibilityReason != "" {
				entry["compatibility_reason"] = runtimeSupport.CompatibilityReason
			}
			if model.Percentage > 0 {
				entry["percentage"] = model.Percentage
			}
			if strings.TrimSpace(model.ResetTime) != "" {
				entry["reset_time"] = strings.TrimSpace(model.ResetTime)
			}
			if strings.TrimSpace(model.DisplayName) != "" {
				entry["display_name"] = strings.TrimSpace(model.DisplayName)
			}
			if model.SupportsImages {
				entry["supports_images"] = true
			}
			if model.SupportsThinking {
				entry["supports_thinking"] = true
			}
			if model.ThinkingBudget > 0 {
				entry["thinking_budget"] = model.ThinkingBudget
			}
			if model.Recommended {
				entry["recommended"] = true
			}
			if model.MaxTokens > 0 {
				entry["max_tokens"] = model.MaxTokens
			}
			if model.MaxOutputTokens > 0 {
				entry["max_output_tokens"] = model.MaxOutputTokens
			}
			if len(model.SupportedMimeTypes) > 0 {
				entry["supported_mime_types"] = cloneSupportedMimeTypes(model.SupportedMimeTypes)
			}
			if _, ok := protected[strings.TrimSpace(model.Name)]; ok {
				entry["protected"] = true
			}
			models = append(models, entry)
		}
		quota["models"] = models
	}
	quota["provider_truth_ready"] = snapshot.GeminiProviderTruthReady
	if strings.TrimSpace(snapshot.GeminiProviderTruthState) != "" {
		quota["provider_truth_state"] = strings.TrimSpace(snapshot.GeminiProviderTruthState)
	}
	if strings.TrimSpace(snapshot.GeminiOperationalState) != "" {
		quota["operational_truth_state"] = strings.TrimSpace(snapshot.GeminiOperationalState)
	}
	if reason := sanitizeStatusMessage(snapshot.GeminiOperationalReason); reason != "" {
		quota["operational_truth_reason"] = reason
	}
	if strings.TrimSpace(snapshot.AntigravityProjectID) != "" {
		quota["project_id"] = strings.TrimSpace(snapshot.AntigravityProjectID)
	}
	if len(quota) == 0 {
		return nil
	}
	return quota
}

func openCodeGeminiRoutingRank(enabled bool, state string) int {
	if !enabled {
		return 3
	}
	switch strings.TrimSpace(state) {
	case "", routingDisplayStateEnabled:
		return 0
	case routingDisplayStateDegradedEnabled:
		return 1
	default:
		return 2
	}
}

func openCodeGeminiOperationalRank(state string) int {
	switch strings.TrimSpace(state) {
	case "", geminiOperationalTruthStateCleanOK:
		return 0
	case geminiOperationalTruthStateCooldown:
		return 1
	case geminiOperationalTruthStateDegradedOK:
		return 2
	case geminiOperationalTruthStateHardFail:
		return 3
	default:
		return 4
	}
}

func buildOpenCodeAntigravityAccounts(h *proxyHandler) openCodePluginAccountsFile {
	out := openCodePluginAccountsFile{
		Version: 3,
		ActiveIndexByFamily: map[string]int{
			"claude": 0,
			"gemini": 0,
		},
	}
	if h == nil || h.pool == nil {
		return out
	}

	now := time.Now().UTC()
	h.pool.mu.RLock()
	accs := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()

	type accountRow struct {
		sortKeyEnabled         bool
		sortKeyProviderReady   bool
		sortKeyRoutingRank     int
		sortKeyOperationalRank int
		sortKeyCurrent         bool
		sortKeyLast            int64
		sortKeyEmail           string
		sortKeyID              string
		account                openCodePluginAccount
	}

	rows := make([]accountRow, 0, len(accs))
	for _, acc := range accs {
		snapshot := snapshotAccountState(acc, now, "", "")
		if snapshot.Type != AccountTypeGemini {
			continue
		}
		refreshToken := strings.TrimSpace(snapshot.RefreshToken)
		if refreshToken == "" || snapshot.Disabled || snapshot.Dead {
			continue
		}
		addedAt := openCodeAccountAddedAtMillis(snapshot)
		lastUsed := openCodeAccountLastUsedMillis(snapshot)
		email := firstNonEmpty(strings.TrimSpace(snapshot.OperatorEmail), strings.TrimSpace(snapshot.AntigravityEmail))
		projectID := strings.TrimSpace(snapshot.AntigravityProjectID)
		routing := buildPoolDashboardRouting(snapshot, snapshot.Routing, now)
		enabled := routing.Eligible
		quota := buildOpenCodeCachedQuota(snapshot)
		if quota == nil {
			quota = make(map[string]any)
		}
		if routingState := strings.TrimSpace(routing.State); routingState != "" {
			quota["routing_state"] = routingState
		}
		if blockReason := strings.TrimSpace(routing.BlockReason); blockReason != "" {
			quota["routing_block_reason"] = blockReason
		}
		if routingReason := strings.TrimSpace(routing.DegradedReason); routingReason != "" {
			quota["routing_reason"] = routingReason
		}
		if recoveryAt := strings.TrimSpace(routing.RecoveryAt); recoveryAt != "" {
			quota["routing_recovery_at"] = recoveryAt
		}
		if len(quota) == 0 {
			quota = nil
		}
		account := openCodePluginAccount{
			Email:            email,
			RefreshToken:     refreshToken,
			ProjectID:        projectID,
			ManagedProjectID: projectID,
			AddedAt:          addedAt,
			LastUsed:         lastUsed,
			CachedQuota:      quota,
			Enabled:          &enabled,
		}
		if !snapshot.GeminiQuotaUpdatedAt.IsZero() {
			account.CachedQuotaUpdated = snapshot.GeminiQuotaUpdatedAt.UTC().UnixMilli()
		}
		if len(snapshot.GeminiModelRateLimitResetTimes) > 0 {
			account.RateLimitResetTimes = make(map[string]int64, len(snapshot.GeminiModelRateLimitResetTimes))
			for model, resetAt := range snapshot.GeminiModelRateLimitResetTimes {
				if resetAt.IsZero() {
					continue
				}
				account.RateLimitResetTimes[model] = resetAt.UTC().UnixMilli()
			}
			if len(account.RateLimitResetTimes) == 0 {
				account.RateLimitResetTimes = nil
			}
		}
		if snapshot.RateLimitUntil.After(now) {
			account.CoolingDownUntil = snapshot.RateLimitUntil.UTC().UnixMilli()
			account.CooldownReason = firstNonEmpty(strings.TrimSpace(routing.DegradedReason), strings.TrimSpace(routing.BlockReason), "rate_limited")
		}
		if !enabled {
			account.LastSwitchReason = firstNonEmpty(
				strings.TrimSpace(routing.BlockReason),
				strings.TrimSpace(routing.State),
				"blocked",
			)
			if account.CooldownReason == "" {
				account.CooldownReason = firstNonEmpty(
					strings.TrimSpace(routing.DegradedReason),
					strings.TrimSpace(routing.BlockReason),
				)
			}
		} else if strings.TrimSpace(routing.State) == routingDisplayStateDegradedEnabled {
			account.LastSwitchReason = routingDisplayStateDegradedEnabled
			account.CooldownReason = strings.TrimSpace(routing.DegradedReason)
		}
		rows = append(rows, accountRow{
			sortKeyEnabled:         enabled,
			sortKeyProviderReady:   snapshot.GeminiProviderTruthReady,
			sortKeyRoutingRank:     openCodeGeminiRoutingRank(enabled, routing.State),
			sortKeyOperationalRank: openCodeGeminiOperationalRank(snapshot.GeminiOperationalState),
			sortKeyCurrent:         snapshot.AntigravityCurrent,
			sortKeyLast:            addedAt,
			sortKeyEmail:           strings.ToLower(email),
			sortKeyID:              snapshot.ID,
			account:                account,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].sortKeyRoutingRank != rows[j].sortKeyRoutingRank {
			return rows[i].sortKeyRoutingRank < rows[j].sortKeyRoutingRank
		}
		if rows[i].sortKeyProviderReady != rows[j].sortKeyProviderReady {
			return rows[i].sortKeyProviderReady
		}
		if rows[i].sortKeyOperationalRank != rows[j].sortKeyOperationalRank {
			return rows[i].sortKeyOperationalRank < rows[j].sortKeyOperationalRank
		}
		if rows[i].sortKeyCurrent != rows[j].sortKeyCurrent {
			return rows[i].sortKeyCurrent
		}
		if rows[i].sortKeyLast != rows[j].sortKeyLast {
			return rows[i].sortKeyLast > rows[j].sortKeyLast
		}
		if rows[i].sortKeyEmail != rows[j].sortKeyEmail {
			return rows[i].sortKeyEmail < rows[j].sortKeyEmail
		}
		return rows[i].sortKeyID < rows[j].sortKeyID
	})

	out.Accounts = make([]openCodePluginAccount, 0, len(rows))
	activeIndex := 0
	activeFound := false
	for _, row := range rows {
		if !activeFound && row.account.Enabled != nil && *row.account.Enabled {
			activeIndex = len(out.Accounts)
			activeFound = true
		}
		out.Accounts = append(out.Accounts, row.account)
	}
	if len(out.Accounts) == 0 {
		out.ActiveIndex = 0
		return out
	}
	out.ActiveIndex = activeIndex
	out.ActiveIndexByFamily["gemini"] = activeIndex
	return out
}

func (h *proxyHandler) buildOpenCodeConfigBundle(r *http.Request, user *PoolUser, secret string) (*openCodeConfigBundle, error) {
	if user == nil {
		return nil, nil
	}
	auth, err := generateClaudeAuth(secret, user)
	if err != nil {
		return nil, err
	}
	baseURL := h.getEffectivePublicURL(r)
	return &openCodeConfigBundle{
		ProviderID:          openCodeAntigravityProviderID,
		BaseURL:             normalizeOpenCodeBaseURL(baseURL),
		APIKey:              auth.AccessToken,
		ConfigFile:          openCodeConfigFileName,
		AccountsFile:        openCodeAntigravityAccountsFile,
		OpenCodeConfig:      buildOpenCodeConfigDocument(baseURL, auth.AccessToken, buildOpenCodeProviderModels(h)),
		AntigravityAccounts: buildOpenCodeAntigravityAccounts(h),
	}, nil
}
