package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestResolveAntigravityGeminiProviderTruthLogsTrace(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	h := &proxyHandler{
		cfg: config{
			geminiBase: geminiBase,
		},
		refreshTransport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1internal:onboardUser":
				return jsonResponse(http.StatusOK, `{
					"done": true,
					"response": {
						"cloudaicompanionProject": {
							"id": "project-1"
						}
					}
				}`), nil
			case "/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{
					"cloudaicompanionProject": "project-1",
					"currentTier": {
						"id": "standard-tier",
						"name": "Standard"
					}
				}`), nil
			default:
				t.Fatalf("unexpected request path %s", r.URL.Path)
			}
			return nil, nil
		}),
	}

	logs := captureLogs(t, func() {
		truth, err := h.resolveAntigravityGeminiProviderTruth(testTraceContext("req-provider-truth"), "access-token")
		if err != nil {
			t.Fatalf("resolveAntigravityGeminiProviderTruth: %v", err)
		}
		if truth.ProjectID != "project-1" {
			t.Fatalf("ProjectID=%q", truth.ProjectID)
		}
		if truth.SubscriptionTierID != "standard-tier" {
			t.Fatalf("SubscriptionTierID=%q", truth.SubscriptionTierID)
		}
	})

	if !strings.Contains(logs, "[req-provider-truth] trace provider_truth") {
		t.Fatalf("missing provider_truth trace log: %s", logs)
	}
	if !strings.Contains(logs, `provider=gemini`) || !strings.Contains(logs, `stage="resolve"`) || !strings.Contains(logs, `result=ok`) {
		t.Fatalf("unexpected provider_truth trace log: %s", logs)
	}
	if !strings.Contains(logs, `project_id="project-1"`) || !strings.Contains(logs, `tier_id="standard-tier"`) {
		t.Fatalf("missing provider truth fields in log: %s", logs)
	}
}

func TestResolveAntigravityGeminiProviderTruthLogsValidationReason(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	h := &proxyHandler{
		cfg: config{
			geminiBase: geminiBase,
		},
		refreshTransport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1internal:onboardUser":
				return jsonResponse(http.StatusBadRequest, `{"error":"validation required"}`), nil
			case "/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{
					"ineligibleTiers": [{
						"id": "standard-tier",
						"reasonCode": "ACCOUNT_VALIDATION_REQUIRED",
						"reasonMessage": "Validate your account",
						"validationUrl": "https://example.com/verify"
					}]
				}`), nil
			default:
				t.Fatalf("unexpected request path %s", r.URL.Path)
			}
			return nil, nil
		}),
	}

	logs := captureLogs(t, func() {
		_, err := h.resolveAntigravityGeminiProviderTruth(testTraceContext("req-provider-validation"), "access-token")
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !strings.Contains(err.Error(), "Validate your account") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(logs, "[req-provider-validation] trace provider_truth") {
		t.Fatalf("missing provider_truth trace log: %s", logs)
	}
	if !strings.Contains(logs, `result=validation_blocked`) {
		t.Fatalf("missing validation_blocked result in trace log: %s", logs)
	}
	if !strings.Contains(logs, `validation_reason="ACCOUNT_VALIDATION_REQUIRED"`) {
		t.Fatalf("missing validation reason in trace log: %s", logs)
	}
}

func TestResolveAntigravityGeminiProviderTruthStopsAfterStandardTierValidation(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	var onboardCalls []string
	h := &proxyHandler{
		cfg: config{
			geminiBase: geminiBase,
		},
		refreshTransport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1internal:onboardUser":
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read onboard body: %v", err)
				}
				payload := string(body)
				onboardCalls = append(onboardCalls, payload)
				if !strings.Contains(payload, `"tierId":"standard-tier"`) {
					t.Fatalf("unexpected fallback onboard call: %s", payload)
				}
				return jsonResponse(http.StatusOK, `{"done":true}`), nil
			case "/v1internal:loadCodeAssist":
				return jsonResponse(http.StatusOK, `{
					"ineligibleTiers": [{
						"reasonCode": "UNSUPPORTED_LOCATION",
						"reasonMessage": "Your current account is not eligible for Gemini Code Assist for individuals because it is not currently available in your location."
					}]
				}`), nil
			default:
				t.Fatalf("unexpected request path %s", r.URL.Path)
			}
			return nil, nil
		}),
	}

	truth, err := h.resolveAntigravityGeminiProviderTruth(testTraceContext("req-provider-location"), "access-token")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "not currently available in your location") {
		t.Fatalf("unexpected error: %v", err)
	}
	if truth.ValidationReasonCode != "UNSUPPORTED_LOCATION" {
		t.Fatalf("ValidationReasonCode=%q", truth.ValidationReasonCode)
	}
	if len(onboardCalls) != 1 {
		t.Fatalf("onboard calls=%d want 1", len(onboardCalls))
	}
}

func TestAntigravityGeminiProviderTruthFromLoadIgnoresIneligibleTierWhenProjectReady(t *testing.T) {
	checkedAt := time.Date(2026, 3, 25, 19, 40, 0, 0, time.UTC)
	truth := antigravityGeminiProviderTruthFromLoad(&antigravityLoadCodeAssistResponse{
		CloudaicompanionProject: "project-1",
		CurrentTier: &antigravityTier{
			ID:   "standard-tier",
			Name: "Standard",
		},
		IneligibleTiers: []antigravityIneligibleTier{{
			ReasonCode:    "ACCOUNT_VALIDATION_REQUIRED",
			ReasonMessage: "Validate your account",
			ValidationURL: "https://example.com/verify",
		}},
	}, "", checkedAt)

	if truth.ProjectID != "project-1" {
		t.Fatalf("ProjectID=%q", truth.ProjectID)
	}
	if truth.SubscriptionTierID != "standard-tier" {
		t.Fatalf("SubscriptionTierID=%q", truth.SubscriptionTierID)
	}
	if hasAntigravityValidationTruth(truth) {
		t.Fatalf("expected validation fields to stay empty for usable project/tier truth: %+v", truth)
	}
	if !truth.ProviderCheckedAt.Equal(checkedAt) {
		t.Fatalf("ProviderCheckedAt=%v", truth.ProviderCheckedAt)
	}
}

func TestCompleteAntigravityGeminiOAuthLogsSingleExchangeFailure(t *testing.T) {
	h := &proxyHandler{
		refreshTransport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != geminiOAuthTokenURL {
				t.Fatalf("unexpected request URL: %s", r.URL.String())
			}
			return jsonResponse(http.StatusBadGateway, `{"error":"upstream down"}`), nil
		}),
	}

	logs := captureLogs(t, func() {
		_, err := h.completeAntigravityGeminiOAuth(testTraceContext("req-antigravity-oauth-fail"), "test-code", &antigravityGeminiOAuthSession{
			RedirectURI: "http://127.0.0.1/oauth-callback",
		})
		if err == nil {
			t.Fatal("expected oauth exchange error")
		}
	})

	if got := strings.Count(logs, "trace oauth_exchange"); got != 1 {
		t.Fatalf("oauth_exchange trace count=%d, logs=%s", got, logs)
	}
	if !strings.Contains(logs, `lane="antigravity"`) || !strings.Contains(logs, `result=fail`) {
		t.Fatalf("unexpected oauth_exchange trace log: %s", logs)
	}
}

func TestCompleteManagedGeminiOAuthLogsSingleExchangeFailure(t *testing.T) {
	h := &proxyHandler{
		refreshTransport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != geminiOAuthTokenURL {
				t.Fatalf("unexpected request URL: %s", r.URL.String())
			}
			return jsonResponse(http.StatusBadGateway, `{"error":"upstream down"}`), nil
		}),
	}

	logs := captureLogs(t, func() {
		_, err := h.completeManagedGeminiOAuth(testTraceContext("req-managed-oauth-fail"), "test-code", &managedGeminiOAuthSession{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RedirectURI:  "http://127.0.0.1/operator/gemini/oauth-callback",
		})
		if err == nil {
			t.Fatal("expected oauth exchange error")
		}
	})

	if got := strings.Count(logs, "trace oauth_exchange"); got != 1 {
		t.Fatalf("oauth_exchange trace count=%d, logs=%s", got, logs)
	}
	if !strings.Contains(logs, `lane="managed"`) || !strings.Contains(logs, `result=fail`) {
		t.Fatalf("unexpected oauth_exchange trace log: %s", logs)
	}
}

func TestBuildAntigravityCodeAssistMetadataUsesAntigravityIdentity(t *testing.T) {
	metadata := buildAntigravityCodeAssistMetadata("project-1")

	if metadata.IdeType != "ANTIGRAVITY" {
		t.Fatalf("IdeType=%q", metadata.IdeType)
	}
	if metadata.Platform != antigravityCodeAssistPlatform() {
		t.Fatalf("Platform=%q", metadata.Platform)
	}
	if metadata.PluginType != "GEMINI" {
		t.Fatalf("PluginType=%q", metadata.PluginType)
	}
	if metadata.IdeVersion != antigravityIDEVersion {
		t.Fatalf("IdeVersion=%q", metadata.IdeVersion)
	}
	if metadata.PluginVersion != antigravityIDEVersion {
		t.Fatalf("PluginVersion=%q", metadata.PluginVersion)
	}
	if metadata.UpdateChannel != antigravityUpdateChannel {
		t.Fatalf("UpdateChannel=%q", metadata.UpdateChannel)
	}
	if metadata.IdeName != antigravityIDEName {
		t.Fatalf("IdeName=%q", metadata.IdeName)
	}
	if metadata.DuetProject != "project-1" {
		t.Fatalf("DuetProject=%q", metadata.DuetProject)
	}
}

func TestNormalizeAntigravityGeminiQuotaPayloadNormalizesCamelCase(t *testing.T) {
	quota, protectedModels := normalizeAntigravityGeminiQuotaPayload(map[string]any{
		"lastUpdated":      float64(1774353900),
		"subscriptionTier": "Antigravity",
		"modelForwardingRules": map[string]any{
			"gemini-1.5-pro": "gemini-2.5-pro",
		},
		"protectedModels": []any{"gemini-3.1-pro-high"},
		"models": []any{
			map[string]any{
				"name":             "gemini-3.1-pro-high",
				"percentage":       float64(67),
				"resetTime":        "2026-03-24T15:00:00Z",
				"displayName":      "Gemini 3.1 Pro High",
				"supportsImages":   true,
				"supportsThinking": true,
				"thinkingBudget":   float64(24576),
				"recommended":      true,
				"maxTokens":        float64(1048576),
				"maxOutputTokens":  float64(65535),
				"supportedMimeTypes": map[string]any{
					"application/pdf": true,
				},
			},
		},
	}, time.Now().UTC())

	if len(protectedModels) != 1 || protectedModels[0] != "gemini-3.1-pro-high" {
		t.Fatalf("protectedModels=%#v", protectedModels)
	}
	if quota["last_updated"] != int64(1774353900) {
		t.Fatalf("last_updated=%#v", quota["last_updated"])
	}
	if quota["subscription_tier"] != "Antigravity" {
		t.Fatalf("subscription_tier=%#v", quota["subscription_tier"])
	}
	rules, _ := quota["model_forwarding_rules"].(map[string]string)
	if rules["gemini-1.5-pro"] != "gemini-2.5-pro" {
		t.Fatalf("model_forwarding_rules=%#v", quota["model_forwarding_rules"])
	}
	models, _ := quota["models"].([]GeminiModelQuotaSnapshot)
	if len(models) != 1 {
		t.Fatalf("models=%#v", quota["models"])
	}
	if models[0].ResetTime != "2026-03-24T15:00:00Z" {
		t.Fatalf("ResetTime=%q", models[0].ResetTime)
	}
	if models[0].MaxOutputTokens != 65535 {
		t.Fatalf("MaxOutputTokens=%d", models[0].MaxOutputTokens)
	}
	if !models[0].SupportedMimeTypes["application/pdf"] {
		t.Fatalf("SupportedMimeTypes=%#v", models[0].SupportedMimeTypes)
	}
}

func TestNormalizeAntigravityGeminiQuotaPayloadNormalizesUpstreamModelMap(t *testing.T) {
	quota, protectedModels := normalizeAntigravityGeminiQuotaPayload(map[string]any{
		"models": map[string]any{
			"gemini-3.1-pro-high": map[string]any{
				"quotaInfo": map[string]any{
					"remainingFraction": 0.67,
					"resetTime":         "2026-03-24T15:00:00Z",
				},
				"displayName":      "Gemini 3.1 Pro High",
				"supportsImages":   true,
				"supportsThinking": true,
				"thinkingBudget":   float64(24576),
				"recommended":      true,
				"maxTokens":        float64(1048576),
				"maxOutputTokens":  float64(65535),
				"supportedMimeTypes": map[string]any{
					"application/pdf": true,
				},
			},
			"chat-bison-internal": map[string]any{
				"quotaInfo": map[string]any{
					"remainingFraction": 0.99,
					"resetTime":         "2026-03-24T16:00:00Z",
				},
				"displayName": "Internal Chat",
			},
		},
		"deprecatedModelIds": map[string]any{
			"gemini-1.5-pro": map[string]any{
				"newModelId": "gemini-2.5-pro",
			},
		},
	}, time.Time{})

	if len(protectedModels) != 0 {
		t.Fatalf("protectedModels=%#v", protectedModels)
	}
	rules, _ := quota["model_forwarding_rules"].(map[string]string)
	if rules["gemini-1.5-pro"] != "gemini-2.5-pro" {
		t.Fatalf("model_forwarding_rules=%#v", quota["model_forwarding_rules"])
	}
	models, _ := quota["models"].([]GeminiModelQuotaSnapshot)
	if len(models) != 1 {
		t.Fatalf("models=%#v", quota["models"])
	}
	if models[0].Name != "gemini-3.1-pro-high" {
		t.Fatalf("Name=%q", models[0].Name)
	}
	if models[0].Percentage != 67 {
		t.Fatalf("Percentage=%d", models[0].Percentage)
	}
	if models[0].ResetTime != "2026-03-24T15:00:00Z" {
		t.Fatalf("ResetTime=%q", models[0].ResetTime)
	}
	if !models[0].SupportedMimeTypes["application/pdf"] {
		t.Fatalf("SupportedMimeTypes=%#v", models[0].SupportedMimeTypes)
	}
	if _, ok := quota["last_updated"]; ok {
		t.Fatalf("last_updated should stay absent before fetch-time fallback: %#v", quota["last_updated"])
	}
}

func TestNormalizeAntigravityGeminiQuotaPayloadFiltersCodexModelsAndClassifiesRoutes(t *testing.T) {
	quota, protectedModels := normalizeAntigravityGeminiQuotaPayload(map[string]any{
		"models": map[string]any{
			"claude-sonnet-4-6": map[string]any{
				"quotaInfo": map[string]any{
					"remainingFraction": 0.48,
				},
				"displayName": "Claude Sonnet 4.6",
			},
			"gpt-oss-120b-medium": map[string]any{
				"quotaInfo": map[string]any{
					"remainingFraction": 0.91,
				},
				"displayName": "GPT OSS 120B Medium",
			},
			"gemini-2.5-flash": map[string]any{
				"quotaInfo": map[string]any{
					"remainingFraction": 0.73,
				},
				"displayName": "Gemini 2.5 Flash",
			},
		},
		"protectedModels": []any{"claude-sonnet-4-6"},
	}, time.Time{})

	if len(protectedModels) != 1 || protectedModels[0] != "claude-sonnet-4-6" {
		t.Fatalf("protectedModels=%#v", protectedModels)
	}

	models, _ := quota["models"].([]GeminiModelQuotaSnapshot)
	if len(models) != 2 {
		t.Fatalf("models=%#v", quota["models"])
	}
	if models[0].Name != "gemini-2.5-flash" || models[0].RouteProvider != "gemini" {
		t.Fatalf("models[0]=%#v", models[0])
	}
	if models[1].Name != "claude-sonnet-4-6" || models[1].RouteProvider != "claude" {
		t.Fatalf("models[1]=%#v", models[1])
	}
	for _, model := range models {
		if model.Name == "gpt-oss-120b-medium" {
			t.Fatalf("unexpected codex model in operator truth: %#v", models)
		}
	}
}
