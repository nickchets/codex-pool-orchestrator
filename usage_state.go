package main

import (
	"log"
)

func bridgeUsageFromTotals(a *Account) bool {
	if a == nil {
		return false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	primaryPct := a.Totals.LastPrimaryPct
	secondaryPct := a.Totals.LastSecondaryPct
	if primaryPct <= 0 && secondaryPct <= 0 {
		return false
	}
	if !a.Usage.RetrievedAt.IsZero() {
		if a.Totals.LastUpdated.IsZero() || !a.Totals.LastUpdated.After(a.Usage.RetrievedAt) {
			return false
		}
	}

	bridge := UsageSnapshot{
		PrimaryUsed:          primaryPct,
		SecondaryUsed:        secondaryPct,
		PrimaryUsedPercent:   primaryPct,
		SecondaryUsedPercent: secondaryPct,
		RetrievedAt:          a.Totals.LastUpdated,
		Source:               "restored_from_totals",
	}
	a.Usage = mergeUsage(a.Usage, bridge)
	return true
}

func persistUsageSnapshot(store *usageStore, a *Account) {
	if store == nil || a == nil {
		return
	}

	a.mu.Lock()
	accountID := a.ID
	snapshot := a.Usage
	a.mu.Unlock()

	if accountID == "" {
		return
	}
	if snapshot.RetrievedAt.IsZero() &&
		snapshot.PrimaryUsedPercent == 0 &&
		snapshot.SecondaryUsedPercent == 0 &&
		snapshot.PrimaryResetAt.IsZero() &&
		snapshot.SecondaryResetAt.IsZero() &&
		snapshot.Source == "" {
		return
	}
	if err := store.saveAccountUsageSnapshot(accountID, snapshot); err != nil {
		log.Printf("warning: failed to persist usage snapshot for %s: %v", accountID, err)
	}
}

func restorePersistedUsageState(accs []*Account, store *usageStore) (int, int, int) {
	if len(accs) == 0 || store == nil {
		return 0, 0, 0
	}

	totals, err := store.loadAllAccountUsage()
	if err != nil {
		log.Printf("warning: failed to restore persisted account totals: %v", err)
	}
	snapshots, err := store.loadAllAccountUsageSnapshots()
	if err != nil {
		log.Printf("warning: failed to restore persisted account usage snapshots: %v", err)
	}

	restoredTotals := 0
	restoredSnapshots := 0
	bridged := 0
	for _, a := range accs {
		if a == nil {
			continue
		}
		if snapshot, ok := snapshots[a.ID]; ok {
			a.mu.Lock()
			a.Usage = mergeUsage(a.Usage, snapshot)
			a.mu.Unlock()
			restoredSnapshots++
		}
		if usage, ok := totals[a.ID]; ok {
			a.mu.Lock()
			a.Totals = usage
			a.mu.Unlock()
			restoredTotals++
		}
		if bridgeUsageFromTotals(a) {
			bridged++
		}
	}
	return restoredTotals, restoredSnapshots, bridged
}
