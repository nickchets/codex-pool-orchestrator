package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	managedOpenAIAPISubdir              = "openai_api"
	managedOpenAIAPIProbeFreshness      = 10 * time.Minute
	managedOpenAIAPIProbeTimeout        = 10 * time.Second
	managedOpenAIAPIRateLimitWait       = 45 * time.Second
	managedOpenAIAPIProbeModel          = "gpt-5.4"
	managedOpenAIAPITransientPenaltyCap = 1.5
)

type managedOpenAIAPIErrorDisposition struct {
	Retry     bool
	MarkDead  bool
	RateLimit bool
	Reason    string
}

func bumpManagedOpenAIAPIPenaltyLocked(acc *Account, delta, cap float64, now time.Time) {
	if acc == nil || delta <= 0 {
		return
	}
	acc.Penalty += delta
	if cap > 0 && acc.Penalty > cap {
		acc.Penalty = cap
	}
	acc.LastPenalty = now
}

func applyManagedOpenAIAPIProbeTransportError(acc *Account, err error, now time.Time) {
	if acc == nil {
		return
	}

	acc.mu.Lock()
	defer acc.mu.Unlock()
	acc.HealthStatus = "error"
	acc.HealthError = sanitizeStatusMessage(err.Error())
	acc.HealthCheckedAt = now
	bumpManagedOpenAIAPIPenaltyLocked(acc, 0.3, managedOpenAIAPITransientPenaltyCap, now)
}

func managedOpenAIAPIAccountID(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return fmt.Sprintf("openai_api_%x", sum[:6])
}

func saveManagedOpenAIAPIKey(poolDir, apiKey string) (*Account, bool, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, false, fmt.Errorf("api key is empty")
	}
	dir := filepath.Join(poolDir, managedOpenAIAPISubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}

	accountID := managedOpenAIAPIAccountID(key)
	path := filepath.Join(dir, accountID+".json")
	_, statErr := os.Stat(path)
	created := os.IsNotExist(statErr)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, false, statErr
	}

	root := map[string]any{
		"OPENAI_API_KEY": key,
		"auth_mode":      accountAuthModeAPIKey,
		"plan_type":      "api",
		"health_status":  "unknown",
	}
	if err := atomicWriteJSON(path, root); err != nil {
		return nil, false, err
	}

	return &Account{
		Type:         AccountTypeCodex,
		ID:           accountID,
		File:         path,
		AccessToken:  key,
		PlanType:     "api",
		AuthMode:     accountAuthModeAPIKey,
		HealthStatus: "unknown",
	}, created, nil
}

func (h *proxyHandler) handleOperatorCodexAPIKeyAdd(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&payload); err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	apiKey := strings.TrimSpace(payload.APIKey)
	if apiKey == "" {
		respondJSONError(w, http.StatusBadRequest, "api_key is required")
		return
	}
	if !strings.HasPrefix(apiKey, "sk-") {
		respondJSONError(w, http.StatusBadRequest, "api_key must start with sk-")
		return
	}

	acc, created, err := saveManagedOpenAIAPIKey(h.cfg.poolDir, apiKey)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	probeErr := h.probeManagedCodexAPIKey(r.Context(), acc)
	if probeErr != nil {
		log.Printf("managed OpenAI API key %s probe failed during add: %v", acc.ID, probeErr)
	}

	h.reloadAccounts()

	respondJSON(w, map[string]any{
		"status":        "ok",
		"account_id":    acc.ID,
		"created":       created,
		"health_status": firstNonEmpty(strings.TrimSpace(acc.HealthStatus), "unknown"),
		"health_error":  sanitizeStatusMessage(acc.HealthError),
		"dead":          acc.Dead,
		"last_healthy_at": func() string {
			if acc.LastHealthyAt.IsZero() {
				return ""
			}
			return acc.LastHealthyAt.UTC().Format(time.RFC3339)
		}(),
	})
}

func (h *proxyHandler) maybeProbeManagedCodexAPIKey(ctx context.Context, acc *Account) error {
	if !isManagedCodexAPIKeyAccount(acc) {
		return nil
	}

	now := time.Now()
	acc.mu.Lock()
	lastCheckedAt := acc.HealthCheckedAt
	healthStatus := strings.TrimSpace(acc.HealthStatus)
	dead := acc.Dead
	acc.mu.Unlock()

	if dead {
		return fmt.Errorf("managed api key %s is marked dead", acc.ID)
	}
	if healthStatus == "healthy" && !lastCheckedAt.IsZero() && now.Sub(lastCheckedAt) < managedOpenAIAPIProbeFreshness {
		return nil
	}
	return h.probeManagedCodexAPIKey(ctx, acc)
}

func (h *proxyHandler) probeManagedCodexAPIKey(ctx context.Context, acc *Account) error {
	if h == nil || acc == nil || !isManagedCodexAPIKeyAccount(acc) {
		return nil
	}
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()

	provider := h.registry.ForType(AccountTypeCodex)
	if provider == nil {
		err := fmt.Errorf("missing codex provider")
		trace.noteProbe(AccountTypeCodex, acc, "error", time.Since(startedAt), err)
		return err
	}

	probeCtx := ctx
	var cancel context.CancelFunc
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > managedOpenAIAPIProbeTimeout {
		probeCtx, cancel = context.WithTimeout(ctx, managedOpenAIAPIProbeTimeout)
		defer cancel()
	}

	targetBase := providerUpstreamURLForAccount(provider, "/v1/responses", acc)
	probeURL := *targetBase
	probeURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, "/v1/responses", acc))
	probeURL.RawQuery = ""

	probeBody, err := json.Marshal(map[string]any{
		"model":        managedOpenAIAPIProbeModel,
		"instructions": "Reply with exactly ok.",
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": "ping",
					},
				},
			},
		},
		"store": false,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL.String(), strings.NewReader(string(probeBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	provider.SetAuthHeaders(req, acc)

	resp, err := h.transport.RoundTrip(req)
	now := time.Now()
	if err != nil {
		applyManagedOpenAIAPIProbeTransportError(acc, err, now)
		if saveErr := saveAccount(acc); saveErr != nil {
			log.Printf("warning: failed to persist managed api key %s probe error: %v", acc.ID, saveErr)
		}
		trace.noteProbe(AccountTypeCodex, acc, "error", time.Since(startedAt), err)
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	body = bodyForInspection(nil, body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		acc.mu.Lock()
		setAccountDeadStateLocked(acc, false, now)
		acc.HealthStatus = "healthy"
		acc.HealthError = ""
		acc.HealthCheckedAt = now
		acc.LastHealthyAt = now
		acc.RateLimitUntil = time.Time{}
		acc.mu.Unlock()
		if saveErr := saveAccount(acc); saveErr != nil {
			log.Printf("warning: failed to persist managed api key %s probe success: %v", acc.ID, saveErr)
		}
		trace.noteProbe(AccountTypeCodex, acc, "healthy", time.Since(startedAt), nil)
		return nil
	}

	disposition := classifyManagedOpenAIAPIError(resp.StatusCode, resp.Header, body)
	applyManagedOpenAIAPIDisposition(acc, disposition, resp.Header, now)
	if saveErr := saveAccount(acc); saveErr != nil {
		log.Printf("warning: failed to persist managed api key %s probe failure: %v", acc.ID, saveErr)
	}

	if disposition.Reason == "" {
		disposition.Reason = resp.Status
	}
	err = fmt.Errorf("managed api key probe failed: %s", disposition.Reason)
	result := "error"
	switch {
	case disposition.RateLimit:
		result = "rate_limited"
	case disposition.MarkDead:
		result = "dead"
	}
	trace.noteProbe(AccountTypeCodex, acc, result, time.Since(startedAt), err)
	return err
}

func classifyManagedOpenAIAPIError(statusCode int, headers http.Header, body []byte) managedOpenAIAPIErrorDisposition {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)

	fields := []string{
		strings.ToLower(strings.TrimSpace(payload.Error.Message)),
		strings.ToLower(strings.TrimSpace(payload.Error.Type)),
		strings.ToLower(strings.TrimSpace(payload.Error.Code)),
		strings.ToLower(strings.TrimSpace(string(body))),
	}
	containsAny := func(parts ...string) bool {
		for _, field := range fields {
			for _, part := range parts {
				if part != "" && strings.Contains(field, strings.ToLower(part)) {
					return true
				}
			}
		}
		return false
	}

	reason := firstNonEmpty(
		strings.TrimSpace(payload.Error.Message),
		strings.TrimSpace(payload.Error.Code),
		http.StatusText(statusCode),
	)
	reason = sanitizeStatusMessage(reason)

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return managedOpenAIAPIErrorDisposition{
			Retry:    true,
			MarkDead: containsAny("invalid_api_key", "incorrect api key", "incorrect_api_key", "organization_deactivated", "account_deactivated"),
			Reason:   reason,
		}
	case http.StatusPaymentRequired:
		return managedOpenAIAPIErrorDisposition{
			Retry:    true,
			MarkDead: true,
			Reason:   reason,
		}
	case http.StatusTooManyRequests:
		markDead := containsAny("insufficient_quota", "billing_hard_limit_reached", "credits exhausted", "credit balance", "quota exceeded")
		return managedOpenAIAPIErrorDisposition{
			Retry:     true,
			MarkDead:  markDead,
			RateLimit: !markDead,
			Reason:    reason,
		}
	default:
		if statusCode >= 500 && statusCode <= 599 {
			return managedOpenAIAPIErrorDisposition{Retry: true, Reason: reason}
		}
	}

	return managedOpenAIAPIErrorDisposition{Reason: reason}
}

func applyManagedOpenAIAPIDisposition(acc *Account, disposition managedOpenAIAPIErrorDisposition, headers http.Header, now time.Time) {
	if acc == nil {
		return
	}

	acc.mu.Lock()
	acc.HealthCheckedAt = now
	acc.HealthError = sanitizeStatusMessage(disposition.Reason)
	switch {
	case disposition.MarkDead:
		setAccountDeadStateLocked(acc, true, now)
		acc.HealthStatus = "dead"
		bumpManagedOpenAIAPIPenaltyLocked(acc, 100.0, 0, now)
	case disposition.RateLimit:
		setAccountDeadStateLocked(acc, false, now)
		acc.HealthStatus = "rate_limited"
		bumpManagedOpenAIAPIPenaltyLocked(acc, 1.0, 0, now)
	default:
		setAccountDeadStateLocked(acc, false, now)
		acc.HealthStatus = "error"
		bumpManagedOpenAIAPIPenaltyLocked(acc, 0.5, managedOpenAIAPITransientPenaltyCap, now)
	}
	acc.mu.Unlock()

	if disposition.RateLimit {
		wait := managedOpenAIAPIRateLimitWait
		if headers != nil {
			if retryAfter, ok := parseRetryAfter(headers); ok {
				wait = retryAfter
			}
		}
		if wait > 0 {
			acc.mu.Lock()
			until := now.Add(wait)
			if acc.RateLimitUntil.Before(until) {
				acc.RateLimitUntil = until
			}
			acc.mu.Unlock()
		}
	}
}

func (h *proxyHandler) handleManagedCodexAPIKeyFailure(reqID string, acc *Account, resp *http.Response, body []byte) error {
	if acc == nil || !isManagedCodexAPIKeyAccount(acc) || resp == nil {
		return nil
	}

	disposition := classifyManagedOpenAIAPIError(resp.StatusCode, resp.Header, body)
	if !disposition.Retry {
		return nil
	}

	applyManagedOpenAIAPIDisposition(acc, disposition, resp.Header, time.Now())
	if err := saveAccount(acc); err != nil {
		log.Printf("[%s] warning: failed to save managed api key %s state: %v", reqID, acc.ID, err)
	}

	log.Printf("[%s] managed api key %s unavailable: status=%d dead=%v rate_limited=%v reason=%s", reqID, acc.ID, resp.StatusCode, disposition.MarkDead, disposition.RateLimit, disposition.Reason)
	if disposition.Reason != "" {
		return fmt.Errorf("managed api fallback %s: %s", resp.Status, disposition.Reason)
	}
	return fmt.Errorf("managed api fallback %s", resp.Status)
}

func classifyManagedOpenAIAPIErrorStrings(message, errType, code string) managedOpenAIAPIErrorDisposition {
	fields := []string{
		strings.ToLower(strings.TrimSpace(message)),
		strings.ToLower(strings.TrimSpace(errType)),
		strings.ToLower(strings.TrimSpace(code)),
	}
	containsAny := func(parts ...string) bool {
		for _, field := range fields {
			for _, part := range parts {
				if part != "" && strings.Contains(field, strings.ToLower(part)) {
					return true
				}
			}
		}
		return false
	}

	reason := sanitizeStatusMessage(firstNonEmpty(message, code, errType))
	if containsAny("invalid_api_key", "incorrect api key", "incorrect_api_key", "organization_deactivated", "account_deactivated") {
		return managedOpenAIAPIErrorDisposition{Retry: true, MarkDead: true, Reason: reason}
	}
	if containsAny("insufficient_quota", "billing_hard_limit_reached", "credits exhausted", "credit balance", "quota exceeded") {
		return managedOpenAIAPIErrorDisposition{Retry: true, MarkDead: true, Reason: reason}
	}
	if containsAny("usage limit", "hit your usage limit", "try again at") {
		return managedOpenAIAPIErrorDisposition{Retry: true, RateLimit: true, Reason: reason}
	}
	if containsAny("rate_limit", "rate limited", "too many requests") {
		return managedOpenAIAPIErrorDisposition{Retry: true, RateLimit: true, Reason: reason}
	}
	return managedOpenAIAPIErrorDisposition{Reason: reason}
}

func classifyManagedOpenAIAPISSEError(data []byte) (managedOpenAIAPIErrorDisposition, bool) {
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
		Response struct {
			Status string `json:"status"`
			Error  struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return managedOpenAIAPIErrorDisposition{}, false
	}

	message := firstNonEmpty(payload.Error.Message, payload.Response.Error.Message)
	errType := firstNonEmpty(payload.Error.Type, payload.Type, payload.Response.Status)
	code := firstNonEmpty(payload.Error.Code, payload.Response.Error.Code)
	if strings.TrimSpace(message) == "" && strings.TrimSpace(code) == "" {
		return managedOpenAIAPIErrorDisposition{}, false
	}

	disposition := classifyManagedOpenAIAPIErrorStrings(message, errType, code)
	if !disposition.Retry {
		return disposition, false
	}
	return disposition, true
}
