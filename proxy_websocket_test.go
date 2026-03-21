package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	codex := NewCodexProvider(baseURL, baseURL, baseURL)
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
	codex := NewCodexProvider(baseURL, baseURL, baseURL)
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
	codex := NewCodexProvider(baseURL, baseURL, baseURL)
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
	if !deadAcc.Dead {
		t.Fatalf("expected dead seat to be marked dead after account_deactivated response")
	}

	secondStatus := performRawWebSocketHandshake(t, proxy.URL, "/responses", map[string]string{
		"Sec-WebSocket-Protocol": "openai-insecure-api-key." + auth.Tokens.AccessToken,
		"session_id":             "thread-ws-dead-2",
	})
	if !strings.Contains(secondStatus, "101") {
		t.Fatalf("expected second response to use next live seat, got %q", secondStatus)
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
	codex := NewCodexProvider(baseURL, baseURL, baseURL)
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
