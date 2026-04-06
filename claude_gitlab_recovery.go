package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	managedGitLabClaudeSharedTPMRecoveryPollInterval = 5 * time.Second
	managedGitLabClaudeSharedTPMCanaryLead           = 5 * time.Second
	managedGitLabClaudeSharedTPMCanaryTimeout        = 20 * time.Second
	managedGitLabClaudeSharedTPMCanaryRetryDelay     = 30 * time.Second
	managedGitLabClaudeDefaultCanaryModel            = "claude-opus-4-6"
)

type gitLabClaudeSharedTPMCanaryScope struct {
	scopeKey    string
	model       string
	nextProbeAt time.Time
	account     *Account
}

func managedGitLabClaudeCanaryModel(requestedModel string) string {
	return firstNonEmpty(strings.TrimSpace(requestedModel), managedGitLabClaudeDefaultCanaryModel)
}

func managedGitLabClaudeCanaryProbeAt(now, until time.Time) time.Time {
	if until.IsZero() {
		return time.Time{}
	}
	probeAt := until.Add(-managedGitLabClaudeSharedTPMCanaryLead)
	if probeAt.Before(now) {
		return now
	}
	return probeAt
}

func managedGitLabClaudeCanaryRetryAt(now, until time.Time) time.Time {
	if !until.After(now) {
		return time.Time{}
	}
	retryAt := now.Add(managedGitLabClaudeSharedTPMCanaryRetryDelay)
	if retryAt.After(until) {
		return until
	}
	return retryAt
}

func applyManagedGitLabClaudeSharedTPMRecoveryScheduleLocked(acc *Account, now, until time.Time, requestedModel string) bool {
	if acc == nil {
		return false
	}
	changed := false
	model := managedGitLabClaudeCanaryModel(requestedModel)
	probeAt := managedGitLabClaudeCanaryProbeAt(now, until)
	if strings.TrimSpace(acc.GitLabCanaryModel) != model {
		acc.GitLabCanaryModel = model
		changed = true
	}
	if !acc.GitLabCanaryNextProbeAt.Equal(probeAt) {
		acc.GitLabCanaryNextProbeAt = probeAt
		changed = true
	}
	if strings.TrimSpace(acc.GitLabCanaryLastResult) != "scheduled" {
		acc.GitLabCanaryLastResult = "scheduled"
		changed = true
	}
	if strings.TrimSpace(acc.GitLabCanaryLastError) != "" {
		acc.GitLabCanaryLastError = ""
		changed = true
	}
	return changed
}

func startOfGitLabClaudeRecoveryCandidate(now time.Time, acc *Account) (gitLabClaudeSharedTPMCanaryScope, bool) {
	if acc == nil || !isGitLabClaudeAccount(acc) {
		return gitLabClaudeSharedTPMCanaryScope{}, false
	}
	scopeKey := gitLabClaudeScopeKey(acc)
	if scopeKey == "" {
		return gitLabClaudeSharedTPMCanaryScope{}, false
	}
	snapshot := snapshotGitLabClaudeAccount(acc)
	if !snapshot.relevantForSharedCooldown() {
		return gitLabClaudeSharedTPMCanaryScope{}, false
	}
	if !snapshot.sharedTPMBlocked(now) {
		return gitLabClaudeSharedTPMCanaryScope{}, false
	}
	if snapshot.CanaryNextProbeAt.IsZero() || snapshot.CanaryNextProbeAt.After(now) {
		return gitLabClaudeSharedTPMCanaryScope{}, false
	}

	return gitLabClaudeSharedTPMCanaryScope{
		scopeKey:    scopeKey,
		model:       managedGitLabClaudeCanaryModel(snapshot.CanaryModel),
		nextProbeAt: snapshot.CanaryNextProbeAt,
		account:     acc,
	}, true
}

func (h *proxyHandler) collectDueManagedGitLabClaudeSharedTPMCanaryScopes(now time.Time) []gitLabClaudeSharedTPMCanaryScope {
	if h == nil || h.pool == nil {
		return nil
	}

	h.pool.mu.RLock()
	accounts := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()

	scopes := make(map[string]gitLabClaudeSharedTPMCanaryScope)
	for _, acc := range accounts {
		scope, ok := startOfGitLabClaudeRecoveryCandidate(now, acc)
		if !ok {
			continue
		}
		existing, exists := scopes[scope.scopeKey]
		if !exists || scope.nextProbeAt.Before(existing.nextProbeAt) {
			scopes[scope.scopeKey] = scope
		}
	}

	out := make([]gitLabClaudeSharedTPMCanaryScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, scope)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].nextProbeAt.Before(out[j].nextProbeAt)
	})
	return out
}

func (h *proxyHandler) startGitLabClaudeSharedTPMRecoveryPoller() {
	if h == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(managedGitLabClaudeSharedTPMRecoveryPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.recoverDueManagedGitLabClaudeSharedTPMScopes("poller")
		}
	}()
}

func (h *proxyHandler) recoverDueManagedGitLabClaudeSharedTPMScopes(source string) {
	now := time.Now().UTC()
	for _, scope := range h.collectDueManagedGitLabClaudeSharedTPMCanaryScopes(now) {
		h.runManagedGitLabClaudeSharedTPMCanary(scope, source)
	}
}

func (h *proxyHandler) mutateManagedGitLabClaudeScope(scopeKey string, mutate func(*Account) bool) []*Account {
	if h == nil || h.pool == nil || scopeKey == "" || mutate == nil {
		return nil
	}

	h.pool.mu.RLock()
	accounts := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()

	changed := make([]*Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil || !isGitLabClaudeAccount(acc) || gitLabClaudeScopeKey(acc) != scopeKey {
			continue
		}
		if mutate(acc) {
			changed = append(changed, acc)
		}
	}
	return changed
}

func persistGitLabClaudeAccounts(accounts []*Account, reqID string) {
	for _, acc := range accounts {
		if acc == nil || strings.TrimSpace(acc.File) == "" {
			continue
		}
		if err := saveAccount(acc); err != nil {
			log.Printf("[%s] warning: failed to persist gitlab claude account %s: %v", reqID, acc.ID, err)
		}
	}
}

func (h *proxyHandler) noteManagedGitLabClaudeCanaryScopeResult(scopeKey string, now time.Time, result, message string, retry bool) {
	reason := sanitizeStatusMessage(message)
	h.mutateManagedGitLabClaudeScope(scopeKey, func(acc *Account) bool {
		acc.mu.Lock()
		defer acc.mu.Unlock()

		changed := false
		if acc.GitLabCanaryLastAttemptAt != now {
			acc.GitLabCanaryLastAttemptAt = now
			changed = true
		}
		if strings.TrimSpace(acc.GitLabCanaryLastResult) != strings.TrimSpace(result) {
			acc.GitLabCanaryLastResult = strings.TrimSpace(result)
			changed = true
		}
		if strings.TrimSpace(acc.GitLabCanaryLastError) != reason {
			acc.GitLabCanaryLastError = reason
			changed = true
		}
		if result == "success" {
			if acc.GitLabCanaryLastSuccessAt != now {
				acc.GitLabCanaryLastSuccessAt = now
				changed = true
			}
		} else {
			if acc.GitLabCanaryLastFailureAt != now {
				acc.GitLabCanaryLastFailureAt = now
				changed = true
			}
		}

		nextProbeAt := time.Time{}
		if retry {
			nextProbeAt = managedGitLabClaudeCanaryRetryAt(now, acc.RateLimitUntil)
		}
		if !acc.GitLabCanaryNextProbeAt.Equal(nextProbeAt) {
			acc.GitLabCanaryNextProbeAt = nextProbeAt
			changed = true
		}
		return changed
	})
}

func (h *proxyHandler) clearManagedGitLabClaudeSharedTPMCooldownScope(reqID, scopeKey string, now time.Time, model string) {
	model = managedGitLabClaudeCanaryModel(model)
	changed := h.mutateManagedGitLabClaudeScope(scopeKey, func(acc *Account) bool {
		acc.mu.Lock()
		defer acc.mu.Unlock()

		if acc.Disabled || acc.Dead {
			return false
		}

		mutated := false
		if !acc.RateLimitUntil.IsZero() {
			acc.RateLimitUntil = time.Time{}
			mutated = true
		}
		if strings.TrimSpace(acc.HealthStatus) == "rate_limited" || strings.TrimSpace(acc.HealthStatus) == "" || strings.TrimSpace(acc.HealthStatus) == "unknown" {
			acc.HealthStatus = "healthy"
			mutated = true
		}
		if strings.TrimSpace(acc.HealthError) != "" {
			acc.HealthError = ""
			mutated = true
		}
		if acc.HealthCheckedAt != now {
			acc.HealthCheckedAt = now
			mutated = true
		}
		if acc.LastHealthyAt != now {
			acc.LastHealthyAt = now
			mutated = true
		}
		if strings.TrimSpace(acc.GitLabCanaryModel) != model {
			acc.GitLabCanaryModel = model
			mutated = true
		}
		if !acc.GitLabCanaryLastAttemptAt.Equal(now) {
			acc.GitLabCanaryLastAttemptAt = now
			mutated = true
		}
		if !acc.GitLabCanaryLastSuccessAt.Equal(now) {
			acc.GitLabCanaryLastSuccessAt = now
			mutated = true
		}
		if strings.TrimSpace(acc.GitLabCanaryLastResult) != "success" {
			acc.GitLabCanaryLastResult = "success"
			mutated = true
		}
		if strings.TrimSpace(acc.GitLabCanaryLastError) != "" {
			acc.GitLabCanaryLastError = ""
			mutated = true
		}
		if !acc.GitLabCanaryNextProbeAt.IsZero() {
			acc.GitLabCanaryNextProbeAt = time.Time{}
			mutated = true
		}
		return mutated
	})
	persistGitLabClaudeAccounts(changed, reqID)
	if len(changed) > 0 {
		if h.metrics != nil {
			h.metrics.incEvent("gitlab_claude_shared_tpm_canary_success")
		}
		log.Printf("[%s] gitlab claude shared org TPM canary succeeded scope=%s model=%q seats=%d", reqID, scopeKey, model, len(changed))
	}
}

func (h *proxyHandler) prepareManagedGitLabClaudeCanarySeat(ctx context.Context, acc *Account) error {
	if h == nil || acc == nil || !isGitLabClaudeAccount(acc) {
		return nil
	}
	snapshot := snapshotGitLabClaudeAccount(acc)
	if h.cfg.disableRefresh {
		if snapshot.MissingGatewayState {
			return context.DeadlineExceeded
		}
		return nil
	}
	needRefresh := snapshot.MissingGatewayState || h.needsRefresh(acc)
	if !needRefresh {
		return nil
	}

	if err := h.refreshAccount(ctx, acc); err != nil {
		if snapshotGitLabClaudeAccount(acc).MissingGatewayState {
			return err
		}
		log.Printf("warning: gitlab claude canary refresh for %s failed but existing gateway state remains usable: %v", acc.ID, err)
	}
	return nil
}

func (h *proxyHandler) runManagedGitLabClaudeSharedTPMCanary(scope gitLabClaudeSharedTPMCanaryScope, source string) {
	if h == nil || scope.account == nil || scope.scopeKey == "" {
		return
	}

	now := time.Now().UTC()
	reqID := "gitlab-claude-canary:" + source + ":" + clipOpaque(scope.scopeKey)
	ctx, cancel := context.WithTimeout(context.Background(), managedGitLabClaudeSharedTPMCanaryTimeout)
	defer cancel()

	if err := h.prepareManagedGitLabClaudeCanarySeat(ctx, scope.account); err != nil {
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "failed", err.Error(), true)
		log.Printf("[%s] gitlab claude canary prepare failed scope=%s account=%s err=%v", reqID, scope.scopeKey, scope.account.ID, err)
		return
	}

	scope.account.mu.Lock()
	accessToken := strings.TrimSpace(scope.account.AccessToken)
	upstreamBaseURL := firstNonEmpty(strings.TrimSpace(scope.account.UpstreamBaseURL), defaultGitLabClaudeGatewayURL)
	extraHeaders := copyStringMap(scope.account.ExtraHeaders)
	scope.account.mu.Unlock()

	if accessToken == "" {
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "failed", "missing gateway access token", true)
		return
	}

	baseURL, err := url.Parse(upstreamBaseURL)
	if err != nil {
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "failed", err.Error(), false)
		return
	}

	body, err := json.Marshal(map[string]any{
		"model":      managedGitLabClaudeCanaryModel(scope.model),
		"max_tokens": 1,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": "ping",
			},
		},
	})
	if err != nil {
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "failed", err.Error(), false)
		return
	}

	outURL := *baseURL
	outURL.Path = singleJoin(baseURL.Path, "/v1/messages")
	outURL.RawQuery = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, outURL.String(), bytes.NewReader(body))
	if err != nil {
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "failed", err.Error(), false)
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "codex-pool-proxy")
	for key, value := range extraHeaders {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	transport := h.transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		if h.metrics != nil {
			h.metrics.incEvent("gitlab_claude_shared_tpm_canary_failed")
		}
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "failed", err.Error(), true)
		log.Printf("[%s] gitlab claude canary transport failed scope=%s account=%s err=%v", reqID, scope.scopeKey, scope.account.ID, err)
		return
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		h.clearManagedGitLabClaudeSharedTPMCooldownScope(reqID, scope.scopeKey, now, scope.model)
		return
	}

	disposition := classifyManagedGitLabClaudeError(managedGitLabClaudeErrorSourceGatewayRequest, resp.StatusCode, resp.Header, responseBody)
	applyManagedGitLabClaudeDisposition(scope.account, disposition, resp.Header, now)
	sharedPersisted := false
	if disposition.RateLimit && disposition.SharedOrgTPM {
		sharedPersisted = h.propagateManagedGitLabClaudeSharedTPMCooldown(reqID, scope.account, disposition, resp.Header, scope.model, now)
		if h.metrics != nil {
			h.metrics.incEvent("gitlab_claude_shared_tpm_canary_rate_limited")
		}
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "rate_limited", disposition.Reason, false)
	} else {
		if h.metrics != nil {
			h.metrics.incEvent("gitlab_claude_shared_tpm_canary_failed")
		}
		h.noteManagedGitLabClaudeCanaryScopeResult(scope.scopeKey, now, "failed", disposition.Reason, false)
	}
	if !sharedPersisted {
		persistGitLabClaudeAccounts([]*Account{scope.account}, reqID)
	}
	log.Printf("[%s] gitlab claude canary failed scope=%s account=%s status=%d reason=%s", reqID, scope.scopeKey, scope.account.ID, resp.StatusCode, sanitizeStatusMessage(disposition.Reason))
}
