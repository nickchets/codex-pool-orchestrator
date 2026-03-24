package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	longDeadAccountQuarantineAfter = 72 * time.Hour
	longDeadCleanupPollInterval    = 15 * time.Minute
	quarantineSubdir               = "quarantine"
	quarantineRecentLimit          = 8
)

type QuarantineEntry struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	QuarantinedAt string `json:"quarantined_at,omitempty"`
}

type QuarantineStatus struct {
	Total     int               `json:"total"`
	Providers map[string]int    `json:"providers,omitempty"`
	Recent    []QuarantineEntry `json:"recent,omitempty"`
}

func setAccountDeadStateLocked(a *Account, dead bool, now time.Time) {
	if a == nil {
		return
	}
	a.Dead = dead
	if dead {
		if a.DeadSince.IsZero() {
			a.DeadSince = now.UTC()
		}
		return
	}
	a.DeadSince = time.Time{}
}

func markAccountDeadStateLocked(a *Account, now time.Time, penaltyDelta float64) {
	if a == nil {
		return
	}
	setAccountDeadStateLocked(a, true, now)
	a.Penalty += penaltyDelta
}

func clearAccountDeadStateLocked(a *Account, now time.Time, resetPenalty bool) {
	if a == nil {
		return
	}
	setAccountDeadStateLocked(a, false, now)
	if resetPenalty {
		a.Penalty = 0
	}
}

func normalizeLoadedDeadState(a *Account) {
	if a == nil {
		return
	}
	if !a.Dead {
		a.DeadSince = time.Time{}
		return
	}
	if a.DeadSince.IsZero() {
		a.DeadSince = inferDeadSince(a)
	}
}

func inferDeadSince(a *Account) time.Time {
	if a == nil {
		return time.Time{}
	}
	for _, candidate := range []time.Time{a.HealthCheckedAt, a.LastRefresh, a.RateLimitUntil} {
		if !candidate.IsZero() {
			return candidate.UTC()
		}
	}
	return time.Time{}
}

func shouldQuarantineAccount(a *Account, now time.Time) bool {
	if a == nil || !a.Dead || a.DeadSince.IsZero() {
		return false
	}
	return !a.DeadSince.After(now.Add(-longDeadAccountQuarantineAfter))
}

func quarantineAccountFile(poolDir string, a *Account, now time.Time) error {
	if a == nil || strings.TrimSpace(poolDir) == "" || strings.TrimSpace(a.File) == "" {
		return nil
	}

	rel, err := filepath.Rel(poolDir, a.File)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil
	}
	if rel == quarantineSubdir || strings.HasPrefix(rel, quarantineSubdir+string(os.PathSeparator)) {
		return nil
	}

	dest := filepath.Join(poolDir, quarantineSubdir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		base := strings.TrimSuffix(filepath.Base(dest), filepath.Ext(dest))
		dest = filepath.Join(filepath.Dir(dest), base+"-"+now.UTC().Format("20060102T150405Z")+".json")
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.Rename(a.File, dest); err != nil {
		return err
	}
	_ = os.Chtimes(dest, now, now)
	return nil
}

func loadQuarantineStatus(poolDir string, now time.Time) QuarantineStatus {
	status := QuarantineStatus{
		Providers: make(map[string]int),
	}
	if strings.TrimSpace(poolDir) == "" {
		return status
	}

	root := filepath.Join(poolDir, quarantineSubdir)
	entries := make([]QuarantineEntry, 0)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		provider := "unknown"
		if parts := strings.Split(rel, string(os.PathSeparator)); len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			provider = parts[0]
		}
		info, statErr := os.Stat(path)
		quarantinedAt := ""
		if statErr == nil {
			quarantinedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		status.Total++
		status.Providers[provider]++
		entries = append(entries, QuarantineEntry{
			ID:            strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			Provider:      provider,
			QuarantinedAt: quarantinedAt,
		})
		return nil
	})

	sort.Slice(entries, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339, entries[i].QuarantinedAt)
		right, rightErr := time.Parse(time.RFC3339, entries[j].QuarantinedAt)
		if leftErr != nil || rightErr != nil {
			return entries[i].QuarantinedAt > entries[j].QuarantinedAt
		}
		return left.After(right)
	})
	if len(entries) > quarantineRecentLimit {
		entries = entries[:quarantineRecentLimit]
	}
	status.Recent = entries
	if status.Total == 0 {
		status.Providers = nil
	}
	return status
}

func (h *proxyHandler) startDeadAccountCleanupPoller() {
	if h == nil || h.pool == nil || strings.TrimSpace(h.cfg.poolDir) == "" {
		return
	}

	ticker := time.NewTicker(longDeadCleanupPollInterval)
	go func() {
		for range ticker.C {
			moved, err := h.quarantineLongDeadAccounts(time.Now().UTC())
			if err != nil {
				log.Printf("warning: long-dead account cleanup failed: %v", err)
				continue
			}
			if moved > 0 {
				log.Printf("quarantined %d long-dead account file(s)", moved)
			}
		}
	}()
}

func (h *proxyHandler) quarantineLongDeadAccounts(now time.Time) (int, error) {
	if h == nil || h.pool == nil || strings.TrimSpace(h.cfg.poolDir) == "" {
		return 0, nil
	}

	h.pool.mu.RLock()
	accs := append([]*Account(nil), h.pool.accounts...)
	h.pool.mu.RUnlock()

	moved := 0
	for _, a := range accs {
		if a == nil {
			continue
		}

		a.mu.Lock()
		if a.Inflight > 0 || !shouldQuarantineAccount(a, now) {
			a.mu.Unlock()
			continue
		}
		err := quarantineAccountFile(h.cfg.poolDir, a, now)
		a.mu.Unlock()
		if err != nil {
			return moved, err
		}
		moved++
	}

	if moved > 0 {
		h.reloadAccounts()
	}
	return moved, nil
}
