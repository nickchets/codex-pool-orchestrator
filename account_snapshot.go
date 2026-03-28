package main

import (
	"strings"
	"sync/atomic"
	"time"
)

type accountSnapshot struct {
	ID                              string
	Type                            AccountType
	PlanType                        string
	AuthMode                        string
	RefreshToken                    string
	OAuthProfileID                  string
	OperatorSource                  string
	OperatorEmail                   string
	AccountID                       string
	IDToken                         string
	IDTokenChatGPTAccountID         string
	Disabled                        bool
	Dead                            bool
	Inflight                        int64
	ExpiresAt                       time.Time
	LastRefresh                     time.Time
	Penalty                         float64
	Score                           float64
	Routing                         routingState
	Usage                           UsageSnapshot
	Totals                          AccountUsage
	LastUsed                        time.Time
	RateLimitUntil                  time.Time
	DeadSince                       time.Time
	HealthStatus                    string
	HealthError                     string
	HealthCheckedAt                 time.Time
	LastHealthyAt                   time.Time
	AntigravityEmail                string
	AntigravityProjectID            string
	AntigravityCurrent              bool
	AntigravityProxyDisabled        bool
	AntigravityValidationBlocked    bool
	AntigravityQuotaForbidden       bool
	AntigravityQuotaForbiddenReason string
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
	GeminiModelRateLimitResetTimes  map[string]time.Time
	GitLabRateLimitName             string
	GitLabRateLimitLimit            int
	GitLabRateLimitRemaining        int
	GitLabRateLimitResetAt          time.Time
	GitLabQuotaExceededCount        int
	GitLabLastQuotaExceededAt       time.Time
	FallbackOnly                    bool
	GitLabClaude                    bool
}

func snapshotAccountState(a *Account, now time.Time, accountType AccountType, requiredPlan string) accountSnapshot {
	if a == nil {
		return accountSnapshot{}
	}

	inflight := atomic.LoadInt64(&a.Inflight)

	a.mu.Lock()
	defer a.mu.Unlock()
	syncGeminiProviderTruthStateLocked(a)
	pruneExpiredGeminiModelRateLimitResetTimesLocked(a, now)

	authMode := accountAuthMode(a)
	quotaModels := mergeGeminiQuotaModelsWithLiveRateLimitResetTimes(a.GeminiQuotaModels, a.GeminiModelRateLimitResetTimes, now)
	return accountSnapshot{
		ID:                              a.ID,
		Type:                            a.Type,
		PlanType:                        a.PlanType,
		AuthMode:                        authMode,
		RefreshToken:                    a.RefreshToken,
		OAuthProfileID:                  a.OAuthProfileID,
		OperatorSource:                  normalizeGeminiOperatorSource(a.OperatorSource, a.OAuthProfileID, a.Type),
		OperatorEmail:                   a.OperatorEmail,
		AccountID:                       a.AccountID,
		IDToken:                         a.IDToken,
		IDTokenChatGPTAccountID:         a.IDTokenChatGPTAccountID,
		Disabled:                        a.Disabled,
		Dead:                            a.Dead,
		Inflight:                        inflight,
		ExpiresAt:                       a.ExpiresAt,
		LastRefresh:                     a.LastRefresh,
		Penalty:                         a.Penalty,
		Score:                           scoreAccountLocked(a, now),
		Routing:                         routingStateLocked(a, now, accountType, requiredPlan),
		Usage:                           a.Usage,
		Totals:                          a.Totals,
		LastUsed:                        a.LastUsed,
		RateLimitUntil:                  a.RateLimitUntil,
		DeadSince:                       a.DeadSince,
		HealthStatus:                    a.HealthStatus,
		HealthError:                     a.HealthError,
		HealthCheckedAt:                 a.HealthCheckedAt,
		LastHealthyAt:                   a.LastHealthyAt,
		AntigravityEmail:                a.AntigravityEmail,
		AntigravityProjectID:            a.AntigravityProjectID,
		AntigravityCurrent:              a.AntigravityCurrent,
		AntigravityProxyDisabled:        a.AntigravityProxyDisabled,
		AntigravityValidationBlocked:    a.AntigravityValidationBlocked,
		AntigravityQuotaForbidden:       a.AntigravityQuotaForbidden,
		AntigravityQuotaForbiddenReason: a.AntigravityQuotaForbiddenReason,
		GeminiSubscriptionTierID:        a.GeminiSubscriptionTierID,
		GeminiSubscriptionTierName:      a.GeminiSubscriptionTierName,
		GeminiValidationReasonCode:      a.GeminiValidationReasonCode,
		GeminiValidationMessage:         a.GeminiValidationMessage,
		GeminiValidationURL:             a.GeminiValidationURL,
		GeminiProviderCheckedAt:         a.GeminiProviderCheckedAt,
		GeminiProviderTruthReady:        a.GeminiProviderTruthReady,
		GeminiProviderTruthState:        a.GeminiProviderTruthState,
		GeminiProviderTruthReason:       a.GeminiProviderTruthReason,
		GeminiOperationalState:          a.GeminiOperationalState,
		GeminiOperationalReason:         a.GeminiOperationalReason,
		GeminiOperationalSource:         a.GeminiOperationalSource,
		GeminiOperationalCheckedAt:      a.GeminiOperationalCheckedAt,
		GeminiOperationalLastSuccessAt:  a.GeminiOperationalLastSuccessAt,
		GeminiProtectedModels:           normalizeStringSlice(a.GeminiProtectedModels),
		GeminiQuotaModels:               quotaModels,
		GeminiQuotaUpdatedAt:            a.GeminiQuotaUpdatedAt,
		GeminiModelForwardingRules:      cloneStringMap(a.GeminiModelForwardingRules),
		GeminiModelRateLimitResetTimes:  cloneTimeMap(a.GeminiModelRateLimitResetTimes),
		GitLabRateLimitName:             a.GitLabRateLimitName,
		GitLabRateLimitLimit:            a.GitLabRateLimitLimit,
		GitLabRateLimitRemaining:        a.GitLabRateLimitRemaining,
		GitLabRateLimitResetAt:          a.GitLabRateLimitResetAt,
		GitLabQuotaExceededCount:        a.GitLabQuotaExceededCount,
		GitLabLastQuotaExceededAt:       a.GitLabLastQuotaExceededAt,
		FallbackOnly:                    a.Type == AccountTypeCodex && authMode == accountAuthModeAPIKey,
		GitLabClaude:                    a.Type == AccountTypeClaude && authMode == accountAuthModeGitLab,
	}
}

func poolIdentityForSnapshot(snapshot accountSnapshot) (codexJWTClaims, string, string) {
	if snapshot.FallbackOnly {
		return codexJWTClaims{}, "", firstNonEmpty(snapshot.ID, "openai_api")
	}
	claims := parseCodexClaims(snapshot.IDToken)
	workspaceID := firstNonEmpty(snapshot.AccountID, snapshot.IDTokenChatGPTAccountID, claims.ChatGPTAccountID)
	seatKey := seatKeyFor(claims, workspaceID, snapshot.ID)
	return claims, workspaceID, seatKey
}

func timePtrUTC(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func (h *proxyHandler) snapshotAccountByID(accountID string, now time.Time) (accountSnapshot, bool) {
	if h == nil || h.pool == nil || strings.TrimSpace(accountID) == "" {
		return accountSnapshot{}, false
	}

	h.pool.mu.RLock()
	var match *Account
	for _, candidate := range h.pool.accounts {
		if candidate != nil && candidate.ID == accountID {
			match = candidate
			break
		}
	}
	h.pool.mu.RUnlock()

	if match == nil {
		return accountSnapshot{}, false
	}
	return snapshotAccountState(match, now, "", ""), true
}
