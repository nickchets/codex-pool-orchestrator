package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if data.BestEligibleSeat == nil || data.BestEligibleSeat.ID != "older-seat" {
		t.Fatalf("best_eligible_seat=%+v", data.BestEligibleSeat)
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
		"Auth TTL",
		"Local Last Used",
		"Local Tokens",
		"usage wham",
		"used 91%",
		"used 15%",
		"Remaining columns show remaining headroom, not used quota.",
		"Primary/Secondary usage and recovery come from the latest observed quota snapshot.",
		"stay eligible at exactly 10% remaining",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
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
		"operator <code>codex-oauth-start</code> command",
		"/operator/codex/oauth-start",
		"Open OAuth Page",
		"keeps the popup opener attached",
		"refreshes this page automatically when the account list changes",
		"Waiting for the account list to change...",
		"Waiting for the account list to change.",
		"codex-oauth-result",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in body", fragment)
		}
	}
	if strings.Contains(body, "noopener noreferrer") {
		t.Fatalf("unexpected opener suppression in status body")
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
		"operator <code>codex-oauth-start</code> command",
		"/operator/codex/oauth-start",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in body", forbidden)
		}
	}
}
