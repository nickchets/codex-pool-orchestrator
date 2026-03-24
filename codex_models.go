package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	codexStartupWarmTimeout = 30 * time.Second
	codexModelsFreshTTL     = time.Hour
	codexModelsMaxStaleTTL  = 24 * time.Hour
	codexModelsFetchTimeout = 10 * time.Second
)

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
	return r != nil && r.Method == http.MethodGet && r.URL.Path == "/backend-api/codex/models"
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
		if a == nil || a.Type != AccountTypeCodex || isManagedCodexAPIKeyAccount(a) {
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

func (h *proxyHandler) ensureCodexRouteReady(w http.ResponseWriter, reqID string, routePlan RoutePlan) bool {
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
	w.Header().Set("Retry-After", "5")
	http.Error(w, fmt.Sprintf("codex pool warming up (%d/%d seats still missing usage state); retry shortly", missing, total), http.StatusServiceUnavailable)
	return false
}

func (h *proxyHandler) maybeServeCachedCodexModels(w http.ResponseWriter, r *http.Request, reqID string, admission AdmissionResult) bool {
	if !isCodexModelsRequest(r) || admission.Kind != AdmissionKindPoolUser {
		return false
	}

	shape := RequestShape{Path: r.URL.Path}
	routePlan, _, err := h.planRoute(admission, r, shape, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return true
	}

	now := time.Now()
	if cached, ok := h.codexModels.load(); ok {
		age := now.Sub(cached.FetchedAt)
		if age < codexModelsFreshTTL {
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
		if h.cfg.debug {
			log.Printf("[%s] serving stale codex models cache after refresh error: %v", reqID, refreshErr)
		}
		writeCodexModelsCacheResponse(w, cached, "stale")
		return true
	}

	http.Error(w, refreshErr.Error(), http.StatusBadGateway)
	return true
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

func (h *proxyHandler) fetchCodexModels(r *http.Request, reqID string, routePlan RoutePlan) (codexModelsCacheEntry, error) {
	if h == nil || h.pool == nil || routePlan.Provider == nil {
		return codexModelsCacheEntry{}, fmt.Errorf("codex models fetch unavailable")
	}

	acc := h.pool.peekCandidate(AccountTypeCodex, routePlan.RequiredPlan)
	if acc == nil || isManagedCodexAPIKeyAccount(acc) {
		return codexModelsCacheEntry{}, fmt.Errorf("no live local codex accounts for models metadata")
	}
	if !providerSupportsPathForAccount(routePlan.Provider, r.URL.Path, acc) {
		return codexModelsCacheEntry{}, fmt.Errorf("account %s does not support models metadata path", acc.ID)
	}

	ctx, cancel := context.WithTimeout(r.Context(), codexModelsFetchTimeout)
	defer cancel()

	targetBase := providerUpstreamURLForAccount(routePlan.Provider, r.URL.Path, acc)
	outURL := *r.URL
	outURL.Scheme = targetBase.Scheme
	outURL.Host = targetBase.Host
	outURL.Path = singleJoin(targetBase.Path, providerNormalizePathForAccount(routePlan.Provider, r.URL.Path, acc))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, outURL.String(), nil)
	if err != nil {
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
		return codexModelsCacheEntry{}, err
	}
	defer resp.Body.Close()

	routePlan.Provider.ParseUsageHeaders(acc, resp.Header)
	persistUsageSnapshot(h.store, acc)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return codexModelsCacheEntry{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codexModelsCacheEntry{}, fmt.Errorf("codex models upstream %s: %s", resp.Status, string(body))
	}

	return codexModelsCacheEntry{
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		FetchedAt:   time.Now(),
	}, nil
}
