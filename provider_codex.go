package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// CodexProvider handles OpenAI Codex accounts.
type CodexProvider struct {
	responsesBase *url.URL
	whamBase      *url.URL
	refreshBase   *url.URL
	apiBase       *url.URL
}

type codexCurrentAccessProbeResult struct {
	StatusCode int
	Working    bool
	MarkDead   bool
	Reason     string
}

// NewCodexProvider creates a new Codex provider.
func NewCodexProvider(responsesBase, whamBase, refreshBase, apiBase *url.URL) *CodexProvider {
	return &CodexProvider{
		responsesBase: responsesBase,
		whamBase:      whamBase,
		refreshBase:   refreshBase,
		apiBase:       apiBase,
	}
}

func (p *CodexProvider) Type() AccountType {
	return AccountTypeCodex
}

func (p *CodexProvider) LoadAccount(name, path string, data []byte) (*Account, error) {
	var aj CodexAuthJSON
	if err := json.Unmarshal(data, &aj); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(aj.GitLabToken) != "" {
		acc := &Account{
			Type:                     AccountTypeCodex,
			ID:                       strings.TrimSuffix(name, filepath.Ext(name)),
			File:                     path,
			AccessToken:              strings.TrimSpace(aj.GitLabGatewayToken),
			RefreshToken:             strings.TrimSpace(aj.GitLabToken),
			PlanType:                 firstNonEmpty(strings.TrimSpace(aj.PlanType), "gitlab_duo"),
			AuthMode:                 accountAuthModeGitLab,
			Disabled:                 aj.Disabled,
			Dead:                     aj.Dead,
			HealthStatus:             strings.TrimSpace(aj.HealthStatus),
			HealthError:              strings.TrimSpace(aj.HealthError),
			SourceBaseURL:            firstNonEmpty(strings.TrimSpace(aj.GitLabInstanceURL), defaultGitLabInstanceURL),
			UpstreamBaseURL:          firstNonEmpty(strings.TrimSpace(aj.GitLabGatewayBaseURL), defaultGitLabCodexGatewayURL),
			ExtraHeaders:             copyStringMap(aj.GitLabGatewayHeaders),
			GitLabRateLimitName:      strings.TrimSpace(aj.GitLabRateLimitName),
			GitLabRateLimitLimit:     aj.GitLabRateLimitLimit,
			GitLabRateLimitRemaining: aj.GitLabRateLimitRemaining,
			GitLabQuotaExceededCount: aj.GitLabQuotaExceededCount,
		}
		if aj.LastRefresh != nil {
			acc.LastRefresh = aj.LastRefresh.UTC()
		}
		if aj.GitLabGatewayExpiresAt != nil {
			acc.ExpiresAt = aj.GitLabGatewayExpiresAt.UTC()
		}
		if aj.GitLabRateLimitResetAt != nil {
			acc.GitLabRateLimitResetAt = aj.GitLabRateLimitResetAt.UTC()
		}
		if aj.GitLabLastQuotaExceededAt != nil {
			acc.GitLabLastQuotaExceededAt = aj.GitLabLastQuotaExceededAt.UTC()
		}
		if aj.RateLimitUntil != nil {
			acc.RateLimitUntil = aj.RateLimitUntil.UTC()
		}
		if aj.HealthCheckedAt != nil {
			acc.HealthCheckedAt = aj.HealthCheckedAt.UTC()
		}
		if aj.LastHealthyAt != nil {
			acc.LastHealthyAt = aj.LastHealthyAt.UTC()
		}
		if aj.DeadSince != nil {
			acc.DeadSince = aj.DeadSince.UTC()
		}
		if acc.HealthStatus == "" {
			acc.HealthStatus = "unknown"
		}
		return acc, nil
	}
	if aj.OpenAIKey != nil && strings.TrimSpace(*aj.OpenAIKey) != "" {
		acc := &Account{
			Type:        AccountTypeCodex,
			ID:          strings.TrimSuffix(name, filepath.Ext(name)),
			File:        path,
			AccessToken: strings.TrimSpace(*aj.OpenAIKey),
			PlanType:    firstNonEmpty(strings.TrimSpace(aj.PlanType), "api"),
			AuthMode:    accountAuthModeAPIKey,
			Disabled:    aj.Disabled,
			Dead:        aj.Dead,
		}
		if strings.TrimSpace(aj.AuthMode) != "" {
			acc.AuthMode = strings.TrimSpace(aj.AuthMode)
		}
		if aj.HealthCheckedAt != nil {
			acc.HealthCheckedAt = *aj.HealthCheckedAt
		}
		if aj.LastHealthyAt != nil {
			acc.LastHealthyAt = *aj.LastHealthyAt
		}
		if aj.DeadSince != nil {
			acc.DeadSince = aj.DeadSince.UTC()
		}
		acc.HealthStatus = strings.TrimSpace(aj.HealthStatus)
		acc.HealthError = strings.TrimSpace(aj.HealthError)
		return acc, nil
	}
	if aj.Tokens == nil {
		return nil, nil
	}
	acc := &Account{
		Type:         AccountTypeCodex,
		ID:           strings.TrimSuffix(name, filepath.Ext(name)),
		File:         path,
		AccessToken:  aj.Tokens.AccessToken,
		RefreshToken: aj.Tokens.RefreshToken,
		IDToken:      aj.Tokens.IDToken,
		Disabled:     aj.Disabled,
	}
	if aj.Tokens.AccountID != nil {
		acc.AccountID = strings.TrimSpace(*aj.Tokens.AccountID)
	}
	claims := parseCodexClaims(aj.Tokens.IDToken)
	acc.IDTokenChatGPTAccountID = claims.ChatGPTAccountID
	if acc.AccountID == "" && acc.IDTokenChatGPTAccountID != "" {
		acc.AccountID = acc.IDTokenChatGPTAccountID
	}
	acc.PlanType = claims.PlanType
	acc.AuthMode = accountAuthModeOAuth
	acc.ExpiresAt = claims.ExpiresAt
	if acc.ExpiresAt.IsZero() && aj.LastRefresh != nil {
		acc.ExpiresAt = aj.LastRefresh.Add(20 * time.Hour)
	}
	if aj.LastRefresh != nil {
		acc.LastRefresh = *aj.LastRefresh
	}
	acc.Dead = aj.Dead
	if aj.DeadSince != nil {
		acc.DeadSince = aj.DeadSince.UTC()
	}
	if aj.HealthCheckedAt != nil {
		acc.HealthCheckedAt = aj.HealthCheckedAt.UTC()
	}
	if aj.LastHealthyAt != nil {
		acc.LastHealthyAt = aj.LastHealthyAt.UTC()
	}
	acc.HealthStatus = strings.TrimSpace(aj.HealthStatus)
	acc.HealthError = strings.TrimSpace(aj.HealthError)
	if acc.Dead && acc.HealthStatus == "" {
		acc.HealthStatus = "dead"
	}
	return acc, nil
}

func (p *CodexProvider) SetAuthHeaders(req *http.Request, acc *Account) {
	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	if isGitLabCodexAccount(acc) {
		for key, value := range acc.ExtraHeaders {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			req.Header.Set(key, value)
		}
		return
	}
	if isManagedCodexAPIKeyAccount(acc) {
		return
	}
	// ChatGPT Account ID needed for some endpoints
	chatgptAccID := acc.AccountID
	if chatgptAccID == "" {
		chatgptAccID = acc.IDTokenChatGPTAccountID
	}
	if chatgptAccID != "" {
		req.Header.Set("ChatGPT-Account-ID", chatgptAccID)
	}
}

func (p *CodexProvider) RefreshToken(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()

	if isGitLabCodexAccount(acc) {
		err := refreshGitLabCodexAccess(ctx, acc, transport)
		status := "ok"
		if err != nil {
			status = "fail"
		}
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", status, time.Since(startedAt), err)
		return err
	}

	acc.mu.Lock()
	refreshTok := acc.RefreshToken
	acc.mu.Unlock()

	if refreshTok == "" {
		err := errors.New("no refresh token")
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
		return err
	}

	// Match Codex behavior: JSON body, Content-Type: application/json
	body := map[string]string{
		"client_id":     "app_EMoamEEZ73f0CkXaXp7hrann",
		"grant_type":    "refresh_token",
		"refresh_token": refreshTok,
		"scope":         "openid profile email",
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}
	refreshURL := p.refreshBase.ResolveReference(&url.URL{Path: "/oauth/token"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL.String(), bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-pool-proxy")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		if len(bytes.TrimSpace(msg)) > 0 {
			err = fmt.Errorf("refresh unauthorized: %s: %s", resp.Status, safeText(msg))
			trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
			return err
		}
		err = fmt.Errorf("refresh unauthorized: %s", resp.Status)
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		if len(bytes.TrimSpace(msg)) > 0 {
			err = fmt.Errorf("refresh failed: %s: %s", resp.Status, safeText(msg))
			trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
			return err
		}
		err = fmt.Errorf("refresh failed: %s", resp.Status)
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
		return err
	}

	var payload struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
		return err
	}
	if payload.AccessToken == "" {
		err := errors.New("empty access token after refresh")
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", "fail", time.Since(startedAt), err)
		return err
	}

	now := time.Now().UTC()
	acc.mu.Lock()
	acc.AccessToken = payload.AccessToken
	if payload.RefreshToken != "" {
		acc.RefreshToken = payload.RefreshToken
	}
	if payload.IDToken != "" {
		acc.IDToken = payload.IDToken
		claims := parseCodexClaims(payload.IDToken)
		if !claims.ExpiresAt.IsZero() {
			acc.ExpiresAt = claims.ExpiresAt
		}
		if claims.ChatGPTAccountID != "" {
			acc.IDTokenChatGPTAccountID = claims.ChatGPTAccountID
			if acc.AccountID == "" {
				acc.AccountID = claims.ChatGPTAccountID
			}
		}
		if claims.PlanType != "" {
			acc.PlanType = claims.PlanType
		}
	}
	acc.LastRefresh = now
	setAccountDeadStateLocked(acc, false, acc.LastRefresh)
	clearCodexRefreshInvalidStateLocked(acc, now)
	acc.mu.Unlock()

	if err := saveAccount(acc); err != nil {
		trace.noteTokenRefresh(AccountTypeCodex, acc, "", "persist_fail", time.Since(startedAt), err)
		return err
	}

	trace.noteTokenRefresh(AccountTypeCodex, acc, "", "ok", time.Since(startedAt), nil)
	return nil
}

func (p *CodexProvider) ParseUsage(obj map[string]any) *RequestUsage {
	return parseCodexUsageDelta(obj).Usage
}

func isCodexRefreshTokenInvalidError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "invalid_grant") || strings.Contains(msg, "refresh_token_reused")
}

func (h *proxyHandler) probeCodexCurrentAccess(ctx context.Context, acc *Account) (codexCurrentAccessProbeResult, error) {
	var result codexCurrentAccessProbeResult
	if h == nil || acc == nil {
		return result, errors.New("nil codex account")
	}
	if h.registry == nil {
		return result, errors.New("missing provider registry")
	}
	provider, ok := h.registry.ForType(AccountTypeCodex).(*CodexProvider)
	if !ok || provider == nil {
		return result, errors.New("codex provider unavailable")
	}

	const path = "/backend-api/codex/models"
	targetBase := providerUpstreamURLForAccount(provider, path, acc)
	if targetBase == nil {
		return result, errors.New("codex models probe base unavailable")
	}
	outURL := *targetBase
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, path, acc))
	outURL.RawQuery = ""
	ensureCodexModelsQueryDefaults(&outURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, outURL.String(), nil)
	if err != nil {
		return result, err
	}
	req.Header = make(http.Header)
	req.Header.Set("Accept", "application/json")
	removeConflictingProxyHeaders(req.Header)
	provider.SetAuthHeaders(req, acc)

	transport := h.transport
	if transport == nil {
		transport = h.refreshTransport
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if err != nil {
		return result, err
	}
	result.StatusCode = resp.StatusCode
	text := strings.TrimSpace(safeText(body))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		result.Working = true
	case resp.StatusCode == http.StatusTooManyRequests:
		result.Working = true
		result.Reason = firstNonEmpty(text, resp.Status)
	case resp.StatusCode == http.StatusPaymentRequired && strings.Contains(strings.ToLower(text), "subscription"):
		result.MarkDead = true
		result.Reason = firstNonEmpty(text, "codex upstream subscription required")
	case isPermanentCodexAuthFailure(resp, body):
		result.MarkDead = true
		result.Reason = firstNonEmpty(text, "codex upstream account_deactivated")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		result.MarkDead = true
		result.Reason = firstNonEmpty(text, fmt.Sprintf("codex current access unauthorized: %s", resp.Status))
	default:
		result.Reason = firstNonEmpty(text, fmt.Sprintf("codex current access probe failed: %s", resp.Status))
	}
	return result, nil
}

func (p *CodexProvider) ParseUsageHeaders(acc *Account, headers http.Header) {
	applyUsageSnapshot(acc, parseCodexUsageHeadersSnapshot(headers, time.Now()))
}

func (p *CodexProvider) UpstreamURL(path string) *url.URL {
	if strings.HasPrefix(path, "/backend-api/") {
		return p.whamBase
	}
	return p.responsesBase
}

func (p *CodexProvider) UpstreamURLForAccount(path string, acc *Account) *url.URL {
	if isGitLabCodexAccount(acc) {
		if parsed, err := url.Parse(firstNonEmpty(strings.TrimSpace(acc.UpstreamBaseURL), defaultGitLabCodexGatewayURL)); err == nil {
			return parsed
		}
	}
	if isManagedCodexAPIKeyAccount(acc) && p.apiBase != nil {
		return p.apiBase
	}
	return p.UpstreamURL(path)
}

func (p *CodexProvider) MatchesPath(path string) bool {
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/responses") ||
		strings.HasPrefix(path, "/ws") ||
		strings.HasPrefix(path, "/backend-api/") ||
		strings.HasPrefix(path, "/api/codex/")
}

func (p *CodexProvider) NormalizePath(path string) string {
	// Map /v1/responses/* and /responses/* to the correct upstream path
	if strings.HasPrefix(path, "/v1/responses") || strings.HasPrefix(path, "/responses") {
		return mapResponsesPath(path)
	}
	// If caller already included /backend-api in the request path, avoid
	// duplicating it when we join against upstreams that also include /backend-api.
	if strings.HasPrefix(path, "/backend-api/") {
		trimmed := strings.TrimPrefix(path, "/backend-api")
		if trimmed == "" {
			return "/"
		}
		return trimmed
	}
	return path
}

func (p *CodexProvider) NormalizePathForAccount(path string, acc *Account) string {
	if isGitLabCodexAccount(acc) {
		switch {
		case strings.HasPrefix(path, "/responses"):
			return "/v1" + path
		case strings.HasPrefix(path, "/v1/"):
			return path
		default:
			return path
		}
	}
	if !isManagedCodexAPIKeyAccount(acc) {
		return p.NormalizePath(path)
	}
	switch {
	case strings.HasPrefix(path, "/responses"):
		return "/v1" + path
	case strings.HasPrefix(path, "/v1/"):
		return path
	default:
		return path
	}
}

func (p *CodexProvider) SupportsAccountPath(path string, acc *Account) bool {
	if isGitLabCodexAccount(acc) {
		return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/responses")
	}
	if !isManagedCodexAPIKeyAccount(acc) {
		return true
	}
	return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/responses")
}

func (p *CodexProvider) DetectsSSE(path string, contentType string) bool {
	// Responses paths are always SSE
	if strings.HasPrefix(path, "/responses/") || strings.HasPrefix(path, "/v1/") {
		return true
	}
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// parseCodexClaims extracts claims from a Codex JWT ID token.
func parseCodexClaims(idToken string) codexJWTClaims {
	var out codexJWTClaims
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
	if acc, ok := payload["chatgpt_account_id"].(string); ok {
		out.ChatGPTAccountID = acc
	}
	if auth, ok := payload["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if acc, ok := auth["chatgpt_account_id"].(string); ok && acc != "" {
			out.ChatGPTAccountID = acc
		}
		if userID, ok := auth["chatgpt_user_id"].(string); ok {
			out.ChatGPTUserID = userID
		}
		if plan, ok := auth["chatgpt_plan_type"].(string); ok {
			out.PlanType = plan
		}
	}
	if profile, ok := payload["https://api.openai.com/profile"].(map[string]interface{}); ok {
		if email, ok := profile["email"].(string); ok {
			out.Email = email
		}
	}
	if out.Email == "" {
		if email, ok := payload["email"].(string); ok {
			out.Email = email
		}
	}
	if sub, ok := payload["sub"].(string); ok {
		out.Subject = sub
	}
	if out.PlanType == "" {
		out.PlanType = "pro"
	}
	return out
}

func providerUpstreamURLForAccount(provider Provider, path string, acc *Account) *url.URL {
	if codexProvider, ok := provider.(*CodexProvider); ok {
		return codexProvider.UpstreamURLForAccount(path, acc)
	}
	if claudeProvider, ok := provider.(*ClaudeProvider); ok {
		return claudeProvider.UpstreamURLForAccount(path, acc)
	}
	return provider.UpstreamURL(path)
}

func providerNormalizePathForAccount(provider Provider, path string, acc *Account) string {
	if codexProvider, ok := provider.(*CodexProvider); ok {
		return codexProvider.NormalizePathForAccount(path, acc)
	}
	return provider.NormalizePath(path)
}

func providerSupportsPathForAccount(provider Provider, path string, acc *Account) bool {
	if codexProvider, ok := provider.(*CodexProvider); ok {
		return codexProvider.SupportsAccountPath(path, acc)
	}
	if geminiProvider, ok := provider.(*GeminiProvider); ok {
		return geminiProvider.SupportsAccountPath(path, acc)
	}
	return true
}

type codexJWTClaims struct {
	ExpiresAt        time.Time
	ChatGPTAccountID string
	ChatGPTUserID    string
	PlanType         string
	Email            string
	Subject          string
}
