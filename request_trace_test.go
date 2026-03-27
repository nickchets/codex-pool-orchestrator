package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type blockingReadCloser struct {
	done chan struct{}
}

func (r *blockingReadCloser) Read(p []byte) (int, error) {
	<-r.done
	return 0, contextCanceledError{}
}

func (r *blockingReadCloser) Close() error {
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	return nil
}

type contextCanceledError struct{}

func (contextCanceledError) Error() string { return "context canceled" }

func captureLogs(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()

	fn()
	return buf.String()
}

func testTraceContext(reqID string) context.Context {
	trace := &requestTrace{
		cfg:       requestTraceConfig{requests: true},
		reqID:     reqID,
		startedAt: time.Now(),
	}
	return context.WithValue(context.Background(), requestTraceContextKey{}, trace)
}

func TestRequestTraceTracksChunkGapAndIdleTimeout(t *testing.T) {
	trace := &requestTrace{
		cfg:       requestTraceConfig{requests: true, packets: true, stallGap: 5 * time.Millisecond},
		reqID:     "req-gap",
		startedAt: time.Now(),
	}

	trace.noteResponseChunk(32)
	time.Sleep(8 * time.Millisecond)
	trace.noteResponseChunk(16)
	trace.noteIdleTimeout(25 * time.Millisecond)

	if trace.maxChunkGap < 5*time.Millisecond {
		t.Fatalf("max_chunk_gap=%v", trace.maxChunkGap)
	}
	if !trace.idleTimedOut {
		t.Fatal("expected idle timeout to be recorded")
	}
	if trace.idleTimeoutDuration != 25*time.Millisecond {
		t.Fatalf("idle_timeout_duration=%v", trace.idleTimeoutDuration)
	}
}

func TestIdleTimeoutReaderReturnsHelpfulIdleTimeout(t *testing.T) {
	rc := &blockingReadCloser{done: make(chan struct{})}
	cancelCalled := make(chan struct{}, 1)
	timeoutCalled := make(chan struct{}, 1)

	reader := newIdleTimeoutReader(rc, 15*time.Millisecond, func() {
		select {
		case cancelCalled <- struct{}{}:
		default:
		}
		_ = rc.Close()
	}, func() {
		select {
		case timeoutCalled <- struct{}{}:
		default:
		}
	})
	defer reader.Close()

	_, err := reader.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected idle timeout error")
	}
	if !strings.Contains(err.Error(), "idle for") {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case <-timeoutCalled:
	default:
		t.Fatal("expected timeout callback to fire")
	}

	select {
	case <-cancelCalled:
	default:
		t.Fatal("expected cancel callback to fire")
	}
}

func TestNewRequestTracePrefersPoolHeadersAndFallsBackToLegacyHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set(localTraceHeaderID, "pool-trace")
	req.Header.Set(localTraceHeaderMode, "codex")
	req.Header.Set(localTraceHeaderStartedAt, "2026-03-25T12:00:00Z")
	req.Header.Set(localTraceHeaderOutputFormat, "json")
	req.Header.Set(legacyLocalTraceHeaderID, "legacy-trace")

	trace := newRequestTrace(config{traceRequests: true}, "req-primary", req)
	if trace == nil {
		t.Fatal("expected trace")
	}
	if trace.wrapperTraceID != "pool-trace" {
		t.Fatalf("wrapperTraceID=%q", trace.wrapperTraceID)
	}
	if trace.wrapperMode != "codex" {
		t.Fatalf("wrapperMode=%q", trace.wrapperMode)
	}

	legacyReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	legacyReq.Header.Set(legacyLocalTraceHeaderID, "legacy-only")
	legacyReq.Header.Set(legacyLocalTraceHeaderMode, "claude")
	legacyReq.Header.Set(legacyLocalTraceHeaderStartedAt, "2026-03-24T11:00:00Z")
	legacyReq.Header.Set(legacyLocalTraceHeaderOutputFormat, "text")

	legacyTrace := newRequestTrace(config{traceRequests: true}, "req-legacy", legacyReq)
	if legacyTrace == nil {
		t.Fatal("expected legacy trace")
	}
	if legacyTrace.wrapperTraceID != "legacy-only" {
		t.Fatalf("legacy wrapperTraceID=%q", legacyTrace.wrapperTraceID)
	}
	if legacyTrace.wrapperMode != "claude" {
		t.Fatalf("legacy wrapperMode=%q", legacyTrace.wrapperMode)
	}
	if legacyTrace.wrapperOutputFormat != "text" {
		t.Fatalf("legacy wrapperOutputFormat=%q", legacyTrace.wrapperOutputFormat)
	}
}

func TestStripLocalTraceHeadersRemovesPoolAndLegacyHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set(localTraceHeaderID, "pool-trace")
	headers.Set(localTraceHeaderMode, "gemini")
	headers.Set(localTraceHeaderStartedAt, "now")
	headers.Set(localTraceHeaderOutputFormat, "json")
	headers.Set(legacyLocalTraceHeaderID, "legacy-trace")
	headers.Set(legacyLocalTraceHeaderMode, "claude")
	headers.Set(legacyLocalTraceHeaderStartedAt, "earlier")
	headers.Set(legacyLocalTraceHeaderOutputFormat, "text")

	stripLocalTraceHeaders(headers)

	for _, name := range []string{
		localTraceHeaderID,
		localTraceHeaderMode,
		localTraceHeaderStartedAt,
		localTraceHeaderOutputFormat,
		legacyLocalTraceHeaderID,
		legacyLocalTraceHeaderMode,
		legacyLocalTraceHeaderStartedAt,
		legacyLocalTraceHeaderOutputFormat,
	} {
		if got := headers.Get(name); got != "" {
			t.Fatalf("%s still present: %q", name, got)
		}
	}
}

func TestRequestTraceRedactsNonUsageSSEPayloadSamples(t *testing.T) {
	trace := &requestTrace{
		cfg:       requestTraceConfig{requests: true, packets: true, payloads: true, payloadLimit: 256},
		reqID:     "req-sse-redacted",
		startedAt: time.Now(),
	}

	logs := captureLogs(t, func() {
		trace.noteSSEEvent([]byte(`{"type":"response.output_text.delta","delta":"top-secret-text"}`), false)
	})

	if !strings.Contains(logs, `trace sse_payload type="response.output_text.delta" sample=[redacted non-usage event]`) {
		t.Fatalf("missing redacted sse payload log: %s", logs)
	}
	if strings.Contains(logs, "top-secret-text") {
		t.Fatalf("unexpected raw SSE payload in logs: %s", logs)
	}
}

func TestRequestTraceKeepsUsageSSEPayloadSamples(t *testing.T) {
	trace := &requestTrace{
		cfg:       requestTraceConfig{requests: true, packets: true, payloads: true, payloadLimit: 256},
		reqID:     "req-sse-usage",
		startedAt: time.Now(),
	}

	logs := captureLogs(t, func() {
		trace.noteSSEUsageEvent([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7}}}`))
	})

	if !strings.Contains(logs, `trace sse_usage_payload type="response.completed"`) {
		t.Fatalf("missing usage payload trace log: %s", logs)
	}
	if !strings.Contains(logs, `"input_tokens":11`) || !strings.Contains(logs, `"output_tokens":7`) {
		t.Fatalf("missing usage payload sample: %s", logs)
	}
}
