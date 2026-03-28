package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForceRefreshAccountPersistsPermanentCodexRefreshFailure(t *testing.T) {
	base, err := url.Parse("https://chatgpt.com")
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	refreshBase, err := url.Parse("https://auth.openai.com")
	if err != nil {
		t.Fatalf("parse refresh base: %v", err)
	}

	accFile := filepath.Join(t.TempDir(), "seat-a.json")
	if err := os.WriteFile(accFile, []byte(`{"tokens":{"access_token":"old-access","refresh_token":"old-refresh"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:           "seat-a",
		Type:         AccountTypeCodex,
		File:         accFile,
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
	}
	h := &proxyHandler{
		pool: newPoolState([]*Account{acc}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeCodex: NewCodexProvider(base, base, refreshBase, base),
			},
		},
		transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/codex/models" {
				t.Fatalf("unexpected probe URL %s", req.URL.String())
			}
			if got := req.Header.Get("Authorization"); got != "Bearer old-access" {
				t.Fatalf("probe auth=%q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"models":[{"id":"gpt-5.4"}]}`)),
			}, nil
		}),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{
  "error": {
    "message": "Your refresh token has already been used to generate a new access token. Please try signing in again.",
    "type": "invalid_request_error",
    "code": "refresh_token_reused"
  }
}`)),
			}, nil
		}),
	}

	rec := httptest.NewRecorder()
	h.forceRefreshAccount(rec, "seat-a")

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" {
		t.Fatalf("status=%v", payload["status"])
	}
	if acc.HealthStatus != codexRefreshInvalidHealthStatus {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != codexRefreshInvalidHealthError {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if acc.Dead {
		t.Fatal("expected account to stay live while current access token still works")
	}

	raw, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if saved["health_status"] != codexRefreshInvalidHealthStatus {
		t.Fatalf("saved health_status=%#v", saved["health_status"])
	}
	if saved["health_error"] != codexRefreshInvalidHealthError {
		t.Fatalf("saved health_error=%#v", saved["health_error"])
	}
	if saved["dead"] == true {
		t.Fatalf("saved dead=%#v", saved["dead"])
	}
}

func TestForceRefreshAccountMarksDeadWhenCodexModelsProbeConfirmsDeactivatedWorkspace(t *testing.T) {
	base, err := url.Parse("https://chatgpt.com")
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	refreshBase, err := url.Parse("https://auth.openai.com")
	if err != nil {
		t.Fatalf("parse refresh base: %v", err)
	}

	accFile := filepath.Join(t.TempDir(), "seat-dead.json")
	if err := os.WriteFile(accFile, []byte(`{"tokens":{"access_token":"old-access","refresh_token":"old-refresh"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:           "seat-dead",
		Type:         AccountTypeCodex,
		File:         accFile,
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
	}
	h := &proxyHandler{
		pool: newPoolState([]*Account{acc}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeCodex: NewCodexProvider(base, base, refreshBase, base),
			},
		},
		transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/codex/models" {
				t.Fatalf("unexpected probe URL %s", req.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusPaymentRequired,
				Status:     "402 Payment Required",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"detail":{"code":"deactivated_workspace"}}`)),
			}, nil
		}),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{
  "error": {
    "message": "Your refresh token has already been used to generate a new access token. Please try signing in again.",
    "type": "invalid_request_error",
    "code": "refresh_token_reused"
  }
}`)),
			}, nil
		}),
	}

	rec := httptest.NewRecorder()
	h.forceRefreshAccount(rec, "seat-dead")

	if acc.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.Dead {
		t.Fatal("expected account to be marked dead")
	}
	if acc.HealthError != `{"detail":{"code":"deactivated_workspace"}}` {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
}
