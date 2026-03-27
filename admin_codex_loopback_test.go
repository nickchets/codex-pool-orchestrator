package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHandleCodexLoopbackCallbackCompletesExchange(t *testing.T) {
	previousClient := codexOAuthHTTPClient
	codexOAuthHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{
				"access_token":"access-token",
				"refresh_token":"refresh-token",
				"id_token":"`+testCodexIDToken(t, "user-a", "workspace-a", "andy@example.com", "sub-a", time.Now().Add(time.Hour))+`",
				"token_type":"Bearer",
				"expires_in":3600,
				"scope":"openid profile email offline_access"
			}`), nil
		}),
	}
	t.Cleanup(func() {
		codexOAuthHTTPClient = previousClient
	})

	codexOAuthSessions.Lock()
	codexOAuthSessions.sessions = map[string]*CodexOAuthSession{
		"verifier-1": {
			Verifier:     "verifier-1",
			State:        "state-1",
			AutoComplete: true,
			CreatedAt:    time.Now(),
		},
	}
	codexOAuthSessions.Unlock()
	t.Cleanup(func() {
		codexOAuthSessions.Lock()
		codexOAuthSessions.sessions = make(map[string]*CodexOAuthSession)
		codexOAuthSessions.Unlock()
		stopCodexLoopbackCallbackServersIfIdle()
	})

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:  config{poolDir: poolDir},
		pool: newPoolState(nil, false),
	}

	req := httptest.NewRequest(http.MethodGet, "http://localhost:1455/auth/callback?code=test-code&state=state-1", nil)
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.handleCodexLoopbackCallback(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Codex account added",
		"andy",
		"Return to pool status",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
		}
	}

	if _, err := os.Stat(filepath.Join(poolDir, "codex", "andy.json")); err != nil {
		t.Fatalf("expected saved account file: %v", err)
	}

	codexOAuthSessions.RLock()
	_, ok := codexOAuthSessions.sessions["verifier-1"]
	codexOAuthSessions.RUnlock()
	if ok {
		t.Fatal("expected oauth session to be removed after successful callback")
	}
}

func TestHandleCodexLoopbackCallbackReportsRefreshedExistingSeat(t *testing.T) {
	previousClient := codexOAuthHTTPClient
	idToken := testCodexIDToken(t, "user-a", "workspace-a", "andy@example.com", "sub-a", time.Now().Add(time.Hour))
	codexOAuthHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{
				"access_token":"access-token-new",
				"refresh_token":"refresh-token-new",
				"id_token":"`+idToken+`",
				"token_type":"Bearer",
				"expires_in":3600,
				"scope":"openid profile email offline_access"
			}`), nil
		}),
	}
	t.Cleanup(func() {
		codexOAuthHTTPClient = previousClient
	})

	codexOAuthSessions.Lock()
	codexOAuthSessions.sessions = map[string]*CodexOAuthSession{
		"verifier-1": {
			Verifier:     "verifier-1",
			State:        "state-1",
			AutoComplete: true,
			CreatedAt:    time.Now(),
		},
	}
	codexOAuthSessions.Unlock()
	t.Cleanup(func() {
		codexOAuthSessions.Lock()
		codexOAuthSessions.sessions = make(map[string]*CodexOAuthSession)
		codexOAuthSessions.Unlock()
		stopCodexLoopbackCallbackServersIfIdle()
	})

	poolDir := t.TempDir()
	existingDir := filepath.Join(poolDir, "codex")
	if err := os.MkdirAll(existingDir, 0755); err != nil {
		t.Fatalf("mkdir existing dir: %v", err)
	}
	existingPath := filepath.Join(existingDir, "andy.json")
	existingJSON := `{
	  "tokens": {
	    "id_token": "` + idToken + `",
	    "access_token": "old-access-token",
	    "refresh_token": "old-refresh-token",
	    "account_id": "workspace-a"
	  }
	}`
	if err := os.WriteFile(existingPath, []byte(existingJSON), 0600); err != nil {
		t.Fatalf("write existing account: %v", err)
	}

	h := &proxyHandler{
		cfg:  config{poolDir: poolDir},
		pool: newPoolState(nil, false),
	}

	req := httptest.NewRequest(http.MethodGet, "http://localhost:1455/auth/callback?code=test-code&state=state-1", nil)
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.handleCodexLoopbackCallback(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Codex account refreshed",
		"andy",
		"may stay the same",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
		}
	}

	entries, err := os.ReadDir(existingDir)
	if err != nil {
		t.Fatalf("read existing dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "andy.json" {
		t.Fatalf("unexpected codex files: %+v", entries)
	}

	saved, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("read refreshed file: %v", err)
	}
	if !strings.Contains(string(saved), "access-token-new") {
		t.Fatalf("expected refreshed token payload, got: %s", string(saved))
	}
}

func TestHandleCodexLoopbackCallbackRejectsDuplicateCallback(t *testing.T) {
	previousClient := codexOAuthHTTPClient
	idToken := testCodexIDToken(t, "user-a", "workspace-a", "andy@example.com", "sub-a", time.Now().Add(time.Hour))

	firstExchangeStarted := make(chan struct{})
	releaseFirstExchange := make(chan struct{})
	var exchangeCalls int32
	codexOAuthHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if atomic.AddInt32(&exchangeCalls, 1) == 1 {
				close(firstExchangeStarted)
				<-releaseFirstExchange
				return jsonResponse(http.StatusOK, `{
					"access_token":"access-token",
					"refresh_token":"refresh-token",
					"id_token":"`+idToken+`",
					"token_type":"Bearer",
					"expires_in":3600,
					"scope":"openid profile email offline_access"
				}`), nil
			}
			t.Fatalf("unexpected duplicate token exchange request")
			return nil, nil
		}),
	}
	t.Cleanup(func() {
		codexOAuthHTTPClient = previousClient
	})

	codexOAuthSessions.Lock()
	codexOAuthSessions.sessions = map[string]*CodexOAuthSession{
		"verifier-1": {
			Verifier:     "verifier-1",
			State:        "state-1",
			AutoComplete: true,
			CreatedAt:    time.Now(),
		},
	}
	codexOAuthSessions.Unlock()
	t.Cleanup(func() {
		codexOAuthSessions.Lock()
		codexOAuthSessions.sessions = make(map[string]*CodexOAuthSession)
		codexOAuthSessions.Unlock()
		stopCodexLoopbackCallbackServersIfIdle()
	})

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:  config{poolDir: poolDir},
		pool: newPoolState(nil, false),
	}

	firstRR := httptest.NewRecorder()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		req := httptest.NewRequest(http.MethodGet, "http://localhost:1455/auth/callback?code=test-code&state=state-1", nil)
		req.RemoteAddr = "127.0.0.1:4242"
		h.handleCodexLoopbackCallback(firstRR, req)
	}()

	select {
	case <-firstExchangeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first callback never reached token exchange")
	}

	dupRR := httptest.NewRecorder()
	dupReq := httptest.NewRequest(http.MethodGet, "http://localhost:1455/auth/callback?code=test-code&state=state-1", nil)
	dupReq.RemoteAddr = "127.0.0.1:4242"
	h.handleCodexLoopbackCallback(dupRR, dupReq)

	if dupRR.Code != http.StatusBadRequest {
		t.Fatalf("duplicate status=%d body=%s", dupRR.Code, dupRR.Body.String())
	}
	if !strings.Contains(dupRR.Body.String(), "invalid or expired session") {
		t.Fatalf("duplicate body=%s", dupRR.Body.String())
	}

	close(releaseFirstExchange)

	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first callback did not complete")
	}

	if got := atomic.LoadInt32(&exchangeCalls); got != 1 {
		t.Fatalf("token exchange calls=%d", got)
	}

	if firstRR.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRR.Code, firstRR.Body.String())
	}
	if _, err := os.Stat(filepath.Join(poolDir, "codex", "andy.json")); err != nil {
		t.Fatalf("expected saved account file: %v", err)
	}
}

func TestCompleteCodexExchangeReleasesSessionAfterTokenExchangeFailure(t *testing.T) {
	previousClient := codexOAuthHTTPClient
	codexOAuthHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusUnauthorized, `{
				"error": {
					"message": "Could not validate your token. Please try signing in again.",
					"type": "invalid_request_error",
					"code": "token_expired"
				}
			}`), nil
		}),
	}
	t.Cleanup(func() {
		codexOAuthHTTPClient = previousClient
	})

	codexOAuthSessions.Lock()
	codexOAuthSessions.sessions = map[string]*CodexOAuthSession{
		"verifier-1": {
			Verifier:     "verifier-1",
			State:        "state-1",
			AutoComplete: true,
			CreatedAt:    time.Now(),
		},
	}
	codexOAuthSessions.Unlock()
	t.Cleanup(func() {
		codexOAuthSessions.Lock()
		codexOAuthSessions.sessions = make(map[string]*CodexOAuthSession)
		codexOAuthSessions.Unlock()
		stopCodexLoopbackCallbackServersIfIdle()
	})

	h := &proxyHandler{cfg: config{poolDir: t.TempDir()}}

	_, err := h.completeCodexExchange(context.Background(), codexExchangeRequest{
		State:       "state-1",
		CallbackURL: "http://localhost:1455/auth/callback?code=test-code&state=state-1",
	})
	if err == nil || !strings.Contains(err.Error(), "token exchange failed") {
		t.Fatalf("expected token exchange failure, got: %v", err)
	}

	codexOAuthSessions.RLock()
	session, ok := codexOAuthSessions.sessions["verifier-1"]
	codexOAuthSessions.RUnlock()
	if !ok || session == nil {
		t.Fatal("expected oauth session to remain available after token exchange failure")
	}
	if session.InFlight {
		t.Fatal("expected oauth session to be released after token exchange failure")
	}
}

func TestCompleteCodexExchangeLogsTrace(t *testing.T) {
	previousClient := codexOAuthHTTPClient
	codexOAuthHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{
				"access_token":"access-token",
				"refresh_token":"refresh-token",
				"id_token":"`+testCodexIDToken(t, "user-a", "workspace-a", "andy@example.com", "sub-a", time.Now().Add(time.Hour))+`",
				"token_type":"Bearer",
				"expires_in":3600,
				"scope":"openid profile email offline_access"
			}`), nil
		}),
	}
	t.Cleanup(func() {
		codexOAuthHTTPClient = previousClient
	})

	codexOAuthSessions.Lock()
	codexOAuthSessions.sessions = map[string]*CodexOAuthSession{
		"verifier-1": {
			Verifier:  "verifier-1",
			State:     "state-1",
			CreatedAt: time.Now(),
		},
	}
	codexOAuthSessions.Unlock()
	t.Cleanup(func() {
		codexOAuthSessions.Lock()
		codexOAuthSessions.sessions = make(map[string]*CodexOAuthSession)
		codexOAuthSessions.Unlock()
		stopCodexLoopbackCallbackServersIfIdle()
	})

	h := &proxyHandler{
		cfg:  config{poolDir: t.TempDir()},
		pool: newPoolState(nil, false),
	}

	logs := captureLogs(t, func() {
		result, err := h.completeCodexExchange(testTraceContext("req-codex-oauth"), codexExchangeRequest{
			State:       "state-1",
			CallbackURL: "http://localhost:1455/auth/callback?code=test-code&state=state-1",
			Lane:        "admin",
		})
		if err != nil {
			t.Fatalf("completeCodexExchange: %v", err)
		}
		if result.AccountID != "andy" {
			t.Fatalf("AccountID=%q", result.AccountID)
		}
	})

	if !strings.Contains(logs, "[req-codex-oauth] trace oauth_exchange") {
		t.Fatalf("missing oauth_exchange trace log: %s", logs)
	}
	if !strings.Contains(logs, `provider=codex`) || !strings.Contains(logs, `result=ok`) || !strings.Contains(logs, `lane="admin"`) {
		t.Fatalf("unexpected oauth_exchange trace log: %s", logs)
	}
}

func TestHandleCodexLoopbackCallbackRejectsNonLoopback(t *testing.T) {
	h := &proxyHandler{}

	req := httptest.NewRequest(http.MethodGet, "http://localhost:1455/auth/callback?code=test-code&state=state-1", nil)
	req.RemoteAddr = "198.51.100.10:4242"
	rr := httptest.NewRecorder()
	h.handleCodexLoopbackCallback(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
