package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	managedGeminiSubdir           = "gemini"
	managedGeminiProbeTimeout     = 8 * time.Second
	managedGeminiRateLimitWait    = 45 * time.Second
	managedGeminiOAuthAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	managedGeminiOAuthUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	managedGeminiOAuthSessionTTL  = 15 * time.Minute
	antigravityOAuthSessionTTL    = 15 * time.Minute
	antigravityOAuthCallbackPath  = "/oauth-callback"
	antigravityCodeAssistPollWait = 500 * time.Millisecond
	antigravityIDEVersion         = "4.1.30"
	antigravityIDEName            = "antigravity-insiders"
	antigravityUpdateChannel      = "stable"
	antigravityCodeAssistUA       = "Antigravity/" + antigravityIDEVersion + " (" + antigravityIDEName + ")"
)

var antigravityGeminiQuotaBaseURLs = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

var managedGeminiOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

var antigravityGeminiOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

type geminiSeatAddOutcome struct {
	AccountID           string
	Created             bool
	ProbeOK             bool
	ProbeError          string
	HealthStatus        string
	HealthError         string
	Dead                bool
	AuthExpiresAt       string
	ProviderTruthReady  bool
	ProviderTruthState  string
	ProviderTruthReason string
	ProviderProjectID   string
}

type managedGeminiOAuthSession struct {
	State        string
	CodeVerifier string
	RedirectURI  string
	ProfileID    string
	ClientID     string
	ClientSecret string
	CreatedAt    time.Time
}

type managedGeminiOAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

type managedGeminiOAuthUserInfo struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type antigravityGeminiOAuthSession struct {
	State       string
	RedirectURI string
	CreatedAt   time.Time
}

type antigravityCodeAssistMetadata struct {
	IdeType       string `json:"ideType,omitempty"`
	IdeVersion    string `json:"ideVersion,omitempty"`
	PluginVersion string `json:"pluginVersion,omitempty"`
	Platform      string `json:"platform,omitempty"`
	UpdateChannel string `json:"updateChannel,omitempty"`
	PluginType    string `json:"pluginType,omitempty"`
	IdeName       string `json:"ideName,omitempty"`
	DuetProject   string `json:"duetProject,omitempty"`
}

type antigravityLoadCodeAssistRequest struct {
	CloudaicompanionProject string                        `json:"cloudaicompanionProject,omitempty"`
	Metadata                antigravityCodeAssistMetadata `json:"metadata"`
	Mode                    string                        `json:"mode,omitempty"`
}

type antigravityTier struct {
	ID                                 string `json:"id,omitempty"`
	Name                               string `json:"name,omitempty"`
	IsDefault                          bool   `json:"isDefault,omitempty"`
	UserDefinedCloudaicompanionProject bool   `json:"userDefinedCloudaicompanionProject,omitempty"`
}

type antigravityIneligibleTier struct {
	ReasonCode    string `json:"reasonCode,omitempty"`
	ReasonMessage string `json:"reasonMessage,omitempty"`
	ValidationURL string `json:"validationUrl,omitempty"`
}

type antigravityLoadCodeAssistResponse struct {
	CurrentTier             *antigravityTier            `json:"currentTier,omitempty"`
	AllowedTiers            []antigravityTier           `json:"allowedTiers,omitempty"`
	IneligibleTiers         []antigravityIneligibleTier `json:"ineligibleTiers,omitempty"`
	CloudaicompanionProject string                      `json:"cloudaicompanionProject,omitempty"`
}

type antigravityOnboardUserRequest struct {
	TierID                  string                        `json:"tierId,omitempty"`
	CloudaicompanionProject string                        `json:"cloudaicompanionProject,omitempty"`
	Metadata                antigravityCodeAssistMetadata `json:"metadata,omitempty"`
}

type antigravityFetchAvailableModelsRequest struct {
	Project string `json:"project,omitempty"`
}

type antigravityOperationProject struct {
	ID string `json:"id,omitempty"`
}

type antigravityOnboardUserResponse struct {
	CloudaicompanionProject *antigravityOperationProject `json:"cloudaicompanionProject,omitempty"`
}

type antigravityOperationResponse struct {
	Name     string                          `json:"name,omitempty"`
	Done     bool                            `json:"done,omitempty"`
	Response *antigravityOnboardUserResponse `json:"response,omitempty"`
}

type geminiCodeAssistHTTPError struct {
	StatusCode int
	Status     string
	Message    string
}

type geminiCodeAssistErrorEnvelope struct {
	Error struct {
		Message string            `json:"message,omitempty"`
		Details []json.RawMessage `json:"details,omitempty"`
	} `json:"error,omitempty"`
}

type geminiCodeAssistErrorDetail struct {
	Type       string         `json:"@type,omitempty"`
	RetryDelay string         `json:"retryDelay,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

var (
	geminiCooldownTimestampRE    = regexp.MustCompile(`quotaResetTimeStamp"\s*:\s*"([^"]+)"`)
	geminiCooldownDelayRE        = regexp.MustCompile(`(?:quotaResetDelay|retryDelay)"\s*:\s*"([^"]+)"`)
	geminiCooldownMessageDelayRE = regexp.MustCompile(`reset after ([0-9]+(?:\.[0-9]+)?[a-z]+)`)
)

func (e *geminiCodeAssistHTTPError) Error() string {
	if e == nil {
		return ""
	}
	status := strings.TrimSpace(e.Status)
	message := strings.TrimSpace(e.Message)
	if status == "" && e.StatusCode > 0 {
		status = http.StatusText(e.StatusCode)
	}
	if status == "" {
		status = "request failed"
	}
	if message == "" {
		return "gemini code assist request failed: " + status
	}
	return fmt.Sprintf("gemini code assist request failed: %s: %s", status, message)
}

func geminiCodeAssistMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := metadata[strings.TrimSpace(key)]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		case fmt.Stringer:
			if trimmed := strings.TrimSpace(typed.String()); trimmed != "" {
				return trimmed
			}
		case json.Number:
			if trimmed := strings.TrimSpace(typed.String()); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func geminiCodeAssistCooldownInfo(err error, now time.Time) (time.Time, string, bool, bool) {
	var httpErr *geminiCodeAssistHTTPError
	if !errors.As(err, &httpErr) || httpErr == nil || httpErr.StatusCode != http.StatusTooManyRequests {
		return time.Time{}, "", false, false
	}

	reason := sanitizeStatusMessage(httpErr.Error())
	var envelope geminiCodeAssistErrorEnvelope
	precise := false
	if json.Unmarshal([]byte(httpErr.Message), &envelope) == nil {
		if parsedReason := sanitizeStatusMessage(envelope.Error.Message); parsedReason != "" {
			reason = parsedReason
		}
		var until time.Time
		for _, raw := range envelope.Error.Details {
			var detail geminiCodeAssistErrorDetail
			if json.Unmarshal(raw, &detail) != nil {
				continue
			}
			if resetAt := geminiCodeAssistMetadataString(detail.Metadata, "quotaResetTimeStamp"); resetAt != "" {
				if parsed, parseErr := time.Parse(time.RFC3339, resetAt); parseErr == nil && parsed.After(now) {
					until = parsed.UTC()
					precise = true
					break
				}
			}
			delay := firstNonEmpty(
				geminiCodeAssistMetadataString(detail.Metadata, "quotaResetDelay"),
				strings.TrimSpace(detail.RetryDelay),
			)
			if delay == "" {
				continue
			}
			if parsedDelay, parseErr := time.ParseDuration(delay); parseErr == nil && parsedDelay > 0 {
				candidate := now.Add(parsedDelay).UTC()
				if candidate.After(until) {
					until = candidate
					precise = true
				}
			}
		}
		if !until.IsZero() {
			return until, reason, precise, true
		}
	}

	if until, ok := geminiCodeAssistCooldownUntilFromText(httpErr.Message, now); ok {
		return until, reason, true, true
	}
	if until, ok := geminiCodeAssistCooldownUntilFromText(httpErr.Error(), now); ok {
		return until, reason, true, true
	}

	return now.Add(managedGeminiRateLimitWait).UTC(), reason, false, true
}

func geminiCodeAssistCooldownUntilFromText(text string, now time.Time) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}

	for _, match := range geminiCooldownTimestampRE.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(match[1]))
		if err == nil && parsed.After(now) {
			return parsed.UTC(), true
		}
	}

	var best time.Time
	for _, pattern := range []*regexp.Regexp{geminiCooldownDelayRE, geminiCooldownMessageDelayRE} {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			if len(match) < 2 {
				continue
			}
			parsedDelay, err := time.ParseDuration(strings.TrimSpace(match[1]))
			if err != nil || parsedDelay <= 0 {
				continue
			}
			candidate := now.Add(parsedDelay).UTC()
			if candidate.After(best) {
				best = candidate
			}
		}
	}
	if best.IsZero() {
		return time.Time{}, false
	}
	return best, true
}

type antigravityGeminiProviderTruth struct {
	ProjectID            string
	SubscriptionTierID   string
	SubscriptionTierName string
	ValidationReasonCode string
	ValidationMessage    string
	ValidationURL        string
	ProviderCheckedAt    time.Time
}

func hasAntigravityValidationTruth(truth antigravityGeminiProviderTruth) bool {
	return hasGeminiValidationTruth(truth.ValidationReasonCode, truth.ValidationMessage, truth.ValidationURL)
}

func antigravityGeminiProviderTruthTraceState(truth antigravityGeminiProviderTruth) string {
	if geminiValidationRestricted(false, truth.ValidationReasonCode, truth.ValidationMessage, truth.ValidationURL) {
		return geminiProviderTruthStateRestricted
	}
	if geminiValidationQuarantined(false, truth.ValidationReasonCode, truth.ValidationMessage, truth.ValidationURL) {
		return geminiProviderTruthStateValidationBlocked
	}
	return "ok"
}

func hasAntigravityProviderTruthMaterialized(truth antigravityGeminiProviderTruth) bool {
	return strings.TrimSpace(truth.ProjectID) != "" ||
		strings.TrimSpace(truth.SubscriptionTierID) != "" ||
		strings.TrimSpace(truth.SubscriptionTierName) != "" ||
		hasAntigravityValidationTruth(truth) ||
		!truth.ProviderCheckedAt.IsZero()
}

func applyAntigravityGeminiProviderTruthLocked(acc *Account, truth antigravityGeminiProviderTruth) {
	if acc == nil {
		return
	}
	if projectID := strings.TrimSpace(truth.ProjectID); projectID != "" {
		acc.AntigravityProjectID = projectID
	}
	acc.GeminiSubscriptionTierID = strings.TrimSpace(truth.SubscriptionTierID)
	acc.GeminiSubscriptionTierName = strings.TrimSpace(truth.SubscriptionTierName)
	acc.GeminiValidationReasonCode = strings.TrimSpace(truth.ValidationReasonCode)
	acc.GeminiValidationMessage = strings.TrimSpace(truth.ValidationMessage)
	acc.GeminiValidationURL = strings.TrimSpace(truth.ValidationURL)
	if !truth.ProviderCheckedAt.IsZero() {
		acc.GeminiProviderCheckedAt = truth.ProviderCheckedAt.UTC()
	}
	acc.AntigravityValidationBlocked = geminiValidationQuarantined(false, truth.ValidationReasonCode, truth.ValidationMessage, truth.ValidationURL)
	syncGeminiProviderTruthStateLocked(acc)
}

func applyAntigravityGeminiQuotaLocked(acc *Account, quota map[string]any) {
	if acc == nil || len(quota) == 0 {
		return
	}
	acc.AntigravityQuota = quota
	acc.AntigravityQuotaForbidden, acc.AntigravityQuotaForbiddenReason = antigravityQuotaDisposition(quota)
	acc.GeminiQuotaModels, acc.GeminiQuotaUpdatedAt, acc.GeminiModelForwardingRules, _ = decodeGeminiQuotaSnapshot(quota)
	syncGeminiProviderTruthStateLocked(acc)
}

func applyAntigravityGeminiQuotaSnapshotLocked(acc *Account, quota map[string]any, protectedModels []string) {
	if acc == nil {
		return
	}
	if len(quota) > 0 {
		applyAntigravityGeminiQuotaLocked(acc, quota)
	}
	if normalized := normalizeStringSlice(protectedModels); len(normalized) > 0 {
		acc.GeminiProtectedModels = normalized
	}
}

func applyAntigravityGeminiQuotaRefreshLocked(acc *Account, quota map[string]any, protectedModels []string, observedAt time.Time) {
	if acc == nil {
		return
	}
	if len(quota) > 0 || len(protectedModels) > 0 {
		applyAntigravityGeminiQuotaSnapshotLocked(acc, quota, protectedModels)
		return
	}
	acc.AntigravityQuota = nil
	acc.AntigravityQuotaForbidden = false
	acc.AntigravityQuotaForbiddenReason = ""
	acc.GeminiQuotaModels = nil
	acc.GeminiProtectedModels = nil
	acc.GeminiModelForwardingRules = nil
	if !observedAt.IsZero() {
		acc.GeminiQuotaUpdatedAt = observedAt.UTC()
	}
	syncGeminiProviderTruthStateLocked(acc)
}

func antigravityGeminiQuotaHydrationEligibleLocked(acc *Account) bool {
	if acc == nil || acc.Type != AccountTypeGemini || !isAntigravityGeminiSeat(acc) {
		return false
	}
	if strings.TrimSpace(acc.AccessToken) == "" {
		return false
	}
	if acc.AntigravityProxyDisabled ||
		geminiValidationQuarantined(acc.AntigravityValidationBlocked, acc.GeminiValidationReasonCode, acc.GeminiValidationMessage, acc.GeminiValidationURL) ||
		acc.AntigravityQuotaForbidden {
		return false
	}
	if !acc.GeminiProviderTruthReady && effectiveGeminiCodeAssistProjectID(acc) == "" {
		return false
	}
	return len(acc.AntigravityQuota) == 0 && len(acc.GeminiQuotaModels) == 0
}

func (h *proxyHandler) hydrateAntigravityGeminiQuotaForAccount(ctx context.Context, acc *Account) error {
	if h == nil || acc == nil {
		return nil
	}

	acc.mu.Lock()
	if !antigravityGeminiQuotaHydrationEligibleLocked(acc) {
		acc.mu.Unlock()
		return nil
	}
	accountID := strings.TrimSpace(acc.ID)
	accountEmail := firstNonEmpty(strings.TrimSpace(acc.OperatorEmail), strings.TrimSpace(acc.AntigravityEmail))
	accessToken := strings.TrimSpace(acc.AccessToken)
	projectID := strings.TrimSpace(acc.AntigravityProjectID)
	acc.mu.Unlock()

	quota, protectedModels, err := h.fetchAntigravityGeminiQuota(ctx, accessToken, projectID)
	if err != nil {
		return err
	}
	if len(quota) == 0 && len(protectedModels) == 0 {
		return nil
	}

	acc.mu.Lock()
	applyAntigravityGeminiQuotaSnapshotLocked(acc, quota, protectedModels)
	quotaModelCount := len(acc.GeminiQuotaModels)
	protectedModelCount := len(acc.GeminiProtectedModels)
	providerTruthState := strings.TrimSpace(acc.GeminiProviderTruthState)
	acc.mu.Unlock()

	if err := saveAccount(acc); err != nil {
		return err
	}

	log.Printf(
		"antigravity gemini quota hydrated for %s (%s): models=%d protected_models=%d state=%s",
		accountID,
		accountEmail,
		quotaModelCount,
		protectedModelCount,
		providerTruthState,
	)
	return nil
}

func (h *proxyHandler) hydrateMissingAntigravityGeminiQuotaOnStartup() {
	if h == nil || h.pool == nil {
		return
	}

	accounts := h.pool.allAccounts()
	targets := make([]*Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		acc.mu.Lock()
		eligible := antigravityGeminiQuotaHydrationEligibleLocked(acc)
		acc.mu.Unlock()
		if eligible {
			targets = append(targets, acc)
		}
	}
	if len(targets) == 0 {
		return
	}

	log.Printf("hydrating missing antigravity gemini quota for %d ready seats", len(targets))
	updated := 0
	for _, acc := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := h.hydrateAntigravityGeminiQuotaForAccount(ctx, acc)
		cancel()
		if err != nil {
			log.Printf("warning: failed to hydrate antigravity gemini quota for %s: %v", acc.ID, err)
			continue
		}
		updated++
	}
	if updated > 0 {
		h.reloadAccounts()
	}
}

const staleAntigravityGeminiTruthRefreshInterval = 10 * time.Minute

func staleAntigravityGeminiTruthRefreshEligibleLocked(acc *Account, now time.Time) bool {
	if acc == nil || acc.Type != AccountTypeGemini || !isAntigravityGeminiSeat(acc) {
		return false
	}
	if strings.TrimSpace(acc.AccessToken) == "" {
		return false
	}
	if acc.AntigravityProxyDisabled ||
		geminiValidationQuarantined(acc.AntigravityValidationBlocked, acc.GeminiValidationReasonCode, acc.GeminiValidationMessage, acc.GeminiValidationURL) ||
		acc.AntigravityQuotaForbidden {
		return false
	}
	syncGeminiProviderTruthStateLocked(acc)
	freshness := geminiProviderTruthFreshnessStatus(acc.GeminiProviderTruthState, acc.GeminiProviderCheckedAt, acc.GeminiQuotaUpdatedAt, now)
	if freshness.Stale {
		return true
	}
	if freshness.FreshUntil.IsZero() {
		return false
	}
	return !freshness.FreshUntil.After(now.Add(staleAntigravityGeminiTruthRefreshInterval))
}

func (h *proxyHandler) refreshStaleAntigravityGeminiTruthForAccount(ctx context.Context, acc *Account) error {
	if h == nil || acc == nil || acc.Type != AccountTypeGemini || !isAntigravityGeminiSeat(acc) {
		return nil
	}
	if !h.cfg.disableRefresh && h.needsRefresh(acc) {
		if err := h.refreshAccount(ctx, acc); err != nil {
			return err
		}
	}

	acc.mu.Lock()
	accountID := strings.TrimSpace(acc.ID)
	accountEmail := firstNonEmpty(strings.TrimSpace(acc.OperatorEmail), strings.TrimSpace(acc.AntigravityEmail))
	accessToken := strings.TrimSpace(acc.AccessToken)
	projectID := strings.TrimSpace(acc.AntigravityProjectID)
	acc.mu.Unlock()

	if accessToken == "" {
		return fmt.Errorf("stale gemini truth refresh %s has empty access token", accountID)
	}

	var (
		truth         antigravityGeminiProviderTruth
		providerErr   error
		quotaErr      error
		quota         map[string]any
		protectedList []string
	)
	if projectID != "" {
		loadRes, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, projectID, "")
		if err != nil {
			providerErr = err
		} else {
			truth = antigravityGeminiProviderTruthFromLoad(loadRes, projectID, time.Now().UTC())
		}
	}
	if projectID == "" || providerErr != nil {
		resolvedTruth, err := h.resolveAntigravityGeminiProviderTruth(ctx, accessToken)
		if err != nil && !hasAntigravityProviderTruthMaterialized(resolvedTruth) {
			return err
		}
		truth = resolvedTruth
		providerErr = err
	}

	acc.mu.Lock()
	applyAntigravityGeminiProviderTruthLocked(acc, truth)
	projectID = strings.TrimSpace(acc.AntigravityProjectID)
	acc.mu.Unlock()

	if projectID != "" {
		quotaObservedAt := time.Now().UTC()
		quota, protectedList, quotaErr = h.fetchAntigravityGeminiQuota(ctx, accessToken, projectID)
		if quotaErr == nil {
			acc.mu.Lock()
			applyAntigravityGeminiQuotaRefreshLocked(acc, quota, protectedList, quotaObservedAt)
			acc.mu.Unlock()
		}
	}

	if err := saveAccount(acc); err != nil {
		return err
	}

	if providerErr != nil {
		return providerErr
	}
	if quotaErr != nil {
		return quotaErr
	}
	quotaModels, _, _, _ := decodeGeminiQuotaSnapshot(quota)

	log.Printf(
		"antigravity gemini truth refreshed for %s (%s): project=%s quota_models=%d quota_keys=%d protected_models=%d",
		accountID,
		accountEmail,
		projectID,
		len(quotaModels),
		len(quota),
		len(protectedList),
	)
	return nil
}

func (h *proxyHandler) refreshStaleAntigravityGeminiTruthBatch(reason string) {
	if h == nil || h.pool == nil {
		return
	}

	now := time.Now().UTC()
	accounts := h.pool.allAccounts()
	targets := make([]*Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		acc.mu.Lock()
		eligible := staleAntigravityGeminiTruthRefreshEligibleLocked(acc, now)
		acc.mu.Unlock()
		if eligible {
			targets = append(targets, acc)
		}
	}
	if len(targets) == 0 {
		return
	}

	log.Printf("refreshing stale antigravity gemini truth for %d seats (%s)", len(targets), reason)
	for _, acc := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := h.refreshStaleAntigravityGeminiTruthForAccount(ctx, acc)
		cancel()
		if err != nil {
			log.Printf("warning: failed to refresh stale antigravity gemini truth for %s: %v", acc.ID, err)
		}
	}
}

func (h *proxyHandler) refreshStaleAntigravityGeminiTruthOnStartup() {
	h.refreshStaleAntigravityGeminiTruthBatch("startup")
}

func (h *proxyHandler) startStaleAntigravityGeminiTruthPoller() {
	if h == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(staleAntigravityGeminiTruthRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.refreshStaleAntigravityGeminiTruthBatch("poller")
		}
	}()
}

func managedGeminiProviderTruthProbeError(state, reason string) error {
	state = strings.TrimSpace(state)
	reason = strings.TrimSpace(reason)
	if state == "" || state == geminiProviderTruthStateReady {
		return nil
	}
	if reason == "" {
		reason = state
	}
	return fmt.Errorf("managed gemini seat provider truth not ready: %s: %s", state, reason)
}

func managedGeminiHealthStatusForProviderTruthState(state string) string {
	switch strings.TrimSpace(state) {
	case "", geminiProviderTruthStateReady:
		return "healthy"
	case geminiProviderTruthStateRestricted:
		return "restricted"
	case geminiProviderTruthStateValidationBlocked:
		return "validation_blocked"
	case geminiProviderTruthStateQuotaForbidden:
		return "quota_forbidden"
	case geminiProviderTruthStateProxyDisabled:
		return "proxy_disabled"
	case geminiProviderTruthStateMissingProjectID:
		return "missing_project_id"
	case geminiProviderTruthStateProjectOnlyUnverified:
		return "project_only_unverified"
	case geminiProviderTruthStateAuthOnly:
		return "auth_only"
	default:
		return strings.TrimSpace(state)
	}
}

var managedGeminiOAuthSessions = struct {
	sync.Mutex
	sessions map[string]*managedGeminiOAuthSession
}{
	sessions: make(map[string]*managedGeminiOAuthSession),
}

var antigravityGeminiOAuthSessions = struct {
	sync.Mutex
	sessions map[string]*antigravityGeminiOAuthSession
}{
	sessions: make(map[string]*antigravityGeminiOAuthSession),
}

func managedGeminiSeatID(refreshToken, accessToken string) string {
	seed := strings.TrimSpace(refreshToken)
	if seed == "" {
		seed = strings.TrimSpace(accessToken)
	}
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("gemini_seat_%x", sum[:6])
}

func saveGeminiSeatFromAuthJSON(poolDir, rawAuthJSON, operatorSource string) (*Account, bool, error) {
	payload := strings.TrimSpace(rawAuthJSON)
	if payload == "" {
		return nil, false, fmt.Errorf("auth_json is empty")
	}

	root, gj, normalizedSource, err := normalizeGeminiImportPayload(payload, operatorSource)
	if err != nil {
		return nil, false, err
	}

	dir := filepath.Join(poolDir, managedGeminiSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}

	accountID := managedGeminiSeatID(gj.RefreshToken, gj.AccessToken)
	path := filepath.Join(dir, accountID+".json")
	_, statErr := os.Stat(path)
	created := os.IsNotExist(statErr)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, false, statErr
	}

	root["auth_mode"] = accountAuthModeOAuth
	root["plan_type"] = firstNonEmpty(strings.TrimSpace(gj.PlanType), "gemini")
	root["operator_source"] = normalizeGeminiOperatorSource(normalizedSource, gj.OAuthProfileID, AccountTypeGemini)
	root["health_status"] = "unknown"
	if normalizedSource != geminiOperatorSourceAntigravityImport {
		delete(root, "disabled")
	}
	delete(root, "dead")
	delete(root, "rate_limit_until")
	delete(root, "last_refresh")
	delete(root, "health_checked_at")
	delete(root, "last_healthy_at")
	delete(root, "health_error")
	if err := atomicWriteJSON(path, root); err != nil {
		return nil, false, err
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	acc, err := (&GeminiProvider{}).LoadAccount(filepath.Base(path), path, updated)
	if err != nil {
		return nil, false, err
	}
	if acc == nil {
		return nil, false, fmt.Errorf("gemini seat could not be loaded after save")
	}
	acc.AuthMode = accountAuthModeOAuth
	acc.OperatorSource = normalizeGeminiOperatorSource(normalizedSource, acc.OAuthProfileID, AccountTypeGemini)
	return acc, created, nil
}

func primeImportedAntigravityGeminiSeat(acc *Account) error {
	if acc == nil || acc.Type != AccountTypeGemini || strings.TrimSpace(acc.AntigravitySource) == "" {
		return nil
	}

	now := time.Now().UTC()
	acc.mu.Lock()
	acc.AuthMode = accountAuthModeOAuth
	setAccountDeadStateLocked(acc, false, now)
	acc.HealthCheckedAt = now
	acc.HealthError = ""

	switch {
	case acc.Disabled:
		acc.HealthStatus = "disabled"
	case acc.AntigravityProxyDisabled:
		acc.HealthStatus = "proxy_disabled"
	case geminiValidationQuarantined(acc.AntigravityValidationBlocked, acc.GeminiValidationReasonCode, acc.GeminiValidationMessage, acc.GeminiValidationURL):
		acc.HealthStatus = "validation_blocked"
	case acc.AntigravityQuotaForbidden:
		acc.HealthStatus = "quota_forbidden"
		acc.HealthError = sanitizeStatusMessage(acc.AntigravityQuotaForbiddenReason)
	default:
		// Browser-added Antigravity seats can land with partial provider truth
		// (for example missing project_id) before any real request runs.
		status := managedGeminiHealthStatusForProviderTruthState(acc.GeminiProviderTruthState)
		if status == "" {
			status = "imported"
		}
		acc.HealthStatus = status
		if warmErr := managedGeminiProviderTruthProbeError(acc.GeminiProviderTruthState, acc.GeminiProviderTruthReason); warmErr != nil {
			acc.HealthError = sanitizeStatusMessage(warmErr.Error())
		}
	}
	syncGeminiProviderTruthStateLocked(acc)
	acc.mu.Unlock()
	return saveAccount(acc)
}

func (h *proxyHandler) probeManagedGeminiSeat(ctx context.Context, acc *Account) error {
	if h == nil || acc == nil || acc.Type != AccountTypeGemini {
		return nil
	}
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()

	provider := h.registry.ForType(AccountTypeGemini)
	if provider == nil {
		err := fmt.Errorf("no provider for account type %s", AccountTypeGemini)
		trace.noteProbe(AccountTypeGemini, acc, "error", time.Since(startedAt), err)
		return err
	}

	transport := h.operatorGeminiTransport()
	probeCtx, cancel := context.WithTimeout(ctx, managedGeminiProbeTimeout)
	defer cancel()

	err := provider.RefreshToken(probeCtx, acc, transport)
	now := time.Now().UTC()
	if err == nil {
		var (
			providerTruthErr error
			providerTruth    antigravityGeminiProviderTruth
		)
		acc.mu.Lock()
		accessToken := acc.AccessToken
		needsProviderTruth := strings.TrimSpace(acc.AntigravityProjectID) == "" || acc.GeminiProviderCheckedAt.IsZero()
		acc.mu.Unlock()
		if needsProviderTruth && strings.TrimSpace(accessToken) != "" {
			truth, resolveErr := h.resolveAntigravityGeminiProviderTruth(probeCtx, accessToken)
			providerTruth = truth
			if resolveErr != nil {
				providerTruthErr = resolveErr
				log.Printf("warning: managed gemini seat %s provider truth hydrate failed after refresh: %v", acc.ID, resolveErr)
			}
			acc.mu.Lock()
			applyAntigravityGeminiProviderTruthLocked(acc, truth)
			acc.mu.Unlock()
		}

		healthStatus := "healthy"
		var warmErr error
		acc.mu.Lock()
		acc.AuthMode = accountAuthModeOAuth
		setAccountDeadStateLocked(acc, false, now)
		acc.HealthCheckedAt = now
		acc.RateLimitUntil = time.Time{}
		syncGeminiProviderTruthStateLocked(acc)
		if providerTruthErr != nil && strings.TrimSpace(acc.GeminiProviderTruthReason) == "" {
			acc.GeminiProviderTruthReason = sanitizeStatusMessage(providerTruthErr.Error())
		}
		healthStatus = managedGeminiHealthStatusForProviderTruthState(acc.GeminiProviderTruthState)
		warmErr = managedGeminiProviderTruthProbeError(acc.GeminiProviderTruthState, acc.GeminiProviderTruthReason)
		acc.HealthStatus = healthStatus
		if warmErr == nil {
			acc.HealthError = ""
			acc.LastHealthyAt = now
		} else {
			acc.HealthError = sanitizeStatusMessage(warmErr.Error())
		}
		acc.mu.Unlock()
		if saveErr := saveAccount(acc); saveErr != nil {
			log.Printf("warning: failed to persist managed gemini seat %s probe success: %v", acc.ID, saveErr)
		}
		traceErr := warmErr
		if traceErr == nil && providerTruthErr != nil && strings.TrimSpace(providerTruth.ProjectID) == "" && !hasAntigravityValidationTruth(providerTruth) {
			traceErr = providerTruthErr
		}
		trace.noteProbe(AccountTypeGemini, acc, healthStatus, time.Since(startedAt), traceErr)
		return warmErr
	}

	msg := strings.ToLower(err.Error())
	healthStatus := "error"
	acc.mu.Lock()
	acc.AuthMode = accountAuthModeOAuth
	setAccountDeadStateLocked(acc, false, now)
	acc.HealthCheckedAt = now
	acc.HealthError = sanitizeStatusMessage(err.Error())
	switch {
	case isRateLimitError(err):
		acc.HealthStatus = "rate_limited"
		until := now.Add(managedGeminiRateLimitWait)
		if acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until
		}
	case strings.Contains(msg, "invalid_grant"),
		strings.Contains(msg, "refresh_token_reused"),
		strings.Contains(msg, "gemini refresh unauthorized"),
		strings.Contains(msg, "no refresh token"):
		setAccountDeadStateLocked(acc, true, now)
		acc.HealthStatus = "dead"
		acc.RateLimitUntil = time.Time{}
	default:
		acc.HealthStatus = "error"
	}
	healthStatus = firstNonEmpty(strings.TrimSpace(acc.HealthStatus), healthStatus)
	acc.mu.Unlock()

	if saveErr := saveAccount(acc); saveErr != nil {
		log.Printf("warning: failed to persist managed gemini seat %s probe failure: %v", acc.ID, saveErr)
	}
	probeErr := fmt.Errorf("managed gemini seat probe failed: %w", err)
	trace.noteProbe(AccountTypeGemini, acc, healthStatus, time.Since(startedAt), probeErr)
	return probeErr
}

func (h *proxyHandler) addGeminiSeatFromAuthJSON(ctx context.Context, rawAuthJSON string) (*geminiSeatAddOutcome, error) {
	acc, created, err := saveGeminiSeatFromAuthJSON(h.cfg.poolDir, rawAuthJSON, geminiOperatorSourceManualImport)
	if err != nil {
		return nil, err
	}
	trace := requestTraceFromContext(ctx)
	probeStartedAt := time.Now()

	probeErr := error(nil)
	if strings.TrimSpace(acc.AntigravitySource) != "" {
		probeErr = primeImportedAntigravityGeminiSeat(acc)
		acc.mu.Lock()
		healthStatus := firstNonEmpty(strings.TrimSpace(acc.HealthStatus), "imported")
		acc.mu.Unlock()
		trace.noteProbe(AccountTypeGemini, acc, healthStatus, time.Since(probeStartedAt), probeErr)
	} else {
		probeErr = h.probeManagedGeminiSeat(ctx, acc)
	}
	if probeErr != nil {
		log.Printf("gemini seat %s probe failed during add: %v", acc.ID, probeErr)
	}

	h.reloadAccounts()

	live, liveOK := h.snapshotAccountByID(acc.ID, time.Now())
	outcome := &geminiSeatAddOutcome{
		AccountID: acc.ID,
		Created:   created,
		ProbeOK:   probeErr == nil,
		ProbeError: sanitizeStatusMessage(func() string {
			if probeErr == nil {
				return ""
			}
			return probeErr.Error()
		}()),
		HealthStatus:        firstNonEmpty(strings.TrimSpace(acc.HealthStatus), "unknown"),
		HealthError:         sanitizeStatusMessage(acc.HealthError),
		Dead:                acc.Dead,
		ProviderTruthReady:  acc.GeminiProviderTruthReady,
		ProviderTruthState:  strings.TrimSpace(acc.GeminiProviderTruthState),
		ProviderTruthReason: sanitizeStatusMessage(acc.GeminiProviderTruthReason),
		ProviderProjectID:   strings.TrimSpace(acc.AntigravityProjectID),
	}
	if liveOK {
		outcome.HealthStatus = firstNonEmpty(strings.TrimSpace(live.HealthStatus), "unknown")
		outcome.HealthError = sanitizeStatusMessage(live.HealthError)
		outcome.Dead = live.Dead
		outcome.ProviderTruthReady = live.GeminiProviderTruthReady
		outcome.ProviderTruthState = strings.TrimSpace(live.GeminiProviderTruthState)
		outcome.ProviderTruthReason = sanitizeStatusMessage(live.GeminiProviderTruthReason)
		outcome.ProviderProjectID = strings.TrimSpace(live.AntigravityProjectID)
		if !live.ExpiresAt.IsZero() {
			outcome.AuthExpiresAt = live.ExpiresAt.UTC().Format(time.RFC3339)
		}
	} else if !acc.ExpiresAt.IsZero() {
		outcome.AuthExpiresAt = acc.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return outcome, nil
}

func generateGeminiOAuthState() (string, error) {
	stateBytes := make([]byte, 24)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate gemini oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}

func antigravityGeminiRedirectURI(r *http.Request) (string, error) {
	if r == nil {
		return "", fmt.Errorf("request is required")
	}
	host := strings.TrimSpace(r.Host)
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("loopback host required for Gemini Browser Auth")
	}
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		if r.TLS != nil {
			port = "443"
		} else {
			port = "80"
		}
	}
	port = strings.TrimSpace(port)
	if port == "" {
		return "", fmt.Errorf("loopback port required for Gemini Browser Auth")
	}
	return "http://localhost:" + port + antigravityOAuthCallbackPath, nil
}

func buildAntigravityGeminiOAuthURL(redirectURI, state string) (string, error) {
	if strings.TrimSpace(redirectURI) == "" {
		return "", fmt.Errorf("redirect uri is required")
	}
	u, err := url.Parse(managedGeminiOAuthAuthURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", geminiOAuthAntigravityClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("access_type", "offline")
	q.Set("scope", strings.Join(antigravityGeminiOAuthScopes, " "))
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func storeAntigravityGeminiOAuthSession(session *antigravityGeminiOAuthSession) {
	if session == nil || strings.TrimSpace(session.State) == "" {
		return
	}
	antigravityGeminiOAuthSessions.Lock()
	antigravityGeminiOAuthSessions.sessions[session.State] = session
	antigravityGeminiOAuthSessions.Unlock()
}

func claimAntigravityGeminiOAuthSession(state string) (*antigravityGeminiOAuthSession, bool) {
	key := strings.TrimSpace(state)
	if key == "" {
		return nil, false
	}
	antigravityGeminiOAuthSessions.Lock()
	defer antigravityGeminiOAuthSessions.Unlock()
	session, ok := antigravityGeminiOAuthSessions.sessions[key]
	if !ok || session == nil {
		return nil, false
	}
	delete(antigravityGeminiOAuthSessions.sessions, key)
	if session.CreatedAt.IsZero() || session.CreatedAt.Before(time.Now().Add(-antigravityOAuthSessionTTL)) {
		return nil, false
	}
	return session, true
}

func cleanupAntigravityGeminiOAuthSessions() {
	cutoff := time.Now().Add(-antigravityOAuthSessionTTL)
	antigravityGeminiOAuthSessions.Lock()
	for state, session := range antigravityGeminiOAuthSessions.sessions {
		if session == nil || session.CreatedAt.Before(cutoff) {
			delete(antigravityGeminiOAuthSessions.sessions, state)
		}
	}
	antigravityGeminiOAuthSessions.Unlock()
}

func buildAntigravityCodeAssistMetadata(projectID string) antigravityCodeAssistMetadata {
	metadata := antigravityCodeAssistMetadata{
		IdeType:       "ANTIGRAVITY",
		IdeVersion:    antigravityIDEVersion,
		PluginVersion: antigravityIDEVersion,
		Platform:      antigravityCodeAssistPlatform(),
		UpdateChannel: antigravityUpdateChannel,
		PluginType:    "GEMINI",
		IdeName:       antigravityIDEName,
	}
	if strings.TrimSpace(projectID) != "" {
		metadata.DuetProject = strings.TrimSpace(projectID)
	}
	return metadata
}

func antigravityCodeAssistPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return "DARWIN_AMD64"
		case "arm64":
			return "DARWIN_ARM64"
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "LINUX_AMD64"
		case "arm64":
			return "LINUX_ARM64"
		}
	case "windows":
		if runtime.GOARCH == "amd64" {
			return "WINDOWS_AMD64"
		}
	}
	return "PLATFORM_UNSPECIFIED"
}

func (h *proxyHandler) geminiCodeAssistBaseURL() *url.URL {
	if h != nil && h.cfg.geminiBase != nil {
		return h.cfg.geminiBase
	}
	if h != nil && h.registry != nil {
		if provider, ok := h.registry.ForType(AccountTypeGemini).(*GeminiProvider); ok && provider != nil && provider.geminiBase != nil {
			return provider.geminiBase
		}
	}
	base, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	return base
}

func antigravityGeminiCodeAssistBaseCandidates(primary *url.URL) []*url.URL {
	knownQuotaBase := make(map[string]struct{}, len(antigravityGeminiQuotaBaseURLs))
	for _, raw := range antigravityGeminiQuotaBaseURLs {
		if parsed, err := url.Parse(strings.TrimSpace(raw)); err == nil && parsed != nil {
			knownQuotaBase[strings.TrimSpace(parsed.String())] = struct{}{}
		}
	}

	seen := make(map[string]struct{})
	var bases []*url.URL
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		base, err := url.Parse(raw)
		if err != nil || base == nil {
			return
		}
		key := strings.TrimSpace(base.String())
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		bases = append(bases, base)
	}

	if primary != nil {
		if _, ok := knownQuotaBase[strings.TrimSpace(primary.String())]; !ok {
			add(primary.String())
		}
	}
	for _, raw := range antigravityGeminiQuotaBaseURLs {
		add(raw)
	}
	if primary != nil {
		add(primary.String())
	}
	return bases
}

func (h *proxyHandler) doGeminiCodeAssistJSONWithBase(ctx context.Context, base *url.URL, method, requestPath, accessToken string, body any, out any) error {
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("access token is required")
	}
	if base == nil {
		base = h.geminiCodeAssistBaseURL()
	}
	target := *base
	target.Path = singleJoin(base.Path, requestPath)

	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", antigravityCodeAssistUA)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.operatorGeminiTransport().RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return &geminiCodeAssistHTTPError{
			StatusCode: resp.StatusCode,
			Status:     strings.TrimSpace(resp.Status),
			Message:    safeText(msg),
		}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (h *proxyHandler) doGeminiCodeAssistJSON(ctx context.Context, method, requestPath, accessToken string, body any, out any) error {
	return h.doGeminiCodeAssistJSONWithBase(ctx, h.geminiCodeAssistBaseURL(), method, requestPath, accessToken, body, out)
}

func (h *proxyHandler) loadAntigravityGeminiCodeAssist(ctx context.Context, accessToken, projectID, mode string) (*antigravityLoadCodeAssistResponse, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	req := antigravityLoadCodeAssistRequest{
		CloudaicompanionProject: strings.TrimSpace(projectID),
		Metadata:                buildAntigravityCodeAssistMetadata(projectID),
	}
	if strings.TrimSpace(mode) != "" {
		req.Mode = strings.TrimSpace(mode)
	}
	var resp antigravityLoadCodeAssistResponse
	if err := h.doGeminiCodeAssistJSON(ctx, http.MethodPost, "/v1internal:loadCodeAssist", accessToken, req, &resp); err != nil {
		trace.noteProviderTruth(AccountTypeGemini, "load_code_assist", "fail", projectID, "", "", time.Since(startedAt), err)
		return nil, err
	}
	trace.noteProviderTruth(AccountTypeGemini, "load_code_assist", "ok", firstNonEmpty(strings.TrimSpace(resp.CloudaicompanionProject), strings.TrimSpace(projectID)), func() string {
		if resp.CurrentTier == nil {
			return ""
		}
		return strings.TrimSpace(resp.CurrentTier.ID)
	}(), func() string {
		for _, tier := range resp.IneligibleTiers {
			if reason := strings.TrimSpace(tier.ReasonCode); reason != "" {
				return reason
			}
		}
		return ""
	}(), time.Since(startedAt), nil)
	return &resp, nil
}

func antigravityQuotaField(raw map[string]any, keys ...string) any {
	if len(raw) == 0 {
		return nil
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if value, ok := raw[key]; ok {
			return value
		}
	}
	return nil
}

func antigravityQuotaString(raw map[string]any, keys ...string) string {
	value := antigravityQuotaField(raw, keys...)
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func antigravityQuotaBool(raw map[string]any, keys ...string) (bool, bool) {
	value := antigravityQuotaField(raw, keys...)
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func antigravityQuotaInt(raw map[string]any, keys ...string) (int, bool) {
	value := antigravityQuotaField(raw, keys...)
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func antigravityQuotaFloat(raw map[string]any, keys ...string) (float64, bool) {
	value := antigravityQuotaField(raw, keys...)
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed, true
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func antigravityQuotaUnix(raw map[string]any, keys ...string) (int64, bool) {
	value := antigravityQuotaField(raw, keys...)
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed, true
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return parsed, true
		}
		if parsedTime, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return parsedTime.UTC().Unix(), true
		}
	}
	return 0, false
}

func antigravityQuotaStringMap(raw map[string]any, keys ...string) map[string]string {
	value := antigravityQuotaField(raw, keys...)
	items, _ := value.(map[string]any)
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for key, item := range items {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		switch typed := item.(type) {
		case string:
			typed = strings.TrimSpace(typed)
			if typed != "" {
				out[key] = typed
			}
		default:
			str := strings.TrimSpace(fmt.Sprintf("%v", typed))
			if str != "" {
				out[key] = str
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func antigravityQuotaBoolMap(raw map[string]any, keys ...string) map[string]bool {
	value := antigravityQuotaField(raw, keys...)
	items, _ := value.(map[string]any)
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]bool, len(items))
	for key, item := range items {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		switch typed := item.(type) {
		case bool:
			out[key] = typed
		case string:
			out[key] = strings.EqualFold(strings.TrimSpace(typed), "true")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func antigravityQuotaModelAllowed(name string) bool {
	return geminiQuotaModelAllowedInOperatorTruth(name)
}

func antigravityQuotaModelName(nameHint string, entry map[string]any) string {
	candidates := []string{
		strings.TrimSpace(nameHint),
		antigravityQuotaString(entry, "name"),
		antigravityQuotaString(entry, "model"),
		antigravityQuotaString(entry, "id"),
	}
	for _, candidate := range candidates {
		if antigravityQuotaModelAllowed(candidate) {
			return candidate
		}
	}
	return firstNonEmpty(candidates...)
}

func normalizeAntigravityGeminiQuotaModelEntry(nameHint string, entry map[string]any) (GeminiModelQuotaSnapshot, bool) {
	if len(entry) == 0 {
		return GeminiModelQuotaSnapshot{}, false
	}
	model := GeminiModelQuotaSnapshot{
		Name:        antigravityQuotaModelName(nameHint, entry),
		ResetTime:   antigravityQuotaString(entry, "reset_time", "resetTime"),
		DisplayName: antigravityQuotaString(entry, "display_name", "displayName"),
	}
	model.RouteProvider = geminiQuotaModelRouteProvider(model.Name)
	if percentage, ok := antigravityQuotaInt(entry, "percentage"); ok {
		model.Percentage = percentage
	} else if quotaInfo, _ := antigravityQuotaField(entry, "quotaInfo", "quota_info").(map[string]any); len(quotaInfo) > 0 {
		if remainingFraction, ok := antigravityQuotaFloat(quotaInfo, "remainingFraction", "remaining_fraction"); ok {
			model.Percentage = int(remainingFraction * 100.0)
		}
		if strings.TrimSpace(model.ResetTime) == "" {
			model.ResetTime = antigravityQuotaString(quotaInfo, "resetTime", "reset_time")
		}
	}
	if supportsImages, ok := antigravityQuotaBool(entry, "supports_images", "supportsImages"); ok {
		model.SupportsImages = supportsImages
	}
	if supportsThinking, ok := antigravityQuotaBool(entry, "supports_thinking", "supportsThinking"); ok {
		model.SupportsThinking = supportsThinking
	}
	if thinkingBudget, ok := antigravityQuotaInt(entry, "thinking_budget", "thinkingBudget"); ok {
		model.ThinkingBudget = thinkingBudget
	}
	if recommended, ok := antigravityQuotaBool(entry, "recommended", "isRecommended"); ok {
		model.Recommended = recommended
	}
	if maxTokens, ok := antigravityQuotaInt(entry, "max_tokens", "maxTokens"); ok {
		model.MaxTokens = maxTokens
	}
	if maxOutputTokens, ok := antigravityQuotaInt(entry, "max_output_tokens", "maxOutputTokens"); ok {
		model.MaxOutputTokens = maxOutputTokens
	}
	if supportedMimeTypes := antigravityQuotaBoolMap(entry, "supported_mime_types", "supportedMimeTypes"); len(supportedMimeTypes) > 0 {
		model.SupportedMimeTypes = supportedMimeTypes
	}
	if strings.TrimSpace(model.Name) == "" || !antigravityQuotaModelAllowed(model.Name) {
		return GeminiModelQuotaSnapshot{}, false
	}
	return model, true
}

func normalizeAntigravityGeminiQuotaModels(raw any) []GeminiModelQuotaSnapshot {
	var models []GeminiModelQuotaSnapshot
	switch typed := raw.(type) {
	case []any:
		models = make([]GeminiModelQuotaSnapshot, 0, len(typed))
		for _, item := range typed {
			entry, _ := item.(map[string]any)
			if model, ok := normalizeAntigravityGeminiQuotaModelEntry("", entry); ok {
				models = append(models, model)
			}
		}
	case map[string]any:
		models = make([]GeminiModelQuotaSnapshot, 0, len(typed))
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			item := typed[name]
			entry, _ := item.(map[string]any)
			if model, ok := normalizeAntigravityGeminiQuotaModelEntry(name, entry); ok {
				models = append(models, model)
			}
		}
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Name == models[j].Name {
			return models[i].DisplayName < models[j].DisplayName
		}
		return models[i].Name < models[j].Name
	})
	return cloneGeminiModelQuotaSnapshots(models)
}

func normalizeAntigravityGeminiQuotaForwardingRules(raw any) map[string]string {
	items, _ := raw.(map[string]any)
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for key, item := range items {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		switch typed := item.(type) {
		case string:
			if value := strings.TrimSpace(typed); value != "" {
				out[key] = value
			}
		case map[string]any:
			if value := antigravityQuotaString(typed, "newModelId", "new_model_id", "model", "id"); value != "" {
				out[key] = value
			}
		default:
			if value := strings.TrimSpace(fmt.Sprintf("%v", typed)); value != "" {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAntigravityGeminiQuotaPayload(raw map[string]any, fetchedAt time.Time) (map[string]any, []string) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]any)
	if forbidden, ok := antigravityQuotaBool(raw, "is_forbidden", "isForbidden"); ok {
		out["is_forbidden"] = forbidden
	}
	if reason := antigravityQuotaString(raw, "forbidden_reason", "forbiddenReason"); reason != "" {
		out["forbidden_reason"] = reason
	}
	if updatedAt, ok := antigravityQuotaUnix(raw, "last_updated", "lastUpdated"); ok && updatedAt > 0 {
		out["last_updated"] = updatedAt
	}
	if subscriptionTier := antigravityQuotaString(raw, "subscription_tier", "subscriptionTier"); subscriptionTier != "" {
		out["subscription_tier"] = subscriptionTier
	}
	if rules := normalizeAntigravityGeminiQuotaForwardingRules(antigravityQuotaField(raw, "deprecatedModelIds", "deprecated_model_ids", "model_forwarding_rules", "modelForwardingRules")); len(rules) > 0 {
		out["model_forwarding_rules"] = rules
	}
	if models := normalizeAntigravityGeminiQuotaModels(antigravityQuotaField(raw, "models", "availableModels", "available_models")); len(models) > 0 {
		out["models"] = models
	}
	protectedModels := normalizeStringSliceFromAny(antigravityQuotaField(raw, "protected_models", "protectedModels"))
	if _, ok := out["last_updated"]; !ok && !fetchedAt.IsZero() {
		if len(out) > 0 || len(protectedModels) > 0 {
			out["last_updated"] = fetchedAt.UTC().Unix()
		}
	}
	if len(out) == 0 {
		out = nil
	}
	return out, protectedModels
}

func (h *proxyHandler) fetchAntigravityGeminiQuota(ctx context.Context, accessToken, projectID string) (map[string]any, []string, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	trimmedProjectID := strings.TrimSpace(projectID)
	requests := []antigravityFetchAvailableModelsRequest{{
		Project: trimmedProjectID,
	}}
	if trimmedProjectID != "" {
		requests = append(requests, antigravityFetchAvailableModelsRequest{})
	}

	bases := antigravityGeminiCodeAssistBaseCandidates(h.geminiCodeAssistBaseURL())
	var lastErr error
	var forbiddenQuota map[string]any
	var forbiddenProtectedModels []string
	for idx, base := range bases {
		for reqIdx, req := range requests {
			fetchedAt := time.Now().UTC()
			var raw map[string]any
			err := h.doGeminiCodeAssistJSONWithBase(ctx, base, http.MethodPost, "/v1internal:fetchAvailableModels", accessToken, req, &raw)
			if err != nil {
				var httpErr *geminiCodeAssistHTTPError
				if errors.As(err, &httpErr) {
					if httpErr.StatusCode == http.StatusForbidden {
						normalizedQuota, protectedModels := normalizeAntigravityGeminiQuotaPayload(map[string]any{
							"is_forbidden":     true,
							"forbidden_reason": strings.TrimSpace(httpErr.Message),
						}, fetchedAt)
						trace.noteProviderTruth(AccountTypeGemini, "fetch_available_models", "forbidden", req.Project, "", "", time.Since(startedAt), fmt.Errorf("%s: %w", base.String(), err))
						forbiddenQuota = normalizedQuota
						forbiddenProtectedModels = protectedModels
						if trimmedProjectID != "" && strings.TrimSpace(req.Project) != "" && reqIdx+1 < len(requests) {
							continue
						}
						return normalizedQuota, protectedModels, nil
					}
					if httpErr.StatusCode != http.StatusTooManyRequests && httpErr.StatusCode < 500 {
						trace.noteProviderTruth(AccountTypeGemini, "fetch_available_models", "fail", req.Project, "", "", time.Since(startedAt), fmt.Errorf("%s: %w", base.String(), err))
						if trimmedProjectID != "" && strings.TrimSpace(req.Project) != "" && reqIdx+1 < len(requests) {
							lastErr = err
							continue
						}
						return nil, nil, err
					}
				}
				lastErr = err
				trace.noteProviderTruth(AccountTypeGemini, "fetch_available_models", "fail", req.Project, "", "", time.Since(startedAt), fmt.Errorf("%s: %w", base.String(), err))
				if idx+1 >= len(bases) && reqIdx+1 >= len(requests) {
					break
				}
				continue
			}
			if raw == nil {
				raw = map[string]any{}
			}
			normalizedQuota, protectedModels := normalizeAntigravityGeminiQuotaPayload(raw, fetchedAt)
			if len(normalizedQuota) == 0 && len(protectedModels) == 0 && trimmedProjectID != "" && strings.TrimSpace(req.Project) != "" && reqIdx+1 < len(requests) {
				trace.noteProviderTruth(AccountTypeGemini, "fetch_available_models", "retry_without_project", req.Project, "", "", time.Since(startedAt), nil)
				continue
			}
			trace.noteProviderTruth(AccountTypeGemini, "fetch_available_models", "ok", req.Project, "", "", time.Since(startedAt), nil)
			return normalizedQuota, protectedModels, nil
		}
	}
	if len(forbiddenQuota) > 0 || len(forbiddenProtectedModels) > 0 {
		return forbiddenQuota, forbiddenProtectedModels, nil
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, fmt.Errorf("fetch available models returned no result")
}

func antigravityOperationProjectID(op *antigravityOperationResponse) string {
	if op == nil || op.Response == nil || op.Response.CloudaicompanionProject == nil {
		return ""
	}
	return strings.TrimSpace(op.Response.CloudaicompanionProject.ID)
}

func antigravityLoadValidationMessage(res *antigravityLoadCodeAssistResponse) string {
	if res == nil {
		return ""
	}
	for _, tier := range res.IneligibleTiers {
		msg := strings.TrimSpace(tier.ReasonMessage)
		link := strings.TrimSpace(tier.ValidationURL)
		if msg == "" && link == "" {
			continue
		}
		if link != "" {
			if msg == "" {
				return "Gemini account validation is required: " + link
			}
			return msg + ": " + link
		}
		return msg
	}
	return ""
}

func antigravityGeminiProviderTruthFromLoad(res *antigravityLoadCodeAssistResponse, fallbackProjectID string, checkedAt time.Time) antigravityGeminiProviderTruth {
	truth := antigravityGeminiProviderTruth{
		ProjectID:         strings.TrimSpace(fallbackProjectID),
		ProviderCheckedAt: checkedAt.UTC(),
	}
	if res == nil {
		return truth
	}
	if projectID := strings.TrimSpace(res.CloudaicompanionProject); projectID != "" {
		truth.ProjectID = projectID
	}
	if res.CurrentTier != nil {
		truth.SubscriptionTierID = strings.TrimSpace(res.CurrentTier.ID)
		truth.SubscriptionTierName = strings.TrimSpace(res.CurrentTier.Name)
	}
	providerProjectReady := res != nil && strings.TrimSpace(res.CloudaicompanionProject) != ""
	usableTierReady := res.CurrentTier != nil &&
		(strings.TrimSpace(res.CurrentTier.ID) != "" || strings.TrimSpace(res.CurrentTier.Name) != "")
	if !providerProjectReady && !usableTierReady {
		for _, tier := range res.IneligibleTiers {
			reasonCode := strings.TrimSpace(tier.ReasonCode)
			reasonMessage := strings.TrimSpace(tier.ReasonMessage)
			validationURL := strings.TrimSpace(tier.ValidationURL)
			if reasonCode == "" && reasonMessage == "" && validationURL == "" {
				continue
			}
			truth.ValidationReasonCode = reasonCode
			truth.ValidationMessage = reasonMessage
			truth.ValidationURL = validationURL
			break
		}
	}
	return truth
}

func (h *proxyHandler) maybeLoadAntigravityGeminiFallbackProject(ctx context.Context, accessToken string, res *antigravityLoadCodeAssistResponse) (*antigravityLoadCodeAssistResponse, error, bool) {
	if h == nil {
		return res, nil, false
	}
	if strings.TrimSpace(accessToken) == "" {
		return res, nil, false
	}
	if res != nil && strings.TrimSpace(res.CloudaicompanionProject) != "" {
		return res, nil, false
	}
	fallbackProjectID := strings.TrimSpace(antigravityGeminiFallbackProject)
	if fallbackProjectID == "" {
		return res, nil, false
	}
	fallbackRes, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, fallbackProjectID, "")
	if err != nil {
		return nil, err, true
	}
	return fallbackRes, nil, true
}

func antigravityOnboardTierCandidates(res *antigravityLoadCodeAssistResponse) []string {
	seen := make(map[string]struct{})
	var tiers []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		tiers = append(tiers, id)
	}
	if res != nil {
		for _, tier := range res.AllowedTiers {
			if tier.IsDefault {
				add(tier.ID)
			}
		}
		if res.CurrentTier != nil {
			add(res.CurrentTier.ID)
		}
		for _, tier := range res.AllowedTiers {
			add(tier.ID)
		}
	}
	add("standard-tier")
	add("free-tier")
	return tiers
}

func (h *proxyHandler) onboardAntigravityGeminiCodeAssist(ctx context.Context, accessToken, tierID string) (*antigravityOperationResponse, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	req := antigravityOnboardUserRequest{
		TierID:   strings.TrimSpace(tierID),
		Metadata: buildAntigravityCodeAssistMetadata(""),
	}
	var op antigravityOperationResponse
	if err := h.doGeminiCodeAssistJSON(ctx, http.MethodPost, "/v1internal:onboardUser", accessToken, req, &op); err != nil {
		trace.noteProviderTruth(AccountTypeGemini, "onboard", "fail", "", tierID, "", time.Since(startedAt), err)
		return nil, err
	}
	for !op.Done && strings.TrimSpace(op.Name) != "" {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			trace.noteProviderTruth(AccountTypeGemini, "onboard", "fail", antigravityOperationProjectID(&op), tierID, "", time.Since(startedAt), err)
			return nil, err
		case <-time.After(antigravityCodeAssistPollWait):
		}
		if err := h.doGeminiCodeAssistJSON(ctx, http.MethodGet, "/v1internal/"+strings.TrimPrefix(strings.TrimSpace(op.Name), "/"), accessToken, nil, &op); err != nil {
			trace.noteProviderTruth(AccountTypeGemini, "onboard", "fail", antigravityOperationProjectID(&op), tierID, "", time.Since(startedAt), err)
			return nil, err
		}
	}
	trace.noteProviderTruth(AccountTypeGemini, "onboard", "ok", antigravityOperationProjectID(&op), tierID, "", time.Since(startedAt), nil)
	return &op, nil
}

func (h *proxyHandler) resolveAntigravityGeminiProviderTruth(ctx context.Context, accessToken string) (antigravityGeminiProviderTruth, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	var (
		lastErr error
		tried   = make(map[string]struct{})
	)

	// Mirror the Antigravity browser onboarding flow: bootstrap a tier first,
	// then hydrate the Cloud Code project via loadCodeAssist.
	for _, tierID := range antigravityOnboardTierCandidates(nil) {
		tierID = strings.TrimSpace(tierID)
		if tierID == "" {
			continue
		}
		if _, ok := tried[tierID]; ok {
			continue
		}
		tried[tierID] = struct{}{}
		op, err := h.onboardAntigravityGeminiCodeAssist(ctx, accessToken, tierID)
		if err != nil {
			lastErr = err
			continue
		}
		if projectID := antigravityOperationProjectID(op); projectID != "" {
			loadRes, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, projectID, "")
			if err != nil {
				truth := antigravityGeminiProviderTruth{ProjectID: projectID}
				trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
				return truth, nil
			}
			truth := antigravityGeminiProviderTruthFromLoad(loadRes, projectID, time.Now().UTC())
			trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
			return truth, nil
		}
		reloaded, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, "", "")
		if err != nil {
			lastErr = err
			continue
		}
		if projectID := strings.TrimSpace(reloaded.CloudaicompanionProject); projectID != "" {
			truth := antigravityGeminiProviderTruthFromLoad(reloaded, projectID, time.Now().UTC())
			trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
			return truth, nil
		}
		if fallbackRes, fallbackErr, usedFallback := h.maybeLoadAntigravityGeminiFallbackProject(ctx, accessToken, reloaded); usedFallback {
			if fallbackErr == nil {
				reloaded = fallbackRes
				truth := antigravityGeminiProviderTruthFromLoad(reloaded, antigravityGeminiFallbackProject, time.Now().UTC())
				if message := antigravityLoadValidationMessage(reloaded); message != "" {
					validationErr := fmt.Errorf("%s", message)
					trace.noteProviderTruth(AccountTypeGemini, "resolve", antigravityGeminiProviderTruthTraceState(truth), truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), validationErr)
					return truth, validationErr
				}
				trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
				return truth, nil
			}
			lastErr = fallbackErr
		}
		if message := antigravityLoadValidationMessage(reloaded); message != "" {
			truth := antigravityGeminiProviderTruthFromLoad(reloaded, antigravityGeminiFallbackProject, time.Now().UTC())
			validationErr := fmt.Errorf("%s", message)
			trace.noteProviderTruth(AccountTypeGemini, "resolve", antigravityGeminiProviderTruthTraceState(truth), truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), validationErr)
			return truth, validationErr
		}
	}

	loadRes, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, "", "")
	if err != nil {
		if lastErr != nil {
			wrappedErr := fmt.Errorf("antigravity onboarding failed before loadCodeAssist: %v; load code assist failed: %w", lastErr, err)
			trace.noteProviderTruth(AccountTypeGemini, "resolve", "fail", "", "", "", time.Since(startedAt), wrappedErr)
			return antigravityGeminiProviderTruth{}, wrappedErr
		}
		trace.noteProviderTruth(AccountTypeGemini, "resolve", "fail", "", "", "", time.Since(startedAt), err)
		return antigravityGeminiProviderTruth{}, err
	}
	if projectID := strings.TrimSpace(loadRes.CloudaicompanionProject); projectID != "" {
		truth := antigravityGeminiProviderTruthFromLoad(loadRes, projectID, time.Now().UTC())
		trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
		return truth, nil
	}
	if fallbackRes, fallbackErr, usedFallback := h.maybeLoadAntigravityGeminiFallbackProject(ctx, accessToken, loadRes); usedFallback {
		if fallbackErr == nil {
			loadRes = fallbackRes
			truth := antigravityGeminiProviderTruthFromLoad(loadRes, antigravityGeminiFallbackProject, time.Now().UTC())
			if message := antigravityLoadValidationMessage(loadRes); message != "" {
				validationErr := fmt.Errorf("%s", message)
				trace.noteProviderTruth(AccountTypeGemini, "resolve", antigravityGeminiProviderTruthTraceState(truth), truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), validationErr)
				return truth, validationErr
			}
			trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
			return truth, nil
		}
		lastErr = fallbackErr
	}

	for _, tierID := range antigravityOnboardTierCandidates(loadRes) {
		tierID = strings.TrimSpace(tierID)
		if tierID == "" {
			continue
		}
		if _, ok := tried[tierID]; ok {
			continue
		}
		tried[tierID] = struct{}{}
		op, err := h.onboardAntigravityGeminiCodeAssist(ctx, accessToken, tierID)
		if err != nil {
			lastErr = err
			continue
		}
		if projectID := antigravityOperationProjectID(op); projectID != "" {
			loadRes, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, projectID, "")
			if err != nil {
				truth := antigravityGeminiProviderTruth{ProjectID: projectID}
				trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
				return truth, nil
			}
			truth := antigravityGeminiProviderTruthFromLoad(loadRes, projectID, time.Now().UTC())
			trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
			return truth, nil
		}
		reloaded, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, "", "")
		if err != nil {
			lastErr = err
			continue
		}
		if projectID := strings.TrimSpace(reloaded.CloudaicompanionProject); projectID != "" {
			truth := antigravityGeminiProviderTruthFromLoad(reloaded, projectID, time.Now().UTC())
			trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
			return truth, nil
		}
		if fallbackRes, fallbackErr, usedFallback := h.maybeLoadAntigravityGeminiFallbackProject(ctx, accessToken, reloaded); usedFallback {
			if fallbackErr == nil {
				reloaded = fallbackRes
				truth := antigravityGeminiProviderTruthFromLoad(reloaded, antigravityGeminiFallbackProject, time.Now().UTC())
				if message := antigravityLoadValidationMessage(reloaded); message != "" {
					validationErr := fmt.Errorf("%s", message)
					trace.noteProviderTruth(AccountTypeGemini, "resolve", antigravityGeminiProviderTruthTraceState(truth), truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), validationErr)
					return truth, validationErr
				}
				trace.noteProviderTruth(AccountTypeGemini, "resolve", "ok", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), nil)
				return truth, nil
			}
			lastErr = fallbackErr
		}
	}

	if message := antigravityLoadValidationMessage(loadRes); message != "" {
		truth := antigravityGeminiProviderTruthFromLoad(loadRes, antigravityGeminiFallbackProject, time.Now().UTC())
		validationErr := fmt.Errorf("%s", message)
		trace.noteProviderTruth(AccountTypeGemini, "resolve", antigravityGeminiProviderTruthTraceState(truth), truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), validationErr)
		return truth, validationErr
	}
	if lastErr != nil {
		trace.noteProviderTruth(AccountTypeGemini, "resolve", "fail", "", "", "", time.Since(startedAt), lastErr)
		return antigravityGeminiProviderTruth{}, lastErr
	}
	finalErr := fmt.Errorf("antigravity auth did not yield a Code Assist project id")
	truth := antigravityGeminiProviderTruthFromLoad(loadRes, antigravityGeminiFallbackProject, time.Now().UTC())
	trace.noteProviderTruth(AccountTypeGemini, "resolve", "fail", truth.ProjectID, truth.SubscriptionTierID, truth.ValidationReasonCode, time.Since(startedAt), finalErr)
	return truth, finalErr
}

func buildAntigravityGeminiAuthJSON(tokens *managedGeminiOAuthTokens, userInfo *managedGeminiOAuthUserInfo, truth antigravityGeminiProviderTruth, quota map[string]any, protectedModels []string) (string, error) {
	if tokens == nil {
		return "", fmt.Errorf("gemini oauth tokens are required")
	}
	projectID := strings.TrimSpace(truth.ProjectID)

	token := map[string]any{
		"access_token":  strings.TrimSpace(tokens.AccessToken),
		"refresh_token": strings.TrimSpace(tokens.RefreshToken),
		"token_type":    firstNonEmpty(strings.TrimSpace(tokens.TokenType), "Bearer"),
	}
	if projectID != "" {
		token["project_id"] = projectID
	}
	if tokens.ExpiresIn > 0 {
		token["expiry_timestamp"] = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).Unix()
	}
	if userInfo != nil {
		if email := strings.TrimSpace(userInfo.Email); email != "" {
			token["email"] = email
		}
	}

	root := map[string]any{
		"token":              token,
		"oauth_profile_id":   geminiOAuthAntigravityProfileID,
		"client_id":          geminiOAuthAntigravityClientID,
		"antigravity_source": "browser_oauth",
	}
	if hasAntigravityValidationTruth(truth) {
		root["validation_blocked"] = true
	}
	if strings.TrimSpace(truth.SubscriptionTierID) != "" {
		root["gemini_subscription_tier_id"] = strings.TrimSpace(truth.SubscriptionTierID)
	}
	if strings.TrimSpace(truth.SubscriptionTierName) != "" {
		root["gemini_subscription_tier_name"] = strings.TrimSpace(truth.SubscriptionTierName)
	}
	if strings.TrimSpace(truth.ValidationReasonCode) != "" {
		root["gemini_validation_reason_code"] = strings.TrimSpace(truth.ValidationReasonCode)
	}
	if strings.TrimSpace(truth.ValidationMessage) != "" {
		root["gemini_validation_message"] = strings.TrimSpace(truth.ValidationMessage)
	}
	if strings.TrimSpace(truth.ValidationURL) != "" {
		root["gemini_validation_url"] = strings.TrimSpace(truth.ValidationURL)
	}
	if !truth.ProviderCheckedAt.IsZero() {
		root["gemini_provider_checked_at"] = truth.ProviderCheckedAt.UTC().Format(time.RFC3339)
	}
	if len(protectedModels) > 0 {
		root["protected_models"] = protectedModels
	}
	if len(quota) > 0 {
		root["quota"] = quota
		quotaModels, quotaUpdatedAt, modelForwardingRules, subscriptionTier := decodeGeminiQuotaSnapshot(quota)
		if len(quotaModels) > 0 {
			root["gemini_quota_models"] = quotaModels
		}
		if !quotaUpdatedAt.IsZero() {
			root["gemini_quota_updated_at"] = quotaUpdatedAt.UTC().Format(time.RFC3339)
		}
		if len(modelForwardingRules) > 0 {
			root["gemini_model_forwarding_rules"] = modelForwardingRules
		}
		if strings.TrimSpace(truth.SubscriptionTierID) == "" && strings.TrimSpace(truth.SubscriptionTierName) == "" && strings.TrimSpace(subscriptionTier) != "" {
			root["gemini_subscription_tier_name"] = strings.TrimSpace(subscriptionTier)
		}
	}
	if userInfo != nil {
		if email := strings.TrimSpace(userInfo.Email); email != "" {
			root["email"] = email
		}
		if name := strings.TrimSpace(userInfo.Name); name != "" {
			root["name"] = name
		}
	}

	payload, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (h *proxyHandler) exchangeAntigravityGeminiOAuthCode(ctx context.Context, code string, session *antigravityGeminiOAuthSession) (*managedGeminiOAuthTokens, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	if session == nil {
		err := fmt.Errorf("antigravity oauth session is required")
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	form := url.Values{}
	form.Set("client_id", geminiOAuthAntigravityClientID)
	if secret := strings.TrimSpace(os.Getenv(geminiOAuthAntigravitySecretVar)); secret != "" {
		form.Set("client_secret", secret)
	}
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", session.RedirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", antigravityCodeAssistUA)

	resp, err := h.operatorGeminiTransport().RoundTrip(req)
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		if len(strings.TrimSpace(string(body))) > 0 {
			err = fmt.Errorf("antigravity gemini oauth exchange failed: %s: %s", resp.Status, safeText(body))
			trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
			return nil, err
		}
		err = fmt.Errorf("antigravity gemini oauth exchange failed: %s", resp.Status)
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}

	var payload managedGeminiOAuthTokens
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		err := fmt.Errorf("antigravity gemini oauth exchange returned an empty access token")
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	if strings.TrimSpace(payload.RefreshToken) == "" {
		err := fmt.Errorf("antigravity gemini oauth exchange did not return a refresh token")
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	payload.TokenType = firstNonEmpty(strings.TrimSpace(payload.TokenType), "Bearer")
	trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "ok", "", false, time.Since(startedAt), nil)
	return &payload, nil
}

func (h *proxyHandler) completeAntigravityGeminiOAuth(ctx context.Context, code string, session *antigravityGeminiOAuthSession) (*geminiSeatAddOutcome, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	tokens, err := h.exchangeAntigravityGeminiOAuthCode(ctx, code, session)
	if err != nil {
		return nil, err
	}
	userInfo, _ := h.fetchManagedGeminiOAuthUserInfo(ctx, tokens.AccessToken)
	providerTruth, resolveErr := h.resolveAntigravityGeminiProviderTruth(ctx, tokens.AccessToken)
	if resolveErr != nil && !hasAntigravityProviderTruthMaterialized(providerTruth) {
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), resolveErr)
		return nil, resolveErr
	}
	var (
		quota           map[string]any
		protectedModels []string
	)
	if fetchedQuota, fetchedProtectedModels, quotaErr := h.fetchAntigravityGeminiQuota(ctx, tokens.AccessToken, providerTruth.ProjectID); quotaErr != nil {
		log.Printf("warning: antigravity gemini quota fetch failed after oauth for project %q: %v", providerTruth.ProjectID, quotaErr)
	} else {
		quota = fetchedQuota
		protectedModels = fetchedProtectedModels
	}
	authJSON, err := buildAntigravityGeminiAuthJSON(tokens, userInfo, providerTruth, quota, protectedModels)
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	outcome, err := h.addGeminiSeatFromAuthJSON(ctx, authJSON)
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	if len(quota) > 0 || len(protectedModels) > 0 {
		for _, liveAcc := range h.pool.accounts {
			if liveAcc == nil || liveAcc.ID != outcome.AccountID {
				continue
			}
			liveAcc.mu.Lock()
			applyAntigravityGeminiQuotaSnapshotLocked(liveAcc, quota, protectedModels)
			if status := managedGeminiHealthStatusForProviderTruthState(liveAcc.GeminiProviderTruthState); status != "" && status != "healthy" {
				liveAcc.HealthStatus = status
			}
			if liveAcc.GeminiProviderTruthState == geminiProviderTruthStateQuotaForbidden {
				liveAcc.HealthError = sanitizeStatusMessage(liveAcc.AntigravityQuotaForbiddenReason)
			}
			liveAcc.mu.Unlock()
			if saveErr := saveAccount(liveAcc); saveErr != nil {
				log.Printf("warning: failed to persist antigravity gemini quota snapshot for %s: %v", liveAcc.ID, saveErr)
			}
			h.reloadAccounts()
			if refreshed, ok := h.snapshotAccountByID(outcome.AccountID, time.Now()); ok {
				outcome.HealthStatus = firstNonEmpty(strings.TrimSpace(refreshed.HealthStatus), outcome.HealthStatus)
				outcome.HealthError = sanitizeStatusMessage(refreshed.HealthError)
				outcome.Dead = refreshed.Dead
				outcome.ProviderTruthReady = refreshed.GeminiProviderTruthReady
				outcome.ProviderTruthState = strings.TrimSpace(refreshed.GeminiProviderTruthState)
				outcome.ProviderTruthReason = sanitizeStatusMessage(refreshed.GeminiProviderTruthReason)
				outcome.ProviderProjectID = strings.TrimSpace(refreshed.AntigravityProjectID)
				if !refreshed.ExpiresAt.IsZero() {
					outcome.AuthExpiresAt = refreshed.ExpiresAt.UTC().Format(time.RFC3339)
				}
			}
			break
		}
	}
	if resolveErr != nil {
		outcome.ProbeOK = false
		outcome.ProbeError = sanitizeStatusMessage(resolveErr.Error())
		if strings.TrimSpace(outcome.HealthError) == "" {
			outcome.HealthError = sanitizeStatusMessage(resolveErr.Error())
		}
	}
	trace.noteOAuthExchange(AccountTypeGemini, "antigravity", "seat_added", outcome.AccountID, !outcome.Created, time.Since(startedAt), nil)
	return outcome, nil
}

func (h *proxyHandler) handleOperatorGeminiAntigravityOAuthStart(w http.ResponseWriter, r *http.Request) {
	redirectURI, err := antigravityGeminiRedirectURI(r)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := generateGeminiOAuthState()
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oauthURL, err := buildAntigravityGeminiOAuthURL(redirectURI, state)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	storeAntigravityGeminiOAuthSession(&antigravityGeminiOAuthSession{
		State:       state,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now().UTC(),
	})
	go cleanupAntigravityGeminiOAuthSessions()

	respondJSON(w, map[string]any{
		"status":    "ok",
		"oauth_url": oauthURL,
		"state":     state,
	})
}

func serveAntigravityGeminiOAuthPopupResult(w http.ResponseWriter, ok bool, outcome *geminiSeatAddOutcome, errMessage string) {
	payload := map[string]any{
		"type": "gemini_oauth_result",
		"ok":   ok,
	}

	title := "Gemini Browser Auth Failed"
	heading := "Gemini Browser Auth failed"
	body := sanitizeStatusMessage(errMessage)
	if ok && outcome != nil {
		title = "Gemini Browser Auth Complete"
		heading = "Gemini seat added"
		body = "Gemini Browser Auth completed. Reloading the operator dashboard."
		if !outcome.Created {
			heading = "Gemini seat refreshed"
			body = "Gemini Browser Auth completed. An existing Gemini seat was refreshed."
		}
		if !outcome.ProbeOK || (!outcome.ProviderTruthReady && strings.TrimSpace(outcome.ProviderTruthState) != "") {
			title = "Gemini Browser Auth Saved Partial Seat"
			heading = "Gemini seat saved with provider block"
			body = "Gemini Browser Auth completed, but the seat is not eligible yet. Reloading the operator dashboard."
		}
		payload["account_id"] = outcome.AccountID
		payload["created"] = outcome.Created
		payload["probe_ok"] = outcome.ProbeOK
		payload["health_status"] = outcome.HealthStatus
		payload["health_error"] = outcome.HealthError
		payload["dead"] = outcome.Dead
		payload["auth_expires_at"] = outcome.AuthExpiresAt
		payload["provider_truth_ready"] = outcome.ProviderTruthReady
		payload["provider_truth_state"] = outcome.ProviderTruthState
		payload["provider_truth_reason"] = outcome.ProviderTruthReason
		payload["provider_project_id"] = outcome.ProviderProjectID
		payload["message"] = body
	} else {
		if body == "" {
			body = "Gemini Browser Auth did not complete."
		}
		payload["message"] = body
	}

	rawPayload, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>%s</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #0d1117; color: #c9d1d9; margin: 0; padding: 24px; }
    .card { max-width: 640px; margin: 48px auto; background: #161b22; border: 1px solid #30363d; border-radius: 12px; padding: 24px; }
    h1 { margin: 0 0 12px; font-size: 24px; }
    p { margin: 0; line-height: 1.5; }
  </style>
</head>
<body>
  <div class="card">
    <h1>%s</h1>
    <p>%s</p>
  </div>
  <script>
    const payload = %s;
    try {
      if (window.opener && !window.opener.closed) {
        window.opener.postMessage(payload, '*');
      }
    } catch (error) {}
    window.setTimeout(() => {
      try { window.close(); } catch (error) {}
    }, 80);
  </script>
</body>
</html>`,
		template.HTMLEscapeString(title),
		template.HTMLEscapeString(heading),
		template.HTMLEscapeString(body),
		string(rawPayload),
	)
}

func (h *proxyHandler) handleOperatorGeminiAntigravityOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if errCode := strings.TrimSpace(r.URL.Query().Get("error")); errCode != "" {
		desc := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if desc == "" {
			desc = "Google OAuth was cancelled or rejected."
		}
		serveAntigravityGeminiOAuthPopupResult(w, false, nil, fmt.Sprintf("Google OAuth error: %s. %s", errCode, desc))
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		serveAntigravityGeminiOAuthPopupResult(w, false, nil, "Missing OAuth state or authorization code.")
		return
	}

	session, ok := claimAntigravityGeminiOAuthSession(state)
	if !ok {
		serveAntigravityGeminiOAuthPopupResult(w, false, nil, "The Gemini Browser Auth session is missing or expired. Start the flow again.")
		return
	}

	outcome, err := h.completeAntigravityGeminiOAuth(r.Context(), code, session)
	if err != nil {
		serveAntigravityGeminiOAuthPopupResult(w, false, nil, err.Error())
		return
	}

	serveAntigravityGeminiOAuthPopupResult(w, true, outcome, "")
}

func (h *proxyHandler) handleOperatorGeminiOAuthStart(w http.ResponseWriter, r *http.Request) {
	redirectURI, err := managedGeminiRedirectURI(r)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	codeVerifier, codeChallenge, state, err := generateManagedGeminiOAuthPKCE()
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	profile := geminiOAuthDefaultProfile()
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Secret) == "" {
		respondJSONError(w, http.StatusServiceUnavailable, geminiOAuthConfigError().Error())
		return
	}
	oauthURL, err := buildManagedGeminiOAuthURL(profile, redirectURI, codeChallenge, state)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	storeManagedGeminiOAuthSession(&managedGeminiOAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
		ProfileID:    geminiOAuthProfileIDForLabel(profile.Label),
		ClientID:     profile.ID,
		ClientSecret: profile.Secret,
		CreatedAt:    time.Now().UTC(),
	})
	go cleanupManagedGeminiOAuthSessions()

	respondJSON(w, map[string]any{
		"status":    "ok",
		"oauth_url": oauthURL,
		"state":     state,
	})
}

func (h *proxyHandler) handleOperatorGeminiOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if errCode := strings.TrimSpace(r.URL.Query().Get("error")); errCode != "" {
		desc := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if desc == "" {
			desc = "Google OAuth was cancelled or rejected."
		}
		serveManagedGeminiOAuthPopupResult(w, false, nil, fmt.Sprintf("Google OAuth error: %s. %s", errCode, desc))
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		serveManagedGeminiOAuthPopupResult(w, false, nil, "Missing OAuth state or authorization code.")
		return
	}

	session, ok := claimManagedGeminiOAuthSession(state)
	if !ok {
		serveManagedGeminiOAuthPopupResult(w, false, nil, "The Gemini OAuth session is missing or expired. Start the flow again.")
		return
	}

	outcome, err := h.completeManagedGeminiOAuth(r.Context(), code, session)
	if err != nil {
		serveManagedGeminiOAuthPopupResult(w, false, nil, err.Error())
		return
	}

	serveManagedGeminiOAuthPopupResult(w, true, outcome, "")
}

func (h *proxyHandler) completeManagedGeminiOAuth(ctx context.Context, code string, session *managedGeminiOAuthSession) (*geminiSeatAddOutcome, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	tokens, err := h.exchangeManagedGeminiOAuthCode(ctx, code, session)
	if err != nil {
		return nil, err
	}

	userInfo, _ := h.fetchManagedGeminiOAuthUserInfo(ctx, tokens.AccessToken)
	authJSON, err := buildManagedGeminiOAuthAuthJSON(tokens, userInfo, session)
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	acc, created, err := saveGeminiSeatFromAuthJSON(h.cfg.poolDir, authJSON, geminiOperatorSourceManagedOAuth)
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}

	probeErr := h.probeManagedGeminiSeat(ctx, acc)
	if probeErr != nil {
		log.Printf("managed gemini seat %s probe failed during add: %v", acc.ID, probeErr)
	}

	h.reloadAccounts()

	live, liveOK := h.snapshotAccountByID(acc.ID, time.Now())
	outcome := &geminiSeatAddOutcome{
		AccountID: acc.ID,
		Created:   created,
		ProbeOK:   probeErr == nil,
		ProbeError: sanitizeStatusMessage(func() string {
			if probeErr == nil {
				return ""
			}
			return probeErr.Error()
		}()),
		HealthStatus: firstNonEmpty(strings.TrimSpace(acc.HealthStatus), "unknown"),
		HealthError:  sanitizeStatusMessage(acc.HealthError),
		Dead:         acc.Dead,
	}
	if liveOK {
		outcome.HealthStatus = firstNonEmpty(strings.TrimSpace(live.HealthStatus), "unknown")
		outcome.HealthError = sanitizeStatusMessage(live.HealthError)
		outcome.Dead = live.Dead
		outcome.ProviderTruthReady = live.GeminiProviderTruthReady
		outcome.ProviderTruthState = strings.TrimSpace(live.GeminiProviderTruthState)
		outcome.ProviderTruthReason = sanitizeStatusMessage(live.GeminiProviderTruthReason)
		outcome.ProviderProjectID = strings.TrimSpace(live.AntigravityProjectID)
		if !live.ExpiresAt.IsZero() {
			outcome.AuthExpiresAt = live.ExpiresAt.UTC().Format(time.RFC3339)
		}
	} else if !acc.ExpiresAt.IsZero() {
		outcome.AuthExpiresAt = acc.ExpiresAt.UTC().Format(time.RFC3339)
		outcome.ProviderTruthReady = acc.GeminiProviderTruthReady
		outcome.ProviderTruthState = strings.TrimSpace(acc.GeminiProviderTruthState)
		outcome.ProviderTruthReason = sanitizeStatusMessage(acc.GeminiProviderTruthReason)
		outcome.ProviderProjectID = strings.TrimSpace(acc.AntigravityProjectID)
	}
	trace.noteOAuthExchange(AccountTypeGemini, "managed", "seat_added", outcome.AccountID, !outcome.Created, time.Since(startedAt), nil)
	return outcome, nil
}

func (h *proxyHandler) exchangeManagedGeminiOAuthCode(ctx context.Context, code string, session *managedGeminiOAuthSession) (*managedGeminiOAuthTokens, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	profile, ok := geminiOAuthProfileByID(session.ProfileID)
	if !ok {
		profile = geminiOAuthClientProfile{
			ID:     strings.TrimSpace(session.ClientID),
			Secret: strings.TrimSpace(session.ClientSecret),
			Label:  "raw",
		}
	}
	if profile.ID == "" || profile.Secret == "" {
		profile = geminiOAuthDefaultProfile()
	}
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Secret) == "" {
		err := geminiOAuthConfigError()
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}

	form := url.Values{}
	form.Set("client_id", profile.ID)
	form.Set("client_secret", profile.Secret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", session.RedirectURI)
	if strings.TrimSpace(session.CodeVerifier) != "" {
		form.Set("code_verifier", session.CodeVerifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-pool-proxy")

	resp, err := h.operatorGeminiTransport().RoundTrip(req)
	if err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		if len(strings.TrimSpace(string(body))) > 0 {
			err = fmt.Errorf("gemini oauth exchange failed: %s: %s", resp.Status, safeText(body))
			trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
			return nil, err
		}
		err = fmt.Errorf("gemini oauth exchange failed: %s", resp.Status)
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}

	var payload managedGeminiOAuthTokens
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		err := fmt.Errorf("gemini oauth exchange returned an empty access token")
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	if strings.TrimSpace(payload.RefreshToken) == "" {
		err := fmt.Errorf("gemini oauth exchange did not return a refresh token; retry and approve offline access")
		trace.noteOAuthExchange(AccountTypeGemini, "managed", "fail", "", false, time.Since(startedAt), err)
		return nil, err
	}
	payload.TokenType = firstNonEmpty(strings.TrimSpace(payload.TokenType), "Bearer")
	trace.noteOAuthExchange(AccountTypeGemini, "managed", "ok", "", false, time.Since(startedAt), nil)
	return &payload, nil
}

func (h *proxyHandler) fetchManagedGeminiOAuthUserInfo(ctx context.Context, accessToken string) (*managedGeminiOAuthUserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("access token is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, managedGeminiOAuthUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-pool-proxy")

	resp, err := h.operatorGeminiTransport().RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		if len(strings.TrimSpace(string(body))) > 0 {
			return nil, fmt.Errorf("gemini userinfo failed: %s: %s", resp.Status, safeText(body))
		}
		return nil, fmt.Errorf("gemini userinfo failed: %s", resp.Status)
	}

	var payload managedGeminiOAuthUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func buildManagedGeminiOAuthAuthJSON(tokens *managedGeminiOAuthTokens, userInfo *managedGeminiOAuthUserInfo, session *managedGeminiOAuthSession) (string, error) {
	if tokens == nil {
		return "", fmt.Errorf("gemini oauth tokens are required")
	}

	root := map[string]any{
		"access_token":    strings.TrimSpace(tokens.AccessToken),
		"refresh_token":   strings.TrimSpace(tokens.RefreshToken),
		"token_type":      firstNonEmpty(strings.TrimSpace(tokens.TokenType), "Bearer"),
		"scope":           strings.TrimSpace(tokens.Scope),
		"plan_type":       "gemini",
		"operator_source": geminiOperatorSourceManagedOAuth,
	}
	if session != nil {
		if profileID := strings.TrimSpace(session.ProfileID); profileID != "" {
			root["oauth_profile_id"] = profileID
		} else {
			if clientID := strings.TrimSpace(session.ClientID); clientID != "" {
				root["client_id"] = clientID
			}
			if clientSecret := strings.TrimSpace(session.ClientSecret); clientSecret != "" {
				root["client_secret"] = clientSecret
			}
		}
	}
	if tokens.ExpiresIn > 0 {
		root["expiry_date"] = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()
	}
	if userInfo != nil {
		if email := strings.TrimSpace(userInfo.Email); email != "" {
			root["operator_email"] = email
		}
		if name := strings.TrimSpace(userInfo.Name); name != "" {
			root["operator_name"] = name
		}
	}

	payload, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (h *proxyHandler) operatorGeminiTransport() http.RoundTripper {
	if h != nil && h.refreshTransport != nil {
		return h.refreshTransport
	}
	if h != nil && h.transport != nil {
		return h.transport
	}
	return http.DefaultTransport
}

func generateManagedGeminiOAuthPKCE() (string, string, string, error) {
	verifierBytes := make([]byte, 48)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", "", fmt.Errorf("failed to generate gemini oauth verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	stateBytes := make([]byte, 24)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", "", fmt.Errorf("failed to generate gemini oauth state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	return verifier, challenge, state, nil
}

func managedGeminiRedirectURI(r *http.Request) (string, error) {
	if r == nil {
		return "", fmt.Errorf("request is required")
	}
	host := strings.TrimSpace(r.Host)
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("loopback host required for Gemini OAuth")
	}

	redirectHost := host
	port := ""
	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		redirectHost = parsedHost
		port = strings.TrimSpace(parsedPort)
	} else if r.TLS != nil {
		port = "443"
	} else {
		port = "80"
	}
	if port == "" {
		return "", fmt.Errorf("loopback port required for Gemini OAuth")
	}

	redirectHost = strings.TrimSpace(strings.Trim(redirectHost, "[]"))
	if redirectHost == "" {
		return "", fmt.Errorf("loopback host required for Gemini OAuth")
	}
	if strings.Contains(redirectHost, ":") {
		redirectHost = "[" + redirectHost + "]"
	}

	return "http://" + redirectHost + ":" + port + "/operator/gemini/oauth-callback", nil
}

func buildManagedGeminiOAuthURL(profile geminiOAuthClientProfile, redirectURI, codeChallenge, state string) (string, error) {
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Secret) == "" {
		return "", geminiOAuthConfigError()
	}
	u, err := url.Parse(managedGeminiOAuthAuthURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", profile.ID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("access_type", "offline")
	q.Set("scope", strings.Join(managedGeminiOAuthScopes, " "))
	q.Set("state", state)
	q.Set("prompt", "consent select_account")
	q.Set("include_granted_scopes", "true")
	if strings.TrimSpace(codeChallenge) != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func storeManagedGeminiOAuthSession(session *managedGeminiOAuthSession) {
	if session == nil || strings.TrimSpace(session.State) == "" {
		return
	}
	managedGeminiOAuthSessions.Lock()
	managedGeminiOAuthSessions.sessions[session.State] = session
	managedGeminiOAuthSessions.Unlock()
}

func claimManagedGeminiOAuthSession(state string) (*managedGeminiOAuthSession, bool) {
	key := strings.TrimSpace(state)
	if key == "" {
		return nil, false
	}

	managedGeminiOAuthSessions.Lock()
	defer managedGeminiOAuthSessions.Unlock()

	session, ok := managedGeminiOAuthSessions.sessions[key]
	if !ok || session == nil {
		return nil, false
	}
	delete(managedGeminiOAuthSessions.sessions, key)
	if session.CreatedAt.IsZero() || session.CreatedAt.Before(time.Now().Add(-managedGeminiOAuthSessionTTL)) {
		return nil, false
	}
	return session, true
}

func cleanupManagedGeminiOAuthSessions() {
	cutoff := time.Now().Add(-managedGeminiOAuthSessionTTL)
	managedGeminiOAuthSessions.Lock()
	for state, session := range managedGeminiOAuthSessions.sessions {
		if session == nil || session.CreatedAt.Before(cutoff) {
			delete(managedGeminiOAuthSessions.sessions, state)
		}
	}
	managedGeminiOAuthSessions.Unlock()
}

func serveManagedGeminiOAuthPopupResult(w http.ResponseWriter, ok bool, outcome *geminiSeatAddOutcome, errMessage string) {
	payload := map[string]any{
		"type": "gemini_oauth_result",
		"ok":   ok,
	}

	title := "Gemini OAuth Failed"
	heading := "Gemini OAuth failed"
	body := sanitizeStatusMessage(errMessage)
	if ok && outcome != nil {
		title = "Gemini OAuth Complete"
		heading = "Gemini seat added"
		body = "Gemini OAuth completed. Reloading the operator dashboard."
		if !outcome.Created {
			heading = "Gemini seat refreshed"
			body = "Gemini OAuth completed. An existing Gemini seat was refreshed."
		}
		payload["account_id"] = outcome.AccountID
		payload["created"] = outcome.Created
		payload["probe_ok"] = outcome.ProbeOK
		payload["health_status"] = outcome.HealthStatus
		payload["health_error"] = outcome.HealthError
		payload["dead"] = outcome.Dead
		payload["auth_expires_at"] = outcome.AuthExpiresAt
		payload["provider_truth_ready"] = outcome.ProviderTruthReady
		payload["provider_truth_state"] = outcome.ProviderTruthState
		payload["provider_truth_reason"] = outcome.ProviderTruthReason
		payload["provider_project_id"] = outcome.ProviderProjectID
		payload["message"] = body
	} else {
		if body == "" {
			body = "Gemini OAuth did not complete."
		}
		payload["message"] = body
	}

	rawPayload, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>%s</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #0d1117; color: #c9d1d9; margin: 0; padding: 24px; }
    .card { max-width: 640px; margin: 48px auto; background: #161b22; border: 1px solid #30363d; border-radius: 12px; padding: 24px; }
    h1 { margin: 0 0 12px; font-size: 24px; }
    p { margin: 0; line-height: 1.5; }
  </style>
</head>
<body>
  <div class="card">
    <h1>%s</h1>
    <p>%s</p>
  </div>
  <script>
    const payload = %s;
    try {
      if (window.opener && !window.opener.closed) {
        window.opener.postMessage(payload, '*');
      }
    } catch (error) {}
    window.setTimeout(() => {
      try { window.close(); } catch (error) {}
    }, 80);
  </script>
</body>
</html>`,
		template.HTMLEscapeString(title),
		template.HTMLEscapeString(heading),
		template.HTMLEscapeString(body),
		string(rawPayload),
	)
}
