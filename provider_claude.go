package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeProvider handles Anthropic Claude accounts.
type ClaudeProvider struct {
	claudeBase *url.URL
}

// NewClaudeProvider creates a new Claude provider.
func NewClaudeProvider(claudeBase *url.URL) *ClaudeProvider {
	return &ClaudeProvider{
		claudeBase: claudeBase,
	}
}

func (p *ClaudeProvider) Type() AccountType {
	return AccountTypeClaude
}

func (p *ClaudeProvider) LoadAccount(name, path string, data []byte) (*Account, error) {
	var cj ClaudeAuthJSON
	if err := json.Unmarshal(data, &cj); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if strings.TrimSpace(cj.GitLabToken) != "" {
		acc := &Account{
			Type:                      AccountTypeClaude,
			ID:                        strings.TrimSuffix(name, filepath.Ext(name)),
			File:                      path,
			AccessToken:               strings.TrimSpace(cj.GitLabGatewayToken),
			RefreshToken:              strings.TrimSpace(cj.GitLabToken),
			PlanType:                  firstNonEmpty(strings.TrimSpace(cj.PlanType), "gitlab_duo"),
			AuthMode:                  accountAuthModeGitLab,
			Disabled:                  cj.Disabled,
			Dead:                      cj.Dead,
			HealthStatus:              strings.TrimSpace(cj.HealthStatus),
			HealthError:               strings.TrimSpace(cj.HealthError),
			SourceBaseURL:             firstNonEmpty(strings.TrimSpace(cj.GitLabInstanceURL), defaultGitLabInstanceURL),
			UpstreamBaseURL:           firstNonEmpty(strings.TrimSpace(cj.GitLabGatewayBaseURL), defaultGitLabClaudeGatewayURL),
			ExtraHeaders:              copyStringMap(cj.GitLabGatewayHeaders),
			GitLabRateLimitName:       strings.TrimSpace(cj.GitLabRateLimitName),
			GitLabRateLimitLimit:      cj.GitLabRateLimitLimit,
			GitLabRateLimitRemaining:  cj.GitLabRateLimitRemaining,
			GitLabRateLimitResetAt:    cj.GitLabRateLimitResetAt,
			GitLabQuotaExceededCount:  cj.GitLabQuotaExceededCount,
			GitLabLastQuotaExceededAt: cj.GitLabLastQuotaExceededAt,
			RateLimitUntil:            cj.RateLimitUntil,
		}
		if cj.LastRefresh != nil {
			acc.LastRefresh = cj.LastRefresh.UTC()
		}
		if !cj.GitLabGatewayExpiresAt.IsZero() {
			acc.ExpiresAt = cj.GitLabGatewayExpiresAt
		}
		if cj.HealthCheckedAt != nil {
			acc.HealthCheckedAt = *cj.HealthCheckedAt
		}
		if cj.LastHealthyAt != nil {
			acc.LastHealthyAt = *cj.LastHealthyAt
		}
		if cj.DeadSince != nil {
			acc.DeadSince = cj.DeadSince.UTC()
		}
		if acc.GitLabQuotaExceededCount == 0 && acc.HealthStatus == "quota_exceeded" && !acc.RateLimitUntil.IsZero() {
			acc.GitLabQuotaExceededCount = 1
		}
		if acc.HealthStatus == "quota_exceeded" {
			acc.Dead = true
			acc.HealthStatus = "dead"
			acc.RateLimitUntil = time.Time{}
			if acc.DeadSince.IsZero() {
				switch {
				case !acc.HealthCheckedAt.IsZero():
					acc.DeadSince = acc.HealthCheckedAt.UTC()
				case !acc.LastRefresh.IsZero():
					acc.DeadSince = acc.LastRefresh.UTC()
				}
			}
		}
		if acc.HealthStatus == "" {
			acc.HealthStatus = "unknown"
		}
		return acc, nil
	}

	acc := &Account{
		Type: AccountTypeClaude,
		ID:   strings.TrimSuffix(name, filepath.Ext(name)),
		File: path,
	}

	// Load last_refresh from root level (for rate limiting across restarts)
	var root map[string]any
	if err := json.Unmarshal(data, &root); err == nil {
		if lr, ok := root["last_refresh"].(string); ok && lr != "" {
			if t, err := time.Parse(time.RFC3339Nano, lr); err == nil {
				acc.LastRefresh = t
			} else if t, err := time.Parse(time.RFC3339, lr); err == nil {
				acc.LastRefresh = t
			}
		}
	}

	// Check for OAuth format first (from Claude Code keychain)
	if cj.ClaudeAiOauth != nil && cj.ClaudeAiOauth.AccessToken != "" {
		acc.AccessToken = cj.ClaudeAiOauth.AccessToken
		acc.RefreshToken = cj.ClaudeAiOauth.RefreshToken
		if cj.ClaudeAiOauth.ExpiresAt > 0 {
			acc.ExpiresAt = time.UnixMilli(cj.ClaudeAiOauth.ExpiresAt)
		}
		acc.PlanType = cj.ClaudeAiOauth.SubscriptionType
		if acc.PlanType == "" {
			acc.PlanType = "claude"
		}
		return acc, nil
	}

	// Fall back to API key format
	if cj.APIKey == "" {
		return nil, nil
	}
	acc.AccessToken = cj.APIKey
	acc.PlanType = cj.PlanType
	if acc.PlanType == "" {
		acc.PlanType = "claude"
	}
	return acc, nil
}

func (p *ClaudeProvider) SetAuthHeaders(req *http.Request, acc *Account) {
	if isGitLabClaudeAccount(acc) {
		req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
		for key, value := range acc.ExtraHeaders {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				continue
			}
			req.Header.Set(key, value)
		}
		return
	}

	// OAuth tokens start with sk-ant-oat, API keys with sk-ant-api
	if strings.HasPrefix(acc.AccessToken, "sk-ant-oat") {
		req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	} else {
		req.Header.Set("X-Api-Key", acc.AccessToken)
	}
}

func (p *ClaudeProvider) RefreshToken(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	if isGitLabClaudeAccount(acc) {
		return refreshGitLabClaudeAccess(ctx, acc, transport)
	}

	// Only OAuth tokens (not API keys) can be refreshed
	if !strings.HasPrefix(acc.AccessToken, "sk-ant-oat") {
		// API keys don't need refresh
		return nil
	}

	return RefreshClaudeAccountTokens(acc)
}

func (p *ClaudeProvider) ParseUsage(obj map[string]any) *RequestUsage {
	eventType, _ := obj["type"].(string)

	if eventType == "message_delta" {
		return parseAnthropicMessageDeltaUsage(obj)
	}

	if eventType == "message_start" {
		return parseAnthropicMessageStartUsage(obj)
	}

	// Non-stream Anthropic responses include a top-level usage object rather than SSE events.
	return parseOpenAIUsagePayload(obj)
}

func (p *ClaudeProvider) ParseUsageHeaders(acc *Account, headers http.Header) {
	// Claude usage should come from the periodic /api/oauth/usage poller only.
	_ = acc
	_ = headers
}

func (p *ClaudeProvider) UpstreamURL(path string) *url.URL {
	return p.claudeBase
}

func (p *ClaudeProvider) UpstreamURLForAccount(path string, acc *Account) *url.URL {
	if isGitLabClaudeAccount(acc) {
		if parsed, err := url.Parse(firstNonEmpty(strings.TrimSpace(acc.UpstreamBaseURL), defaultGitLabClaudeGatewayURL)); err == nil {
			return parsed
		}
	}
	return p.UpstreamURL(path)
}

func (p *ClaudeProvider) MatchesPath(path string) bool {
	return strings.HasPrefix(path, "/v1/messages")
}

func (p *ClaudeProvider) NormalizePath(path string) string {
	// Claude paths don't need normalization
	return path
}

func (p *ClaudeProvider) DetectsSSE(path string, contentType string) bool {
	// Claude uses text/event-stream content type for SSE
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}
