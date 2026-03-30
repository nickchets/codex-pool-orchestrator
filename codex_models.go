package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	codexStartupWarmTimeout = 30 * time.Second
	codexModelsFreshTTL     = time.Hour
	codexModelsMaxStaleTTL  = 24 * time.Hour
	codexModelsFetchTimeout = 10 * time.Second
	codexClientVersion      = "0.106.0"

	gitLabCodexModelsFreshTTL     = 30 * time.Minute
	gitLabCodexModelsMaxStaleTTL  = 12 * time.Hour
	gitLabCodexModelsFetchTimeout = 45 * time.Second
	gitLabCodexModelProbeTimeout  = 8 * time.Second
)

type gitLabCodexModelSpec struct {
	Slug                  string
	DisplayName           string
	Description           string
	DefaultReasoningLevel string
	DefaultVerbosity      string
	Priority              int
	SupportsParallelTools bool
	SupportsReasoning     bool
	SupportsSearchTool    bool
	SupportsImageDetail   bool
}

var defaultGitLabCodexModelSpecs = []gitLabCodexModelSpec{
	{
		Slug:                  "gpt-5-codex",
		DisplayName:           "GPT-5 Codex",
		Description:           "GitLab backed Codex sidecar model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              1,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5.3-codex",
		DisplayName:           "GPT-5.3 Codex",
		Description:           "Discovered GitLab backed Codex model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              2,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5.2-codex",
		DisplayName:           "GPT-5.2 Codex",
		Description:           "Discovered GitLab backed Codex model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              3,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5",
		DisplayName:           "GPT-5",
		Description:           "Discovered GitLab backed general GPT-5 model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              4,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5.4-mini",
		DisplayName:           "GPT-5.4 Mini",
		Description:           "Discovered GitLab backed compact GPT-5.4 model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              5,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5.4-nano",
		DisplayName:           "GPT-5.4 Nano",
		Description:           "Discovered GitLab backed ultra-compact GPT-5.4 model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              6,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5.4",
		DisplayName:           "GPT-5.4",
		Description:           "Discovered GitLab backed GPT-5.4 model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              7,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5.2",
		DisplayName:           "GPT-5.2",
		Description:           "Discovered GitLab backed GPT-5.2 model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              8,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5-mini",
		DisplayName:           "GPT-5 Mini",
		Description:           "Discovered GitLab backed compact GPT-5 model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              9,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
	{
		Slug:                  "gpt-5-nano",
		DisplayName:           "GPT-5 Nano",
		Description:           "Discovered GitLab backed ultra-compact GPT-5 model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              10,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	},
}

type codexModelsCacheEntry struct {
	Body        []byte
	ContentType string
	FetchedAt   time.Time
}

type codexModelsCache struct {
	mu    sync.RWMutex
	entry codexModelsCacheEntry
}

func isCodexModelsRequest(r *http.Request) bool {
	return r != nil && r.Method == http.MethodGet && isCodexModelsPath(r.URL.Path)
}

func (c *codexModelsCache) load() (codexModelsCacheEntry, bool) {
	if c == nil {
		return codexModelsCacheEntry{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.entry.FetchedAt.IsZero() || len(c.entry.Body) == 0 {
		return codexModelsCacheEntry{}, false
	}
	entry := codexModelsCacheEntry{
		Body:        append([]byte(nil), c.entry.Body...),
		ContentType: c.entry.ContentType,
		FetchedAt:   c.entry.FetchedAt,
	}
	return entry, true
}

func (c *codexModelsCache) store(entry codexModelsCacheEntry) {
	if c == nil || entry.FetchedAt.IsZero() || len(entry.Body) == 0 {
		return
	}
	c.mu.Lock()
	c.entry = codexModelsCacheEntry{
		Body:        append([]byte(nil), entry.Body...),
		ContentType: entry.ContentType,
		FetchedAt:   entry.FetchedAt,
	}
	c.mu.Unlock()
}

func (h *proxyHandler) codexWarmState(now time.Time) (bool, int, int) {
	if h == nil || h.pool == nil {
		return true, 0, 0
	}

	h.pool.mu.RLock()
	accs := append([]*Account{}, h.pool.accounts...)
	h.pool.mu.RUnlock()

	total := 0
	warmed := 0
	for _, a := range accs {
		if a == nil || a.Type != AccountTypeCodex || isManagedCodexAPIKeyAccount(a) || isGitLabCodexAccount(a) {
			continue
		}
		a.mu.Lock()
		disabled := a.Disabled
		dead := a.Dead
		hasToken := a.AccessToken != ""
		warm := !a.Usage.RetrievedAt.IsZero()
		a.mu.Unlock()
		if disabled || dead || !hasToken {
			continue
		}
		total++
		if warm {
			warmed++
		}
	}

	if total == 0 || warmed == total {
		return true, 0, total
	}
	if now.Sub(h.startTime) >= codexStartupWarmTimeout {
		return true, total - warmed, total
	}
	return false, total - warmed, total
}

func (h *proxyHandler) ensureCodexRouteReady(w http.ResponseWriter, reqID string, routePlan RoutePlan, trace *requestTrace) bool {
	if h == nil || routePlan.AccountType != AccountTypeCodex {
		return true
	}
	ready, missing, total := h.codexWarmState(time.Now())
	if ready {
		return true
	}
	if h.cfg.debug {
		log.Printf("[%s] blocking codex request during warm-up: missing_usage=%d/%d", reqID, missing, total)
	}
	trace.noteEvent("route_gate", "provider=%s result=blocked reason=warmup missing_usage=%d total=%d", AccountTypeCodex, missing, total)
	w.Header().Set("Retry-After", "5")
	http.Error(w, fmt.Sprintf("codex pool warming up (%d/%d seats still missing usage state); retry shortly", missing, total), http.StatusServiceUnavailable)
	return false
}

func (h *proxyHandler) maybeServeCachedCodexModels(w http.ResponseWriter, r *http.Request, reqID string, admission AdmissionResult) bool {
	if !isCodexModelsRequest(r) || admission.Kind != AdmissionKindPoolUser {
		return false
	}
	trace := requestTraceFromContext(r.Context())

	shape := RequestShape{Path: r.URL.Path}
	routePlan, _, err := h.planRoute(admission, r, shape, nil)
	if err != nil {
		trace.noteCacheDecision(AccountTypeCodex, nil, "plan_error", 0, 0, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return true
	}
	if codexRequiresGitLabPlan(routePlan.RequiredPlan) {
		h.serveGitLabCodexModelsCache(w, r, reqID, routePlan, trace)
		return true
	}

	now := time.Now()
	if cached, ok := h.codexModels.load(); ok {
		age := now.Sub(cached.FetchedAt)
		if age < codexModelsFreshTTL {
			trace.noteCacheDecision(AccountTypeCodex, nil, "hit", age, 0, nil)
			writeCodexModelsCacheResponse(w, cached, "hit")
			return true
		}
	}

	refreshed, refreshErr := h.fetchCodexModels(r, reqID, routePlan)
	if refreshErr == nil {
		h.codexModels.store(refreshed)
		writeCodexModelsCacheResponse(w, refreshed, "refresh")
		return true
	}

	if cached, ok := h.codexModels.load(); ok && now.Sub(cached.FetchedAt) < codexModelsMaxStaleTTL {
		trace.noteCacheDecision(AccountTypeCodex, nil, "stale", now.Sub(cached.FetchedAt), 0, refreshErr)
		if h.cfg.debug {
			log.Printf("[%s] serving stale codex models cache after refresh error: %v", reqID, refreshErr)
		}
		writeCodexModelsCacheResponse(w, cached, "stale")
		return true
	}

	http.Error(w, refreshErr.Error(), http.StatusBadGateway)
	return true
}

func (h *proxyHandler) serveGitLabCodexModelsCache(w http.ResponseWriter, r *http.Request, reqID string, routePlan RoutePlan, trace *requestTrace) {
	now := time.Now()
	if cached, ok := h.gitlabCodexModels.load(); ok {
		age := now.Sub(cached.FetchedAt)
		if age < gitLabCodexModelsFreshTTL {
			trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_hit", age, 0, nil)
			writeCodexModelsCacheResponse(w, cached, "gitlab-hit")
			return
		}
	}

	refreshed, refreshErr := h.fetchGitLabCodexModels(r, reqID, routePlan)
	if refreshErr == nil {
		h.gitlabCodexModels.store(refreshed)
		writeCodexModelsCacheResponse(w, refreshed, "gitlab-refresh")
		return
	}

	if cached, ok := h.gitlabCodexModels.load(); ok && now.Sub(cached.FetchedAt) < gitLabCodexModelsMaxStaleTTL {
		trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_stale", now.Sub(cached.FetchedAt), 0, refreshErr)
		if h.cfg.debug {
			log.Printf("[%s] serving stale gitlab codex models cache after refresh error: %v", reqID, refreshErr)
		}
		writeCodexModelsCacheResponse(w, cached, "gitlab-stale")
		return
	}

	trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_fallback", 0, 0, refreshErr)
	if h.cfg.debug {
		log.Printf("[%s] falling back to minimal gitlab codex catalog after discovery error: %v", reqID, refreshErr)
	}
	writeCodexModelsCacheResponse(w, buildSyntheticGitLabCodexModelsEntry(), "gitlab-fallback")
}

func (h *proxyHandler) fetchGitLabCodexModels(r *http.Request, reqID string, routePlan RoutePlan) (codexModelsCacheEntry, error) {
	trace := requestTraceFromContext(r.Context())
	startedAt := time.Now()
	if h == nil || h.pool == nil {
		err := fmt.Errorf("gitlab codex models discovery unavailable")
		trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}
	provider, ok := routePlan.Provider.(*CodexProvider)
	if !ok || provider == nil {
		err := fmt.Errorf("gitlab codex models provider unavailable")
		trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}

	specs := gitLabCodexDiscoverySpecs(h.cfg.gitLabCodexDiscoveryModels)
	seats := h.gitLabCodexDiscoverySeats()
	if len(seats) == 0 {
		err := fmt.Errorf("no live gitlab codex seats for model discovery")
		trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}

	ctx, cancel := context.WithTimeout(r.Context(), gitLabCodexModelsFetchTimeout)
	defer cancel()

	discovered, err := h.discoverGitLabCodexModels(ctx, provider, seats, specs)
	if err != nil {
		trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}

	trace.noteCacheDecision(AccountTypeCodex, nil, "gitlab_refresh", 0, time.Since(startedAt), nil)
	return buildGitLabCodexModelsEntry(discovered), nil
}

func (h *proxyHandler) gitLabCodexDiscoverySeats() []*Account {
	if h == nil || h.pool == nil {
		return nil
	}
	type seatSnapshot struct {
		acc         *Account
		id          string
		healthy     bool
		hasGateway  bool
		lastHealthy time.Time
	}

	now := time.Now()
	h.pool.mu.RLock()
	accs := append([]*Account{}, h.pool.accounts...)
	h.pool.mu.RUnlock()

	healthySeats := make([]seatSnapshot, 0, len(accs))
	fallbackSeats := make([]seatSnapshot, 0, len(accs))
	for _, acc := range accs {
		if !isGitLabCodexAccount(acc) {
			continue
		}
		acc.mu.Lock()
		disabled := acc.Disabled
		dead := acc.Dead
		hasSource := strings.TrimSpace(acc.RefreshToken) != ""
		rateLimited := !acc.RateLimitUntil.IsZero() && acc.RateLimitUntil.After(now)
		healthy := strings.EqualFold(strings.TrimSpace(acc.HealthStatus), "healthy")
		hasGateway := !missingGitLabCodexGatewayState(acc)
		lastHealthy := acc.LastHealthyAt
		id := acc.ID
		acc.mu.Unlock()
		if disabled || dead || !hasSource || rateLimited {
			continue
		}
		snapshot := seatSnapshot{
			acc:         acc,
			id:          id,
			healthy:     healthy,
			hasGateway:  hasGateway,
			lastHealthy: lastHealthy,
		}
		if healthy {
			healthySeats = append(healthySeats, snapshot)
			continue
		}
		fallbackSeats = append(fallbackSeats, snapshot)
	}

	seats := healthySeats
	if len(seats) == 0 {
		seats = fallbackSeats
	}

	sort.SliceStable(seats, func(i, j int) bool {
		if seats[i].healthy != seats[j].healthy {
			return seats[i].healthy
		}
		if seats[i].hasGateway != seats[j].hasGateway {
			return seats[i].hasGateway
		}
		if !seats[i].lastHealthy.Equal(seats[j].lastHealthy) {
			return seats[i].lastHealthy.After(seats[j].lastHealthy)
		}
		return seats[i].id < seats[j].id
	})

	out := make([]*Account, 0, len(seats))
	for _, seat := range seats {
		out = append(out, seat.acc)
	}
	return out
}

func (h *proxyHandler) discoverGitLabCodexModels(ctx context.Context, provider *CodexProvider, seats []*Account, specs []gitLabCodexModelSpec) ([]gitLabCodexModelSpec, error) {
	remaining := append([]gitLabCodexModelSpec(nil), specs...)
	for _, seat := range seats {
		if err := h.ensureGitLabCodexDiscoverySeat(ctx, provider, seat); err != nil {
			return nil, fmt.Errorf("prepare seat %s: %w", seat.ID, err)
		}
		next := make([]gitLabCodexModelSpec, 0, len(remaining))
		for _, spec := range remaining {
			supported, err := h.probeGitLabCodexModel(ctx, provider, seat, spec)
			if err != nil {
				return nil, fmt.Errorf("probe %s on %s: %w", spec.Slug, seat.ID, err)
			}
			if supported {
				next = append(next, spec)
			}
		}
		remaining = next
		if len(remaining) == 0 {
			return nil, fmt.Errorf("gitlab codex discovery found no common supported models")
		}
	}
	return remaining, nil
}

func (h *proxyHandler) ensureGitLabCodexDiscoverySeat(ctx context.Context, provider *CodexProvider, acc *Account) error {
	if h == nil || provider == nil || acc == nil {
		return fmt.Errorf("gitlab codex discovery seat unavailable")
	}
	acc.mu.Lock()
	expiresSoon := !acc.ExpiresAt.IsZero() && time.Until(acc.ExpiresAt) <= time.Minute
	needsRefresh := missingGitLabCodexGatewayState(acc) || expiresSoon
	acc.mu.Unlock()
	if !needsRefresh {
		return nil
	}
	transport := h.refreshTransport
	if transport == nil {
		transport = h.transport
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	return provider.RefreshToken(ctx, acc, transport)
}

func (h *proxyHandler) probeGitLabCodexModel(ctx context.Context, provider *CodexProvider, acc *Account, spec gitLabCodexModelSpec) (bool, error) {
	if h == nil || provider == nil || acc == nil {
		return false, fmt.Errorf("gitlab codex probe unavailable")
	}
	targetBase := provider.UpstreamURLForAccount("/responses", acc)
	if targetBase == nil {
		return false, fmt.Errorf("missing gitlab upstream base")
	}
	outURL := *targetBase
	outURL.Path = singleJoin(targetBase.Path, provider.NormalizePathForAccount("/responses", acc))
	outURL.RawQuery = ""

	payload := map[string]any{
		"model":             spec.Slug,
		"input":             "Reply with exactly OK.",
		"max_output_tokens": 32,
		"store":             false,
	}
	if strings.Contains(spec.Slug, "codex") {
		payload["text"] = map[string]any{"verbosity": "medium"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, gitLabCodexModelProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, outURL.String(), strings.NewReader(string(body)))
	if err != nil {
		return false, err
	}
	req.Header = make(http.Header)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	removeConflictingProxyHeaders(req.Header)
	provider.SetAuthHeaders(req, acc)

	transport := h.transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	provider.ParseUsageHeaders(acc, resp.Header)
	persistUsageSnapshot(h.store, acc)

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return false, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	if gitLabCodexProbeUnsupportedModel(resp.StatusCode, responseBody) {
		return false, nil
	}
	if resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		disposition := classifyManagedGitLabCodexError(managedGitLabCodexErrorSourceGatewayRequest, resp.StatusCode, resp.Header, responseBody)
		applyManagedGitLabCodexDisposition(acc, disposition, resp.Header, time.Now())
		if saveErr := saveAccount(acc); saveErr != nil {
			log.Printf("warning: failed to save gitlab codex discovery seat %s after probe failure: %v", acc.ID, saveErr)
		}
	}
	return false, fmt.Errorf("gitlab codex model probe failed: %s", firstNonEmpty(strings.TrimSpace(safeText(responseBody)), resp.Status))
}

func gitLabCodexProbeUnsupportedModel(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(safeText(body)))
	return strings.Contains(lower, "unsupported model")
}

func gitLabCodexDiscoverySpecs(override []string) []gitLabCodexModelSpec {
	if len(override) == 0 {
		return append([]gitLabCodexModelSpec(nil), defaultGitLabCodexModelSpecs...)
	}
	index := make(map[string]gitLabCodexModelSpec, len(defaultGitLabCodexModelSpecs))
	for _, spec := range defaultGitLabCodexModelSpecs {
		index[spec.Slug] = spec
	}
	out := make([]gitLabCodexModelSpec, 0, len(override))
	seen := make(map[string]struct{}, len(override))
	for _, slug := range override {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		if spec, ok := index[slug]; ok {
			out = append(out, spec)
			continue
		}
		out = append(out, gitLabCodexGenericModelSpec(slug, len(out)+1))
	}
	if len(out) == 0 {
		return append([]gitLabCodexModelSpec(nil), defaultGitLabCodexModelSpecs...)
	}
	return out
}

func gitLabCodexGenericModelSpec(slug string, priority int) gitLabCodexModelSpec {
	display := strings.ToUpper(strings.ReplaceAll(slug, "-", " "))
	display = strings.ReplaceAll(display, "GPT ", "GPT-")
	display = strings.ReplaceAll(display, " CODEX", " Codex")
	display = strings.ReplaceAll(display, " MINI", " Mini")
	display = strings.ReplaceAll(display, " NANO", " Nano")
	return gitLabCodexModelSpec{
		Slug:                  slug,
		DisplayName:           display,
		Description:           "Discovered GitLab backed model.",
		DefaultReasoningLevel: "medium",
		DefaultVerbosity:      "medium",
		Priority:              priority,
		SupportsParallelTools: true,
		SupportsReasoning:     true,
		SupportsSearchTool:    true,
		SupportsImageDetail:   true,
	}
}

func buildSyntheticGitLabCodexModelsEntry() codexModelsCacheEntry {
	return buildGitLabCodexModelsEntry(defaultGitLabCodexModelSpecs[:1])
}

func buildGitLabCodexModelsEntry(specs []gitLabCodexModelSpec) codexModelsCacheEntry {
	supportedReasoningLevels := []map[string]string{
		{
			"effort":      "low",
			"description": "Fast responses with lighter reasoning",
		},
		{
			"effort":      "medium",
			"description": "Balanced reasoning depth",
		},
		{
			"effort":      "high",
			"description": "Deep reasoning for complex problems",
		},
	}
	if len(specs) == 0 {
		specs = defaultGitLabCodexModelSpecs[:1]
	}
	models := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		defaultReasoning := strings.TrimSpace(spec.DefaultReasoningLevel)
		if defaultReasoning == "" {
			defaultReasoning = "medium"
		}
		defaultVerbosity := strings.TrimSpace(spec.DefaultVerbosity)
		if defaultVerbosity == "" {
			defaultVerbosity = "medium"
		}
		models = append(models, map[string]any{
			"id":                           spec.Slug,
			"slug":                         spec.Slug,
			"display_name":                 spec.DisplayName,
			"owned_by":                     "gitlab_duo",
			"description":                  spec.Description,
			"apply_patch_tool_type":        "freeform",
			"availability_nux":             nil,
			"available_in_plans":           []string{"business", "edu", "education", "enterprise", "finserv", "go", "hc", "plus", "pro", "team"},
			"base_instructions":            "You are Codex. Work directly and pragmatically.",
			"context_window":               272000,
			"default_reasoning_level":      defaultReasoning,
			"default_reasoning_summary":    "none",
			"default_verbosity":            defaultVerbosity,
			"experimental_supported_tools": []string{},
			"input_modalities":             []string{"text", "image"},
			"minimal_client_version":       "0.98.0",
			"model_messages": map[string]any{
				"instructions_template": "You are Codex. {{ personality }}",
				"instructions_variables": map[string]string{
					"personality_default":   "",
					"personality_friendly":  "Friendly personality.",
					"personality_pragmatic": "Pragmatic personality.",
				},
			},
			"prefer_websockets":              false,
			"priority":                       spec.Priority,
			"reasoning_summary_format":       "experimental",
			"shell_type":                     "shell_command",
			"support_verbosity":              true,
			"supported_in_api":               true,
			"supported_reasoning_levels":     supportedReasoningLevels,
			"supports_image_detail_original": spec.SupportsImageDetail,
			"supports_parallel_tool_calls":   spec.SupportsParallelTools,
			"supports_reasoning_summaries":   spec.SupportsReasoning,
			"supports_search_tool":           spec.SupportsSearchTool,
			"truncation_policy": map[string]any{
				"limit": 10000,
				"mode":  "tokens",
			},
			"upgrade":              nil,
			"visibility":           "list",
			"web_search_tool_type": "text_and_image",
		})
	}
	body, err := json.Marshal(map[string]any{
		"models": models,
	})
	if err != nil {
		body = []byte(`{"models":[{"id":"gpt-5-codex","slug":"gpt-5-codex","display_name":"GPT-5 Codex","owned_by":"gitlab_duo","description":"GitLab backed Codex sidecar model.","apply_patch_tool_type":"freeform","availability_nux":null,"available_in_plans":["business","edu","education","enterprise","finserv","go","hc","plus","pro","team"],"base_instructions":"You are Codex. Work directly and pragmatically.","context_window":272000,"default_reasoning_level":"medium","default_reasoning_summary":"none","default_verbosity":"medium","experimental_supported_tools":[],"input_modalities":["text","image"],"minimal_client_version":"0.98.0","model_messages":{"instructions_template":"You are Codex. {{ personality }}","instructions_variables":{"personality_default":"","personality_friendly":"Friendly personality.","personality_pragmatic":"Pragmatic personality."}},"prefer_websockets":false,"priority":1,"reasoning_summary_format":"experimental","shell_type":"shell_command","support_verbosity":true,"supported_in_api":true,"supported_reasoning_levels":[{"effort":"low","description":"Fast responses with lighter reasoning"},{"effort":"medium","description":"Balanced reasoning depth"},{"effort":"high","description":"Deep reasoning for complex problems"}],"supports_image_detail_original":true,"supports_parallel_tool_calls":true,"supports_reasoning_summaries":true,"supports_search_tool":true,"truncation_policy":{"limit":10000,"mode":"tokens"},"upgrade":null,"visibility":"list","web_search_tool_type":"text_and_image"}]}`)
	}
	return codexModelsCacheEntry{
		Body:        body,
		ContentType: "application/json",
		FetchedAt:   time.Now(),
	}
}

func writeCodexModelsCacheResponse(w http.ResponseWriter, entry codexModelsCacheEntry, cacheState string) {
	if entry.ContentType == "" {
		entry.ContentType = "application/json"
	}
	w.Header().Set("Content-Type", entry.ContentType)
	w.Header().Set("X-Codex-Models-Cache", cacheState)
	if !entry.FetchedAt.IsZero() {
		w.Header().Set("X-Codex-Models-Fetched-At", entry.FetchedAt.UTC().Format(time.RFC3339))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Body)
}

func ensureCodexModelsQueryDefaults(u *url.URL) {
	if u == nil {
		return
	}
	q := u.Query()
	if q.Get("client_version") != "" {
		return
	}
	q.Set("client_version", codexClientVersion)
	u.RawQuery = q.Encode()
}

func (h *proxyHandler) fetchCodexModels(r *http.Request, reqID string, routePlan RoutePlan) (codexModelsCacheEntry, error) {
	trace := requestTraceFromContext(r.Context())
	startedAt := time.Now()
	if h == nil || h.pool == nil || routePlan.Provider == nil {
		err := fmt.Errorf("codex models fetch unavailable")
		trace.noteCacheDecision(AccountTypeCodex, nil, "refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}

	acc, err := h.candidateSupportingPath("", nil, AccountTypeCodex, routePlan.RequiredPlan, routePlan.Provider, r.URL.Path, "", "")
	if err != nil {
		trace.noteCacheDecision(AccountTypeCodex, nil, "refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}
	if acc == nil || isManagedCodexAPIKeyAccount(acc) || isGitLabCodexAccount(acc) {
		err := fmt.Errorf("no live local codex accounts for models metadata")
		trace.noteCacheDecision(AccountTypeCodex, acc, "refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}

	ctx, cancel := context.WithTimeout(r.Context(), codexModelsFetchTimeout)
	defer cancel()

	targetBase := providerUpstreamURLForAccount(routePlan.Provider, r.URL.Path, acc)
	outURL := *r.URL
	outURL.Scheme = targetBase.Scheme
	outURL.Host = targetBase.Host
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(routePlan.Provider, r.URL.Path, acc))
	ensureCodexModelsQueryDefaults(&outURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, outURL.String(), nil)
	if err != nil {
		trace.noteCacheDecision(AccountTypeCodex, acc, "refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}
	req.Header = cloneHeader(r.Header)
	req.Header.Del("Authorization")
	req.Header.Del("ChatGPT-Account-ID")
	req.Header.Del("X-Api-Key")
	req.Header.Del("x-goog-api-key")
	removeConflictingProxyHeaders(req.Header)
	routePlan.Provider.SetAuthHeaders(req, acc)

	resp, err := h.transport.RoundTrip(req)
	if err != nil {
		trace.noteCacheDecision(AccountTypeCodex, acc, "refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}
	defer resp.Body.Close()

	routePlan.Provider.ParseUsageHeaders(acc, resp.Header)
	persistUsageSnapshot(h.store, acc)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		trace.noteCacheDecision(AccountTypeCodex, acc, "refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("codex models upstream %s: %s", resp.Status, string(body))
		trace.noteCacheDecision(AccountTypeCodex, acc, "refresh_error", 0, time.Since(startedAt), err)
		return codexModelsCacheEntry{}, err
	}

	if h.poolHasGitLabCodexAccounts() {
		body = withGitLabCodexModelAlias(body)
	}
	trace.noteCacheDecision(AccountTypeCodex, acc, "refresh", 0, time.Since(startedAt), nil)
	return codexModelsCacheEntry{
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		FetchedAt:   time.Now(),
	}, nil
}

func (h *proxyHandler) poolHasGitLabCodexAccounts() bool {
	if h == nil || h.pool == nil {
		return false
	}
	h.pool.mu.RLock()
	defer h.pool.mu.RUnlock()
	for _, acc := range h.pool.accounts {
		if isGitLabCodexAccount(acc) {
			return true
		}
	}
	return false
}

func withGitLabCodexModelAlias(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	collectionKey := ""
	rawModels, ok := payload["models"].([]any)
	if ok {
		collectionKey = "models"
	} else {
		rawModels, ok = payload["data"].([]any)
		if ok {
			collectionKey = "data"
		}
	}
	if collectionKey == "" {
		return body
	}

	aliasID := gitLabCodexAliasModel()
	var aliasExists bool
	var baseModel map[string]any
	for _, item := range rawModels {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		modelID := firstNonEmpty(
			strings.TrimSpace(stringValue(entry["slug"])),
			strings.TrimSpace(stringValue(entry["id"])),
		)
		switch modelID {
		case aliasID:
			aliasExists = true
		case "gpt-5-codex":
			baseModel = cloneJSONMap(entry)
		}
	}
	if aliasExists {
		return body
	}
	if baseModel == nil {
		baseModel = map[string]any{}
	}
	if _, hasSlug := baseModel["slug"]; hasSlug || collectionKey == "models" {
		baseModel["slug"] = aliasID
	}
	if _, hasID := baseModel["id"]; hasID || collectionKey == "data" {
		baseModel["id"] = aliasID
	}
	if _, ok := baseModel["owned_by"]; !ok {
		baseModel["owned_by"] = "gitlab_duo"
	}
	payload[collectionKey] = append(rawModels, baseModel)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}

func cloneJSONMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}
