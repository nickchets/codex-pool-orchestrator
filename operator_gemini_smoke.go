package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const operatorGeminiSeatSmokeTimeout = 30 * time.Second

type operatorGeminiSeatSmokeRequest struct {
	AccountID    string `json:"account_id"`
	Model        string `json:"model,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	ForceRefresh bool   `json:"force_refresh,omitempty"`
}

type operatorGeminiSeatSmokeLoadResult struct {
	OK                    bool     `json:"ok"`
	HTTPStatus            int      `json:"http_status,omitempty"`
	ProjectID             string   `json:"project_id,omitempty"`
	CurrentTierID         string   `json:"current_tier_id,omitempty"`
	CurrentTierName       string   `json:"current_tier_name,omitempty"`
	IneligibleReasonCodes []string `json:"ineligible_reason_codes,omitempty"`
	ValidationMessage     string   `json:"validation_message,omitempty"`
	Error                 string   `json:"error,omitempty"`
}

type operatorGeminiSeatSmokeGenerateResult struct {
	OK           bool            `json:"ok"`
	HTTPStatus   int             `json:"http_status,omitempty"`
	Error        string          `json:"error,omitempty"`
	ResponseText string          `json:"response_text,omitempty"`
	RawResponse  json.RawMessage `json:"raw_response,omitempty"`
}

type operatorGeminiSeatSmokeResponse struct {
	AccountID                string                                `json:"account_id"`
	Model                    string                                `json:"model"`
	Prompt                   string                                `json:"prompt"`
	ProjectID                string                                `json:"project_id,omitempty"`
	FallbackProjectUsed      bool                                  `json:"fallback_project_used,omitempty"`
	HealthStatus             string                                `json:"health_status,omitempty"`
	ProviderTruthState       string                                `json:"provider_truth_state,omitempty"`
	OperationalTruth         *GeminiOperationalTruthStatus         `json:"operational_truth,omitempty"`
	RoutingState             string                                `json:"routing_state,omitempty"`
	RoutingBlockReason       string                                `json:"routing_block_reason,omitempty"`
	RoutingDegradedReason    string                                `json:"routing_degraded_reason,omitempty"`
	RoutingRecoveryAt        string                                `json:"routing_recovery_at,omitempty"`
	RequestedModelKey        string                                `json:"requested_model_key,omitempty"`
	RequestedModelLimited    bool                                  `json:"requested_model_limited,omitempty"`
	RequestedModelRecoveryAt string                                `json:"requested_model_recovery_at,omitempty"`
	RateLimitResetTimes      map[string]string                     `json:"rate_limit_reset_times,omitempty"`
	ValidationReasonCode     string                                `json:"validation_reason_code,omitempty"`
	RefreshForced            bool                                  `json:"refresh_forced,omitempty"`
	RefreshApplied           bool                                  `json:"refresh_applied,omitempty"`
	RefreshError             string                                `json:"refresh_error,omitempty"`
	LoadCodeAssist           *operatorGeminiSeatSmokeLoadResult    `json:"load_code_assist,omitempty"`
	Generate                 operatorGeminiSeatSmokeGenerateResult `json:"generate"`
}

func (h *proxyHandler) accountByID(id string) *Account {
	if h == nil || h.pool == nil {
		return nil
	}
	h.pool.mu.RLock()
	defer h.pool.mu.RUnlock()
	for _, acc := range h.pool.accounts {
		if acc != nil && acc.ID == id {
			return acc
		}
	}
	return nil
}

func operatorGeminiSeatSmokeDefaultModel(raw string) string {
	if model := strings.TrimSpace(raw); model != "" {
		return model
	}
	return "gemini-3.1-pro"
}

func operatorGeminiSeatSmokeDefaultPrompt(raw, accountID string) string {
	if prompt := strings.TrimSpace(raw); prompt != "" {
		return prompt
	}
	return fmt.Sprintf("Reply with exactly GEMINI_SMOKE_OK:%s.", accountID)
}

func extractGeminiResponseText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	candidates, _ := root["candidates"].([]any)
	if len(candidates) == 0 {
		return ""
	}
	first, _ := candidates[0].(map[string]any)
	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	var out strings.Builder
	for _, part := range parts {
		partMap, _ := part.(map[string]any)
		text, _ := partMap["text"].(string)
		out.WriteString(text)
	}
	return strings.TrimSpace(out.String())
}

func geminiSmokeHTTPStatus(err error) int {
	var httpErr *geminiCodeAssistHTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		return httpErr.StatusCode
	}
	return 0
}

func (h *proxyHandler) doOperatorGeminiSeatSmokeGenerate(ctx context.Context, accessToken, model, projectID, reqID, prompt string, out any) error {
	rewrittenModel := rewriteGeminiCodeAssistFacadeModel(model)
	payload := geminiCodeAssistRequestPayload{
		Model:        rewrittenModel,
		Project:      projectID,
		UserPromptID: reqID,
		Request: geminiCodeAssistInnerRequestPayload{
			Contents:  json.RawMessage(fmt.Sprintf(`[{"role":"user","parts":[{"text":%q}]}]`, prompt)),
			SessionID: reqID,
		},
	}

	if shouldUseAntigravityGeminiCodeAssistBaseFallback(rewrittenModel) {
		var lastErr error
		for _, base := range antigravityGeminiCodeAssistBaseCandidates(h.geminiCodeAssistBaseURL()) {
			err := h.doGeminiCodeAssistJSONWithBase(ctx, base, http.MethodPost, "/v1internal:generateContent", accessToken, payload, out)
			if err == nil {
				return nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return lastErr
		}
	}

	return h.doGeminiCodeAssistJSON(ctx, http.MethodPost, "/v1internal:generateContent", accessToken, payload, out)
}

func formatGeminiModelRateLimitResetTimes(values map[string]time.Time, now time.Time) map[string]string {
	values = normalizeGeminiModelRateLimitResetTimes(values, now)
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for model, resetAt := range values {
		if resetAt.IsZero() || !resetAt.After(now) {
			continue
		}
		out[model] = resetAt.UTC().Format(time.RFC3339)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func snapshotGeminiRequestedModelCooldown(snapshot accountSnapshot, requestedModel string, now time.Time) (time.Time, string, bool) {
	modelKey := requestedGeminiModelRateLimitKey(requestedModel, "")
	if modelKey == "" {
		return time.Time{}, "", false
	}
	resetTimes := normalizeGeminiModelRateLimitResetTimes(snapshot.GeminiModelRateLimitResetTimes, now)
	if until, ok := resetTimes[modelKey]; ok && until.After(now) {
		return until.UTC(), modelKey, true
	}
	if until, ok := geminiQuotaModelRateLimitUntil(snapshot.GeminiQuotaModels, modelKey, now); ok {
		return until.UTC(), modelKey, true
	}
	return time.Time{}, modelKey, false
}

func (h *proxyHandler) populateOperatorGeminiSeatSmokeState(result *operatorGeminiSeatSmokeResponse, accountID, requestedModel string, now time.Time) {
	if h == nil || result == nil {
		return
	}
	snapshot, ok := h.snapshotAccountByID(accountID, now)
	if !ok {
		return
	}
	result.HealthStatus = strings.TrimSpace(snapshot.HealthStatus)
	result.ProviderTruthState = strings.TrimSpace(snapshot.GeminiProviderTruthState)
	result.ValidationReasonCode = strings.TrimSpace(snapshot.GeminiValidationReasonCode)
	result.OperationalTruth = geminiOperationalTruthStatus(snapshot)
	routing := buildPoolDashboardRouting(snapshot, snapshot.Routing, now)
	result.RoutingState = strings.TrimSpace(routing.State)
	result.RoutingBlockReason = strings.TrimSpace(routing.BlockReason)
	result.RoutingDegradedReason = strings.TrimSpace(routing.DegradedReason)
	result.RoutingRecoveryAt = strings.TrimSpace(routing.RecoveryAt)
	result.RateLimitResetTimes = formatGeminiModelRateLimitResetTimes(snapshot.GeminiModelRateLimitResetTimes, now)
	if until, modelKey, limited := snapshotGeminiRequestedModelCooldown(snapshot, requestedModel, now); modelKey != "" {
		result.RequestedModelKey = modelKey
		result.RequestedModelLimited = limited
		if limited {
			result.RequestedModelRecoveryAt = until.UTC().Format(time.RFC3339)
		}
	}
}

func (h *proxyHandler) handleOperatorGeminiSeatSmoke(w http.ResponseWriter, r *http.Request) {
	var req operatorGeminiSeatSmokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		http.Error(w, "account_id is required", http.StatusBadRequest)
		return
	}

	acc := h.accountByID(accountID)
	if acc == nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	if acc.Type != AccountTypeGemini {
		http.Error(w, "account is not gemini", http.StatusBadRequest)
		return
	}

	model := operatorGeminiSeatSmokeDefaultModel(req.Model)
	prompt := operatorGeminiSeatSmokeDefaultPrompt(req.Prompt, accountID)
	result := operatorGeminiSeatSmokeResponse{
		AccountID: accountID,
		Model:     model,
		Prompt:    prompt,
	}

	ctx, cancel := context.WithTimeout(r.Context(), operatorGeminiSeatSmokeTimeout)
	defer cancel()

	if req.ForceRefresh {
		result.RefreshForced = true
	}
	if req.ForceRefresh || (!h.cfg.disableRefresh && h.needsRefresh(acc)) {
		var err error
		if req.ForceRefresh {
			err = h.refreshAccountForced(ctx, acc)
		} else {
			err = h.refreshAccount(ctx, acc)
		}
		if err != nil {
			result.RefreshError = err.Error()
		} else {
			result.RefreshApplied = true
		}
	}

	acc.mu.Lock()
	accessToken := strings.TrimSpace(acc.AccessToken)
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(acc.AntigravityProjectID)
	}
	if projectID == "" && isAntigravityGeminiSeat(acc) {
		projectID = antigravityGeminiFallbackProject
		result.FallbackProjectUsed = true
	}
	acc.mu.Unlock()
	h.populateOperatorGeminiSeatSmokeState(&result, accountID, model, time.Now().UTC())

	if accessToken == "" {
		http.Error(w, "gemini account has empty access token", http.StatusServiceUnavailable)
		return
	}

	loadRes, loadErr := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, projectID, "")
	result.LoadCodeAssist = &operatorGeminiSeatSmokeLoadResult{}
	if loadErr != nil {
		result.LoadCodeAssist.OK = false
		result.LoadCodeAssist.HTTPStatus = geminiSmokeHTTPStatus(loadErr)
		result.LoadCodeAssist.Error = loadErr.Error()
	} else if loadRes != nil {
		result.LoadCodeAssist.OK = true
		result.LoadCodeAssist.HTTPStatus = http.StatusOK
		result.LoadCodeAssist.ProjectID = strings.TrimSpace(loadRes.CloudaicompanionProject)
		if loadRes.CurrentTier != nil {
			result.LoadCodeAssist.CurrentTierID = strings.TrimSpace(loadRes.CurrentTier.ID)
			result.LoadCodeAssist.CurrentTierName = strings.TrimSpace(loadRes.CurrentTier.Name)
		}
		for _, tier := range loadRes.IneligibleTiers {
			if code := strings.TrimSpace(tier.ReasonCode); code != "" {
				result.LoadCodeAssist.IneligibleReasonCodes = append(result.LoadCodeAssist.IneligibleReasonCodes, code)
			}
		}
		result.LoadCodeAssist.ValidationMessage = antigravityLoadValidationMessage(loadRes)
		if result.ProjectID == "" {
			if resolvedProjectID := strings.TrimSpace(loadRes.CloudaicompanionProject); resolvedProjectID != "" {
				result.ProjectID = resolvedProjectID
				projectID = resolvedProjectID
				result.FallbackProjectUsed = false
			}
		}
	}
	if loadRes != nil {
		acc.mu.Lock()
		applyAntigravityGeminiProviderTruthLocked(acc, antigravityGeminiProviderTruthFromLoad(loadRes, projectID, time.Now().UTC()))
		acc.mu.Unlock()
	}

	if result.ProjectID == "" {
		result.ProjectID = projectID
	}
	if strings.TrimSpace(result.ProjectID) == "" {
		if saveErr := saveAccount(acc); saveErr != nil {
			result.RefreshError = firstNonEmpty(result.RefreshError, saveErr.Error())
		}
		h.populateOperatorGeminiSeatSmokeState(&result, accountID, model, time.Now().UTC())
		w.WriteHeader(http.StatusBadRequest)
		result.Generate.Error = "no project_id available for smoke request"
		respondJSON(w, result)
		return
	}

	reqID := "operator-gemini-smoke-" + randomID()
	var rawEnvelope json.RawMessage
	if err := h.doOperatorGeminiSeatSmokeGenerate(ctx, accessToken, model, result.ProjectID, reqID, prompt, &rawEnvelope); err != nil {
		now := time.Now().UTC()
		acc.mu.Lock()
		noteGeminiOperationalFailureForModelLocked(acc, now, "operator_smoke", err, model, "")
		acc.mu.Unlock()
		if saveErr := saveAccount(acc); saveErr != nil {
			result.RefreshError = firstNonEmpty(result.RefreshError, saveErr.Error())
		}
		result.Generate.OK = false
		result.Generate.HTTPStatus = geminiSmokeHTTPStatus(err)
		result.Generate.Error = err.Error()
		h.populateOperatorGeminiSeatSmokeState(&result, accountID, model, now)
		respondJSON(w, result)
		return
	}

	unwrapped, err := unwrapGeminiCodeAssistResponse(rawEnvelope)
	if err != nil {
		acc.mu.Lock()
		noteGeminiOperationalFailureLocked(acc, time.Now().UTC(), "operator_smoke", err)
		acc.mu.Unlock()
		if saveErr := saveAccount(acc); saveErr != nil {
			result.RefreshError = firstNonEmpty(result.RefreshError, saveErr.Error())
		}
		result.Generate.OK = false
		result.Generate.HTTPStatus = http.StatusOK
		result.Generate.Error = fmt.Sprintf("unwrap response: %v", err)
		result.Generate.RawResponse = append(json.RawMessage(nil), rawEnvelope...)
		h.populateOperatorGeminiSeatSmokeState(&result, accountID, model, time.Now().UTC())
		respondJSON(w, result)
		return
	}

	acc.mu.Lock()
	noteGeminiOperationalSuccessLocked(acc, time.Now().UTC(), "operator_smoke")
	acc.mu.Unlock()
	if saveErr := saveAccount(acc); saveErr != nil {
		result.RefreshError = firstNonEmpty(result.RefreshError, saveErr.Error())
	}
	result.Generate.OK = true
	result.Generate.HTTPStatus = http.StatusOK
	result.Generate.RawResponse = append(json.RawMessage(nil), unwrapped...)
	result.Generate.ResponseText = extractGeminiResponseText(unwrapped)
	h.populateOperatorGeminiSeatSmokeState(&result, accountID, model, time.Now().UTC())
	respondJSON(w, result)
}
