package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestClassifyManagedOpenAIAPISSEErrorMarksQuotaExhaustedKeyDead(t *testing.T) {
	data := []byte(`{"type":"response.failed","response":{"status":"failed","error":{"message":"You exceeded your current quota, please check your plan and billing details.","code":"insufficient_quota"}}}`)

	disposition, ok := classifyManagedOpenAIAPISSEError(data)
	if !ok {
		t.Fatal("expected SSE quota failure to be classified as retryable")
	}
	if !disposition.Retry {
		t.Fatalf("expected retryable disposition, got %+v", disposition)
	}
	if !disposition.MarkDead {
		t.Fatalf("expected quota failure to mark key dead, got %+v", disposition)
	}
	if disposition.RateLimit {
		t.Fatalf("expected quota failure to avoid rate-limit disposition, got %+v", disposition)
	}
	if disposition.Reason == "" {
		t.Fatalf("expected non-empty reason, got %+v", disposition)
	}
}

func TestClassifyManagedOpenAIAPISSEErrorTreatsUsageLimitMessageAsRateLimit(t *testing.T) {
	data := []byte(`{"type":"response.failed","response":{"status":"failed","error":{"message":"You've hit your usage limit. To get more access now, send a request to your admin or try again at 4:56 PM."}}}`)

	disposition, ok := classifyManagedOpenAIAPISSEError(data)
	if !ok {
		t.Fatal("expected usage-limit failure to be classified as retryable")
	}
	if !disposition.RateLimit {
		t.Fatalf("expected usage-limit failure to be treated as rate-limited, got %+v", disposition)
	}
	if disposition.MarkDead {
		t.Fatalf("expected usage-limit failure to avoid dead disposition, got %+v", disposition)
	}
}

func TestClassifyManagedOpenAIAPISSEErrorIgnoresUnsupportedParameter(t *testing.T) {
	data := []byte(`{"type":"error","error":{"message":"Unsupported parameter: 'max_output_tokens' is not supported with this model.","type":"invalid_request_error","code":"unsupported_parameter"}}`)

	disposition, ok := classifyManagedOpenAIAPISSEError(data)
	if ok {
		t.Fatalf("expected non-retryable invalid request to be ignored, got %+v", disposition)
	}
	if disposition.MarkDead || disposition.RateLimit || disposition.Retry {
		t.Fatalf("expected no dead/rate-limit/retry disposition, got %+v", disposition)
	}
	if disposition.Reason == "" {
		t.Fatalf("expected non-empty reason, got %+v", disposition)
	}
}

func TestSSEInterceptWriterEventCallbackReceivesNonUsageEvents(t *testing.T) {
	var forwarded bytes.Buffer
	var events [][]byte
	var usageEvents [][]byte
	writer := &sseInterceptWriter{
		w: &forwarded,
		eventCallback: func(data []byte) {
			events = append(events, append([]byte(nil), data...))
		},
		callback: func(data []byte) {
			usageEvents = append(usageEvents, append([]byte(nil), data...))
		},
	}

	chunks := [][]byte{
		[]byte("event: error\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"quota exceeded\",\"code\":\"insufficient_quota\"}}}\n\n"),
		[]byte("event: message\ndata: {\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}\n\n"),
	}
	for _, chunk := range chunks {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatalf("write chunk: %v", err)
		}
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 intercepted SSE events, got %d", len(events))
	}
	if len(usageEvents) != 1 {
		t.Fatalf("expected only usage event to hit usage callback, got %d", len(usageEvents))
	}
	if !bytes.Contains(events[0], []byte(`"response.failed"`)) {
		t.Fatalf("expected first event callback payload to contain response.failed, got %s", string(events[0]))
	}
	if !bytes.Contains(usageEvents[0], []byte(`"usage"`)) {
		t.Fatalf("expected usage callback payload, got %s", string(usageEvents[0]))
	}
}

func TestApplyManagedOpenAIAPIProbeTransportErrorCapsPenalty(t *testing.T) {
	now := time.Date(2026, 3, 25, 15, 0, 0, 0, time.UTC)
	acc := &Account{Penalty: 1.4}

	applyManagedOpenAIAPIProbeTransportError(acc, context.DeadlineExceeded, now)

	if acc.HealthStatus != "error" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != "context deadline exceeded" {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if !acc.HealthCheckedAt.Equal(now) {
		t.Fatalf("health_checked_at=%v", acc.HealthCheckedAt)
	}
	if acc.Penalty != managedOpenAIAPITransientPenaltyCap {
		t.Fatalf("penalty=%v", acc.Penalty)
	}
	if !acc.LastPenalty.Equal(now) {
		t.Fatalf("last_penalty=%v", acc.LastPenalty)
	}
}

func TestApplyManagedOpenAIAPIDispositionCapsGenericPenalty(t *testing.T) {
	now := time.Date(2026, 3, 25, 15, 5, 0, 0, time.UTC)
	acc := &Account{Penalty: 1.4}

	applyManagedOpenAIAPIDisposition(acc, managedOpenAIAPIErrorDisposition{Reason: "upstream unavailable"}, nil, now)

	if acc.HealthStatus != "error" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if acc.HealthError != "upstream unavailable" {
		t.Fatalf("health_error=%q", acc.HealthError)
	}
	if !acc.HealthCheckedAt.Equal(now) {
		t.Fatalf("health_checked_at=%v", acc.HealthCheckedAt)
	}
	if acc.Penalty != managedOpenAIAPITransientPenaltyCap {
		t.Fatalf("penalty=%v", acc.Penalty)
	}
	if !acc.LastPenalty.Equal(now) {
		t.Fatalf("last_penalty=%v", acc.LastPenalty)
	}
}
