package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProxyStreamedRequestClaude(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	receivedCh := make(chan int64, 1)
	keyCh := make(chan string, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedCh <- int64(len(body))
		keyCh <- r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	baseURL, _ := url.Parse(upstream.URL)
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	registry := NewProviderRegistry(codex, claude, gemini)

	acc := &Account{Type: AccountTypeClaude, ID: "claude_test", AccessToken: "sk-ant-api-test"}
	pool := newPoolState([]*Account{acc}, false)

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
			streamTimeout:        5 * time.Second,
			maxInMemoryBodyBytes: 1024,
		},
		transport: http.DefaultTransport,
		pool:      pool,
		registry:  registry,
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
	}

	proxy := httptest.NewServer(h)
	defer proxy.Close()

	body := bytes.Repeat([]byte("a"), 2048)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "streamed-claude-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	select {
	case got := <-receivedCh:
		if got != int64(len(body)) {
			t.Fatalf("upstream received %d bytes, want %d", got, len(body))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream body size")
	}

	select {
	case got := <-keyCh:
		if got == "" {
			t.Fatalf("expected X-Api-Key to be set for Claude API key")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream header")
	}
}

func TestProxyStreamedManagedAPI5xxPreservesFullErrorBody(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	largeMessage := strings.Repeat("x", 3000)
	expectedBody := []byte(fmt.Sprintf(`{"error":{"message":"%s"}}`, largeMessage))
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"probe","status":"completed"}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(expectedBody)
	}))
	defer upstream.Close()

	baseURL, _ := url.Parse(upstream.URL)
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	registry := NewProviderRegistry(codex, claude, gemini)

	tmp := t.TempDir()
	accFile := filepath.Join(tmp, "openai_api_deadbeef.json")
	if err := os.WriteFile(accFile, []byte(`{"OPENAI_API_KEY":"sk-proj-test","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	acc := &Account{
		ID:          "openai_api_deadbeef",
		Type:        AccountTypeCodex,
		File:        accFile,
		AccessToken: "sk-proj-test",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}
	pool := newPoolState([]*Account{acc}, false)

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
			streamTimeout:        5 * time.Second,
			maxInMemoryBodyBytes: 10,
		},
		transport: http.DefaultTransport,
		pool:      pool,
		registry:  registry,
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
	}

	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"gpt-4.1-mini","input":"` + strings.Repeat("a", 128) + `"}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "streamed-managed-api-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(gotBody, expectedBody) {
		t.Fatalf("body len = %d want %d", len(gotBody), len(expectedBody))
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}
