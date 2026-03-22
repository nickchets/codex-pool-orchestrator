package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stubCodexLoopbackEnsure(t *testing.T) {
	t.Helper()
	previous := ensureCodexLoopbackCallbackServersForOperator
	ensureCodexLoopbackCallbackServersForOperator = func(h *proxyHandler) error { return nil }
	t.Cleanup(func() {
		ensureCodexLoopbackCallbackServersForOperator = previous
	})
}

func testCodexIDToken(t *testing.T, userID, accountID, email, subject string, exp time.Time) string {
	t.Helper()
	payload := map[string]any{
		"exp": exp.Unix(),
		"sub": subject,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_user_id":    userID,
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  "team",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": email,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal id token payload: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func TestBuildPoolDashboardDataGroupsWorkspaceSeats(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	blocked := &Account{
		ID:        "blocked",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.95,
			SecondaryResetAt:     now.Add(90 * time.Minute),
		},
	}
	healthySibling := &Account{
		ID:        "healthy-sibling",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-b", "workspace-a", "b@example.com", "sub-b", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.12,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthyOther := &Account{
		ID:        "healthy-other",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-b",
		IDToken:   testCodexIDToken(t, "user-c", "workspace-b", "c@example.com", "sub-c", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.30,
			SecondaryUsedPercent: 0.25,
			SecondaryResetAt:     now.Add(48 * time.Hour),
		},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{blocked, healthySibling, healthyOther}, false),
		startTime: now.Add(-2 * time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.PoolSummary.TotalAccounts != 3 {
		t.Fatalf("total_accounts=%d", data.PoolSummary.TotalAccounts)
	}
	if data.PoolSummary.EligibleAccounts != 2 {
		t.Fatalf("eligible_accounts=%d", data.PoolSummary.EligibleAccounts)
	}
	if data.PoolSummary.WorkspaceCount != 2 {
		t.Fatalf("workspace_count=%d", data.PoolSummary.WorkspaceCount)
	}
	if data.PoolSummary.NextRecoveryAt == "" {
		t.Fatal("expected next_recovery_at to be populated")
	}

	var workspaceA *PoolDashboardWorkspaceGroup
	for i := range data.WorkspaceGroups {
		if data.WorkspaceGroups[i].WorkspaceID == "workspace-a" {
			workspaceA = &data.WorkspaceGroups[i]
			break
		}
	}
	if workspaceA == nil {
		t.Fatal("workspace-a group missing")
	}
	if workspaceA.SeatCount != 2 || workspaceA.EligibleSeatCount != 1 || workspaceA.BlockedSeatCount != 1 {
		t.Fatalf("workspace-a counts=%+v", *workspaceA)
	}
	if len(workspaceA.SeatKeys) != 2 {
		t.Fatalf("expected 2 seat keys, got %v", workspaceA.SeatKeys)
	}

	blockedAccount := data.Accounts[0]
	if blockedAccount.ID != "blocked" {
		t.Fatalf("expected blocked account to sort first, got %s", blockedAccount.ID)
	}
	if blockedAccount.Routing.BlockReason != "secondary_headroom_lt_10" {
		t.Fatalf("block_reason=%q", blockedAccount.Routing.BlockReason)
	}
	if blockedAccount.WorkspaceID != "workspace-a" {
		t.Fatalf("workspace_id=%q", blockedAccount.WorkspaceID)
	}
	if !strings.Contains(blockedAccount.SeatKey, "workspace-a") {
		t.Fatalf("seat_key=%q", blockedAccount.SeatKey)
	}
}

func TestBuildPoolDashboardDataTracksOpenAIAPIPool(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	seat := &Account{
		ID:        "healthy-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	apiKey := &Account{
		ID:              "openai_api_deadbeef",
		Type:            AccountTypeCodex,
		PlanType:        "api",
		AuthMode:        accountAuthModeAPIKey,
		HealthStatus:    "healthy",
		HealthCheckedAt: now.Add(-2 * time.Minute),
		LastHealthyAt:   now.Add(-2 * time.Minute),
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{seat, apiKey}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.CodexSeatCount != 1 {
		t.Fatalf("codex_seat_count=%d", data.CodexSeatCount)
	}
	if data.OpenAIAPIPool.TotalKeys != 1 {
		t.Fatalf("api total=%d", data.OpenAIAPIPool.TotalKeys)
	}
	if data.OpenAIAPIPool.HealthyKeys != 1 {
		t.Fatalf("api healthy=%d", data.OpenAIAPIPool.HealthyKeys)
	}
	if data.OpenAIAPIPool.EligibleKeys != 1 {
		t.Fatalf("api eligible=%d", data.OpenAIAPIPool.EligibleKeys)
	}
	if data.OpenAIAPIPool.NextKeyID != "openai_api_deadbeef" {
		t.Fatalf("next api key=%q", data.OpenAIAPIPool.NextKeyID)
	}
	if len(data.WorkspaceGroups) != 1 {
		t.Fatalf("workspace groups should exclude api keys, got %d", len(data.WorkspaceGroups))
	}
	if !data.Accounts[1].FallbackOnly {
		t.Fatalf("expected fallback_only account status, got %+v", data.Accounts[1])
	}
}

func TestBuildPoolDashboardDataSelectsCurrentSeatFromInflightAndLastUsed(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	current := &Account{
		ID:        "current-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Inflight:  2,
		LastUsed:  now.Add(-15 * time.Second),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.30,
			SecondaryUsedPercent: 0.20,
		},
	}
	older := &Account{
		ID:        "older-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-b",
		IDToken:   testCodexIDToken(t, "user-b", "workspace-b", "b@example.com", "sub-b", now.Add(4*time.Hour)),
		LastUsed:  now.Add(-2 * time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.10,
		},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{current, older}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.CurrentSeat == nil {
		t.Fatal("expected current_seat to be populated")
	}
	if data.CurrentSeat.ID != "current-seat" {
		t.Fatalf("current seat=%+v", data.CurrentSeat)
	}
	if data.ActiveSeat == nil || data.ActiveSeat.ID != "current-seat" {
		t.Fatalf("active_seat=%+v", data.ActiveSeat)
	}
	if data.ActiveSeat.Inflight != 2 {
		t.Fatalf("expected inflight=2, got %+v", data.ActiveSeat)
	}
	if data.ActiveSeat.ActiveSeatCount != 1 {
		t.Fatalf("expected active_seat_count=1, got %+v", data.ActiveSeat)
	}
	if !strings.Contains(data.ActiveSeat.Basis, "Live requests") {
		t.Fatalf("expected live-request basis, got %+v", data.ActiveSeat)
	}
	if data.LastUsedSeat != nil {
		t.Fatalf("expected last_used_seat to be omitted when it matches active_seat, got %+v", data.LastUsedSeat)
	}
	if data.BestEligibleSeat != nil {
		t.Fatalf("expected best_eligible_seat to be omitted when it matches active_seat, got %+v", data.BestEligibleSeat)
	}
}

func TestBuildPoolDashboardDataSeparatesLastUsedAndBestEligibleWhenIdle(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	lastUsedBlocked := &Account{
		ID:        "blocked-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		LastUsed:  now.Add(-15 * time.Second),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.91,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	healthy := &Account{
		ID:        "healthy-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-b",
		IDToken:   testCodexIDToken(t, "user-b", "workspace-b", "b@example.com", "sub-b", now.Add(4*time.Hour)),
		LastUsed:  now.Add(-2 * time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
		},
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{lastUsedBlocked, healthy}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.ActiveSeat != nil {
		t.Fatalf("expected no active_seat, got %+v", data.ActiveSeat)
	}
	if data.LastUsedSeat == nil || data.LastUsedSeat.ID != "blocked-seat" {
		t.Fatalf("last_used_seat=%+v", data.LastUsedSeat)
	}
	if data.LastUsedSeat.RoutingStatus != "secondary_headroom_lt_10" {
		t.Fatalf("last_used routing=%+v", data.LastUsedSeat)
	}
	if data.BestEligibleSeat == nil || data.BestEligibleSeat.ID != "healthy-seat" {
		t.Fatalf("best_eligible_seat=%+v", data.BestEligibleSeat)
	}
	if data.CurrentSeat == nil || data.CurrentSeat.ID != "healthy-seat" {
		t.Fatalf("current_seat=%+v", data.CurrentSeat)
	}
}

func TestBuildPoolDashboardDataPrefersCodexSeatPreviewBeforeFallbackAPIKey(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	codexSeat := &Account{
		ID:        "healthy-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
		},
	}
	fallbackKey := &Account{
		ID:          "openai_api_deadbeef",
		Type:        AccountTypeCodex,
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
		AccessToken: "sk-proj-test",
	}

	h := &proxyHandler{
		pool:      newPoolState([]*Account{codexSeat, fallbackKey}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.BestEligibleSeat == nil || data.BestEligibleSeat.ID != "healthy-seat" {
		t.Fatalf("best_eligible_seat=%+v", data.BestEligibleSeat)
	}
	if data.CurrentSeat == nil || data.CurrentSeat.ID != "healthy-seat" {
		t.Fatalf("current_seat=%+v", data.CurrentSeat)
	}
	if data.OpenAIAPIPool.NextKeyID != "openai_api_deadbeef" {
		t.Fatalf("next api key=%q", data.OpenAIAPIPool.NextKeyID)
	}
}

func TestServePoolDashboardRouteReturnsJSONContract(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID:        "blocked",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.91,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		cfg:       config{adminToken: "secret"},
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/admin/pool/dashboard", nil)
	req.Header.Set("X-Admin-Token", "secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.PoolSummary.TotalAccounts != 1 {
		t.Fatalf("total_accounts=%d", payload.PoolSummary.TotalAccounts)
	}
	if len(payload.WorkspaceGroups) != 1 || payload.WorkspaceGroups[0].WorkspaceID != "workspace-a" {
		t.Fatalf("workspace_groups=%+v", payload.WorkspaceGroups)
	}
	if len(payload.Accounts) != 1 || payload.Accounts[0].Routing.BlockReason != "secondary_headroom_lt_10" {
		t.Fatalf("accounts=%+v", payload.Accounts)
	}
}

func TestServeStatusPageClarifiesQuotaVsLocalFields(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID:        "blocked",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.91,
			SecondaryResetAt:     now.Add(2 * time.Hour),
			RetrievedAt:          now.Add(-3 * time.Minute),
			Source:               "wham",
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status", nil)
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Current Active Seat",
		"No live request is active right now.",
		"Remaining (5h)",
		"Remaining (7d)",
		"healthy seats routable",
		"Auth TTL",
		"Local Last Used",
		"Local Tokens",
		"usage wham",
		"remaining 85%",
		"remaining 9%",
		"used 91%",
		"used 15%",
		"Remaining columns show remaining headroom, not used quota.",
		"Primary/Secondary usage and recovery come from the latest observed quota snapshot.",
		"leave rotation once headroom reaches 10% remaining",
		"Status JSON",
		"Health check",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
		}
	}
	for _, forbidden := range []string{
		`href="/admin/accounts"`,
		`href="/admin/tokens"`,
		`href="/metrics"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in body", forbidden)
		}
	}
}

func TestServeStatusPageReturnsJSONForExplicitJSONClients(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.25,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status", nil)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.CodexCount != 1 {
		t.Fatalf("codex_count=%d", payload.CodexCount)
	}
	if payload.PoolSummary.EligibleAccounts != 1 {
		t.Fatalf("eligible_accounts=%d", payload.PoolSummary.EligibleAccounts)
	}
	if payload.ActiveSeat != nil {
		t.Fatalf("active_seat=%+v", payload.ActiveSeat)
	}
	if payload.BestEligibleSeat == nil || payload.BestEligibleSeat.ID != "healthy" {
		t.Fatalf("best_eligible_seat=%+v", payload.BestEligibleSeat)
	}
	if payload.CurrentSeat == nil || payload.CurrentSeat.ID != "healthy" {
		t.Fatalf("current_seat=%+v", payload.CurrentSeat)
	}
	if payload.Accounts[0].AuthExpiresAt == "" {
		t.Fatalf("auth_expires_at missing: %+v", payload.Accounts[0])
	}
}

func TestLocalOperatorCodexOAuthStartAllowsLoopbackWithoutAdminHeader(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if _, ok := payload["oauth_url"].(string); !ok {
		t.Fatalf("payload missing oauth_url: %+v", payload)
	}
	if _, ok := payload["state"].(string); !ok {
		t.Fatalf("payload missing state: %+v", payload)
	}
}

func TestLocalOperatorCodexOAuthStartRejectsNonLoopback(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://example.com/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.RemoteAddr = "198.51.100.10:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestLocalOperatorCodexOAuthStartRejectsForwardedRequests(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestLocalOperatorCodexAPIKeyAddStoresManagedKey(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_probe","status":"completed"}`))
	}))
	defer apiServer.Close()

	baseURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:       config{poolDir: poolDir},
		pool:      newPoolState(nil, false),
		registry:  NewProviderRegistry(codex, claude, gemini),
		transport: http.DefaultTransport,
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/api-key-add", strings.NewReader(`{"api_key":"sk-proj-test"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	accountID, _ := payload["account_id"].(string)
	if accountID == "" {
		t.Fatalf("missing account_id: %+v", payload)
	}
	if payload["health_status"] != "healthy" {
		t.Fatalf("unexpected health_status: %+v", payload)
	}
	if h.pool.count() != 1 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
	keyPath := filepath.Join(poolDir, managedOpenAIAPISubdir, accountID+".json")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("expected stored key file at %s: %v", keyPath, err)
	}
}

func TestLocalOperatorCodexAPIKeyAddMarksQuotaKeyDead(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`))
	}))
	defer apiServer.Close()

	baseURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)

	poolDir := t.TempDir()
	h := &proxyHandler{
		cfg:       config{poolDir: poolDir},
		pool:      newPoolState(nil, false),
		registry:  NewProviderRegistry(codex, claude, gemini),
		transport: http.DefaultTransport,
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/api-key-add", strings.NewReader(`{"api_key":"sk-proj-test"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload["health_status"] != "dead" {
		t.Fatalf("unexpected health_status: %+v", payload)
	}
	if payload["dead"] != true {
		t.Fatalf("expected dead=true, got %+v", payload)
	}
}

func TestLocalOperatorAccountDeleteRemovesManagedAPIKeyAndReloadsPool(t *testing.T) {
	apiBase, err := url.Parse("https://api.openai.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	acc, _, err := saveManagedOpenAIAPIKey(poolDir, "sk-proj-test-delete")
	if err != nil {
		t.Fatalf("save managed key: %v", err)
	}

	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState([]*Account{acc}, false),
		registry: NewProviderRegistry(codex, claude, gemini),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/account-delete", strings.NewReader(`{"account_id":"`+acc.ID+`"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(acc.File); !os.IsNotExist(err) {
		t.Fatalf("expected key file to be removed, stat err=%v", err)
	}
	if h.pool.count() != 0 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
}

func TestLocalOperatorAccountDeleteRejectsInflightAccount(t *testing.T) {
	apiBase, err := url.Parse("https://api.openai.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)

	poolDir := t.TempDir()
	codexDir := filepath.Join(poolDir, "codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	authPath := filepath.Join(codexDir, "seat-a.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"access","refresh_token":"refresh"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	h := &proxyHandler{
		cfg: config{poolDir: poolDir},
		pool: newPoolState([]*Account{{
			ID:          "seat-a",
			Type:        AccountTypeCodex,
			File:        authPath,
			AccessToken: "access",
			Inflight:    1,
		}}, false),
		registry: NewProviderRegistry(codex, claude, gemini),
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/account-delete", strings.NewReader(`{"account_id":"seat-a"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("expected auth file to remain: %v", err)
	}
	if h.pool.count() != 1 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
}

func TestLocalOperatorCodexOAuthStartDisabledInFriendMode(t *testing.T) {
	stubCodexLoopbackEnsure(t)

	h := &proxyHandler{
		cfg: config{friendCode: "friend-code", adminToken: "secret"},
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operator/codex/oauth-start", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestServeStatusPageReturnsJSONForFormatQuery(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status?format=json", nil)
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}

	var payload StatusData
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.CodexCount != 1 {
		t.Fatalf("codex_count=%d", payload.CodexCount)
	}
}

func TestServeStatusPageIncludesOperatorActionForLocalLoopback(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/status", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Start Codex OAuth",
		"Fallback API Pool",
		"Add API Key",
		"openai-api-key-input",
		"/operator/codex/api-key-add",
		"/operator/account-delete",
		"deleteAccountFromStatus",
		"account-action-status",
		"/v1/responses",
		"codex_pool_manager.py codex-oauth-start",
		"/operator/codex/oauth-start",
		"Open OAuth Page",
		"keeps the popup opener attached",
		"refreshes this page automatically when pool seat state changes",
		"Waiting for pool seat state to change...",
		"Waiting for pool seat state to change.",
		"codex-oauth-result",
		"auth_expires_at",
		"last_refresh_at",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
		}
	}
	for _, forbidden := range []string{
		"noopener noreferrer",
		"auth_expires_in || ''",
		"local_last_used || ''",
		"local_tokens || ''",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in status body", forbidden)
		}
	}
}

func TestServeStatusPageHidesOperatorActionOutsideLoopback(t *testing.T) {
	now := time.Date(2026, 3, 19, 13, 0, 0, 0, time.UTC)
	account := &Account{
		ID:        "healthy",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		AccountID: "workspace-a",
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}
	h := &proxyHandler{
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/status", nil)
	req.RemoteAddr = "198.51.100.10:4242"
	rr := httptest.NewRecorder()
	h.serveStatusPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, forbidden := range []string{
		"Start Codex OAuth",
		"codex_pool_manager.py codex-oauth-start",
		"/operator/codex/oauth-start",
		"Fallback API Pool",
		"/operator/codex/api-key-add",
		"/operator/account-delete",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in body", forbidden)
		}
	}
}
