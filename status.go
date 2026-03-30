package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"
)

// StatusData contains all the data for the status page.
type StatusData struct {
	GeneratedAt          time.Time                     `json:"generated_at"`
	Uptime               time.Duration                 `json:"uptime"`
	TotalCount           int                           `json:"total_count"`
	CodexCount           int                           `json:"codex_count"`
	CodexSeatCount       int                           `json:"codex_seat_count,omitempty"`
	GeminiCount          int                           `json:"gemini_count"`
	ClaudeCount          int                           `json:"claude_count"`
	KimiCount            int                           `json:"kimi_count"`
	MinimaxCount         int                           `json:"minimax_count"`
	PoolUsers            int                           `json:"pool_users,omitempty"`
	OpenAIAPIPool        OpenAIAPIPoolStatus           `json:"openai_api_pool"`
	GitLabClaudePool     GitLabClaudePoolStatus        `json:"gitlab_claude_pool"`
	GeminiOperator       GeminiOperatorStatus          `json:"gemini_operator"`
	GeminiPool           *GeminiPoolStatus             `json:"gemini_pool,omitempty"`
	Quarantine           QuarantineStatus              `json:"quarantine,omitempty"`
	PoolSummary          PoolDashboardSummary          `json:"pool_summary"`
	CurrentSeat          *CurrentSeatStatus            `json:"current_seat,omitempty"`
	ActiveSeat           *CurrentSeatStatus            `json:"active_seat,omitempty"`
	LastUsedSeat         *CurrentSeatStatus            `json:"last_used_seat,omitempty"`
	BestEligibleSeat     *CurrentSeatStatus            `json:"best_eligible_seat,omitempty"`
	WorkspaceGroups      []PoolDashboardWorkspaceGroup `json:"workspace_groups"`
	Accounts             []AccountStatus               `json:"accounts"`
	TokenAnalytics       *TokenAnalytics               `json:"token_analytics,omitempty"`
	PoolUtilization      []PoolUtilization             `json:"pool_utilization,omitempty"`
	CodexForcedPlan      string                        `json:"codex_forced_plan,omitempty"`
	LocalOperatorEnabled bool                          `json:"-"`
}

type PoolDashboardSummary struct {
	TotalAccounts    int                                 `json:"total_accounts"`
	EligibleAccounts int                                 `json:"eligible_accounts"`
	WorkspaceCount   int                                 `json:"workspace_count"`
	NextRecoveryAt   string                              `json:"next_recovery_at,omitempty"`
	Providers        map[string]PoolDashboardProviderSum `json:"providers,omitempty"`
}

type PoolDashboardProviderSum struct {
	TotalAccounts            int     `json:"total_accounts"`
	EligibleAccounts         int     `json:"eligible_accounts"`
	TimeWeightedPrimaryPct   float64 `json:"time_weighted_primary_pct"`
	TimeWeightedSecondaryPct float64 `json:"time_weighted_secondary_pct"`
}

type OpenAIAPIPoolStatus struct {
	TotalKeys             int    `json:"total_keys"`
	HealthyKeys           int    `json:"healthy_keys"`
	EligibleKeys          int    `json:"eligible_keys"`
	EligibleUnhealthyKeys int    `json:"eligible_unhealthy_keys,omitempty"`
	DeadKeys              int    `json:"dead_keys"`
	NextKeyID             string `json:"next_key_id,omitempty"`
	StatusNote            string `json:"status_note,omitempty"`
}

type GitLabClaudePoolStatus struct {
	TotalTokens    int    `json:"total_tokens"`
	HealthyTokens  int    `json:"healthy_tokens"`
	EligibleTokens int    `json:"eligible_tokens"`
	DeadTokens     int    `json:"dead_tokens"`
	NextTokenID    string `json:"next_token_id,omitempty"`
}

type GeminiOperatorStatus struct {
	ManagedOAuthAvailable bool   `json:"managed_oauth_available"`
	ManagedOAuthProfile   string `json:"managed_oauth_profile,omitempty"`
	ManagedSeatCount      int    `json:"managed_seat_count"`
	ImportedSeatCount     int    `json:"imported_seat_count"`
	AntigravitySeatCount  int    `json:"antigravity_seat_count"`
	LegacySeatCount       int    `json:"legacy_seat_count,omitempty"`
	Note                  string `json:"note,omitempty"`
}

type GeminiPoolStatus struct {
	TotalSeats             int    `json:"total_seats"`
	EligibleSeats          int    `json:"eligible_seats"`
	CleanEligibleSeats     int    `json:"clean_eligible_seats,omitempty"`
	DegradedEligibleSeats  int    `json:"degraded_eligible_seats,omitempty"`
	ReadySeats             int    `json:"ready_seats"`
	WarmSeats              int    `json:"warm_seats"`
	CooldownSeats          int    `json:"cooldown_seats,omitempty"`
	RestrictedSeats        int    `json:"restricted_seats,omitempty"`
	ValidationFlaggedSeats int    `json:"validation_flagged_seats,omitempty"`
	MissingProjectSeats    int    `json:"missing_project_seats,omitempty"`
	NotWarmedSeats         int    `json:"not_warmed_seats,omitempty"`
	StaleTruthSeats        int    `json:"stale_truth_seats,omitempty"`
	StaleQuotaSeats        int    `json:"stale_quota_seats,omitempty"`
	QuotaTrackedSeats      int    `json:"quota_tracked_seats,omitempty"`
	QuotaModelCount        int    `json:"quota_model_count,omitempty"`
	QuotaEmptySeats        int    `json:"quota_empty_seats,omitempty"`
	ProtectedModelCount    int    `json:"protected_model_count,omitempty"`
	Note                   string `json:"note,omitempty"`
}

type GeminiProviderTruthStatus struct {
	Ready                bool                       `json:"ready"`
	State                string                     `json:"state,omitempty"`
	Reason               string                     `json:"reason,omitempty"`
	FreshnessState       string                     `json:"freshness_state,omitempty"`
	Stale                bool                       `json:"stale,omitempty"`
	StaleReason          string                     `json:"stale_reason,omitempty"`
	FreshUntil           string                     `json:"fresh_until,omitempty"`
	ProjectID            string                     `json:"project_id,omitempty"`
	SubscriptionTierID   string                     `json:"subscription_tier_id,omitempty"`
	SubscriptionTierName string                     `json:"subscription_tier_name,omitempty"`
	ValidationReasonCode string                     `json:"validation_reason_code,omitempty"`
	ValidationMessage    string                     `json:"validation_message,omitempty"`
	ValidationURL        string                     `json:"validation_url,omitempty"`
	CheckedAt            string                     `json:"checked_at,omitempty"`
	ProxyDisabled        bool                       `json:"proxy_disabled,omitempty"`
	Restricted           bool                       `json:"restricted,omitempty"`
	ValidationBlocked    bool                       `json:"validation_blocked,omitempty"`
	QuotaForbidden       bool                       `json:"quota_forbidden,omitempty"`
	QuotaForbiddenReason string                     `json:"quota_forbidden_reason,omitempty"`
	ProtectedModels      []string                   `json:"protected_models,omitempty"`
	RateLimitResetTimes  map[string]string          `json:"rate_limit_reset_times,omitempty"`
	Quota                *GeminiProviderQuotaStatus `json:"quota,omitempty"`
}

type GeminiOperationalTruthStatus struct {
	State         string `json:"state,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Source        string `json:"source,omitempty"`
	CheckedAt     string `json:"checked_at,omitempty"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
}

type GeminiProviderQuotaStatus struct {
	UpdatedAt            string                   `json:"updated_at,omitempty"`
	ModelForwardingRules map[string]string        `json:"model_forwarding_rules,omitempty"`
	Models               []GeminiModelQuotaStatus `json:"models,omitempty"`
}

type GeminiModelQuotaStatus struct {
	Name                string          `json:"name"`
	RouteProvider       string          `json:"route_provider,omitempty"`
	Routable            bool            `json:"routable"`
	CompatibilityLane   string          `json:"compatibility_lane,omitempty"`
	CompatibilityReason string          `json:"compatibility_reason,omitempty"`
	Percentage          int             `json:"percentage"`
	ResetTime           string          `json:"reset_time,omitempty"`
	DisplayName         string          `json:"display_name,omitempty"`
	SupportsImages      bool            `json:"supports_images,omitempty"`
	SupportsThinking    bool            `json:"supports_thinking,omitempty"`
	ThinkingBudget      int             `json:"thinking_budget,omitempty"`
	Recommended         bool            `json:"recommended,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxOutputTokens     int             `json:"max_output_tokens,omitempty"`
	SupportedMimeTypes  map[string]bool `json:"supported_mime_types,omitempty"`
	Protected           bool            `json:"protected,omitempty"`
}

type PoolDashboardWorkspaceGroup struct {
	WorkspaceID       string   `json:"workspace_id"`
	Provider          string   `json:"provider"`
	SeatCount         int      `json:"seat_count"`
	EligibleSeatCount int      `json:"eligible_seat_count"`
	BlockedSeatCount  int      `json:"blocked_seat_count"`
	NextRecoveryAt    string   `json:"next_recovery_at,omitempty"`
	SeatKeys          []string `json:"seat_keys"`
	AccountIDs        []string `json:"account_ids"`
	Emails            []string `json:"emails,omitempty"`
}

type CurrentSeatStatus struct {
	ID                     string  `json:"id"`
	Email                  string  `json:"email,omitempty"`
	WorkspaceID            string  `json:"workspace_id,omitempty"`
	SeatKey                string  `json:"seat_key,omitempty"`
	RoutingStatus          string  `json:"routing_status,omitempty"`
	PrimaryHeadroomPct     float64 `json:"primary_headroom_pct"`
	SecondaryHeadroomPct   float64 `json:"secondary_headroom_pct"`
	PrimaryHeadroomKnown   bool    `json:"primary_headroom_known,omitempty"`
	SecondaryHeadroomKnown bool    `json:"secondary_headroom_known,omitempty"`
	Inflight               int64   `json:"inflight"`
	LocalLastUsed          string  `json:"local_last_used,omitempty"`
	ActiveSeatCount        int     `json:"active_seat_count,omitempty"`
	Basis                  string  `json:"basis"`
}

func geminiOperatorSourceLabel(source string) string {
	switch strings.TrimSpace(source) {
	case geminiOperatorSourceManagedOAuth:
		return "legacy managed oauth"
	case geminiOperatorSourceManualImport, geminiOperatorSourceManualImportLegacy:
		return "legacy local import"
	case geminiOperatorSourceAntigravityImport:
		return "Gemini Browser Auth"
	default:
		return ""
	}
}

func managedOpenAIAPIProbeState(snapshot accountSnapshot, now time.Time) string {
	healthStatus := strings.TrimSpace(snapshot.HealthStatus)
	if snapshot.HealthCheckedAt.IsZero() {
		return "never_probed"
	}
	if healthStatus == "healthy" {
		if now.Sub(snapshot.HealthCheckedAt) >= managedOpenAIAPIProbeFreshness {
			return "stale"
		}
		return "healthy"
	}
	return "error"
}

func managedOpenAIAPIProbeSummary(snapshot accountSnapshot, now time.Time) string {
	prefix := ""
	if snapshot.Routing.Eligible {
		prefix = "selector-eligible; "
	}
	switch managedOpenAIAPIProbeState(snapshot, now) {
	case "never_probed":
		return prefix + "probe has not run yet"
	case "healthy":
		if snapshot.Routing.Eligible {
			return prefix + "last probe succeeded"
		}
		return "last probe succeeded; selector-blocked"
	case "stale":
		return prefix + "last healthy probe is stale and will refresh on next use"
	default:
		if strings.Contains(strings.ToLower(strings.TrimSpace(snapshot.HealthError)), "deadline exceeded") {
			return prefix + "last probe timed out"
		}
		return prefix + "last probe failed"
	}
}

func displayAccountHealthStatus(snapshot accountSnapshot, routing routingState) string {
	healthStatus := strings.TrimSpace(snapshot.HealthStatus)
	if snapshot.Type != AccountTypeGemini || snapshot.Dead || snapshot.Disabled {
		return healthStatus
	}
	if strings.TrimSpace(snapshot.GeminiOperationalState) == geminiOperationalTruthStateCooldown ||
		strings.TrimSpace(routing.BlockReason) == "rate_limited" {
		return geminiOperationalTruthStateCooldown
	}
	return healthStatus
}

func managedOpenAIAPIPoolStatusNote(pool OpenAIAPIPoolStatus) string {
	if pool.TotalKeys == 0 {
		return ""
	}
	note := "Healthy counts only keys whose last probe succeeded. Eligible now follows selector routing."
	if pool.EligibleUnhealthyKeys > 0 {
		note += " Some eligible keys still do not have a fresh healthy probe."
	}
	return note
}

func geminiOperatorStatusNote(status GeminiOperatorStatus) string {
	parts := []string{"Gemini Browser Auth is the only supported Gemini seat onboarding flow for this pool."}
	if status.LegacySeatCount > 0 {
		parts = append(parts, fmt.Sprintf("%d legacy local Gemini seat(s) still remain in the pool.", status.LegacySeatCount))
	}
	if status.ManagedSeatCount > 0 {
		if profile := strings.TrimSpace(status.ManagedOAuthProfile); profile != "" {
			parts = append(parts, fmt.Sprintf("%d service-owned Gemini OAuth seat(s) still remain for legacy maintenance via %s.", status.ManagedSeatCount, profile))
		} else {
			parts = append(parts, fmt.Sprintf("%d service-owned Gemini OAuth seat(s) still remain for legacy maintenance.", status.ManagedSeatCount))
		}
	} else if status.ManagedOAuthAvailable {
		if profile := strings.TrimSpace(status.ManagedOAuthProfile); profile != "" {
			parts = append(parts, "Service-owned Gemini OAuth stays configured internally via "+profile+" for legacy maintenance only.")
		} else {
			parts = append(parts, "Service-owned Gemini OAuth stays configured internally for legacy maintenance only.")
		}
	}
	return strings.Join(parts, " ")
}

func geminiPoolStatusNote(status GeminiPoolStatus) string {
	if status.TotalSeats == 0 {
		return ""
	}
	parts := make([]string, 0, 6)
	if status.EligibleSeats > 0 {
		switch {
		case status.CleanEligibleSeats > 0 && status.DegradedEligibleSeats > 0:
			parts = append(parts, fmt.Sprintf("%d eligible (%d clean · %d degraded)", status.EligibleSeats, status.CleanEligibleSeats, status.DegradedEligibleSeats))
		case status.CleanEligibleSeats > 0:
			parts = append(parts, fmt.Sprintf("%d eligible (%d clean)", status.EligibleSeats, status.CleanEligibleSeats))
		case status.DegradedEligibleSeats > 0:
			parts = append(parts, fmt.Sprintf("%d eligible (%d degraded)", status.EligibleSeats, status.DegradedEligibleSeats))
		default:
			parts = append(parts, fmt.Sprintf("%d eligible", status.EligibleSeats))
		}
	}
	if status.ReadySeats > 0 {
		parts = append(parts, fmt.Sprintf("%d ready", status.ReadySeats))
	}
	if status.WarmSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d warmed", status.WarmSeats))
	}
	if status.CooldownSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d cooling down", status.CooldownSeats))
	}
	if status.NotWarmedSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting for warm proof", status.NotWarmedSeats))
	}
	staleProviderSeats := status.StaleTruthSeats - status.StaleQuotaSeats
	if staleProviderSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d stale provider truth", staleProviderSeats))
	}
	if status.StaleQuotaSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d stale quota snapshot", status.StaleQuotaSeats))
	}
	if status.MissingProjectSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d missing project", status.MissingProjectSeats))
	}
	if status.QuotaEmptySeats > 0 {
		parts = append(parts, fmt.Sprintf("%d quota snapshots without model rows", status.QuotaEmptySeats))
	}
	return strings.Join(parts, " · ")
}

// TokenAnalytics contains capacity estimation data for the status page.
type TokenAnalytics struct {
	PlanCapacities []PlanCapacityView
	TotalSamples   int64
	ModelInfo      string
}

// PlanCapacityView is a display-friendly view of plan capacity.
type PlanCapacityView struct {
	PlanType                   string
	SampleCount                int64
	Confidence                 string
	TotalInputTokens           int64
	TotalOutputTokens          int64
	TotalCachedTokens          int64
	TotalReasoningTokens       int64
	TotalBillableTokens        int64
	OutputMultiplier           float64
	EffectivePerPrimaryPct     int64
	EffectivePerSecondaryPct   int64
	EstimatedPrimaryCapacity   string // e.g., "~2.5M tokens"
	EstimatedSecondaryCapacity string
}

// AccountStatus shows the status of a single account.
type AccountStatus struct {
	ID                        string                        `json:"id"`
	Type                      string                        `json:"type"`
	PlanType                  string                        `json:"plan_type,omitempty"`
	AuthMode                  string                        `json:"auth_mode,omitempty"`
	AccountID                 string                        `json:"account_id,omitempty"`
	Email                     string                        `json:"email,omitempty"`
	Subject                   string                        `json:"subject,omitempty"`
	ChatGPTUserID             string                        `json:"chatgpt_user_id,omitempty"`
	WorkspaceID               string                        `json:"workspace_id,omitempty"`
	SeatKey                   string                        `json:"seat_key,omitempty"`
	FallbackOnly              bool                          `json:"fallback_only,omitempty"`
	OperatorSource            string                        `json:"operator_source,omitempty"`
	Disabled                  bool                          `json:"disabled"`
	Dead                      bool                          `json:"dead"`
	PrimaryUsed               float64                       `json:"primary_used_pct"`
	SecondaryUsed             float64                       `json:"secondary_used_pct"`
	EffectivePrimary          float64                       `json:"effective_primary_pct"`
	EffectiveSecondary        float64                       `json:"effective_secondary_pct"`
	Routing                   PoolDashboardRouting          `json:"routing"`
	RecoveryAt                string                        `json:"recovery_at,omitempty"`
	PrimaryResetIn            string                        `json:"primary_reset_in,omitempty"`
	SecondaryResetIn          string                        `json:"secondary_reset_in,omitempty"`
	LastRefreshAt             string                        `json:"last_refresh_at,omitempty"`
	AuthExpiresAt             string                        `json:"auth_expires_at,omitempty"`
	AuthExpiresIn             string                        `json:"auth_expires_in,omitempty"`
	HealthStatus              string                        `json:"health_status,omitempty"`
	HealthError               string                        `json:"health_error,omitempty"`
	HealthCheckedAt           string                        `json:"health_checked_at,omitempty"`
	LastHealthyAt             string                        `json:"last_healthy_at,omitempty"`
	ProbeState                string                        `json:"probe_state,omitempty"`
	ProbeSummary              string                        `json:"probe_summary,omitempty"`
	ProviderSubscriptionTier  string                        `json:"provider_subscription_tier,omitempty"`
	ProviderSubscriptionName  string                        `json:"provider_subscription_name,omitempty"`
	ProviderValidationCode    string                        `json:"provider_validation_code,omitempty"`
	ProviderValidationMessage string                        `json:"provider_validation_message,omitempty"`
	ProviderValidationURL     string                        `json:"provider_validation_url,omitempty"`
	ProviderCheckedAt         string                        `json:"provider_checked_at,omitempty"`
	ProviderTruth             *GeminiProviderTruthStatus    `json:"provider_truth,omitempty"`
	OperationalTruth          *GeminiOperationalTruthStatus `json:"operational_truth,omitempty"`
	ProviderQuotaSummary      string                        `json:"provider_quota_summary,omitempty"`
	DeadSince                 string                        `json:"dead_since,omitempty"`
	LocalLastUsed             string                        `json:"local_last_used,omitempty"`
	UsageObserved             string                        `json:"usage_observed,omitempty"`
	GitLabRateLimitName       string                        `json:"gitlab_rate_limit_name,omitempty"`
	GitLabRateLimitLimit      int                           `json:"gitlab_rate_limit_limit,omitempty"`
	GitLabRateLimitRemaining  int                           `json:"gitlab_rate_limit_remaining"`
	GitLabRateLimitResetAt    string                        `json:"gitlab_rate_limit_reset_at,omitempty"`
	GitLabRateLimitResetIn    string                        `json:"gitlab_rate_limit_reset_in,omitempty"`
	GitLabQuotaExceededCount  int                           `json:"gitlab_quota_exceeded_count,omitempty"`
	GitLabQuotaProbeIn        string                        `json:"gitlab_quota_probe_in,omitempty"`
	Penalty                   float64                       `json:"penalty,omitempty"`
	Score                     float64                       `json:"score"`
	Inflight                  int64                         `json:"inflight"`
	LocalTokens               int64                         `json:"local_tokens"`
}

type PoolDashboardRouting struct {
	State                  string  `json:"state,omitempty"`
	Eligible               bool    `json:"eligible"`
	BlockReason            string  `json:"block_reason,omitempty"`
	DegradedReason         string  `json:"degraded_reason,omitempty"`
	PrimaryUsedPct         float64 `json:"primary_used_pct"`
	SecondaryUsedPct       float64 `json:"secondary_used_pct"`
	PrimaryHeadroomPct     float64 `json:"primary_headroom_pct"`
	SecondaryHeadroomPct   float64 `json:"secondary_headroom_pct"`
	PrimaryHeadroomKnown   bool    `json:"primary_headroom_known,omitempty"`
	SecondaryHeadroomKnown bool    `json:"secondary_headroom_known,omitempty"`
	RecoveryAt             string  `json:"recovery_at,omitempty"`
	CodexRateLimitBypass   bool    `json:"codex_rate_limit_bypass,omitempty"`
	PreemptiveThresholdPct float64 `json:"preemptive_threshold_pct,omitempty"`
}

type poolWorkspaceAccumulator struct {
	WorkspaceID    string
	Provider       string
	SeatKeys       map[string]struct{}
	AccountIDs     map[string]struct{}
	Emails         map[string]struct{}
	SeatCount      int
	EligibleCount  int
	BlockedCount   int
	NextRecoveryAt time.Time
}

const (
	geminiQuotaCompatibilityLaneGeminiFacade             = "gemini_facade"
	geminiQuotaCompatibilityLaneAnthropicAdapterRequired = "anthropic_adapter_required"
)

type geminiQuotaModelRuntimeSupport struct {
	Routable            bool
	CompatibilityLane   string
	CompatibilityReason string
}

func geminiQuotaModelRuntimeSupportForSnapshot(snapshot accountSnapshot, routeProvider string) geminiQuotaModelRuntimeSupport {
	routeProvider = strings.TrimSpace(routeProvider)
	switch routeProvider {
	case "gemini":
		if (!snapshot.AntigravityProxyDisabled && !snapshot.AntigravityQuotaForbidden && snapshot.GeminiProviderTruthReady && !snapshot.AntigravityValidationBlocked) ||
			canRouteValidationBlockedAntigravityGeminiSnapshot(snapshot) ||
			(effectiveGeminiCodeAssistProjectIDForSnapshot(snapshot) != "" && geminiHasOperationalProof(snapshot.GeminiOperationalState)) {
			return geminiQuotaModelRuntimeSupport{
				Routable:          true,
				CompatibilityLane: geminiQuotaCompatibilityLaneGeminiFacade,
			}
		}
		reason := sanitizeStatusMessage(firstNonEmpty(
			strings.TrimSpace(snapshot.GeminiProviderTruthReason),
			strings.TrimSpace(snapshot.GeminiProviderTruthState),
			"seat not ready",
		))
		if reason != "" && !strings.HasPrefix(strings.ToLower(reason), "seat not ready") {
			reason = "seat not ready: " + reason
		}
		return geminiQuotaModelRuntimeSupport{
			Routable:            false,
			CompatibilityLane:   geminiQuotaCompatibilityLaneGeminiFacade,
			CompatibilityReason: reason,
		}
	case "claude":
		return geminiQuotaModelRuntimeSupport{
			Routable:            false,
			CompatibilityLane:   geminiQuotaCompatibilityLaneAnthropicAdapterRequired,
			CompatibilityReason: "quota catalog only; Anthropic-compatible adapter is not implemented",
		}
	default:
		return geminiQuotaModelRuntimeSupport{
			Routable:            false,
			CompatibilityReason: "unsupported route provider",
		}
	}
}

func summarizeGeminiQuotaModels(models []GeminiModelQuotaStatus) string {
	if len(models) == 0 {
		return ""
	}

	total := len(models)
	geminiTotal := 0
	geminiRoutable := 0
	claudeTotal := 0
	otherTotal := 0

	for _, model := range models {
		switch strings.TrimSpace(model.RouteProvider) {
		case "gemini":
			geminiTotal++
			if model.Routable {
				geminiRoutable++
			}
		case "claude":
			claudeTotal++
		default:
			otherTotal++
		}
	}

	parts := []string{fmt.Sprintf("%d models", total)}
	if geminiTotal > 0 {
		switch {
		case geminiRoutable == geminiTotal:
			parts = append(parts, fmt.Sprintf("gemini %d routable", geminiTotal))
		case geminiRoutable == 0:
			parts = append(parts, fmt.Sprintf("gemini %d seat-blocked", geminiTotal))
		default:
			parts = append(parts, fmt.Sprintf("gemini %d (%d routable)", geminiTotal, geminiRoutable))
		}
	}
	if claudeTotal > 0 {
		parts = append(parts, fmt.Sprintf("claude %d catalog-only", claudeTotal))
	}
	if otherTotal > 0 {
		parts = append(parts, fmt.Sprintf("other %d", otherTotal))
	}
	return strings.Join(parts, " · ")
}

func summarizeGeminiQuotaStatus(updatedAt time.Time, models []GeminiModelQuotaStatus) string {
	if len(models) > 0 {
		return summarizeGeminiQuotaModels(models)
	}
	if !updatedAt.IsZero() {
		return "0 models captured"
	}
	return ""
}

type currentSeatCandidate struct {
	status   AccountStatus
	lastUsed time.Time
}

func prefersLiveSeat(next currentSeatCandidate, best *currentSeatCandidate) bool {
	if best == nil {
		return true
	}
	if next.status.Inflight != best.status.Inflight {
		return next.status.Inflight > best.status.Inflight
	}
	if next.lastUsed.IsZero() != best.lastUsed.IsZero() {
		return !next.lastUsed.IsZero()
	}
	if !next.lastUsed.Equal(best.lastUsed) {
		return next.lastUsed.After(best.lastUsed)
	}
	if next.status.Routing.Eligible != best.status.Routing.Eligible {
		return next.status.Routing.Eligible
	}
	if next.status.Score != best.status.Score {
		return next.status.Score > best.status.Score
	}
	return next.status.ID < best.status.ID
}

func prefersLastUsedSeat(next currentSeatCandidate, best *currentSeatCandidate) bool {
	if next.lastUsed.IsZero() {
		return false
	}
	if best == nil {
		return true
	}
	if best.lastUsed.IsZero() {
		return true
	}
	if !next.lastUsed.Equal(best.lastUsed) {
		return next.lastUsed.After(best.lastUsed)
	}
	if next.status.Inflight != best.status.Inflight {
		return next.status.Inflight > best.status.Inflight
	}
	if next.status.Routing.Eligible != best.status.Routing.Eligible {
		return next.status.Routing.Eligible
	}
	if next.status.Score != best.status.Score {
		return next.status.Score > best.status.Score
	}
	return next.status.ID < best.status.ID
}

func prefersBestEligibleSeat(next currentSeatCandidate, best *currentSeatCandidate) bool {
	if !next.status.Routing.Eligible {
		return false
	}
	if best == nil {
		return true
	}
	if next.status.Score != best.status.Score {
		return next.status.Score > best.status.Score
	}
	if next.status.Inflight != best.status.Inflight {
		return next.status.Inflight > best.status.Inflight
	}
	if next.lastUsed.IsZero() != best.lastUsed.IsZero() {
		return !next.lastUsed.IsZero()
	}
	if !next.lastUsed.Equal(best.lastUsed) {
		return next.lastUsed.After(best.lastUsed)
	}
	return next.status.ID < best.status.ID
}

func currentSeatStatusFromCandidate(candidate *currentSeatCandidate, basis string, activeSeatCount int) *CurrentSeatStatus {
	if candidate == nil {
		return nil
	}
	routingStatus := "eligible"
	if !candidate.status.Routing.Eligible && strings.TrimSpace(candidate.status.Routing.BlockReason) != "" {
		routingStatus = strings.TrimSpace(candidate.status.Routing.BlockReason)
	} else if strings.TrimSpace(candidate.status.Routing.State) != "" {
		routingStatus = strings.TrimSpace(candidate.status.Routing.State)
	} else if strings.TrimSpace(candidate.status.Routing.BlockReason) != "" {
		routingStatus = strings.TrimSpace(candidate.status.Routing.BlockReason)
	}
	return &CurrentSeatStatus{
		ID:                     candidate.status.ID,
		Email:                  candidate.status.Email,
		WorkspaceID:            candidate.status.WorkspaceID,
		SeatKey:                candidate.status.SeatKey,
		RoutingStatus:          routingStatus,
		PrimaryHeadroomPct:     candidate.status.Routing.PrimaryHeadroomPct,
		SecondaryHeadroomPct:   candidate.status.Routing.SecondaryHeadroomPct,
		PrimaryHeadroomKnown:   candidate.status.Routing.PrimaryHeadroomKnown,
		SecondaryHeadroomKnown: candidate.status.Routing.SecondaryHeadroomKnown,
		Inflight:               candidate.status.Inflight,
		LocalLastUsed:          candidate.status.LocalLastUsed,
		ActiveSeatCount:        activeSeatCount,
		Basis:                  basis,
	}
}

func geminiOperationalTruthStatus(snapshot accountSnapshot) *GeminiOperationalTruthStatus {
	if snapshot.Type != AccountTypeGemini {
		return nil
	}
	state := strings.TrimSpace(snapshot.GeminiOperationalState)
	reason := sanitizeStatusMessage(snapshot.GeminiOperationalReason)
	source := strings.TrimSpace(snapshot.GeminiOperationalSource)
	if state == "" && reason == "" && source == "" && snapshot.GeminiOperationalCheckedAt.IsZero() && snapshot.GeminiOperationalLastSuccessAt.IsZero() {
		return nil
	}
	status := &GeminiOperationalTruthStatus{
		State:  state,
		Reason: reason,
		Source: source,
	}
	if !snapshot.GeminiOperationalCheckedAt.IsZero() {
		status.CheckedAt = snapshot.GeminiOperationalCheckedAt.UTC().Format(time.RFC3339)
	}
	if !snapshot.GeminiOperationalLastSuccessAt.IsZero() {
		status.LastSuccessAt = snapshot.GeminiOperationalLastSuccessAt.UTC().Format(time.RFC3339)
	}
	return status
}

func compactGeminiCooldownReason(snapshot accountSnapshot, now time.Time) string {
	resetTimes := normalizeGeminiModelRateLimitResetTimes(snapshot.GeminiModelRateLimitResetTimes, now)
	if len(resetTimes) == 0 {
		return ""
	}
	type cooldownRow struct {
		model string
		at    time.Time
	}
	rows := make([]cooldownRow, 0, len(resetTimes))
	for model, resetAt := range resetTimes {
		if model == "" || resetAt.IsZero() || !resetAt.After(now) {
			continue
		}
		rows = append(rows, cooldownRow{model: model, at: resetAt.UTC()})
	}
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].at.Equal(rows[j].at) {
			return rows[i].model < rows[j].model
		}
		return rows[i].at.Before(rows[j].at)
	})
	if len(rows) == 1 {
		return fmt.Sprintf("model cooldown active: %s until %s", rows[0].model, rows[0].at.Format(time.RFC3339))
	}
	return fmt.Sprintf("%d model cooldowns active; next reset %s for %s", len(rows), rows[0].at.Format(time.RFC3339), rows[0].model)
}

func compactGeminiEligibleDegradedReason(snapshot accountSnapshot, now time.Time) string {
	if strings.TrimSpace(snapshot.GeminiOperationalState) == geminiOperationalTruthStateCooldown {
		if reason := compactGeminiCooldownReason(snapshot, now); reason != "" {
			return reason
		}
	}
	if strings.TrimSpace(snapshot.GeminiProviderTruthState) == geminiProviderTruthStateMissingProjectID &&
		effectiveGeminiCodeAssistProjectIDForSnapshot(snapshot) != "" &&
		geminiHasOperationalProof(snapshot.GeminiOperationalState) {
		return "fallback project in use; provider truth missing project_id"
	}
	return ""
}

func geminiRoutingDisplay(snapshot accountSnapshot, routing routingState, now time.Time) (string, string) {
	if snapshot.Type != AccountTypeGemini {
		if routing.Eligible {
			return routingDisplayStateEnabled, ""
		}
		if routing.BlockReason == "rate_limited" {
			return routingDisplayStateCooldown, ""
		}
		return routingDisplayStateBlocked, ""
	}

	if routing.Eligible {
		switch strings.TrimSpace(snapshot.GeminiOperationalState) {
		case geminiOperationalTruthStateHardFail:
			return routingDisplayStateDegradedEnabled, sanitizeStatusMessage(firstNonEmpty(snapshot.GeminiOperationalReason, "last Gemini smoke failed"))
		case geminiOperationalTruthStateCooldown:
			return routingDisplayStateDegradedEnabled, sanitizeStatusMessage(firstNonEmpty(
				compactGeminiEligibleDegradedReason(snapshot, now),
				snapshot.GeminiOperationalReason,
				"last Gemini proof is still cooling down",
			))
		case geminiOperationalTruthStateDegradedOK:
			return routingDisplayStateDegradedEnabled, sanitizeStatusMessage(firstNonEmpty(
				compactGeminiEligibleDegradedReason(snapshot, now),
				snapshot.GeminiOperationalReason,
			))
		}
		switch strings.TrimSpace(snapshot.GeminiProviderTruthState) {
		case "", geminiProviderTruthStateReady:
			return routingDisplayStateEnabled, ""
		default:
			return routingDisplayStateDegradedEnabled, sanitizeStatusMessage(firstNonEmpty(
				compactGeminiEligibleDegradedReason(snapshot, now),
				snapshot.GeminiOperationalReason,
				snapshot.GeminiProviderTruthReason,
				geminiValidationReasonSummary(snapshot.GeminiValidationReasonCode, snapshot.GeminiValidationMessage, snapshot.GeminiValidationURL, snapshot.GeminiProviderTruthState),
			))
		}
	}

	switch strings.TrimSpace(routing.BlockReason) {
	case "rate_limited":
		detail := sanitizeStatusMessage(firstNonEmpty(
			snapshot.GeminiOperationalReason,
			"Gemini seat is cooling down after a recent rate limit",
		))
		return routingDisplayStateCooldown, detail
	case "validation_blocked":
		return routingDisplayStateQuarantined, sanitizeStatusMessage(firstNonEmpty(
			snapshot.GeminiProviderTruthReason,
			geminiValidationReasonSummary(snapshot.GeminiValidationReasonCode, snapshot.GeminiValidationMessage, snapshot.GeminiValidationURL, routing.BlockReason),
		))
	case "missing_project_id":
		return routingDisplayStateBlocked, sanitizeStatusMessage(firstNonEmpty(
			snapshot.GeminiProviderTruthReason,
			"provider truth missing project_id",
		))
	case "not_warmed":
		return routingDisplayStateBlocked, sanitizeStatusMessage(firstNonEmpty(
			snapshot.GeminiOperationalReason,
			snapshot.GeminiProviderTruthReason,
			"seat not warmed by successful Gemini proof",
		))
	case "stale_provider_truth":
		freshness := geminiProviderTruthFreshnessStatus(snapshot.GeminiProviderTruthState, snapshot.GeminiProviderCheckedAt, snapshot.GeminiQuotaUpdatedAt, now)
		return routingDisplayStateBlocked, sanitizeStatusMessage(firstNonEmpty(
			freshness.Reason,
			"provider truth is stale and must refresh before routing",
		))
	case "stale_quota_snapshot":
		freshness := geminiProviderTruthFreshnessStatus(snapshot.GeminiProviderTruthState, snapshot.GeminiProviderCheckedAt, snapshot.GeminiQuotaUpdatedAt, now)
		return routingDisplayStateBlocked, sanitizeStatusMessage(firstNonEmpty(
			freshness.Reason,
			"quota snapshot is stale and must refresh before routing",
		))
	case "quota_pressured":
		return routingDisplayStateBlocked, "Gemini quota headroom is below the routing threshold"
	case "operational_hard_fail":
		return routingDisplayStateBlocked, sanitizeStatusMessage(firstNonEmpty(
			snapshot.GeminiOperationalReason,
			"last Gemini proof failed",
		))
	default:
		return routingDisplayStateBlocked, ""
	}
}

func sameSeatStatus(a, b *CurrentSeatStatus) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.WorkspaceID == b.WorkspaceID && a.SeatKey == b.SeatKey
}

func firstSeatStatus(values ...*CurrentSeatStatus) *CurrentSeatStatus {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func buildPoolDashboardRouting(snapshot accountSnapshot, routing routingState, now time.Time) PoolDashboardRouting {
	state, degradedReason := geminiRoutingDisplay(snapshot, routing, now)
	row := PoolDashboardRouting{
		State:                  state,
		Eligible:               routing.Eligible,
		BlockReason:            routing.BlockReason,
		DegradedReason:         degradedReason,
		PrimaryUsedPct:         routing.PrimaryUsed * 100,
		SecondaryUsedPct:       routing.SecondaryUsed * 100,
		PrimaryHeadroomPct:     routing.PrimaryHeadroom * 100,
		SecondaryHeadroomPct:   routing.SecondaryHeadroom * 100,
		PrimaryHeadroomKnown:   routing.PrimaryHeadroomKnown,
		SecondaryHeadroomKnown: routing.SecondaryHeadroomKnown,
		CodexRateLimitBypass:   routing.CodexRateLimitBypass,
		PreemptiveThresholdPct: codexPreemptiveUsedThreshold * 100,
	}
	if !routing.RecoveryAt.IsZero() && routing.RecoveryAt.After(now) {
		row.RecoveryAt = routing.RecoveryAt.Format(time.RFC3339)
	}
	return row
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func uniqueSortedStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func workspaceKeyFor(provider, workspaceID string) string {
	return firstNonEmpty(provider, "unknown") + "|" + firstNonEmpty(workspaceID, "unknown")
}

func seatKeyFor(claims codexJWTClaims, workspaceID, fallback string) string {
	seatIdentity := firstNonEmpty(claims.ChatGPTUserID, claims.Email, claims.Subject, fallback)
	if workspaceID == "" {
		return seatIdentity
	}
	return seatIdentity + "|" + workspaceID
}

func (h *proxyHandler) buildPoolDashboardData(now time.Time) StatusData {
	data := StatusData{
		GeneratedAt: now,
		Uptime:      now.Sub(h.startTime),
	}
	data.CodexForcedPlan = strings.TrimSpace(h.cfg.forceCodexRequiredPlan)

	if h.poolUsers != nil {
		data.PoolUsers = len(h.poolUsers.List())
	}
	data.Quarantine = loadQuarantineStatus(h.cfg.poolDir, now)
	if profile := geminiOAuthDefaultProfile(); strings.TrimSpace(profile.ID) != "" && strings.TrimSpace(profile.Secret) != "" {
		data.GeminiOperator.ManagedOAuthAvailable = true
		data.GeminiOperator.ManagedOAuthProfile = firstNonEmpty(geminiOAuthProfileIDForLabel(profile.Label), strings.TrimSpace(profile.Label))
	}

	providerSummary := make(map[string]PoolDashboardProviderSum)
	workspaceGroups := make(map[string]*poolWorkspaceAccumulator)
	candidateByID := make(map[string]currentSeatCandidate)
	earliestRecovery := time.Time{}
	geminiPool := GeminiPoolStatus{}
	var activeSeat *currentSeatCandidate
	var lastUsedSeat *currentSeatCandidate
	var nextOpenAIAPIKey *currentSeatCandidate
	var nextGitLabClaudeToken *currentSeatCandidate
	activeSeatCount := 0

	h.pool.mu.RLock()
	accounts := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()

	data.TotalCount = len(accounts)
	for _, a := range accounts {
		snapshot := snapshotAccountState(a, now, "", "")

		switch snapshot.Type {
		case AccountTypeCodex:
			data.CodexCount++
			if snapshot.FallbackOnly {
				data.OpenAIAPIPool.TotalKeys++
			} else {
				data.CodexSeatCount++
			}
		case AccountTypeGemini:
			data.GeminiCount++
		case AccountTypeClaude:
			data.ClaudeCount++
		case AccountTypeKimi:
			data.KimiCount++
		case AccountTypeMinimax:
			data.MinimaxCount++
		}

		routing := snapshot.Routing
		routingRow := buildPoolDashboardRouting(snapshot, routing, now)
		primaryUsed := routing.PrimaryUsed
		secondaryUsed := routing.SecondaryUsed
		effectivePrimary := primaryUsed
		effectiveSecondary := secondaryUsed
		claims, workspaceID, seatKey := poolIdentityForSnapshot(snapshot)

		operatorSource := normalizeGeminiOperatorSource(snapshot.OperatorSource, snapshot.OAuthProfileID, snapshot.Type)
		status := AccountStatus{
			ID:                 snapshot.ID,
			Type:               string(snapshot.Type),
			PlanType:           snapshot.PlanType,
			AuthMode:           snapshot.AuthMode,
			AccountID:          firstNonEmpty(snapshot.AccountID, snapshot.IDTokenChatGPTAccountID),
			Email:              firstNonEmpty(claims.Email, snapshot.OperatorEmail, snapshot.AntigravityEmail),
			Subject:            claims.Subject,
			ChatGPTUserID:      claims.ChatGPTUserID,
			WorkspaceID:        workspaceID,
			SeatKey:            seatKey,
			FallbackOnly:       snapshot.FallbackOnly,
			OperatorSource:     geminiOperatorSourceLabel(operatorSource),
			Disabled:           snapshot.Disabled,
			Dead:               snapshot.Dead,
			PrimaryUsed:        primaryUsed * 100,
			SecondaryUsed:      secondaryUsed * 100,
			EffectivePrimary:   effectivePrimary * 100,
			EffectiveSecondary: effectiveSecondary * 100,
			Routing:            routingRow,
			Score:              snapshot.Score,
			Inflight:           snapshot.Inflight,
			LocalTokens:        snapshot.Totals.TotalBillableTokens,
		}
		if routingRow.RecoveryAt != "" {
			status.RecoveryAt = routingRow.RecoveryAt
		}
		if !snapshot.Usage.PrimaryResetAt.IsZero() && snapshot.Usage.PrimaryResetAt.After(now) {
			status.PrimaryResetIn = formatDuration(snapshot.Usage.PrimaryResetAt.Sub(now))
		} else if snapshot.Usage.PrimaryWindowMinutes > 0 {
			status.PrimaryResetIn = fmt.Sprintf("~%dm", snapshot.Usage.PrimaryWindowMinutes)
		}
		if !snapshot.Usage.SecondaryResetAt.IsZero() && snapshot.Usage.SecondaryResetAt.After(now) {
			status.SecondaryResetIn = formatDuration(snapshot.Usage.SecondaryResetAt.Sub(now))
		} else if snapshot.Usage.SecondaryWindowMinutes > 0 {
			status.SecondaryResetIn = fmt.Sprintf("~%dd", snapshot.Usage.SecondaryWindowMinutes/60/24)
		}
		if !snapshot.LastRefresh.IsZero() {
			status.LastRefreshAt = snapshot.LastRefresh.UTC().Format(time.RFC3339)
		}
		if !snapshot.HealthCheckedAt.IsZero() {
			status.HealthCheckedAt = snapshot.HealthCheckedAt.UTC().Format(time.RFC3339)
		}
		if !snapshot.LastHealthyAt.IsZero() {
			status.LastHealthyAt = snapshot.LastHealthyAt.UTC().Format(time.RFC3339)
		}
		if snapshot.Type == AccountTypeGemini {
			geminiPool.TotalSeats++
			if routing.Eligible {
				geminiPool.EligibleSeats++
				if status.Routing.State == routingDisplayStateDegradedEnabled {
					geminiPool.DegradedEligibleSeats++
				} else {
					geminiPool.CleanEligibleSeats++
				}
			}
			if snapshot.GeminiProviderTruthReady {
				geminiPool.ReadySeats++
			}
			operationalState := strings.TrimSpace(snapshot.GeminiOperationalState)
			if geminiHasOperationalProof(operationalState) {
				geminiPool.WarmSeats++
			}
			cooldownCounted := false
			if operationalState == geminiOperationalTruthStateCooldown {
				geminiPool.CooldownSeats++
				cooldownCounted = true
			}
			if snapshot.AntigravityValidationBlocked {
				geminiPool.ValidationFlaggedSeats++
			}
			switch strings.TrimSpace(snapshot.GeminiProviderTruthState) {
			case geminiProviderTruthStateRestricted:
				geminiPool.RestrictedSeats++
			case geminiProviderTruthStateMissingProjectID:
				geminiPool.MissingProjectSeats++
			}
			switch strings.TrimSpace(routing.BlockReason) {
			case "rate_limited":
				if !cooldownCounted {
					geminiPool.CooldownSeats++
				}
			case "not_warmed":
				geminiPool.NotWarmedSeats++
			case "stale_provider_truth":
				geminiPool.StaleTruthSeats++
			case "stale_quota_snapshot":
				geminiPool.StaleTruthSeats++
				geminiPool.StaleQuotaSeats++
			}
			status.ProviderSubscriptionTier = strings.TrimSpace(snapshot.GeminiSubscriptionTierID)
			status.ProviderSubscriptionName = strings.TrimSpace(snapshot.GeminiSubscriptionTierName)
			status.ProviderValidationCode = strings.TrimSpace(snapshot.GeminiValidationReasonCode)
			status.ProviderValidationMessage = sanitizeStatusMessage(snapshot.GeminiValidationMessage)
			status.ProviderValidationURL = strings.TrimSpace(snapshot.GeminiValidationURL)
			if !snapshot.GeminiProviderCheckedAt.IsZero() {
				status.ProviderCheckedAt = snapshot.GeminiProviderCheckedAt.UTC().Format(time.RFC3339)
			}
			protectedModels := normalizeStringSlice(snapshot.GeminiProtectedModels)
			protectedSet := make(map[string]struct{}, len(protectedModels))
			for _, modelID := range protectedModels {
				protectedSet[modelID] = struct{}{}
			}
			geminiPool.ProtectedModelCount += len(protectedModels)
			quotaModels := make([]GeminiModelQuotaStatus, 0, len(snapshot.GeminiQuotaModels))
			for _, model := range snapshot.GeminiQuotaModels {
				routeProvider := firstNonEmpty(strings.TrimSpace(model.RouteProvider), geminiQuotaModelRouteProvider(model.Name))
				runtimeSupport := geminiQuotaModelRuntimeSupportForSnapshot(snapshot, routeProvider)
				quotaModel := GeminiModelQuotaStatus{
					Name:                strings.TrimSpace(model.Name),
					RouteProvider:       routeProvider,
					Routable:            runtimeSupport.Routable,
					CompatibilityLane:   runtimeSupport.CompatibilityLane,
					CompatibilityReason: runtimeSupport.CompatibilityReason,
					Percentage:          model.Percentage,
					ResetTime:           strings.TrimSpace(model.ResetTime),
					DisplayName:         strings.TrimSpace(model.DisplayName),
					SupportsImages:      model.SupportsImages,
					SupportsThinking:    model.SupportsThinking,
					ThinkingBudget:      model.ThinkingBudget,
					Recommended:         model.Recommended,
					MaxTokens:           model.MaxTokens,
					MaxOutputTokens:     model.MaxOutputTokens,
					SupportedMimeTypes:  cloneSupportedMimeTypes(model.SupportedMimeTypes),
				}
				_, quotaModel.Protected = protectedSet[quotaModel.Name]
				quotaModels = append(quotaModels, quotaModel)
			}
			var quotaStatus *GeminiProviderQuotaStatus
			if !snapshot.GeminiQuotaUpdatedAt.IsZero() || len(snapshot.GeminiModelForwardingRules) > 0 || len(quotaModels) > 0 {
				quotaStatus = &GeminiProviderQuotaStatus{
					ModelForwardingRules: cloneStringMap(snapshot.GeminiModelForwardingRules),
					Models:               quotaModels,
				}
				if !snapshot.GeminiQuotaUpdatedAt.IsZero() {
					quotaStatus.UpdatedAt = snapshot.GeminiQuotaUpdatedAt.UTC().Format(time.RFC3339)
				}
			}
			if quotaStatus != nil {
				geminiPool.QuotaTrackedSeats++
				geminiPool.QuotaModelCount += len(quotaModels)
				if len(quotaModels) == 0 {
					geminiPool.QuotaEmptySeats++
				}
			}
			providerTruth := &GeminiProviderTruthStatus{
				Ready:                snapshot.GeminiProviderTruthReady,
				State:                strings.TrimSpace(snapshot.GeminiProviderTruthState),
				Reason:               sanitizeStatusMessage(snapshot.GeminiProviderTruthReason),
				ProjectID:            strings.TrimSpace(snapshot.AntigravityProjectID),
				SubscriptionTierID:   strings.TrimSpace(snapshot.GeminiSubscriptionTierID),
				SubscriptionTierName: strings.TrimSpace(snapshot.GeminiSubscriptionTierName),
				ValidationReasonCode: strings.TrimSpace(snapshot.GeminiValidationReasonCode),
				ValidationMessage:    sanitizeStatusMessage(snapshot.GeminiValidationMessage),
				ValidationURL:        strings.TrimSpace(snapshot.GeminiValidationURL),
				ProxyDisabled:        snapshot.AntigravityProxyDisabled,
				Restricted:           strings.TrimSpace(snapshot.GeminiProviderTruthState) == geminiProviderTruthStateRestricted,
				ValidationBlocked:    snapshot.AntigravityValidationBlocked,
				QuotaForbidden:       snapshot.AntigravityQuotaForbidden,
				QuotaForbiddenReason: sanitizeStatusMessage(snapshot.AntigravityQuotaForbiddenReason),
				ProtectedModels:      protectedModels,
				RateLimitResetTimes:  formatGeminiModelRateLimitResetTimes(snapshot.GeminiModelRateLimitResetTimes, now),
				Quota:                quotaStatus,
			}
			if !snapshot.GeminiProviderCheckedAt.IsZero() {
				providerTruth.CheckedAt = snapshot.GeminiProviderCheckedAt.UTC().Format(time.RFC3339)
			}
			freshness := geminiProviderTruthFreshnessStatus(
				snapshot.GeminiProviderTruthState,
				snapshot.GeminiProviderCheckedAt,
				snapshot.GeminiQuotaUpdatedAt,
				now,
			)
			if freshness.State != "" {
				providerTruth.FreshnessState = freshness.State
				providerTruth.Stale = freshness.Stale
				providerTruth.StaleReason = sanitizeStatusMessage(freshness.Reason)
				if !freshness.FreshUntil.IsZero() {
					providerTruth.FreshUntil = freshness.FreshUntil.UTC().Format(time.RFC3339)
				}
			}
			status.ProviderTruth = providerTruth
			status.OperationalTruth = geminiOperationalTruthStatus(snapshot)
			status.ProviderQuotaSummary = summarizeGeminiQuotaStatus(snapshot.GeminiQuotaUpdatedAt, quotaModels)
		}
		if !snapshot.DeadSince.IsZero() {
			status.DeadSince = snapshot.DeadSince.UTC().Format(time.RFC3339)
		}
		status.HealthStatus = displayAccountHealthStatus(snapshot, routing)
		status.HealthError = sanitizeStatusMessage(snapshot.HealthError)
		status.Penalty = snapshot.Penalty
		if snapshot.FallbackOnly {
			status.ProbeState = managedOpenAIAPIProbeState(snapshot, now)
			status.ProbeSummary = managedOpenAIAPIProbeSummary(snapshot, now)
		}
		authExpiresAt := snapshot.ExpiresAt
		if authExpiresAt.IsZero() && !claims.ExpiresAt.IsZero() {
			authExpiresAt = claims.ExpiresAt
		}
		if !authExpiresAt.IsZero() {
			status.AuthExpiresAt = authExpiresAt.UTC().Format(time.RFC3339)
			if authExpiresAt.Before(now) {
				status.AuthExpiresIn = "EXPIRED"
			} else {
				status.AuthExpiresIn = formatDuration(authExpiresAt.Sub(now))
			}
		}
		if !snapshot.LastUsed.IsZero() {
			status.LocalLastUsed = formatDuration(now.Sub(snapshot.LastUsed)) + " ago"
		} else {
			status.LocalLastUsed = "never"
		}
		if !snapshot.Usage.RetrievedAt.IsZero() {
			status.UsageObserved = firstNonEmpty(snapshot.Usage.Source, "usage") + " · " + formatDuration(now.Sub(snapshot.Usage.RetrievedAt)) + " ago"
		} else if strings.TrimSpace(snapshot.Usage.Source) != "" {
			status.UsageObserved = strings.TrimSpace(snapshot.Usage.Source)
		}
		if snapshot.GitLabClaude {
			status.GitLabRateLimitName = strings.TrimSpace(snapshot.GitLabRateLimitName)
			status.GitLabRateLimitLimit = snapshot.GitLabRateLimitLimit
			status.GitLabRateLimitRemaining = snapshot.GitLabRateLimitRemaining
			status.GitLabQuotaExceededCount = snapshot.GitLabQuotaExceededCount
			if !snapshot.GitLabRateLimitResetAt.IsZero() {
				status.GitLabRateLimitResetAt = snapshot.GitLabRateLimitResetAt.UTC().Format(time.RFC3339)
				if snapshot.GitLabRateLimitResetAt.After(now) {
					status.GitLabRateLimitResetIn = formatDuration(snapshot.GitLabRateLimitResetAt.Sub(now))
				}
			}
			if snapshot.GitLabQuotaExceededCount > 0 && !snapshot.RateLimitUntil.IsZero() && snapshot.RateLimitUntil.After(now) {
				status.GitLabQuotaProbeIn = formatDuration(snapshot.RateLimitUntil.Sub(now))
			}
			if status.UsageObserved == "" {
				status.UsageObserved = "local totals only · GitLab quota hidden"
			}
		}
		if snapshot.Type == AccountTypeGemini {
			switch operatorSource {
			case geminiOperatorSourceManagedOAuth:
				data.GeminiOperator.ManagedSeatCount++
			case geminiOperatorSourceAntigravityImport:
				data.GeminiOperator.ImportedSeatCount++
				data.GeminiOperator.AntigravitySeatCount++
			case geminiOperatorSourceManualImport, geminiOperatorSourceManualImportLegacy:
				data.GeminiOperator.ImportedSeatCount++
				data.GeminiOperator.LegacySeatCount++
			}
		}

		providerKey := status.Type
		prov := providerSummary[providerKey]
		prov.TotalAccounts++
		if routing.Eligible {
			prov.EligibleAccounts++
		}
		providerSummary[providerKey] = prov

		if !status.FallbackOnly {
			groupKey := workspaceKeyFor(status.Type, workspaceID)
			group := workspaceGroups[groupKey]
			if group == nil {
				group = &poolWorkspaceAccumulator{
					WorkspaceID: workspaceID,
					Provider:    status.Type,
					SeatKeys:    make(map[string]struct{}),
					AccountIDs:  make(map[string]struct{}),
					Emails:      make(map[string]struct{}),
				}
				workspaceGroups[groupKey] = group
			}
			group.SeatCount++
			group.SeatKeys[seatKey] = struct{}{}
			group.AccountIDs[status.ID] = struct{}{}
			if status.Email != "" {
				group.Emails[status.Email] = struct{}{}
			}
			if routing.Eligible {
				group.EligibleCount++
			} else {
				group.BlockedCount++
				if routing.RecoveryAt.After(now) && (group.NextRecoveryAt.IsZero() || routing.RecoveryAt.Before(group.NextRecoveryAt)) {
					group.NextRecoveryAt = routing.RecoveryAt
				}
			}
		}

		if routing.RecoveryAt.After(now) && (earliestRecovery.IsZero() || routing.RecoveryAt.Before(earliestRecovery)) {
			earliestRecovery = routing.RecoveryAt
		}

		if status.Inflight > 0 {
			activeSeatCount++
		}
		candidate := currentSeatCandidate{status: status, lastUsed: snapshot.LastUsed}
		candidateByID[status.ID] = candidate
		if status.Inflight > 0 && prefersLiveSeat(candidate, activeSeat) {
			candidateCopy := candidate
			activeSeat = &candidateCopy
		}
		if prefersLastUsedSeat(candidate, lastUsedSeat) {
			candidateCopy := candidate
			lastUsedSeat = &candidateCopy
		}
		if status.FallbackOnly {
			if status.Dead {
				data.OpenAIAPIPool.DeadKeys++
			}
			if status.HealthStatus == "healthy" && !status.Dead && !status.Disabled {
				data.OpenAIAPIPool.HealthyKeys++
			}
			if routing.Eligible {
				data.OpenAIAPIPool.EligibleKeys++
				if status.ProbeState != "healthy" {
					data.OpenAIAPIPool.EligibleUnhealthyKeys++
				}
				if nextOpenAIAPIKey == nil || prefersBestEligibleSeat(candidate, nextOpenAIAPIKey) {
					candidateCopy := candidate
					nextOpenAIAPIKey = &candidateCopy
				}
			}
		}
		if snapshot.GitLabClaude {
			data.GitLabClaudePool.TotalTokens++
			if status.Dead {
				data.GitLabClaudePool.DeadTokens++
			}
			if status.HealthStatus == "healthy" && !status.Dead && !status.Disabled {
				data.GitLabClaudePool.HealthyTokens++
			}
			if routing.Eligible {
				data.GitLabClaudePool.EligibleTokens++
				if nextGitLabClaudeToken == nil || prefersBestEligibleSeat(candidate, nextGitLabClaudeToken) {
					candidateCopy := candidate
					nextGitLabClaudeToken = &candidateCopy
				}
			}
		}

		data.Accounts = append(data.Accounts, status)
	}

	data.PoolUtilization = h.pool.getPoolUtilization()
	for _, utilization := range data.PoolUtilization {
		prov := providerSummary[utilization.Provider]
		prov.TimeWeightedPrimaryPct = utilization.TimeWeightedPrimaryPct
		prov.TimeWeightedSecondaryPct = utilization.TimeWeightedSecondaryPct
		providerSummary[utilization.Provider] = prov
	}

	data.PoolSummary = PoolDashboardSummary{
		TotalAccounts:    len(data.Accounts),
		WorkspaceCount:   len(workspaceGroups),
		EligibleAccounts: 0,
		Providers:        providerSummary,
	}
	if !earliestRecovery.IsZero() {
		data.PoolSummary.NextRecoveryAt = earliestRecovery.Format(time.RFC3339)
	}
	data.ActiveSeat = currentSeatStatusFromCandidate(activeSeat, "Live requests are currently using this seat.", activeSeatCount)
	data.LastUsedSeat = currentSeatStatusFromCandidate(lastUsedSeat, "This is the most recently used local seat.", activeSeatCount)
	if sameSeatStatus(data.ActiveSeat, data.LastUsedSeat) {
		data.LastUsedSeat = nil
	}
	nextAccount := h.pool.peekCandidateAt(now, AccountTypeCodex, "")
	if nextAccount == nil {
		nextAccount = h.pool.peekCandidateAt(now, "", "")
	}
	if nextAccount != nil {
		if nextCandidate, ok := candidateByID[nextAccount.ID]; ok {
			nextBasis := "If a new unpinned request starts now, the pool will choose this seat."
			if data.ActiveSeat != nil && sameSeatStatus(data.ActiveSeat, currentSeatStatusFromCandidate(&nextCandidate, nextBasis, activeSeatCount)) {
				nextBasis = "New unpinned requests would also land on this seat right now."
			}
			data.BestEligibleSeat = currentSeatStatusFromCandidate(&nextCandidate, nextBasis, activeSeatCount)
		}
	}
	if sameSeatStatus(data.ActiveSeat, data.BestEligibleSeat) || sameSeatStatus(data.LastUsedSeat, data.BestEligibleSeat) {
		data.BestEligibleSeat = nil
	}
	data.CurrentSeat = firstSeatStatus(data.ActiveSeat, data.BestEligibleSeat, data.LastUsedSeat)
	if nextOpenAIAPIKey != nil {
		data.OpenAIAPIPool.NextKeyID = nextOpenAIAPIKey.status.ID
	}
	data.OpenAIAPIPool.StatusNote = managedOpenAIAPIPoolStatusNote(data.OpenAIAPIPool)
	if nextGitLabClaudeToken != nil {
		data.GitLabClaudePool.NextTokenID = nextGitLabClaudeToken.status.ID
	}
	data.GeminiOperator.Note = geminiOperatorStatusNote(data.GeminiOperator)
	if geminiPool.TotalSeats > 0 {
		geminiPool.Note = geminiPoolStatusNote(geminiPool)
		data.GeminiPool = &geminiPool
	}

	for _, account := range data.Accounts {
		if account.Routing.Eligible {
			data.PoolSummary.EligibleAccounts++
		}
	}

	for _, group := range workspaceGroups {
		entry := PoolDashboardWorkspaceGroup{
			WorkspaceID:       group.WorkspaceID,
			Provider:          group.Provider,
			SeatCount:         group.SeatCount,
			EligibleSeatCount: group.EligibleCount,
			BlockedSeatCount:  group.BlockedCount,
			SeatKeys:          uniqueSortedStrings(mapKeys(group.SeatKeys)),
			AccountIDs:        uniqueSortedStrings(mapKeys(group.AccountIDs)),
			Emails:            uniqueSortedStrings(mapKeys(group.Emails)),
		}
		if !group.NextRecoveryAt.IsZero() {
			entry.NextRecoveryAt = group.NextRecoveryAt.Format(time.RFC3339)
		}
		data.WorkspaceGroups = append(data.WorkspaceGroups, entry)
	}

	sort.Slice(data.WorkspaceGroups, func(i, j int) bool {
		if data.WorkspaceGroups[i].Provider == data.WorkspaceGroups[j].Provider {
			return data.WorkspaceGroups[i].WorkspaceID < data.WorkspaceGroups[j].WorkspaceID
		}
		return data.WorkspaceGroups[i].Provider < data.WorkspaceGroups[j].Provider
	})

	sort.Slice(data.Accounts, func(i, j int) bool {
		left := data.Accounts[i]
		right := data.Accounts[j]
		if left.Routing.Eligible != right.Routing.Eligible {
			return !left.Routing.Eligible
		}
		if left.RecoveryAt != "" && right.RecoveryAt != "" && left.RecoveryAt != right.RecoveryAt {
			return left.RecoveryAt < right.RecoveryAt
		}
		leftMaxUsed := left.SecondaryUsed
		if left.PrimaryUsed > leftMaxUsed {
			leftMaxUsed = left.PrimaryUsed
		}
		rightMaxUsed := right.SecondaryUsed
		if right.PrimaryUsed > rightMaxUsed {
			rightMaxUsed = right.PrimaryUsed
		}
		if leftMaxUsed != rightMaxUsed {
			return leftMaxUsed > rightMaxUsed
		}
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		return left.ID < right.ID
	})

	if h.store != nil {
		data.TokenAnalytics = h.loadTokenAnalytics()
	}

	return data
}

func mapKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

func (h *proxyHandler) servePoolDashboard(w http.ResponseWriter, r *http.Request) {
	data := h.buildPoolDashboardData(time.Now())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (h *proxyHandler) serveStatusPage(w http.ResponseWriter, r *http.Request) {
	data := h.buildPoolDashboardData(time.Now())
	data.LocalOperatorEnabled = h.cfg.friendCode == "" &&
		!hasForwardingHeaders(r) &&
		isLoopbackHost(r.Host) &&
		isLoopbackRemoteAddr(r.RemoteAddr)

	// Allow both explicit JSON clients and a direct human-openable JSON URL.
	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl := template.Must(template.New("status").Funcs(template.FuncMap{
		"pct": func(v float64) string {
			return fmt.Sprintf("%.0f%%", v)
		},
		"headroomPct": func(v float64, known bool) string {
			if !known {
				return "n/a"
			}
			return fmt.Sprintf("%.0f%%", v)
		},
		"clip": func(v string, max int) string {
			return clipMiddle(v, max)
		},
		"clipOpaque": func(v string) string {
			return clipOpaque(v)
		},
		"join": func(items []string, sep string) string {
			return strings.Join(items, sep)
		},
		"sanitize": func(v string) string {
			return sanitizeStatusMessage(v)
		},
		"score": func(v float64) string {
			return fmt.Sprintf("%.2f", v)
		},
		"quotaStateLabel": func(routable bool, lane, routeProvider string) string {
			routeProvider = strings.TrimSpace(routeProvider)
			if routable {
				return "routable"
			}
			if strings.TrimSpace(lane) == geminiQuotaCompatibilityLaneAnthropicAdapterRequired {
				return "catalog-only"
			}
			if routeProvider != "" && routeProvider != "gemini" {
				return "unsupported-provider"
			}
			return "seat-blocked"
		},
		"quotaStateTagClass": func(routable bool, lane, routeProvider string) string {
			routeProvider = strings.TrimSpace(routeProvider)
			if routable {
				return "tag tag-state-routable"
			}
			if strings.TrimSpace(lane) == geminiQuotaCompatibilityLaneAnthropicAdapterRequired {
				return "tag tag-state-catalog"
			}
			if routeProvider != "" && routeProvider != "gemini" {
				return "tag tag-state-unknown"
			}
			return "tag tag-state-seat-blocked"
		},
		"bar": func(v float64) template.HTML {
			width := v
			if width > 100 {
				width = 100
			}
			color := "#4a4"
			if v > 80 {
				color = "#a44"
			} else if v > 50 {
				color = "#aa4"
			}
			return template.HTML(fmt.Sprintf(
				`<div class="bar"><div class="fill" style="width:%.0f%%;background:%s"></div></div>`,
				width, color,
			))
		},
		"remainingBarKnown": func(v float64, known bool) template.HTML {
			if !known {
				return template.HTML(`<div class="bar"><div class="fill" style="width:100%;background:#30363d"></div></div>`)
			}
			width := v
			if width > 100 {
				width = 100
			}
			color := "#3fb950"
			if v < 10 {
				color = "#f85149"
			} else if v < 25 {
				color = "#d29922"
			}
			return template.HTML(fmt.Sprintf(
				`<div class="bar"><div class="fill" style="width:%.0f%%;background:%s"></div></div>`,
				width, color,
			))
		},
	}).Parse(statusHTML))
	tmpl.Execute(w, data)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

func (h *proxyHandler) loadTokenAnalytics() *TokenAnalytics {
	caps, err := h.store.loadAllPlanCapacity()
	if err != nil || len(caps) == 0 {
		return nil
	}

	analytics := &TokenAnalytics{
		ModelInfo: "effective = input + (cached × 0.1) + (output × mult) + (reasoning × mult)",
	}

	for planType, cap := range caps {
		analytics.TotalSamples += cap.SampleCount

		confidence := "low"
		if cap.SampleCount >= 20 {
			confidence = "high"
		} else if cap.SampleCount >= 5 {
			confidence = "medium"
		}

		mult := cap.OutputMultiplier
		if mult == 0 {
			mult = 4.0
		}

		view := PlanCapacityView{
			PlanType:                 planType,
			SampleCount:              cap.SampleCount,
			Confidence:               confidence,
			TotalInputTokens:         cap.TotalInputTokens,
			TotalOutputTokens:        cap.TotalOutputTokens,
			TotalCachedTokens:        cap.TotalCachedTokens,
			TotalReasoningTokens:     cap.TotalReasoningTokens,
			TotalBillableTokens:      cap.TotalTokens,
			OutputMultiplier:         mult,
			EffectivePerPrimaryPct:   int64(cap.EffectivePerPrimaryPct),
			EffectivePerSecondaryPct: int64(cap.EffectivePerSecondaryPct),
		}

		// Format capacity estimates
		if cap.EffectivePerPrimaryPct > 0 {
			total := int64(cap.EffectivePerPrimaryPct * 100)
			view.EstimatedPrimaryCapacity = formatTokenCount(total)
		}
		if cap.EffectivePerSecondaryPct > 0 {
			total := int64(cap.EffectivePerSecondaryPct * 100)
			view.EstimatedSecondaryCapacity = formatTokenCount(total)
		}

		analytics.PlanCapacities = append(analytics.PlanCapacities, view)
	}

	// Sort by plan type
	sort.Slice(analytics.PlanCapacities, func(i, j int) bool {
		order := map[string]int{"team": 0, "pro": 1, "plus": 2, "gemini": 3}
		return order[analytics.PlanCapacities[i].PlanType] < order[analytics.PlanCapacities[j].PlanType]
	})

	return analytics
}

func formatTokenCount(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("~%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("~%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("~%.0fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

const statusHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Pool Diagnostics</title>
    <meta http-equiv="refresh" content="30">
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, monospace;
            margin: 0;
            padding: 20px;
            background: #0d1117;
            color: #c9d1d9;
        }
        h1 { color: #58a6ff; margin-bottom: 5px; }
        .meta { color: #8b949e; margin-bottom: 20px; font-size: 14px; }
        .toolbar {
            display: flex;
            flex-wrap: wrap;
            gap: 16px;
            margin-bottom: 20px;
        }
        .operator-card {
            flex: 1 1 420px;
            background: #161b22;
            padding: 16px;
            border-radius: 8px;
            border: 1px solid #30363d;
            min-width: min(100%, 420px);
            box-shadow: 0 10px 30px rgba(0,0,0,0.18);
        }
        .seat-card {
            flex-basis: 340px;
            min-width: min(100%, 340px);
        }
        .operator-title { color: #58a6ff; font-weight: 600; margin-bottom: 6px; }
        .muted { color: #8b949e; font-size: 13px; line-height: 1.5; }
        .action-btn {
            margin-top: 14px;
            border: 1px solid #2f81f7;
            background: linear-gradient(180deg, rgba(47, 129, 247, 0.22), rgba(31, 111, 235, 0.14));
            color: #dbeafe;
            border-radius: 6px;
            padding: 10px 14px;
            font-weight: 600;
            cursor: pointer;
            transition: transform 0.16s ease, box-shadow 0.16s ease, border-color 0.16s ease;
        }
        .action-btn:hover {
            transform: translateY(-1px);
            box-shadow: 0 10px 22px rgba(31, 111, 235, 0.18);
            border-color: #58a6ff;
        }
        .danger-btn {
            border-color: #f85149;
            background: linear-gradient(180deg, rgba(248, 81, 73, 0.22), rgba(218, 54, 51, 0.14));
            color: #ffe2e0;
        }
        .danger-btn:hover {
            box-shadow: 0 10px 22px rgba(248, 81, 73, 0.16);
            border-color: #ff7b72;
        }
        .action-btn:disabled {
            cursor: wait;
            opacity: 0.7;
            transform: none;
            box-shadow: none;
        }
        .row-action-btn {
            margin-top: 0;
            padding: 6px 10px;
            font-size: 12px;
            white-space: nowrap;
        }
        .action-row {
            display: flex;
            gap: 10px;
            margin-top: 14px;
            flex-wrap: wrap;
        }
        .action-input {
            flex: 1 1 260px;
            min-width: 220px;
            border: 1px solid #30363d;
            background: #0d1117;
            color: #c9d1d9;
            border-radius: 6px;
            padding: 10px 12px;
        }
        .action-input:focus {
            outline: none;
            border-color: #58a6ff;
            box-shadow: 0 0 0 3px rgba(88, 166, 255, 0.18);
        }
        .action-textarea {
            min-height: 140px;
            resize: vertical;
            width: 100%;
            font-family: ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, Liberation Mono, monospace;
        }
        .result-block {
            margin-top: 14px;
            background: #0d1117;
            border: 1px solid #21262d;
            border-radius: 6px;
            padding: 12px;
        }
        .stats {
            display: flex;
            gap: 20px;
            margin-bottom: 20px;
            flex-wrap: wrap;
        }
        .stat {
            background: #161b22;
            padding: 15px 20px;
            border-radius: 6px;
            border: 1px solid #30363d;
        }
        .table-wrap {
            overflow-x: auto;
            margin-bottom: 20px;
        }
        .stat-value { font-size: 28px; font-weight: bold; color: #58a6ff; }
        .stat-label { font-size: 12px; color: #8b949e; text-transform: uppercase; }
        table {
            width: 100%;
            border-collapse: collapse;
            background: #161b22;
            border-radius: 6px;
            overflow: hidden;
        }
        th, td {
            padding: 10px 12px;
            text-align: left;
            border-bottom: 1px solid #21262d;
        }
        th {
            background: #21262d;
            color: #8b949e;
            font-weight: 500;
            font-size: 12px;
            text-transform: uppercase;
        }
        tr:hover { background: #1c2128; }
        .bar {
            width: 80px;
            height: 8px;
            background: #21262d;
            border-radius: 4px;
            overflow: hidden;
            display: inline-block;
            vertical-align: middle;
            margin-right: 8px;
        }
        .fill { height: 100%; }
        .status-ok { color: #3fb950; }
        .status-warn { color: #d29922; }
        .status-dead { color: #f85149; }
        .tag {
            display: inline-block;
            padding: 2px 6px;
            border-radius: 3px;
            font-size: 11px;
            font-weight: 500;
        }
        .tag-pro { background: #238636; color: #fff; }
        .tag-plus { background: #1f6feb; color: #fff; }
        .tag-team { background: #8957e5; color: #fff; }
        .tag-gemini { background: #ea4335; color: #fff; }
        .tag-claude { background: #cc785c; color: #fff; }
        .tag-codex { background: #10a37f; color: #fff; }
        .tag-api { background: #1f6feb; color: #fff; }
        .tag-disabled { background: #6e7681; color: #fff; }
        .tag-dead { background: #f85149; color: #fff; }
        .tag-state-routable { background: #238636; color: #fff; }
        .tag-state-seat-blocked { background: #d29922; color: #111; }
        .tag-state-catalog { background: #6e7681; color: #fff; }
        .tag-state-unknown { background: #30363d; color: #fff; }
        .tag-capability { background: #1f6feb; color: #fff; }
        .tag-capability-thinking { background: #8957e5; color: #fff; }
        .usage-cell { white-space: nowrap; }
        .detail-line {
            display: block;
            max-width: 340px;
            white-space: normal;
            overflow-wrap: anywhere;
        }
        .quota-model-list {
            margin-top: 8px;
            display: grid;
            gap: 8px;
        }
        .quota-model-row {
            background: #0d1117;
            border: 1px solid #21262d;
            border-radius: 6px;
            padding: 8px 10px;
        }
        .quota-model-head {
            display: flex;
            justify-content: space-between;
            align-items: baseline;
            gap: 10px;
        }
        .quota-model-name {
            font-weight: 600;
            color: #dbeafe;
        }
        .quota-model-meter {
            color: #58a6ff;
            font-weight: 600;
            white-space: nowrap;
        }
        .quota-model-tags {
            margin-top: 6px;
            display: flex;
            flex-wrap: wrap;
            gap: 6px;
        }
        .effective { color: #8b949e; font-size: 11px; }
        .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
        a { color: #58a6ff; text-decoration: none; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <h1>🧪 Pool Diagnostics</h1>
    <div class="meta">
        Generated: {{.GeneratedAt.Format "2006-01-02 15:04:05"}} · Uptime: {{.Uptime.Round 1000000000}}
    </div>
    <div class="operator-card" style="margin-bottom: 20px;">
        <div class="operator-title">Diagnostics Surface</div>
        <div class="muted">
            This page is intentionally read-only and diagnostic-heavy. Use the tabbed landing page at <a href="/">/</a> for provider onboarding, operator actions, and day-to-day pool control.
        </div>
        <div class="result-block">
            <a href="/">Return to Dashboard</a> ·
            <a href="/status?format=json">Status JSON</a> ·
            <a href="/healthz">Health check</a>
        </div>
    </div>
    {{if gt .Quarantine.Total 0}}
    <div class="operator-card" style="margin-bottom: 20px;">
        <div class="operator-title">Quarantine</div>
        <div class="muted">
            Accounts that stay dead for more than 72 hours are moved out of the active pool automatically so they stop inflating routing totals and recovery expectations.
        </div>
        <div class="result-block">
            <div><strong>Quarantined files:</strong> {{.Quarantine.Total}}</div>
            {{range .Quarantine.Recent}}<div><strong>{{.Provider}}:</strong> <span class="mono">{{clip .ID 24}}</span>{{if .QuarantinedAt}} · {{.QuarantinedAt}}{{end}}</div>{{end}}
        </div>
    </div>
    {{end}}

    <div class="stats">
        <div class="stat">
            <div class="stat-value">{{.TotalCount}}</div>
            <div class="stat-label">Total Accounts</div>
        </div>
        <div class="stat">
            <div class="stat-value">{{.PoolSummary.EligibleAccounts}}</div>
            <div class="stat-label">Eligible Seats</div>
        </div>
        <div class="stat">
            <div class="stat-value">{{.PoolSummary.WorkspaceCount}}</div>
            <div class="stat-label">Workspaces</div>
        </div>
        <div class="stat">
            <div class="stat-value">{{if .PoolSummary.NextRecoveryAt}}{{.PoolSummary.NextRecoveryAt}}{{else}}—{{end}}</div>
            <div class="stat-label">Next Recovery</div>
        </div>
        <div class="stat">
            <div class="stat-value">{{if .CodexSeatCount}}{{.CodexSeatCount}}{{else}}{{.CodexCount}}{{end}}</div>
            <div class="stat-label">Codex Seats</div>
        </div>
        {{if or .LocalOperatorEnabled (gt .OpenAIAPIPool.TotalKeys 0)}}
        <div class="stat">
            <div class="stat-value">{{.OpenAIAPIPool.TotalKeys}}</div>
            <div class="stat-label">OpenAI API Keys</div>
        </div>
        {{end}}
        {{if or .LocalOperatorEnabled (gt .GitLabClaudePool.TotalTokens 0)}}
        <div class="stat">
            <div class="stat-value">{{.GitLabClaudePool.TotalTokens}}</div>
            <div class="stat-label">GitLab Claude Tokens</div>
        </div>
        {{end}}
        <div class="stat">
            <div class="stat-value">{{.GeminiCount}}</div>
            <div class="stat-label">Gemini</div>
        </div>
        {{if .ClaudeCount}}
        <div class="stat">
            <div class="stat-value">{{.ClaudeCount}}</div>
            <div class="stat-label">Claude</div>
        </div>
        {{end}}
        {{if .PoolUsers}}
        <div class="stat">
            <div class="stat-value">{{.PoolUsers}}</div>
            <div class="stat-label">Pool Users</div>
        </div>
        {{end}}
    </div>

    {{if .WorkspaceGroups}}
    <h2 style="color: #58a6ff; margin-top: 20px; margin-bottom: 10px;">🧩 Workspace Groups</h2>
    <div class="table-wrap">
    <table>
        <tr>
            <th>Workspace</th>
            <th>Provider</th>
            <th>Seats</th>
            <th>Eligible</th>
            <th>Blocked</th>
            <th>Next Recovery</th>
            <th>Accounts</th>
            <th>Emails</th>
        </tr>
        {{range .WorkspaceGroups}}
        <tr>
            <td class="mono">{{if .WorkspaceID}}{{.WorkspaceID}}{{else}}unknown{{end}}</td>
            <td>
                {{if eq .Provider "codex"}}<span class="tag tag-codex">codex</span>{{end}}
                {{if eq .Provider "gemini"}}<span class="tag tag-gemini">gemini</span>{{end}}
                {{if eq .Provider "claude"}}<span class="tag tag-claude">claude</span>{{end}}
            </td>
            <td>{{.SeatCount}}</td>
            <td><span class="status-ok">{{.EligibleSeatCount}}</span></td>
            <td>{{if .BlockedSeatCount}}<span class="status-warn">{{.BlockedSeatCount}}</span>{{else}}0{{end}}</td>
            <td>{{if .NextRecoveryAt}}{{.NextRecoveryAt}}{{else}}—{{end}}</td>
            <td class="mono">{{join .AccountIDs ", "}}</td>
            <td>{{join .Emails ", "}}</td>
        </tr>
        {{end}}
    </table>
    </div>
    {{end}}

    {{if .PoolUtilization}}
    <h2 style="color: #58a6ff; margin-top: 20px; margin-bottom: 10px;">⏱ Time-Weighted Utilization</h2>
    <p style="color: #8b949e; font-size: 12px; margin-bottom: 15px;">
        Accounts near reset are discounted — their high usage is about to be wiped.
        <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">effective = used% × time_to_reset / window</code>
    </p>
    <div class="stats" style="flex-wrap: wrap;">
        {{range .PoolUtilization}}
        <div class="stat" style="min-width: 200px;">
            <div style="margin-bottom: 8px;">
                {{if eq .Provider "codex"}}<span class="tag tag-codex">codex</span>{{end}}
                {{if eq .Provider "claude"}}<span class="tag tag-claude">claude</span>{{end}}
                {{if eq .Provider "gemini"}}<span class="tag tag-gemini">gemini</span>{{end}}
            </div>
            <div style="display: flex; gap: 20px; margin-bottom: 4px;">
                <div>
                    <div class="stat-value" style="font-size: 22px;">{{printf "%.0f%%" .TimeWeightedSecondaryPct}}</div>
                    <div class="stat-label">Secondary</div>
                </div>
                <div>
                    <div class="stat-value" style="font-size: 22px;">{{printf "%.0f%%" .TimeWeightedPrimaryPct}}</div>
                    <div class="stat-label">Primary</div>
                </div>
            </div>
            <div style="color: #8b949e; font-size: 12px; margin-top: 6px;">
                {{.AvailableAccounts}}/{{.TotalAccounts}} healthy seats routable
                {{if .NextSecondaryResetIn}} · next reset: {{.NextSecondaryResetIn}}{{end}}
                {{if .ResetsIn24h}} · {{.ResetsIn24h}} reset in 24h{{end}}
            </div>
        </div>
        {{end}}
    </div>
    {{end}}

    <h2 style="color: #58a6ff; margin-top: 20px; margin-bottom: 10px;">🪑 Seats</h2>
    <div class="table-wrap">
    <table>
        <tr>
            <th>Account</th>
            <th>Provider</th>
            <th>Plan</th>
            <th>Workspace</th>
            <th>Seat</th>
            <th>Routing</th>
            <th>Remaining (5h)</th>
            <th>Remaining (7d)</th>
            <th>Recovery</th>
            <th>Routing Score</th>
            <th>Auth TTL</th>
            <th>Local Last Used</th>
            <th>Local Tokens</th>
        </tr>
        {{range .Accounts}}
        <tr>
            <td>
                <span title="{{.ID}}">{{clip .ID 30}}</span>
                {{if .Disabled}}<span class="tag tag-disabled">disabled</span>{{end}}
                {{if .Dead}}<span class="tag tag-dead">dead</span>{{end}}
                {{if .Email}}<br><small>{{.Email}}</small>{{end}}
            </td>
            <td>
                {{if eq .Type "codex"}}<span class="tag tag-codex">codex</span>{{end}}
                {{if eq .Type "gemini"}}<span class="tag tag-gemini">gemini</span>{{end}}
                {{if eq .Type "claude"}}<span class="tag tag-claude">claude</span>{{end}}
            </td>
            <td>
                {{if eq .PlanType "pro"}}<span class="tag tag-pro">pro</span>{{end}}
                {{if eq .PlanType "plus"}}<span class="tag tag-plus">plus</span>{{end}}
                {{if eq .PlanType "team"}}<span class="tag tag-team">team</span>{{end}}
                {{if eq .PlanType "api"}}<span class="tag tag-api">api</span>{{end}}
                {{if eq .PlanType "max"}}<span class="tag tag-claude">max</span>{{end}}
                {{if eq .PlanType "gemini"}}<span class="tag tag-gemini">gemini</span>{{end}}
                {{if eq .PlanType "claude"}}<span class="tag tag-claude">claude</span>{{end}}
            </td>
            <td class="mono" title="{{if .WorkspaceID}}{{.WorkspaceID}}{{else}}unknown{{end}}">{{if .WorkspaceID}}{{clipOpaque .WorkspaceID}}{{else}}unknown{{end}}</td>
            <td class="mono" title="{{if .SeatKey}}{{.SeatKey}}{{else}}{{.ID}}{{end}}">{{if .SeatKey}}{{clipOpaque .SeatKey}}{{else}}{{clip .ID 24}}{{end}}</td>
            <td>
                {{if .Routing.Eligible}}<span class="status-ok">{{if .Routing.State}}{{.Routing.State}}{{else}}eligible{{end}}</span>{{else}}<span class="status-warn">{{if .Routing.State}}{{.Routing.State}}{{else}}{{.Routing.BlockReason}}{{end}}</span>{{end}}
                {{if .FallbackOnly}}<br><small><span class="tag tag-api">fallback</span></small>{{end}}
                {{if .OperatorSource}}<br><small><span class="tag tag-gemini">{{.OperatorSource}}</span></small>{{end}}
	                <br><small class="detail-line">headroom {{headroomPct .Routing.PrimaryHeadroomPct .Routing.PrimaryHeadroomKnown}} / {{headroomPct .Routing.SecondaryHeadroomPct .Routing.SecondaryHeadroomKnown}}</small>
                {{if .Routing.DegradedReason}}<br><small class="detail-line">routing detail {{clip .Routing.DegradedReason 88}}</small>{{end}}
                {{if .UsageObserved}}<br><small class="detail-line">usage {{.UsageObserved}}</small>{{end}}
                {{if .GitLabRateLimitName}}<br><small class="detail-line" title="{{.GitLabRateLimitName}}{{if .GitLabRateLimitResetAt}} · reset {{.GitLabRateLimitResetAt}}{{end}}">gitlab api {{.GitLabRateLimitRemaining}}/{{.GitLabRateLimitLimit}}{{if .GitLabRateLimitResetIn}} · resets in {{.GitLabRateLimitResetIn}}{{end}}</small>{{end}}
                {{if .GitLabQuotaExceededCount}}<br><small class="detail-line">quota backoff ×{{.GitLabQuotaExceededCount}}{{if .GitLabQuotaProbeIn}} · next probe {{.GitLabQuotaProbeIn}}{{end}}</small>{{end}}
                {{if or .HealthStatus .HealthError}}<br><small class="detail-line" title="{{sanitize .HealthError}}">health {{if .HealthStatus}}{{.HealthStatus}}{{else}}unknown{{end}}{{if .HealthError}} · {{clip (sanitize .HealthError) 88}}{{end}}</small>{{end}}
                {{if and (eq .Type "gemini") .ProviderTruth}}<br><small class="detail-line">provider {{if .ProviderTruth.State}}{{.ProviderTruth.State}}{{else if .ProviderTruth.Ready}}ready{{else}}unknown{{end}}{{if .ProviderTruth.Stale}} · stale{{end}}{{if .ProviderTruth.ProjectID}} · project <span class="mono" title="{{.ProviderTruth.ProjectID}}">{{clipOpaque .ProviderTruth.ProjectID}}</span>{{end}}</small>{{end}}
                {{if and (eq .Type "gemini") .OperationalTruth}}<br><small class="detail-line">operational {{if .OperationalTruth.State}}{{.OperationalTruth.State}}{{else}}unknown{{end}}{{if .OperationalTruth.Reason}} · {{clip .OperationalTruth.Reason 88}}{{end}}{{if .OperationalTruth.CheckedAt}} · checked {{.OperationalTruth.CheckedAt}}{{end}}</small>{{end}}
                {{if and (eq .Type "gemini") .ProviderQuotaSummary}}<br><small class="detail-line">quota {{.ProviderQuotaSummary}}</small>{{end}}
                {{if and (eq .Type "gemini") .ProviderTruth .ProviderTruth.Quota}}
                    {{with .ProviderTruth.Quota}}
                        {{if gt (len .Models) 0}}
                        <div class="quota-model-list">
                            {{range .Models}}
                            <div class="quota-model-row">
                                <div class="quota-model-head">
                                    <span class="quota-model-name">{{if .DisplayName}}{{.DisplayName}}{{else}}{{.Name}}{{end}}</span>
                                    <span class="quota-model-meter">{{.Percentage}}%</span>
                                </div>
                                {{if and .DisplayName (ne .DisplayName .Name)}}<small class="detail-line mono">{{.Name}}</small>{{else if .Name}}<small class="detail-line mono">{{.Name}}</small>{{end}}
                                <small class="detail-line">
                                    {{if .ResetTime}}reset {{.ResetTime}}{{end}}
                                    {{if .CompatibilityReason}}{{if .ResetTime}} · {{end}}{{clip .CompatibilityReason 88}}{{end}}
                                </small>
                                {{if or (gt .MaxTokens 0) (gt .MaxOutputTokens 0) (gt .ThinkingBudget 0)}}
                                <small class="detail-line">
                                    {{if gt .MaxTokens 0}}max {{printf "%d" .MaxTokens}}{{end}}
                                    {{if and (gt .MaxTokens 0) (gt .MaxOutputTokens 0)}} · {{end}}
                                    {{if gt .MaxOutputTokens 0}}max out {{printf "%d" .MaxOutputTokens}}{{end}}
                                    {{if and (or (gt .MaxTokens 0) (gt .MaxOutputTokens 0)) (gt .ThinkingBudget 0)}} · {{end}}
                                    {{if gt .ThinkingBudget 0}}thinking {{printf "%d" .ThinkingBudget}}{{end}}
                                </small>
                                {{end}}
                                <div class="quota-model-tags">
                                    {{if eq .RouteProvider "gemini"}}<span class="tag tag-gemini">gemini</span>{{end}}
                                    {{if eq .RouteProvider "claude"}}<span class="tag tag-claude">claude</span>{{end}}
                                    {{if and .RouteProvider (ne .RouteProvider "gemini") (ne .RouteProvider "claude")}}<span class="tag tag-state-unknown">{{.RouteProvider}}</span>{{end}}
                                    <span class="{{quotaStateTagClass .Routable .CompatibilityLane .RouteProvider}}">{{quotaStateLabel .Routable .CompatibilityLane .RouteProvider}}</span>
                                    {{if .Protected}}<span class="tag tag-disabled">protected</span>{{end}}
                                    {{if .Recommended}}<span class="tag tag-pro">recommended</span>{{end}}
                                    {{if .SupportsImages}}<span class="tag tag-capability">images</span>{{end}}
                                    {{if .SupportsThinking}}<span class="tag tag-capability-thinking">thinking</span>{{end}}
                                </div>
                            </div>
                            {{end}}
                        </div>
                        {{end}}
                    {{end}}
                {{end}}
                {{if .ProbeSummary}}<br><small class="detail-line">{{.ProbeSummary}}</small>{{end}}
                {{if and .FallbackOnly (gt .Penalty 0)}}<br><small class="detail-line">penalty {{printf "%.2f" .Penalty}}</small>{{end}}
                {{if .DeadSince}}<br><small class="detail-line">dead since {{.DeadSince}}</small>{{end}}
            </td>
            <td class="usage-cell">
	                {{remainingBarKnown .Routing.PrimaryHeadroomPct .Routing.PrimaryHeadroomKnown}}<small>remaining {{headroomPct .Routing.PrimaryHeadroomPct .Routing.PrimaryHeadroomKnown}}</small>
	                <br><small>used {{if .Routing.PrimaryHeadroomKnown}}{{pct .PrimaryUsed}}{{else}}n/a{{end}}</small>
                {{if .PrimaryResetIn}}<br><small>resets in {{.PrimaryResetIn}}</small>{{end}}
            </td>
            <td class="usage-cell">
	                {{remainingBarKnown .Routing.SecondaryHeadroomPct .Routing.SecondaryHeadroomKnown}}<small>remaining {{headroomPct .Routing.SecondaryHeadroomPct .Routing.SecondaryHeadroomKnown}}</small>
	                <br><small>used {{if .Routing.SecondaryHeadroomKnown}}{{pct .SecondaryUsed}}{{else}}n/a{{end}}</small>
                {{if .SecondaryResetIn}}<br><small>resets in {{.SecondaryResetIn}}</small>{{end}}
            </td>
            <td>{{if .RecoveryAt}}{{.RecoveryAt}}{{else}}—{{end}}</td>
            <td>
                {{if .Dead}}<span class="status-dead">—</span>
                {{else if .Disabled}}<span class="status-warn">—</span>
                {{else}}{{score .Score}}{{end}}
            </td>
            <td>{{if .AuthExpiresIn}}{{.AuthExpiresIn}}{{else if .FallbackOnly}}managed key{{else}}—{{end}}</td>
            <td>{{.LocalLastUsed}}</td>
            <td>{{.LocalTokens}}</td>
        </tr>
        {{end}}
    </table>
    </div>

    {{if .TokenAnalytics}}
    <h2 style="color: #58a6ff; margin-top: 30px;">📊 Capacity Analysis</h2>
    <p style="color: #8b949e; font-size: 13px; margin-bottom: 15px;">
        Estimating capacity from <strong>{{.TokenAnalytics.TotalSamples}}</strong> samples.
        Formula: <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">{{.TokenAnalytics.ModelInfo}}</code>
    </p>

    {{if .TokenAnalytics.PlanCapacities}}
    <table style="margin-bottom: 20px;">
        <tr>
            <th>Plan</th>
            <th>Samples</th>
            <th>Confidence</th>
            <th>Input Tokens</th>
            <th>Output Tokens</th>
            <th>Cached</th>
            <th>Reasoning</th>
            <th>Output Mult</th>
            <th>5h Capacity</th>
            <th>7d Capacity</th>
        </tr>
        {{range .TokenAnalytics.PlanCapacities}}
        <tr>
            <td>
                {{if eq .PlanType "pro"}}<span class="tag tag-pro">pro</span>{{end}}
                {{if eq .PlanType "plus"}}<span class="tag tag-plus">plus</span>{{end}}
                {{if eq .PlanType "team"}}<span class="tag tag-team">team</span>{{end}}
                {{if eq .PlanType "gemini"}}<span class="tag tag-gemini">gemini</span>{{end}}
            </td>
            <td>{{.SampleCount}}</td>
            <td>
                {{if eq .Confidence "high"}}<span style="color: #3fb950;">●</span> high{{end}}
                {{if eq .Confidence "medium"}}<span style="color: #d29922;">●</span> medium{{end}}
                {{if eq .Confidence "low"}}<span style="color: #8b949e;">●</span> low{{end}}
            </td>
            <td>{{.TotalInputTokens}}</td>
            <td>{{.TotalOutputTokens}}</td>
            <td>{{.TotalCachedTokens}}</td>
            <td>{{.TotalReasoningTokens}}</td>
            <td>{{printf "%.1fx" .OutputMultiplier}}</td>
            <td>{{if .EstimatedPrimaryCapacity}}{{.EstimatedPrimaryCapacity}}{{else}}—{{end}}</td>
            <td>{{if .EstimatedSecondaryCapacity}}{{.EstimatedSecondaryCapacity}}{{else}}—{{end}}</td>
        </tr>
        {{end}}
    </table>
    {{else}}
    <p style="color: #8b949e;">No capacity data collected yet. Use the pool to gather samples.</p>
    {{end}}
    {{end}}

    <p style="margin-top: 20px; color: #8b949e; font-size: 12px;">
	        <strong>Note:</strong> Remaining columns show remaining headroom, not used quota.
	        Primary/Secondary usage and recovery come from the latest observed quota snapshot.
	        Codex seats leave rotation once headroom reaches 10% remaining and stay out until the observed reset restores headroom.
	        Gemini seats can show <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">n/a</code> until the local proxy has an actual headroom observation.
	        Gemini <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">provider</code>, <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">operational</code>, and <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">routing</code> lines are additive on purpose: a seat may be degraded-enabled even when provider truth is restricted.
	        <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">Auth TTL</code>,
        <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">Local Last Used</code>, and
        <code style="background: #21262d; padding: 2px 6px; border-radius: 3px;">Local Tokens</code>
        are local proxy/runtime fields, not external quota consumption.
        "Effective" usage shows the weighted value used for load balancing.
        <br>
        <a href="/">Return to Dashboard</a> ·
        <a href="/status?format=json">Status JSON</a> ·
        <a href="/healthz">Health check</a>
    </p>
</body>
</html>`
