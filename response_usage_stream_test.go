package main

import (
	"bytes"
	"net/url"
	"path/filepath"
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
