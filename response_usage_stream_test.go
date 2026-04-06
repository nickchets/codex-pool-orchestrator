package main

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWrapUsageInterceptWriterAppliesCodexSnapshot(t *testing.T) {
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 7)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	h := &proxyHandler{store: store}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{ID: "seat-a", Type: AccountTypeCodex, PlanType: "team"}
	managedStreamFailed := false
	var managedStreamFailureOnce sync.Once
	var forwarded bytes.Buffer

	writer := h.wrapUsageInterceptWriter(
		"req-1",
		&forwarded,
		provider,
		acc,
		"user-1",
		nil,
		0,
		0,
		&managedStreamFailed,
		&managedStreamFailureOnce,
	)

	chunk := []byte("event: message\ndata: {\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"input_tokens\":100,\"cached_input_tokens\":40,\"output_tokens\":10}},\"rate_limits\":{\"primary\":{\"used_percent\":25},\"secondary\":{\"used_percent\":50}}}\n\n")
	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("write sse chunk: %v", err)
	}

	if acc.Usage.PrimaryUsedPercent != 0.25 || acc.Usage.SecondaryUsedPercent != 0.50 {
		t.Fatalf("usage=%+v", acc.Usage)
	}
	if acc.Totals.RequestCount != 1 {
		t.Fatalf("request_count=%d", acc.Totals.RequestCount)
	}
	if acc.Totals.LastPrimaryPct != 0.25 || acc.Totals.LastSecondaryPct != 0.50 {
		t.Fatalf("totals=%+v", acc.Totals)
	}

	snapshots, err := store.loadAllAccountUsageSnapshots()
	if err != nil {
		t.Fatalf("load snapshots: %v", err)
	}
	snapshot, ok := snapshots["seat-a"]
	if !ok {
		t.Fatal("expected persisted usage snapshot")
	}
	if snapshot.PrimaryUsedPercent != 0.25 || snapshot.SecondaryUsedPercent != 0.50 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestWrapUsageInterceptWriterRecordsTraceEvents(t *testing.T) {
	store, err := newUsageStore(filepath.Join(t.TempDir(), "usage.db"), 7)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	h := &proxyHandler{store: store}
	provider := NewClaudeProvider(baseURL)
	acc := &Account{ID: "claude-seat", Type: AccountTypeClaude}
	trace := &requestTrace{
		cfg:       requestTraceConfig{packets: true},
		reqID:     "req-trace",
		startedAt: time.Now(),
	}
	managedStreamFailed := false
	var managedStreamFailureOnce sync.Once
	var forwarded bytes.Buffer

	writer := h.wrapUsageInterceptWriter(
		"req-trace",
		&forwarded,
		provider,
		acc,
		"user-1",
		trace,
		0,
		0,
		&managedStreamFailed,
		&managedStreamFailureOnce,
	)

	chunk := []byte("event: message\ndata: {\"type\":\"message_start\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":4}}\n\nevent: message\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n")
	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("write sse chunk: %v", err)
	}

	if trace.sseEvents != 2 {
		t.Fatalf("sse_events=%d", trace.sseEvents)
	}
	if trace.usageEvents != 1 {
		t.Fatalf("usage_events=%d", trace.usageEvents)
	}
}

func TestWrapUsageInterceptWriterMarksLocalCodexUsageLimitAsStreamFailure(t *testing.T) {
	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	accFile := filepath.Join(t.TempDir(), "codex_oauth.json")
	if err := os.WriteFile(accFile, []byte(`{"tokens":{"access_token":"seed-access","refresh_token":"seed-refresh"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	resetAt := time.Now().UTC().Add(45 * time.Minute).Truncate(time.Second)
	h := &proxyHandler{}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{
		ID:           "seat-a",
		Type:         AccountTypeCodex,
		File:         accFile,
		AccessToken:  "seed-access",
		RefreshToken: "seed-refresh",
		PlanType:     "team",
		Usage: UsageSnapshot{
			PrimaryResetAt: resetAt,
		},
	}
	managedStreamFailed := false
	var managedStreamFailureOnce sync.Once
	var forwarded bytes.Buffer

	writer := h.wrapUsageInterceptWriter(
		"req-codex-usage-limit",
		&forwarded,
		provider,
		acc,
		"user-1",
		nil,
		0,
		0,
		&managedStreamFailed,
		&managedStreamFailureOnce,
	)

	chunk := []byte("event: error\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"You've hit your usage limit. To get more access now, send a request to your admin or try again at 4:56 PM.\"}}}\n\n")
	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("write sse chunk: %v", err)
	}

	if !managedStreamFailed {
		t.Fatal("expected local Codex usage-limit event to mark the stream as failed")
	}
	if acc.HealthStatus != "rate_limited" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.Equal(resetAt) {
		t.Fatalf("rate_limit_until=%v want %v", acc.RateLimitUntil, resetAt)
	}
	if !strings.Contains(strings.ToLower(acc.HealthError), "usage limit") {
		t.Fatalf("health_error=%q", acc.HealthError)
	}

	saved, err := os.ReadFile(accFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if !strings.Contains(string(saved), "\"rate_limit_until\"") {
		t.Fatalf("expected persisted cooldown in auth file: %s", string(saved))
	}
}

func TestClaudePingTailWatcherCutsOffGitLabPingOnlyTail(t *testing.T) {
	trace := &requestTrace{
		cfg:       requestTraceConfig{requests: true},
		reqID:     "req-claude-tail",
		startedAt: time.Now(),
	}
	watcher := newClaudePingTailWatcher("claude_gitlab_test", trace, 18*time.Second)
	if watcher == nil {
		t.Fatal("expected watcher")
	}
	watcher.sawContentDelta = true
	watcher.sawContentBlockStop = true
	watcher.lastNonPingAt = time.Now().Add(-21 * time.Second)
	watcher.lastNonPingType = "content_block_delta"

	err := watcher.noteEvent("ping")
	var cutoff *claudePingTailCutoffError
	if !errors.As(err, &cutoff) {
		t.Fatalf("expected ping tail cutoff, got %v", err)
	}
	if cutoff.accountID != "claude_gitlab_test" {
		t.Fatalf("cutoff=%+v", cutoff)
	}
}

func TestClaudePingTailWatcherDoesNotCutBeforeContentStop(t *testing.T) {
	watcher := newClaudePingTailWatcher("claude_gitlab_test", nil, 18*time.Second)
	if watcher == nil {
		t.Fatal("expected watcher")
	}
	watcher.sawContentDelta = true
	watcher.lastNonPingAt = time.Now().Add(-30 * time.Second)
	watcher.lastNonPingType = "content_block_delta"

	if err := watcher.noteEvent("ping"); err != nil {
		t.Fatalf("unexpected cutoff without content_block_stop: %v", err)
	}
}

func TestClaudePingTailWatcherDoesNotCutAfterMessageStop(t *testing.T) {
	watcher := newClaudePingTailWatcher("claude_gitlab_test", nil, 18*time.Second)
	if watcher == nil {
		t.Fatal("expected watcher")
	}
	watcher.sawContentDelta = true
	watcher.sawContentBlockStop = true
	watcher.sawMessageStop = true
	watcher.lastNonPingAt = time.Now().Add(-30 * time.Second)
	watcher.lastNonPingType = "message_delta"

	if err := watcher.noteEvent("ping"); err != nil {
		t.Fatalf("unexpected cutoff after message_stop: %v", err)
	}
}

func TestClaudePingTailWatcherResetsTimerAfterNonPingEvent(t *testing.T) {
	watcher := newClaudePingTailWatcher("claude_gitlab_test", nil, 18*time.Second)
	if watcher == nil {
		t.Fatal("expected watcher")
	}
	watcher.sawContentDelta = true
	watcher.sawContentBlockStop = true
	watcher.lastNonPingAt = time.Now().Add(-30 * time.Second)
	watcher.lastNonPingType = "content_block_delta"

	if err := watcher.noteEvent("message_delta"); err != nil {
		t.Fatalf("unexpected non-ping event error: %v", err)
	}
	if err := watcher.noteEvent("ping"); err != nil {
		t.Fatalf("unexpected cutoff immediately after non-ping event: %v", err)
	}
}
