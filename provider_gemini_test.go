package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	testGeminiOAuthCLIClientID     = "test-gemini-cli-client-id"
	testGeminiOAuthCLIClientSecret = "test-gemini-cli-client-secret"
	testGeminiOAuthGCloudClientID  = "test-gcloud-client-id"
	testGeminiOAuthGCloudSecret    = "test-gcloud-client-secret"
)

func setGeminiOAuthTestProfiles(t *testing.T) {
	t.Helper()
	t.Setenv(geminiOAuthCLIClientIDVar, testGeminiOAuthCLIClientID)
	t.Setenv(geminiOAuthCLIClientSecretVar, testGeminiOAuthCLIClientSecret)
	t.Setenv(geminiOAuthGCloudClientIDVar, testGeminiOAuthGCloudClientID)
	t.Setenv(geminiOAuthGCloudClientSecretVar, testGeminiOAuthGCloudSecret)
}

func TestGeminiProviderLoadAccountLoadsPersistedState(t *testing.T) {
	rateLimitUntil := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	healthCheckedAt := time.Date(2026, 3, 23, 11, 45, 0, 0, time.UTC)
	lastHealthyAt := time.Date(2026, 3, 23, 10, 30, 0, 0, time.UTC)
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"client_id": "client-id",
		"client_secret": "client-secret",
		"expiry_date": 1774353600000,
		"plan_type": "gemini",
		"last_refresh": "2026-03-23T10:00:00Z",
		"rate_limit_until": "2026-03-24T12:00:00Z",
		"health_status": "quota_exceeded",
		"health_error": "quota",
		"health_checked_at": "2026-03-23T11:45:00Z",
		"last_healthy_at": "2026-03-23T10:30:00Z",
		"disabled": true,
		"dead": true
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_test.json", "/tmp/gemini_test.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if !acc.Disabled {
		t.Fatal("expected Disabled to load")
	}
	if !acc.Dead {
		t.Fatal("expected Dead to load")
	}
	if acc.RateLimitUntil != rateLimitUntil {
		t.Fatalf("RateLimitUntil = %v, want %v", acc.RateLimitUntil, rateLimitUntil)
	}
	if acc.HealthStatus != "quota_exceeded" {
		t.Fatalf("HealthStatus = %q", acc.HealthStatus)
	}
	if acc.HealthError != "quota" {
		t.Fatalf("HealthError = %q", acc.HealthError)
	}
	if acc.OAuthClientID != "client-id" {
		t.Fatalf("OAuthClientID = %q", acc.OAuthClientID)
	}
	if acc.OAuthClientSecret != "client-secret" {
		t.Fatalf("OAuthClientSecret = %q", acc.OAuthClientSecret)
	}
	if acc.HealthCheckedAt != healthCheckedAt {
		t.Fatalf("HealthCheckedAt = %v, want %v", acc.HealthCheckedAt, healthCheckedAt)
	}
	if acc.LastHealthyAt != lastHealthyAt {
		t.Fatalf("LastHealthyAt = %v, want %v", acc.LastHealthyAt, lastHealthyAt)
	}
}

func TestGeminiProviderLoadAccountLoadsOAuthProfileID(t *testing.T) {
	raw := []byte(`{
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"oauth_profile_id": "gcloud",
		"expiry_date": 1774353600000
	}`)

	acc, err := (&GeminiProvider{}).LoadAccount("gemini_profile.json", "/tmp/gemini_profile.json", raw)
	if err != nil {
		t.Fatalf("LoadAccount error: %v", err)
	}
	if acc == nil {
		t.Fatal("expected Gemini account")
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID = %q", acc.OAuthProfileID)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to stay empty, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}
}

func TestSaveGeminiAccountPersistsStateFields(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_state.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token": "old-access",
		"refresh_token": "old-refresh",
		"scope": "scope",
		"extra": "keep-me"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:                "gemini_state",
		Type:              AccountTypeGemini,
		File:              accFile,
		AccessToken:       "new-access",
		RefreshToken:      "new-refresh",
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		ExpiresAt:         time.Date(2026, 3, 25, 8, 0, 0, 0, time.UTC),
		LastRefresh:       time.Date(2026, 3, 23, 9, 30, 0, 0, time.UTC),
		RateLimitUntil:    time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC),
		HealthStatus:      "quota_exceeded",
		HealthError:       "quota",
		HealthCheckedAt:   time.Date(2026, 3, 23, 11, 0, 0, 0, time.UTC),
		LastHealthyAt:     time.Date(2026, 3, 23, 8, 45, 0, 0, time.UTC),
		Disabled:          true,
		Dead:              true,
	}

	if err := saveGeminiAccount(acc); err != nil {
		t.Fatalf("saveGeminiAccount error: %v", err)
	}

	var root map[string]any
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}

	if root["extra"] != "keep-me" {
		t.Fatalf("expected custom field to be preserved, got %#v", root["extra"])
	}
	if root["client_id"] != "client-id" {
		t.Fatalf("client_id = %#v", root["client_id"])
	}
	if root["client_secret"] != "client-secret" {
		t.Fatalf("client_secret = %#v", root["client_secret"])
	}
	if root["health_status"] != "quota_exceeded" {
		t.Fatalf("health_status = %#v", root["health_status"])
	}
	if root["health_error"] != "quota" {
		t.Fatalf("health_error = %#v", root["health_error"])
	}
	if root["disabled"] != true {
		t.Fatalf("disabled = %#v", root["disabled"])
	}
	if root["dead"] != true {
		t.Fatalf("dead = %#v", root["dead"])
	}
	if root["rate_limit_until"] != "2026-03-23T12:00:00Z" {
		t.Fatalf("rate_limit_until = %#v", root["rate_limit_until"])
	}
	if root["health_checked_at"] != "2026-03-23T11:00:00Z" {
		t.Fatalf("health_checked_at = %#v", root["health_checked_at"])
	}
	if root["last_healthy_at"] != "2026-03-23T08:45:00Z" {
		t.Fatalf("last_healthy_at = %#v", root["last_healthy_at"])
	}
}

func TestSaveGeminiAccountPersistsOAuthProfileID(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_profile_state.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token": "old-access",
		"refresh_token": "old-refresh",
		"client_id": "legacy-client",
		"client_secret": "legacy-secret"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:             "gemini_profile_state",
		Type:           AccountTypeGemini,
		File:           accFile,
		AccessToken:    "new-access",
		RefreshToken:   "new-refresh",
		OAuthProfileID: "gcloud",
	}

	if err := saveGeminiAccount(acc); err != nil {
		t.Fatalf("saveGeminiAccount error: %v", err)
	}

	var root map[string]any
	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}

	if root["oauth_profile_id"] != "gcloud" {
		t.Fatalf("oauth_profile_id = %#v", root["oauth_profile_id"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected client_id to be dropped: %s", string(saved))
	}
	if _, ok := root["client_secret"]; ok {
		t.Fatalf("expected client_secret to be dropped: %s", string(saved))
	}
}

func TestFinalizeProxyResponsePersistsHealthyGeminiRecovery(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_dead.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token",
		"rate_limit_until":"2026-03-23T12:00:00Z",
		"health_status":"quota_exceeded",
		"health_error":"quota",
		"dead":true
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:             "gemini_dead",
		Type:           AccountTypeGemini,
		File:           accFile,
		AccessToken:    "access-token",
		RefreshToken:   "refresh-token",
		Dead:           true,
		RateLimitUntil: time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC),
		HealthStatus:   "quota_exceeded",
		HealthError:    "quota",
	}

	h.finalizeProxyResponse("req-test", &GeminiProvider{}, acc, "pool-user", http.StatusOK, true, false, "", 0, 0, nil)

	if acc.Dead {
		t.Fatal("expected gemini account to be resurrected")
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", acc.RateLimitUntil)
	}
	if acc.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != "" {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if acc.HealthCheckedAt.IsZero() || acc.LastHealthyAt.IsZero() {
		t.Fatal("expected health timestamps to be updated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if _, ok := root["rate_limit_until"]; ok {
		t.Fatalf("expected saved file to clear rate_limit_until: %s", string(saved))
	}
	if _, ok := root["dead"]; ok {
		t.Fatalf("expected saved file to clear dead flag: %s", string(saved))
	}
	if root["health_status"] != "healthy" {
		t.Fatalf("saved health_status = %#v", root["health_status"])
	}
}

func TestFinalizeProxyResponsePersistsHealthyGeminiStateFromUnknown(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_unknown.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:           "gemini_unknown",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	}

	h.finalizeProxyResponse("req-test", &GeminiProvider{}, acc, "pool-user", http.StatusOK, true, false, "", 0, 0, nil)

	if acc.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthCheckedAt.IsZero() || acc.LastHealthyAt.IsZero() {
		t.Fatal("expected health timestamps to be populated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if root["health_status"] != "healthy" {
		t.Fatalf("saved health_status = %#v", root["health_status"])
	}
	if _, ok := root["health_checked_at"]; !ok {
		t.Fatalf("expected health_checked_at to be persisted: %s", string(saved))
	}
	if _, ok := root["last_healthy_at"]; !ok {
		t.Fatalf("expected last_healthy_at to be persisted: %s", string(saved))
	}
}

func TestFinalizeProxyResponsePersistsHealthyGeminiTimestampsWhenAlreadyHealthy(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_healthy_missing_timestamps.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token",
		"health_status":"healthy"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:           "gemini_healthy",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		HealthStatus: "healthy",
	}

	h.finalizeProxyResponse("req-test", &GeminiProvider{}, acc, "pool-user", http.StatusOK, true, false, "", 0, 0, nil)

	if acc.HealthCheckedAt.IsZero() || acc.LastHealthyAt.IsZero() {
		t.Fatal("expected health timestamps to be populated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if _, ok := root["health_checked_at"]; !ok {
		t.Fatalf("expected health_checked_at to be persisted: %s", string(saved))
	}
	if _, ok := root["last_healthy_at"]; !ok {
		t.Fatalf("expected last_healthy_at to be persisted: %s", string(saved))
	}
}

func TestFinalizeWebSocketSuccessStatePersistsHealthyGeminiState(t *testing.T) {
	accFile := filepath.Join(t.TempDir(), "gemini_ws_unknown.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"access-token",
		"refresh_token":"refresh-token"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{}
	acc := &Account{
		ID:           "gemini_ws_unknown",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
	}

	h.finalizeWebSocketSuccessState(acc, "", http.StatusSwitchingProtocols)

	if acc.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthCheckedAt.IsZero() || acc.LastHealthyAt.IsZero() {
		t.Fatal("expected health timestamps to be populated")
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if root["health_status"] != "healthy" {
		t.Fatalf("saved health_status = %#v", root["health_status"])
	}
	if _, ok := root["health_checked_at"]; !ok {
		t.Fatalf("expected health_checked_at to be persisted: %s", string(saved))
	}
	if _, ok := root["last_healthy_at"]; !ok {
		t.Fatalf("expected last_healthy_at to be persisted: %s", string(saved))
	}
}

func TestGeminiProviderRefreshTokenFallsBackToGCloudClient(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"seed-access",
		"refresh_token":"seed-refresh"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:           "gemini_refresh",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "seed-access",
		RefreshToken: "seed-refresh",
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch calls {
		case 1:
			if values.Get("client_id") != testGeminiOAuthCLIClientID {
				t.Fatalf("first client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusUnauthorized, `{"error":"unauthorized_client","error_description":"Unauthorized"}`), nil
		case 2:
			if values.Get("client_id") != testGeminiOAuthGCloudClientID {
				t.Fatalf("second client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		default:
			t.Fatalf("unexpected refresh call #%d", calls)
		}
		return nil, nil
	})

	if err := (&GeminiProvider{}).RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if acc.AccessToken != "fresh-access" {
		t.Fatalf("AccessToken=%q", acc.AccessToken)
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID=%q", acc.OAuthProfileID)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to be cleared, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(saved, &root); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if root["oauth_profile_id"] != "gcloud" {
		t.Fatalf("saved oauth_profile_id=%#v", root["oauth_profile_id"])
	}
	if _, ok := root["client_id"]; ok {
		t.Fatalf("expected saved client_id to be dropped: %s", string(saved))
	}
}

func TestGeminiProviderRefreshTokenFallsBackOn400InvalidGrant(t *testing.T) {
	testGeminiProviderRefreshTokenFallsBackOnRetryableBadRequest(t, "invalid_grant")
}

func TestGeminiProviderRefreshTokenFallsBackOn400InvalidClient(t *testing.T) {
	testGeminiProviderRefreshTokenFallsBackOnRetryableBadRequest(t, "invalid_client")
}

func testGeminiProviderRefreshTokenFallsBackOnRetryableBadRequest(t *testing.T, oauthError string) {
	t.Helper()
	setGeminiOAuthTestProfiles(t)

	accFile := filepath.Join(t.TempDir(), "gemini_refresh_bad_request.json")
	if err := os.WriteFile(accFile, []byte(`{
		"access_token":"seed-access",
		"refresh_token":"seed-refresh"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	acc := &Account{
		ID:           "gemini_refresh_bad_request",
		Type:         AccountTypeGemini,
		File:         accFile,
		AccessToken:  "seed-access",
		RefreshToken: "seed-refresh",
	}

	var calls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch calls {
		case 1:
			if values.Get("client_id") != testGeminiOAuthCLIClientID {
				t.Fatalf("first client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusBadRequest, `{"error":"`+oauthError+`","error_description":"retry another client"}`), nil
		case 2:
			if values.Get("client_id") != testGeminiOAuthGCloudClientID {
				t.Fatalf("second client_id=%q", values.Get("client_id"))
			}
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer","scope":"scope"}`), nil
		default:
			t.Fatalf("unexpected refresh call #%d", calls)
		}
		return nil, nil
	})

	if err := (&GeminiProvider{}).RefreshToken(context.Background(), acc, transport); err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if acc.AccessToken != "fresh-access" {
		t.Fatalf("AccessToken=%q", acc.AccessToken)
	}
	if acc.OAuthProfileID != "gcloud" {
		t.Fatalf("OAuthProfileID=%q", acc.OAuthProfileID)
	}
	if acc.OAuthClientID != "" || acc.OAuthClientSecret != "" {
		t.Fatalf("expected raw client credentials to be cleared, got %q / %q", acc.OAuthClientID, acc.OAuthClientSecret)
	}
}
