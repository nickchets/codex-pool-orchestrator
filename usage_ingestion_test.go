package main

import (
	"net/url"
	"testing"
	"time"
)

func TestParseCodexUsageDeltaTokenCountCapturesUsageAndSnapshot(t *testing.T) {
	delta := parseCodexUsageDelta(map[string]any{
		"type": "token_count",
		"info": map[string]any{
			"last_token_usage": map[string]any{
				"input_tokens":            100.0,
				"cached_input_tokens":     40.0,
				"output_tokens":           10.0,
				"reasoning_output_tokens": 2.0,
			},
		},
		"rate_limits": map[string]any{
			"primary":   map[string]any{"used_percent": 25.0},
			"secondary": map[string]any{"used_percent": 50.0},
		},
	})

	if delta.Usage == nil {
		t.Fatal("expected usage")
	}
	if delta.Snapshot == nil {
		t.Fatal("expected snapshot")
	}
	if delta.Usage.BillableTokens != 70 {
		t.Fatalf("billable=%d", delta.Usage.BillableTokens)
	}
	if delta.Usage.PrimaryUsedPct != 0.25 || delta.Usage.SecondaryUsedPct != 0.50 {
		t.Fatalf("usage pcts=%+v", delta.Usage)
	}
	if delta.Snapshot.Source != "token_count" {
		t.Fatalf("snapshot source=%q", delta.Snapshot.Source)
	}
}

func TestParseCodexUsageDeltaResponseWrapperCapturesLegacyRateLimit(t *testing.T) {
	delta := parseCodexUsageDelta(map[string]any{
		"response": map[string]any{
			"prompt_cache_key": "pc-1",
			"usage": map[string]any{
				"input_tokens":        120.0,
				"cached_input_tokens": 20.0,
				"output_tokens":       5.0,
			},
			"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 30.0},
				"secondary_window": map[string]any{"used_percent": 44.0},
			},
		},
	})

	if delta.Usage == nil {
		t.Fatal("expected usage")
	}
	if delta.Snapshot == nil {
		t.Fatal("expected snapshot")
	}
	if delta.Usage.PromptCacheKey != "pc-1" {
		t.Fatalf("prompt_cache_key=%q", delta.Usage.PromptCacheKey)
	}
	if delta.Usage.BillableTokens != 105 {
		t.Fatalf("billable=%d", delta.Usage.BillableTokens)
	}
	if delta.Snapshot.PrimaryUsedPercent != 0.30 || delta.Snapshot.SecondaryUsedPercent != 0.44 {
		t.Fatalf("snapshot=%+v", delta.Snapshot)
	}
	if delta.Snapshot.Source != "body" {
		t.Fatalf("snapshot source=%q", delta.Snapshot.Source)
	}
}

func TestApplyUsageSnapshotDoesNotCarryExpiredResetAcrossTokenCount(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	acc := &Account{
		ID:       "seat-a",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryUsedPercent:   0.91,
			SecondaryUsedPercent: 0.94,
			PrimaryResetAt:       now.Add(-10 * time.Minute),
			SecondaryResetAt:     now.Add(-2 * time.Hour),
			RetrievedAt:          now.Add(-30 * time.Minute),
			Source:               "headers",
		},
	}

	applyUsageSnapshot(acc, &UsageSnapshot{
		PrimaryUsedPercent:   0.05,
		SecondaryUsedPercent: 0.15,
		RetrievedAt:          now,
		Source:               "token_count",
	})

	acc.mu.Lock()
	snapshot := acc.Usage
	acc.mu.Unlock()

	if !snapshot.PrimaryResetAt.IsZero() || !snapshot.SecondaryResetAt.IsZero() {
		t.Fatalf("expected expired resets to be cleared, got %+v", snapshot)
	}
	primaryUsed, secondaryUsed := effectiveUsageForRouting(snapshot, now.Add(time.Second))
	if primaryUsed != 0.05 || secondaryUsed != 0.15 {
		t.Fatalf("effective usage = (%.2f, %.2f)", primaryUsed, secondaryUsed)
	}
}

func TestParseOpenAIUsagePayloadSupportsPromptAndCompletionAliases(t *testing.T) {
	ru := parseOpenAIUsagePayload(map[string]any{
		"usage": map[string]any{
			"prompt_tokens":     25.0,
			"completion_tokens": 7.0,
			"cached_tokens":     5.0,
		},
	})

	if ru == nil {
		t.Fatal("expected usage")
	}
	if ru.InputTokens != 25 || ru.OutputTokens != 7 || ru.CachedInputTokens != 5 {
		t.Fatalf("usage=%+v", ru)
	}
	if ru.BillableTokens != 27 {
		t.Fatalf("billable=%d", ru.BillableTokens)
	}
}

func TestParseUsageEventObjectAcceptsJSONArrayEnvelope(t *testing.T) {
	obj, ok := parseUsageEventObject([]byte(`[{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}]`))
	if !ok {
		t.Fatal("expected object")
	}
	if _, ok := obj["usageMetadata"].(map[string]any); !ok {
		t.Fatalf("obj=%+v", obj)
	}
}

func TestClaudeProviderParseUsageSupportsNonStreamMessagePayload(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)

	ru := provider.ParseUsage(map[string]any{
		"type":  "message",
		"model": "claude-sonnet-4-20250514",
		"usage": map[string]any{
			"input_tokens":            11.0,
			"cache_read_input_tokens": 2.0,
			"output_tokens":           4.0,
		},
	})

	if ru == nil {
		t.Fatal("expected usage")
	}
	if ru.InputTokens != 11 || ru.CachedInputTokens != 2 || ru.OutputTokens != 4 {
		t.Fatalf("usage=%+v", ru)
	}
	if ru.BillableTokens != 13 {
		t.Fatalf("billable=%d", ru.BillableTokens)
	}
}

func TestUpdateUsageFromBodyRecordsClaudeNonStreamMessage(t *testing.T) {
	baseURL, _ := url.Parse("https://claude.example.com")
	provider := NewClaudeProvider(baseURL)
	h := &proxyHandler{}
	acc := &Account{
		ID:       "claude_gitlab_test",
		Type:     AccountTypeClaude,
		PlanType: "gitlab_duo",
	}

	h.updateUsageFromBody(provider, acc, "pool-user-1", 0, 0, []byte(`{"type":"message","model":"claude-sonnet-4-20250514","usage":{"input_tokens":11,"cache_read_input_tokens":2,"output_tokens":4}}`))

	if acc.Totals.RequestCount != 1 {
		t.Fatalf("request_count=%d", acc.Totals.RequestCount)
	}
	if acc.Totals.TotalInputTokens != 11 || acc.Totals.TotalCachedTokens != 2 || acc.Totals.TotalOutputTokens != 4 {
		t.Fatalf("totals=%+v", acc.Totals)
	}
	if acc.Totals.TotalBillableTokens != 13 {
		t.Fatalf("billable=%d", acc.Totals.TotalBillableTokens)
	}
}
