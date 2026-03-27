package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testWebSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func TestIsWebSocketUpgradeRequest(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://localhost/ws", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	if !isWebSocketUpgradeRequest(req) {
		t.Fatalf("expected websocket upgrade request")
	}

	req.Header.Set("Upgrade", "h2c")
	if isWebSocketUpgradeRequest(req) {
		t.Fatalf("unexpected websocket upgrade detection for non-websocket upgrade")
	}
}

func TestExtractWebSocketProtocolBearerToken(t *testing.T) {
	token, ok := extractWebSocketProtocolBearerToken("openai-insecure-api-key.test-token, openai-beta.responses-v1")
	if !ok {
		t.Fatalf("expected token to be extracted")
	}
	if token != "test-token" {
		t.Fatalf("token = %q, want %q", token, "test-token")
	}
	if _, ok := extractWebSocketProtocolBearerToken("openai-beta.responses-v1"); ok {
		t.Fatalf("unexpected token extracted from non-auth subprotocol")
	}
}

func TestProxyWebSocketPoolRewritesAuthAndPinsSession(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	user := &PoolUser{
		ID:       "0bed5e30f3489bee45d17a781156cb96",
		Email:    "pool@example.com",
		PlanType: "pro",
	}
	auth, err := generateCodexAuth(getPoolJWTSecret(), user)
	if err != nil {
		t.Fatalf("generate auth: %v", err)
	}

	type upstreamReq struct {
		path       string
		auth       string
		accountID  string
		sessionID  string
		connection string
		upgrade    string
	}

	upstreamReqCh := make(chan upstreamReq, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamReqCh <- upstreamReq{
			path:       r.URL.Path,
			auth:       r.Header.Get("Authorization"),
			accountID:  r.Header.Get("ChatGPT-Account-ID"),
			sessionID:  r.Header.Get("session_id"),
			connection: r.Header.Get("Connection"),
			upgrade:    r.Header.Get("Upgrade"),
		}
		writeWebSocketSwitchingProtocolsResponse(w, r)
	}))
	defer upstream.Close()

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	registry := NewProviderRegistry(codex, claude, gemini)

	acc := &Account{
		Type:        AccountTypeCodex,
		ID:          "codex_pool_1",
		AccessToken: "pool-access-token",
		AccountID:   "acct_pool_1",
		PlanType:    "pro",
	}
	pool := newPoolState([]*Account{acc}, false)

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
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

	statusLine := performRawWebSocketHandshake(t, proxy.URL, "/responses", map[string]string{
		"Authorization": "Bearer " + auth.Tokens.AccessToken,
		"session_id":    "thread-ws-1",
	})
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("expected 101 response, got %q", statusLine)
	}

	select {
	case got := <-upstreamReqCh:
		if got.path != "/responses" {
			t.Fatalf("upstream path = %q, want %q", got.path, "/responses")
		}
		if got.auth != "Bearer pool-access-token" {
			t.Fatalf("upstream auth = %q, want pooled auth", got.auth)
		}
		if got.accountID != "acct_pool_1" {
			t.Fatalf("upstream ChatGPT-Account-ID = %q, want %q", got.accountID, "acct_pool_1")
		}
		if got.sessionID != "thread-ws-1" {
			t.Fatalf("upstream session_id = %q, want %q", got.sessionID, "thread-ws-1")
		}
		if !strings.EqualFold(got.upgrade, "websocket") {
			t.Fatalf("upstream Upgrade = %q, want websocket", got.upgrade)
		}
		if !headerContainsToken(http.Header{"Connection": []string{got.connection}}, "Connection", "Upgrade") {
			t.Fatalf("upstream Connection header missing Upgrade token: %q", got.connection)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream websocket request")
	}

	pool.mu.RLock()
	pinned := pool.convPin["thread-ws-1"]
	pool.mu.RUnlock()
	if pinned != "codex_pool_1" {
		t.Fatalf("session pin = %q, want %q", pinned, "codex_pool_1")
	}
}

func TestProxyWebSocketPoolAcceptsAuthFromSubprotocol(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	user := &PoolUser{
		ID:       "0bed5e30f3489bee45d17a781156cb96",
		Email:    "pool@example.com",
		PlanType: "pro",
	}
	auth, err := generateCodexAuth(getPoolJWTSecret(), user)
	if err != nil {
		t.Fatalf("generate auth: %v", err)
	}

	type upstreamReq struct {
		auth      string
		protocol  string
		accountID string
	}

	upstreamReqCh := make(chan upstreamReq, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamReqCh <- upstreamReq{
			auth:      r.Header.Get("Authorization"),
			protocol:  r.Header.Get("Sec-WebSocket-Protocol"),
			accountID: r.Header.Get("ChatGPT-Account-ID"),
		}
		writeWebSocketSwitchingProtocolsResponse(w, r)
	}))
	defer upstream.Close()

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	registry := NewProviderRegistry(codex, claude, gemini)

	acc := &Account{
		Type:        AccountTypeCodex,
		ID:          "codex_pool_1",
		AccessToken: "upstream-access-token",
		AccountID:   "acct_pool_1",
		PlanType:    "pro",
	}
	pool := newPoolState([]*Account{acc}, false)

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
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

	statusLine := performRawWebSocketHandshake(t, proxy.URL, "/responses", map[string]string{
		"Sec-WebSocket-Protocol": "openai-insecure-api-key." + auth.Tokens.AccessToken + ", openai-beta.responses-v1",
		"session_id":             "thread-ws-subprotocol-1",
	})
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("expected 101 response, got %q", statusLine)
	}

	select {
	case got := <-upstreamReqCh:
		if got.auth != "Bearer upstream-access-token" {
			t.Fatalf("upstream auth = %q, want pooled auth", got.auth)
		}
		if got.accountID != "acct_pool_1" {
			t.Fatalf("upstream ChatGPT-Account-ID = %q, want %q", got.accountID, "acct_pool_1")
		}
		if !strings.Contains(got.protocol, "openai-insecure-api-key.upstream-access-token") {
			t.Fatalf("upstream subprotocol = %q, want rewritten pooled auth token", got.protocol)
		}
		if strings.Contains(got.protocol, auth.Tokens.AccessToken) {
			t.Fatalf("upstream subprotocol leaked pool-user token: %q", got.protocol)
		}
		if !strings.Contains(got.protocol, "openai-beta.responses-v1") {
			t.Fatalf("upstream subprotocol = %q, want non-auth protocols preserved", got.protocol)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream websocket request")
	}
}

func TestProxyWebSocketLogsTraceLifecycle(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	user := &PoolUser{
		ID:       "0bed5e30f3489bee45d17a781156cb96",
		Email:    "pool@example.com",
		PlanType: "pro",
	}
	auth, err := generateCodexAuth(getPoolJWTSecret(), user)
	if err != nil {
		t.Fatalf("generate auth: %v", err)
	}

	upstreamReqCh := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamReqCh <- struct{}{}
		writeWebSocketSwitchingProtocolsResponse(w, r)
	}))
	defer upstream.Close()

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	registry := NewProviderRegistry(codex, claude, gemini)

	acc := &Account{
		Type:        AccountTypeCodex,
		ID:          "codex_pool_trace",
		AccessToken: "pool-access-token",
		AccountID:   "acct_pool_trace",
		PlanType:    "pro",
	}
	pool := newPoolState([]*Account{acc}, false)

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
			maxInMemoryBodyBytes: 1024,
			traceRequests:        true,
		},
		transport: http.DefaultTransport,
		pool:      pool,
		registry:  registry,
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
	}

	proxy := httptest.NewServer(h)
	defer proxy.Close()

	logs := captureLogs(t, func() {
		statusLine := performRawWebSocketHandshake(t, proxy.URL, "/responses", map[string]string{
			"Authorization": "Bearer " + auth.Tokens.AccessToken,
			"session_id":    "thread-ws-trace-1",
		})
		if !strings.Contains(statusLine, "101") {
			t.Fatalf("expected 101 response, got %q", statusLine)
		}

		select {
		case <-upstreamReqCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for upstream websocket request")
		}

		time.Sleep(20 * time.Millisecond)
	})

	if !strings.Contains(logs, "trace route mode=websocket") {
		t.Fatalf("missing websocket route trace log: %s", logs)
	}
	if !strings.Contains(logs, "trace response status=101") {
		t.Fatalf("missing websocket response trace log: %s", logs)
	}
	if !strings.Contains(logs, "trace finish mode=websocket") {
		t.Fatalf("missing websocket finish trace log: %s", logs)
	}
}

func TestProxyWebSocketMarksDeactivatedCodexAccountDeadAndFallsThroughNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	user := &PoolUser{
		ID:       "0bed5e30f3489bee45d17a781156cb96",
		Email:    "pool@example.com",
		PlanType: "pro",
	}
	auth, err := generateCodexAuth(getPoolJWTSecret(), user)
	if err != nil {
		t.Fatalf("generate auth: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer dead-seat-token":
			w.Header().Set("X-Openai-Ide-Error-Code", "account_deactivated")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"account_deactivated"}}`))
		case "Bearer live-seat-token":
			writeWebSocketSwitchingProtocolsResponse(w, r)
		default:
			t.Fatalf("unexpected upstream auth: %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	registry := NewProviderRegistry(codex, claude, gemini)

	deadAcc := &Account{
		Type:        AccountTypeCodex,
		ID:          "dead_seat",
		AccessToken: "dead-seat-token",
		AccountID:   "acct_dead",
		PlanType:    "pro",
	}
	liveAcc := &Account{
		Type:        AccountTypeCodex,
		ID:          "live_seat",
		AccessToken: "live-seat-token",
		AccountID:   "acct_live",
		PlanType:    "pro",
	}
	pool := newPoolState([]*Account{deadAcc, liveAcc}, false)

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
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

	firstStatus := performRawWebSocketHandshake(t, proxy.URL, "/responses", map[string]string{
		"Sec-WebSocket-Protocol": "openai-insecure-api-key." + auth.Tokens.AccessToken,
		"session_id":             "thread-ws-dead-1",
	})
	if !strings.Contains(firstStatus, "401") {
		t.Fatalf("expected first response to fail with 401, got %q", firstStatus)
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatalf("expected dead seat to be marked dead after account_deactivated response")
	}
	pool.mu.RLock()
	_, pinnedDeadSession := pool.convPin["thread-ws-dead-1"]
	pool.mu.RUnlock()
	if pinnedDeadSession {
		t.Fatalf("expected failed handshake session to stay unpinned")
	}

	secondStatus := performRawWebSocketHandshake(t, proxy.URL, "/responses", map[string]string{
		"Sec-WebSocket-Protocol": "openai-insecure-api-key." + auth.Tokens.AccessToken,
		"session_id":             "thread-ws-dead-2",
	})
	if !strings.Contains(secondStatus, "101") {
		t.Fatalf("expected second response to use next live seat, got %q", secondStatus)
	}
}

func TestProxyWebSocketManagedAPI5xxPreservesFullErrorBodyAndRecordsFallback(t *testing.T) {
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

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
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

	status, gotBody := performRawWebSocketHTTPResponse(t, proxy.URL, "/responses", map[string]string{
		"Authorization": "Bearer " + generateClaudePoolToken(getPoolJWTSecret(), "thread-ws-managed-502"),
		"session_id":    "thread-ws-managed-502",
	})
	if !strings.Contains(status, "502") {
		t.Fatalf("expected 502 response, got %q", status)
	}
	if !bytes.Equal(gotBody, expectedBody) {
		t.Fatalf("body len = %d want %d", len(gotBody), len(expectedBody))
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	accState := snapshotProxyTestAccount(acc)
	if accState.Dead {
		t.Fatalf("expected managed api key to remain non-dead on transient 5xx")
	}
	if accState.HealthStatus != "error" {
		t.Fatalf("health status = %q", accState.HealthStatus)
	}
	if !strings.Contains(accState.HealthError, "Bad Gateway") {
		t.Fatalf("health error = %q", accState.HealthError)
	}
	if !accState.LastUsed.IsZero() {
		t.Fatalf("expected failed websocket handshake to keep LastUsed zero, got %v", accState.LastUsed)
	}
	recent := h.recent.snapshot()
	if len(recent) != 1 || !strings.Contains(recent[0], "managed api fallback 502 Bad Gateway") {
		t.Fatalf("recent = %+v", recent)
	}
	pool.mu.RLock()
	_, pinned := pool.convPin["thread-ws-managed-502"]
	pool.mu.RUnlock()
	if pinned {
		t.Fatalf("expected failed managed-api handshake session to stay unpinned")
	}
}

func TestProxyWebSocketManagedAPICompressed429ClassifiesQuotaAndPreservesBody(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	expectedJSON := []byte(fmt.Sprintf(
		`{"error":{"message":"%s","code":"insufficient_quota"}}`,
		strings.Repeat("prefix-", 24)+"insufficient_quota",
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

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
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

	status, gotCompressedBody := performRawWebSocketHTTPResponse(t, proxy.URL, "/responses", map[string]string{
		"Authorization":   "Bearer " + generateClaudePoolToken(getPoolJWTSecret(), "thread-ws-managed-gzip-429"),
		"session_id":      "thread-ws-managed-gzip-429",
		"Accept-Encoding": "gzip",
	})
	if !strings.Contains(status, "429") {
		t.Fatalf("expected 429 response, got %q", status)
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
	pool.mu.RLock()
	_, pinned := pool.convPin["thread-ws-managed-gzip-429"]
	pool.mu.RUnlock()
	if pinned {
		t.Fatalf("expected failed managed-api handshake session to stay unpinned")
	}
}

func TestProxyWebSocketPassthroughPreservesAuthorization(t *testing.T) {
	upstreamAuth := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth <- r.Header.Get("Authorization")
		writeWebSocketSwitchingProtocolsResponse(w, r)
	}))
	defer upstream.Close()

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	registry := NewProviderRegistry(codex, claude, gemini)

	h := &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
			maxInMemoryBodyBytes: 1024,
		},
		transport: http.DefaultTransport,
		pool:      newPoolState(nil, false),
		registry:  registry,
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
	}

	proxy := httptest.NewServer(h)
	defer proxy.Close()

	statusLine := performRawWebSocketHandshake(t, proxy.URL, "/responses", map[string]string{
		"Authorization": "Bearer sk-proj-test-passthrough",
	})
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("expected 101 response, got %q", statusLine)
	}

	select {
	case got := <-upstreamAuth:
		if got != "Bearer sk-proj-test-passthrough" {
			t.Fatalf("upstream auth = %q, want passthrough auth", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream websocket auth header")
	}
}

func performRawWebSocketHandshake(
	t *testing.T,
	serverURL string,
	path string,
	headers map[string]string,
) string {
	t.Helper()

	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial websocket endpoint: %v", err)
	}
	defer conn.Close()

	key := "dGhlIHNhbXBsZSBub25jZQ=="
	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n",
		path,
		u.Host,
		key,
	)
	for k, v := range headers {
		request += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	request += "\r\n"

	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write websocket handshake: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read websocket status line: %v", err)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read websocket response header: %v", readErr)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	return strings.TrimSpace(statusLine)
}

func performRawWebSocketHTTPResponse(
	t *testing.T,
	serverURL string,
	path string,
	headers map[string]string,
) (string, []byte) {
	t.Helper()

	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial websocket endpoint: %v", err)
	}
	defer conn.Close()

	key := "dGhlIHNhbXBsZSBub25jZQ=="
	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n",
		path,
		u.Host,
		key,
	)
	for k, v := range headers {
		request += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	request += "\r\n"

	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}

	reader := bufio.NewReader(conn)
	req, err := http.NewRequest(http.MethodGet, "http://"+u.Host+path, nil)
	if err != nil {
		t.Fatalf("new request for response parsing: %v", err)
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatalf("read websocket HTTP response: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read websocket HTTP response body: %v", err)
	}
	return resp.Status, body
}

func writeWebSocketSwitchingProtocolsResponse(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	sum := sha1.Sum([]byte(key + testWebSocketGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])

	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.WriteString("Upgrade: websocket\r\n")
	_, _ = rw.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n")
	_, _ = rw.WriteString("\r\n")
	_ = rw.Flush()
}
