package main

import (
	"bytes"
	"compress/gzip"
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
	traceHeaderCh := make(chan string, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedCh <- int64(len(body))
		keyCh <- r.Header.Get("X-Api-Key")
		traceHeaderCh <- r.Header.Get(localTraceHeaderID)
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
	req.Header.Set(localTraceHeaderID, "trace-123")

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

	select {
	case got := <-traceHeaderCh:
		if got != "" {
			t.Fatalf("expected local trace header to be stripped, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream local trace header")
	}
}

func TestProxyPassthroughStreamedStripsLocalTraceHeaders(t *testing.T) {
	traceHeaderCh := make(chan string, 1)
	legacyTraceHeaderCh := make(chan string, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceHeaderCh <- r.Header.Get(localTraceHeaderID)
		legacyTraceHeaderCh <- r.Header.Get(legacyLocalTraceHeaderID)
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

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
			streamTimeout:        5 * time.Second,
			maxInMemoryBodyBytes: 8,
		},
		transport: http.DefaultTransport,
		pool:      newPoolState(nil, false),
		registry:  registry,
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
	}

	proxy := httptest.NewServer(h)
	defer proxy.Close()

	body := bytes.Repeat([]byte("a"), 64)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-proj-test-passthrough")
	req.Header.Set(localTraceHeaderID, "pool-trace")
	req.Header.Set(legacyLocalTraceHeaderID, "legacy-trace")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	select {
	case got := <-traceHeaderCh:
		if got != "" {
			t.Fatalf("expected pool trace header to be stripped, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream pool trace header")
	}

	select {
	case got := <-legacyTraceHeaderCh:
		if got != "" {
			t.Fatalf("expected legacy trace header to be stripped, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream legacy trace header")
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

func TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAndPreservesBody(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	expectedJSON := []byte(fmt.Sprintf(
		`{"error":{"message":"%s","code":"insufficient_quota"}}`,
		strings.Repeat("prefix-", 48)+"insufficient_quota",
	))
	var gzippedBody bytes.Buffer
	gz := gzip.NewWriter(&gzippedBody)
	if _, err := gz.Write(expectedJSON); err != nil {
		t.Fatalf("write gzip body: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip body: %v", err)
	}

	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"probe","status":"completed"}`))
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(gzippedBody.Bytes())
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
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "streamed-managed-api-gzip-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	gotCompressedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read compressed body: %v", err)
	}
	gotGzip, err := gzip.NewReader(bytes.NewReader(gotCompressedBody))
	if err != nil {
		t.Fatalf("new gzip reader: %v", err)
	}
	defer gotGzip.Close()

	gotBody, err := io.ReadAll(gotGzip)
	if err != nil {
		t.Fatalf("read decompressed body: %v", err)
	}
	if !bytes.Equal(gotBody, expectedJSON) {
		t.Fatalf("body = %q want %q", string(gotBody), string(expectedJSON))
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	accState := snapshotProxyTestAccount(acc)
	if !accState.Dead {
		t.Fatalf("expected managed api key to be marked dead on insufficient quota")
	}
	if accState.HealthStatus != "dead" {
		t.Fatalf("health status = %q", accState.HealthStatus)
	}
	if !strings.Contains(accState.HealthError, "insufficient_quota") {
		t.Fatalf("health error = %q", accState.HealthError)
	}
	recent := h.recent.snapshot()
	if len(recent) != 1 || !strings.Contains(recent[0], "managed api fallback 429 Too Many Requests") {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestProxyStreamedManagedAPICompressed429DoesNotWaitForFullLargeBody(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var detail strings.Builder
	detail.Grow(24000)
	for i := 0; detail.Len() < 24000; i++ {
		fmt.Fprintf(&detail, "%08x", (i+1)*2654435761)
	}
	fullJSON := []byte(fmt.Sprintf(`{"error":{"message":"insufficient_quota-%s","code":"insufficient_quota"}}`, detail.String()))
	var gzippedBody bytes.Buffer
	gz := gzip.NewWriter(&gzippedBody)
	if _, err := gz.Write(fullJSON); err != nil {
		t.Fatalf("write gzip body: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip body: %v", err)
	}
	if gzippedBody.Len() <= 2500 {
		t.Fatalf("gzipped body unexpectedly small: %d", gzippedBody.Len())
	}

	firstChunkLen := 2500
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"probe","status":"completed"}`))
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(gzippedBody.Bytes()[:firstChunkLen])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(1500 * time.Millisecond)
		_, _ = w.Write(gzippedBody.Bytes()[firstChunkLen:])
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
			streamTimeout:        0,
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
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "streamed-managed-api-gzip-slow-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")

	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	accState := snapshotProxyTestAccount(acc)
	if !accState.Dead {
		t.Fatalf("expected managed api key to be marked dead before full body arrives")
	}
}

func TestProxyStreamedManagedAPICompressed429ClassifiesQuotaAfterShortFirstReads(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	expectedJSON := []byte(fmt.Sprintf(
		`{"error":{"message":"%s","code":"insufficient_quota"}}`,
		strings.Repeat("prefix-", 32)+"insufficient_quota",
	))
	var gzippedBody bytes.Buffer
	gz := gzip.NewWriter(&gzippedBody)
	if _, err := gz.Write(expectedJSON); err != nil {
		t.Fatalf("write gzip body: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip body: %v", err)
	}

	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"probe","status":"completed"}`))
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(gzippedBody.Bytes()[:1])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(gzippedBody.Bytes()[1:8])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(gzippedBody.Bytes()[8:])
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
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "streamed-managed-api-gzip-short-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")

	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	gotCompressedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read compressed body: %v", err)
	}
	gotGzip, err := gzip.NewReader(bytes.NewReader(gotCompressedBody))
	if err != nil {
		t.Fatalf("new gzip reader: %v", err)
	}
	defer gotGzip.Close()

	gotBody, err := io.ReadAll(gotGzip)
	if err != nil {
		t.Fatalf("read decompressed body: %v", err)
	}
	if !bytes.Equal(gotBody, expectedJSON) {
		t.Fatalf("body = %q want %q", string(gotBody), string(expectedJSON))
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	accState := snapshotProxyTestAccount(acc)
	if !accState.Dead {
		t.Fatalf("expected managed api key to be marked dead after short gzip prefix reads")
	}
	if accState.HealthStatus != "dead" {
		t.Fatalf("health status = %q", accState.HealthStatus)
	}
	if !strings.Contains(accState.HealthError, "insufficient_quota") {
		t.Fatalf("health error = %q", accState.HealthError)
	}
}

func TestProxyStreamedManagedAPI5xxDoesNotWaitForFullLargeBody(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	largeMessage := strings.Repeat("x", 3000)
	fullBody := []byte(fmt.Sprintf(`{"error":{"message":"%s"}}`, largeMessage))
	firstChunkLen := 2100
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
		_, _ = w.Write(fullBody[:firstChunkLen])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(1500 * time.Millisecond)
		_, _ = w.Write(fullBody[firstChunkLen:])
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
			streamTimeout:        0,
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

	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}
