package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHydrateAntigravityGeminiQuotaForAccountPersistsQuota(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:fetchAvailableModels" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer live-access" {
			t.Fatalf("authorization=%q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept=%q", got)
		}
		if got := r.Header.Get("User-Agent"); got != antigravityCodeAssistUA {
			t.Fatalf("user-agent=%q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if project, _ := payload["project"].(string); project != "" {
			_, _ = w.Write([]byte(`{
				"models": {
					"tab_flash_lite_preview": {
						"maxTokens": 16384,
						"maxOutputTokens": 4096,
						"quotaInfo": {
							"remainingFraction": 1
						}
					}
				}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"models": {
				"gemini-3-flash": {
					"displayName": "Gemini 3 Flash",
					"supportsImages": true,
					"supportsThinking": true,
					"thinkingBudget": 32,
					"recommended": true,
					"maxTokens": 1048576,
					"maxOutputTokens": 65536,
					"quotaInfo": {
						"remainingFraction": 1,
						"resetTime": "2026-04-02T12:48:18Z"
					}
				}
			},
			"protectedModels": ["gemini-3-flash"]
		}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	geminiDir := filepath.Join(dir, "gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("mkdir gemini dir: %v", err)
	}

	accountPath := filepath.Join(geminiDir, "gemini_ready.json")
	if err := os.WriteFile(accountPath, []byte(`{
		"access_token": "live-access",
		"refresh_token": "live-refresh",
		"token_type": "Bearer",
		"expiry_date": 4102444800000,
		"operator_source": "antigravity_import",
		"operator_email": "ready@example.com",
		"antigravity_source": "browser_oauth",
		"antigravity_project_id": "psyched-sphere-vj8c5",
		"gemini_provider_truth_ready": true,
		"gemini_provider_truth_state": "ready",
		"health_status": "imported"
	}`), 0o600); err != nil {
		t.Fatalf("write account: %v", err)
	}

	base := mustParse(server.URL)
	registry := NewProviderRegistry(
		NewCodexProvider(mustParse("https://chatgpt.com"), mustParse("https://chatgpt.com"), mustParse("https://auth.openai.com"), mustParse("https://api.openai.com")),
		NewClaudeProvider(mustParse("https://api.anthropic.com")),
		NewGeminiProvider(base, mustParse("https://generativelanguage.googleapis.com")),
	)
	accounts, err := loadPool(dir, registry)
	if err != nil {
		t.Fatalf("loadPool: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts=%d", len(accounts))
	}

	h := &proxyHandler{
		cfg:              config{poolDir: dir, geminiBase: base},
		transport:        server.Client().Transport,
		refreshTransport: server.Client().Transport,
		registry:         registry,
		pool:             newPoolState(accounts, false),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.hydrateAntigravityGeminiQuotaForAccount(ctx, accounts[0]); err != nil {
		t.Fatalf("hydrateAntigravityGeminiQuotaForAccount: %v", err)
	}

	var persisted map[string]any
	raw, err := os.ReadFile(accountPath)
	if err != nil {
		t.Fatalf("read persisted account: %v", err)
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("unmarshal persisted account: %v", err)
	}

	quota, _ := persisted["antigravity_quota"].(map[string]any)
	if len(quota) == 0 {
		t.Fatalf("antigravity_quota missing: %v", persisted)
	}
	models, _ := persisted["gemini_quota_models"].([]any)
	if len(models) == 0 {
		t.Fatalf("gemini_quota_models missing: %v", persisted)
	}
	protectedModels, _ := persisted["gemini_protected_models"].([]any)
	if len(protectedModels) != 1 {
		t.Fatalf("gemini_protected_models=%v", protectedModels)
	}
}

func TestApplyAntigravityGeminiQuotaRefreshLockedMarksEmptySnapshotFresh(t *testing.T) {
	now := time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC)
	acc := &Account{
		ID:                      "gemini-seat",
		Type:                    AccountTypeGemini,
		OperatorSource:          geminiOperatorSourceAntigravityImport,
		AntigravitySource:       "browser_auth",
		AntigravityProjectID:    "project-1",
		GeminiProviderCheckedAt: now.Add(-time.Minute),
		GeminiQuotaUpdatedAt:    now.Add(-2 * time.Hour),
		GeminiQuotaModels: []GeminiModelQuotaSnapshot{{
			Name:       "gemini-3.1-pro-high",
			Percentage: 42,
		}},
		GeminiProtectedModels: []string{"gemini-3.1-pro-high"},
		AntigravityQuota: map[string]any{
			"models": map[string]any{"gemini-3.1-pro-high": map[string]any{}},
		},
	}

	acc.mu.Lock()
	applyAntigravityGeminiQuotaRefreshLocked(acc, nil, nil, now)
	freshness := geminiProviderTruthFreshnessStatus(acc.GeminiProviderTruthState, acc.GeminiProviderCheckedAt, acc.GeminiQuotaUpdatedAt, now)
	acc.mu.Unlock()

	if freshness.Stale {
		t.Fatalf("expected empty quota refresh to stay fresh, got %+v", freshness)
	}
	if len(acc.GeminiQuotaModels) != 0 {
		t.Fatalf("expected quota models to be cleared, got %+v", acc.GeminiQuotaModels)
	}
	if len(acc.GeminiProtectedModels) != 0 {
		t.Fatalf("expected protected models to be cleared, got %+v", acc.GeminiProtectedModels)
	}
	if !acc.GeminiQuotaUpdatedAt.Equal(now) {
		t.Fatalf("quota_updated_at=%s, want %s", acc.GeminiQuotaUpdatedAt, now)
	}
}

func TestGeminiCodeAssistCooldownInfoFallsBackToTextMetadata(t *testing.T) {
	now := time.Date(2026, 3, 27, 15, 31, 42, 0, time.UTC)
	wantUntil := time.Date(2026, 3, 27, 15, 31, 46, 0, time.UTC)
	err := &geminiCodeAssistHTTPError{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Message:    `gemini code assist request failed: 429 Too Many Requests: {"error":{"message":"You have exhausted your capacity on this model. Your quota will reset after 3s.","details":[{"metadata":{"quotaResetTimeStamp":"2026-03-27T15:31:46Z","quotaResetDelay":"3.923606893s"}},{"retryDelay":"3.923606893s"}]}}`,
	}

	gotUntil, _, precise, ok := geminiCodeAssistCooldownInfo(err, now)
	if !ok {
		t.Fatal("expected cooldown info")
	}
	if !precise {
		t.Fatal("expected precise cooldown metadata")
	}
	if !gotUntil.Equal(wantUntil) {
		t.Fatalf("until=%s, want %s", gotUntil, wantUntil)
	}
}
