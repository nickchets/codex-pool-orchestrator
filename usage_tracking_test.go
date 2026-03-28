package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchUsageCodexRefreshInvalidKeepsLiveCurrentAccess(t *testing.T) {
	whamBase, err := url.Parse("https://chatgpt.com/backend-api")
	if err != nil {
		t.Fatalf("parse wham base: %v", err)
	}
	responsesBase, err := url.Parse("https://chatgpt.com")
	if err != nil {
		t.Fatalf("parse responses base: %v", err)
	}
	refreshBase, err := url.Parse("https://auth.openai.com")
	if err != nil {
		t.Fatalf("parse refresh base: %v", err)
	}

	accFile := filepath.Join(t.TempDir(), "seat-live.json")
	if err := os.WriteFile(accFile, []byte(`{"tokens":{"access_token":"current-access","refresh_token":"old-refresh","account_id":"acct-1"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:           "seat-live",
		Type:         AccountTypeCodex,
		File:         accFile,
		AccessToken:  "current-access",
		RefreshToken: "old-refresh",
		AccountID:    "acct-1",
		ExpiresAt:    time.Now().Add(-time.Hour).UTC(),
	}

	var refreshCalls, probeCalls, usageCalls int
	h := &proxyHandler{
		cfg:  config{whamBase: whamBase},
		pool: newPoolState([]*Account{acc}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeCodex: NewCodexProvider(responsesBase, whamBase, refreshBase, responsesBase),
			},
		},
		transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/backend-api/codex/models":
				probeCalls++
				if got := req.Header.Get("Authorization"); got != "Bearer current-access" {
					t.Fatalf("probe auth=%q", got)
				}
				return jsonResponse(http.StatusOK, `{"models":[{"id":"gpt-5.4"}]}`), nil
			case "/backend-api/wham/usage":
				usageCalls++
				if got := req.Header.Get("Authorization"); got != "Bearer current-access" {
					t.Fatalf("usage auth=%q", got)
				}
				return jsonResponse(http.StatusOK, `{"rate_limit":{"primary_window":{"used_percent":42,"reset_at":1740000000},"secondary_window":{"used_percent":84,"reset_at":1740003600}}}`), nil
			default:
				t.Fatalf("unexpected transport URL %s", req.URL.String())
			}
			return nil, nil
		}),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			refreshCalls++
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

	err = h.fetchUsage(time.Now().UTC(), acc)
	if err != nil {
		t.Fatalf("fetchUsage error: %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if probeCalls != 1 {
		t.Fatalf("probeCalls=%d", probeCalls)
	}
	if usageCalls != 1 {
		t.Fatalf("usageCalls=%d", usageCalls)
	}
	if acc.Dead {
		t.Fatal("expected account to stay live")
	}
	if acc.HealthStatus != codexRefreshInvalidHealthStatus {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != codexRefreshInvalidHealthError {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if acc.LastHealthyAt.IsZero() {
		t.Fatal("expected last_healthy_at to be set")
	}
	if acc.Usage.PrimaryUsedPercent != 0.42 || acc.Usage.SecondaryUsedPercent != 0.84 {
		t.Fatalf("usage=%+v", acc.Usage)
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
	if saved["dead"] == true {
		t.Fatalf("saved dead=%#v", saved["dead"])
	}
}

func TestFetchUsageCodexUnauthorizedAfterRefreshInvalidKeepsLiveWhenModelsStillWork(t *testing.T) {
	whamBase, err := url.Parse("https://chatgpt.com/backend-api")
	if err != nil {
		t.Fatalf("parse wham base: %v", err)
	}
	responsesBase, err := url.Parse("https://chatgpt.com")
	if err != nil {
		t.Fatalf("parse responses base: %v", err)
	}
	refreshBase, err := url.Parse("https://auth.openai.com")
	if err != nil {
		t.Fatalf("parse refresh base: %v", err)
	}

	acc := &Account{
		ID:           "seat-unauthorized",
		Type:         AccountTypeCodex,
		AccessToken:  "current-access",
		RefreshToken: "old-refresh",
		AccountID:    "acct-1",
		ExpiresAt:    time.Now().Add(time.Hour).UTC(),
	}

	var refreshCalls, probeCalls, usageCalls int
	h := &proxyHandler{
		cfg: config{whamBase: whamBase},
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeCodex: NewCodexProvider(responsesBase, whamBase, refreshBase, responsesBase),
			},
		},
		transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/backend-api/wham/usage":
				usageCalls++
				return jsonResponse(http.StatusUnauthorized, `{"error":"stale usage token"}`), nil
			case "/backend-api/codex/models":
				probeCalls++
				return jsonResponse(http.StatusOK, `{"models":[{"id":"gpt-5.4"}]}`), nil
			default:
				t.Fatalf("unexpected transport URL %s", req.URL.String())
			}
			return nil, nil
		}),
		refreshTransport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			refreshCalls++
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

	err = h.fetchUsage(time.Now().UTC(), acc)
	if err == nil || !strings.Contains(err.Error(), "current codex access still works") {
		t.Fatalf("fetchUsage error=%v", err)
	}
	if usageCalls != 1 {
		t.Fatalf("usageCalls=%d", usageCalls)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if probeCalls != 1 {
		t.Fatalf("probeCalls=%d", probeCalls)
	}
	if acc.Dead {
		t.Fatal("expected account to stay live")
	}
	if acc.HealthStatus != codexRefreshInvalidHealthStatus {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != codexRefreshInvalidHealthError {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
}
