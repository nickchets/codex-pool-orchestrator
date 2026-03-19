package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReloadAccountsPreservesRuntimeState(t *testing.T) {
	tmp := t.TempDir()
	poolDir := filepath.Join(tmp, "codex")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatalf("mkdir pool dir: %v", err)
	}

	authPath := filepath.Join(poolDir, "seat-a.json")
	auth := map[string]any{
		"tokens": map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"account_id":    "workspace-a",
		},
	}
	buf, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	if err := os.WriteFile(authPath, buf, 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	handler := &proxyHandler{
		cfg: config{poolDir: tmp},
		pool: newPoolState([]*Account{{
			ID:           "seat-a",
			Type:         AccountTypeCodex,
			File:         authPath,
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			Usage: UsageSnapshot{
				PrimaryUsedPercent:   0.42,
				SecondaryUsedPercent: 0.84,
				PrimaryUsed:          0.42,
				SecondaryUsed:        0.84,
				PrimaryResetAt:       now.Add(90 * time.Minute),
				SecondaryResetAt:     now.Add(12 * time.Hour),
				RetrievedAt:          now.Add(-2 * time.Minute),
				Source:               "wham",
			},
			Penalty:        1.5,
			LastPenalty:    now.Add(-5 * time.Minute),
			LastUsed:       now.Add(-30 * time.Second),
			RateLimitUntil: now.Add(10 * time.Minute),
			Totals: AccountUsage{
				TotalBillableTokens: 1234,
				RequestCount:        7,
				LastPrimaryPct:      0.42,
				LastSecondaryPct:    0.84,
				LastUpdated:         now.Add(-30 * time.Second),
			},
		}}, false),
		registry: &ProviderRegistry{
			byType: map[AccountType]Provider{
				AccountTypeCodex: &CodexProvider{},
			},
		},
	}

	handler.reloadAccounts()

	if handler.pool.count() != 1 {
		t.Fatalf("reloaded accounts=%d", handler.pool.count())
	}

	reloaded := handler.pool.allAccounts()[0]
	if reloaded.AccessToken != "new-access" {
		t.Fatalf("access token not refreshed from disk: %q", reloaded.AccessToken)
	}
	if reloaded.RefreshToken != "new-refresh" {
		t.Fatalf("refresh token not refreshed from disk: %q", reloaded.RefreshToken)
	}
	if reloaded.Usage.PrimaryUsedPercent != 0.42 || reloaded.Usage.SecondaryUsedPercent != 0.84 {
		t.Fatalf("usage lost across reload: %+v", reloaded.Usage)
	}
	if reloaded.Usage.Source != "wham" {
		t.Fatalf("usage source=%q", reloaded.Usage.Source)
	}
	if reloaded.Penalty != 1.5 {
		t.Fatalf("penalty=%v", reloaded.Penalty)
	}
	if !reloaded.LastPenalty.Equal(now.Add(-5 * time.Minute)) {
		t.Fatalf("last penalty=%v", reloaded.LastPenalty)
	}
	if !reloaded.LastUsed.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("last used=%v", reloaded.LastUsed)
	}
	if !reloaded.RateLimitUntil.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("rate limit until=%v", reloaded.RateLimitUntil)
	}
	if reloaded.Totals.TotalBillableTokens != 1234 || reloaded.Totals.RequestCount != 7 {
		t.Fatalf("totals lost across reload: %+v", reloaded.Totals)
	}
}
