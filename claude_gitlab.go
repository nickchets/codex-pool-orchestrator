package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	managedGitLabClaudeSubdir                   = "claude_gitlab"
	defaultGitLabInstanceURL                    = "https://gitlab.com"
	defaultGitLabClaudeGatewayURL               = "https://cloud.gitlab.com/ai/v1/proxy/anthropic"
	managedGitLabClaudeDirectAccess             = "/api/v4/ai/third_party_agents/direct_access"
	managedGitLabClaudeDefaultTTL               = 20 * time.Minute
	managedGitLabClaudeRateLimitWait            = 15 * time.Minute
	managedGitLabClaudeOrgTPMRateLimitWait      = 75 * time.Second
	managedGitLabClaudeGatewayRejectWait        = 2 * time.Minute
	managedGitLabClaudeQuotaExceededInitialWait = 30 * time.Minute
	managedGitLabClaudeQuotaExceededMaxWait     = 24 * time.Hour
	managedGitLabClaudeSharedOrgTPMHealthPrefix = "shared_org_tpm: "
)

type managedGitLabClaudeErrorSource string

const (
	managedGitLabClaudeErrorSourceDirectAccess   managedGitLabClaudeErrorSource = "direct_access"
	managedGitLabClaudeErrorSourceGatewayRequest managedGitLabClaudeErrorSource = "gateway_request"
)

type managedGitLabClaudeErrorDisposition struct {
	MarkDead     bool
	RateLimit    bool
	Reason       string
	HealthStatus string
	Cooldown     time.Duration
	SharedOrgTPM bool
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func getHeaderValueFold(headers http.Header, key string) string {
	if headers == nil {
		return ""
	}
	for headerKey, values := range headers {
		if !strings.EqualFold(headerKey, key) || len(values) == 0 {
			continue
		}
		return strings.TrimSpace(values[0])
	}
	return ""
}

func getMapValueFold(values map[string]string, key string) string {
	if values == nil {
		return ""
	}
	for valueKey, value := range values {
		if !strings.EqualFold(valueKey, key) {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

func gitLabClaudeScopeKey(acc *Account) string {
	if acc == nil || !isGitLabClaudeAccount(acc) {
		return ""
	}
	source := strings.ToLower(firstNonEmpty(strings.TrimSpace(acc.SourceBaseURL), defaultGitLabInstanceURL))
	gateway := strings.ToLower(firstNonEmpty(strings.TrimSpace(acc.UpstreamBaseURL), defaultGitLabClaudeGatewayURL))
	instanceID := strings.ToLower(getMapValueFold(acc.ExtraHeaders, "X-Gitlab-Instance-Id"))
	entitlementScope := strings.TrimSpace(getMapValueFold(acc.ExtraHeaders, "X-Gitlab-Feature-Enabled-By-Namespace-Ids"))
	if entitlementScope == "" {
		entitlementScope = strings.TrimSpace(getMapValueFold(acc.ExtraHeaders, "X-Gitlab-Root-Namespace-Id"))
	}
	if entitlementScope == "" {
		entitlementScope = strings.TrimSpace(getMapValueFold(acc.ExtraHeaders, "X-Gitlab-Namespace-Id"))
	}
	if entitlementScope == "" {
		entitlementScope = strings.TrimSpace(getMapValueFold(acc.ExtraHeaders, "X-Gitlab-Global-User-Id"))
	}
	if entitlementScope == "" {
		entitlementScope = strings.TrimSpace(getMapValueFold(acc.ExtraHeaders, "X-Gitlab-User-Id"))
	}
	return strings.ToLower(strings.Join([]string{source, gateway, instanceID, entitlementScope}, "|"))
}

func isManagedGitLabClaudeOrgTPMRateLimit(reason string) bool {
	lower := strings.ToLower(strings.TrimSpace(reason))
	if lower == "" {
		return false
	}
	hasOrgLimit := strings.Contains(lower, "organization's rate limit") ||
		strings.Contains(lower, "organizations rate limit") ||
		strings.Contains(lower, "organization rate limit") ||
		strings.Contains(lower, "organisation's rate limit") ||
		strings.Contains(lower, "organisation rate limit")
	hasTPM := strings.Contains(lower, "tokens per minute") ||
		strings.Contains(lower, "input tokens per minute") ||
		strings.Contains(lower, "output tokens per minute")
	return hasOrgLimit && hasTPM
}

func managedGitLabClaudeCooldownWait(disposition managedGitLabClaudeErrorDisposition, headers http.Header) time.Duration {
	wait := disposition.Cooldown
	switch {
	case disposition.SharedOrgTPM && wait <= 0:
		wait = managedGitLabClaudeOrgTPMRateLimitWait
	case wait <= 0:
		wait = managedGitLabClaudeRateLimitWait
	}
	if retryAfter, ok := parseRetryAfter(headers); ok && retryAfter > 0 {
		wait = retryAfter
	}
	return wait
}

func managedGitLabClaudeSharedOrgTPMHealthError(reason string) string {
	return managedGitLabClaudeSharedOrgTPMHealthPrefix + sanitizeStatusMessage(firstNonEmpty(reason, "gitlab claude organization token-per-minute rate limited"))
}

func isManagedGitLabClaudeSharedOrgTPMHealthError(reason string) bool {
	trimmed := strings.TrimSpace(reason)
	return len(trimmed) >= len(managedGitLabClaudeSharedOrgTPMHealthPrefix) &&
		strings.EqualFold(trimmed[:len(managedGitLabClaudeSharedOrgTPMHealthPrefix)], managedGitLabClaudeSharedOrgTPMHealthPrefix)
}

func stripManagedGitLabClaudeSharedOrgTPMHealthPrefix(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if len(trimmed) >= len(managedGitLabClaudeSharedOrgTPMHealthPrefix) &&
		strings.EqualFold(trimmed[:len(managedGitLabClaudeSharedOrgTPMHealthPrefix)], managedGitLabClaudeSharedOrgTPMHealthPrefix) {
		return strings.TrimSpace(trimmed[len(managedGitLabClaudeSharedOrgTPMHealthPrefix):])
	}
	return trimmed
}

func parseGitLabRateLimitHeaderInt(headers http.Header, key string) (int, bool) {
	raw := getHeaderValueFold(headers, key)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func applyGitLabDirectAccessHeaders(acc *Account, headers http.Header, now time.Time) {
	if acc == nil || headers == nil {
		return
	}

	name := getHeaderValueFold(headers, "RateLimit-Name")
	limit, hasLimit := parseGitLabRateLimitHeaderInt(headers, "RateLimit-Limit")
	remaining, hasRemaining := parseGitLabRateLimitHeaderInt(headers, "RateLimit-Remaining")
	resetRaw, hasReset := parseGitLabRateLimitHeaderInt(headers, "RateLimit-Reset")
	retryAfter, hasRetryAfter := parseRetryAfter(headers)

	var resetAt time.Time
	if hasReset && resetRaw > 0 {
		resetAt = time.Unix(int64(resetRaw), 0).UTC()
	} else if hasRetryAfter && retryAfter > 0 {
		resetAt = now.Add(retryAfter).UTC()
	}

	acc.mu.Lock()
	defer acc.mu.Unlock()

	if name != "" {
		acc.GitLabRateLimitName = name
	}
	if hasLimit {
		acc.GitLabRateLimitLimit = limit
	}
	if hasRemaining {
		acc.GitLabRateLimitRemaining = remaining
	}
	if !resetAt.IsZero() {
		acc.GitLabRateLimitResetAt = resetAt
	}
}

func gitLabClaudeQuotaExceededCooldown(nextCount int) time.Duration {
	if nextCount <= 1 {
		return managedGitLabClaudeQuotaExceededInitialWait
	}
	wait := managedGitLabClaudeQuotaExceededInitialWait
	for step := 1; step < nextCount; step++ {
		if wait >= managedGitLabClaudeQuotaExceededMaxWait/2 {
			return managedGitLabClaudeQuotaExceededMaxWait
		}
		wait *= 2
	}
	if wait > managedGitLabClaudeQuotaExceededMaxWait {
		return managedGitLabClaudeQuotaExceededMaxWait
	}
	return wait
}

func isGitLabClaudeAccount(a *Account) bool {
	return a != nil && a.Type == AccountTypeClaude && accountAuthMode(a) == accountAuthModeGitLab
}

func missingGitLabClaudeGatewayState(a *Account) bool {
	if !isGitLabClaudeAccount(a) {
		return false
	}
	return strings.TrimSpace(a.AccessToken) == "" || len(a.ExtraHeaders) == 0
}

func normalizeGitLabInstanceURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = defaultGitLabInstanceURL
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("instance_url must include a valid host")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func managedGitLabClaudeAccountID(instanceURL, sourceToken string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(instanceURL) + "\n" + strings.TrimSpace(sourceToken)))
	return fmt.Sprintf("claude_gitlab_%x", sum[:6])
}

func saveManagedGitLabClaudeToken(poolDir, instanceURL, sourceToken string) (*Account, bool, error) {
	token := strings.TrimSpace(sourceToken)
	if token == "" {
		return nil, false, fmt.Errorf("token is empty")
	}

	normalizedInstanceURL, err := normalizeGitLabInstanceURL(instanceURL)
	if err != nil {
		return nil, false, err
	}

	dir := filepath.Join(poolDir, managedGitLabClaudeSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}

	accountID := managedGitLabClaudeAccountID(normalizedInstanceURL, token)
	path := filepath.Join(dir, accountID+".json")
	_, statErr := os.Stat(path)
	created := os.IsNotExist(statErr)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, false, statErr
	}

	acc := &Account{
		Type:            AccountTypeClaude,
		ID:              accountID,
		File:            path,
		RefreshToken:    token,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		HealthStatus:    "unknown",
		SourceBaseURL:   normalizedInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
	}
	if err := saveGitLabClaudeAccountFile(acc, true); err != nil {
		return nil, false, err
	}

	return acc, created, nil
}

func buildGitLabClaudeAuthJSON(a *Account) (ClaudeAuthJSON, error) {
	if a == nil {
		return ClaudeAuthJSON{}, fmt.Errorf("nil account")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	root := ClaudeAuthJSON{
		PlanType:                 firstNonEmpty(strings.TrimSpace(a.PlanType), "gitlab_duo"),
		AuthMode:                 accountAuthModeGitLab,
		GitLabToken:              strings.TrimSpace(a.RefreshToken),
		GitLabInstanceURL:        firstNonEmpty(strings.TrimSpace(a.SourceBaseURL), defaultGitLabInstanceURL),
		GitLabGatewayToken:       strings.TrimSpace(a.AccessToken),
		GitLabGatewayBaseURL:     firstNonEmpty(strings.TrimSpace(a.UpstreamBaseURL), defaultGitLabClaudeGatewayURL),
		GitLabGatewayHeaders:     copyStringMap(a.ExtraHeaders),
		GitLabRateLimitName:      strings.TrimSpace(a.GitLabRateLimitName),
		GitLabRateLimitLimit:     a.GitLabRateLimitLimit,
		GitLabRateLimitRemaining: a.GitLabRateLimitRemaining,
		GitLabQuotaExceededCount: a.GitLabQuotaExceededCount,
		Disabled:                 a.Disabled,
		Dead:                     a.Dead,
		HealthStatus:             strings.TrimSpace(a.HealthStatus),
		HealthError:              sanitizeStatusMessage(a.HealthError),
	}
	if !a.ExpiresAt.IsZero() {
		root.GitLabGatewayExpiresAt = a.ExpiresAt.UTC()
	}
	if !a.GitLabRateLimitResetAt.IsZero() {
		root.GitLabRateLimitResetAt = a.GitLabRateLimitResetAt.UTC()
	}
	if !a.GitLabLastQuotaExceededAt.IsZero() {
		root.GitLabLastQuotaExceededAt = a.GitLabLastQuotaExceededAt.UTC()
	}
	if !a.RateLimitUntil.IsZero() {
		root.RateLimitUntil = a.RateLimitUntil.UTC()
	}
	if !a.DeadSince.IsZero() {
		value := a.DeadSince.UTC()
		root.DeadSince = &value
	}
	if !a.LastRefresh.IsZero() {
		value := a.LastRefresh.UTC()
		root.LastRefresh = &value
	}
	if !a.HealthCheckedAt.IsZero() {
		value := a.HealthCheckedAt.UTC()
		root.HealthCheckedAt = &value
	}
	if !a.LastHealthyAt.IsZero() {
		value := a.LastHealthyAt.UTC()
		root.LastHealthyAt = &value
	}
	return root, nil
}

func loadGitLabClaudeRoot(file string, allowCreate bool) (map[string]any, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		if allowCreate && os.IsNotExist(err) {
			return make(map[string]any), nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("parse %s: empty file", file)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	if root == nil {
		root = make(map[string]any)
	}
	return root, nil
}

func setJSONField(root map[string]any, key string, value any, present bool) {
	if present {
		root[key] = value
		return
	}
	delete(root, key)
}

func setJSONTimeField(root map[string]any, key string, value time.Time) {
	setJSONField(root, key, value.UTC().Format(time.RFC3339Nano), !value.IsZero())
}

func setJSONTimePtrField(root map[string]any, key string, value *time.Time) {
	if value == nil || value.IsZero() {
		delete(root, key)
		return
	}
	root[key] = value.UTC().Format(time.RFC3339Nano)
}

func mergeGitLabClaudeAuthJSON(root map[string]any, state ClaudeAuthJSON) {
	root["plan_type"] = state.PlanType
	root["auth_mode"] = state.AuthMode
	root["gitlab_token"] = state.GitLabToken
	root["gitlab_instance_url"] = state.GitLabInstanceURL
	setJSONField(root, "gitlab_gateway_token", state.GitLabGatewayToken, strings.TrimSpace(state.GitLabGatewayToken) != "")
	setJSONField(root, "gitlab_gateway_base_url", state.GitLabGatewayBaseURL, strings.TrimSpace(state.GitLabGatewayBaseURL) != "")
	setJSONField(root, "gitlab_gateway_headers", state.GitLabGatewayHeaders, len(state.GitLabGatewayHeaders) > 0)
	setJSONTimeField(root, "gitlab_gateway_expires_at", state.GitLabGatewayExpiresAt)
	setJSONField(root, "gitlab_rate_limit_name", state.GitLabRateLimitName, strings.TrimSpace(state.GitLabRateLimitName) != "")
	setJSONField(root, "gitlab_rate_limit_limit", state.GitLabRateLimitLimit, state.GitLabRateLimitLimit > 0)
	setJSONField(root, "gitlab_rate_limit_remaining", state.GitLabRateLimitRemaining, state.GitLabRateLimitLimit > 0 || state.GitLabRateLimitRemaining > 0)
	setJSONTimeField(root, "gitlab_rate_limit_reset_at", state.GitLabRateLimitResetAt)
	setJSONField(root, "gitlab_quota_exceeded_count", state.GitLabQuotaExceededCount, state.GitLabQuotaExceededCount > 0)
	setJSONTimeField(root, "gitlab_last_quota_exceeded_at", state.GitLabLastQuotaExceededAt)
	setJSONTimeField(root, "rate_limit_until", state.RateLimitUntil)
	setJSONTimePtrField(root, "last_refresh", state.LastRefresh)
	setJSONField(root, "disabled", true, state.Disabled)
	setJSONField(root, "dead", true, state.Dead)
	setJSONField(root, "health_status", state.HealthStatus, strings.TrimSpace(state.HealthStatus) != "")
	setJSONField(root, "health_error", state.HealthError, strings.TrimSpace(state.HealthError) != "")
	setJSONTimePtrField(root, "health_checked_at", state.HealthCheckedAt)
	setJSONTimePtrField(root, "last_healthy_at", state.LastHealthyAt)

	delete(root, "api_key")
	delete(root, "claudeAiOauth")
}

func saveGitLabClaudeAccountFile(a *Account, allowCreate bool) error {
	state, err := buildGitLabClaudeAuthJSON(a)
	if err != nil {
		return err
	}
	root, err := loadGitLabClaudeRoot(a.File, allowCreate)
	if err != nil {
		return err
	}
	mergeGitLabClaudeAuthJSON(root, state)
	return atomicWriteJSON(a.File, root)
}

func saveGitLabClaudeAccount(a *Account) error {
	return saveGitLabClaudeAccountFile(a, false)
}

func (h *proxyHandler) handleOperatorClaudeGitLabTokenAdd(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Token       string `json:"token"`
		InstanceURL string `json:"instance_url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&payload); err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	token := strings.TrimSpace(payload.Token)
	if token == "" {
		respondJSONError(w, http.StatusBadRequest, "token is required")
		return
	}

	acc, created, err := saveManagedGitLabClaudeToken(h.cfg.poolDir, payload.InstanceURL, token)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	refreshErr := h.refreshAccount(r.Context(), acc)

	h.reloadAccounts()

	live, liveOK := h.snapshotAccountByID(acc.ID, time.Now())
	healthStatus := firstNonEmpty(strings.TrimSpace(acc.HealthStatus), "unknown")
	healthError := sanitizeStatusMessage(acc.HealthError)
	dead := acc.Dead
	instanceURL := firstNonEmpty(strings.TrimSpace(acc.SourceBaseURL), defaultGitLabInstanceURL)
	authExpiresAt := ""
	if liveOK {
		healthStatus = firstNonEmpty(strings.TrimSpace(live.HealthStatus), "unknown")
		healthError = sanitizeStatusMessage(live.HealthError)
		dead = live.Dead
		if !live.ExpiresAt.IsZero() {
			authExpiresAt = live.ExpiresAt.UTC().Format(time.RFC3339)
		}
	} else if !acc.ExpiresAt.IsZero() {
		authExpiresAt = acc.ExpiresAt.UTC().Format(time.RFC3339)
	}

	respondJSON(w, map[string]any{
		"status":     "ok",
		"account_id": acc.ID,
		"created":    created,
		"refresh_ok": refreshErr == nil,
		"refresh_error": func() string {
			if refreshErr == nil {
				return ""
			}
			return sanitizeStatusMessage(refreshErr.Error())
		}(),
		"instance_url":    instanceURL,
		"health_status":   healthStatus,
		"health_error":    healthError,
		"dead":            dead,
		"auth_expires_at": authExpiresAt,
	})
}

func markGitLabClaudeRefreshError(acc *Account, now time.Time, message string, clearGatewayState bool) error {
	err := fmt.Errorf("%s", message)
	if acc == nil {
		return err
	}

	acc.mu.Lock()
	if clearGatewayState {
		acc.AccessToken = ""
		acc.ExtraHeaders = nil
		acc.ExpiresAt = time.Time{}
	}
	acc.HealthStatus = "error"
	acc.HealthError = sanitizeStatusMessage(message)
	acc.HealthCheckedAt = now
	acc.Penalty += 0.3
	acc.mu.Unlock()
	return err
}

func refreshGitLabClaudeAccess(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	if acc == nil {
		return fmt.Errorf("nil account")
	}

	instanceURL, err := normalizeGitLabInstanceURL(acc.SourceBaseURL)
	if err != nil {
		return err
	}

	sourceToken := strings.TrimSpace(acc.RefreshToken)
	if sourceToken == "" {
		return fmt.Errorf("missing gitlab source token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, instanceURL+managedGitLabClaudeDirectAccess, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+sourceToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex-pool-proxy")

	resp, err := transport.RoundTrip(req)
	now := time.Now().UTC()
	if err != nil {
		return markGitLabClaudeRefreshError(acc, now, err.Error(), false)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	applyGitLabDirectAccessHeaders(acc, resp.Header, now)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		disposition := classifyManagedGitLabClaudeError(managedGitLabClaudeErrorSourceDirectAccess, resp.StatusCode, resp.Header, body)
		applyManagedGitLabClaudeDisposition(acc, disposition, resp.Header, now)
		reason := firstNonEmpty(disposition.Reason, resp.Status)
		return fmt.Errorf("gitlab direct access failed: %s", reason)
	}

	var payload struct {
		Token     string            `json:"token"`
		BaseURL   string            `json:"base_url"`
		Headers   map[string]string `json:"headers"`
		ExpiresAt int64             `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return markGitLabClaudeRefreshError(acc, now, fmt.Sprintf("decode gitlab direct access response: %v", err), true)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return markGitLabClaudeRefreshError(acc, now, "gitlab direct access response did not include token", true)
	}
	headersCopy := copyStringMap(payload.Headers)
	if len(headersCopy) == 0 {
		return markGitLabClaudeRefreshError(acc, now, "gitlab direct access response did not include gateway headers", true)
	}

	expiresAt := now.Add(managedGitLabClaudeDefaultTTL)
	if payload.ExpiresAt > 0 {
		expiresAt = time.Unix(payload.ExpiresAt, 0).UTC()
	}

	acc.mu.Lock()
	acc.AccessToken = strings.TrimSpace(payload.Token)
	acc.SourceBaseURL = instanceURL
	acc.UpstreamBaseURL = firstNonEmpty(strings.TrimSpace(payload.BaseURL), defaultGitLabClaudeGatewayURL)
	acc.ExtraHeaders = headersCopy
	acc.ExpiresAt = expiresAt
	setAccountDeadStateLocked(acc, false, now)
	acc.RateLimitUntil = time.Time{}
	acc.HealthStatus = "healthy"
	acc.HealthError = ""
	acc.HealthCheckedAt = now
	acc.LastHealthyAt = now
	acc.mu.Unlock()
	return nil
}

func classifyManagedGitLabClaudeError(source managedGitLabClaudeErrorSource, statusCode int, headers http.Header, body []byte) managedGitLabClaudeErrorDisposition {
	reason := extractGitLabClaudeErrorSummary(body)
	lower := strings.ToLower(reason)

	disposition := managedGitLabClaudeErrorDisposition{Reason: reason}
	switch {
	case statusCode == http.StatusPaymentRequired,
		strings.Contains(lower, "usage_quota_exceeded"),
		strings.Contains(lower, "usage quota exceeded"),
		strings.Contains(lower, "quota exceeded"),
		strings.Contains(lower, "insufficient_credits"),
		strings.Contains(lower, "insufficient credits"):
		disposition.MarkDead = true
		disposition.HealthStatus = "dead"
	case statusCode == http.StatusTooManyRequests:
		disposition.RateLimit = true
		disposition.HealthStatus = "rate_limited"
		disposition.Cooldown = managedGitLabClaudeRateLimitWait
		if isManagedGitLabClaudeOrgTPMRateLimit(reason) {
			disposition.SharedOrgTPM = true
			disposition.Cooldown = managedGitLabClaudeOrgTPMRateLimitWait
		}
	case strings.Contains(lower, "rate limit"):
		disposition.RateLimit = true
		disposition.HealthStatus = "rate_limited"
		disposition.Cooldown = managedGitLabClaudeRateLimitWait
		if isManagedGitLabClaudeOrgTPMRateLimit(reason) {
			disposition.SharedOrgTPM = true
			disposition.Cooldown = managedGitLabClaudeOrgTPMRateLimitWait
		}
	case source == managedGitLabClaudeErrorSourceGatewayRequest &&
		(statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden):
		disposition.RateLimit = true
		disposition.HealthStatus = "gateway_rejected"
		disposition.Cooldown = managedGitLabClaudeGatewayRejectWait
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		disposition.MarkDead = true
		disposition.HealthStatus = "dead"
	}
	if disposition.Reason == "" {
		disposition.Reason = firstNonEmpty(strings.TrimSpace(headers.Get("Retry-After")), http.StatusText(statusCode))
	}
	return disposition
}

func applyManagedGitLabClaudeDisposition(acc *Account, disposition managedGitLabClaudeErrorDisposition, headers http.Header, now time.Time) {
	if acc == nil {
		return
	}

	reason := sanitizeStatusMessage(firstNonEmpty(disposition.Reason, "gitlab claude request failed"))
	acc.mu.Lock()
	defer acc.mu.Unlock()

	acc.HealthCheckedAt = now
	acc.HealthError = reason

	if disposition.RateLimit {
		wait := disposition.Cooldown
		if disposition.HealthStatus == "quota_exceeded" {
			acc.GitLabQuotaExceededCount++
			acc.GitLabLastQuotaExceededAt = now
			wait = gitLabClaudeQuotaExceededCooldown(acc.GitLabQuotaExceededCount)
		}
		wait = managedGitLabClaudeCooldownWait(disposition, headers)
		until := now.Add(wait)
		if acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until
		}
		setAccountDeadStateLocked(acc, false, now)
		if strings.TrimSpace(disposition.HealthStatus) != "" {
			acc.HealthStatus = disposition.HealthStatus
		} else if strings.Contains(strings.ToLower(reason), "quota") {
			acc.HealthStatus = "quota_exceeded"
		} else {
			acc.HealthStatus = "rate_limited"
		}
		acc.Penalty += 0.5
		return
	}

	if disposition.MarkDead {
		setAccountDeadStateLocked(acc, true, now)
		acc.HealthStatus = firstNonEmpty(strings.TrimSpace(disposition.HealthStatus), "dead")
		acc.RateLimitUntil = time.Time{}
		acc.Penalty += 100.0
		return
	}

	acc.HealthStatus = "error"
	acc.Penalty += 0.5
}

func extractGitLabClaudeErrorSummary(body []byte) string {
	body = bodyForInspection(nil, body)
	if len(body) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		var parts []string
		appendValue := func(v any) {}
		appendValue = func(v any) {
			switch typed := v.(type) {
			case string:
				if trimmed := strings.TrimSpace(typed); trimmed != "" {
					parts = append(parts, trimmed)
				}
			case []any:
				for _, item := range typed {
					appendValue(item)
				}
			case map[string]any:
				for _, key := range []string{"message", "error", "detail", "code"} {
					if value, ok := typed[key]; ok {
						appendValue(value)
					}
				}
			}
		}
		for _, key := range []string{"message", "error", "errors", "detail"} {
			if value, ok := payload[key]; ok {
				appendValue(value)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " | ")
		}
	}
	return strings.TrimSpace(safeText(body))
}
