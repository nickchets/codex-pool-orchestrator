package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testT34ProviderRegistry(t *testing.T) *ProviderRegistry {
	t.Helper()
	apiBase, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatalf("parse api base: %v", err)
	}
	codex := NewCodexProvider(apiBase, apiBase, apiBase, apiBase)
	claude := NewClaudeProvider(apiBase)
	gemini := NewGeminiProvider(apiBase, apiBase)
	return NewProviderRegistry(codex, claude, gemini)
}

func TestApplySuccessfulAccountStateLockedClearsDeadSince(t *testing.T) {
	now := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	acc := &Account{
		ID:             "gemini-dead",
		Type:           AccountTypeGemini,
		Dead:           true,
		DeadSince:      now.Add(-24 * time.Hour),
		HealthStatus:   "dead",
		HealthError:    "revoked",
		RateLimitUntil: now.Add(time.Hour),
	}

	acc.mu.Lock()
	applySuccessfulAccountStateLocked(acc, now)
	acc.mu.Unlock()

	if acc.Dead {
		t.Fatalf("expected account to be alive after success path")
	}
	if !acc.DeadSince.IsZero() {
		t.Fatalf("expected dead_since to be cleared, got %s", acc.DeadSince)
	}
	if acc.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
}

func TestApplySuccessfulAccountStateLockedPreservesGeminiValidationBlockedTruth(t *testing.T) {
	now := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	acc := &Account{
		ID:                           "gemini-blocked",
		Type:                         AccountTypeGemini,
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
		GeminiProviderTruthState:     geminiProviderTruthStateRestricted,
		HealthStatus:                 "error",
		HealthError:                  "stale transport error",
		RateLimitUntil:               now.Add(time.Hour),
	}

	acc.mu.Lock()
	applySuccessfulAccountStateLocked(acc, now)
	acc.mu.Unlock()

	if acc.HealthStatus != "restricted" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != "" {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if acc.HealthCheckedAt != now {
		t.Fatalf("health_checked_at=%s want %s", acc.HealthCheckedAt, now)
	}
	if !acc.LastHealthyAt.IsZero() {
		t.Fatalf("last_healthy_at=%s want zero", acc.LastHealthyAt)
	}
	if !acc.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%s want zero", acc.RateLimitUntil)
	}
	if acc.GeminiOperationalState != geminiOperationalTruthStateDegradedOK {
		t.Fatalf("gemini_operational_state=%q", acc.GeminiOperationalState)
	}
}

func TestBuildPoolDashboardDataIncludesDeadSince(t *testing.T) {
	now := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	deadSince := now.Add(-96 * time.Hour).UTC()
	account := &Account{
		ID:        "dead-seat",
		Type:      AccountTypeCodex,
		PlanType:  "team",
		Dead:      true,
		DeadSince: deadSince,
		IDToken:   testCodexIDToken(t, "user-a", "workspace-a", "a@example.com", "sub-a", now.Add(4*time.Hour)),
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.25,
			SecondaryUsedPercent: 0.95,
			SecondaryResetAt:     now.Add(2 * time.Hour),
		},
	}

	h := &proxyHandler{
		cfg:       config{poolDir: t.TempDir()},
		pool:      newPoolState([]*Account{account}, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if len(data.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(data.Accounts))
	}
	if data.Accounts[0].DeadSince != deadSince.Format(time.RFC3339) {
		t.Fatalf("dead_since=%q want %q", data.Accounts[0].DeadSince, deadSince.Format(time.RFC3339))
	}
}

func TestBuildPoolDashboardDataIncludesQuarantineSummary(t *testing.T) {
	now := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	poolDir := t.TempDir()
	quarantineDir := filepath.Join(poolDir, quarantineSubdir, "codex")
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		t.Fatalf("mkdir quarantine dir: %v", err)
	}
	quarantinedFile := filepath.Join(quarantineDir, "seat-a.json")
	if err := os.WriteFile(quarantinedFile, []byte(`{"dead":true}`), 0o600); err != nil {
		t.Fatalf("write quarantine file: %v", err)
	}
	quarantinedAt := now.Add(-2 * time.Hour)
	if err := os.Chtimes(quarantinedFile, quarantinedAt, quarantinedAt); err != nil {
		t.Fatalf("chtimes quarantine file: %v", err)
	}

	h := &proxyHandler{
		cfg:       config{poolDir: poolDir},
		pool:      newPoolState(nil, false),
		startTime: now.Add(-time.Hour),
	}

	data := h.buildPoolDashboardData(now)
	if data.Quarantine.Total != 1 {
		t.Fatalf("quarantine total=%d", data.Quarantine.Total)
	}
	if data.Quarantine.Providers["codex"] != 1 {
		t.Fatalf("quarantine providers=%+v", data.Quarantine.Providers)
	}
	if len(data.Quarantine.Recent) != 1 {
		t.Fatalf("quarantine recent=%+v", data.Quarantine.Recent)
	}
	if data.Quarantine.Recent[0].ID != "seat-a" {
		t.Fatalf("quarantine recent id=%q", data.Quarantine.Recent[0].ID)
	}
}

func TestQuarantineLongDeadAccountsMovesFileAndReloadsPool(t *testing.T) {
	now := time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	poolDir := t.TempDir()
	providerDir := filepath.Join(poolDir, "openai_api")
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatalf("mkdir provider dir: %v", err)
	}

	deadSince := now.Add(-longDeadAccountQuarantineAfter - time.Hour).UTC()
	accountFile := filepath.Join(providerDir, "dead-key.json")
	payload := `{"OPENAI_API_KEY":"sk-test","auth_mode":"api","plan_type":"api","dead":true,"dead_since":"` + deadSince.Format(time.RFC3339) + `","health_status":"dead"}`
	if err := os.WriteFile(accountFile, []byte(payload), 0o600); err != nil {
		t.Fatalf("write account file: %v", err)
	}

	acc := &Account{
		ID:          "dead-key",
		Type:        AccountTypeCodex,
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
		AccessToken: "sk-test",
		File:        accountFile,
		Dead:        true,
		DeadSince:   deadSince,
	}
	h := &proxyHandler{
		cfg:      config{poolDir: poolDir},
		pool:     newPoolState([]*Account{acc}, false),
		registry: testT34ProviderRegistry(t),
	}

	moved, err := h.quarantineLongDeadAccounts(now)
	if err != nil {
		t.Fatalf("quarantineLongDeadAccounts error: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved=%d", moved)
	}
	if h.pool.count() != 0 {
		t.Fatalf("pool count=%d", h.pool.count())
	}
	if _, err := os.Stat(accountFile); !os.IsNotExist(err) {
		t.Fatalf("expected original file to be moved, stat err=%v", err)
	}

	quarantinedFile := filepath.Join(poolDir, quarantineSubdir, "openai_api", "dead-key.json")
	if _, err := os.Stat(quarantinedFile); err != nil {
		t.Fatalf("expected quarantined file at %s: %v", quarantinedFile, err)
	}

	data := h.buildPoolDashboardData(now)
	if data.Quarantine.Total != 1 {
		t.Fatalf("quarantine total=%d", data.Quarantine.Total)
	}
}
