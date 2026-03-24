package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRestorePersistedUsageStateRestoresSnapshotAndTotals(t *testing.T) {
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 7)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.record(RequestUsage{
		AccountID:        "seat-a",
		InputTokens:      10,
		OutputTokens:     5,
		BillableTokens:   15,
		PrimaryUsedPct:   0.42,
		SecondaryUsedPct: 0.84,
		Timestamp:        now,
		RequestID:        "req-1",
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	snapshot := UsageSnapshot{
		PrimaryUsed:          0.55,
		SecondaryUsed:        0.65,
		PrimaryUsedPercent:   0.55,
		SecondaryUsedPercent: 0.65,
		PrimaryResetAt:       now.Add(90 * time.Minute),
		SecondaryResetAt:     now.Add(12 * time.Hour),
		RetrievedAt:          now.Add(1 * time.Minute),
		Source:               "headers",
	}
	if err := store.saveAccountUsageSnapshot("seat-a", snapshot); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	acc := &Account{ID: "seat-a", Type: AccountTypeCodex}
	restoredTotals, restoredSnapshots, bridged := restorePersistedUsageState([]*Account{acc}, store)

	if restoredTotals != 1 || restoredSnapshots != 1 || bridged != 0 {
		t.Fatalf("restore counts = (%d, %d, %d)", restoredTotals, restoredSnapshots, bridged)
	}
	if acc.Totals.TotalBillableTokens != 15 || acc.Totals.RequestCount != 1 {
		t.Fatalf("totals=%+v", acc.Totals)
	}
	if acc.Usage.PrimaryUsedPercent != 0.55 || acc.Usage.SecondaryUsedPercent != 0.65 {
		t.Fatalf("usage=%+v", acc.Usage)
	}
	if acc.Usage.Source != "headers" {
		t.Fatalf("usage source=%q", acc.Usage.Source)
	}
	if !acc.Usage.RetrievedAt.Equal(now.Add(1 * time.Minute)) {
		t.Fatalf("retrieved_at=%v", acc.Usage.RetrievedAt)
	}
}

func TestRestorePersistedUsageStateBridgesFromTotalsWhenSnapshotMissing(t *testing.T) {
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 7)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.record(RequestUsage{
		AccountID:        "seat-b",
		InputTokens:      11,
		OutputTokens:     4,
		BillableTokens:   15,
		PrimaryUsedPct:   0.33,
		SecondaryUsedPct: 0.66,
		Timestamp:        now,
		RequestID:        "req-2",
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	acc := &Account{ID: "seat-b", Type: AccountTypeCodex}
	restoredTotals, restoredSnapshots, bridged := restorePersistedUsageState([]*Account{acc}, store)

	if restoredTotals != 1 || restoredSnapshots != 0 || bridged != 1 {
		t.Fatalf("restore counts = (%d, %d, %d)", restoredTotals, restoredSnapshots, bridged)
	}
	if acc.Usage.PrimaryUsedPercent != 0.33 || acc.Usage.SecondaryUsedPercent != 0.66 {
		t.Fatalf("usage=%+v", acc.Usage)
	}
	if acc.Usage.Source != "restored_from_totals" {
		t.Fatalf("usage source=%q", acc.Usage.Source)
	}
	if !acc.Usage.RetrievedAt.Equal(now) {
		t.Fatalf("retrieved_at=%v", acc.Usage.RetrievedAt)
	}
}

func TestRestorePersistedUsageStatePrefersNewerTotalsWhenSnapshotStale(t *testing.T) {
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 7)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Second)
	snapshot := UsageSnapshot{
		PrimaryUsed:          0.11,
		SecondaryUsed:        0.22,
		PrimaryUsedPercent:   0.11,
		SecondaryUsedPercent: 0.22,
		SecondaryResetAt:     now.Add(12 * time.Hour),
		RetrievedAt:          now,
		Source:               "headers",
	}
	if err := store.saveAccountUsageSnapshot("seat-c", snapshot); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	if err := store.record(RequestUsage{
		AccountID:        "seat-c",
		InputTokens:      9,
		OutputTokens:     6,
		BillableTokens:   15,
		PrimaryUsedPct:   0.35,
		SecondaryUsedPct: 0.91,
		Timestamp:        now.Add(2 * time.Minute),
		RequestID:        "req-3",
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	acc := &Account{ID: "seat-c", Type: AccountTypeCodex}
	restoredTotals, restoredSnapshots, bridged := restorePersistedUsageState([]*Account{acc}, store)

	if restoredTotals != 1 || restoredSnapshots != 1 || bridged != 1 {
		t.Fatalf("restore counts = (%d, %d, %d)", restoredTotals, restoredSnapshots, bridged)
	}
	if acc.Usage.PrimaryUsedPercent != 0.35 || acc.Usage.SecondaryUsedPercent != 0.91 {
		t.Fatalf("usage=%+v", acc.Usage)
	}
	if acc.Usage.Source != "restored_from_totals" {
		t.Fatalf("usage source=%q", acc.Usage.Source)
	}
	if !acc.Usage.RetrievedAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("retrieved_at=%v", acc.Usage.RetrievedAt)
	}
	if !acc.Usage.SecondaryResetAt.Equal(now.Add(12 * time.Hour)) {
		t.Fatalf("secondary_reset_at=%v", acc.Usage.SecondaryResetAt)
	}
}
