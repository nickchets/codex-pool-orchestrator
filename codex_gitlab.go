package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultGitLabCodexGatewayURL = "https://cloud.gitlab.com/ai/v1/proxy/openai"

const gitLabCodexModelPrefix = "gitlab/"

const (
	managedGitLabCodexRateLimitWait            = 15 * time.Minute
	managedGitLabCodexGatewayRejectWait        = 2 * time.Minute
	managedGitLabCodexPaymentRequiredWait      = 5 * time.Minute
	managedGitLabCodexQuotaExceededInitialWait = 30 * time.Minute
	managedGitLabCodexQuotaExceededMaxWait     = 24 * time.Hour
)

type managedGitLabCodexErrorSource string

const (
	managedGitLabCodexErrorSourceDirectAccess   managedGitLabCodexErrorSource = "direct_access"
	managedGitLabCodexErrorSourceGatewayRequest managedGitLabCodexErrorSource = "gateway_request"
)

type managedGitLabCodexErrorDisposition struct {
	MarkDead     bool
	RateLimit    bool
	Reason       string
	HealthStatus string
	Cooldown     time.Duration
}

func gitLabCodexAliasModel() string {
	return gitLabCodexModelPrefix + "gpt-5-codex"
}

func gitLabCodexTargetModel(model string) (string, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, gitLabCodexModelPrefix) || len(trimmed) <= len(gitLabCodexModelPrefix) {
		return "", false
	}
	target := strings.TrimSpace(trimmed[len(gitLabCodexModelPrefix):])
	if target == "" {
		return "", false
	}
	return target, true
}

func isGitLabCodexAccount(a *Account) bool {
	return a != nil && a.Type == AccountTypeCodex && accountAuthMode(a) == accountAuthModeGitLab
}

func missingGitLabCodexGatewayState(a *Account) bool {
	if !isGitLabCodexAccount(a) {
		return false
	}
	return strings.TrimSpace(a.AccessToken) == "" || len(a.ExtraHeaders) == 0
}

func coerceGitLabCodexRequestBody(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}

	textValue, ok := payload["text"]
	if !ok || textValue == nil {
		return body, false
	}
	textObject, ok := textValue.(map[string]any)
	if !ok {
		return body, false
	}

	rawVerbosity, ok := textObject["verbosity"]
	if !ok {
		return body, false
	}
	verbosity := strings.TrimSpace(fmt.Sprint(rawVerbosity))
	if verbosity == "" || verbosity == "medium" {
		return body, false
	}

	textObject["verbosity"] = "medium"
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return rewritten, true
}

func gitLabCodexQuotaExceededCooldown(nextCount int) time.Duration {
	if nextCount <= 1 {
		return managedGitLabCodexQuotaExceededInitialWait
	}
	wait := managedGitLabCodexQuotaExceededInitialWait
	for step := 1; step < nextCount; step++ {
		if wait >= managedGitLabCodexQuotaExceededMaxWait/2 {
			return managedGitLabCodexQuotaExceededMaxWait
		}
		wait *= 2
	}
	if wait > managedGitLabCodexQuotaExceededMaxWait {
		return managedGitLabCodexQuotaExceededMaxWait
	}
	return wait
}

func classifyManagedGitLabCodexError(source managedGitLabCodexErrorSource, statusCode int, headers http.Header, body []byte) managedGitLabCodexErrorDisposition {
	reason := extractGitLabClaudeErrorSummary(body)
	lower := strings.ToLower(reason)

	containsAny := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(lower, part) {
				return true
			}
		}
		return false
	}

	disposition := managedGitLabCodexErrorDisposition{Reason: reason}
	switch {
	case containsAny("usage_quota_exceeded", "usage quota exceeded", "quota exceeded", "insufficient_credits", "insufficient credits", "sufficient credits"):
		if source == managedGitLabCodexErrorSourceGatewayRequest {
			disposition.RateLimit = true
			disposition.HealthStatus = "quota_exceeded"
			disposition.Cooldown = managedGitLabCodexQuotaExceededInitialWait
		} else {
			disposition.MarkDead = true
			disposition.HealthStatus = "dead"
		}
	case statusCode == http.StatusTooManyRequests:
		disposition.RateLimit = true
		disposition.HealthStatus = "rate_limited"
		disposition.Cooldown = managedGitLabCodexRateLimitWait
	case containsAny("rate limit", "too many requests"):
		disposition.RateLimit = true
		disposition.HealthStatus = "rate_limited"
		disposition.Cooldown = managedGitLabCodexRateLimitWait
	case statusCode == http.StatusPaymentRequired && containsAny("subscription", "deactivated_workspace", "deactivated workspace"):
		disposition.MarkDead = true
		disposition.HealthStatus = "dead"
	case source == managedGitLabCodexErrorSourceGatewayRequest &&
		(statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden):
		disposition.RateLimit = true
		disposition.HealthStatus = "gateway_rejected"
		disposition.Cooldown = managedGitLabCodexGatewayRejectWait
	case source == managedGitLabCodexErrorSourceGatewayRequest &&
		statusCode == http.StatusPaymentRequired:
		disposition.RateLimit = true
		disposition.HealthStatus = "payment_required"
		disposition.Cooldown = managedGitLabCodexPaymentRequiredWait
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusPaymentRequired:
		disposition.MarkDead = true
		disposition.HealthStatus = "dead"
	}
	if disposition.Reason == "" {
		disposition.Reason = firstNonEmpty(strings.TrimSpace(headers.Get("Retry-After")), http.StatusText(statusCode))
	}
	return disposition
}

func applyManagedGitLabCodexDisposition(acc *Account, disposition managedGitLabCodexErrorDisposition, headers http.Header, now time.Time) {
	if acc == nil {
		return
	}

	reason := sanitizeStatusMessage(firstNonEmpty(disposition.Reason, "gitlab codex request failed"))
	acc.mu.Lock()
	defer acc.mu.Unlock()

	acc.HealthCheckedAt = now
	acc.HealthError = reason

	if disposition.RateLimit {
		wait := disposition.Cooldown
		if disposition.HealthStatus == "quota_exceeded" {
			acc.GitLabQuotaExceededCount++
			acc.GitLabLastQuotaExceededAt = now
			wait = gitLabCodexQuotaExceededCooldown(acc.GitLabQuotaExceededCount)
		}
		if retryAfter, ok := parseRetryAfter(headers); ok && retryAfter > 0 {
			wait = retryAfter
		}
		if wait <= 0 {
			wait = managedGitLabCodexRateLimitWait
		}
		until := now.Add(wait).UTC()
		if acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until
		}
		setAccountDeadStateLocked(acc, false, now)
		acc.HealthStatus = firstNonEmpty(strings.TrimSpace(disposition.HealthStatus), "rate_limited")
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

func refreshGitLabCodexAccess(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	if acc == nil {
		return fmt.Errorf("nil account")
	}
	if transport == nil {
		transport = http.DefaultTransport
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
		return markGitLabCodexRefreshError(acc, now, err.Error(), false)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	applyGitLabDirectAccessHeaders(acc, resp.Header, now)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := firstNonEmpty(extractGitLabClaudeErrorSummary(body), resp.Status)
		return applyGitLabCodexDirectAccessFailure(acc, now, resp.StatusCode, resp.Header, reason)
	}

	var payload struct {
		Token     string            `json:"token"`
		BaseURL   string            `json:"base_url"`
		Headers   map[string]string `json:"headers"`
		ExpiresAt int64             `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return markGitLabCodexRefreshError(acc, now, fmt.Sprintf("decode gitlab direct access response: %v", err), true)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return markGitLabCodexRefreshError(acc, now, "gitlab direct access response did not include token", true)
	}
	headersCopy := copyStringMap(payload.Headers)
	if len(headersCopy) == 0 {
		return markGitLabCodexRefreshError(acc, now, "gitlab direct access response did not include gateway headers", true)
	}

	expiresAt := now.Add(managedGitLabClaudeDefaultTTL)
	if payload.ExpiresAt > 0 {
		expiresAt = time.Unix(payload.ExpiresAt, 0).UTC()
	}

	acc.mu.Lock()
	acc.AccessToken = strings.TrimSpace(payload.Token)
	acc.SourceBaseURL = instanceURL
	acc.UpstreamBaseURL = firstNonEmpty(strings.TrimSpace(payload.BaseURL), defaultGitLabCodexGatewayURL)
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

func markGitLabCodexRefreshError(acc *Account, now time.Time, message string, clearGatewayState bool) error {
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

func applyGitLabCodexDirectAccessFailure(acc *Account, now time.Time, statusCode int, headers http.Header, reason string) error {
	if acc == nil {
		return fmt.Errorf("gitlab direct access failed: %s", firstNonEmpty(strings.TrimSpace(reason), http.StatusText(statusCode)))
	}

	reason = sanitizeStatusMessage(firstNonEmpty(strings.TrimSpace(reason), http.StatusText(statusCode)))
	acc.mu.Lock()
	defer acc.mu.Unlock()

	switch statusCode {
	case http.StatusTooManyRequests:
		wait := managedGitLabClaudeRateLimitWait
		if retryAfter, ok := parseRetryAfter(headers); ok && retryAfter > 0 {
			wait = retryAfter
		}
		acc.RateLimitUntil = now.Add(wait).UTC()
		acc.HealthStatus = "rate_limited"
		acc.HealthError = reason
		acc.HealthCheckedAt = now
		acc.Penalty += 1.0
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired:
		acc.AccessToken = ""
		acc.ExtraHeaders = nil
		acc.ExpiresAt = time.Time{}
		markAccountDeadWithReasonLocked(acc, now, 100.0, reason)
	default:
		acc.HealthStatus = "error"
		acc.HealthError = reason
		acc.HealthCheckedAt = now
		acc.Penalty += 0.3
	}

	return fmt.Errorf("gitlab direct access failed: %s", reason)
}

func saveGitLabCodexAccount(a *Account) error {
	if a == nil {
		return fmt.Errorf("nil account")
	}
	root, err := loadGitLabClaudeRoot(a.File, false)
	if err != nil {
		if os.IsNotExist(err) {
			root = make(map[string]any)
		} else {
			return err
		}
	}

	root["plan_type"] = firstNonEmpty(strings.TrimSpace(a.PlanType), "gitlab_duo")
	root["auth_mode"] = accountAuthModeGitLab
	root["gitlab_token"] = strings.TrimSpace(a.RefreshToken)
	root["gitlab_instance_url"] = firstNonEmpty(strings.TrimSpace(a.SourceBaseURL), defaultGitLabInstanceURL)
	setJSONField(root, "gitlab_gateway_token", strings.TrimSpace(a.AccessToken), strings.TrimSpace(a.AccessToken) != "")
	setJSONField(root, "gitlab_gateway_base_url", firstNonEmpty(strings.TrimSpace(a.UpstreamBaseURL), defaultGitLabCodexGatewayURL), strings.TrimSpace(firstNonEmpty(strings.TrimSpace(a.UpstreamBaseURL), defaultGitLabCodexGatewayURL)) != "")
	setJSONField(root, "gitlab_gateway_headers", copyStringMap(a.ExtraHeaders), len(a.ExtraHeaders) > 0)
	setJSONTimeField(root, "gitlab_gateway_expires_at", a.ExpiresAt)
	setJSONField(root, "gitlab_rate_limit_name", strings.TrimSpace(a.GitLabRateLimitName), strings.TrimSpace(a.GitLabRateLimitName) != "")
	setJSONField(root, "gitlab_rate_limit_limit", a.GitLabRateLimitLimit, a.GitLabRateLimitLimit > 0)
	setJSONField(root, "gitlab_rate_limit_remaining", a.GitLabRateLimitRemaining, a.GitLabRateLimitLimit > 0 || a.GitLabRateLimitRemaining > 0)
	setJSONTimeField(root, "gitlab_rate_limit_reset_at", a.GitLabRateLimitResetAt)
	setJSONField(root, "gitlab_quota_exceeded_count", a.GitLabQuotaExceededCount, a.GitLabQuotaExceededCount > 0)
	setJSONTimeField(root, "gitlab_last_quota_exceeded_at", a.GitLabLastQuotaExceededAt)
	setJSONTimeField(root, "rate_limit_until", a.RateLimitUntil)
	setJSONTimeField(root, "last_refresh", a.LastRefresh)
	setJSONField(root, "disabled", true, a.Disabled)
	setJSONField(root, "dead", true, a.Dead)
	setJSONField(root, "health_status", strings.TrimSpace(a.HealthStatus), strings.TrimSpace(a.HealthStatus) != "")
	setJSONField(root, "health_error", sanitizeStatusMessage(a.HealthError), strings.TrimSpace(a.HealthError) != "")
	setJSONTimeField(root, "health_checked_at", a.HealthCheckedAt)
	setJSONTimeField(root, "last_healthy_at", a.LastHealthyAt)
	setJSONTimeField(root, "dead_since", a.DeadSince)

	delete(root, "OPENAI_API_KEY")
	delete(root, "tokens")
	return atomicWriteJSON(a.File, root)
}
