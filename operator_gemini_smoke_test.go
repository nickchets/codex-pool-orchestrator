package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	h := &proxyHandler{
		cfg: config{
			geminiBase: mustParse(server.URL),
		},
		refreshTransport: server.Client().Transport,
		pool: newPoolState([]*Account{
			{
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
			},
		}, false),
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
