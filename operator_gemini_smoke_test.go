package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOperatorGeminiSeatSmokeBlockedSeatUsesFallbackProject(t *testing.T) {
	t.Helper()

	var sawLoad bool
	var sawGenerate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			sawLoad = true
			if got := r.Header.Get("Authorization"); got != "Bearer seat-access" {
				t.Fatalf("load auth = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != antigravityCodeAssistUA {
				t.Fatalf("load user-agent = %q", got)
			}
			var req antigravityLoadCodeAssistRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode load request: %v", err)
			}
			if req.CloudaicompanionProject != antigravityGeminiFallbackProject {
				t.Fatalf("load project = %q", req.CloudaicompanionProject)
			}
			respondJSON(w, map[string]any{
				"allowedTiers": []map[string]any{
					{"id": "standard-tier", "name": "Antigravity"},
				},
				"ineligibleTiers": []map[string]any{
					{"reasonCode": "UNSUPPORTED_LOCATION", "reasonMessage": "region blocked"},
				},
			})
		case "/v1internal:generateContent":
			sawGenerate = true
			if got := r.Header.Get("Authorization"); got != "Bearer seat-access" {
				t.Fatalf("generate auth = %q", got)
			}
			var req geminiCodeAssistRequestPayload
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode generate request: %v", err)
			}
			if req.Project != antigravityGeminiFallbackProject {
				t.Fatalf("generate project = %q", req.Project)
			}
			if req.Model != "gemini-2.5-flash" {
				t.Fatalf("generate model = %q", req.Model)
			}
			respondJSON(w, map[string]any{
				"response": map[string]any{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"role": "model",
								"parts": []map[string]any{
									{"text": "BLOCKED_OK"},
								},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	acc := &Account{
		ID:                           "gemini-seat-blocked",
		Type:                         AccountTypeGemini,
		PlanType:                     "gemini",
		AuthMode:                     accountAuthModeOAuth,
		AccessToken:                  "seat-access",
		OperatorSource:               geminiOperatorSourceAntigravityImport,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravityValidationBlocked: true,
		HealthStatus:                 "restricted",
		GeminiProviderTruthState:     geminiProviderTruthStateRestricted,
		GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
	}

	h := &proxyHandler{
		cfg: config{
			geminiBase: mustParse(server.URL),
		},
		refreshTransport: server.Client().Transport,
		pool:             newPoolState([]*Account{acc}, false),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/seat-smoke", strings.NewReader(`{"account_id":"gemini-seat-blocked","model":"gemini-2.5-flash","prompt":"Reply with exactly BLOCKED_OK."}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !sawLoad || !sawGenerate {
		t.Fatalf("expected load and generate calls, sawLoad=%v sawGenerate=%v", sawLoad, sawGenerate)
	}

	var resp operatorGeminiSeatSmokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.FallbackProjectUsed {
		t.Fatal("expected fallback_project_used=true")
	}
	if resp.ProjectID != antigravityGeminiFallbackProject {
		t.Fatalf("project_id = %q", resp.ProjectID)
	}
	if !resp.Generate.OK {
		t.Fatalf("generate not ok: %+v", resp.Generate)
	}
	if resp.Generate.ResponseText != "BLOCKED_OK" {
		t.Fatalf("response_text = %q", resp.Generate.ResponseText)
	}
	if resp.ValidationReasonCode != "UNSUPPORTED_LOCATION" {
		t.Fatalf("validation_reason_code = %q", resp.ValidationReasonCode)
	}
	if resp.HealthStatus != "restricted" {
		t.Fatalf("health_status = %q", resp.HealthStatus)
	}
	if resp.ProviderTruthState != geminiProviderTruthStateRestricted {
		t.Fatalf("provider_truth_state = %q", resp.ProviderTruthState)
	}
	if resp.RoutingState != routingDisplayStateDegradedEnabled {
		t.Fatalf("routing_state = %q", resp.RoutingState)
	}
	if resp.OperationalTruth == nil || resp.OperationalTruth.State != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("operational_truth = %+v", resp.OperationalTruth)
	}
	if resp.LoadCodeAssist == nil || !resp.LoadCodeAssist.OK {
		t.Fatalf("load_code_assist = %+v", resp.LoadCodeAssist)
	}
	acc.mu.Lock()
	gotProjectID := acc.AntigravityProjectID
	gotProviderTruthState := acc.GeminiProviderTruthState
	acc.mu.Unlock()
	if gotProjectID != antigravityGeminiFallbackProject {
		t.Fatalf("saved project_id = %q", gotProjectID)
	}
	if gotProviderTruthState != geminiProviderTruthStateRestricted {
		t.Fatalf("saved provider_truth_state = %q", gotProviderTruthState)
	}
}

func TestOperatorGeminiSeatSmokePersistsFallbackProjectForProjectlessSeat(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			var req antigravityLoadCodeAssistRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode load request: %v", err)
			}
			if req.CloudaicompanionProject != antigravityGeminiFallbackProject {
				t.Fatalf("load project = %q", req.CloudaicompanionProject)
			}
			respondJSON(w, map[string]any{
				"currentTier": map[string]any{
					"id":   "free-tier",
					"name": "Antigravity",
				},
			})
		case "/v1internal:generateContent":
			var req geminiCodeAssistRequestPayload
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode generate request: %v", err)
			}
			if req.Project != antigravityGeminiFallbackProject {
				t.Fatalf("generate project = %q", req.Project)
			}
			respondJSON(w, map[string]any{
				"response": map[string]any{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"role": "model",
								"parts": []map[string]any{
									{"text": "PROJECT_OK"},
								},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	acc := &Account{
		ID:                     "gemini-seat-projectless",
		Type:                   AccountTypeGemini,
		PlanType:               "gemini",
		AuthMode:               accountAuthModeOAuth,
		AccessToken:            "seat-access",
		OperatorSource:         geminiOperatorSourceAntigravityImport,
		OAuthProfileID:         geminiOAuthAntigravityProfileID,
		GeminiOperationalState: geminiOperationalTruthStateDegradedOK,
	}

	h := &proxyHandler{
		cfg: config{
			geminiBase: mustParse(server.URL),
		},
		refreshTransport: server.Client().Transport,
		pool:             newPoolState([]*Account{acc}, false),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/seat-smoke", strings.NewReader(`{"account_id":"gemini-seat-projectless","model":"gemini-2.5-flash","prompt":"Reply with exactly PROJECT_OK."}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp operatorGeminiSeatSmokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Generate.OK {
		t.Fatalf("generate not ok: %+v", resp.Generate)
	}
	acc.mu.Lock()
	gotProjectID := acc.AntigravityProjectID
	gotProviderTruthState := acc.GeminiProviderTruthState
	gotOperationalState := acc.GeminiOperationalState
	acc.mu.Unlock()
	if gotProjectID != antigravityGeminiFallbackProject {
		t.Fatalf("saved project_id = %q", gotProjectID)
	}
	if gotProviderTruthState != geminiProviderTruthStateReady {
		t.Fatalf("saved provider_truth_state = %q", gotProviderTruthState)
	}
	if gotOperationalState != geminiOperationalTruthStateCleanOK {
		t.Fatalf("saved operational_state = %q", gotOperationalState)
	}
}

func TestOperatorGeminiSeatSmokeRewritesGemini31ProAndFallsBackBases(t *testing.T) {
	t.Helper()

	originalBases := append([]string(nil), antigravityGeminiQuotaBaseURLs...)
	defer func() { antigravityGeminiQuotaBaseURLs = originalBases }()

	var primaryGenerateCalls int
	var fallbackGenerateCalls int
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:generateContent" {
			http.NotFound(w, r)
			return
		}
		fallbackGenerateCalls++
		var req geminiCodeAssistRequestPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode fallback generate request: %v", err)
		}
		if req.Model != "gemini-3.1-pro-high" {
			t.Fatalf("fallback generate model = %q", req.Model)
		}
		if req.Project != "primeval-bit-pxq56" {
			t.Fatalf("fallback generate project = %q", req.Project)
		}
		respondJSON(w, map[string]any{
			"response": map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"role": "model",
							"parts": []map[string]any{
								{"text": "READY_OK"},
							},
						},
					},
				},
			},
		})
	}))
	defer fallback.Close()
	antigravityGeminiQuotaBaseURLs = []string{fallback.URL}

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			respondJSON(w, map[string]any{
				"cloudaicompanionProject": "primeval-bit-pxq56",
				"currentTier": map[string]any{
					"id":   "free-tier",
					"name": "Antigravity",
				},
			})
		case "/v1internal:generateContent":
			primaryGenerateCalls++
			var req geminiCodeAssistRequestPayload
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode primary generate request: %v", err)
			}
			if req.Model != "gemini-3.1-pro-high" {
				t.Fatalf("primary generate model = %q", req.Model)
			}
			http.Error(w, `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`, http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	h := &proxyHandler{
		cfg: config{
			geminiBase: mustParse(primary.URL),
		},
		refreshTransport: primary.Client().Transport,
		pool: newPoolState([]*Account{
			{
				ID:                       "gemini-seat-ready",
				Type:                     AccountTypeGemini,
				PlanType:                 "gemini",
				AuthMode:                 accountAuthModeOAuth,
				AccessToken:              "seat-access",
				OperatorSource:           geminiOperatorSourceAntigravityImport,
				OAuthProfileID:           geminiOAuthAntigravityProfileID,
				AntigravityProjectID:     "primeval-bit-pxq56",
				HealthStatus:             "healthy",
				GeminiProviderTruthState: geminiProviderTruthStateReady,
			},
		}, false),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/seat-smoke", strings.NewReader(`{"account_id":"gemini-seat-ready","model":"gemini-3.1-pro","prompt":"Reply with exactly READY_OK."}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if primaryGenerateCalls != 1 {
		t.Fatalf("primary generate calls = %d", primaryGenerateCalls)
	}
	if fallbackGenerateCalls != 1 {
		t.Fatalf("fallback generate calls = %d", fallbackGenerateCalls)
	}

	var resp operatorGeminiSeatSmokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Generate.OK {
		t.Fatalf("generate not ok: %+v", resp.Generate)
	}
	if resp.Generate.ResponseText != "READY_OK" {
		t.Fatalf("response_text = %q", resp.Generate.ResponseText)
	}
	if resp.OperationalTruth == nil || resp.OperationalTruth.State != geminiOperationalTruthStateCleanOK {
		t.Fatalf("operational_truth = %+v", resp.OperationalTruth)
	}
	if resp.RoutingState != routingDisplayStateEnabled {
		t.Fatalf("routing_state = %q", resp.RoutingState)
	}
}

func TestResolveAntigravityGeminiProviderTruthUsesFallbackProjectForProjectlessSeat(t *testing.T) {
	t.Helper()

	var emptyProjectLoads int
	var fallbackProjectLoads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:loadCodeAssist" {
			http.NotFound(w, r)
			return
		}
		var req antigravityLoadCodeAssistRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode load request: %v", err)
		}
		switch req.CloudaicompanionProject {
		case "":
			emptyProjectLoads++
			respondJSON(w, map[string]any{
				"currentTier": map[string]any{
					"id":   "free-tier",
					"name": "Antigravity",
				},
			})
		case antigravityGeminiFallbackProject:
			fallbackProjectLoads++
			respondJSON(w, map[string]any{
				"currentTier": map[string]any{
					"id":   "free-tier",
					"name": "Antigravity",
				},
			})
		default:
			t.Fatalf("unexpected project %q", req.CloudaicompanionProject)
		}
	}))
	defer server.Close()

	h := &proxyHandler{
		cfg:              config{geminiBase: mustParse(server.URL)},
		refreshTransport: server.Client().Transport,
	}

	truth, err := h.resolveAntigravityGeminiProviderTruth(context.Background(), "seat-access")
	if err != nil {
		t.Fatalf("resolveAntigravityGeminiProviderTruth: %v", err)
	}
	if truth.ProjectID != antigravityGeminiFallbackProject {
		t.Fatalf("project_id = %q", truth.ProjectID)
	}
	if truth.SubscriptionTierID != "free-tier" {
		t.Fatalf("subscription_tier_id = %q", truth.SubscriptionTierID)
	}
	if emptyProjectLoads != 1 || fallbackProjectLoads != 1 {
		t.Fatalf("load counts empty=%d fallback=%d", emptyProjectLoads, fallbackProjectLoads)
	}
}

func TestResolveAntigravityGeminiProviderTruthUsesFallbackProjectForRestrictedSeat(t *testing.T) {
	t.Helper()

	var fallbackProjectLoads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:loadCodeAssist" {
			http.NotFound(w, r)
			return
		}
		var req antigravityLoadCodeAssistRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode load request: %v", err)
		}
		if req.CloudaicompanionProject == antigravityGeminiFallbackProject {
			fallbackProjectLoads++
		}
		respondJSON(w, map[string]any{
			"ineligibleTiers": []map[string]any{
				{"reasonCode": "UNSUPPORTED_LOCATION", "reasonMessage": "region blocked"},
			},
		})
	}))
	defer server.Close()

	h := &proxyHandler{
		cfg:              config{geminiBase: mustParse(server.URL)},
		refreshTransport: server.Client().Transport,
	}

	truth, err := h.resolveAntigravityGeminiProviderTruth(context.Background(), "seat-access")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if truth.ProjectID != antigravityGeminiFallbackProject {
		t.Fatalf("project_id = %q", truth.ProjectID)
	}
	if truth.ValidationReasonCode != "UNSUPPORTED_LOCATION" {
		t.Fatalf("validation_reason_code = %q", truth.ValidationReasonCode)
	}
	if fallbackProjectLoads != 1 {
		t.Fatalf("fallback project loads = %d", fallbackProjectLoads)
	}
}

func TestOperatorGeminiSeatSmokeRequiresLoopback(t *testing.T) {
	t.Helper()

	h := &proxyHandler{}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/operator/gemini/seat-smoke", strings.NewReader(`{"account_id":"x"}`))
	req.RemoteAddr = "203.0.113.10:4321"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "loopback access required") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestOperatorGeminiSeatSmokeDefaultModelUsesGemini31Pro(t *testing.T) {
	if got := operatorGeminiSeatSmokeDefaultModel(""); got != "gemini-3.1-pro" {
		t.Fatalf("default model = %q", got)
	}
	if got := operatorGeminiSeatSmokeDefaultModel(" gemini-2.5-flash "); got != "gemini-2.5-flash" {
		t.Fatalf("explicit model = %q", got)
	}
}

func TestOperatorGeminiSeatSmokeRateLimitBecomesCooldown(t *testing.T) {
	t.Helper()
	expectedUntil := time.Now().UTC().Add(4 * time.Second).Truncate(time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			respondJSON(w, map[string]any{
				"cloudaicompanionProject": "primeval-bit-pxq56",
				"currentTier": map[string]any{
					"id":   "free-tier",
					"name": "Antigravity",
				},
			})
		case "/v1internal:generateContent":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"error": {
					"code": 429,
					"message": "You have exhausted your capacity on this model. Your quota will reset after 4s.",
					"status": "RESOURCE_EXHAUSTED",
					"details": [
						{
							"@type": "type.googleapis.com/google.rpc.ErrorInfo",
							"reason": "RATE_LIMIT_EXCEEDED",
							"metadata": {
								"quotaResetTimeStamp": %q,
								"quotaResetDelay": "4s"
							}
						},
						{
							"@type": "type.googleapis.com/google.rpc.RetryInfo",
							"retryDelay": "4s"
						}
					]
				}
			}`, expectedUntil.Format(time.RFC3339))))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 3, 27, 15, 0, 46, 0, time.UTC)
	acc := &Account{
		ID:                      "gemini-seat-ready",
		Type:                    AccountTypeGemini,
		PlanType:                "gemini",
		AuthMode:                accountAuthModeOAuth,
		AccessToken:             "seat-access",
		RefreshToken:            "refresh-token",
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		OAuthProfileID:          geminiOAuthAntigravityProfileID,
		AntigravityProjectID:    "primeval-bit-pxq56",
		GeminiProviderCheckedAt: now,
		GeminiOperationalState:  geminiOperationalTruthStateCleanOK,
		LastHealthyAt:           now.Add(-time.Minute),
	}

	h := &proxyHandler{
		cfg: config{
			geminiBase: mustParse(server.URL),
		},
		refreshTransport: server.Client().Transport,
		pool:             newPoolState([]*Account{acc}, false),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/gemini/seat-smoke", strings.NewReader(`{"account_id":"gemini-seat-ready","model":"gemini-2.5-flash"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp operatorGeminiSeatSmokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Generate.OK {
		t.Fatalf("generate unexpectedly ok: %+v", resp.Generate)
	}
	if resp.Generate.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("generate.http_status = %d", resp.Generate.HTTPStatus)
	}
	if resp.OperationalTruth == nil || resp.OperationalTruth.State != geminiOperationalTruthStateCooldown {
		t.Fatalf("operational_truth = %+v", resp.OperationalTruth)
	}
	if resp.RoutingState != routingDisplayStateDegradedEnabled {
		t.Fatalf("routing_state = %q", resp.RoutingState)
	}
	if resp.RoutingBlockReason != "" {
		t.Fatalf("routing_block_reason = %q", resp.RoutingBlockReason)
	}
	if resp.OperationalTruth.Reason == "" || !strings.Contains(strings.ToLower(resp.OperationalTruth.Reason), "capacity") {
		t.Fatalf("operational_truth = %+v", resp.OperationalTruth)
	}
	acc.mu.Lock()
	gotSeatCooldown := acc.RateLimitUntil
	gotModelCooldown := acc.GeminiModelRateLimitResetTimes["gemini-2.5-flash"]
	acc.mu.Unlock()
	if !gotSeatCooldown.IsZero() {
		t.Fatalf("expected seat-wide rate_limit_until to stay clear, got %s", gotSeatCooldown)
	}
	if gotModelCooldown.IsZero() {
		t.Fatalf("expected model cooldown for gemini-2.5-flash, got %#v", acc.GeminiModelRateLimitResetTimes)
	}
	if resp.RoutingRecoveryAt != "" {
		t.Fatalf("routing_recovery_at = %q", resp.RoutingRecoveryAt)
	}
	if resp.RequestedModelKey != "gemini-2.5-flash" {
		t.Fatalf("requested_model_key = %q", resp.RequestedModelKey)
	}
	if !resp.RequestedModelLimited {
		t.Fatalf("requested_model_limited = %#v", resp)
	}
	if resp.RequestedModelRecoveryAt != gotModelCooldown.Format(time.RFC3339) {
		t.Fatalf("requested_model_recovery_at = %q", resp.RequestedModelRecoveryAt)
	}
	if resp.RateLimitResetTimes["gemini-2.5-flash"] != gotModelCooldown.Format(time.RFC3339) {
		t.Fatalf("rate_limit_reset_times = %#v", resp.RateLimitResetTimes)
	}
	if !gotModelCooldown.Equal(expectedUntil) {
		t.Fatalf("expected model cooldown at %s, got %#v", expectedUntil, gotModelCooldown)
	}
}
