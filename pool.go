package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AccountType distinguishes between different API backends.
type AccountType string

const (
	AccountTypeCodex   AccountType = "codex"
	AccountTypeGemini  AccountType = "gemini"
	AccountTypeClaude  AccountType = "claude"
	AccountTypeKimi    AccountType = "kimi"
	AccountTypeMinimax AccountType = "minimax"
)

const (
	accountAuthModeOAuth  = "oauth"
	accountAuthModeAPIKey = "api_key"
	accountAuthModeGitLab = "gitlab_duo"
)

const (
	geminiOperatorSourceManagedOAuth       = "managed_oauth"
	geminiOperatorSourceManualImport       = "manual_import"
	geminiOperatorSourceAntigravityImport  = "antigravity_import"
	geminiOperatorSourceManualImportLegacy = "manual_import_legacy"
)

const (
	geminiProviderTruthStateReady                 = "ready"
	geminiProviderTruthStateRestricted            = "restricted"
	geminiProviderTruthStateAuthOnly              = "auth_only"
	geminiProviderTruthStateProjectOnlyUnverified = "project_only_unverified"
	geminiProviderTruthStateMissingProjectID      = "missing_project_id"
	geminiProviderTruthStateProxyDisabled         = "proxy_disabled"
	geminiProviderTruthStateValidationBlocked     = "validation_blocked"
	geminiProviderTruthStateQuotaForbidden        = "quota_forbidden"
)

const (
	geminiProviderTruthFreshnessStateFresh = "fresh"
	geminiProviderTruthFreshnessStateStale = "stale"
	geminiProviderTruthFreshnessWindow     = 30 * time.Minute
)

const (
	geminiOperationalTruthStateCleanOK    = "clean_ok"
	geminiOperationalTruthStateDegradedOK = "degraded_ok"
	geminiOperationalTruthStateCooldown   = "cooldown"
	geminiOperationalTruthStateHardFail   = "hard_fail"
)

const (
	routingDisplayStateEnabled         = "enabled"
	routingDisplayStateDegradedEnabled = "degraded_enabled"
	routingDisplayStateQuarantined     = "quarantined"
	routingDisplayStateCooldown        = "cooldown"
	routingDisplayStateBlocked         = "blocked"
)

// Codex seats leave rotation once usage reaches 90%, i.e. when remaining headroom is 10% or below.
const codexPreemptiveUsedThreshold = 0.90

type routingState struct {
	Eligible               bool
	BlockReason            string
	PrimaryUsed            float64
	SecondaryUsed          float64
	PrimaryHeadroom        float64
	SecondaryHeadroom      float64
	PrimaryHeadroomKnown   bool
	SecondaryHeadroomKnown bool
	RecoveryAt             time.Time
	CodexRateLimitBypass   bool
}

type geminiProviderTruthFreshness struct {
	State         string
	Stale         bool
	Reason        string
	FreshUntil    time.Time
	ProviderStale bool
	QuotaStale    bool
}

func usagePercentOrLegacy(percentValue, rawValue float64) float64 {
	used := percentValue
	if used == 0 && rawValue > 0 {
		used = rawValue
	}
	if used < 0 {
		return 0
	}
	if used > 1 {
		return 1
	}
	return used
}

func effectiveUsageForRouting(snapshot UsageSnapshot, now time.Time) (float64, float64) {
	primaryUsed := usagePercentOrLegacy(snapshot.PrimaryUsedPercent, snapshot.PrimaryUsed)
	secondaryUsed := usagePercentOrLegacy(snapshot.SecondaryUsedPercent, snapshot.SecondaryUsed)
	if !snapshot.PrimaryResetAt.IsZero() && !snapshot.PrimaryResetAt.After(now) {
		primaryUsed = 0
	}
	if !snapshot.SecondaryResetAt.IsZero() && !snapshot.SecondaryResetAt.After(now) {
		secondaryUsed = 0
	}
	return primaryUsed, secondaryUsed
}

func usageSnapshotHasObservedHeadroom(snapshot UsageSnapshot) bool {
	if snapshot.PrimaryUsed != 0 || snapshot.SecondaryUsed != 0 ||
		snapshot.PrimaryUsedPercent != 0 || snapshot.SecondaryUsedPercent != 0 {
		return true
	}
	if !snapshot.RetrievedAt.IsZero() || strings.TrimSpace(snapshot.Source) != "" {
		return true
	}
	if !snapshot.PrimaryResetAt.IsZero() || !snapshot.SecondaryResetAt.IsZero() {
		return true
	}
	if snapshot.PrimaryWindowMinutes > 0 || snapshot.SecondaryWindowMinutes > 0 {
		return true
	}
	return false
}

func usageAtOrAbovePreemptiveThreshold(used float64) bool {
	return used >= codexPreemptiveUsedThreshold
}

func earliestFutureTime(now time.Time, candidates ...time.Time) time.Time {
	var earliest time.Time
	for _, candidate := range candidates {
		if candidate.IsZero() || !candidate.After(now) {
			continue
		}
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return earliest
}

func routingStateLocked(a *Account, now time.Time, accountType AccountType, requiredPlan string) routingState {
	if a == nil {
		return routingState{Eligible: false, BlockReason: "missing_account"}
	}
	isManagedCodexAPI := isManagedCodexAPIKeyAccount(a)
	primaryUsed, secondaryUsed := effectiveUsageForRouting(a.Usage, now)
	state := routingState{
		Eligible:               true,
		PrimaryUsed:            primaryUsed,
		SecondaryUsed:          secondaryUsed,
		PrimaryHeadroom:        1.0 - primaryUsed,
		SecondaryHeadroom:      1.0 - secondaryUsed,
		PrimaryHeadroomKnown:   true,
		SecondaryHeadroomKnown: true,
	}
	if a.Type == AccountTypeGemini && !usageSnapshotHasObservedHeadroom(a.Usage) {
		state.PrimaryHeadroomKnown = false
		state.SecondaryHeadroomKnown = false
	}
	if a.Dead {
		state.Eligible = false
		state.BlockReason = "dead"
		return state
	}
	if a.Disabled {
		state.Eligible = false
		state.BlockReason = "disabled"
		return state
	}
	if accountType != "" && a.Type != accountType {
		state.Eligible = false
		state.BlockReason = "type_mismatch"
		return state
	}
	if !planMatchesRequired(a.PlanType, requiredPlan) {
		state.Eligible = false
		state.BlockReason = "plan_mismatch"
		return state
	}
	if isGitLabClaudeAccount(a) && missingGitLabClaudeGatewayState(a) {
		state.Eligible = false
		state.BlockReason = "missing_gateway_state"
		return state
	}
	if a.Type == AccountTypeGemini {
		syncGeminiProviderTruthStateLocked(a)
		freshness := geminiProviderTruthFreshnessStatus(a.GeminiProviderTruthState, a.GeminiProviderCheckedAt, a.GeminiQuotaUpdatedAt, now)
		switch {
		case a.AntigravityProxyDisabled:
			state.Eligible = false
			state.BlockReason = "proxy_disabled"
			return state
		case a.AntigravityValidationBlocked:
			if !canRouteValidationBlockedAntigravityGemini(a) {
				state.Eligible = false
				state.BlockReason = "validation_blocked"
				return state
			}
		case a.AntigravityQuotaForbidden:
			state.Eligible = false
			state.BlockReason = "quota_forbidden"
			return state
		}
		switch strings.TrimSpace(a.GeminiOperationalState) {
		case geminiOperationalTruthStateHardFail:
			state.Eligible = false
			state.BlockReason = "operational_hard_fail"
			return state
		}
		switch strings.TrimSpace(a.GeminiProviderTruthState) {
		case geminiProviderTruthStateMissingProjectID:
			state.Eligible = false
			state.BlockReason = "missing_project_id"
			return state
		case geminiProviderTruthStateRestricted, geminiProviderTruthStateProjectOnlyUnverified, geminiProviderTruthStateAuthOnly:
			if !geminiHasOperationalProof(a.GeminiOperationalState) {
				state.Eligible = false
				state.BlockReason = "not_warmed"
				return state
			}
		}
		if freshness.Stale {
			state.Eligible = false
			state.BlockReason = "stale_provider_truth"
			if freshness.QuotaStale && !freshness.ProviderStale {
				state.BlockReason = "stale_quota_snapshot"
			}
			if !freshness.FreshUntil.IsZero() {
				state.RecoveryAt = freshness.FreshUntil
			}
			return state
		}
		primaryBlocked := state.PrimaryHeadroomKnown && usageAtOrAbovePreemptiveThreshold(primaryUsed)
		secondaryBlocked := state.SecondaryHeadroomKnown && usageAtOrAbovePreemptiveThreshold(secondaryUsed)
		if primaryBlocked || secondaryBlocked {
			state.Eligible = false
			state.BlockReason = "quota_pressured"
			switch {
			case primaryBlocked && secondaryBlocked:
				state.RecoveryAt = earliestFutureTime(now, a.Usage.PrimaryResetAt, a.Usage.SecondaryResetAt)
			case primaryBlocked:
				state.RecoveryAt = earliestFutureTime(now, a.Usage.PrimaryResetAt)
			default:
				state.RecoveryAt = earliestFutureTime(now, a.Usage.SecondaryResetAt)
			}
			return state
		}
	}
	if !a.RateLimitUntil.IsZero() && a.RateLimitUntil.After(now) {
		state.Eligible = false
		state.BlockReason = "rate_limited"
		state.RecoveryAt = a.RateLimitUntil
		return state
	}
	if a.Type == AccountTypeCodex && !isManagedCodexAPI {
		primaryBlocked := usageAtOrAbovePreemptiveThreshold(primaryUsed)
		secondaryBlocked := usageAtOrAbovePreemptiveThreshold(secondaryUsed)
		switch {
		case primaryBlocked && secondaryBlocked:
			state.Eligible = false
			state.BlockReason = "codex_headroom_lt_10"
			state.RecoveryAt = earliestFutureTime(now, a.Usage.PrimaryResetAt, a.Usage.SecondaryResetAt)
		case primaryBlocked:
			state.Eligible = false
			state.BlockReason = "primary_headroom_lt_10"
			state.RecoveryAt = earliestFutureTime(now, a.Usage.PrimaryResetAt)
		case secondaryBlocked:
			state.Eligible = false
			state.BlockReason = "secondary_headroom_lt_10"
			state.RecoveryAt = earliestFutureTime(now, a.Usage.SecondaryResetAt)
		}
	}
	return state
}

type Account struct {
	mu sync.Mutex

	Type         AccountType // codex, gemini, or claude
	ID           string
	File         string
	Label        string
	AccessToken  string
	RefreshToken string
	IDToken      string
	// Optional provider OAuth client metadata for providers that support multiple public clients.
	OAuthProfileID                  string
	OAuthClientID                   string
	OAuthClientSecret               string
	OperatorSource                  string
	OperatorEmail                   string
	AntigravitySource               string
	AntigravityAccountID            string
	AntigravityEmail                string
	AntigravityName                 string
	AntigravityProjectID            string
	AntigravityFile                 string
	AntigravityCurrent              bool
	AntigravityProxyDisabled        bool
	AntigravityValidationBlocked    bool
	AntigravityQuotaForbidden       bool
	AntigravityQuotaForbiddenReason string
	AntigravityQuota                map[string]any
	GeminiSubscriptionTierID        string
	GeminiSubscriptionTierName      string
	GeminiValidationReasonCode      string
	GeminiValidationMessage         string
	GeminiValidationURL             string
	GeminiProviderCheckedAt         time.Time
	GeminiProviderTruthReady        bool
	GeminiProviderTruthState        string
	GeminiProviderTruthReason       string
	GeminiOperationalState          string
	GeminiOperationalReason         string
	GeminiOperationalSource         string
	GeminiOperationalCheckedAt      time.Time
	GeminiOperationalLastSuccessAt  time.Time
	GeminiProtectedModels           []string
	GeminiQuotaModels               []GeminiModelQuotaSnapshot
	GeminiQuotaUpdatedAt            time.Time
	GeminiModelForwardingRules      map[string]string
	// AccountID corresponds to Codex `auth.json` field `tokens.account_id`.
	// Codex uses this value as the `ChatGPT-Account-ID` header.
	AccountID string
	// IDTokenChatGPTAccountID is the `chatgpt_account_id` claim extracted from the ID token.
	// We keep it for debugging/fallback but prefer AccountID when present.
	IDTokenChatGPTAccountID   string
	PlanType                  string
	AuthMode                  string
	Disabled                  bool
	Inflight                  int64
	ExpiresAt                 time.Time
	LastRefresh               time.Time
	Usage                     UsageSnapshot
	Penalty                   float64
	LastPenalty               time.Time
	Dead                      bool
	LastUsed                  time.Time
	RateLimitUntil            time.Time
	DeadSince                 time.Time
	HealthStatus              string
	HealthError               string
	HealthCheckedAt           time.Time
	LastHealthyAt             time.Time
	SourceBaseURL             string
	UpstreamBaseURL           string
	ExtraHeaders              map[string]string
	GitLabRateLimitName       string
	GitLabRateLimitLimit      int
	GitLabRateLimitRemaining  int
	GitLabRateLimitResetAt    time.Time
	GitLabQuotaExceededCount  int
	GitLabLastQuotaExceededAt time.Time

	// Aggregated token counters (in-memory for now; persist later)
	Totals AccountUsage
}

func accountAuthMode(a *Account) string {
	if a == nil {
		return accountAuthModeOAuth
	}
	mode := strings.TrimSpace(strings.ToLower(a.AuthMode))
	if mode == "" {
		return accountAuthModeOAuth
	}
	return mode
}

func isManagedCodexAPIKeyAccount(a *Account) bool {
	return a != nil && a.Type == AccountTypeCodex && accountAuthMode(a) == accountAuthModeAPIKey
}

func normalizeGeminiOperatorSource(source, profileID string, accountType AccountType) string {
	if accountType != AccountTypeGemini {
		return ""
	}
	switch strings.TrimSpace(source) {
	case geminiOperatorSourceManagedOAuth:
		return geminiOperatorSourceManagedOAuth
	case geminiOperatorSourceManualImport:
		return geminiOperatorSourceManualImport
	case geminiOperatorSourceAntigravityImport:
		return geminiOperatorSourceAntigravityImport
	case geminiOperatorSourceManualImportLegacy:
		return geminiOperatorSourceManualImportLegacy
	}
	if strings.TrimSpace(profileID) != "" {
		return geminiOperatorSourceManagedOAuth
	}
	return ""
}

func storedGeminiOperatorSource(source, profileID string, accountType AccountType) string {
	if accountType != AccountTypeGemini {
		return ""
	}
	switch strings.TrimSpace(source) {
	case geminiOperatorSourceManagedOAuth:
		return geminiOperatorSourceManagedOAuth
	case geminiOperatorSourceManualImport:
		return geminiOperatorSourceManualImport
	case geminiOperatorSourceAntigravityImport:
		return geminiOperatorSourceAntigravityImport
	case geminiOperatorSourceManualImportLegacy:
		return geminiOperatorSourceManualImportLegacy
	}
	if strings.TrimSpace(profileID) != "" {
		return geminiOperatorSourceManagedOAuth
	}
	return ""
}

func codexAccountCountsAgainstQuota(a *Account) bool {
	return a != nil && a.Type == AccountTypeCodex && !isManagedCodexAPIKeyAccount(a)
}

// UsageSnapshot captures Codex usage headroom and optional credit info.
// PrimaryUsed/SecondaryUsed are kept for backward compatibility; values are 0-1.
type UsageSnapshot struct {
	PrimaryUsed            float64
	SecondaryUsed          float64
	PrimaryUsedPercent     float64
	SecondaryUsedPercent   float64
	PrimaryWindowMinutes   int
	SecondaryWindowMinutes int
	PrimaryResetAt         time.Time
	SecondaryResetAt       time.Time
	CreditsBalance         float64
	HasCredits             bool
	CreditsUnlimited       bool
	RetrievedAt            time.Time
	Source                 string
}

// RequestUsage captures per-request token consumption parsed from SSE events.
type RequestUsage struct {
	Timestamp         time.Time
	AccountID         string
	PlanType          string
	UserID            string
	PromptCacheKey    string
	RequestID         string
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
	BillableTokens    int64
	// Rate limit snapshot after this request
	PrimaryUsedPct   float64
	SecondaryUsedPct float64
	// Model and provider info
	Model       string      `json:"model,omitempty"`        // e.g., "claude-sonnet-4-5-20250929", "o4-mini"
	AccountType AccountType `json:"account_type,omitempty"` // "claude", "codex", "gemini"
}

// AccountUsage stores aggregates for an account with time windows.
type AccountUsage struct {
	TotalInputTokens     int64 `json:"total_input_tokens"`
	TotalCachedTokens    int64 `json:"total_cached_tokens"`
	TotalOutputTokens    int64 `json:"total_output_tokens"`
	TotalReasoningTokens int64 `json:"total_reasoning_tokens"`
	TotalBillableTokens  int64 `json:"total_billable_tokens"`
	RequestCount         int64 `json:"request_count"`
	// For calculating tokens-per-percent
	LastPrimaryPct   float64   `json:"last_primary_pct"`
	LastSecondaryPct float64   `json:"last_secondary_pct"`
	LastUpdated      time.Time `json:"last_updated"`
}

// TokenCapacity tracks tokens-per-percent for capacity analysis.
type TokenCapacity struct {
	PlanType               string  `json:"plan_type"`
	SampleCount            int64   `json:"sample_count"`
	TotalTokens            int64   `json:"total_tokens"`
	TotalPrimaryPctDelta   float64 `json:"total_primary_pct_delta"`
	TotalSecondaryPctDelta float64 `json:"total_secondary_pct_delta"`

	// Raw token type totals for weighted estimation
	TotalInputTokens     int64 `json:"total_input_tokens"`
	TotalCachedTokens    int64 `json:"total_cached_tokens"`
	TotalOutputTokens    int64 `json:"total_output_tokens"`
	TotalReasoningTokens int64 `json:"total_reasoning_tokens"`

	// Derived: raw billable tokens per 1% of quota
	TokensPerPrimaryPct   float64 `json:"tokens_per_primary_pct,omitempty"`
	TokensPerSecondaryPct float64 `json:"tokens_per_secondary_pct,omitempty"`

	// Derived: weighted effective tokens per 1% (accounts for token cost differences)
	// Formula: effective = input + (cached * 0.1) + (output * OutputMultiplier) + (reasoning * ReasoningMultiplier)
	EffectivePerPrimaryPct   float64 `json:"effective_per_primary_pct,omitempty"`
	EffectivePerSecondaryPct float64 `json:"effective_per_secondary_pct,omitempty"`

	// Estimated multipliers (refined over time with more data)
	OutputMultiplier    float64 `json:"output_multiplier,omitempty"`    // How much more output costs vs input (typically 3-5x)
	ReasoningMultiplier float64 `json:"reasoning_multiplier,omitempty"` // How much reasoning costs vs input
}

// applyRequestUsage increments aggregate counters for the account.
func (a *Account) applyRequestUsage(u RequestUsage) {
	a.mu.Lock()
	a.Totals.TotalInputTokens += u.InputTokens
	a.Totals.TotalCachedTokens += u.CachedInputTokens
	a.Totals.TotalOutputTokens += u.OutputTokens
	a.Totals.TotalReasoningTokens += u.ReasoningTokens
	a.Totals.TotalBillableTokens += u.BillableTokens
	a.Totals.RequestCount++
	if u.PrimaryUsedPct > 0 {
		a.Totals.LastPrimaryPct = u.PrimaryUsedPct
	}
	if u.SecondaryUsedPct > 0 {
		a.Totals.LastSecondaryPct = u.SecondaryUsedPct
	}
	a.Totals.LastUpdated = u.Timestamp
	a.mu.Unlock()
}

// CodexAuthJSON is the format for Codex auth.json files.
type CodexAuthJSON struct {
	OpenAIKey       *string    `json:"OPENAI_API_KEY"`
	Tokens          *TokenData `json:"tokens"`
	LastRefresh     *time.Time `json:"last_refresh"`
	LastHealthyAt   *time.Time `json:"last_healthy_at"`
	HealthCheckedAt *time.Time `json:"health_checked_at"`
	DeadSince       *time.Time `json:"dead_since"`
	HealthStatus    string     `json:"health_status"`
	HealthError     string     `json:"health_error"`
	PlanType        string     `json:"plan_type"`
	AuthMode        string     `json:"auth_mode"`
	Dead            bool       `json:"dead"`
	Disabled        bool       `json:"disabled"`
}

type TokenData struct {
	IDToken      string  `json:"id_token"`
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	AccountID    *string `json:"account_id"`
}

type GeminiModelQuotaSnapshot struct {
	Name               string          `json:"name"`
	RouteProvider      string          `json:"route_provider,omitempty"`
	Percentage         int             `json:"percentage"`
	ResetTime          string          `json:"reset_time,omitempty"`
	DisplayName        string          `json:"display_name,omitempty"`
	SupportsImages     bool            `json:"supports_images,omitempty"`
	SupportsThinking   bool            `json:"supports_thinking,omitempty"`
	ThinkingBudget     int             `json:"thinking_budget,omitempty"`
	Recommended        bool            `json:"recommended,omitempty"`
	MaxTokens          int             `json:"max_tokens,omitempty"`
	MaxOutputTokens    int             `json:"max_output_tokens,omitempty"`
	SupportedMimeTypes map[string]bool `json:"supported_mime_types,omitempty"`
}

type geminiQuotaSnapshotPayload struct {
	Models               []GeminiModelQuotaSnapshot `json:"models,omitempty"`
	LastUpdated          int64                      `json:"last_updated,omitempty"`
	SubscriptionTier     string                     `json:"subscription_tier,omitempty"`
	ModelForwardingRules map[string]string          `json:"model_forwarding_rules,omitempty"`
}

// GeminiAuthJSON is the format for Gemini oauth_creds.json files.
// Files should be named gemini_*.json in the pool folder.
type GeminiAuthJSON struct {
	AccessToken                    string                     `json:"access_token"`
	RefreshToken                   string                     `json:"refresh_token"`
	TokenType                      string                     `json:"token_type"`
	Scope                          string                     `json:"scope"`
	OAuthProfileID                 string                     `json:"oauth_profile_id,omitempty"`
	ClientID                       string                     `json:"client_id,omitempty"`
	ClientSecret                   string                     `json:"client_secret,omitempty"`
	OperatorSource                 string                     `json:"operator_source,omitempty"`
	OperatorEmail                  string                     `json:"operator_email,omitempty"`
	OperatorName                   string                     `json:"operator_name,omitempty"`
	ExpiryDate                     int64                      `json:"expiry_date"`  // Unix timestamp in milliseconds
	PlanType                       string                     `json:"plan_type"`    // e.g., "ultra", "gemini"
	LastRefresh                    string                     `json:"last_refresh"` // RFC3339 timestamp of last refresh attempt
	LastHealthyAt                  *time.Time                 `json:"last_healthy_at,omitempty"`
	HealthCheckedAt                *time.Time                 `json:"health_checked_at,omitempty"`
	DeadSince                      *time.Time                 `json:"dead_since,omitempty"`
	HealthStatus                   string                     `json:"health_status,omitempty"`
	HealthError                    string                     `json:"health_error,omitempty"`
	RateLimitUntil                 *time.Time                 `json:"rate_limit_until,omitempty"`
	Disabled                       bool                       `json:"disabled,omitempty"`
	Dead                           bool                       `json:"dead,omitempty"`
	AntigravitySource              string                     `json:"antigravity_source,omitempty"`
	AntigravityAccountID           string                     `json:"antigravity_account_id,omitempty"`
	AntigravityEmail               string                     `json:"antigravity_email,omitempty"`
	AntigravityName                string                     `json:"antigravity_name,omitempty"`
	AntigravityProjectID           string                     `json:"antigravity_project_id,omitempty"`
	AntigravityFile                string                     `json:"antigravity_file,omitempty"`
	AntigravityCurrent             bool                       `json:"antigravity_current,omitempty"`
	AntigravityProxyDisabled       bool                       `json:"antigravity_proxy_disabled,omitempty"`
	AntigravityValidationBlocked   bool                       `json:"antigravity_validation_blocked,omitempty"`
	AntigravityQuota               map[string]any             `json:"antigravity_quota,omitempty"`
	GeminiSubscriptionTierID       string                     `json:"gemini_subscription_tier_id,omitempty"`
	GeminiSubscriptionTierName     string                     `json:"gemini_subscription_tier_name,omitempty"`
	GeminiValidationReasonCode     string                     `json:"gemini_validation_reason_code,omitempty"`
	GeminiValidationMessage        string                     `json:"gemini_validation_message,omitempty"`
	GeminiValidationURL            string                     `json:"gemini_validation_url,omitempty"`
	GeminiProviderCheckedAt        *time.Time                 `json:"gemini_provider_checked_at,omitempty"`
	GeminiProviderTruthReady       bool                       `json:"gemini_provider_truth_ready,omitempty"`
	GeminiProviderTruthState       string                     `json:"gemini_provider_truth_state,omitempty"`
	GeminiProviderTruthReason      string                     `json:"gemini_provider_truth_reason,omitempty"`
	GeminiOperationalState         string                     `json:"gemini_operational_state,omitempty"`
	GeminiOperationalReason        string                     `json:"gemini_operational_reason,omitempty"`
	GeminiOperationalSource        string                     `json:"gemini_operational_source,omitempty"`
	GeminiOperationalCheckedAt     *time.Time                 `json:"gemini_operational_checked_at,omitempty"`
	GeminiOperationalLastSuccessAt *time.Time                 `json:"gemini_operational_last_success_at,omitempty"`
	GeminiProtectedModels          []string                   `json:"gemini_protected_models,omitempty"`
	GeminiQuotaModels              []GeminiModelQuotaSnapshot `json:"gemini_quota_models,omitempty"`
	GeminiQuotaUpdatedAt           *time.Time                 `json:"gemini_quota_updated_at,omitempty"`
	GeminiModelForwardingRules     map[string]string          `json:"gemini_model_forwarding_rules,omitempty"`
}

func syncGeminiProviderTruthState(acc *Account) {
	if acc == nil || acc.Type != AccountTypeGemini {
		return
	}
	syncGeminiProviderTruthStateLocked(acc)
}

func syncGeminiProviderTruthStateLocked(acc *Account) {
	if acc == nil || acc.Type != AccountTypeGemini {
		return
	}

	state := ""
	reason := ""
	ready := false
	validationReasonCode := strings.TrimSpace(acc.GeminiValidationReasonCode)
	validationMessage := strings.TrimSpace(acc.GeminiValidationMessage)
	validationURL := strings.TrimSpace(acc.GeminiValidationURL)

	switch {
	case acc.AntigravityProxyDisabled:
		state = geminiProviderTruthStateProxyDisabled
		reason = "provider marked seat proxy_disabled"
	case geminiValidationQuarantined(acc.AntigravityValidationBlocked, validationReasonCode, validationMessage, validationURL):
		state = geminiProviderTruthStateValidationBlocked
		reason = geminiValidationReasonSummary(validationReasonCode, validationMessage, validationURL, "provider validation blocked")
	case geminiValidationRestricted(acc.AntigravityValidationBlocked, validationReasonCode, validationMessage, validationURL):
		state = geminiProviderTruthStateRestricted
		reason = geminiValidationReasonSummary(validationReasonCode, validationMessage, validationURL, "provider restriction detected")
	case acc.AntigravityQuotaForbidden:
		state = geminiProviderTruthStateQuotaForbidden
		reason = firstNonEmpty(strings.TrimSpace(acc.AntigravityQuotaForbiddenReason), "provider quota forbidden")
	case strings.TrimSpace(acc.AntigravityProjectID) != "" && !acc.GeminiProviderCheckedAt.IsZero():
		state = geminiProviderTruthStateReady
		ready = true
	case strings.TrimSpace(acc.AntigravityProjectID) != "":
		state = geminiProviderTruthStateProjectOnlyUnverified
		reason = "project_id present without provider verification"
	case !acc.GeminiProviderCheckedAt.IsZero():
		state = geminiProviderTruthStateMissingProjectID
		reason = "provider truth missing project_id"
	case strings.TrimSpace(acc.AccessToken) != "" || strings.TrimSpace(acc.RefreshToken) != "":
		state = geminiProviderTruthStateAuthOnly
		reason = "provider truth not hydrated"
	}

	if state == geminiProviderTruthStateAuthOnly &&
		strings.TrimSpace(acc.GeminiProviderTruthReason) != "" &&
		(reason == "" || reason == "provider truth not hydrated") {
		reason = strings.TrimSpace(acc.GeminiProviderTruthReason)
	}

	acc.GeminiProviderTruthReady = ready
	acc.GeminiProviderTruthState = state
	acc.GeminiProviderTruthReason = strings.TrimSpace(reason)
}

func hasGeminiValidationTruth(reasonCode, message, url string) bool {
	return strings.TrimSpace(reasonCode) != "" ||
		strings.TrimSpace(message) != "" ||
		strings.TrimSpace(url) != ""
}

func geminiValidationQuarantined(markedBlocked bool, reasonCode, message, url string) bool {
	reasonCode = strings.TrimSpace(reasonCode)
	if hasGeminiValidationTruth(reasonCode, message, url) {
		return !canRouteGeminiValidationBlockedReasonCode(reasonCode)
	}
	return markedBlocked
}

func geminiValidationRestricted(markedBlocked bool, reasonCode, message, url string) bool {
	if geminiValidationQuarantined(markedBlocked, reasonCode, message, url) {
		return false
	}
	if !hasGeminiValidationTruth(reasonCode, message, url) {
		return false
	}
	return canRouteGeminiValidationBlockedReasonCode(reasonCode)
}

func geminiValidationReasonSummary(reasonCode, message, url, fallback string) string {
	return firstNonEmpty(
		strings.TrimSpace(message),
		strings.TrimSpace(reasonCode),
		strings.TrimSpace(url),
		strings.TrimSpace(fallback),
	)
}

func geminiHasOperationalProof(state string) bool {
	switch strings.TrimSpace(state) {
	case geminiOperationalTruthStateCleanOK, geminiOperationalTruthStateDegradedOK, geminiOperationalTruthStateCooldown:
		return true
	default:
		return false
	}
}

func successfulGeminiOperationalStateLocked(acc *Account) (string, string) {
	if acc == nil || acc.Type != AccountTypeGemini {
		return geminiOperationalTruthStateCleanOK, ""
	}
	syncGeminiProviderTruthStateLocked(acc)
	state := strings.TrimSpace(acc.GeminiProviderTruthState)
	if state == "" || state == geminiProviderTruthStateReady {
		return geminiOperationalTruthStateCleanOK, ""
	}
	return geminiOperationalTruthStateDegradedOK, sanitizeStatusMessage(firstNonEmpty(
		strings.TrimSpace(acc.GeminiProviderTruthReason),
		geminiValidationReasonSummary(acc.GeminiValidationReasonCode, acc.GeminiValidationMessage, acc.GeminiValidationURL, state),
	))
}

func noteGeminiOperationalSuccessLocked(acc *Account, now time.Time, source string) {
	if acc == nil || acc.Type != AccountTypeGemini {
		return
	}
	state, reason := successfulGeminiOperationalStateLocked(acc)
	acc.RateLimitUntil = time.Time{}
	acc.GeminiOperationalState = state
	acc.GeminiOperationalReason = strings.TrimSpace(reason)
	acc.GeminiOperationalSource = strings.TrimSpace(source)
	acc.GeminiOperationalCheckedAt = now.UTC()
	acc.GeminiOperationalLastSuccessAt = now.UTC()
}

func noteGeminiOperationalFailureLocked(acc *Account, now time.Time, source string, err error) {
	if acc == nil || acc.Type != AccountTypeGemini || err == nil {
		return
	}
	if until, reason, precise, ok := geminiCodeAssistCooldownInfo(err, now); ok {
		acc.GeminiOperationalState = geminiOperationalTruthStateCooldown
		acc.GeminiOperationalReason = sanitizeStatusMessage(firstNonEmpty(reason, err.Error()))
		acc.GeminiOperationalSource = strings.TrimSpace(source)
		acc.GeminiOperationalCheckedAt = now.UTC()
		if precise || acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until.UTC()
		}
		return
	}
	if isRateLimitError(err) {
		until := now.Add(managedGeminiRateLimitWait)
		acc.GeminiOperationalState = geminiOperationalTruthStateCooldown
		acc.GeminiOperationalReason = sanitizeStatusMessage(err.Error())
		acc.GeminiOperationalSource = strings.TrimSpace(source)
		acc.GeminiOperationalCheckedAt = now.UTC()
		if acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until.UTC()
		}
		return
	}
	acc.GeminiOperationalState = geminiOperationalTruthStateHardFail
	acc.GeminiOperationalReason = sanitizeStatusMessage(err.Error())
	acc.GeminiOperationalSource = strings.TrimSpace(source)
	acc.GeminiOperationalCheckedAt = now.UTC()
}

func geminiProviderTruthFreshnessStatus(providerTruthState string, providerCheckedAt, quotaUpdatedAt, now time.Time) geminiProviderTruthFreshness {
	providerTruthState = strings.TrimSpace(providerTruthState)
	if providerTruthState != geminiProviderTruthStateReady || providerCheckedAt.IsZero() {
		return geminiProviderTruthFreshness{}
	}

	freshness := geminiProviderTruthFreshness{
		State: geminiProviderTruthFreshnessStateFresh,
	}
	freshUntil := providerCheckedAt.Add(geminiProviderTruthFreshnessWindow)
	providerStale := !freshUntil.After(now)
	quotaStale := false

	if !quotaUpdatedAt.IsZero() {
		quotaFreshUntil := quotaUpdatedAt.Add(geminiProviderTruthFreshnessWindow)
		if quotaFreshUntil.Before(freshUntil) {
			freshUntil = quotaFreshUntil
		}
		quotaStale = !quotaFreshUntil.After(now)
	}

	freshness.FreshUntil = freshUntil.UTC()
	freshness.ProviderStale = providerStale
	freshness.QuotaStale = quotaStale
	if providerStale || quotaStale {
		freshness.State = geminiProviderTruthFreshnessStateStale
		freshness.Stale = true
		switch {
		case providerStale && quotaStale:
			freshness.Reason = "provider and quota snapshots are older than the freshness window"
		case quotaStale:
			freshness.Reason = "quota snapshot is older than the freshness window"
		default:
			freshness.Reason = "provider snapshot is older than the freshness window"
		}
	}
	return freshness
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeStringSliceFromAny(value any) []string {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return normalizeStringSlice(values)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		keys = append(keys, key)
		normalized[key] = value
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = normalized[key]
	}
	return out
}

func cloneSupportedMimeTypes(values map[string]bool) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key] = values[key]
	}
	return out
}

func cloneGeminiModelQuotaSnapshots(models []GeminiModelQuotaSnapshot) []GeminiModelQuotaSnapshot {
	if len(models) == 0 {
		return nil
	}
	out := make([]GeminiModelQuotaSnapshot, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		if !geminiQuotaModelAllowedInOperatorTruth(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		routeProvider := firstNonEmpty(strings.TrimSpace(model.RouteProvider), geminiQuotaModelRouteProvider(name))
		out = append(out, GeminiModelQuotaSnapshot{
			Name:               name,
			RouteProvider:      routeProvider,
			Percentage:         model.Percentage,
			ResetTime:          strings.TrimSpace(model.ResetTime),
			DisplayName:        strings.TrimSpace(model.DisplayName),
			SupportsImages:     model.SupportsImages,
			SupportsThinking:   model.SupportsThinking,
			ThinkingBudget:     model.ThinkingBudget,
			Recommended:        model.Recommended,
			MaxTokens:          model.MaxTokens,
			MaxOutputTokens:    model.MaxOutputTokens,
			SupportedMimeTypes: cloneSupportedMimeTypes(model.SupportedMimeTypes),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		leftRank := geminiQuotaModelRouteProviderSortRank(out[i].RouteProvider)
		rightRank := geminiQuotaModelRouteProviderSortRank(out[j].RouteProvider)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if out[i].Name == out[j].Name {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].Name < out[j].Name
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func geminiQuotaModelRouteProvider(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(name, "gemini"),
		strings.HasPrefix(name, "image"),
		strings.HasPrefix(name, "imagen"):
		return "gemini"
	case strings.HasPrefix(name, "claude"):
		return "claude"
	case strings.HasPrefix(name, "gpt"):
		return "codex"
	default:
		return ""
	}
}

func geminiQuotaModelAllowedInOperatorTruth(name string) bool {
	switch geminiQuotaModelRouteProvider(name) {
	case "gemini", "claude":
		return true
	default:
		return false
	}
}

func geminiQuotaModelRouteProviderSortRank(routeProvider string) int {
	switch strings.ToLower(strings.TrimSpace(routeProvider)) {
	case "gemini":
		return 0
	case "claude":
		return 1
	case "codex":
		return 2
	default:
		return 9
	}
}

func decodeGeminiQuotaSnapshot(raw map[string]any) ([]GeminiModelQuotaSnapshot, time.Time, map[string]string, string) {
	if len(raw) == 0 {
		return nil, time.Time{}, nil, ""
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, time.Time{}, nil, ""
	}
	var payload geminiQuotaSnapshotPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, time.Time{}, nil, ""
	}
	updatedAt := time.Time{}
	if payload.LastUpdated > 0 {
		updatedAt = time.Unix(payload.LastUpdated, 0).UTC()
	}
	return cloneGeminiModelQuotaSnapshots(payload.Models), updatedAt, cloneStringMap(payload.ModelForwardingRules), strings.TrimSpace(payload.SubscriptionTier)
}

// ClaudeAuthJSON is the format for Claude auth files.
// Files should be named claude_*.json in the pool folder.
// Supports both API key format and OAuth format (from Claude Code).
type ClaudeAuthJSON struct {
	// API key format
	APIKey   string `json:"api_key,omitempty"`
	PlanType string `json:"plan_type,omitempty"` // optional: pro, max, etc.
	AuthMode string `json:"auth_mode,omitempty"`

	// OAuth format (from Claude Code keychain)
	ClaudeAiOauth *ClaudeOAuthData `json:"claudeAiOauth,omitempty"`

	// GitLab-managed Claude format.
	GitLabToken               string            `json:"gitlab_token,omitempty"`
	GitLabInstanceURL         string            `json:"gitlab_instance_url,omitempty"`
	GitLabGatewayToken        string            `json:"gitlab_gateway_token,omitempty"`
	GitLabGatewayBaseURL      string            `json:"gitlab_gateway_base_url,omitempty"`
	GitLabGatewayHeaders      map[string]string `json:"gitlab_gateway_headers,omitempty"`
	GitLabGatewayExpiresAt    time.Time         `json:"gitlab_gateway_expires_at,omitempty"`
	GitLabRateLimitName       string            `json:"gitlab_rate_limit_name,omitempty"`
	GitLabRateLimitLimit      int               `json:"gitlab_rate_limit_limit,omitempty"`
	GitLabRateLimitRemaining  int               `json:"gitlab_rate_limit_remaining,omitempty"`
	GitLabRateLimitResetAt    time.Time         `json:"gitlab_rate_limit_reset_at,omitempty"`
	GitLabQuotaExceededCount  int               `json:"gitlab_quota_exceeded_count,omitempty"`
	GitLabLastQuotaExceededAt time.Time         `json:"gitlab_last_quota_exceeded_at,omitempty"`
	RateLimitUntil            time.Time         `json:"rate_limit_until,omitempty"`
	LastRefresh               *time.Time        `json:"last_refresh,omitempty"`
	Disabled                  bool              `json:"disabled,omitempty"`
	Dead                      bool              `json:"dead,omitempty"`
	DeadSince                 *time.Time        `json:"dead_since,omitempty"`
	HealthStatus              string            `json:"health_status,omitempty"`
	HealthError               string            `json:"health_error,omitempty"`
	HealthCheckedAt           *time.Time        `json:"health_checked_at,omitempty"`
	LastHealthyAt             *time.Time        `json:"last_healthy_at,omitempty"`
}

// ClaudeOAuthData is the OAuth token structure from Claude Code.
type ClaudeOAuthData struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"` // Unix timestamp in milliseconds
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"` // pro, max, etc.
	RateLimitTier    string   `json:"rateLimitTier"`
}

func loadPool(dir string, registry *ProviderRegistry) ([]*Account, error) {
	var accs []*Account

	// Load accounts from provider subdirectories: pool/codex/, pool/claude/, pool/gemini/
	type providerDir struct {
		name        string
		accountType AccountType
	}
	providerDirs := []providerDir{
		{name: "codex", accountType: AccountTypeCodex},
		{name: "openai_api", accountType: AccountTypeCodex},
		{name: "claude", accountType: AccountTypeClaude},
		{name: "claude_gitlab", accountType: AccountTypeClaude},
		{name: "gemini", accountType: AccountTypeGemini},
		{name: "kimi", accountType: AccountTypeKimi},
		{name: "minimax", accountType: AccountTypeMinimax},
	}

	for _, spec := range providerDirs {
		providerDir := filepath.Join(dir, spec.name)
		entries, err := os.ReadDir(providerDir)
		if os.IsNotExist(err) {
			continue // Skip if provider directory doesn't exist
		}
		if err != nil {
			return nil, fmt.Errorf("read pool dir %s: %w", providerDir, err)
		}

		provider := registry.ForType(spec.accountType)
		if provider == nil {
			continue
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(providerDir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}

			acc, err := provider.LoadAccount(e.Name(), path, data)
			if err != nil {
				return nil, err
			}
			if acc != nil {
				normalizeLoadedDeadState(acc)
				now := time.Now().UTC()
				if shouldQuarantineAccount(acc, now) {
					if err := quarantineAccountFile(dir, acc, now); err != nil {
						return nil, fmt.Errorf("quarantine %s: %w", acc.File, err)
					}
					continue
				}
				accs = append(accs, acc)
			}
		}
	}

	return accs, nil
}

// Note: Individual account loading functions are now in the provider files:
// - provider_codex.go: CodexProvider.LoadAccount
// - provider_claude.go: ClaudeProvider.LoadAccount
// - provider_gemini.go: GeminiProvider.LoadAccount

type jwtClaims struct {
	ExpiresAt        time.Time
	ChatGPTAccountID string
	PlanType         string
}

func parseClaims(idToken string) jwtClaims {
	var out jwtClaims
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return out
	}
	payloadB64 := parts[1]
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return out
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return out
	}
	if exp, ok := payload["exp"].(float64); ok {
		out.ExpiresAt = time.Unix(int64(exp), 0)
	}
	// account id may live at top-level or under auth claim
	if acc, ok := payload["chatgpt_account_id"].(string); ok {
		out.ChatGPTAccountID = acc
	}
	if auth, ok := payload["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if acc, ok := auth["chatgpt_account_id"].(string); ok && acc != "" {
			out.ChatGPTAccountID = acc
		}
		if plan, ok := auth["chatgpt_plan_type"].(string); ok {
			out.PlanType = plan
		}
	}
	if out.PlanType == "" {
		out.PlanType = "pro"
	}
	return out
}

// poolState wraps accounts with a mutex.
type poolState struct {
	mu             sync.RWMutex
	accounts       []*Account
	convPin        map[string]string // conversation_id -> account ID
	activeCodexID  string
	activeAPIID    string
	activeGeminiID string
	debug          bool
	rr             uint64
	tierThreshold  float64 // secondary usage % at which we stop preferring a tier
}

type accountRuntimeState struct {
	Usage          UsageSnapshot
	Penalty        float64
	LastPenalty    time.Time
	LastUsed       time.Time
	RateLimitUntil time.Time
	Totals         AccountUsage
}

func newPoolState(accs []*Account, debug bool) *poolState {
	return &poolState{accounts: accs, convPin: map[string]string{}, debug: debug, tierThreshold: 0.15}
}

// replace swaps the pool accounts (used on reload).
func (p *poolState) replace(accs []*Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = accs
	p.convPin = map[string]string{}
	p.rr = 0
}

func (p *poolState) runtimeStateByID() map[string]accountRuntimeState {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state := make(map[string]accountRuntimeState, len(p.accounts))
	for _, a := range p.accounts {
		if a == nil {
			continue
		}
		a.mu.Lock()
		state[a.ID] = accountRuntimeState{
			Usage:          a.Usage,
			Penalty:        a.Penalty,
			LastPenalty:    a.LastPenalty,
			LastUsed:       a.LastUsed,
			RateLimitUntil: a.RateLimitUntil,
			Totals:         a.Totals,
		}
		a.mu.Unlock()
	}
	return state
}

func applyRuntimeState(accs []*Account, state map[string]accountRuntimeState) {
	for _, a := range accs {
		if a == nil {
			continue
		}
		prev, ok := state[a.ID]
		if !ok {
			continue
		}
		a.mu.Lock()
		a.Usage = mergeUsage(prev.Usage, a.Usage)
		a.Penalty = prev.Penalty
		a.LastPenalty = prev.LastPenalty
		a.LastUsed = prev.LastUsed
		if prev.RateLimitUntil.After(a.RateLimitUntil) {
			a.RateLimitUntil = prev.RateLimitUntil
		}
		a.Totals = prev.Totals
		a.mu.Unlock()
	}
}

func (p *poolState) count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.accounts)
}

// accountTier returns the preference tier for an account (1 = best, 2 = lesser).
// Tier 1: max for Claude, pro for Codex, ultra for Gemini
// Tier 2: everything else
func accountTier(accType AccountType, planType string) int {
	switch accType {
	case AccountTypeClaude:
		if planType == "max" {
			return 1
		}
		return 2
	case AccountTypeCodex:
		if planType == "pro" {
			return 1
		}
		return 2
	case AccountTypeGemini:
		if planType == "ultra" {
			return 1
		}
		return 2
	}
	return 2
}

// candidate selects the best account using tiered selection, optionally filtering by type.
// If accountType is empty, all account types are considered.
//
// Selection strategy:
//  1. Conversation pinning (stickiness) — only unpin at hard limits
//  2. Reuse the most recently used eligible account for new unpinned work
//  3. Split remaining eligible accounts into Tier 1 and Tier 2
//  4. If any Tier 1 account has secondary < tierThreshold → pick from Tier 1
//  5. Else if any Tier 2 account has secondary < tierThreshold → pick from Tier 2
//  6. Else → pick from all accounts with most headroom
//  7. Within a tier, use score as tiebreaker (headroom, drain urgency, recency, inflight)
func (p *poolState) candidate(conversationID string, exclude map[string]bool, accountType AccountType, requiredPlan string) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.candidateAtLocked(time.Now(), conversationID, exclude, accountType, requiredPlan, true)
}

func (p *poolState) peekCandidate(accountType AccountType, requiredPlan string) *Account {
	return p.peekCandidateAt(time.Now(), accountType, requiredPlan)
}

func (p *poolState) peekCandidateAt(now time.Time, accountType AccountType, requiredPlan string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.candidateAtLocked(now, "", nil, accountType, requiredPlan, false)
}

func (p *poolState) candidateAtLocked(now time.Time, conversationID string, exclude map[string]bool, accountType AccountType, requiredPlan string, advanceRR bool) *Account {
	if pinned := p.pinnedEligibleCandidateLocked(now, conversationID, exclude, accountType, requiredPlan); pinned != nil {
		return pinned
	}
	if sticky := p.stickyEligibleCandidateLocked(now, exclude, accountType, requiredPlan); sticky != nil {
		if advanceRR && len(exclude) == 0 {
			p.rememberSelectedSeatLocked(accountType, sticky)
		}
		return sticky
	}
	selected := p.selectEligibleCandidateLocked(now, exclude, accountType, requiredPlan, advanceRR)
	if advanceRR && len(exclude) == 0 {
		p.rememberSelectedSeatLocked(accountType, selected)
	}
	return selected
}

func (p *poolState) pinnedEligibleCandidateLocked(now time.Time, conversationID string, exclude map[string]bool, accountType AccountType, requiredPlan string) *Account {
	if conversationID == "" {
		return nil
	}
	id, ok := p.convPin[conversationID]
	if !ok {
		return nil
	}
	if exclude != nil && exclude[id] {
		return nil
	}
	a := p.getLocked(id)
	if a == nil {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	routing := routingStateLocked(a, now, accountType, requiredPlan)
	ok = routing.Eligible
	if ok && routing.CodexRateLimitBypass && p.debug {
		log.Printf("ignoring rate limit for codex account %s (until %s)",
			id, a.RateLimitUntil.Format(time.RFC3339))
	}
	if ok && !a.ExpiresAt.IsZero() && a.ExpiresAt.Before(now) {
		ok = false
		if p.debug {
			log.Printf("unpinning conversation %s from expired account %s",
				conversationID, id)
		}
	} else if !ok && p.debug {
		log.Printf(
			"unpinning conversation %s from account %s (%s, primary=%.0f%% secondary=%.0f%%)",
			conversationID,
			id,
			routing.BlockReason,
			routing.PrimaryUsed*100,
			routing.SecondaryUsed*100,
		)
	}
	if !ok {
		return nil
	}
	return a
}

func (p *poolState) stickyEligibleCandidateLocked(now time.Time, exclude map[string]bool, accountType AccountType, requiredPlan string) *Account {
	if accountType == AccountTypeCodex {
		if acc := p.activeCodexCandidateLocked(now, exclude, requiredPlan, false); acc != nil {
			return acc
		}
		if acc := p.stickyEligibleCandidateMatchingLocked(now, exclude, accountType, requiredPlan, func(a *Account) bool {
			return !isManagedCodexAPIKeyAccount(a)
		}); acc != nil {
			return acc
		}
		return p.stickyEligibleCandidateMatchingLocked(now, exclude, accountType, "", func(a *Account) bool {
			return isManagedCodexAPIKeyAccount(a)
		})
	}
	if accountType == AccountTypeGemini {
		if acc := p.activeGeminiCandidateLocked(now, exclude, requiredPlan); acc != nil {
			return acc
		}
	}
	return p.stickyEligibleCandidateMatchingLocked(now, exclude, accountType, requiredPlan, nil)
}

func (p *poolState) rememberSelectedSeatLocked(accountType AccountType, acc *Account) {
	if acc == nil {
		return
	}
	switch accountType {
	case AccountTypeCodex:
		if isManagedCodexAPIKeyAccount(acc) {
			p.activeAPIID = acc.ID
			return
		}
		p.activeCodexID = acc.ID
	case AccountTypeGemini:
		p.activeGeminiID = acc.ID
	}
}

func (p *poolState) activeGeminiCandidateLocked(now time.Time, exclude map[string]bool, requiredPlan string) *Account {
	if p.activeGeminiID == "" {
		return nil
	}
	if exclude != nil && exclude[p.activeGeminiID] {
		return nil
	}

	a := p.getLocked(p.activeGeminiID)
	if a == nil {
		p.activeGeminiID = ""
		return nil
	}

	a.mu.Lock()
	expired := !a.ExpiresAt.IsZero() && a.ExpiresAt.Before(now)
	routing := routingStateLocked(a, now, AccountTypeGemini, requiredPlan)
	ok := routing.Eligible && !expired
	a.mu.Unlock()
	if !ok {
		p.activeGeminiID = ""
		return nil
	}
	return a
}

func (p *poolState) activeCodexCandidateLocked(now time.Time, exclude map[string]bool, requiredPlan string, managed bool) *Account {
	activeID := p.activeCodexID
	if managed {
		activeID = p.activeAPIID
	}
	if activeID == "" {
		return nil
	}
	if exclude != nil && exclude[activeID] {
		return nil
	}

	a := p.getLocked(activeID)
	if a == nil {
		p.clearActiveCodexSeatLocked(managed)
		return nil
	}

	a.mu.Lock()
	expired := !a.ExpiresAt.IsZero() && a.ExpiresAt.Before(now)
	routing := routingStateLocked(a, now, AccountTypeCodex, requiredPlan)
	ok := routing.Eligible && !expired && (isManagedCodexAPIKeyAccount(a) == managed)
	a.mu.Unlock()
	if !ok {
		p.clearActiveCodexSeatLocked(managed)
		return nil
	}
	return a
}

func (p *poolState) clearActiveCodexSeatLocked(managed bool) {
	if managed {
		p.activeAPIID = ""
		return
	}
	p.activeCodexID = ""
}

func (p *poolState) stickyEligibleCandidateMatchingLocked(now time.Time, exclude map[string]bool, accountType AccountType, requiredPlan string, include func(*Account) bool) *Account {
	var sticky *Account
	var stickyLastUsed time.Time

	for _, a := range p.accounts {
		if include != nil && !include(a) {
			continue
		}
		if exclude != nil && exclude[a.ID] {
			continue
		}

		a.mu.Lock()
		lastUsed := a.LastUsed
		expired := !a.ExpiresAt.IsZero() && a.ExpiresAt.Before(now)
		routing := routingStateLocked(a, now, accountType, requiredPlan)
		ok := routing.Eligible && !expired && !lastUsed.IsZero()
		if ok && (sticky == nil || lastUsed.After(stickyLastUsed)) {
			sticky = a
			stickyLastUsed = lastUsed
		}
		a.mu.Unlock()
	}

	return sticky
}

func (p *poolState) selectEligibleCandidateLocked(now time.Time, exclude map[string]bool, accountType AccountType, requiredPlan string, advanceRR bool) *Account {
	if accountType == AccountTypeCodex {
		if acc := p.selectEligibleCandidateMatchingLocked(now, exclude, accountType, requiredPlan, advanceRR, func(a *Account) bool {
			return !isManagedCodexAPIKeyAccount(a)
		}); acc != nil {
			return acc
		}
		if acc := p.activeCodexCandidateLocked(now, exclude, "", true); acc != nil {
			return acc
		}
		return p.selectEligibleCandidateMatchingLocked(now, exclude, accountType, "", advanceRR, func(a *Account) bool {
			return isManagedCodexAPIKeyAccount(a)
		})
	}
	return p.selectEligibleCandidateMatchingLocked(now, exclude, accountType, requiredPlan, advanceRR, nil)
}

func (p *poolState) selectEligibleCandidateMatchingLocked(now time.Time, exclude map[string]bool, accountType AccountType, requiredPlan string, advanceRR bool, include func(*Account) bool) *Account {
	n := len(p.accounts)
	if n == 0 {
		return nil
	}

	type scoredAccount struct {
		acc          *Account
		tier         int
		secondaryPct float64
		score        float64
	}
	var eligible []scoredAccount

	start := int(p.rr % uint64(n))
	for i := 0; i < n; i++ {
		a := p.accounts[(start+i)%n]
		if include != nil && !include(a) {
			continue
		}
		if exclude != nil && exclude[a.ID] {
			continue
		}
		a.mu.Lock()
		routing := routingStateLocked(a, now, accountType, requiredPlan)
		if !routing.Eligible {
			if p.debug {
				log.Printf(
					"excluding account %s: %s (primary=%.1f%% secondary=%.1f%%)",
					a.ID,
					routing.BlockReason,
					routing.PrimaryUsed*100,
					routing.SecondaryUsed*100,
				)
			}
			a.mu.Unlock()
			continue
		}
		if routing.CodexRateLimitBypass && p.debug {
			log.Printf("ignoring rate limit for codex account %s (until %s)", a.ID, a.RateLimitUntil.Format(time.RFC3339))
		}
		tier := accountTier(a.Type, a.PlanType)
		score := scoreAccountLocked(a, now)
		a.mu.Unlock()
		score -= float64(atomic.LoadInt64(&a.Inflight)) * 0.02
		eligible = append(eligible, scoredAccount{acc: a, tier: tier, secondaryPct: routing.SecondaryUsed, score: score})
	}

	if len(eligible) == 0 {
		return nil
	}

	threshold := p.tierThreshold

	var bestTier1 *scoredAccount
	for i := range eligible {
		sa := &eligible[i]
		if sa.tier == 1 && sa.secondaryPct < threshold {
			if bestTier1 == nil || sa.score > bestTier1.score {
				bestTier1 = sa
			}
		}
	}
	if bestTier1 != nil {
		if advanceRR {
			p.rr++
		}
		return bestTier1.acc
	}

	var bestTier2 *scoredAccount
	for i := range eligible {
		sa := &eligible[i]
		if sa.tier == 2 && sa.secondaryPct < threshold {
			if bestTier2 == nil || sa.score > bestTier2.score {
				bestTier2 = sa
			}
		}
	}
	if bestTier2 != nil {
		if advanceRR {
			p.rr++
		}
		return bestTier2.acc
	}

	var bestAll *scoredAccount
	for i := range eligible {
		sa := &eligible[i]
		if bestAll == nil || sa.score > bestAll.score {
			bestAll = sa
		}
	}
	if bestAll != nil {
		if advanceRR {
			p.rr++
		}
		return bestAll.acc
	}
	return nil
}

func planMatchesRequired(planType, requiredPlan string) bool {
	if requiredPlan == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(planType), strings.TrimSpace(requiredPlan))
}

// countByType returns the number of accounts of a given type (or all if empty).
func (p *poolState) countByType(accountType AccountType) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if accountType == "" {
		return len(p.accounts)
	}
	count := 0
	for _, a := range p.accounts {
		if a.Type == accountType {
			count++
		}
	}
	return count
}

func scoreAccount(a *Account, now time.Time) float64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return scoreAccountLocked(a, now)
}

func scoreAccountLocked(a *Account, now time.Time) float64 {
	decayPenaltyLocked(a, now)
	primaryUsed, secondaryUsed := effectiveUsageForRouting(a.Usage, now)

	// Calculate headroom based on weekly (secondary) usage with drain urgency.
	// Key insight: an account at 51% with 1.5 days left can sustain 33%/day burn rate,
	// while an account at 4% with 7 days left can only sustain 14%/day to last the week.
	// We should prefer accounts that need draining (high urgency, available capacity).
	headroom := 1.0 - secondaryUsed

	// Calculate drain urgency based on time until reset
	// Accounts closer to reset should be used more aggressively
	if !a.Usage.SecondaryResetAt.IsZero() && a.Usage.SecondaryResetAt.After(now) {
		hoursRemaining := a.Usage.SecondaryResetAt.Sub(now).Hours()
		totalHours := 168.0 // 7 days

		// Available burn rate = remaining capacity / time remaining
		// e.g., 49% remaining / 37 hours = 1.32%/hour available
		// e.g., 96% remaining / 165 hours = 0.58%/hour available
		// The first can sustain 2.3x more load right now!

		if hoursRemaining > 1 && hoursRemaining < totalHours {
			// Calculate sustainable burn rate (% per hour)
			sustainableBurnRate := headroom / hoursRemaining

			// Baseline burn rate is 100% / 168 hours = 0.595%/hour
			baselineBurnRate := 1.0 / totalHours

			// Ratio of sustainable to baseline: >1 means we can use more than average
			burnRateRatio := sustainableBurnRate / baselineBurnRate

			// Higher cap for urgent drains (reset < 6 hours with significant headroom)
			// This ensures accounts about to reset get priority ("use it or lose it")
			maxMultiplier := 3.0
			if hoursRemaining < 6 && headroom > 0.1 {
				maxMultiplier = 8.0 // Much more aggressive when reset imminent
			}

			// Apply as multiplier to headroom, capped to reasonable range
			if burnRateRatio > maxMultiplier {
				burnRateRatio = maxMultiplier
			} else if burnRateRatio < 0.3 {
				burnRateRatio = 0.3
			}
			headroom *= burnRateRatio
		}
	}

	// 5hr window: bonus for accounts with more short-term capacity
	primaryPaceBonus := 0.0
	if !a.Usage.PrimaryResetAt.IsZero() && a.Usage.PrimaryResetAt.After(now) {
		hoursRemaining := a.Usage.PrimaryResetAt.Sub(now).Hours()
		primaryHeadroom := 1.0 - primaryUsed
		if hoursRemaining > 0.1 && hoursRemaining < 5.0 && primaryHeadroom > 0.1 {
			// Sustainable burn rate for 5hr window
			sustainableBurnRate := primaryHeadroom / hoursRemaining
			baselineBurnRate := 1.0 / 5.0 // 20%/hour baseline
			burnRateRatio := sustainableBurnRate / baselineBurnRate
			if burnRateRatio > 1.5 {
				primaryPaceBonus = 0.15 // Significant bonus for high 5hr capacity
			} else if burnRateRatio > 1.0 {
				primaryPaceBonus = 0.05 // Small bonus
			}
		}
	}
	headroom += primaryPaceBonus

	// 5hr usage only penalizes when getting critically high (>80%)
	// to avoid immediate rate limits
	if primaryUsed > 0.8 {
		primaryPenalty := (primaryUsed - 0.8) * 2.0 // Scales 0.8->1.0 to 0->0.4 penalty
		headroom -= primaryPenalty
	}

	// expiry risk - be gentle since access tokens often outlive ID token expiry.
	// Accounts that truly fail will get marked dead via 401/403 handling.
	if !a.ExpiresAt.IsZero() {
		ttl := a.ExpiresAt.Sub(now).Minutes()
		if ttl < 0 {
			headroom -= 0.3 // Expired but may still work - mild penalty
		} else if ttl < 30 {
			headroom -= 0.2
		} else if ttl < 60 {
			headroom -= 0.1
		}
	}
	// Reduce penalty effect when drain urgency is high
	// Penalties matter less when we need to drain capacity before reset
	penaltyFactor := 1.0
	if !a.Usage.SecondaryResetAt.IsZero() {
		hoursRemaining := a.Usage.SecondaryResetAt.Sub(now).Hours()
		secondaryHeadroom := 1.0 - secondaryUsed
		if hoursRemaining > 0 && hoursRemaining < 6 && secondaryHeadroom > 0.1 {
			penaltyFactor = 0.3 // Penalties matter less when draining urgently
		}
	}
	headroom -= a.Penalty * penaltyFactor
	if headroom < 0.01 {
		headroom = 0.01
	}

	// Recency bonus: accounts used in the last 5 minutes get a small bonus
	// to reduce swapping between accounts with similar scores (stickiness).
	if !a.LastUsed.IsZero() && now.Sub(a.LastUsed) < 5*time.Minute {
		headroom += 0.1
	}

	// credits bonuses
	creditBonus := 1.0
	if a.Usage.CreditsUnlimited || a.Usage.HasCredits {
		creditBonus = 1.1
	}

	return headroom * creditBonus
}

func (p *poolState) pin(conversationID, accountID string) {
	if conversationID == "" || accountID == "" {
		return
	}
	p.mu.Lock()
	p.convPin[conversationID] = accountID
	p.mu.Unlock()
}

// allAccounts returns a copy of all accounts for stats/reporting.
func (p *poolState) allAccounts() []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Account, len(p.accounts))
	copy(out, p.accounts)
	return out
}

// saveAccount persists the account back to its auth.json file.
func saveAccount(a *Account) error {
	if a == nil {
		return fmt.Errorf("nil account")
	}
	if strings.TrimSpace(a.File) == "" {
		return fmt.Errorf("account %s has empty file path", a.ID)
	}

	switch a.Type {
	case AccountTypeGemini:
		return saveGeminiAccount(a)
	case AccountTypeClaude:
		if isGitLabClaudeAccount(a) {
			return saveGitLabClaudeAccount(a)
		}
		return saveClaudeAccount(a)
	default:
		return saveCodexAccount(a)
	}
}

func saveCodexAccount(a *Account) error {
	// Preserve ALL fields in the original auth.json by modifying only token fields that
	// refresh updates. If we can't parse the existing file, fail closed to avoid
	// clobbering user-provided auth.json content.
	raw, err := os.ReadFile(a.File)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("parse %s: %w", a.File, err)
	}

	if isManagedCodexAPIKeyAccount(a) {
		root["OPENAI_API_KEY"] = a.AccessToken
		root["auth_mode"] = accountAuthModeAPIKey
		if strings.TrimSpace(a.PlanType) != "" {
			root["plan_type"] = strings.TrimSpace(a.PlanType)
		} else {
			root["plan_type"] = "api"
		}
		if !a.HealthCheckedAt.IsZero() {
			root["health_checked_at"] = a.HealthCheckedAt.UTC().Format(time.RFC3339Nano)
		}
		if strings.TrimSpace(a.HealthStatus) != "" {
			root["health_status"] = strings.TrimSpace(a.HealthStatus)
		} else {
			delete(root, "health_status")
		}
		if strings.TrimSpace(a.HealthError) != "" {
			root["health_error"] = strings.TrimSpace(a.HealthError)
		} else {
			delete(root, "health_error")
		}
		if !a.LastHealthyAt.IsZero() {
			root["last_healthy_at"] = a.LastHealthyAt.UTC().Format(time.RFC3339Nano)
		} else {
			delete(root, "last_healthy_at")
		}
		if a.Disabled {
			root["disabled"] = true
		} else {
			delete(root, "disabled")
		}
		if a.Dead {
			root["dead"] = true
		} else {
			delete(root, "dead")
		}
		setJSONTimeField(root, "dead_since", a.DeadSince)
		delete(root, "tokens")
		delete(root, "last_refresh")
		return atomicWriteJSON(a.File, root)
	}

	tokensAny := root["tokens"]
	tokens, ok := tokensAny.(map[string]any)
	if !ok || tokens == nil {
		tokens = map[string]any{}
		root["tokens"] = tokens
	}

	// Only update the minimum set of fields we own.
	if a.AccessToken != "" {
		tokens["access_token"] = a.AccessToken
	}
	if a.RefreshToken != "" {
		tokens["refresh_token"] = a.RefreshToken
	}
	if a.IDToken != "" {
		tokens["id_token"] = a.IDToken
	}

	// Preserve tokens.account_id unless it is missing and we have a value.
	if _, exists := tokens["account_id"]; !exists && strings.TrimSpace(a.AccountID) != "" {
		tokens["account_id"] = strings.TrimSpace(a.AccountID)
	}

	if !a.LastRefresh.IsZero() {
		root["last_refresh"] = a.LastRefresh.UTC().Format(time.RFC3339Nano)
	}

	// Persist dead flag so accounts stay dead across restarts
	if a.Dead {
		root["dead"] = true
	} else {
		delete(root, "dead")
	}
	setJSONTimeField(root, "dead_since", a.DeadSince)

	return atomicWriteJSON(a.File, root)
}

func saveGeminiAccount(a *Account) error {
	syncGeminiProviderTruthState(a)

	// Preserve existing fields in the file
	raw, err := os.ReadFile(a.File)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("parse %s: %w", a.File, err)
	}

	// Update token fields
	if a.AccessToken != "" {
		root["access_token"] = a.AccessToken
	}
	if a.RefreshToken != "" {
		root["refresh_token"] = a.RefreshToken
	}
	profileID := strings.TrimSpace(a.OAuthProfileID)
	rawClientID := strings.TrimSpace(a.OAuthClientID)
	rawClientSecret := strings.TrimSpace(a.OAuthClientSecret)
	if profileID != "" {
		root["oauth_profile_id"] = profileID
		delete(root, "client_id")
		delete(root, "client_secret")
	} else {
		delete(root, "oauth_profile_id")
		if rawClientID != "" {
			root["client_id"] = rawClientID
		} else {
			delete(root, "client_id")
		}
		if rawClientSecret != "" {
			root["client_secret"] = rawClientSecret
		} else {
			delete(root, "client_secret")
		}
	}
	operatorSource := storedGeminiOperatorSource(a.OperatorSource, a.OAuthProfileID, a.Type)
	if operatorSource != "" {
		root["operator_source"] = operatorSource
	} else {
		delete(root, "operator_source")
	}
	setJSONField(root, "operator_email", strings.TrimSpace(a.OperatorEmail), strings.TrimSpace(a.OperatorEmail) != "")
	setJSONField(root, "antigravity_source", strings.TrimSpace(a.AntigravitySource), strings.TrimSpace(a.AntigravitySource) != "")
	setJSONField(root, "antigravity_account_id", strings.TrimSpace(a.AntigravityAccountID), strings.TrimSpace(a.AntigravityAccountID) != "")
	setJSONField(root, "antigravity_email", strings.TrimSpace(a.AntigravityEmail), strings.TrimSpace(a.AntigravityEmail) != "")
	setJSONField(root, "antigravity_name", strings.TrimSpace(a.AntigravityName), strings.TrimSpace(a.AntigravityName) != "")
	setJSONField(root, "antigravity_project_id", strings.TrimSpace(a.AntigravityProjectID), strings.TrimSpace(a.AntigravityProjectID) != "")
	setJSONField(root, "antigravity_file", strings.TrimSpace(a.AntigravityFile), strings.TrimSpace(a.AntigravityFile) != "")
	setJSONField(root, "antigravity_current", true, a.AntigravityCurrent)
	setJSONField(root, "antigravity_proxy_disabled", true, a.AntigravityProxyDisabled)
	setJSONField(root, "antigravity_validation_blocked", true, a.AntigravityValidationBlocked)
	if len(a.AntigravityQuota) > 0 {
		root["antigravity_quota"] = a.AntigravityQuota
	} else {
		delete(root, "antigravity_quota")
	}
	setJSONField(root, "gemini_subscription_tier_id", strings.TrimSpace(a.GeminiSubscriptionTierID), strings.TrimSpace(a.GeminiSubscriptionTierID) != "")
	setJSONField(root, "gemini_subscription_tier_name", strings.TrimSpace(a.GeminiSubscriptionTierName), strings.TrimSpace(a.GeminiSubscriptionTierName) != "")
	setJSONField(root, "gemini_validation_reason_code", strings.TrimSpace(a.GeminiValidationReasonCode), strings.TrimSpace(a.GeminiValidationReasonCode) != "")
	setJSONField(root, "gemini_validation_message", strings.TrimSpace(a.GeminiValidationMessage), strings.TrimSpace(a.GeminiValidationMessage) != "")
	setJSONField(root, "gemini_validation_url", strings.TrimSpace(a.GeminiValidationURL), strings.TrimSpace(a.GeminiValidationURL) != "")
	setJSONTimeField(root, "gemini_provider_checked_at", a.GeminiProviderCheckedAt)
	providerTruthState := strings.TrimSpace(a.GeminiProviderTruthState)
	providerTruthReason := strings.TrimSpace(a.GeminiProviderTruthReason)
	setJSONField(root, "gemini_provider_truth_ready", a.GeminiProviderTruthReady, a.GeminiProviderTruthReady || providerTruthState != "" || providerTruthReason != "")
	setJSONField(root, "gemini_provider_truth_state", providerTruthState, providerTruthState != "")
	setJSONField(root, "gemini_provider_truth_reason", providerTruthReason, providerTruthReason != "")
	setJSONField(root, "gemini_operational_state", strings.TrimSpace(a.GeminiOperationalState), strings.TrimSpace(a.GeminiOperationalState) != "")
	setJSONField(root, "gemini_operational_reason", strings.TrimSpace(a.GeminiOperationalReason), strings.TrimSpace(a.GeminiOperationalReason) != "")
	setJSONField(root, "gemini_operational_source", strings.TrimSpace(a.GeminiOperationalSource), strings.TrimSpace(a.GeminiOperationalSource) != "")
	setJSONTimeField(root, "gemini_operational_checked_at", a.GeminiOperationalCheckedAt)
	setJSONTimeField(root, "gemini_operational_last_success_at", a.GeminiOperationalLastSuccessAt)
	geminiProtectedModels := normalizeStringSlice(a.GeminiProtectedModels)
	geminiQuotaModels := cloneGeminiModelQuotaSnapshots(a.GeminiQuotaModels)
	geminiModelForwardingRules := cloneStringMap(a.GeminiModelForwardingRules)
	setJSONField(root, "gemini_protected_models", geminiProtectedModels, len(geminiProtectedModels) > 0)
	setJSONField(root, "gemini_quota_models", geminiQuotaModels, len(geminiQuotaModels) > 0)
	setJSONTimeField(root, "gemini_quota_updated_at", a.GeminiQuotaUpdatedAt)
	setJSONField(root, "gemini_model_forwarding_rules", geminiModelForwardingRules, len(geminiModelForwardingRules) > 0)
	if !a.ExpiresAt.IsZero() {
		root["expiry_date"] = a.ExpiresAt.UnixMilli()
	}
	if !a.LastRefresh.IsZero() {
		root["last_refresh"] = a.LastRefresh.UTC().Format(time.RFC3339Nano)
	}
	setJSONField(root, "disabled", true, a.Disabled)
	setJSONField(root, "dead", true, a.Dead)
	setJSONTimeField(root, "dead_since", a.DeadSince)
	setJSONTimeField(root, "rate_limit_until", a.RateLimitUntil)
	setJSONField(root, "health_status", strings.TrimSpace(a.HealthStatus), strings.TrimSpace(a.HealthStatus) != "")
	setJSONField(root, "health_error", strings.TrimSpace(a.HealthError), strings.TrimSpace(a.HealthError) != "")
	setJSONTimeField(root, "health_checked_at", a.HealthCheckedAt)
	setJSONTimeField(root, "last_healthy_at", a.LastHealthyAt)

	return atomicWriteJSON(a.File, root)
}

func atomicWriteJSON(filePath string, data any) error {
	updated, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file then rename.
	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, "*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filePath)
}

// mergeUsage blends a newer usage snapshot with prior data, preserving meaningful
// fields that were absent or zeroed in the new payload.
func mergeUsage(prev, next UsageSnapshot) UsageSnapshot {
	res := next
	hardSource := res.Source == "body" || res.Source == "headers" || res.Source == "wham"
	authoritativeZero := res.Source == "claude-api" || res.Source == "wham"
	hardReset := res.PrimaryUsedPercent == 0 && res.SecondaryUsedPercent == 0
	referenceTime := res.RetrievedAt
	if referenceTime.IsZero() {
		referenceTime = time.Now()
	}

	if res.PrimaryUsedPercent == 0 && prev.PrimaryUsedPercent > 0 && !authoritativeZero && !(hardSource && hardReset) {
		res.PrimaryUsedPercent = prev.PrimaryUsedPercent
	}
	if res.SecondaryUsedPercent == 0 && prev.SecondaryUsedPercent > 0 && !authoritativeZero && !(hardSource && hardReset) {
		res.SecondaryUsedPercent = prev.SecondaryUsedPercent
	}
	if res.PrimaryUsed == 0 && prev.PrimaryUsed > 0 && !authoritativeZero && !(hardSource && hardReset) {
		res.PrimaryUsed = prev.PrimaryUsed
	}
	if res.SecondaryUsed == 0 && prev.SecondaryUsed > 0 && !authoritativeZero && !(hardSource && hardReset) {
		res.SecondaryUsed = prev.SecondaryUsed
	}
	if res.PrimaryWindowMinutes == 0 && prev.PrimaryWindowMinutes > 0 {
		res.PrimaryWindowMinutes = prev.PrimaryWindowMinutes
	}
	if res.SecondaryWindowMinutes == 0 && prev.SecondaryWindowMinutes > 0 {
		res.SecondaryWindowMinutes = prev.SecondaryWindowMinutes
	}
	if res.PrimaryResetAt.IsZero() && !prev.PrimaryResetAt.IsZero() && prev.PrimaryResetAt.After(referenceTime) {
		res.PrimaryResetAt = prev.PrimaryResetAt
	}
	if res.SecondaryResetAt.IsZero() && !prev.SecondaryResetAt.IsZero() && prev.SecondaryResetAt.After(referenceTime) {
		res.SecondaryResetAt = prev.SecondaryResetAt
	}
	if res.CreditsBalance == 0 && prev.CreditsBalance > 0 {
		res.CreditsBalance = prev.CreditsBalance
	}
	res.HasCredits = res.HasCredits || prev.HasCredits
	res.CreditsUnlimited = res.CreditsUnlimited || prev.CreditsUnlimited

	if res.RetrievedAt.IsZero() || (!prev.RetrievedAt.IsZero() && prev.RetrievedAt.After(res.RetrievedAt)) {
		res.RetrievedAt = prev.RetrievedAt
	}
	if res.Source == "" {
		res.Source = prev.Source
	}
	return res
}

func (p *poolState) getLocked(id string) *Account {
	for _, a := range p.accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (p *poolState) getByID(id string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.getLocked(id)
}

// averageUsage produces a synthetic usage payload across all alive accounts.
func (p *poolState) averageUsage() UsageSnapshot {
	return p.averageUsageByType("")
}

// averageUsageByType produces a synthetic usage payload for accounts of a specific type.
// If accountType is empty, averages across all accounts.
func (p *poolState) averageUsageByType(accountType AccountType) UsageSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var totalP, totalS float64
	var totalPW, totalSW float64
	var nP, nS float64
	var n float64
	var latestPrimaryReset, latestSecondaryReset time.Time
	for _, a := range p.accounts {
		if a.Dead {
			continue
		}
		if accountType != "" && a.Type != accountType {
			continue
		}
		usedP := a.Usage.PrimaryUsedPercent
		if usedP == 0 {
			usedP = a.Usage.PrimaryUsed
		}
		usedS := a.Usage.SecondaryUsedPercent
		if usedS == 0 {
			usedS = a.Usage.SecondaryUsed
		}
		totalP += usedP
		totalS += usedS
		n += 1
		if a.Usage.PrimaryWindowMinutes > 0 {
			totalPW += float64(a.Usage.PrimaryWindowMinutes)
			nP += 1
		}
		if a.Usage.SecondaryWindowMinutes > 0 {
			totalSW += float64(a.Usage.SecondaryWindowMinutes)
			nS += 1
		}
		// Track latest reset times
		if !a.Usage.PrimaryResetAt.IsZero() && a.Usage.PrimaryResetAt.After(latestPrimaryReset) {
			latestPrimaryReset = a.Usage.PrimaryResetAt
		}
		if !a.Usage.SecondaryResetAt.IsZero() && a.Usage.SecondaryResetAt.After(latestSecondaryReset) {
			latestSecondaryReset = a.Usage.SecondaryResetAt
		}
	}
	if n == 0 {
		return UsageSnapshot{}
	}
	return UsageSnapshot{
		PrimaryUsed:            totalP / n,
		SecondaryUsed:          totalS / n,
		PrimaryUsedPercent:     totalP / n,
		SecondaryUsedPercent:   totalS / n,
		PrimaryWindowMinutes:   int(totalPW / max(1, nP)),
		SecondaryWindowMinutes: int(totalSW / max(1, nS)),
		PrimaryResetAt:         latestPrimaryReset,
		SecondaryResetAt:       latestSecondaryReset,
		RetrievedAt:            time.Now(),
	}
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// PoolUtilization contains time-weighted utilization metrics for a provider or the whole pool.
type PoolUtilization struct {
	Provider                 string  `json:"provider"`
	TimeWeightedPrimaryPct   float64 `json:"time_weighted_primary_pct"`
	TimeWeightedSecondaryPct float64 `json:"time_weighted_secondary_pct"`
	AvailableAccounts        int     `json:"available_accounts"`
	TotalAccounts            int     `json:"total_accounts"`
	NextSecondaryResetIn     string  `json:"next_secondary_reset_in,omitempty"`
	ResetsIn24h              int     `json:"resets_in_24h"`
}

const (
	primaryWindowDuration   = 5 * time.Hour
	secondaryWindowDuration = 7 * 24 * time.Hour
)

// timeWeightedUsage produces a time-weighted usage snapshot across all alive accounts.
func (p *poolState) timeWeightedUsage() UsageSnapshot {
	return p.timeWeightedUsageByType("")
}

// timeWeightedUsageByType produces a time-weighted usage snapshot for accounts of a specific type.
// Instead of simple averaging, it weights each account's utilization by how much time remains
// until its window resets. An account at 80% that resets in 2 hours contributes almost nothing,
// while one at 80% that resets in 6 days contributes heavily.
//
// Formula: effective_util = used_pct × min(time_to_reset, window_length) / window_length
func (p *poolState) timeWeightedUsageByType(accountType AccountType) UsageSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	var totalEffP, totalEffS float64
	var totalPW, totalSW float64
	var nP, nS float64
	var n float64
	var earliestPrimaryReset, earliestSecondaryReset time.Time

	for _, a := range p.accounts {
		if a.Dead {
			continue
		}
		if accountType != "" && a.Type != accountType {
			continue
		}
		if a.Type == AccountTypeCodex && !codexAccountCountsAgainstQuota(a) {
			continue
		}
		usedP, usedS := effectiveUsageForRouting(a.Usage, now)

		// Compute time weight for primary window
		primaryWeight := 1.0 // default: no reset info, use full weight (conservative)
		if !a.Usage.PrimaryResetAt.IsZero() && a.Usage.PrimaryResetAt.After(now) {
			timeToReset := a.Usage.PrimaryResetAt.Sub(now)
			if timeToReset > primaryWindowDuration {
				timeToReset = primaryWindowDuration
			}
			primaryWeight = float64(timeToReset) / float64(primaryWindowDuration)
		}

		// Compute time weight for secondary window
		secondaryWeight := 1.0 // default: no reset info, use full weight (conservative)
		if !a.Usage.SecondaryResetAt.IsZero() && a.Usage.SecondaryResetAt.After(now) {
			timeToReset := a.Usage.SecondaryResetAt.Sub(now)
			if timeToReset > secondaryWindowDuration {
				timeToReset = secondaryWindowDuration
			}
			secondaryWeight = float64(timeToReset) / float64(secondaryWindowDuration)
		}

		totalEffP += usedP * primaryWeight
		totalEffS += usedS * secondaryWeight
		n += 1

		if a.Usage.PrimaryWindowMinutes > 0 {
			totalPW += float64(a.Usage.PrimaryWindowMinutes)
			nP += 1
		}
		if a.Usage.SecondaryWindowMinutes > 0 {
			totalSW += float64(a.Usage.SecondaryWindowMinutes)
			nS += 1
		}

		// Track earliest reset times (soonest capacity refill)
		if !a.Usage.PrimaryResetAt.IsZero() && a.Usage.PrimaryResetAt.After(now) {
			if earliestPrimaryReset.IsZero() || a.Usage.PrimaryResetAt.Before(earliestPrimaryReset) {
				earliestPrimaryReset = a.Usage.PrimaryResetAt
			}
		}
		if !a.Usage.SecondaryResetAt.IsZero() && a.Usage.SecondaryResetAt.After(now) {
			if earliestSecondaryReset.IsZero() || a.Usage.SecondaryResetAt.Before(earliestSecondaryReset) {
				earliestSecondaryReset = a.Usage.SecondaryResetAt
			}
		}
	}

	if n == 0 {
		return UsageSnapshot{}
	}
	return UsageSnapshot{
		PrimaryUsed:            totalEffP / n,
		SecondaryUsed:          totalEffS / n,
		PrimaryUsedPercent:     totalEffP / n,
		SecondaryUsedPercent:   totalEffS / n,
		PrimaryWindowMinutes:   int(totalPW / max(1, nP)),
		SecondaryWindowMinutes: int(totalSW / max(1, nS)),
		PrimaryResetAt:         earliestPrimaryReset,
		SecondaryResetAt:       earliestSecondaryReset,
		RetrievedAt:            now,
	}
}

// getPoolUtilization computes per-provider time-weighted utilization stats.
func (p *poolState) getPoolUtilization() []PoolUtilization {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	in24h := now.Add(24 * time.Hour)

	type provAccum struct {
		totalEffP, totalEffS   float64
		n                      float64
		available, total       int
		earliestSecondaryReset time.Time
		resetsIn24h            int
	}

	accums := map[AccountType]*provAccum{
		AccountTypeCodex:  {},
		AccountTypeClaude: {},
		AccountTypeGemini: {},
	}

	for _, a := range p.accounts {
		if a.Dead || a.Disabled {
			continue
		}
		if a.Type == AccountTypeCodex && !codexAccountCountsAgainstQuota(a) {
			continue
		}

		pa := accums[a.Type]
		if pa == nil {
			continue
		}
		pa.total++

		routing := routingStateLocked(a, now, "", "")
		usedP := routing.PrimaryUsed
		usedS := routing.SecondaryUsed

		if routing.Eligible {
			pa.available++
		}

		// Primary time weight
		primaryWeight := 1.0
		if !a.Usage.PrimaryResetAt.IsZero() && a.Usage.PrimaryResetAt.After(now) {
			ttr := a.Usage.PrimaryResetAt.Sub(now)
			if ttr > primaryWindowDuration {
				ttr = primaryWindowDuration
			}
			primaryWeight = float64(ttr) / float64(primaryWindowDuration)
		}

		// Secondary time weight
		secondaryWeight := 1.0
		if !a.Usage.SecondaryResetAt.IsZero() && a.Usage.SecondaryResetAt.After(now) {
			ttr := a.Usage.SecondaryResetAt.Sub(now)
			if ttr > secondaryWindowDuration {
				ttr = secondaryWindowDuration
			}
			secondaryWeight = float64(ttr) / float64(secondaryWindowDuration)

			if pa.earliestSecondaryReset.IsZero() || a.Usage.SecondaryResetAt.Before(pa.earliestSecondaryReset) {
				pa.earliestSecondaryReset = a.Usage.SecondaryResetAt
			}
			if a.Usage.SecondaryResetAt.Before(in24h) {
				pa.resetsIn24h++
			}
		}

		pa.totalEffP += usedP * primaryWeight
		pa.totalEffS += usedS * secondaryWeight
		pa.n++
	}

	var results []PoolUtilization
	for _, accType := range []AccountType{AccountTypeCodex, AccountTypeClaude, AccountTypeGemini} {
		pa := accums[accType]
		if pa.total == 0 {
			continue
		}

		pu := PoolUtilization{
			Provider:          string(accType),
			AvailableAccounts: pa.available,
			TotalAccounts:     pa.total,
			ResetsIn24h:       pa.resetsIn24h,
		}
		if pa.n > 0 {
			pu.TimeWeightedPrimaryPct = (pa.totalEffP / pa.n) * 100
			pu.TimeWeightedSecondaryPct = (pa.totalEffS / pa.n) * 100
		}
		if !pa.earliestSecondaryReset.IsZero() && pa.earliestSecondaryReset.After(now) {
			pu.NextSecondaryResetIn = formatDuration(pa.earliestSecondaryReset.Sub(now))
		}

		results = append(results, pu)
	}
	return results
}

func earliestReset(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.Before(b) {
		return a
	}
	return b
}

// UsagePoolStats contains aggregate stats about the pool for the usage endpoint.
type UsagePoolStats struct {
	TotalCount       int            `json:"total_count"`
	HealthyCount     int            `json:"healthy_count"`
	DeadCount        int            `json:"dead_count"`
	CodexCount       int            `json:"codex_count"`
	GeminiCount      int            `json:"gemini_count"`
	ClaudeCount      int            `json:"claude_count"`
	AvgPrimaryUsed   float64        `json:"avg_primary_used"`
	AvgSecondaryUsed float64        `json:"avg_secondary_used"`
	MinSecondaryUsed float64        `json:"min_secondary_used"`
	MaxSecondaryUsed float64        `json:"max_secondary_used"`
	Accounts         []AccountBrief `json:"accounts"`
	// Provider-specific usage summaries
	Providers *ProviderUsageSummary `json:"providers,omitempty"`
}

// ProviderUsageSummary contains usage summaries for each provider type.
type ProviderUsageSummary struct {
	Codex  *CodexUsageSummary  `json:"codex,omitempty"`
	Claude *ClaudeUsageSummary `json:"claude,omitempty"`
	Gemini *GeminiUsageSummary `json:"gemini,omitempty"`
}

// CodexUsageSummary contains Codex-specific usage info.
type CodexUsageSummary struct {
	HealthyCount int              `json:"healthy_count"`
	TotalCount   int              `json:"total_count"`
	FiveHour     UsageWindowStats `json:"five_hour"` // Primary window
	Weekly       UsageWindowStats `json:"weekly"`    // Secondary window
}

// ClaudeUsageSummary contains Claude-specific usage info.
type ClaudeUsageSummary struct {
	HealthyCount int              `json:"healthy_count"`
	TotalCount   int              `json:"total_count"`
	Tokens       UsageWindowStats `json:"tokens"`   // Token rate limit
	Requests     UsageWindowStats `json:"requests"` // Request rate limit
}

// GeminiUsageSummary contains Gemini-specific usage info.
type GeminiUsageSummary struct {
	HealthyCount int              `json:"healthy_count"`
	TotalCount   int              `json:"total_count"`
	Daily        UsageWindowStats `json:"daily"` // Daily usage
}

// UsageWindowStats contains stats for a usage window.
type UsageWindowStats struct {
	AvgUsedPct  float64   `json:"avg_used_pct"`
	MinUsedPct  float64   `json:"min_used_pct"`
	MaxUsedPct  float64   `json:"max_used_pct"`
	NextResetAt time.Time `json:"next_reset_at,omitempty"`
	WindowName  string    `json:"window_name,omitempty"` // e.g., "5 hours", "7 days", "24 hours"
}

// AccountBrief is a summary of an account for the usage endpoint.
type AccountBrief struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Plan         string  `json:"plan"`
	Status       string  `json:"status"` // "healthy", "dead", "disabled"
	PrimaryPct   int     `json:"primary_pct"`
	SecondaryPct int     `json:"secondary_pct"`
	Score        float64 `json:"score"`
	// Provider-specific labels for the percentages
	PrimaryLabel   string `json:"primary_label,omitempty"`   // e.g., "5hr tokens", "daily"
	SecondaryLabel string `json:"secondary_label,omitempty"` // e.g., "weekly", "requests"
}

// getPoolStats returns aggregate stats about the pool.
func (p *poolState) getPoolStats() UsagePoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	stats := UsagePoolStats{
		TotalCount:       len(p.accounts),
		MinSecondaryUsed: 1.0,
	}

	var totalP, totalS float64
	var healthyCount int

	// Provider-specific tracking
	type providerStats struct {
		total, healthy                       int
		primarySum, secondarySum             float64
		primaryMin, primaryMax               float64
		secondaryMin, secondaryMax           float64
		nextPrimaryReset, nextSecondaryReset time.Time
	}
	codexStats := providerStats{primaryMin: 1.0, secondaryMin: 1.0}
	claudeStats := providerStats{primaryMin: 1.0, secondaryMin: 1.0}
	geminiStats := providerStats{primaryMin: 1.0, secondaryMin: 1.0}

	for _, a := range p.accounts {
		a.mu.Lock()

		// Count by type
		switch a.Type {
		case AccountTypeCodex:
			stats.CodexCount++
		case AccountTypeGemini:
			stats.GeminiCount++
		case AccountTypeClaude:
			stats.ClaudeCount++
		}

		// Determine status
		status := "healthy"
		if a.Dead {
			status = "dead"
			stats.DeadCount++
		} else if a.Disabled {
			status = "disabled"
		} else {
			stats.HealthyCount++
		}

		primaryUsed, secondaryUsed := effectiveUsageForRouting(a.Usage, now)

		isHealthy := !a.Dead && !a.Disabled

		// Track min/max for healthy accounts
		if isHealthy {
			healthyCount++
			totalP += primaryUsed
			totalS += secondaryUsed
			if secondaryUsed < stats.MinSecondaryUsed {
				stats.MinSecondaryUsed = secondaryUsed
			}
			if secondaryUsed > stats.MaxSecondaryUsed {
				stats.MaxSecondaryUsed = secondaryUsed
			}
		}

		// Track provider-specific stats
		var ps *providerStats
		switch a.Type {
		case AccountTypeCodex:
			ps = &codexStats
		case AccountTypeClaude:
			ps = &claudeStats
		case AccountTypeGemini:
			ps = &geminiStats
		}
		if ps != nil {
			ps.total++
			if isHealthy {
				ps.healthy++
				ps.primarySum += primaryUsed
				ps.secondarySum += secondaryUsed
				if primaryUsed < ps.primaryMin {
					ps.primaryMin = primaryUsed
				}
				if primaryUsed > ps.primaryMax {
					ps.primaryMax = primaryUsed
				}
				if secondaryUsed < ps.secondaryMin {
					ps.secondaryMin = secondaryUsed
				}
				if secondaryUsed > ps.secondaryMax {
					ps.secondaryMax = secondaryUsed
				}
				// Track earliest reset times
				if !a.Usage.PrimaryResetAt.IsZero() && (ps.nextPrimaryReset.IsZero() || a.Usage.PrimaryResetAt.Before(ps.nextPrimaryReset)) {
					ps.nextPrimaryReset = a.Usage.PrimaryResetAt
				}
				if !a.Usage.SecondaryResetAt.IsZero() && (ps.nextSecondaryReset.IsZero() || a.Usage.SecondaryResetAt.Before(ps.nextSecondaryReset)) {
					ps.nextSecondaryReset = a.Usage.SecondaryResetAt
				}
			}
		}

		score := 0.0
		if isHealthy {
			score = scoreAccountLocked(a, now)
		}

		// Provider-specific labels
		var primaryLabel, secondaryLabel string
		switch a.Type {
		case AccountTypeCodex:
			primaryLabel = "5hr"
			secondaryLabel = "weekly"
		case AccountTypeClaude:
			primaryLabel = "tokens"
			secondaryLabel = "requests"
		case AccountTypeGemini:
			primaryLabel = "daily"
			secondaryLabel = ""
		}

		stats.Accounts = append(stats.Accounts, AccountBrief{
			ID:             a.ID,
			Type:           string(a.Type),
			Plan:           a.PlanType,
			Status:         status,
			PrimaryPct:     int(primaryUsed * 100),
			SecondaryPct:   int(secondaryUsed * 100),
			Score:          score,
			PrimaryLabel:   primaryLabel,
			SecondaryLabel: secondaryLabel,
		})

		a.mu.Unlock()
	}

	if healthyCount > 0 {
		stats.AvgPrimaryUsed = totalP / float64(healthyCount)
		stats.AvgSecondaryUsed = totalS / float64(healthyCount)
	}
	if stats.MinSecondaryUsed > stats.MaxSecondaryUsed {
		stats.MinSecondaryUsed = 0
	}

	// Build provider-specific summaries
	stats.Providers = &ProviderUsageSummary{}

	if codexStats.total > 0 {
		stats.Providers.Codex = &CodexUsageSummary{
			TotalCount:   codexStats.total,
			HealthyCount: codexStats.healthy,
			FiveHour: UsageWindowStats{
				WindowName:  "5 hours",
				MinUsedPct:  codexStats.primaryMin * 100,
				MaxUsedPct:  codexStats.primaryMax * 100,
				NextResetAt: codexStats.nextPrimaryReset,
			},
			Weekly: UsageWindowStats{
				WindowName:  "7 days",
				MinUsedPct:  codexStats.secondaryMin * 100,
				MaxUsedPct:  codexStats.secondaryMax * 100,
				NextResetAt: codexStats.nextSecondaryReset,
			},
		}
		if codexStats.healthy > 0 {
			stats.Providers.Codex.FiveHour.AvgUsedPct = (codexStats.primarySum / float64(codexStats.healthy)) * 100
			stats.Providers.Codex.Weekly.AvgUsedPct = (codexStats.secondarySum / float64(codexStats.healthy)) * 100
		}
	}

	if claudeStats.total > 0 {
		stats.Providers.Claude = &ClaudeUsageSummary{
			TotalCount:   claudeStats.total,
			HealthyCount: claudeStats.healthy,
			Tokens: UsageWindowStats{
				WindowName:  "tokens",
				MinUsedPct:  claudeStats.primaryMin * 100,
				MaxUsedPct:  claudeStats.primaryMax * 100,
				NextResetAt: claudeStats.nextPrimaryReset,
			},
			Requests: UsageWindowStats{
				WindowName:  "requests",
				MinUsedPct:  claudeStats.secondaryMin * 100,
				MaxUsedPct:  claudeStats.secondaryMax * 100,
				NextResetAt: claudeStats.nextSecondaryReset,
			},
		}
		if claudeStats.healthy > 0 {
			stats.Providers.Claude.Tokens.AvgUsedPct = (claudeStats.primarySum / float64(claudeStats.healthy)) * 100
			stats.Providers.Claude.Requests.AvgUsedPct = (claudeStats.secondarySum / float64(claudeStats.healthy)) * 100
		}
	}

	if geminiStats.total > 0 {
		stats.Providers.Gemini = &GeminiUsageSummary{
			TotalCount:   geminiStats.total,
			HealthyCount: geminiStats.healthy,
			Daily: UsageWindowStats{
				WindowName:  "24 hours",
				MinUsedPct:  geminiStats.primaryMin * 100,
				MaxUsedPct:  geminiStats.primaryMax * 100,
				NextResetAt: geminiStats.nextPrimaryReset,
			},
		}
		if geminiStats.healthy > 0 {
			stats.Providers.Gemini.Daily.AvgUsedPct = (geminiStats.primarySum / float64(geminiStats.healthy)) * 100
		}
	}

	return stats
}

// decayPenalty slowly reduces penalties over time to avoid permanent punishment.
func decayPenalty(a *Account, now time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	decayPenaltyLocked(a, now)
}

func decayPenaltyLocked(a *Account, now time.Time) {
	if a.LastPenalty.IsZero() {
		a.LastPenalty = now
		return
	}
	if now.Sub(a.LastPenalty) < 5*time.Minute {
		return
	}
	// decay 20% every 5 minutes.
	a.Penalty *= 0.8
	if a.Penalty < 0.01 {
		a.Penalty = 0
	}
	a.LastPenalty = now
}

func (p *poolState) debugf(format string, args ...any) {
	if p == nil || !p.debug {
		return
	}
	log.Printf(format, args...)
}
