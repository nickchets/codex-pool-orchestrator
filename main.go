package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

type config struct {
	listenAddr    string
	responsesBase *url.URL
	whamBase      *url.URL
	refreshBase   *url.URL
	openAIAPIBase *url.URL
	geminiBase    *url.URL // Gemini CloudCode endpoint (for OAuth/Code Assist mode)
	geminiAPIBase *url.URL // Gemini API endpoint (for API key mode)
	claudeBase    *url.URL // Claude API endpoint
	kimiBase      *url.URL // Kimi API endpoint
	minimaxBase   *url.URL // MiniMax API endpoint
	poolDir       string

	disableRefresh  bool
	refreshProxyURL string // HTTP proxy URL for refresh operations

	debug                      bool
	logBodies                  bool
	bodyLogLimit               int64
	traceRequests              bool
	tracePackets               bool
	tracePayloads              bool
	tracePayloadLimit          int
	traceStallGap              time.Duration
	forceCodexRequiredPlan     string
	gitLabCodexDiscoveryModels []string
	maxInMemoryBodyBytes       int64
	flushInterval              time.Duration
	usageRefresh               time.Duration
	maxAttempts                int
	storePath                  string
	retentionDays              int
	friendCode                 string
	adminToken                 string
	requestTimeout             time.Duration // Timeout for non-streaming requests (0 = no timeout)
	streamTimeout              time.Duration // Timeout for streaming/SSE requests (0 = no timeout)
	streamIdleTimeout          time.Duration // Kill SSE streams idle for this long (0 = no idle timeout)
	claudePingTailTimeout      time.Duration // Cut GitLab Claude SSE tails that degrade into ping-only keepalives after useful output
	tierThreshold              float64       // Secondary usage % at which we stop preferring a tier (default 0.15)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustParse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		log.Fatalf("invalid URL %q: %v", raw, err)
	}
	return u
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// Global config file reference for pool users config
var globalConfigFile *ConfigFile

func buildConfig() config {
	// Load config.toml if it exists
	configFile, err := loadConfigFile("config.toml")
	if err != nil {
		log.Printf("warning: failed to load config.toml: %v", err)
	}
	globalConfigFile = configFile

	var fileCfg ConfigFile
	if configFile != nil {
		fileCfg = *configFile
	}

	cfg := config{}
	cfg.listenAddr = getConfigString("PROXY_LISTEN_ADDR", fileCfg.ListenAddr, "127.0.0.1:8989")
	cfg.responsesBase = mustParse(getenv("UPSTREAM_RESPONSES_BASE", "https://chatgpt.com/backend-api/codex"))
	cfg.whamBase = mustParse(getenv("UPSTREAM_WHAM_BASE", "https://chatgpt.com/backend-api"))
	cfg.refreshBase = mustParse(getenv("UPSTREAM_REFRESH_BASE", "https://auth.openai.com"))
	cfg.openAIAPIBase = mustParse(getenv("UPSTREAM_OPENAI_API_BASE", "https://api.openai.com"))
	cfg.geminiBase = mustParse(getenv("UPSTREAM_GEMINI_BASE", "https://cloudcode-pa.googleapis.com"))
	cfg.geminiAPIBase = mustParse(getenv("UPSTREAM_GEMINI_API_BASE", "https://generativelanguage.googleapis.com"))
	cfg.claudeBase = mustParse(getenv("UPSTREAM_CLAUDE_BASE", "https://api.anthropic.com"))
	cfg.kimiBase = mustParse(getenv("UPSTREAM_KIMI_BASE", "https://api.kimi.com/coding"))
	cfg.minimaxBase = mustParse(getenv("UPSTREAM_MINIMAX_BASE", "https://api.minimax.io/anthropic"))
	cfg.poolDir = getConfigString("POOL_DIR", fileCfg.PoolDir, "pool")

	// Refresh often fails for some auth.json fixtures; allow opting out.
	cfg.disableRefresh = getConfigBool("PROXY_DISABLE_REFRESH", fileCfg.DisableRefresh, false)
	cfg.refreshProxyURL = getConfigString("REFRESH_PROXY_URL", fileCfg.RefreshProxyURL, "")

	cfg.debug = getConfigBool("PROXY_DEBUG", fileCfg.Debug, false)
	cfg.forceCodexRequiredPlan = normalizeForceCodexRequiredPlan(getConfigString("PROXY_FORCE_CODEX_REQUIRED_PLAN", fileCfg.ForceCodexRequiredPlan, ""))
	cfg.gitLabCodexDiscoveryModels = parseCSVEnvList(getConfigString("PROXY_GITLAB_CODEX_DISCOVERY_MODELS", fileCfg.GitLabCodexDiscoveryModels, ""))
	cfg.logBodies = getenv("PROXY_LOG_BODIES", "0") == "1"
	cfg.bodyLogLimit = 16 * 1024 // 16 KiB
	if v := getenv("PROXY_BODY_LOG_LIMIT", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n > 0 {
			cfg.bodyLogLimit = n
		}
	}
	cfg.traceRequests = getenv("PROXY_TRACE_REQUESTS", "0") == "1"
	cfg.tracePackets = getenv("PROXY_TRACE_PACKETS", "0") == "1"
	cfg.tracePayloads = getenv("PROXY_TRACE_PAYLOADS", "0") == "1"
	cfg.tracePayloadLimit = 512
	if v := getenv("PROXY_TRACE_PAYLOAD_LIMIT", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n > 0 {
			cfg.tracePayloadLimit = int(n)
		}
	}
	if v := getenv("PROXY_TRACE_STALL_GAP_MS", ""); v != "" {
		if ms, err := parseInt64(v); err == nil && ms > 0 {
			cfg.traceStallGap = time.Duration(ms) * time.Millisecond
		}
	}
	cfg.maxInMemoryBodyBytes = 16 * 1024 * 1024 // 16 MiB
	if v := getenv("PROXY_MAX_INMEM_BODY_BYTES", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n >= 0 {
			cfg.maxInMemoryBodyBytes = n
		}
	}
	cfg.flushInterval = 200 * time.Millisecond
	if v := getenv("PROXY_FLUSH_INTERVAL_MS", ""); v != "" {
		if ms, err := parseInt64(v); err == nil && ms > 0 {
			cfg.flushInterval = time.Duration(ms) * time.Millisecond
		}
	}
	cfg.usageRefresh = 5 * time.Minute
	if v := getenv("PROXY_USAGE_REFRESH_SECONDS", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n > 0 {
			cfg.usageRefresh = time.Duration(n) * time.Second
		}
	}
	cfg.maxAttempts = getConfigInt("PROXY_MAX_ATTEMPTS", fileCfg.MaxAttempts, 3)
	cfg.storePath = getConfigString("PROXY_DB_PATH", fileCfg.DBPath, "./data/proxy.db")
	cfg.friendCode = getConfigString("FRIEND_CODE", fileCfg.FriendCode, "")
	cfg.adminToken = getConfigString("ADMIN_TOKEN", fileCfg.AdminToken, "")
	cfg.retentionDays = 30
	if v := getenv("PROXY_USAGE_RETENTION_DAYS", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n > 0 {
			cfg.retentionDays = int(n)
		}
	}

	// Request timeouts: default 5 min for regular requests, 0 (unlimited) for streaming.
	// Set to 0 to disable timeout entirely.
	cfg.requestTimeout = 5 * time.Minute
	if v := getenv("PROXY_REQUEST_TIMEOUT_SECONDS", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n >= 0 {
			cfg.requestTimeout = time.Duration(n) * time.Second
		}
	}
	cfg.streamTimeout = 0 // No timeout for streaming - Claude Code sessions can run indefinitely
	if v := getenv("PROXY_STREAM_TIMEOUT_SECONDS", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n >= 0 {
			cfg.streamTimeout = time.Duration(n) * time.Second
		}
	}
	cfg.streamIdleTimeout = 10 * time.Minute // Kill SSE streams that receive no data for this long
	if v := getenv("STREAM_IDLE_TIMEOUT_SECONDS", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n >= 0 {
			cfg.streamIdleTimeout = time.Duration(n) * time.Second
		}
	}
	cfg.claudePingTailTimeout = 18 * time.Second
	if v := getenv("CLAUDE_PING_TAIL_TIMEOUT_SECONDS", ""); v != "" {
		if n, err := parseInt64(v); err == nil && n >= 0 {
			cfg.claudePingTailTimeout = time.Duration(n) * time.Second
		}
	}

	// Tier threshold: secondary usage % at which we stop preferring a tier (default 15%)
	cfg.tierThreshold = getConfigFloat64("TIER_THRESHOLD", fileCfg.TierThreshold, 0.15)

	flag.StringVar(&cfg.listenAddr, "listen", cfg.listenAddr, "listen address")
	flag.Parse()
	return cfg
}

func main() {
	cfg := buildConfig()

	// Create provider registry
	codexProvider := NewCodexProvider(cfg.responsesBase, cfg.whamBase, cfg.refreshBase, cfg.openAIAPIBase)
	claudeProvider := NewClaudeProvider(cfg.claudeBase)
	geminiProvider := NewGeminiProvider(cfg.geminiBase, cfg.geminiAPIBase)
	kimiProvider := NewKimiProvider(cfg.kimiBase)
	minimaxProvider := NewMinimaxProvider(cfg.minimaxBase)
	registry := NewProviderRegistry(codexProvider, claudeProvider, geminiProvider, kimiProvider, minimaxProvider)

	log.Printf("loading pool from %s", cfg.poolDir)
	accounts, err := loadPool(cfg.poolDir, registry)
	if err != nil {
		log.Fatalf("load pool: %v", err)
	}
	pool := newPoolState(accounts, cfg.debug)
	pool.tierThreshold = cfg.tierThreshold
	codexCount := pool.countByType(AccountTypeCodex)
	claudeCount := pool.countByType(AccountTypeClaude)
	geminiCount := pool.countByType(AccountTypeGemini)
	kimiCount := pool.countByType(AccountTypeKimi)
	minimaxCount := pool.countByType(AccountTypeMinimax)
	if pool.count() == 0 {
		log.Printf("warning: loaded 0 accounts from %s", cfg.poolDir)
	}

	store, err := newUsageStore(cfg.storePath, cfg.retentionDays)
	if err != nil {
		log.Fatalf("open usage store: %v", err)
	}
	defer store.Close()

	if restoredTotals, restoredSnapshots, bridged, restoredRuntime := restorePersistedUsageState(pool.accounts, store); restoredTotals > 0 || restoredSnapshots > 0 || bridged > 0 || restoredRuntime > 0 {
		log.Printf(
			"restored usage state from disk: totals=%d snapshots=%d bridged_from_totals=%d runtime=%d",
			restoredTotals,
			restoredSnapshots,
			bridged,
			restoredRuntime,
		)
	}

	standardTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second, // TCP keepalives to prevent NAT/router timeouts
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0, // Disable - we handle timeouts per-request based on streaming
		ExpectContinueTimeout: 5 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
	}
	_ = http2.ConfigureTransport(standardTransport)

	// Use hybrid transport: rustls fingerprint for Cloudflare-protected hosts, standard for others
	// NOTE: rustls fingerprint disabled - Cloudflare started blocking the HTTP/1.1-only fingerprint
	// with 403 challenge pages. Using standard Go transport with HTTP/2 for all hosts.
	var transport http.RoundTripper = standardTransport

	// Create refresh transport - may use a proxy for token refresh operations
	var refreshTransport http.RoundTripper = transport
	if cfg.refreshProxyURL != "" {
		proxyURL, err := url.Parse(cfg.refreshProxyURL)
		if err != nil {
			log.Fatalf("invalid refresh proxy URL %q: %v", cfg.refreshProxyURL, err)
		}
		refreshProxyTransport := &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 5 * time.Second,
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   10,
		}
		_ = http2.ConfigureTransport(refreshProxyTransport)
		refreshTransport = refreshProxyTransport
		log.Printf("refresh operations will use proxy: %s", proxyURL.Host)
	}

	// Initialize pool users store if configured
	var poolUsers *PoolUserStore
	// Pool users require a JWT secret. Admin token or friend code provides access control.
	if (cfg.adminToken != "" || cfg.friendCode != "") && getPoolJWTSecret() != "" {
		poolUsersPath := getPoolUsersPath()
		var err error
		poolUsers, err = newPoolUserStore(poolUsersPath)
		if err != nil {
			log.Printf("warning: failed to load pool users: %v", err)
		} else {
			log.Printf("pool users enabled (%d users)", len(poolUsers.List()))
		}
	}

	h := &proxyHandler{
		cfg:              cfg,
		transport:        transport,
		refreshTransport: refreshTransport,
		pool:             pool,
		poolUsers:        poolUsers,
		registry:         registry,
		store:            store,
		metrics:          newMetrics(),
		recent:           newRecentErrors(50),
		startTime:        time.Now(),
	}
	h.startUsagePoller()
	h.startDeadAccountCleanupPoller()
	h.startStaleAntigravityGeminiTruthPoller()
	go func() {
		h.refreshStaleAntigravityGeminiTruthOnStartup()
		h.hydrateMissingAntigravityGeminiQuotaOnStartup()
	}()

	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           h,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       5 * time.Minute, // Keep connections alive for reuse
	}

	// Configure HTTP/2 with settings optimized for long-running streams.
	http2Srv := &http2.Server{
		MaxConcurrentStreams:         250,
		IdleTimeout:                  5 * time.Minute,
		MaxUploadBufferPerConnection: 1 << 20, // 1MB
		MaxUploadBufferPerStream:     1 << 20, // 1MB
		MaxReadFrameSize:             1 << 20, // 1MB
	}
	if err := http2.ConfigureServer(srv, http2Srv); err != nil {
		log.Printf("warning: failed to configure HTTP/2 server: %v", err)
	}

	if cfg.adminToken != "" {
		log.Printf("admin token configured (len=%d)", len(cfg.adminToken))
	} else {
		log.Printf("WARNING: no admin token configured")
	}
	log.Printf("codex-pool proxy listening on %s (codex=%d, claude=%d, gemini=%d, kimi=%d, minimax=%d, request_timeout=%v, stream_timeout=%v, stream_idle_timeout=%v, claude_ping_tail_timeout=%v, trace_requests=%v, trace_packets=%v, trace_payloads=%v, trace_stall_gap=%v)",
		cfg.listenAddr, codexCount, claudeCount, geminiCount, kimiCount, minimaxCount, cfg.requestTimeout, cfg.streamTimeout, cfg.streamIdleTimeout, cfg.claudePingTailTimeout, cfg.traceRequests, cfg.tracePackets, cfg.tracePayloads, cfg.traceStallGap)
	if cfg.forceCodexRequiredPlan != "" {
		log.Printf("codex forced required plan enabled: %s", cfg.forceCodexRequiredPlan)
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

type proxyHandler struct {
	cfg               config
	transport         http.RoundTripper
	refreshTransport  http.RoundTripper // Separate transport for refresh ops (may use proxy)
	pool              *poolState
	poolUsers         *PoolUserStore
	registry          *ProviderRegistry
	store             *usageStore
	metrics           *metrics
	recent            *recentErrors
	inflight          int64
	startTime         time.Time
	codexModels       codexModelsCache
	gitlabCodexModels codexModelsCache

	// Rate limiting for token refresh operations
	refreshMu       sync.Mutex
	lastRefreshTime time.Time
	refreshCallsMu  sync.Mutex
	refreshCalls    map[string]*refreshCall
}

type refreshCall struct {
	done chan struct{}
	err  error
}

// Note: ServeHTTP is now in router.go
// Note: Handler functions (serveHealth, serveAccounts, etc.) are now in handlers.go

func (h *proxyHandler) pickUpstream(path string, headers http.Header) (Provider, *url.URL) {
	// Check headers first - Anthropic requests have X-Api-Key or anthropic-* headers
	if headers.Get("X-Api-Key") != "" {
		// X-Api-Key is used by Anthropic Claude API
		provider := h.registry.ForType(AccountTypeClaude)
		return provider, provider.UpstreamURL(path)
	}
	// Check for any anthropic-* headers (version, beta, etc.)
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), "anthropic-") {
			provider := h.registry.ForType(AccountTypeClaude)
			return provider, provider.UpstreamURL(path)
		}
	}

	// Fall back to path-based routing
	provider := h.registry.ForPath(path)
	if provider == nil {
		// Fallback to Codex provider
		provider = h.registry.ForType(AccountTypeCodex)
	}
	return provider, provider.UpstreamURL(path)
}

func mapResponsesPath(in string) string {
	switch {
	case strings.HasPrefix(in, "/v1/responses/compact"), strings.HasPrefix(in, "/responses/compact"):
		return "/responses/compact"
	case strings.HasPrefix(in, "/v1/responses"), strings.HasPrefix(in, "/responses"):
		return "/responses"
	default:
		return "/responses"
	}
}

const openAIInsecureAPIKeyProtocolPrefix = "openai-insecure-api-key."

func extractWebSocketProtocolBearerToken(headerValue string) (string, bool) {
	for _, part := range strings.Split(headerValue, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, openAIInsecureAPIKeyProtocolPrefix) {
			token := strings.TrimPrefix(part, openAIInsecureAPIKeyProtocolPrefix)
			if token != "" {
				return token, true
			}
		}
	}
	return "", false
}

func requestAuthHeader(r *http.Request) string {
	if r == nil {
		return ""
	}
	if authHeader := strings.TrimSpace(r.Header.Get("Authorization")); authHeader != "" {
		return authHeader
	}
	if !isWebSocketUpgradeRequest(r) {
		return ""
	}
	if token, ok := extractWebSocketProtocolBearerToken(r.Header.Get("Sec-WebSocket-Protocol")); ok {
		return "Bearer " + token
	}
	return ""
}

func rewriteWebSocketProtocolBearerToken(headers http.Header, token string) {
	if token == "" {
		return
	}
	values := headers.Values("Sec-WebSocket-Protocol")
	if len(values) == 0 {
		return
	}
	rewritten := make([]string, 0, len(values))
	changed := false
	for _, value := range values {
		parts := strings.Split(value, ",")
		for i, part := range parts {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, openAIInsecureAPIKeyProtocolPrefix) {
				part = openAIInsecureAPIKeyProtocolPrefix + token
				changed = true
			}
			parts[i] = part
		}
		rewritten = append(rewritten, strings.Join(parts, ", "))
	}
	if !changed {
		return
	}
	headers.Del("Sec-WebSocket-Protocol")
	for _, value := range rewritten {
		headers.Add("Sec-WebSocket-Protocol", value)
	}
}

func isPermanentCodexAuthFailure(resp *http.Response, body []byte) bool {
	if resp == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Openai-Ide-Error-Code")), "account_deactivated") {
		return true
	}
	if len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "account_deactivated") || strings.Contains(text, "deactivated_workspace")
}

func markAccountDead(reqID string, acc *Account, reason string) {
	now := time.Now().UTC()
	acc.mu.Lock()
	markAccountDeadWithReasonLocked(acc, now, 100.0, reason)
	acc.mu.Unlock()
	log.Printf("[%s] marking account %s as dead: %s", reqID, acc.ID, reason)
	if err := saveAccount(acc); err != nil {
		log.Printf("[%s] warning: failed to save dead account %s: %v", reqID, acc.ID, err)
	}
}

func accountIsDead(acc *Account) bool {
	if acc == nil {
		return false
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	return acc.Dead
}

func isManagedCodexAPIKeyRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusPaymentRequired ||
		isRetryableStatus(statusCode)
}

func (h *proxyHandler) applyUpstreamAuthFailureDisposition(reqID string, acc *Account, resp *http.Response, refreshFailed bool, inspectedBody []byte) {
	if acc == nil || resp == nil {
		return
	}
	if isGitLabCodexAccount(acc) {
		if accountIsDead(acc) {
			return
		}
		disposition := classifyManagedGitLabCodexError(managedGitLabCodexErrorSourceGatewayRequest, resp.StatusCode, resp.Header, inspectedBody)
		applyManagedGitLabCodexDisposition(acc, disposition, resp.Header, time.Now())
		if err := saveAccount(acc); err != nil {
			log.Printf("[%s] warning: failed to save gitlab codex account %s: %v", reqID, acc.ID, err)
		}
		if disposition.MarkDead {
			log.Printf("[%s] gitlab codex account %s marked dead: %s", reqID, acc.ID, disposition.Reason)
		}
		return
	}
	if isGitLabClaudeAccount(acc) {
		if accountIsDead(acc) {
			return
		}
		disposition := classifyManagedGitLabClaudeError(managedGitLabClaudeErrorSourceGatewayRequest, resp.StatusCode, resp.Header, inspectedBody)
		applyManagedGitLabClaudeDisposition(acc, disposition, resp.Header, time.Now())
		if err := saveAccount(acc); err != nil {
			log.Printf("[%s] warning: failed to save gitlab claude account %s: %v", reqID, acc.ID, err)
		}
		if disposition.MarkDead {
			log.Printf("[%s] gitlab claude account %s marked dead: %s", reqID, acc.ID, disposition.Reason)
		}
		return
	}
	if acc.Type == AccountTypeCodex && isPermanentCodexAuthFailure(resp, inspectedBody) {
		markAccountDead(reqID, acc, "codex upstream account_deactivated")
		return
	}
	if refreshFailed && acc.Type != AccountTypeCodex {
		now := time.Now().UTC()
		acc.mu.Lock()
		markAccountDeadWithReasonLocked(acc, now, 1.0, "refresh failed after upstream auth failure")
		acc.mu.Unlock()
		if err := saveAccount(acc); err != nil {
			log.Printf("[%s] warning: failed to save dead account %s: %v", reqID, acc.ID, err)
		}
		return
	}
	acc.mu.Lock()
	acc.Penalty += 10.0
	acc.mu.Unlock()
}

func geminiRateLimitUntilFromResponse(resp *http.Response, inspectedBody []byte, now time.Time) (time.Time, string, bool) {
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return time.Time{}, "", false
	}
	bodyText := strings.TrimSpace(string(inspectedBody))
	httpErr := &geminiCodeAssistHTTPError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Message:    bodyText,
	}
	if until, reason, precise, ok := geminiCodeAssistCooldownInfo(httpErr, now); ok {
		if wait, hasRetryAfter := parseRetryAfter(resp.Header); hasRetryAfter {
			headerUntil := now.Add(wait).UTC()
			if !precise && headerUntil.After(until) {
				until = headerUntil
			}
		}
		return until.UTC(), sanitizeStatusMessage(firstNonEmpty(reason, bodyText, resp.Status)), true
	}
	if wait, ok := parseRetryAfter(resp.Header); ok {
		return now.Add(wait).UTC(), sanitizeStatusMessage(firstNonEmpty(bodyText, resp.Status)), true
	}
	return now.Add(managedGeminiRateLimitWait).UTC(), sanitizeStatusMessage(firstNonEmpty(bodyText, resp.Status)), true
}

func (h *proxyHandler) applyGeminiRateLimitDisposition(acc *Account, resp *http.Response, inspectedBody []byte, requestedModel, requestPath string) bool {
	if acc == nil || acc.Type != AccountTypeGemini || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	now := time.Now().UTC()
	until, reason, ok := geminiRateLimitUntilFromResponse(resp, inspectedBody, now)
	if !ok || until.IsZero() {
		return false
	}
	acc.mu.Lock()
	modelKey := noteGeminiModelRateLimitedLocked(acc, requestedModel, requestPath, until)
	if modelKey != "" {
		acc.RateLimitUntil = time.Time{}
	} else if acc.RateLimitUntil.Before(until) {
		acc.RateLimitUntil = until
	}
	noteGeminiOperationalCooldownLocked(acc, now, "proxy", firstNonEmpty(reason, "rate limited"))
	acc.Penalty += 1.0
	acc.mu.Unlock()
	return true
}

func (h *proxyHandler) applyPreCopyUpstreamStatusDisposition(reqID string, acc *Account, resp *http.Response, refreshFailed bool, inspectedBody []byte, requestedModel, requestPath string) error {
	if acc == nil || resp == nil {
		return nil
	}
	if isGitLabCodexAccount(acc) && (resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		if accountIsDead(acc) {
			return nil
		}
		disposition := classifyManagedGitLabCodexError(managedGitLabCodexErrorSourceGatewayRequest, resp.StatusCode, resp.Header, inspectedBody)
		applyManagedGitLabCodexDisposition(acc, disposition, resp.Header, time.Now())
		if err := saveAccount(acc); err != nil {
			log.Printf("[%s] warning: failed to save gitlab codex account %s: %v", reqID, acc.ID, err)
		}
		if disposition.MarkDead {
			log.Printf("[%s] gitlab codex account %s unavailable: %s", reqID, acc.ID, disposition.Reason)
		}
		return nil
	}
	if isGitLabClaudeAccount(acc) && (resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		if accountIsDead(acc) {
			return nil
		}
		now := time.Now()
		disposition := classifyManagedGitLabClaudeError(managedGitLabClaudeErrorSourceGatewayRequest, resp.StatusCode, resp.Header, inspectedBody)
		applyManagedGitLabClaudeDisposition(acc, disposition, resp.Header, now)
		sharedPersisted := false
		if disposition.RateLimit && disposition.SharedOrgTPM {
			sharedPersisted = h.propagateManagedGitLabClaudeSharedTPMCooldown(reqID, acc, disposition, resp.Header, requestedModel, now)
		}
		if !sharedPersisted {
			if err := saveAccount(acc); err != nil {
				log.Printf("[%s] warning: failed to save gitlab claude account %s: %v", reqID, acc.ID, err)
			}
		}
		if disposition.MarkDead {
			log.Printf("[%s] gitlab claude account %s unavailable: %s", reqID, acc.ID, disposition.Reason)
		}
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests && !isManagedCodexAPIKeyAccount(acc) {
		if h.applyGeminiRateLimitDisposition(acc, resp, inspectedBody, requestedModel, requestPath) {
			return nil
		}
		h.applyRateLimit(acc, resp.Header, defaultRateLimitBackoff)
		acc.mu.Lock()
		acc.Penalty += 1.0
		acc.mu.Unlock()
		return nil
	}
	if isManagedCodexAPIKeyAccount(acc) && isManagedCodexAPIKeyRetryableStatus(resp.StatusCode) {
		err := h.handleManagedCodexAPIKeyFailure(reqID, acc, resp, inspectedBody)
		if err == nil {
			err = fmt.Errorf("managed api fallback %s", resp.Status)
		}
		if h.recent != nil {
			h.recent.add(err.Error())
		}
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		h.applyUpstreamAuthFailureDisposition(reqID, acc, resp, refreshFailed, inspectedBody)
		return nil
	}
	if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
		acc.mu.Lock()
		acc.Penalty += 0.3
		acc.mu.Unlock()
	}
	return nil
}

type preCopyUpstreamStatusHandlingResult struct {
	NeedStatusBody bool
	InspectedBody  []byte
}

func (h *proxyHandler) applyPreCopyUpstreamStatusHandling(reqID string, acc *Account, resp *http.Response, refreshFailed bool, requestedModel, requestPath string, skipSwitchingProtocols bool) preCopyUpstreamStatusHandlingResult {
	result := preCopyUpstreamStatusHandlingResult{}
	if resp == nil {
		return result
	}

	result.NeedStatusBody = (isManagedCodexAPIKeyAccount(acc) && isManagedCodexAPIKeyRetryableStatus(resp.StatusCode)) ||
		(acc != nil && acc.Type == AccountTypeGemini && resp.StatusCode == http.StatusTooManyRequests) ||
		(isGitLabClaudeAccount(acc) && resp.StatusCode == http.StatusTooManyRequests) ||
		resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden
	if result.NeedStatusBody {
		inspection := inspectResponseBodyForClassification(resp, preCopyStatusInspectionLimit)
		result.InspectedBody = inspection.Inspected
		if len(inspection.RawPrefix) > 0 {
			resp.Body = replayResponseBody(inspection.RawPrefix, resp.Body)
		}
	}
	if !(skipSwitchingProtocols && resp.StatusCode == http.StatusSwitchingProtocols) {
		_ = h.applyPreCopyUpstreamStatusDisposition(reqID, acc, resp, refreshFailed, result.InspectedBody, requestedModel, requestPath)
	}
	return result
}

func extractConversationIDFromHeaders(headers http.Header) string {
	for _, key := range []string{
		"session_id",
		"Session-Id",
		"conversation_id",
		"prompt_cache_key",
		"x-codex-conversation-id",
	} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func removeConflictingProxyHeaders(h http.Header) {
	// Remove ALL Cloudflare headers (Cf-*) — our own Cloudflare adds these,
	// and they confuse upstream Cloudflare (e.g. chatgpt.com) into blocking us.
	for key := range h {
		if strings.HasPrefix(strings.ToLower(key), "cf-") {
			h.Del(key)
		}
	}
	h.Del("Cdn-Loop")
	// Remove proxy/forwarding headers added by Caddy or Cloudflare
	h.Del("X-Forwarded-For")
	h.Del("X-Forwarded-Proto")
	h.Del("X-Forwarded-Host")
	h.Del("X-Real-Ip")
	h.Del("Via")
	h.Del("True-Client-Ip")
}

func normalizePath(basePath, incoming string) string {
	if basePath == "" || basePath == "/" {
		return incoming
	}
	if strings.HasPrefix(incoming, basePath) {
		trimmed := strings.TrimPrefix(incoming, basePath)
		if !strings.HasPrefix(trimmed, "/") {
			trimmed = "/" + trimmed
		}
		return trimmed
	}
	return incoming
}

func singleJoin(basePath, reqPath string) string {
	if basePath == "" || basePath == "/" {
		return reqPath
	}
	if strings.HasSuffix(basePath, "/") && strings.HasPrefix(reqPath, "/") {
		return basePath + strings.TrimPrefix(reqPath, "/")
	}
	if !strings.HasSuffix(basePath, "/") && !strings.HasPrefix(reqPath, "/") {
		return basePath + "/" + reqPath
	}
	return basePath + reqPath
}

func extractConversationIDFromJSON(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(blob, &obj); err != nil {
		return ""
	}
	// Check top-level keys - prompt_cache_key first for Codex session affinity
	for _, key := range []string{"prompt_cache_key", "conversation_id", "conversation", "session_id"} {
		if v, ok := obj[key].(string); ok && v != "" {
			return v
		}
	}
	// Some variants may tuck metadata under a sub-object.
	// Claude Code sends metadata.user_id like "user_..._session_UUID"
	for _, containerKey := range []string{"metadata", "meta"} {
		if sub, ok := obj[containerKey].(map[string]any); ok {
			for _, key := range []string{"conversation_id", "conversation", "prompt_cache_key", "session_id", "user_id"} {
				if v, ok := sub[key].(string); ok && v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func extractRequestedModelFromJSON(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(blob, &obj); err != nil {
		return ""
	}
	if v, ok := obj["model"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func modelRequiresCodexPro(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "gpt-5.3-codex-spark")
}

// modelRouteOverride checks if the requested model should be routed to an external
// provider (Kimi, MiniMax, etc.) instead of the path-detected provider.
// Returns (provider, baseURL, rewrittenBody) or (nil, nil, nil) if no override.
func (h *proxyHandler) modelRouteOverride(reqPath, model string, body []byte) modelRouteDecision {
	if strings.HasPrefix(strings.TrimSpace(model), "gemini-") {
		p := h.registry.ForType(AccountTypeGemini)
		if p != nil {
			if upstreamPath, rewrittenBody, responseAdapter, _, err := maybeBuildAnthropicMessagesGeminiRequest(reqPath, model, body); err == nil && upstreamPath != "" {
				return modelRouteDecision{
					Provider:        p,
					TargetBase:      p.UpstreamURL(upstreamPath),
					UpstreamPath:    upstreamPath,
					RewrittenBody:   rewrittenBody,
					ResponseAdapter: responseAdapter,
				}
			}
			if upstreamPath, rewrittenBody, responseAdapter, _, err := maybeBuildOpenAIChatCompletionsGeminiRequest(reqPath, model, body); err == nil && upstreamPath != "" {
				return modelRouteDecision{
					Provider:        p,
					TargetBase:      p.UpstreamURL(upstreamPath),
					UpstreamPath:    upstreamPath,
					RewrittenBody:   rewrittenBody,
					ResponseAdapter: responseAdapter,
				}
			}
		}
	}
	if isKimiModel(model) {
		p := h.registry.ForType(AccountTypeKimi)
		if p == nil {
			return modelRouteDecision{}
		}
		return modelRouteDecision{Provider: p, TargetBase: p.UpstreamURL("")}
	}
	if isMinimaxModel(model) {
		p := h.registry.ForType(AccountTypeMinimax)
		if p == nil {
			return modelRouteDecision{}
		}
		// Rewrite the model name to the canonical upstream name
		canonical := minimaxCanonicalModel(model)
		rewritten := rewriteModelInBody(body, canonical)
		return modelRouteDecision{Provider: p, TargetBase: p.UpstreamURL(""), RewrittenBody: rewritten}
	}
	return modelRouteDecision{}
}

// rewriteModelInBody replaces the "model" field in a JSON request body.
func rewriteModelInBody(body []byte, newModel string) []byte {
	if len(body) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil
	}
	if _, ok := obj["model"]; !ok {
		return nil
	}
	obj["model"] = newModel
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	return rewritten
}

func extractConversationIDFromSSE(sample []byte) string {
	// Best-effort: scan lines for JSON fragments and grab conversation_id/conversation.
	for _, line := range bytes.Split(sample, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		}
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		if id := extractConversationIDFromJSON(line); id != "" {
			return id
		}
	}
	return ""
}

func bodyForInspection(r *http.Request, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	enc := ""
	if r != nil {
		enc = strings.ToLower(r.Header.Get("Content-Encoding"))
	}
	looksGzip := len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
	if strings.Contains(enc, "gzip") || looksGzip {
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return body
		}
		defer gr.Close()
		decoded, err := io.ReadAll(io.LimitReader(gr, 512*1024))
		if err != nil || len(decoded) == 0 {
			return body
		}
		return decoded
	}
	return body
}

const preCopyStatusInspectionLimit int64 = 2048
const gzipPreCopyStatusInspectionLimit int64 = 2048
const gzipPreCopyStatusRawReadLimit int64 = 16384
const gzipPreCopyStatusReadChunkSize int64 = 4096

func responseBodyLooksGzip(resp *http.Response, prefix []byte) bool {
	if resp != nil && strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		return true
	}
	return len(prefix) >= 2 && prefix[0] == 0x1f && prefix[1] == 0x8b
}

func replayResponseBody(prefix []byte, body io.ReadCloser) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{
		Reader: io.MultiReader(bytes.NewReader(prefix), body),
		Closer: body,
	}
}

func preCopyStatusPrefixCouldStillBeGzip(prefix []byte) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(prefix) == 1 {
		return prefix[0] == 0x1f
	}
	return prefix[0] == 0x1f && prefix[1] == 0x8b
}

func containsPreCopyStatusInspectionSignal(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	signals := [...]string{
		"invalid_api_key",
		"incorrect api key",
		"incorrect_api_key",
		"organization_deactivated",
		"account_deactivated",
		"insufficient_quota",
		"billing_hard_limit_reached",
		"credits exhausted",
		"credit balance",
		"quota exceeded",
		"rate_limit",
		"rate limited",
		"too many requests",
	}
	for _, signal := range signals {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

func decodeGzipPreCopyStatusPrefix(rawPrefix []byte, limit int64) ([]byte, bool, bool) {
	if len(rawPrefix) == 0 {
		return nil, true, false
	}

	gz, err := gzip.NewReader(bytes.NewReader(rawPrefix))
	if err != nil {
		if preCopyStatusPrefixCouldStillBeGzip(rawPrefix) {
			return nil, true, false
		}
		return rawPrefix, false, false
	}
	defer gz.Close()

	inspected, readErr := io.ReadAll(io.LimitReader(gz, limit))
	if int64(len(inspected)) >= limit || containsPreCopyStatusInspectionSignal(inspected) {
		return inspected, false, true
	}
	switch {
	case readErr == nil || errors.Is(readErr, io.EOF):
		return inspected, false, true
	case errors.Is(readErr, io.ErrUnexpectedEOF):
		return inspected, true, true
	default:
		return inspected, false, true
	}
}

func inspectGzipResponseBodyPrefix(body io.ReadCloser, limit int64) ([]byte, []byte) {
	if body == nil || limit <= 0 {
		return nil, nil
	}
	if limit > gzipPreCopyStatusInspectionLimit {
		limit = gzipPreCopyStatusInspectionLimit
	}

	rawLimit := gzipPreCopyStatusRawReadLimit
	if rawLimit < limit {
		rawLimit = limit
	}
	chunkSize := gzipPreCopyStatusReadChunkSize
	if chunkSize > rawLimit {
		chunkSize = rawLimit
	}
	rawPrefix := make([]byte, 0, rawLimit)
	scratch := make([]byte, chunkSize)

	// Keep reading only while the accumulated prefix is still insufficient to
	// decode enough semantic body for disposition/logging decisions.
	for int64(len(rawPrefix)) < rawLimit {
		remaining := int(rawLimit) - len(rawPrefix)
		readSize := len(scratch)
		if remaining < readSize {
			readSize = remaining
		}
		n, readErr := body.Read(scratch[:readSize])
		if n > 0 {
			rawPrefix = append(rawPrefix, scratch[:n]...)
		}
		if len(rawPrefix) == 0 {
			if readErr != nil || n == 0 {
				return nil, nil
			}
			continue
		}

		inspected, needMore, decoded := decodeGzipPreCopyStatusPrefix(rawPrefix, limit)
		if !needMore || readErr != nil || int64(len(rawPrefix)) >= rawLimit || n == 0 {
			if decoded {
				return inspected, rawPrefix
			}
			return rawPrefix, rawPrefix
		}
	}

	inspected, _, decoded := decodeGzipPreCopyStatusPrefix(rawPrefix, limit)
	if decoded {
		return inspected, rawPrefix
	}
	return rawPrefix, rawPrefix
}

type preCopyInspection struct {
	Inspected []byte
	RawPrefix []byte
}

// Streamed and websocket pre-copy paths must preserve the original wire body for
// the client, so they split semantic inspection from raw replay explicitly.
func inspectResponseBodyForClassification(resp *http.Response, limit int64) preCopyInspection {
	if resp == nil || resp.Body == nil || limit <= 0 {
		return preCopyInspection{}
	}

	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		inspected, rawPrefix := inspectGzipResponseBodyPrefix(resp.Body, limit)
		if len(inspected) > int(limit) {
			inspected = inspected[:limit]
		}
		return preCopyInspection{Inspected: inspected, RawPrefix: rawPrefix}
	}

	prefix, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	if responseBodyLooksGzip(resp, prefix) {
		resp.Body = replayResponseBody(prefix, resp.Body)
		inspected, rawPrefix := inspectGzipResponseBodyPrefix(resp.Body, limit)
		if len(inspected) > int(limit) {
			inspected = inspected[:limit]
		}
		return preCopyInspection{Inspected: inspected, RawPrefix: rawPrefix}
	}

	inspected := bodyForInspection(nil, prefix)
	if len(inspected) > int(limit) {
		inspected = inspected[:limit]
	}
	return preCopyInspection{Inspected: inspected, RawPrefix: prefix}
}

// Buffered retry paths do not replay upstream error bodies to the client: they
// either retry another account or synthesize a local error response, so a
// limited fully buffered semantic snapshot is enough here.
func inspectBufferedRetryBody(body io.ReadCloser, limit int64) []byte {
	if body == nil || limit <= 0 {
		return nil
	}
	defer body.Close()

	inspected, _ := io.ReadAll(io.LimitReader(body, limit))
	return bodyForInspection(nil, inspected)
}

type bufferedRetryInspection struct {
	Body []byte
	Text string
}

func needsBufferedRetryInspection(acc *Account, statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusPaymentRequired ||
		(isManagedCodexAPIKeyAccount(acc) && statusCode == http.StatusTooManyRequests) ||
		isRetryableStatus(statusCode)
}

func inspectBufferedRetryStatus(resp *http.Response, limit int64) bufferedRetryInspection {
	body := inspectBufferedRetryBody(resp.Body, limit)
	return bufferedRetryInspection{
		Body: body,
		Text: string(body),
	}
}

func formatBufferedRetryStatusError(resp *http.Response, bodyText string) error {
	if resp == nil {
		return nil
	}
	message := fmt.Sprintf("upstream %s", resp.Status)
	if strings.TrimSpace(bodyText) != "" {
		message = fmt.Sprintf("upstream %s: %s", resp.Status, bodyText)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter, _ := parseRetryAfter(resp.Header)
		return &rateLimitResponseError{message: message, retryAfter: retryAfter}
	}
	return errors.New(message)
}

type bufferedAttemptSuccess struct {
	acc       *Account
	resp      *http.Response
	sampleBuf *bytes.Buffer
}

type copiedProxyResponseDeliveryOptions struct {
	requestPath           string
	initialConversationID string
	debugLabel            string
	flushAfterWrite       bool
	logResponseDebug      bool
	closeBodyAfterCopy    bool
	captureResponseSample bool
	existingSample        *bytes.Buffer
}

type rateLimitResponseError struct {
	message    string
	retryAfter time.Duration
}

func (e *rateLimitResponseError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func asRateLimitResponseError(err error) (*rateLimitResponseError, bool) {
	var target *rateLimitResponseError
	if !errors.As(err, &target) || target == nil {
		return nil, false
	}
	return target, true
}

func retryAfterHeaderValue(wait time.Duration) string {
	if wait <= 0 {
		return ""
	}
	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

func writeHTTPErrorWithOptionalRateLimit(w http.ResponseWriter, err error, fallbackStatus int) {
	if err == nil {
		http.Error(w, "request failed", fallbackStatus)
		return
	}
	status := fallbackStatus
	if rateLimitErr, ok := asRateLimitResponseError(err); ok {
		status = http.StatusTooManyRequests
		if retryAfter := retryAfterHeaderValue(rateLimitErr.retryAfter); retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
	}
	http.Error(w, err.Error(), status)
}

func writeBufferedUnavailableAccountError(w http.ResponseWriter, lastErr error, accountType AccountType, requiredPlan, requestedModel string) {
	if lastErr != nil {
		writeHTTPErrorWithOptionalRateLimit(w, lastErr, http.StatusServiceUnavailable)
		return
	}
	if requiredPlan != "" {
		http.Error(w, fmt.Sprintf("no live %s %s accounts for model %s", accountType, requiredPlan, requestedModel), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, fmt.Sprintf("no live %s accounts", accountType), http.StatusServiceUnavailable)
}

func writeBufferedAttemptFailure(w http.ResponseWriter, lastStatus int, lastErr error) {
	status := http.StatusBadGateway
	if lastStatus == http.StatusTooManyRequests {
		status = http.StatusTooManyRequests
	}
	if lastErr == nil {
		lastErr = errors.New("all attempts failed")
	}
	writeHTTPErrorWithOptionalRateLimit(w, lastErr, status)
}

func bufferedRetryTraceReason(statusCode int, bodyText string) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "rate_limited"
	case statusCode == http.StatusPaymentRequired && strings.Contains(bodyText, "deactivated_workspace"):
		return "account_deactivated"
	case statusCode == http.StatusPaymentRequired && strings.Contains(bodyText, "subscription"):
		return "subscription_required"
	case statusCode == http.StatusPaymentRequired:
		return "payment_required"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return "auth_retry"
	default:
		return fmt.Sprintf("status_%d", statusCode)
	}
}

func (h *proxyHandler) applyBufferedRetryDisposition(reqID string, trace *requestTrace, acc *Account, resp *http.Response, refreshFailed bool, requestedModel, requestPath string, attempt, attempts int) (bool, error) {
	var retryInspection bufferedRetryInspection
	if needsBufferedRetryInspection(acc, resp.StatusCode) {
		// Buffered retries never replay upstream bodies to the client, so one
		// bounded semantic snapshot is enough for all status-specific branches.
		retryInspection = inspectBufferedRetryStatus(resp, preCopyStatusInspectionLimit)
	}

	if resp.StatusCode == http.StatusTooManyRequests && !isManagedCodexAPIKeyAccount(acc) {
		_ = h.applyPreCopyUpstreamStatusDisposition(reqID, acc, resp, refreshFailed, retryInspection.Body, requestedModel, requestPath)
		err := formatBufferedRetryStatusError(resp, retryInspection.Text)
		h.recent.add(err.Error())
		if h.cfg.debug {
			log.Printf("[%s] attempt %d/%d account=%s retryable status=%d refreshFailed=%v", reqID, attempt, attempts, acc.ID, resp.StatusCode, refreshFailed)
		}
		trace.noteRetryDisposition(acc.Type, acc, attempt, attempts, resp.StatusCode, true, bufferedRetryTraceReason(resp.StatusCode, retryInspection.Text), refreshFailed)
		return true, err
	}

	if isManagedCodexAPIKeyAccount(acc) && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired) {
		err := h.applyPreCopyUpstreamStatusDisposition(reqID, acc, resp, refreshFailed, retryInspection.Body, requestedModel, requestPath)
		trace.noteRetryDisposition(acc.Type, acc, attempt, attempts, resp.StatusCode, true, bufferedRetryTraceReason(resp.StatusCode, retryInspection.Text), refreshFailed)
		return true, err
	}

	// Handle 402 Payment Required - often means deactivated workspace/subscription.
	if resp.StatusCode == http.StatusPaymentRequired {
		if isGitLabClaudeAccount(acc) || isGitLabCodexAccount(acc) {
			_ = h.applyPreCopyUpstreamStatusDisposition(reqID, acc, resp, refreshFailed, retryInspection.Body, requestedModel, requestPath)
			err := formatBufferedRetryStatusError(resp, retryInspection.Text)
			h.recent.add(err.Error())
			trace.noteRetryDisposition(acc.Type, acc, attempt, attempts, resp.StatusCode, true, bufferedRetryTraceReason(resp.StatusCode, retryInspection.Text), refreshFailed)
			return true, err
		}
		// Check for deactivated_workspace or similar permanent failures.
		if strings.Contains(retryInspection.Text, "deactivated_workspace") || strings.Contains(retryInspection.Text, "subscription") {
			now := time.Now().UTC()
			acc.mu.Lock()
			markAccountDeadWithReasonLocked(acc, now, 100.0, retryInspection.Text)
			acc.mu.Unlock()
			log.Printf("[%s] marking account %s as DEAD: %s", reqID, acc.ID, retryInspection.Text)
			if err := saveAccount(acc); err != nil {
				log.Printf("[%s] warning: failed to save dead account %s: %v", reqID, acc.ID, err)
			}
			err := fmt.Errorf("account deactivated: %s", retryInspection.Text)
			h.recent.add(err.Error())
			trace.noteRetryDisposition(acc.Type, acc, attempt, attempts, resp.StatusCode, true, bufferedRetryTraceReason(resp.StatusCode, retryInspection.Text), refreshFailed)
			return true, err
		}
	}

	if isRetryableStatus(resp.StatusCode) {
		if isManagedCodexAPIKeyAccount(acc) {
			err := h.applyPreCopyUpstreamStatusDisposition(reqID, acc, resp, refreshFailed, retryInspection.Body, requestedModel, requestPath)
			if h.cfg.debug {
				log.Printf("[%s] attempt %d/%d account=%s retryable status=%d refreshFailed=%v", reqID, attempt, attempts, acc.ID, resp.StatusCode, refreshFailed)
			}
			trace.noteRetryDisposition(acc.Type, acc, attempt, attempts, resp.StatusCode, true, bufferedRetryTraceReason(resp.StatusCode, retryInspection.Text), refreshFailed)
			return true, err
		}

		_ = h.applyPreCopyUpstreamStatusDisposition(reqID, acc, resp, refreshFailed, retryInspection.Body, requestedModel, requestPath)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if refreshFailed && acc.Type != AccountTypeCodex {
				log.Printf("[%s] account %s DEAD: 401/403 refresh failed, body=%s", reqID, acc.ID, retryInspection.Text)
			} else if !isPermanentCodexAuthFailure(resp, retryInspection.Body) {
				// Always log 401/403 with error body and response headers for debugging.
				var respHdrs []string
				for k, v := range resp.Header {
					respHdrs = append(respHdrs, fmt.Sprintf("%s=%s", k, v[0]))
				}
				acc.mu.Lock()
				penalty := acc.Penalty
				acc.mu.Unlock()
				log.Printf("[%s] account %s got %d, penalty now %.0f, body=%s, resp_headers=%v", reqID, acc.ID, resp.StatusCode, penalty, retryInspection.Text, respHdrs)
			}
		}
		err := formatBufferedRetryStatusError(resp, retryInspection.Text)
		h.recent.add(err.Error())
		if h.cfg.debug {
			log.Printf("[%s] attempt %d/%d account=%s retryable status=%d refreshFailed=%v", reqID, attempt, attempts, acc.ID, resp.StatusCode, refreshFailed)
		}
		trace.noteRetryDisposition(acc.Type, acc, attempt, attempts, resp.StatusCode, true, bufferedRetryTraceReason(resp.StatusCode, retryInspection.Text), refreshFailed)
		return true, err
	}

	return false, nil
}

func (h *proxyHandler) runBufferedAttemptContour(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	bodyBytes []byte,
	reqID string,
	routePlan RoutePlan,
) (*bufferedAttemptSuccess, bool) {
	trace := requestTraceFromContext(ctx)
	attempts := h.cfg.maxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	// Try at least all accounts of this type, up to configured max.
	if n := h.pool.countByType(routePlan.AccountType); n > attempts {
		attempts = n
	}
	// But don't exceed total pool size.
	if n := h.pool.count(); n > 0 && attempts > n {
		attempts = n
	}

	exclude := map[string]bool{}
	var lastErr error
	var lastStatus int

	for attempt := 1; attempt <= attempts; attempt++ {
		acc, err := h.candidateSupportingPath(routePlan.Shape.ConversationID, exclude, routePlan.AccountType, routePlan.RequiredPlan, routePlan.Provider, routePlan.UpstreamPath, routePlan.Shape.RequestedModel, routePlan.DebugGeminiSeatID)
		if err != nil {
			lastErr = err
			continue
		}
		if acc == nil {
			writeBufferedUnavailableAccountError(w, lastErr, routePlan.AccountType, routePlan.RequiredPlan, routePlan.Shape.RequestedModel)
			return nil, true
		}

		atomic.AddInt64(&acc.Inflight, 1)
		atomic.AddInt64(&h.inflight, 1)
		if trace != nil {
			trace.noteRoute(routePlan, acc, routePlan.TargetBase, "buffered", attempt, attempts)
		}

		resp, sampleBuf, refreshFailed, err := h.tryOnce(ctx, r, bodyBytes, routePlan, acc, reqID)

		atomic.AddInt64(&acc.Inflight, -1)
		atomic.AddInt64(&h.inflight, -1)

		if err != nil {
			lastErr = err
			h.recent.add(err.Error())
			if h.cfg.debug {
				log.Printf("[%s] attempt %d/%d account=%s failed: %v", reqID, attempt, attempts, acc.ID, err)
			}
			continue
		}
		lastStatus = resp.StatusCode

		if retry, retryErr := h.applyBufferedRetryDisposition(reqID, trace, acc, resp, refreshFailed, routePlan.Shape.RequestedModel, routePlan.UpstreamPath, attempt, attempts); retry {
			lastErr = retryErr
			continue
		}

		return &bufferedAttemptSuccess{
			acc:       acc,
			resp:      resp,
			sampleBuf: sampleBuf,
		}, false
	}

	writeBufferedAttemptFailure(w, lastStatus, lastErr)
	return nil, true
}

func (h *proxyHandler) candidateSupportingPath(conversationID string, exclude map[string]bool, accountType AccountType, requiredPlan string, provider Provider, path, requestedModel, forcedGeminiSeatID string) (*Account, error) {
	if h == nil || h.pool == nil {
		return nil, nil
	}
	if exclude == nil {
		exclude = map[string]bool{}
	}
	if forcedGeminiSeatID != "" {
		acc := h.pool.getByID(forcedGeminiSeatID)
		if acc == nil {
			return nil, fmt.Errorf("debug Gemini seat %s not found", forcedGeminiSeatID)
		}
		if accountType != AccountTypeGemini || acc.Type != AccountTypeGemini {
			return nil, fmt.Errorf("debug Gemini seat %s does not match the requested provider", forcedGeminiSeatID)
		}
		if !providerSupportsPathForAccount(provider, path, acc) {
			return nil, fmt.Errorf("debug Gemini seat %s does not support path %s", forcedGeminiSeatID, path)
		}
		return acc, nil
	}

	attempts := h.pool.count()
	if accountType != "" {
		if n := h.pool.countByType(accountType); n > 0 {
			attempts = n
		}
	}

	now := time.Now().UTC()
	for attempt := 0; attempt < attempts; attempt++ {
		acc := h.pool.candidate(conversationID, exclude, accountType, requiredPlan)
		if acc == nil {
			break
		}
		exclude[acc.ID] = true
		if !providerSupportsPathForAccount(provider, path, acc) {
			continue
		}
		if acc.Type == AccountTypeGemini {
			acc.mu.Lock()
			until, _, limited := geminiRequestedModelRateLimitUntilLocked(acc, requestedModel, path, now)
			acc.mu.Unlock()
			if limited && until.After(now) {
				continue
			}
		}
		return acc, nil
	}

	if err := h.gitLabClaudeSharedTPMCooldownError(now, accountType, requiredPlan, provider, path); err != nil {
		return nil, err
	}
	if err := h.gitLabCodexCooldownError(now, accountType, requiredPlan, provider, path); err != nil {
		return nil, err
	}
	return nil, nil
}

func (h *proxyHandler) propagateManagedGitLabClaudeSharedTPMCooldown(reqID string, trigger *Account, disposition managedGitLabClaudeErrorDisposition, headers http.Header, requestedModel string, now time.Time) bool {
	if h == nil || h.pool == nil || trigger == nil || !isGitLabClaudeAccount(trigger) || !disposition.RateLimit || !disposition.SharedOrgTPM {
		return false
	}

	scopeKey := gitLabClaudeScopeKey(trigger)
	if scopeKey == "" {
		return false
	}
	wait := managedGitLabClaudeCooldownWait(disposition, headers)
	if wait <= 0 {
		wait = managedGitLabClaudeOrgTPMRateLimitWait
	}
	until := now.Add(wait)
	sharedReason := managedGitLabClaudeSharedOrgTPMHealthError(disposition.Reason)

	h.pool.mu.RLock()
	accounts := append([]*Account{}, h.pool.accounts...)
	h.pool.mu.RUnlock()

	var affected []string
	for _, acc := range accounts {
		if acc == nil || !isGitLabClaudeAccount(acc) || gitLabClaudeScopeKey(acc) != scopeKey {
			continue
		}

		changed := false
		acc.mu.Lock()
		if acc.Disabled || acc.Dead {
			acc.mu.Unlock()
			continue
		}
		if acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until
			changed = true
		}
		if acc.HealthStatus != "rate_limited" {
			acc.HealthStatus = "rate_limited"
			changed = true
		}
		if acc.HealthCheckedAt.Before(now) {
			acc.HealthCheckedAt = now
			changed = true
		}
		if acc.HealthError != sharedReason {
			acc.HealthError = sharedReason
			changed = true
		}
		acc.mu.Unlock()

		if !changed {
			continue
		}
		affected = append(affected, acc.ID)
		if err := saveAccount(acc); err != nil {
			log.Printf("[%s] warning: failed to persist shared gitlab claude cooldown for %s: %v", reqID, acc.ID, err)
		}
	}

	if len(affected) == 0 {
		return false
	}
	log.Printf("[%s] gitlab claude shared org TPM cooldown activated scope=%s requested_model=%q until=%s seats=%s reason=%s", reqID, scopeKey, requestedModel, until.UTC().Format(time.RFC3339), strings.Join(affected, ","), stripManagedGitLabClaudeSharedOrgTPMHealthPrefix(sharedReason))
	return true
}

func (h *proxyHandler) gitLabClaudeSharedTPMCooldownError(now time.Time, accountType AccountType, requiredPlan string, provider Provider, path string) error {
	if h == nil || h.pool == nil || accountType != AccountTypeClaude || provider == nil || provider.Type() != AccountTypeClaude {
		return nil
	}

	h.pool.mu.RLock()
	accounts := append([]*Account{}, h.pool.accounts...)
	h.pool.mu.RUnlock()

	var until time.Time
	reason := ""
	for _, acc := range accounts {
		if acc == nil || !isGitLabClaudeAccount(acc) || !planMatchesRequired(acc.PlanType, requiredPlan) || !providerSupportsPathForAccount(provider, path, acc) {
			continue
		}
		acc.mu.Lock()
		rateLimitUntil := acc.RateLimitUntil
		healthStatus := acc.HealthStatus
		healthError := acc.HealthError
		acc.mu.Unlock()
		if !rateLimitUntil.After(now) || healthStatus != "rate_limited" || !isManagedGitLabClaudeSharedOrgTPMHealthError(healthError) {
			continue
		}
		if until.IsZero() || rateLimitUntil.Before(until) {
			until = rateLimitUntil
			reason = stripManagedGitLabClaudeSharedOrgTPMHealthPrefix(healthError)
		}
	}
	if until.IsZero() {
		return nil
	}
	return &rateLimitResponseError{
		message:    firstNonEmpty(reason, "gitlab claude organization token-per-minute cooldown active"),
		retryAfter: until.Sub(now),
	}
}

func (h *proxyHandler) gitLabCodexCooldownError(now time.Time, accountType AccountType, requiredPlan string, provider Provider, path string) error {
	if h == nil || h.pool == nil || accountType != AccountTypeCodex || provider == nil || provider.Type() != AccountTypeCodex || !codexRequiresGitLabPlan(requiredPlan) {
		return nil
	}

	h.pool.mu.RLock()
	accounts := append([]*Account{}, h.pool.accounts...)
	h.pool.mu.RUnlock()

	var until time.Time
	reason := ""
	relevant := 0
	for _, acc := range accounts {
		if acc == nil || !isGitLabCodexAccount(acc) || !planMatchesRequired(acc.PlanType, requiredPlan) || !providerSupportsPathForAccount(provider, path, acc) {
			continue
		}
		acc.mu.Lock()
		disabled := acc.Disabled
		dead := acc.Dead
		rateLimitUntil := acc.RateLimitUntil
		healthError := acc.HealthError
		acc.mu.Unlock()
		if disabled || dead {
			continue
		}
		relevant++
		if !rateLimitUntil.After(now) {
			return nil
		}
		if until.IsZero() || rateLimitUntil.Before(until) {
			until = rateLimitUntil
			reason = strings.TrimSpace(healthError)
		}
	}
	if relevant == 0 || until.IsZero() {
		return nil
	}
	return &rateLimitResponseError{
		message:    firstNonEmpty(reason, "gitlab codex cooldown active"),
		retryAfter: until.Sub(now),
	}
}

func (h *proxyHandler) deliverCopiedProxyResponse(
	w http.ResponseWriter,
	cancel context.CancelFunc,
	reqID string,
	trace *requestTrace,
	provider Provider,
	acc *Account,
	userID string,
	resp *http.Response,
	start time.Time,
	opts copiedProxyResponseDeliveryOptions,
) bool {
	if resp == nil {
		return false
	}

	provider.ParseUsageHeaders(acc, resp.Header)
	persistUsageSnapshot(h.store, acc)

	// Snapshot rate limits from headers for use in SSE callback
	// (Claude SSE events carry 0% — real data comes from headers)
	acc.mu.Lock()
	headerPrimaryPct := acc.Usage.PrimaryUsedPercent
	headerSecondaryPct := acc.Usage.SecondaryUsedPercent
	acc.mu.Unlock()

	// Write response to client.
	copyHeader(w.Header(), resp.Header)
	removeHopByHopHeaders(w.Header())
	// Replace individual account usage headers with pool aggregate usage.
	h.replaceUsageHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	respContentType := resp.Header.Get("Content-Type")
	// Use provider's SSE detection logic.
	isSSE := provider.DetectsSSE(opts.requestPath, respContentType)
	if trace != nil {
		trace.noteResponse(resp.StatusCode, resp, isSSE)
	}
	if opts.flushAfterWrite && !isSSE && flusher != nil {
		flusher.Flush()
	}
	if opts.logResponseDebug && h.cfg.debug {
		log.Printf("[%s] response: isSSE=%v content-type=%s", reqID, isSSE, respContentType)
	}

	// Stream body while optionally flushing.
	var writer io.Writer = w
	var fw *flushWriter
	if isSSE && flusher != nil {
		fw = &flushWriter{w: w, f: flusher, flushInterval: h.cfg.flushInterval}
		writer = fw
	}
	writer = newTraceWriter(writer, trace)
	managedStreamFailed := false
	var managedStreamFailureOnce sync.Once

	sampleBuf := opts.existingSample
	if opts.captureResponseSample {
		// Tee a bounded sample for usage extraction and conversation pinning.
		sampleLimit := int64(16 * 1024)
		if h.cfg.logBodies && h.cfg.bodyLogLimit > 0 {
			sampleLimit = h.cfg.bodyLogLimit
		}
		sampleBuf = &bytes.Buffer{}
		resp.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.TeeReader(resp.Body, &limitedWriter{w: sampleBuf, n: sampleLimit}),
			Closer: resp.Body,
		}
	}

	if isSSE {
		writer = h.wrapUsageInterceptWriter(
			reqID,
			writer,
			provider,
			acc,
			userID,
			trace,
			headerPrimaryPct,
			headerSecondaryPct,
			&managedStreamFailed,
			&managedStreamFailureOnce,
		)
	}

	// Wrap response body with idle timeout to kill zombie SSE connections.
	var idleReader *idleTimeoutReader
	if isSSE && h.cfg.streamIdleTimeout > 0 {
		var onIdleTimeout func()
		if trace != nil {
			onIdleTimeout = func() {
				trace.noteIdleTimeout(h.cfg.streamIdleTimeout)
			}
		}
		idleReader = newIdleTimeoutReader(resp.Body, h.cfg.streamIdleTimeout, cancel, onIdleTimeout)
		resp.Body = idleReader
	}

	_, copyErr := io.Copy(writer, resp.Body)
	if opts.closeBodyAfterCopy {
		resp.Body.Close()
	}
	if fw != nil {
		fw.stop()
	}

	var respSample []byte
	if sampleBuf != nil {
		respSample = sampleBuf.Bytes()
	}
	return h.finalizeCopiedProxyResponse(reqID, trace, provider, acc, userID, resp.StatusCode, isSSE, managedStreamFailed, opts.initialConversationID, headerPrimaryPct, headerSecondaryPct, respSample, copyErr, idleReader != nil, start, opts.debugLabel)
}

func (h *proxyHandler) deliverBufferedAttemptSuccess(
	w http.ResponseWriter,
	cancel context.CancelFunc,
	reqID string,
	trace *requestTrace,
	provider Provider,
	attemptSuccess *bufferedAttemptSuccess,
	requestPath string,
	userID, conversationID string,
	start time.Time,
) bool {
	if attemptSuccess == nil {
		return false
	}

	return h.deliverCopiedProxyResponse(
		w,
		cancel,
		reqID,
		trace,
		provider,
		attemptSuccess.acc,
		userID,
		attemptSuccess.resp,
		start,
		copiedProxyResponseDeliveryOptions{
			requestPath:           requestPath,
			initialConversationID: conversationID,
			debugLabel:            "done",
			logResponseDebug:      true,
			closeBodyAfterCopy:    true,
			existingSample:        attemptSuccess.sampleBuf,
		},
	)
}

func (h *proxyHandler) deliverStreamedProxyResponse(
	w http.ResponseWriter,
	cancel context.CancelFunc,
	reqID string,
	trace *requestTrace,
	requestPath string,
	provider Provider,
	acc *Account,
	userID string,
	resp *http.Response,
	needStatusBody bool,
	start time.Time,
) bool {
	return h.deliverCopiedProxyResponse(
		w,
		cancel,
		reqID,
		trace,
		provider,
		acc,
		userID,
		resp,
		start,
		copiedProxyResponseDeliveryOptions{
			requestPath:           requestPath,
			debugLabel:            "streamed done",
			flushAfterWrite:       needStatusBody,
			captureResponseSample: true,
		},
	)
}

func (h *proxyHandler) proxyRequest(w http.ResponseWriter, r *http.Request, reqID string) {
	start := time.Now()
	admission := h.resolveProxyAdmission(r, reqID)
	trace := requestTraceFromContext(r.Context())
	if trace != nil {
		trace.noteAdmission(admission)
	}
	if admission.Kind == AdmissionKindPassthrough {
		h.proxyPassthrough(w, r, reqID, admission.ProviderType, start)
		return
	}
	if admission.Kind == AdmissionKindRejected {
		http.Error(w, admission.Message, admission.StatusCode)
		return
	}
	if h.maybeServeCachedCodexModels(w, r, reqID, admission) {
		return
	}
	if h.maybeServeGitLabCodexAuxiliary(w, r, reqID, admission) {
		return
	}

	if isWebSocketUpgradeRequest(r) {
		shape := buildWebSocketRequestShape(r)
		routePlan, _, err := h.planRoute(admission, r, shape, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		statusCode := 0
		if routePlan.DebugGeminiSeatID, statusCode, err = h.resolveDebugGeminiSeatOverride(r, routePlan.AccountType); err != nil {
			http.Error(w, err.Error(), statusCode)
			return
		}
		if !h.ensureCodexRouteReady(w, reqID, routePlan, trace) {
			return
		}
		h.proxyRequestWebSocket(w, r, reqID, routePlan)
		return
	}

	streamBody := shouldStreamBody(r, h.cfg.maxInMemoryBodyBytes)
	if streamBody {
		shape := buildStreamedRequestShape(r)
		routePlan, _, err := h.planRoute(admission, r, shape, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		statusCode := 0
		if routePlan.DebugGeminiSeatID, statusCode, err = h.resolveDebugGeminiSeatOverride(r, routePlan.AccountType); err != nil {
			http.Error(w, err.Error(), statusCode)
			return
		}
		if !h.ensureCodexRouteReady(w, reqID, routePlan, trace) {
			return
		}
		if h.cfg.debug {
			log.Printf("[%s] streaming request body: method=%s path=%s provider=%s content-length=%d",
				reqID, r.Method, r.URL.Path, routePlan.AccountType, r.ContentLength)
		}
		h.proxyRequestStreamed(w, r, reqID, routePlan)
		return
	}

	bodyBytes, bodySample, err := readBodyForReplay(r.Body, h.cfg.logBodies, h.cfg.bodyLogLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	shape := buildBufferedRequestShape(r, bodyBytes, bodySample)
	routePlan, rewrittenBody, err := h.planRoute(admission, r, shape, bodyBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	statusCode := 0
	if routePlan.DebugGeminiSeatID, statusCode, err = h.resolveDebugGeminiSeatOverride(r, routePlan.AccountType); err != nil {
		http.Error(w, err.Error(), statusCode)
		return
	}
	if !h.ensureCodexRouteReady(w, reqID, routePlan, trace) {
		return
	}
	if rewrittenBody != nil {
		bodyBytes = rewrittenBody
	}
	inspect := bodyBytes
	if len(inspect) == 0 {
		inspect = bodySample
	}
	inspect = bodyForInspection(r, inspect)
	conversationID := routePlan.Shape.ConversationID
	requestedModel := routePlan.Shape.RequestedModel
	accountType := routePlan.AccountType
	provider := routePlan.Provider
	userID := routePlan.Admission.UserID

	if h.cfg.debug && conversationID == "" && len(inspect) > 0 {
		// Help debug why conversation id isn't being extracted without dumping the full body.
		var obj map[string]any
		if err := json.Unmarshal(inspect, &obj); err == nil {
			keys := make([]string, 0, len(obj))
			for k := range obj {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if len(keys) > 30 {
				keys = keys[:30]
			}
			log.Printf("[%s] conv_id empty; top-level keys (first %d): %s", reqID, len(keys), strings.Join(keys, ","))
		}
	}

	if h.cfg.debug {
		log.Printf("[%s] incoming %s %s provider=%s conv_id=%s authZ_len=%d chatgpt-id=%q content-type=%q content-encoding=%q body_bytes=%d",
			reqID,
			r.Method,
			r.URL.Path,
			accountType,
			conversationID,
			len(r.Header.Get("Authorization")),
			r.Header.Get("ChatGPT-Account-ID"),
			r.Header.Get("Content-Type"),
			r.Header.Get("Content-Encoding"),
			len(bodyBytes),
		)
		if requestedModel != "" {
			log.Printf("[%s] requested model=%s", reqID, requestedModel)
		}
	}
	if h.cfg.logBodies && len(bodySample) > 0 {
		log.Printf("[%s] request body sample (%d bytes): %s", reqID, len(bodySample), safeText(bodySample))
	}

	// Determine timeout: honour X-Stainless-Timeout from the Anthropic SDK when present,
	// otherwise fall back to streaming vs non-streaming defaults.
	timeout := clientOrDefaultTimeout(r, h.cfg.requestTimeout, h.cfg.streamTimeout, inspect)

	ctx := r.Context()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	attemptSuccess, handled := h.runBufferedAttemptContour(ctx, w, r, bodyBytes, reqID, routePlan)
	if handled {
		return
	}

	if !h.deliverBufferedAttemptSuccess(w, cancel, reqID, trace, provider, attemptSuccess, r.URL.Path, userID, conversationID, start) {
		return
	}
	return
}

func (h *proxyHandler) proxyRequestWebSocket(w http.ResponseWriter, r *http.Request, reqID string, routePlan RoutePlan) {
	start := time.Now()
	trace := requestTraceFromContext(r.Context())
	accountType := routePlan.AccountType
	conversationID := routePlan.Shape.ConversationID
	requestedModel := routePlan.Shape.RequestedModel
	requestPath := routePlan.UpstreamPath
	userID := routePlan.Admission.UserID
	provider := routePlan.Provider
	targetBase := routePlan.TargetBase

	acc, err := h.candidateSupportingPath(conversationID, map[string]bool{}, accountType, routePlan.RequiredPlan, provider, requestPath, requestedModel, routePlan.DebugGeminiSeatID)
	if err != nil {
		writeHTTPErrorWithOptionalRateLimit(w, err, http.StatusServiceUnavailable)
		return
	}
	if acc == nil {
		http.Error(w, fmt.Sprintf("no live %s accounts", accountType), http.StatusServiceUnavailable)
		return
	}

	atomic.AddInt64(&acc.Inflight, 1)
	atomic.AddInt64(&h.inflight, 1)
	defer func() {
		atomic.AddInt64(&acc.Inflight, -1)
		atomic.AddInt64(&h.inflight, -1)
	}()

	refreshFailed := false
	if !h.cfg.disableRefresh && !skipPreemptiveRefreshForAccount(acc) && h.needsRefresh(acc) {
		if err := h.refreshAccount(r.Context(), acc); err != nil {
			if isRateLimitError(err) {
				h.applyRateLimit(acc, nil, defaultRateLimitBackoff)
			} else {
				refreshFailed = true
			}
			if h.cfg.debug {
				log.Printf("[%s] refresh %s failed before websocket request: %v", reqID, acc.ID, err)
			}
		}
	}

	if !providerSupportsPathForAccount(provider, routePlan.UpstreamPath, acc) {
		http.Error(w, fmt.Sprintf("account %s does not support path %s", acc.ID, routePlan.UpstreamPath), http.StatusServiceUnavailable)
		return
	}
	if err := h.maybeProbeManagedCodexAPIKey(r.Context(), acc); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	targetBase = providerUpstreamURLForAccount(provider, routePlan.UpstreamPath, acc)
	if trace != nil {
		trace.noteRoute(routePlan, acc, targetBase, "websocket", 1, 1)
	}

	acc.mu.Lock()
	access := acc.AccessToken
	acc.mu.Unlock()
	if access == "" {
		http.Error(w, fmt.Sprintf("account %s has empty access token", acc.ID), http.StatusServiceUnavailable)
		return
	}
	_, protocolAuthUsed := extractWebSocketProtocolBearerToken(r.Header.Get("Sec-WebSocket-Protocol"))

	outURL := new(url.URL)
	*outURL = *r.URL
	outURL.Scheme = targetBase.Scheme
	outURL.Host = targetBase.Host
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, routePlan.UpstreamPath, acc))

	// For Claude OAuth tokens, add beta=true query param (required for OAuth to work)
	if provider.Type() == AccountTypeClaude && strings.HasPrefix(access, "sk-ant-oat") {
		q := outURL.Query()
		q.Set("beta", "true")
		outURL.RawQuery = q.Encode()
	}

	var statusCode int
	var proxyErr error
	statusCode, proxyErr = h.servePooledWebSocketProxy(w, r, reqID, trace, outURL, targetBase, provider, acc, access, protocolAuthUsed, refreshFailed, conversationID, requestedModel, requestPath)

	if proxyErr != nil {
		h.recent.add(proxyErr.Error())
		h.metrics.inc("error", acc.ID)
		return
	}
	if statusCode != 0 {
		h.metrics.inc(strconv.Itoa(statusCode), acc.ID)
	}
	if h.cfg.debug {
		log.Printf("[%s] websocket done status=%d account=%s user=%s duration_ms=%d", reqID, statusCode, acc.ID, userID, time.Since(start).Milliseconds())
	}
}

func (h *proxyHandler) servePooledWebSocketProxy(
	w http.ResponseWriter,
	r *http.Request,
	reqID string,
	trace *requestTrace,
	outURL, targetBase *url.URL,
	provider Provider,
	acc *Account,
	access string,
	protocolAuthUsed bool,
	refreshFailed bool,
	conversationID, requestedModel, requestPath string,
) (int, error) {
	var statusCode int
	var proxyErr error

	reverseProxy := &httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: h.cfg.flushInterval,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = outURL.Scheme
			pr.Out.URL.Host = outURL.Host
			pr.Out.URL.Path = outURL.Path
			pr.Out.URL.RawPath = outURL.RawPath
			pr.Out.URL.RawQuery = outURL.RawQuery
			pr.Out.Host = targetBase.Host
			pr.Out.Header = cloneHeader(pr.In.Header)
			stripLocalTraceHeaders(pr.Out.Header)

			// Always overwrite client-provided auth for pooled accounts.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("ChatGPT-Account-ID")
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("x-goog-api-key")
			removeConflictingProxyHeaders(pr.Out.Header)
			provider.SetAuthHeaders(pr.Out, acc)
			if protocolAuthUsed {
				rewriteWebSocketProtocolBearerToken(pr.Out.Header, access)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			statusCode = resp.StatusCode
			return h.modifyWebSocketProxyResponse(reqID, trace, provider, acc, resp, refreshFailed, conversationID, requestedModel, requestPath)
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			proxyErr = err
			if trace != nil {
				trace.noteTransportError("websocket_proxy", acc, err)
			}
			if h.cfg.debug {
				log.Printf("[%s] websocket proxy error (account=%s): %v", reqID, acc.ID, err)
			}
			http.Error(rw, err.Error(), http.StatusBadGateway)
		},
	}

	if h.cfg.debug {
		log.Printf("[%s] websocket -> %s %s (account=%s)", reqID, r.Method, outURL.String(), acc.ID)
	}

	reverseProxy.ServeHTTP(w, r)
	if trace != nil {
		finalStatusCode := statusCode
		if finalStatusCode == 0 && proxyErr != nil {
			finalStatusCode = http.StatusBadGateway
		}
		trace.noteFinish(finalStatusCode, false, false, proxyErr)
	}
	return statusCode, proxyErr
}

func (h *proxyHandler) finalizeWebSocketSuccessState(acc *Account, conversationID string, statusCode int) {
	if acc == nil {
		return
	}
	if statusCode != http.StatusSwitchingProtocols && (statusCode < 200 || statusCode >= 300) {
		return
	}

	if conversationID != "" && h.pool != nil {
		h.pool.pin(conversationID, acc.ID)
	}

	now := time.Now()
	shouldPersistGemini := false
	acc.mu.Lock()
	if acc.Type == AccountTypeGemini {
		shouldPersistGemini = shouldPersistHealthyGeminiStateLocked(acc)
	}
	applySuccessfulAccountStateLocked(acc, now)
	acc.mu.Unlock()
	persistAccountRuntimeState(h.store, acc)
	if shouldPersistGemini {
		if err := saveAccount(acc); err != nil {
			log.Printf("warning: failed to persist healthy gemini account %s after websocket success: %v", acc.ID, err)
		}
	}
}

func (h *proxyHandler) modifyWebSocketProxyResponse(reqID string, trace *requestTrace, provider Provider, acc *Account, resp *http.Response, refreshFailed bool, conversationID, requestedModel, requestPath string) error {
	if resp == nil {
		return nil
	}

	provider.ParseUsageHeaders(acc, resp.Header)
	persistUsageSnapshot(h.store, acc)
	if trace != nil {
		trace.noteResponse(resp.StatusCode, resp, false)
	}
	h.applyPreCopyUpstreamStatusHandling(reqID, acc, resp, refreshFailed, requestedModel, requestPath, true)
	h.finalizeWebSocketSuccessState(acc, conversationID, resp.StatusCode)
	return nil
}

func (h *proxyHandler) proxyPassthroughWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	reqID string,
	providerType AccountType,
	provider Provider,
	targetBase *url.URL,
	accountHint *Account,
	start time.Time,
) {
	trace := requestTraceFromContext(r.Context())
	outURL := new(url.URL)
	*outURL = *r.URL
	outURL.Scheme = targetBase.Scheme
	outURL.Host = targetBase.Host
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, r.URL.Path, accountHint))
	if trace != nil {
		traceAcc := accountHint
		if traceAcc == nil || strings.TrimSpace(traceAcc.ID) == "" {
			traceAcc = &Account{ID: "passthrough", Type: providerType}
		}
		trace.noteRoute(RoutePlan{
			Provider: provider,
			Shape:    buildWebSocketRequestShape(r),
		}, traceAcc, targetBase, "websocket_passthrough", 1, 1)
	}

	// For Claude OAuth passthrough tokens, add beta=true query param.
	if providerType == AccountTypeClaude {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token := strings.TrimPrefix(auth, "Bearer ")
			if strings.HasPrefix(token, "sk-ant-oat") {
				q := outURL.Query()
				q.Set("beta", "true")
				outURL.RawQuery = q.Encode()
			}
		}
	}

	var statusCode int
	var proxyErr error

	reverseProxy := &httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: h.cfg.flushInterval,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = outURL.Scheme
			pr.Out.URL.Host = outURL.Host
			pr.Out.URL.Path = outURL.Path
			pr.Out.URL.RawPath = outURL.RawPath
			pr.Out.URL.RawQuery = outURL.RawQuery
			pr.Out.Host = targetBase.Host
			pr.Out.Header = cloneHeader(pr.In.Header)
			stripLocalTraceHeaders(pr.Out.Header)
			removeConflictingProxyHeaders(pr.Out.Header)

			if providerType == AccountTypeClaude && pr.Out.Header.Get("anthropic-version") == "" {
				pr.Out.Header.Set("anthropic-version", "2023-06-01")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			statusCode = resp.StatusCode
			if trace != nil {
				trace.noteResponse(resp.StatusCode, resp, false)
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			proxyErr = err
			if trace != nil {
				trace.noteTransportError("websocket_passthrough_proxy", accountHint, err)
			}
			if h.cfg.debug {
				log.Printf("[%s] passthrough websocket proxy error: %v", reqID, err)
			}
			http.Error(rw, err.Error(), http.StatusBadGateway)
		},
	}

	if h.cfg.debug {
		log.Printf("[%s] passthrough websocket -> %s %s", reqID, r.Method, outURL.String())
	}

	reverseProxy.ServeHTTP(w, r)
	if trace != nil {
		finalStatusCode := statusCode
		if finalStatusCode == 0 && proxyErr != nil {
			finalStatusCode = http.StatusBadGateway
		}
		trace.noteFinish(finalStatusCode, false, false, proxyErr)
	}

	if proxyErr != nil {
		h.recent.add(proxyErr.Error())
		h.metrics.inc("error", "passthrough")
		return
	}
	if statusCode != 0 {
		h.metrics.inc(strconv.Itoa(statusCode), "passthrough")
	}
	if h.cfg.debug {
		log.Printf("[%s] passthrough websocket done status=%d duration_ms=%d", reqID, statusCode, time.Since(start).Milliseconds())
	}
}

func (h *proxyHandler) proxyRequestStreamed(w http.ResponseWriter, r *http.Request, reqID string, routePlan RoutePlan) {
	start := time.Now()
	trace := requestTraceFromContext(r.Context())
	accountType := routePlan.AccountType
	userID := routePlan.Admission.UserID
	provider := routePlan.Provider
	targetBase := routePlan.TargetBase

	acc, err := h.candidateSupportingPath(routePlan.Shape.ConversationID, map[string]bool{}, accountType, routePlan.RequiredPlan, provider, routePlan.UpstreamPath, routePlan.Shape.RequestedModel, routePlan.DebugGeminiSeatID)
	if err != nil {
		writeHTTPErrorWithOptionalRateLimit(w, err, http.StatusServiceUnavailable)
		return
	}
	if acc == nil {
		http.Error(w, fmt.Sprintf("no live %s accounts", accountType), http.StatusServiceUnavailable)
		return
	}

	atomic.AddInt64(&acc.Inflight, 1)
	atomic.AddInt64(&h.inflight, 1)
	defer func() {
		atomic.AddInt64(&acc.Inflight, -1)
		atomic.AddInt64(&h.inflight, -1)
	}()

	// For streamed-body requests we can't inspect the body, so pass nil.
	// clientOrDefaultTimeout will still check X-Stainless-Timeout header.
	timeout := clientOrDefaultTimeout(r, h.cfg.requestTimeout, h.cfg.streamTimeout, nil)

	ctx := r.Context()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// Refresh before building headers to ensure we use the latest token.
	refreshFailed := false
	if !h.cfg.disableRefresh && !skipPreemptiveRefreshForAccount(acc) && h.needsRefresh(acc) {
		if err := h.refreshAccount(ctx, acc); err != nil {
			if isRateLimitError(err) {
				h.applyRateLimit(acc, nil, defaultRateLimitBackoff)
			} else {
				refreshFailed = true
			}
			if h.cfg.debug {
				log.Printf("[%s] refresh %s failed before streamed request: %v", reqID, acc.ID, err)
			}
		}
	}

	if !providerSupportsPathForAccount(provider, routePlan.UpstreamPath, acc) {
		http.Error(w, fmt.Sprintf("account %s does not support path %s", acc.ID, routePlan.UpstreamPath), http.StatusServiceUnavailable)
		return
	}
	if err := h.maybeProbeManagedCodexAPIKey(ctx, acc); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	targetBase = providerUpstreamURLForAccount(provider, routePlan.UpstreamPath, acc)
	if trace != nil {
		trace.noteRoute(routePlan, acc, targetBase, "streamed", 1, 1)
	}

	outURL := new(url.URL)
	*outURL = *r.URL
	outURL.Scheme = targetBase.Scheme
	outURL.Host = targetBase.Host
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, routePlan.UpstreamPath, acc))

	acc.mu.Lock()
	access := acc.AccessToken
	acc.mu.Unlock()
	if access == "" {
		http.Error(w, fmt.Sprintf("account %s has empty access token", acc.ID), http.StatusServiceUnavailable)
		return
	}

	// For Claude OAuth tokens, add beta=true query param (required for OAuth to work)
	if provider.Type() == AccountTypeClaude && strings.HasPrefix(access, "sk-ant-oat") {
		q := outURL.Query()
		q.Set("beta", "true")
		outURL.RawQuery = q.Encode()
	}

	var reqSample *bytes.Buffer
	var body io.Reader = r.Body
	if h.cfg.logBodies && h.cfg.bodyLogLimit > 0 {
		reqSample = &bytes.Buffer{}
		body = io.TeeReader(r.Body, &limitedWriter{w: reqSample, n: h.cfg.bodyLogLimit})
	}

	outReq, err := http.NewRequestWithContext(ctx, r.Method, outURL.String(), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Host = targetBase.Host
	outReq.Header = cloneHeader(r.Header)
	removeHopByHopHeaders(outReq.Header)
	removeConflictingProxyHeaders(outReq.Header)
	stripLocalTraceHeaders(outReq.Header)
	stripLocalTraceHeaders(outReq.Header)
	if r.ContentLength >= 0 {
		outReq.ContentLength = r.ContentLength
	}

	// Always overwrite client-provided auth; the proxy is the single source of truth.
	outReq.Header.Del("Authorization")
	outReq.Header.Del("ChatGPT-Account-ID")
	outReq.Header.Del("X-Api-Key")
	outReq.Header.Del("x-goog-api-key")

	// Remove Cloudflare/proxy headers that would cause issues with OpenAI's Cloudflare
	outReq.Header.Del("Cdn-Loop")
	outReq.Header.Del("Cf-Connecting-Ip")
	outReq.Header.Del("Cf-Ray")
	outReq.Header.Del("Cf-Visitor")
	outReq.Header.Del("Cf-Warp-Tag-Id")
	outReq.Header.Del("Cf-Ipcountry")
	outReq.Header.Del("X-Forwarded-For")
	outReq.Header.Del("X-Forwarded-Proto")
	outReq.Header.Del("X-Real-Ip")

	// Use provider's SetAuthHeaders method for provider-specific auth
	provider.SetAuthHeaders(outReq, acc)

	if h.cfg.debug {
		authHeader := outReq.Header.Get("Authorization")
		authLen := len(authHeader)
		authPreview := ""
		if authLen > 20 {
			authPreview = authHeader[:20] + "..."
		} else if authLen > 0 {
			authPreview = authHeader
		}
		log.Printf("[%s] streamed -> %s %s (account=%s account_id=%s auth_len=%d auth=%s)", reqID, outReq.Method, outReq.URL.String(), acc.ID, acc.AccountID, authLen, authPreview)
	}

	resp, err := h.transport.RoundTrip(outReq)
	if err != nil {
		if trace != nil {
			trace.noteTransportError("streamed_roundtrip", acc, err)
		}
		acc.mu.Lock()
		acc.Penalty += 0.2
		acc.mu.Unlock()
		h.recent.add(err.Error())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if h.cfg.logBodies && reqSample != nil && reqSample.Len() > 0 {
		log.Printf("[%s] request body sample (%d bytes): %s", reqID, reqSample.Len(), safeText(reqSample.Bytes()))
	}

	statusHandling := h.applyPreCopyUpstreamStatusHandling(reqID, acc, resp, refreshFailed, routePlan.Shape.RequestedModel, routePlan.UpstreamPath, false)
	if statusHandling.NeedStatusBody && !isManagedCodexAPIKeyAccount(acc) && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		log.Printf("[%s] account %s got %d from %s, body=%s", reqID, acc.ID, resp.StatusCode, outReq.URL.Host, safeText(statusHandling.InspectedBody))
	}
	if !h.deliverStreamedProxyResponse(w, cancel, reqID, trace, r.URL.Path, provider, acc, userID, resp, statusHandling.NeedStatusBody, start) {
		return
	}
}

// clientOrDefaultTimeout picks the request timeout. If the client sent X-Stainless-Timeout
// (Anthropic SDK), use that. Otherwise fall back to streaming vs non-streaming defaults.
func clientOrDefaultTimeout(r *http.Request, reqTimeout, streamTimeout time.Duration, body []byte) time.Duration {
	// Honour the SDK's requested timeout when present.
	if v := r.Header.Get("X-Stainless-Timeout"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}

	isStreaming := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
	if !isStreaming && len(body) > 0 {
		var obj map[string]any
		if json.Unmarshal(body, &obj) == nil {
			if s, ok := obj["stream"].(bool); ok && s {
				isStreaming = true
			}
		}
	}
	if isStreaming {
		return streamTimeout // 0 means no timeout
	}
	return reqTimeout
}

func isRetryableStatus(code int) bool {
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return true
	}
	return code >= 500 && code <= 599
}

func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate limited") || strings.Contains(msg, "too many requests") || strings.Contains(msg, "429")
}

func parseRetryAfter(h http.Header) (time.Duration, bool) {
	if h == nil {
		return 0, false
	}
	val := strings.TrimSpace(h.Get("Retry-After"))
	if val == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(val, 10, 64); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(val); err == nil {
		wait := time.Until(when)
		if wait <= 0 {
			return 0, false
		}
		return wait, true
	}
	return 0, false
}

func (h *proxyHandler) finalizeCopiedProxyResponse(reqID string, trace *requestTrace, provider Provider, acc *Account, userID string, statusCode int, isSSE bool, managedStreamFailed bool, initialConversationID string, headerPrimaryPct, headerSecondaryPct float64, respSample []byte, copyErr error, logStreamError bool, start time.Time, debugLabel string) bool {
	if acc == nil {
		return false
	}
	if copyErr != nil {
		if cutoff, ok := matchClaudePingTailCutoff(copyErr, acc); ok {
			h.finalizeProxyResponse(reqID, provider, acc, userID, statusCode, isSSE, managedStreamFailed, initialConversationID, headerPrimaryPct, headerSecondaryPct, respSample)
			if h.metrics != nil {
				h.metrics.inc(strconv.Itoa(statusCode), acc.ID)
			}
			if trace != nil {
				trace.noteFinish(statusCode, isSSE, managedStreamFailed, nil)
			}
			log.Printf(
				"[%s] claude gitlab ping-only tail cut off early (account=%s stalled_ms=%d timeout_ms=%d last_non_ping_type=%q)",
				reqID,
				acc.ID,
				cutoff.stalledFor.Milliseconds(),
				cutoff.timeout.Milliseconds(),
				cutoff.lastNonPingType,
			)
			if h.cfg.debug {
				log.Printf("[%s] %s status=%d account=%s duration_ms=%d", reqID, debugLabel, statusCode, acc.ID, time.Since(start).Milliseconds())
			}
			return true
		}
		if h.recent != nil {
			h.recent.add(copyErr.Error())
		}
		if h.metrics != nil {
			h.metrics.inc("error", acc.ID)
		}
		if logStreamError {
			log.Printf("[%s] SSE stream error (account=%s): %v", reqID, acc.ID, copyErr)
		}
		if trace != nil {
			trace.noteFinish(statusCode, isSSE, managedStreamFailed, copyErr)
		}
		return false
	}

	h.finalizeProxyResponse(reqID, provider, acc, userID, statusCode, isSSE, managedStreamFailed, initialConversationID, headerPrimaryPct, headerSecondaryPct, respSample)
	if h.metrics != nil {
		h.metrics.inc(strconv.Itoa(statusCode), acc.ID)
	}
	if trace != nil {
		trace.noteFinish(statusCode, isSSE, managedStreamFailed, nil)
	}
	if h.cfg.debug {
		log.Printf("[%s] %s status=%d account=%s duration_ms=%d", reqID, debugLabel, statusCode, acc.ID, time.Since(start).Milliseconds())
	}
	return true
}

func (h *proxyHandler) finalizeProxyResponse(reqID string, provider Provider, acc *Account, userID string, statusCode int, isSSE bool, managedStreamFailed bool, initialConversationID string, headerPrimaryPct, headerSecondaryPct float64, respSample []byte) {
	if acc == nil {
		return
	}
	if h.cfg.logBodies && len(respSample) > 0 {
		log.Printf("[%s] response body sample (%d bytes): %s", reqID, len(respSample), safeText(respSample))
	}
	if !isSSE && len(respSample) > 0 {
		h.updateUsageFromBody(provider, acc, userID, headerPrimaryPct, headerSecondaryPct, respSample)
	}
	if statusCode < 200 || statusCode >= 300 || managedStreamFailed {
		return
	}

	conversationID := initialConversationID
	if conversationID == "" && len(respSample) > 0 {
		conversationID = extractConversationIDFromSSE(respSample)
	}
	if conversationID != "" && h.pool != nil {
		h.pool.pin(conversationID, acc.ID)
	}

	now := time.Now()
	shouldPersistAccountState := false
	persistLabel := "account"
	acc.mu.Lock()
	if isGitLabClaudeAccount(acc) {
		shouldPersistAccountState = acc.Dead || !acc.RateLimitUntil.IsZero() || acc.GitLabQuotaExceededCount > 0 || acc.HealthStatus != "healthy" || acc.HealthError != ""
		persistLabel = "gitlab claude"
		clearAccountDeadStateLocked(acc, now, false)
		acc.HealthStatus = "healthy"
		acc.HealthError = ""
		acc.HealthCheckedAt = now
		acc.LastHealthyAt = now
		acc.RateLimitUntil = time.Time{}
		acc.GitLabQuotaExceededCount = 0
		acc.GitLabLastQuotaExceededAt = time.Time{}
	} else if acc.Type == AccountTypeGemini {
		shouldPersistAccountState = shouldPersistHealthyGeminiStateLocked(acc)
		persistLabel = "gemini"
	}
	applySuccessfulAccountStateLocked(acc, now)
	acc.mu.Unlock()
	persistAccountRuntimeState(h.store, acc)
	if shouldPersistAccountState {
		if err := saveAccount(acc); err != nil {
			log.Printf("[%s] warning: failed to persist healthy %s account %s: %v", reqID, persistLabel, acc.ID, err)
		}
	}
}

func shouldPersistHealthyGeminiStateLocked(acc *Account) bool {
	if acc == nil || acc.Type != AccountTypeGemini {
		return false
	}
	expectedStatus, expectedError := successfulGeminiHealthStateLocked(acc)
	expectedOperationalState, expectedOperationalReason := successfulGeminiOperationalStateLocked(acc)
	healthStatus := strings.TrimSpace(acc.HealthStatus)
	healthError := strings.TrimSpace(acc.HealthError)
	return acc.Dead ||
		!acc.RateLimitUntil.IsZero() ||
		healthStatus != expectedStatus ||
		healthError != expectedError ||
		strings.TrimSpace(acc.GeminiOperationalState) != expectedOperationalState ||
		strings.TrimSpace(acc.GeminiOperationalReason) != expectedOperationalReason ||
		acc.GeminiOperationalCheckedAt.IsZero() ||
		acc.GeminiOperationalLastSuccessAt.IsZero() ||
		acc.HealthCheckedAt.IsZero() ||
		(expectedStatus == "healthy" && acc.LastHealthyAt.IsZero())
}

func successfulGeminiHealthStateLocked(acc *Account) (string, string) {
	if acc == nil || acc.Type != AccountTypeGemini {
		return "healthy", ""
	}
	syncGeminiProviderTruthStateLocked(acc)
	status := managedGeminiHealthStatusForProviderTruthState(acc.GeminiProviderTruthState)
	if status == "" {
		status = "healthy"
	}
	switch status {
	case "quota_forbidden":
		return status, sanitizeStatusMessage(firstNonEmpty(strings.TrimSpace(acc.AntigravityQuotaForbiddenReason), strings.TrimSpace(acc.GeminiProviderTruthReason)))
	case "missing_project_id", "project_only_unverified", "auth_only", "proxy_disabled":
		return status, sanitizeStatusMessage(acc.GeminiProviderTruthReason)
	default:
		return status, ""
	}
}

// Caller must hold acc.mu when applying shared success-state recovery.
func applySuccessfulAccountStateLocked(acc *Account, now time.Time) {
	if acc == nil {
		return
	}
	if isManagedCodexAPIKeyAccount(acc) {
		clearAccountDeadStateLocked(acc, now, false)
		acc.HealthStatus = "healthy"
		acc.HealthError = ""
		acc.HealthCheckedAt = now
		acc.LastHealthyAt = now
		acc.RateLimitUntil = time.Time{}
	} else if acc.Type == AccountTypeGemini {
		clearAccountDeadStateLocked(acc, now, false)
		acc.HealthStatus, acc.HealthError = successfulGeminiHealthStateLocked(acc)
		noteGeminiOperationalSuccessLocked(acc, now, "proxy")
		acc.HealthCheckedAt = now
		if acc.HealthStatus == "healthy" {
			acc.LastHealthyAt = now
		}
		acc.RateLimitUntil = time.Time{}
	}
	acc.LastUsed = now
	if acc.Penalty > 0 {
		acc.Penalty *= 0.5
		if acc.Penalty < 0.01 {
			acc.Penalty = 0
		}
	}
}

func (h *proxyHandler) applyRateLimit(a *Account, hdr http.Header, fallback time.Duration) time.Duration {
	if a == nil {
		return 0
	}
	wait, ok := parseRetryAfter(hdr)
	if !ok {
		wait = fallback
	}
	until := time.Now().Add(wait)
	if wait <= 0 {
		return 0
	}

	a.mu.Lock()

	if a.RateLimitUntil.Before(until) {
		a.RateLimitUntil = until
	}
	a.mu.Unlock()
	return wait
}

// looksLikeProviderCredential checks if a token looks like a real provider credential
// that should be passed through directly rather than replaced with pool credentials.
func looksLikeProviderCredential(authHeader string) (bool, AccountType) {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false, ""
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		return false, ""
	}

	// Pool-generated Claude tokens (current and legacy) should NOT be passed through.
	// These are fake Claude OAuth/API-looking tokens that identify pool users.
	if strings.HasPrefix(token, ClaudePoolTokenPrefix) || strings.HasPrefix(token, ClaudePoolTokenLegacyPrefix) {
		return false, ""
	}

	// Claude/Anthropic API keys: sk-ant-api* or sk-ant-oat* (OAuth tokens)
	if strings.HasPrefix(token, "sk-ant-") {
		return true, AccountTypeClaude
	}

	// OpenAI-style API keys: sk-proj-*, sk-* (but not sk-ant-)
	if strings.HasPrefix(token, "sk-proj-") || (strings.HasPrefix(token, "sk-") && !strings.HasPrefix(token, "sk-ant-")) {
		return true, AccountTypeCodex
	}

	// Google OAuth tokens typically start with ya29. (access tokens)
	// But NOT pool tokens which are ya29.pool-*
	if strings.HasPrefix(token, "ya29.") && !strings.HasPrefix(token, "ya29.pool-") {
		return true, AccountTypeGemini
	}

	return false, ""
}

// isClaudePoolToken checks if the auth header contains a pool-generated Claude token.
// Returns (isPoolToken, userID) if valid.
func isClaudePoolToken(secret, authHeader string) (bool, string) {
	if secret == "" {
		return false, ""
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false, ""
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	userID, valid := parseClaudePoolToken(secret, token)
	return valid, userID
}

// proxyPassthrough handles requests where the user provides their own credentials.
// The request is proxied directly to the upstream without using pool accounts.
func (h *proxyHandler) proxyPassthrough(w http.ResponseWriter, r *http.Request, reqID string, providerType AccountType, start time.Time) {
	provider := h.registry.ForType(providerType)
	if provider == nil {
		// Fallback: try to detect from path and headers
		provider, _ = h.pickUpstream(r.URL.Path, r.Header)
	}
	if provider == nil {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}

	var accountHint *Account
	if providerType == AccountTypeCodex {
		accountHint = &Account{Type: AccountTypeCodex, AuthMode: accountAuthModeAPIKey, PlanType: "api"}
		if !providerSupportsPathForAccount(provider, r.URL.Path, accountHint) {
			http.Error(w, "openai api passthrough does not support this path", http.StatusBadRequest)
			return
		}
	}

	targetBase := providerUpstreamURLForAccount(provider, r.URL.Path, accountHint)
	if isWebSocketUpgradeRequest(r) {
		h.proxyPassthroughWebSocket(w, r, reqID, providerType, provider, targetBase, accountHint, start)
		return
	}
	streamBody := shouldStreamBody(r, h.cfg.maxInMemoryBodyBytes)
	if streamBody {
		if h.cfg.debug {
			log.Printf("[%s] passthrough streaming body: method=%s path=%s provider=%s content-length=%d",
				reqID, r.Method, r.URL.Path, providerType, r.ContentLength)
		}
		h.proxyPassthroughStreamed(w, r, reqID, providerType, provider, targetBase, accountHint, start)
		return
	}

	bodyBytes, bodySample, err := readBodyForReplay(r.Body, h.cfg.logBodies, h.cfg.bodyLogLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if h.cfg.debug {
		log.Printf("[%s] passthrough %s %s provider=%s content-type=%q body_bytes=%d",
			reqID, r.Method, r.URL.Path, providerType,
			r.Header.Get("Content-Type"), len(bodyBytes))
		// Debug: log all headers for Claude passthrough
		if providerType == AccountTypeClaude {
			var hdrs []string
			for k, v := range r.Header {
				if strings.HasPrefix(strings.ToLower(k), "anthropic") {
					hdrs = append(hdrs, fmt.Sprintf("%s=%s", k, v[0]))
				}
			}
			log.Printf("[%s] passthrough claude anthropic headers: %v", reqID, hdrs)
		}
	}
	if h.cfg.logBodies && len(bodySample) > 0 {
		log.Printf("[%s] passthrough request body sample (%d bytes): %s", reqID, len(bodySample), safeText(bodySample))
	}

	timeout := clientOrDefaultTimeout(r, h.cfg.requestTimeout, h.cfg.streamTimeout, bodyBytes)

	ctx := r.Context()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// Build the outgoing request - preserving the original Authorization header
	outURL := new(url.URL)
	*outURL = *r.URL
	outURL.Scheme = targetBase.Scheme
	outURL.Host = targetBase.Host
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, r.URL.Path, accountHint))

	var body io.Reader
	if len(bodyBytes) > 0 {
		body = bytes.NewReader(bodyBytes)
	}
	outReq, err := http.NewRequestWithContext(ctx, r.Method, outURL.String(), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	outReq.Host = targetBase.Host
	outReq.Header = cloneHeader(r.Header)
	removeHopByHopHeaders(outReq.Header)
	removeConflictingProxyHeaders(outReq.Header)
	stripLocalTraceHeaders(outReq.Header)

	// For Claude, ensure required headers are set
	if providerType == AccountTypeClaude {
		if outReq.Header.Get("anthropic-version") == "" {
			outReq.Header.Set("anthropic-version", "2023-06-01")
		}
	}

	if h.cfg.debug {
		log.Printf("[%s] passthrough -> %s %s", reqID, outReq.Method, outReq.URL.String())
	}

	// Full dump for Claude passthrough requests
	if providerType == AccountTypeClaude {
		log.Printf("[%s] === CLAUDE PASSTHROUGH FULL DUMP ===", reqID)
		log.Printf("[%s] URL: %s", reqID, outReq.URL.String())
		for k, v := range outReq.Header {
			for _, val := range v {
				if len(val) > 100 {
					log.Printf("[%s] Header %s: %s...(truncated)", reqID, k, val[:100])
				} else {
					log.Printf("[%s] Header %s: %s", reqID, k, val)
				}
			}
		}
		log.Printf("[%s] === END DUMP ===", reqID)
	}

	resp, err := h.transport.RoundTrip(outReq)
	if err != nil {
		h.recent.add(err.Error())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Write response to client
	copyHeader(w.Header(), resp.Header)
	removeHopByHopHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	respContentType := resp.Header.Get("Content-Type")
	isSSE := provider.DetectsSSE(r.URL.Path, respContentType)

	var writer io.Writer = w
	if isSSE && flusher != nil {
		fw := &flushWriter{w: w, f: flusher, flushInterval: h.cfg.flushInterval}
		writer = fw
		defer fw.stop()
	}

	// Wrap response body with idle timeout to kill zombie SSE connections.
	var idleReader *idleTimeoutReader
	if isSSE && h.cfg.streamIdleTimeout > 0 {
		idleReader = newIdleTimeoutReader(resp.Body, h.cfg.streamIdleTimeout, cancel, func() {
			log.Printf("[%s] passthrough SSE idle timeout after %v", reqID, h.cfg.streamIdleTimeout)
		})
		defer idleReader.Close()
	}

	if _, copyErr := io.Copy(writer, resp.Body); copyErr != nil {
		h.recent.add(copyErr.Error())
		h.metrics.inc("error", "passthrough")
		if idleReader != nil {
			log.Printf("[%s] passthrough SSE stream error: %v", reqID, copyErr)
		}
		return
	}

	h.metrics.inc(strconv.Itoa(resp.StatusCode), "passthrough")

	if h.cfg.debug {
		log.Printf("[%s] passthrough done status=%d duration_ms=%d", reqID, resp.StatusCode, time.Since(start).Milliseconds())
	}
}

func (h *proxyHandler) proxyPassthroughStreamed(w http.ResponseWriter, r *http.Request, reqID string, providerType AccountType, provider Provider, targetBase *url.URL, accountHint *Account, start time.Time) {
	timeout := clientOrDefaultTimeout(r, h.cfg.requestTimeout, h.cfg.streamTimeout, nil)

	ctx := r.Context()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// Build the outgoing request - preserving the original Authorization header
	outURL := new(url.URL)
	*outURL = *r.URL
	outURL.Scheme = targetBase.Scheme
	outURL.Host = targetBase.Host
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, r.URL.Path, accountHint))

	var reqSample *bytes.Buffer
	var body io.Reader = r.Body
	if h.cfg.logBodies && h.cfg.bodyLogLimit > 0 {
		reqSample = &bytes.Buffer{}
		body = io.TeeReader(r.Body, &limitedWriter{w: reqSample, n: h.cfg.bodyLogLimit})
	}

	outReq, err := http.NewRequestWithContext(ctx, r.Method, outURL.String(), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	outReq.Host = targetBase.Host
	outReq.Header = cloneHeader(r.Header)
	removeHopByHopHeaders(outReq.Header)
	removeConflictingProxyHeaders(outReq.Header)
	stripLocalTraceHeaders(outReq.Header)
	if r.ContentLength >= 0 {
		outReq.ContentLength = r.ContentLength
	}

	// For Claude, ensure required headers are set
	if providerType == AccountTypeClaude {
		if outReq.Header.Get("anthropic-version") == "" {
			outReq.Header.Set("anthropic-version", "2023-06-01")
		}
	}

	if h.cfg.debug {
		log.Printf("[%s] passthrough streamed -> %s %s", reqID, outReq.Method, outReq.URL.String())
	}

	resp, err := h.transport.RoundTrip(outReq)
	if err != nil {
		h.recent.add(err.Error())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if h.cfg.logBodies && reqSample != nil && reqSample.Len() > 0 {
		log.Printf("[%s] passthrough request body sample (%d bytes): %s", reqID, reqSample.Len(), safeText(reqSample.Bytes()))
	}

	// Write response to client
	copyHeader(w.Header(), resp.Header)
	removeHopByHopHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	respContentType := resp.Header.Get("Content-Type")
	isSSE := provider.DetectsSSE(r.URL.Path, respContentType)

	var writer io.Writer = w
	if isSSE && flusher != nil {
		fw := &flushWriter{w: w, f: flusher, flushInterval: h.cfg.flushInterval}
		writer = fw
		defer fw.stop()
	}

	// Wrap response body with idle timeout to kill zombie SSE connections.
	var idleReader *idleTimeoutReader
	if isSSE && h.cfg.streamIdleTimeout > 0 {
		idleReader = newIdleTimeoutReader(resp.Body, h.cfg.streamIdleTimeout, cancel, func() {
			log.Printf("[%s] passthrough streamed SSE idle timeout after %v", reqID, h.cfg.streamIdleTimeout)
		})
		defer idleReader.Close()
	}

	if _, copyErr := io.Copy(writer, resp.Body); copyErr != nil {
		h.recent.add(copyErr.Error())
		h.metrics.inc("error", "passthrough")
		if idleReader != nil {
			log.Printf("[%s] passthrough streamed SSE error: %v", reqID, copyErr)
		}
		return
	}

	h.metrics.inc(strconv.Itoa(resp.StatusCode), "passthrough")

	if h.cfg.debug {
		log.Printf("[%s] passthrough streamed done status=%d duration_ms=%d", reqID, resp.StatusCode, time.Since(start).Milliseconds())
	}
}

func (h *proxyHandler) tryOnce(
	ctx context.Context,
	in *http.Request,
	bodyBytes []byte,
	routePlan RoutePlan,
	acc *Account,
	reqID string,
) (*http.Response, *bytes.Buffer, bool, error) { // Added refreshFailed return value
	if acc == nil {
		return nil, nil, false, errors.New("nil account")
	}
	trace := requestTraceFromContext(ctx)
	refreshFailed := false // Track if refresh was attempted but failed
	provider := routePlan.Provider
	targetBase := routePlan.TargetBase
	upstreamPath := routePlan.UpstreamPath

	if !h.cfg.disableRefresh && !skipPreemptiveRefreshForAccount(acc) && h.needsRefresh(acc) {
		if err := h.refreshAccount(ctx, acc); err != nil {
			if isRateLimitError(err) {
				h.applyRateLimit(acc, nil, defaultRateLimitBackoff)
			}
			if h.cfg.debug {
				log.Printf("[%s] refresh %s failed: %v (continuing with existing token)", reqID, acc.ID, err)
			}
		}
	}

	if !providerSupportsPathForAccount(provider, upstreamPath, acc) {
		return nil, nil, false, fmt.Errorf("account %s does not support path %s", acc.ID, upstreamPath)
	}
	if err := h.maybeProbeManagedCodexAPIKey(ctx, acc); err != nil {
		return nil, nil, false, err
	}

	facadeReq, err := maybeBuildGeminiCodeAssistFacadeRequest(ctx, provider, upstreamPath, bodyBytes, acc, reqID)
	if err != nil {
		return nil, nil, false, err
	}
	if err := h.maybePrimeGeminiCodeAssistFacade(ctx, acc, facadeReq); err != nil {
		return nil, nil, false, err
	}

	requestBody := bodyBytes
	if isGitLabCodexAccount(acc) {
		if rewritten, changed := coerceGitLabCodexRequestBody(requestBody); changed {
			requestBody = rewritten
			if h.cfg.debug {
				log.Printf("[%s] coerced gitlab codex request body text.verbosity -> medium (account=%s)", reqID, acc.ID)
			}
		}
	}
	targetBase = providerUpstreamURLForAccount(provider, upstreamPath, acc)
	targetBases := []*url.URL{targetBase}
	if facadeReq != nil {
		requestBody = facadeReq.body
		if len(facadeReq.targetBases) > 0 {
			targetBases = facadeReq.targetBases
		} else if facadeReq.targetBase != nil {
			targetBases = []*url.URL{facadeReq.targetBase}
		}
		targetBase = targetBases[0]
	}

	buildReq := func(targetBase *url.URL) (*http.Request, error) {
		outURL := new(url.URL)
		*outURL = *in.URL
		outURL.Scheme = targetBase.Scheme
		outURL.Host = targetBase.Host
		// Use provider's NormalizePath method for path handling
		outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(provider, upstreamPath, acc))
		if facadeReq != nil {
			outURL.Path = singleJoin(targetBase.Path, facadeReq.path)
		}

		// For Claude OAuth tokens, add beta=true query param (required for OAuth to work)
		if provider.Type() == AccountTypeClaude && strings.HasPrefix(acc.AccessToken, "sk-ant-oat") {
			q := outURL.Query()
			q.Set("beta", "true")
			outURL.RawQuery = q.Encode()
		}

		var body io.Reader
		if len(requestBody) > 0 {
			body = bytes.NewReader(requestBody)
		}
		outReq, err := http.NewRequestWithContext(ctx, in.Method, outURL.String(), body)
		if err != nil {
			return nil, err
		}

		outReq.Host = targetBase.Host
		outReq.Header = cloneHeader(in.Header)
		removeHopByHopHeaders(outReq.Header)
		removeConflictingProxyHeaders(outReq.Header)
		stripLocalTraceHeaders(outReq.Header)

		// Always overwrite client-provided auth; the proxy is the single source of truth.
		outReq.Header.Del("Authorization")
		outReq.Header.Del("ChatGPT-Account-ID")
		outReq.Header.Del("X-Api-Key") // Remove Claude API key from client (might be pool token)
		// Remove Gemini API key header (we use Bearer auth for pool accounts)
		outReq.Header.Del("x-goog-api-key")
		outReq.Header.Del(debugGeminiSeatHeader)

		acc.mu.Lock()
		access := acc.AccessToken
		acc.mu.Unlock()

		if access == "" {
			return nil, fmt.Errorf("account %s has empty access token", acc.ID)
		}

		// Use provider's SetAuthHeaders method for provider-specific auth
		provider.SetAuthHeaders(outReq, acc)
		maybeApplyOpencodeGeminiAnthropicHeaders(outReq.Header, routePlan.ResponseAdapter)
		if facadeReq != nil {
			outReq.Header.Set("Accept", "application/json")
			outReq.Header.Set("User-Agent", antigravityCodeAssistUA)
			if len(requestBody) > 0 {
				outReq.Header.Set("Content-Type", "application/json")
			}
		}

		// Debug: log ALL outgoing headers
		if h.cfg.debug {
			var hdrs []string
			for k, v := range outReq.Header {
				val := v[0]
				if len(val) > 80 {
					val = val[:80]
				}
				hdrs = append(hdrs, fmt.Sprintf("%s=%s", k, val))
			}
			log.Printf("[%s] ALL outgoing headers (%s): %v", reqID, provider.Type(), hdrs)
		}

		// Keep the original User-Agent from the client - don't override it
		return outReq, nil
	}

	var resp *http.Response
	var outReq *http.Request
	var candidateErr error
	for baseIdx, candidateBase := range targetBases {
		outReq, err = buildReq(candidateBase)
		if err != nil {
			return nil, nil, false, err
		}

		if h.cfg.debug {
			acc.mu.Lock()
			log.Printf("[%s] -> %s %s (account=%s account_id=%s)", reqID, outReq.Method, outReq.URL.String(), acc.ID, acc.AccountID)
			acc.mu.Unlock()
		}

		resp, err = h.transport.RoundTrip(outReq)
		if err != nil {
			if trace != nil {
				trace.noteTransportError("buffered_roundtrip", acc, err)
			}
			acc.mu.Lock()
			acc.Penalty += 0.2
			acc.mu.Unlock()
			if facadeReq != nil && baseIdx+1 < len(targetBases) {
				if trace != nil {
					trace.noteEvent(
						"gemini_code_assist_base_fallback",
						"account=%s base=%q next_base=%q reason=%q",
						acc.ID,
						candidateBase.String(),
						targetBases[baseIdx+1].String(),
						traceErrString(err),
					)
				}
				candidateErr = err
				continue
			}
			return nil, nil, false, err
		}

		// If we got a 401/403, try to refresh and retry on the *same* account once.
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && !h.cfg.disableRefresh {
			// Log the error response body for debugging
			if h.cfg.debug {
				errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				// Try to decompress if gzip
				decompressed := bodyForInspection(nil, errBody)
				log.Printf("[%s] got %d from upstream, body: %s", reqID, resp.StatusCode, safeText(decompressed))
			}
			acc.mu.Lock()
			hasRefresh := acc.RefreshToken != ""
			acc.mu.Unlock()
			if hasRefresh {
				_ = resp.Body.Close()
				if err := h.refreshAccountForced(ctx, acc); err == nil {
					outReq, err = buildReq(candidateBase)
					if err != nil {
						return nil, nil, false, err
					}
					if h.cfg.debug {
						acc.mu.Lock()
						log.Printf("[%s] retry after refresh -> %s %s (account=%s account_id=%s)", reqID, outReq.Method, outReq.URL.String(), acc.ID, acc.AccountID)
						acc.mu.Unlock()
					}
					resp, err = h.transport.RoundTrip(outReq)
					if err != nil {
						if trace != nil {
							trace.noteTransportError("buffered_roundtrip_after_refresh", acc, err)
						}
						acc.mu.Lock()
						acc.Penalty += 0.2
						acc.mu.Unlock()
						if facadeReq != nil && baseIdx+1 < len(targetBases) {
							if trace != nil {
								trace.noteEvent(
									"gemini_code_assist_base_fallback",
									"account=%s base=%q next_base=%q reason=%q",
									acc.ID,
									candidateBase.String(),
									targetBases[baseIdx+1].String(),
									traceErrString(err),
								)
							}
							candidateErr = err
							continue
						}
						return nil, nil, false, err
					}
					// Log response after retry
					if h.cfg.debug && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
						errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
						decompressed := bodyForInspection(nil, errBody)
						log.Printf("[%s] after refresh retry got %d, body: %s", reqID, resp.StatusCode, safeText(decompressed))
						// Recreate body for downstream processing
						resp.Body = io.NopCloser(bytes.NewReader(errBody))
					}
					// Refresh succeeded - if we still get 401/403 after refresh,
					// the account is truly dead (fresh token still rejected)
				} else {
					errStr := err.Error()
					if isRateLimitError(err) {
						h.applyRateLimit(acc, nil, defaultRateLimitBackoff)
					} else if isCodexRefreshTokenInvalidError(err) {
						if acc.Type == AccountTypeCodex {
							probeCtx, cancel := context.WithTimeout(ctx, codexModelsFetchTimeout)
							probe, probeErr := h.probeCodexCurrentAccess(probeCtx, acc)
							cancel()
							now := time.Now().UTC()
							acc.mu.Lock()
							if probeErr == nil {
								applyCodexRefreshInvalidProbeResultLocked(acc, now, probe, codexRefreshInvalidHealthError)
							} else {
								markCodexRefreshInvalidStateLocked(acc, now, codexRefreshInvalidHealthError, false)
							}
							acc.mu.Unlock()
							if saveErr := saveAccount(acc); saveErr != nil {
								log.Printf("[%s] warning: failed to persist codex account %s after refresh-invalid probe: %v", reqID, acc.ID, saveErr)
							}
							if probeErr != nil {
								log.Printf("[%s] codex current access probe after refresh failure for %s failed: %v", reqID, acc.ID, probeErr)
							} else {
								log.Printf("[%s] codex current access probe after refresh failure for %s: status=%d working=%v mark_dead=%v reason=%q", reqID, acc.ID, probe.StatusCode, probe.Working, probe.MarkDead, probe.Reason)
							}
						} else {
							now := time.Now().UTC()
							acc.mu.Lock()
							markAccountDeadWithReasonLocked(acc, now, 100.0, codexRefreshInvalidHealthError)
							acc.mu.Unlock()
							log.Printf("[%s] marking account %s as dead: %s", reqID, acc.ID, codexRefreshInvalidHealthError)
							if err := saveAccount(acc); err != nil {
								log.Printf("[%s] warning: failed to save dead account %s: %v", reqID, acc.ID, err)
							}
						}
						refreshFailed = true
					} else if !strings.Contains(errStr, "rate limited") {
						// Other non-rate-limited failures also count as refresh failed
						refreshFailed = true
					}
					if h.cfg.debug {
						log.Printf("[%s] refresh failed for %s: %v (refreshFailed=%v)", reqID, acc.ID, err, refreshFailed)
					}
				}
			} else {
				// No refresh token available - can't recover from 401/403
				refreshFailed = true
			}
		}

		if facadeReq != nil && baseIdx+1 < len(targetBases) && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError) {
			if trace != nil {
				trace.noteEvent(
					"gemini_code_assist_base_fallback",
					"account=%s base=%q next_base=%q status=%d",
					acc.ID,
					candidateBase.String(),
					targetBases[baseIdx+1].String(),
					resp.StatusCode,
				)
			}
			_ = resp.Body.Close()
			candidateErr = fmt.Errorf("gemini code assist endpoint %s returned status %d", candidateBase.String(), resp.StatusCode)
			continue
		}

		if facadeReq != nil {
			if err := maybeTransformGeminiCodeAssistFacadeResponse(upstreamPath, resp); err != nil {
				_ = resp.Body.Close()
				return nil, nil, false, err
			}
		}
		if err := maybeTransformOpenAIChatCompletionsGeminiResponse(routePlan.ResponseAdapter, routePlan.Shape.RequestedModel, resp); err != nil {
			_ = resp.Body.Close()
			return nil, nil, false, err
		}
		if err := maybeTransformAnthropicMessagesGeminiResponse(routePlan.ResponseAdapter, routePlan.Shape.RequestedModel, resp); err != nil {
			_ = resp.Body.Close()
			return nil, nil, false, err
		}

		// Always tee a bounded sample of response body for usage extraction and conversation pinning.
		sampleLimit := int64(16 * 1024)
		if h.cfg.logBodies && h.cfg.bodyLogLimit > 0 {
			sampleLimit = h.cfg.bodyLogLimit
		}
		buf := &bytes.Buffer{}
		resp.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.TeeReader(resp.Body, &limitedWriter{w: buf, n: sampleLimit}),
			Closer: resp.Body,
		}
		return resp, buf, refreshFailed, nil
	}
	if candidateErr != nil {
		return nil, nil, false, candidateErr
	}
	return nil, nil, false, fmt.Errorf("gemini code assist request had no candidate endpoints")
}

func (h *proxyHandler) needsRefresh(a *Account) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if (isGitLabClaudeAccount(a) || isGitLabCodexAccount(a)) && (strings.TrimSpace(a.AccessToken) == "" || len(a.ExtraHeaders) == 0) {
		return true
	}
	if a.RefreshToken == "" {
		return false
	}
	now := time.Now()

	// Per-account rate limiting: don't refresh too frequently
	// This prevents hammering the OAuth endpoint when refresh tokens are invalid
	if !a.LastRefresh.IsZero() && now.Sub(a.LastRefresh) < refreshPerAccountInterval {
		return false
	}

	// Only refresh if token is ACTUALLY expired (not "about to expire")
	// This is more conservative - we only refresh when we know the token won't work
	if !a.ExpiresAt.IsZero() && a.ExpiresAt.Before(now) {
		return true
	}
	// If no expiry time known, refresh after 12 hours since last refresh
	if a.ExpiresAt.IsZero() && !a.LastRefresh.IsZero() && now.Sub(a.LastRefresh) > 12*time.Hour {
		return true
	}
	return false
}

func skipPreemptiveRefreshForAccount(a *Account) bool {
	return canRouteValidationBlockedAntigravityGemini(a)
}

// refreshMinInterval is the minimum time between ANY refresh attempts globally
const refreshMinInterval = 5 * time.Second

// refreshPerAccountInterval is the minimum time between refresh attempts for a single account
// This is persisted to disk and survives restarts, preventing hammering OAuth endpoints
// 15 minutes balances between preventing hammering and allowing recovery from expired tokens
const refreshPerAccountInterval = 15 * time.Minute

const defaultRateLimitBackoff = 30 * time.Second

func shouldPersistFailedRefreshAttempt(err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "no configured gemini oauth client"):
		return false
	case strings.Contains(msg, "client_secret is missing"):
		return false
	case strings.Contains(msg, "invalid_client"):
		return false
	default:
		return true
	}
}

func (h *proxyHandler) refreshAccount(ctx context.Context, a *Account) error {
	return h.refreshAccountWithPolicy(ctx, a, false)
}

func (h *proxyHandler) refreshAccountForced(ctx context.Context, a *Account) error {
	return h.refreshAccountWithPolicy(ctx, a, true)
}

func (h *proxyHandler) refreshAccountWithPolicy(ctx context.Context, a *Account, force bool) error {
	if a == nil {
		return errors.New("nil account")
	}
	key := fmt.Sprintf("%s:%s", a.Type, a.ID)

	h.refreshCallsMu.Lock()
	if h.refreshCalls == nil {
		h.refreshCalls = map[string]*refreshCall{}
	}
	if existing, ok := h.refreshCalls[key]; ok {
		h.refreshCallsMu.Unlock()
		<-existing.done
		return existing.err
	}
	call := &refreshCall{done: make(chan struct{})}
	h.refreshCalls[key] = call
	h.refreshCallsMu.Unlock()

	defer func() {
		h.refreshCallsMu.Lock()
		delete(h.refreshCalls, key)
		h.refreshCallsMu.Unlock()
		close(call.done)
	}()

	err := h.refreshAccountOnce(ctx, a, force)
	call.err = err
	return err
}

func (h *proxyHandler) refreshAccountOnce(ctx context.Context, a *Account, force bool) error {
	// Per-account rate limiting (persisted to disk via LastRefresh)
	a.mu.Lock()
	sinceLastRefresh := time.Since(a.LastRefresh)
	skipPerAccountThrottle := accountAuthMode(a) == accountAuthModeGitLab
	if !force && !skipPerAccountThrottle && !a.LastRefresh.IsZero() && sinceLastRefresh < refreshPerAccountInterval {
		a.mu.Unlock()
		return fmt.Errorf("account refresh rate limited (%s), wait %v", a.ID, refreshPerAccountInterval-sinceLastRefresh)
	}
	accType := a.Type
	a.mu.Unlock()

	// Global rate limit - max 1 refresh globally every 5 seconds
	h.refreshMu.Lock()
	elapsed := time.Since(h.lastRefreshTime)
	if elapsed < refreshMinInterval {
		h.refreshMu.Unlock()
		return fmt.Errorf("refresh rate limited, wait %v", refreshMinInterval-elapsed)
	}
	h.lastRefreshTime = time.Now()
	h.refreshMu.Unlock()

	// Use the provider's RefreshToken method
	provider := h.registry.ForType(accType)
	if provider == nil {
		return fmt.Errorf("no provider for account type %s", accType)
	}
	err := provider.RefreshToken(ctx, a, h.refreshTransport)

	if err == nil || shouldPersistFailedRefreshAttempt(err) {
		a.mu.Lock()
		a.LastRefresh = time.Now().UTC()
		a.mu.Unlock()
	}

	// Always save to disk after refresh (success or failure)
	// - On success: persist the new access token
	// - On failure: persist LastRefresh only for retry-worthy failures
	if saveErr := saveAccount(a); saveErr != nil {
		log.Printf("warning: failed to save account %s after refresh: %v", a.ID, saveErr)
	}

	return err
}

// Note: Account refresh logic is now in the provider files:
// - provider_codex.go: CodexProvider.RefreshToken
// - provider_claude.go: ClaudeProvider.RefreshToken
// - provider_gemini.go: GeminiProvider.RefreshToken

// Note: Usage tracking functions are now in usage_tracking.go:
// - startUsagePoller, refreshUsageIfStale, fetchUsage, buildWhamUsageURL
// - DailyBreakdownDay, fetchDailyBreakdownData, replaceUsageHeaders

func (h *proxyHandler) updateUsageFromBody(provider Provider, a *Account, userID string, headerPrimaryPct, headerSecondaryPct float64, sample []byte) {
	if provider == nil || a == nil || len(sample) == 0 {
		return
	}
	lines := bytes.Split(sample, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		}
		if bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		obj, ok := parseUsageEventObject(line)
		if !ok {
			continue
		}

		delta := UsageDelta{Usage: provider.ParseUsage(obj)}
		if provider.Type() == AccountTypeCodex {
			delta = parseCodexUsageDelta(obj)
		}
		if delta.Snapshot != nil {
			applyUsageSnapshot(a, delta.Snapshot)
			persistUsageSnapshot(h.store, a)
		}
		if delta.Usage != nil {
			h.recordUsage(a, *enrichUsageRecord(a, userID, delta.Usage, headerPrimaryPct, headerSecondaryPct))
		}
	}
}
