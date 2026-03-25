package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScorePrefersHeadroomAndPlan(t *testing.T) {
	now := time.Now()
	pro := &Account{PlanType: "pro", Usage: UsageSnapshot{PrimaryUsedPercent: 0.2, SecondaryUsedPercent: 0.2}}
	plus := &Account{PlanType: "plus", Usage: UsageSnapshot{PrimaryUsedPercent: 0.1, SecondaryUsedPercent: 0.1}, Penalty: 0.5}

	if scoreAccount(pro, now) <= scoreAccount(plus, now) {
		t.Fatalf("expected pro with headroom to win")
	}
}

func TestPenaltyDecay(t *testing.T) {
	now := time.Now()
	a := &Account{Penalty: 1.0, LastPenalty: now.Add(-10 * time.Minute)}
	scoreAccount(a, now)
	if a.Penalty >= 1.0 {
		t.Fatalf("penalty should decay")
	}
}

func TestLoadAccountsQuarantinesLongDeadAccount(t *testing.T) {
	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	registry := NewProviderRegistry(
		NewCodexProvider(apiBase, apiBase, apiBase, apiBase),
		NewClaudeProvider(apiBase),
		NewGeminiProvider(apiBase, apiBase),
	)

	poolDir := t.TempDir()
	codexDir := filepath.Join(poolDir, "codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}

	deadSince := time.Now().Add(-longDeadAccountQuarantineAfter - time.Hour).UTC().Format(time.RFC3339)
	authPath := filepath.Join(codexDir, "dead-seat.json")
	payload := `{"tokens":{"access_token":"access","refresh_token":"refresh"},"dead":true,"dead_since":"` + deadSince + `"}`
	if err := os.WriteFile(authPath, []byte(payload), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	accounts, err := loadPool(poolDir, registry)
	if err != nil {
		t.Fatalf("loadPool: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected long-dead account to be quarantined, got %d accounts", len(accounts))
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("expected source file to be moved, stat err=%v", err)
	}

	quarantinedPath := filepath.Join(poolDir, quarantineSubdir, "codex", "dead-seat.json")
	if _, err := os.Stat(quarantinedPath); err != nil {
		t.Fatalf("expected quarantined file at %s: %v", quarantinedPath, err)
	}

	status := loadQuarantineStatus(poolDir, time.Now().UTC())
	if status.Total != 1 {
		t.Fatalf("quarantine total=%d", status.Total)
	}
	if got := status.Providers["codex"]; got != 1 {
		t.Fatalf("quarantine codex count=%d", got)
	}
	if len(status.Recent) != 1 || status.Recent[0].ID != "dead-seat" {
		t.Fatalf("unexpected recent quarantine entries: %+v", status.Recent)
	}
}

func TestCandidateUsesPinUnlessExcluded(t *testing.T) {
	a1 := &Account{ID: "a1", Type: AccountTypeCodex, Usage: UsageSnapshot{PrimaryUsedPercent: 0.1}}
	a2 := &Account{ID: "a2", Type: AccountTypeCodex, Usage: UsageSnapshot{PrimaryUsedPercent: 0.2}}
	p := newPoolState([]*Account{a1, a2}, true)
	p.pin("c1", "a1")

	if got := p.candidate("c1", nil, "", ""); got == nil || got.ID != "a1" {
		t.Fatalf("expected pinned a1, got %+v", got)
	}
	if got := p.candidate("c1", map[string]bool{"a1": true}, "", ""); got == nil || got.ID != "a2" {
		t.Fatalf("expected a2 when pinned excluded, got %+v", got)
	}
}

func TestCandidatePrefersAccountsUnderPreemptiveThreshold(t *testing.T) {
	nearLimit := &Account{
		ID:   "near-limit",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.20,
			SecondaryUsedPercent: 0.91,
			SecondaryResetAt:     time.Now().Add(2 * time.Hour),
		},
	}
	healthy := &Account{
		ID:   "healthy",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			SecondaryResetAt:     time.Now().Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{nearLimit, healthy}, false)

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected healthy account, got %+v", got)
	}
}

func TestRoutingStateBlocksExactTenPercentHeadroom(t *testing.T) {
	now := time.Now()
	exactThreshold := &Account{
		ID:   "exact-threshold",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.90,
			SecondaryUsedPercent: 0.90,
			PrimaryResetAt:       now.Add(30 * time.Minute),
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}

	exactThreshold.mu.Lock()
	routing := routingStateLocked(exactThreshold, now, AccountTypeCodex, "")
	exactThreshold.mu.Unlock()

	if routing.Eligible {
		t.Fatalf("expected exact-threshold seat to be blocked")
	}
	if routing.BlockReason != "codex_headroom_lt_10" {
		t.Fatalf("expected codex_headroom_lt_10, got %q", routing.BlockReason)
	}
}

func TestPinnedConversationUnpinsAtExactPreemptiveThreshold(t *testing.T) {
	exactThreshold := &Account{
		ID:   "exact-threshold",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.90,
			SecondaryUsedPercent: 0.90,
			PrimaryResetAt:       time.Now().Add(30 * time.Minute),
			SecondaryResetAt:     time.Now().Add(2 * time.Hour),
		},
	}
	healthy := &Account{
		ID:   "healthy",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
		},
	}
	p := newPoolState([]*Account{exactThreshold, healthy}, false)
	p.pin("conv", "exact-threshold")

	got := p.candidate("conv", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected exact-threshold account to unpin to healthy, got %+v", got)
	}
}

func TestPinnedConversationUnpinsAbovePreemptiveThreshold(t *testing.T) {
	exhausted := &Account{
		ID:   "exhausted",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.91,
			SecondaryUsedPercent: 0.10,
			PrimaryResetAt:       time.Now().Add(30 * time.Minute),
		},
	}
	healthy := &Account{
		ID:   "healthy",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
		},
	}
	p := newPoolState([]*Account{exhausted, healthy}, false)
	p.pin("conv", "exhausted")

	got := p.candidate("conv", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected pinned exhausted account to unpin to healthy, got %+v", got)
	}
}

func TestCandidateReusesMostRecentlyUsedEligibleSeat(t *testing.T) {
	now := time.Now()
	sticky := &Account{
		ID:       "sticky",
		Type:     AccountTypeCodex,
		PlanType: "team",
		LastUsed: now.Add(-15 * time.Second),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.89,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(30 * time.Minute),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthier := &Account{
		ID:       "healthier",
		Type:     AccountTypeCodex,
		PlanType: "team",
		LastUsed: now.Add(-2 * time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.05,
			SecondaryUsedPercent: 0.05,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{sticky, healthier}, false)

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "sticky" {
		t.Fatalf("expected most recently used eligible seat, got %+v", got)
	}
}

func TestCandidateStopsReusingMostRecentlyUsedSeatAtExactPrimaryThreshold(t *testing.T) {
	now := time.Now()
	sticky := &Account{
		ID:       "sticky",
		Type:     AccountTypeCodex,
		PlanType: "team",
		LastUsed: now.Add(-15 * time.Second),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.90,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(30 * time.Minute),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthy := &Account{
		ID:       "healthy",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{sticky, healthy}, false)

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected sticky seat at exact primary threshold to be bypassed, got %+v", got)
	}
}

func TestCandidateStopsReusingMostRecentlyUsedSeatAtExactSecondaryThreshold(t *testing.T) {
	now := time.Now()
	sticky := &Account{
		ID:       "sticky",
		Type:     AccountTypeCodex,
		PlanType: "team",
		LastUsed: now.Add(-15 * time.Second),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.20,
			SecondaryUsedPercent: 0.90,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthy := &Account{
		ID:       "healthy",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.20,
			SecondaryUsedPercent: 0.10,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{sticky, healthy}, false)

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected sticky seat at exact secondary threshold to be bypassed, got %+v", got)
	}
}

func TestCandidateKeepsActiveCodexSeatWhileEligible(t *testing.T) {
	now := time.Now()
	active := &Account{
		ID:       "active",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.25,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	betterScore := &Account{
		ID:       "better-score",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.05,
			SecondaryUsedPercent: 0.05,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{active, betterScore}, false)
	p.activeCodexID = active.ID

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "active" {
		t.Fatalf("expected active codex seat to be reused before LastUsed is populated, got %+v", got)
	}
}

func TestCandidateDropsActiveCodexSeatAtExactPrimaryThreshold(t *testing.T) {
	now := time.Now()
	active := &Account{
		ID:       "active",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.90,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(30 * time.Minute),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthy := &Account{
		ID:       "healthy",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{active, healthy}, false)
	p.activeCodexID = active.ID

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected exact-threshold active seat to rotate, got %+v", got)
	}
	if p.activeCodexID != "healthy" {
		t.Fatalf("expected active codex seat to move to healthy, got %q", p.activeCodexID)
	}
}

func TestCandidateDropsActiveCodexSeatAtExactSecondaryThreshold(t *testing.T) {
	now := time.Now()
	active := &Account{
		ID:       "active",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.20,
			SecondaryUsedPercent: 0.90,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthy := &Account{
		ID:       "healthy",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{active, healthy}, false)
	p.activeCodexID = active.ID

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected exact-threshold secondary active seat to rotate, got %+v", got)
	}
	if p.activeCodexID != "healthy" {
		t.Fatalf("expected active codex seat to move to healthy, got %q", p.activeCodexID)
	}
}

func TestCandidateActiveManagedAPIFallbackDoesNotStealEligibleCodexSeat(t *testing.T) {
	now := time.Now()
	local := &Account{
		ID:       "local-pro",
		Type:     AccountTypeCodex,
		PlanType: "pro",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	api := &Account{
		ID:           "openai-api-key",
		Type:         AccountTypeCodex,
		PlanType:     "api",
		AuthMode:     accountAuthModeAPIKey,
		HealthStatus: "healthy",
	}
	p := newPoolState([]*Account{local, api}, false)
	p.activeAPIID = api.ID

	got := p.candidate("", nil, AccountTypeCodex, "pro")
	if got == nil || got.ID != "local-pro" {
		t.Fatalf("expected eligible local codex seat to win over active api fallback, got %+v", got)
	}
}

func TestPeekCandidateDoesNotClaimActiveCodexSeat(t *testing.T) {
	now := time.Now()
	a := &Account{
		ID:       "seat-a",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{a}, false)

	got := p.peekCandidate(AccountTypeCodex, "")
	if got == nil || got.ID != "seat-a" {
		t.Fatalf("expected peek candidate seat-a, got %+v", got)
	}
	if p.activeCodexID != "" {
		t.Fatalf("peekCandidate should not mutate activeCodexID, got %q", p.activeCodexID)
	}
}

func TestRoutingStateReentersAfterSecondaryResetWithFreshUsage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	acc := &Account{
		ID:       "seat-a",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.96,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(-10 * time.Minute),
			RetrievedAt:          now.Add(-30 * time.Minute),
			Source:               "headers",
		},
	}

	applyUsageSnapshot(acc, &UsageSnapshot{
		PrimaryUsedPercent:   0.12,
		SecondaryUsedPercent: 0.06,
		RetrievedAt:          now,
		Source:               "token_count",
	})

	acc.mu.Lock()
	snapshot := acc.Usage
	acc.mu.Unlock()
	if !snapshot.SecondaryResetAt.IsZero() {
		t.Fatalf("expected stale secondary reset to be cleared, got %v", snapshot.SecondaryResetAt)
	}

	state := routingStateLocked(acc, now.Add(time.Second), AccountTypeCodex, "")
	if !state.Eligible {
		t.Fatalf("expected account to re-enter after reset, got %+v", state)
	}
	if state.SecondaryUsed != 0.06 {
		t.Fatalf("secondary_used=%v", state.SecondaryUsed)
	}
}

func TestRoutingStateReentersAfterReset(t *testing.T) {
	now := time.Now()
	resetAccount := &Account{
		ID:   "reset-account",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.12,
			SecondaryUsedPercent: 0.97,
			SecondaryResetAt:     now.Add(-5 * time.Minute),
		},
	}

	resetAccount.mu.Lock()
	routing := routingStateLocked(resetAccount, now, AccountTypeCodex, "")
	resetAccount.mu.Unlock()

	if !routing.Eligible {
		t.Fatalf("expected reset account to reenter, block_reason=%s", routing.BlockReason)
	}
	if routing.SecondaryUsed != 0 {
		t.Fatalf("expected secondary usage to reset to zero, got %v", routing.SecondaryUsed)
	}

	p := newPoolState([]*Account{resetAccount}, false)
	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "reset-account" {
		t.Fatalf("expected reset account to be selectable again, got %+v", got)
	}
}

func TestCandidateSkipsDeadOrDisabled(t *testing.T) {
	dead := &Account{ID: "dead", Type: AccountTypeCodex, Dead: true, Usage: UsageSnapshot{PrimaryUsedPercent: 0.0}}
	disabled := &Account{ID: "disabled", Type: AccountTypeCodex, Disabled: true, Usage: UsageSnapshot{PrimaryUsedPercent: 0.0}}
	ok := &Account{ID: "ok", Type: AccountTypeCodex, Usage: UsageSnapshot{PrimaryUsedPercent: 0.5}}
	p := newPoolState([]*Account{dead, disabled, ok}, false)

	got := p.candidate("", nil, "", "")
	if got == nil || got.ID != "ok" {
		t.Fatalf("expected ok, got %+v", got)
	}
}

func TestCandidateRequiredPlanFiltersAccounts(t *testing.T) {
	plus := &Account{ID: "plus", Type: AccountTypeCodex, PlanType: "plus", Usage: UsageSnapshot{PrimaryUsedPercent: 0.1}}
	pro := &Account{ID: "pro", Type: AccountTypeCodex, PlanType: "pro", Usage: UsageSnapshot{PrimaryUsedPercent: 0.2}}
	p := newPoolState([]*Account{plus, pro}, false)

	got := p.candidate("", nil, AccountTypeCodex, "pro")
	if got == nil || got.ID != "pro" {
		t.Fatalf("expected pro account, got %+v", got)
	}
}

func TestCandidateRequiredPlanOverridesPinnedConversation(t *testing.T) {
	plus := &Account{ID: "plus", Type: AccountTypeCodex, PlanType: "plus", Usage: UsageSnapshot{PrimaryUsedPercent: 0.1}}
	pro := &Account{ID: "pro", Type: AccountTypeCodex, PlanType: "pro", Usage: UsageSnapshot{PrimaryUsedPercent: 0.2}}
	p := newPoolState([]*Account{plus, pro}, false)
	p.pin("c1", "plus")

	got := p.candidate("c1", nil, AccountTypeCodex, "pro")
	if got == nil || got.ID != "pro" {
		t.Fatalf("expected pinned plus to be bypassed for required plan, got %+v", got)
	}
}

func TestCandidateFallsBackToManagedOpenAIAPIKeyWhenCodexSeatsUnavailable(t *testing.T) {
	blockedSeat := &Account{
		ID:       "blocked-seat",
		Type:     AccountTypeCodex,
		PlanType: "pro",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.15,
			SecondaryUsedPercent: 0.96,
			SecondaryResetAt:     time.Now().Add(2 * time.Hour),
		},
	}
	apiKey := &Account{
		ID:           "openai-api-key",
		Type:         AccountTypeCodex,
		PlanType:     "api",
		AuthMode:     accountAuthModeAPIKey,
		HealthStatus: "healthy",
	}

	p := newPoolState([]*Account{blockedSeat, apiKey}, false)

	got := p.candidate("", nil, AccountTypeCodex, "pro")
	if got == nil || got.ID != "openai-api-key" {
		t.Fatalf("expected managed api key fallback, got %+v", got)
	}
}

func TestRoutingStateBlocksRateLimitedManagedOpenAIAPIKey(t *testing.T) {
	now := time.Now()
	apiKey := &Account{
		ID:             "openai-api-key",
		Type:           AccountTypeCodex,
		PlanType:       "api",
		AuthMode:       accountAuthModeAPIKey,
		RateLimitUntil: now.Add(2 * time.Minute),
	}

	apiKey.mu.Lock()
	routing := routingStateLocked(apiKey, now, AccountTypeCodex, "")
	apiKey.mu.Unlock()

	if routing.Eligible {
		t.Fatalf("expected rate-limited managed api key to be blocked")
	}
	if routing.BlockReason != "rate_limited" {
		t.Fatalf("block_reason=%q", routing.BlockReason)
	}
}

func TestRoutingStateBlocksRateLimitedLocalCodexSeat(t *testing.T) {
	now := time.Now()
	seat := &Account{
		ID:             "codex-seat",
		Type:           AccountTypeCodex,
		PlanType:       "team",
		RateLimitUntil: now.Add(2 * time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.30,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}

	seat.mu.Lock()
	routing := routingStateLocked(seat, now, AccountTypeCodex, "")
	seat.mu.Unlock()

	if routing.Eligible {
		t.Fatalf("expected rate-limited local codex seat to be blocked")
	}
	if routing.BlockReason != "rate_limited" {
		t.Fatalf("block_reason=%q", routing.BlockReason)
	}
	if routing.CodexRateLimitBypass {
		t.Fatalf("expected local codex cooldown to stop bypassing rate limits")
	}
}

func TestRoutingStateBlocksValidationBlockedGeminiSeat(t *testing.T) {
	now := time.Now()
	seat := &Account{
		ID:                           "gemini-seat",
		Type:                         AccountTypeGemini,
		AntigravityValidationBlocked: true,
	}

	seat.mu.Lock()
	routing := routingStateLocked(seat, now, AccountTypeGemini, "")
	seat.mu.Unlock()

	if routing.Eligible {
		t.Fatalf("expected validation-blocked Gemini seat to be blocked")
	}
	if routing.BlockReason != "validation_blocked" {
		t.Fatalf("block_reason=%q", routing.BlockReason)
	}
}

func TestRoutingStateBlocksProxyDisabledGeminiSeat(t *testing.T) {
	now := time.Now()
	seat := &Account{
		ID:                       "gemini-seat",
		Type:                     AccountTypeGemini,
		AntigravityProxyDisabled: true,
	}

	seat.mu.Lock()
	routing := routingStateLocked(seat, now, AccountTypeGemini, "")
	seat.mu.Unlock()

	if routing.Eligible {
		t.Fatalf("expected proxy-disabled Gemini seat to be blocked")
	}
	if routing.BlockReason != "proxy_disabled" {
		t.Fatalf("block_reason=%q", routing.BlockReason)
	}
}

func TestCandidateSkipsRateLimitedLocalCodexSeat(t *testing.T) {
	now := time.Now()
	cooling := &Account{
		ID:             "cooling",
		Type:           AccountTypeCodex,
		PlanType:       "team",
		RateLimitUntil: now.Add(2 * time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthy := &Account{
		ID:       "healthy",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{cooling, healthy}, false)
	p.activeCodexID = cooling.ID

	got := p.candidate("", nil, AccountTypeCodex, "")
	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected candidate to skip rate-limited local codex seat, got %+v", got)
	}
	if p.activeCodexID != "healthy" {
		t.Fatalf("expected active codex seat to move to healthy, got %q", p.activeCodexID)
	}
}

func TestCandidateRetryPathDoesNotMoveActiveCodexSeat(t *testing.T) {
	now := time.Now()
	active := &Account{
		ID:       "active",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.20,
			SecondaryUsedPercent: 0.20,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	healthy := &Account{
		ID:       "healthy",
		Type:     AccountTypeCodex,
		PlanType: "team",
		LastUsed: now.Add(-time.Minute),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.10,
			SecondaryUsedPercent: 0.10,
			PrimaryResetAt:       now.Add(2 * time.Hour),
			SecondaryResetAt:     now.Add(24 * time.Hour),
		},
	}
	p := newPoolState([]*Account{active, healthy}, false)
	p.activeCodexID = active.ID

	p.mu.Lock()
	got := p.candidateAtLocked(now, "", map[string]bool{"active": true}, AccountTypeCodex, "", true)
	activeID := p.activeCodexID
	p.mu.Unlock()

	if got == nil || got.ID != "healthy" {
		t.Fatalf("expected retry path to choose healthy seat, got %+v", got)
	}
	if activeID != "active" {
		t.Fatalf("expected retry path to keep prior active seat, got %q", activeID)
	}
}

func TestMergeUsagePreservesExistingFields(t *testing.T) {
	prev := UsageSnapshot{
		PrimaryUsedPercent:   0.2,
		SecondaryUsedPercent: 0.3,
		PrimaryWindowMinutes: 300,
		Source:               "old",
		RetrievedAt:          time.Now(),
	}
	next := UsageSnapshot{
		PrimaryUsedPercent: 0.25,
		RetrievedAt:        time.Now().Add(1 * time.Minute),
		Source:             "body",
	}
	merged := mergeUsage(prev, next)
	if merged.SecondaryUsedPercent != 0.3 {
		t.Fatalf("expected secondary preserved when new absent, got %v", merged.SecondaryUsedPercent)
	}
	if merged.PrimaryWindowMinutes != 300 {
		t.Fatalf("expected window preserved, got %d", merged.PrimaryWindowMinutes)
	}
	if merged.PrimaryUsedPercent != 0.25 {
		t.Fatalf("expected primary updated, got %v", merged.PrimaryUsedPercent)
	}
	if merged.Source != "body" {
		t.Fatalf("expected source updated, got %s", merged.Source)
	}
}

func TestSaveAccountPreservesUnknownFields(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "auth.json")

	original := map[string]any{
		"tokens": map[string]any{
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
			"id_token":      "old-id",
			"account_id":    "acct_123",
			"extra_token": map[string]any{
				"foo": 1,
			},
		},
		"last_refresh": "2025-12-01T00:00:00Z",
		"extra_top":    []any{1, 2, 3},
		"meta": map[string]any{
			"x": "y",
		},
	}
	buf, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	acc := &Account{
		ID:           "a1",
		File:         path,
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		IDToken:      "new-id",
		AccountID:    "acct_123",
		LastRefresh:  time.Date(2025, 12, 17, 0, 0, 0, 0, time.UTC),
	}
	if err := saveAccount(acc); err != nil {
		t.Fatalf("saveAccount: %v", err)
	}

	afterRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var after map[string]any
	if err := json.Unmarshal(afterRaw, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}

	// Top-level unknown fields preserved.
	if _, ok := after["extra_top"]; !ok {
		t.Fatalf("expected extra_top preserved")
	}
	if _, ok := after["meta"]; !ok {
		t.Fatalf("expected meta preserved")
	}

	// Token fields updated, unknown token fields preserved.
	tokens, ok := after["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("expected tokens object")
	}
	if tokens["access_token"] != "new-access" {
		t.Fatalf("access_token=%v", tokens["access_token"])
	}
	if tokens["refresh_token"] != "new-refresh" {
		t.Fatalf("refresh_token=%v", tokens["refresh_token"])
	}
	if tokens["id_token"] != "new-id" {
		t.Fatalf("id_token=%v", tokens["id_token"])
	}
	if tokens["account_id"] != "acct_123" {
		t.Fatalf("account_id=%v", tokens["account_id"])
	}
	if _, ok := tokens["extra_token"]; !ok {
		t.Fatalf("expected tokens.extra_token preserved")
	}
}
