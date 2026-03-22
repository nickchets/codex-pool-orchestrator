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
	"strings"
	"time"
)

const (
	managedGitLabClaudeSubdir        = "claude_gitlab"
	defaultGitLabInstanceURL         = "https://gitlab.com"
	defaultGitLabClaudeGatewayURL    = "https://cloud.gitlab.com/ai/v1/proxy/anthropic"
	managedGitLabClaudeDirectAccess  = "/api/v4/ai/third_party_agents/direct_access"
	managedGitLabClaudeDefaultTTL    = 20 * time.Minute
	managedGitLabClaudeRateLimitWait = 15 * time.Minute
)

type managedGitLabClaudeErrorDisposition struct {
	MarkDead  bool
	RateLimit bool
	Reason    string
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

	root := ClaudeAuthJSON{
		PlanType:          "gitlab_duo",
		AuthMode:          accountAuthModeGitLab,
		GitLabToken:       token,
		GitLabInstanceURL: normalizedInstanceURL,
		HealthStatus:      "unknown",
	}
	if err := atomicWriteJSON(path, root); err != nil {
		return nil, false, err
	}

	return &Account{
		Type:            AccountTypeClaude,
		ID:              accountID,
		File:            path,
		RefreshToken:    token,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		HealthStatus:    "unknown",
		SourceBaseURL:   normalizedInstanceURL,
		UpstreamBaseURL: defaultGitLabClaudeGatewayURL,
	}, created, nil
}

func saveGitLabClaudeAccount(a *Account) error {
	root := ClaudeAuthJSON{
		PlanType:             firstNonEmpty(strings.TrimSpace(a.PlanType), "gitlab_duo"),
		AuthMode:             accountAuthModeGitLab,
		GitLabToken:          strings.TrimSpace(a.RefreshToken),
		GitLabInstanceURL:    firstNonEmpty(strings.TrimSpace(a.SourceBaseURL), defaultGitLabInstanceURL),
		GitLabGatewayToken:   strings.TrimSpace(a.AccessToken),
		GitLabGatewayBaseURL: firstNonEmpty(strings.TrimSpace(a.UpstreamBaseURL), defaultGitLabClaudeGatewayURL),
		GitLabGatewayHeaders: copyStringMap(a.ExtraHeaders),
		Disabled:             a.Disabled,
		Dead:                 a.Dead,
		HealthStatus:         strings.TrimSpace(a.HealthStatus),
		HealthError:          sanitizeStatusMessage(a.HealthError),
	}
	if !a.ExpiresAt.IsZero() {
		root.GitLabGatewayExpiresAt = a.ExpiresAt.UTC()
	}
	if !a.HealthCheckedAt.IsZero() {
		value := a.HealthCheckedAt.UTC()
		root.HealthCheckedAt = &value
	}
	if !a.LastHealthyAt.IsZero() {
		value := a.LastHealthyAt.UTC()
		root.LastHealthyAt = &value
	}
	if !a.LastRefresh.IsZero() {
		var existing map[string]any
		if raw, err := os.ReadFile(a.File); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &existing)
		}
		if existing == nil {
			existing = make(map[string]any)
		}
		existing["plan_type"] = root.PlanType
		existing["auth_mode"] = root.AuthMode
		existing["gitlab_token"] = root.GitLabToken
		existing["gitlab_instance_url"] = root.GitLabInstanceURL
		existing["gitlab_gateway_token"] = root.GitLabGatewayToken
		existing["gitlab_gateway_base_url"] = root.GitLabGatewayBaseURL
		if len(root.GitLabGatewayHeaders) > 0 {
			existing["gitlab_gateway_headers"] = root.GitLabGatewayHeaders
		} else {
			delete(existing, "gitlab_gateway_headers")
		}
		if !root.GitLabGatewayExpiresAt.IsZero() {
			existing["gitlab_gateway_expires_at"] = root.GitLabGatewayExpiresAt.UTC().Format(time.RFC3339Nano)
		} else {
			delete(existing, "gitlab_gateway_expires_at")
		}
		if root.Disabled {
			existing["disabled"] = true
		} else {
			delete(existing, "disabled")
		}
		if root.Dead {
			existing["dead"] = true
		} else {
			delete(existing, "dead")
		}
		if root.HealthStatus != "" {
			existing["health_status"] = root.HealthStatus
		} else {
			delete(existing, "health_status")
		}
		if root.HealthError != "" {
			existing["health_error"] = root.HealthError
		} else {
			delete(existing, "health_error")
		}
		if root.HealthCheckedAt != nil {
			existing["health_checked_at"] = root.HealthCheckedAt.UTC().Format(time.RFC3339Nano)
		} else {
			delete(existing, "health_checked_at")
		}
		if root.LastHealthyAt != nil {
			existing["last_healthy_at"] = root.LastHealthyAt.UTC().Format(time.RFC3339Nano)
		} else {
			delete(existing, "last_healthy_at")
		}
		existing["last_refresh"] = a.LastRefresh.UTC().Format(time.RFC3339Nano)
		return atomicWriteJSON(a.File, existing)
	}
	return atomicWriteJSON(a.File, root)
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

	if err := h.refreshAccount(r.Context(), acc); err != nil {
		// State is already persisted by refreshAccount.
	}

	h.reloadAccounts()

	respondJSON(w, map[string]any{
		"status":        "ok",
		"account_id":    acc.ID,
		"created":       created,
		"instance_url":  firstNonEmpty(strings.TrimSpace(acc.SourceBaseURL), defaultGitLabInstanceURL),
		"health_status": firstNonEmpty(strings.TrimSpace(acc.HealthStatus), "unknown"),
		"health_error":  sanitizeStatusMessage(acc.HealthError),
		"dead":          acc.Dead,
		"auth_expires_at": func() string {
			if acc.ExpiresAt.IsZero() {
				return ""
			}
			return acc.ExpiresAt.UTC().Format(time.RFC3339)
		}(),
	})
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
		acc.mu.Lock()
		acc.HealthStatus = "error"
		acc.HealthError = sanitizeStatusMessage(err.Error())
		acc.HealthCheckedAt = now
		acc.Penalty += 0.3
		acc.mu.Unlock()
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		disposition := classifyManagedGitLabClaudeError(resp.StatusCode, resp.Header, body)
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
		return fmt.Errorf("decode gitlab direct access response: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return fmt.Errorf("gitlab direct access response did not include token")
	}
	headersCopy := copyStringMap(payload.Headers)
	if len(headersCopy) == 0 {
		return fmt.Errorf("gitlab direct access response did not include gateway headers")
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
	acc.Dead = false
	acc.RateLimitUntil = time.Time{}
	acc.HealthStatus = "healthy"
	acc.HealthError = ""
	acc.HealthCheckedAt = now
	acc.LastHealthyAt = now
	acc.mu.Unlock()
	return nil
}

func classifyManagedGitLabClaudeError(statusCode int, headers http.Header, body []byte) managedGitLabClaudeErrorDisposition {
	reason := extractGitLabClaudeErrorSummary(body)
	lower := strings.ToLower(reason)

	disposition := managedGitLabClaudeErrorDisposition{Reason: reason}
	switch {
	case statusCode == http.StatusTooManyRequests:
		disposition.RateLimit = true
	case strings.Contains(lower, "usage_quota_exceeded"),
		strings.Contains(lower, "usage quota exceeded"),
		strings.Contains(lower, "quota exceeded"),
		strings.Contains(lower, "rate limit"):
		disposition.RateLimit = true
	case statusCode == http.StatusUnauthorized:
		disposition.MarkDead = true
	case statusCode == http.StatusForbidden:
		disposition.MarkDead = true
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
		wait := managedGitLabClaudeRateLimitWait
		if retryAfter := strings.TrimSpace(headers.Get("Retry-After")); retryAfter != "" {
			if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil && seconds > 0 {
				wait = seconds
			}
		}
		until := now.Add(wait)
		if acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until
		}
		acc.Dead = false
		if strings.Contains(strings.ToLower(reason), "quota") {
			acc.HealthStatus = "quota_exceeded"
		} else {
			acc.HealthStatus = "rate_limited"
		}
		acc.Penalty += 0.5
		return
	}

	if disposition.MarkDead {
		acc.Dead = true
		acc.HealthStatus = "dead"
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
