package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

type streamUsageTracker struct {
	mu sync.Mutex

	bytesWritten  int64
	eventsWritten int64
	firstByteAt   time.Time

	outputTextBytes            int64
	authoritativeUsageRecorded bool
	readErr                    error
}

type streamUsageSnapshot struct {
	BytesWritten               int64
	EventsWritten              int64
	FirstByteAt                time.Time
	OutputTextBytes            int64
	AuthoritativeUsageRecorded bool
	ReadErr                    error
}

func newStreamUsageTracker() *streamUsageTracker {
	return &streamUsageTracker{}
}

func (t *streamUsageTracker) noteWrite(n int) {
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	if t.firstByteAt.IsZero() {
		t.firstByteAt = time.Now().UTC()
	}
	t.bytesWritten += int64(n)
	t.mu.Unlock()
}

func (t *streamUsageTracker) noteRead(err error) {
	if t == nil || err == nil || errors.Is(err, io.EOF) {
		return
	}
	t.mu.Lock()
	if t.readErr == nil {
		t.readErr = err
	}
	t.mu.Unlock()
}

func (t *streamUsageTracker) noteSSEEvent(data []byte) {
	if t == nil {
		return
	}
	textBytes := int64(len([]byte(extractOutputTextFromSSEData(data))))
	t.mu.Lock()
	t.eventsWritten++
	if textBytes > 0 {
		t.outputTextBytes += textBytes
	}
	t.mu.Unlock()
}

func (t *streamUsageTracker) markAuthoritativeUsage() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.authoritativeUsageRecorded = true
	t.mu.Unlock()
}

func (t *streamUsageTracker) snapshot() streamUsageSnapshot {
	if t == nil {
		return streamUsageSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return streamUsageSnapshot{
		BytesWritten:               t.bytesWritten,
		EventsWritten:              t.eventsWritten,
		FirstByteAt:                t.firstByteAt,
		OutputTextBytes:            t.outputTextBytes,
		AuthoritativeUsageRecorded: t.authoritativeUsageRecorded,
		ReadErr:                    t.readErr,
	}
}

type streamWriteTrackingWriter struct {
	w       io.Writer
	tracker *streamUsageTracker
}

func (w *streamWriteTrackingWriter) Write(p []byte) (int, error) {
	if w == nil || w.w == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := w.w.Write(p)
	if n > 0 && w.tracker != nil {
		w.tracker.noteWrite(n)
	}
	return n, err
}

type streamReadTrackingCloser struct {
	rc      io.ReadCloser
	tracker *streamUsageTracker
}

func (rc *streamReadTrackingCloser) Read(p []byte) (int, error) {
	if rc == nil || rc.rc == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := rc.rc.Read(p)
	if err != nil && rc.tracker != nil {
		rc.tracker.noteRead(err)
	}
	return n, err
}

func (rc *streamReadTrackingCloser) Close() error {
	if rc == nil || rc.rc == nil {
		return nil
	}
	return rc.rc.Close()
}

func firstStringAny(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func extractOutputTextFromSSEData(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return ""
	}

	var v any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&v); err != nil {
		return ""
	}
	return extractOutputTextFromStreamValue(v)
}

func extractOutputTextFromStreamValue(v any) string {
	switch t := v.(type) {
	case map[string]any:
		return extractOutputTextFromStreamMap(t)
	case []any:
		var b strings.Builder
		for _, item := range t {
			b.WriteString(extractOutputTextFromStreamValue(item))
		}
		return b.String()
	default:
		return ""
	}
}

func extractOutputTextFromStreamMap(m map[string]any) string {
	var b strings.Builder

	if choices, ok := m["choices"].([]any); ok {
		for _, choice := range choices {
			cm, ok := choice.(map[string]any)
			if !ok || cm == nil {
				continue
			}
			if delta, ok := cm["delta"].(map[string]any); ok && delta != nil {
				b.WriteString(firstStringAny(delta, "content", "text", "reasoning_content"))
			}
			b.WriteString(firstStringAny(cm, "text"))
		}
	}

	if candidates, ok := m["candidates"].([]any); ok {
		for _, candidate := range candidates {
			cm, ok := candidate.(map[string]any)
			if !ok || cm == nil {
				continue
			}
			content, _ := cm["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, part := range parts {
				pm, ok := part.(map[string]any)
				if !ok || pm == nil {
					continue
				}
				b.WriteString(firstStringAny(pm, "text"))
			}
		}
	}

	eventType := strings.ToLower(firstStringAny(m, "type"))
	if strings.Contains(eventType, "delta") {
		b.WriteString(firstStringAny(m, "delta", "text", "content"))
		if delta, ok := m["delta"].(map[string]any); ok && delta != nil {
			b.WriteString(firstStringAny(delta, "text", "content", "partial_json"))
		}
	}

	return b.String()
}

func estimateTokensFromBytes(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + 3) / 4
}

func partialUsageErrorClass(copyErr, readErr error) string {
	err := readErr
	if err == nil {
		err = copyErr
	}
	if err == nil {
		return "upstream_stream_error"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "upstream_unexpected_eof"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "idle"):
		return "stream_idle_timeout"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"):
		return "upstream_timeout"
	case strings.Contains(msg, "context canceled"):
		return "upstream_context_canceled"
	case strings.Contains(msg, "connection reset"):
		return "upstream_connection_reset"
	default:
		return "upstream_stream_error"
	}
}

func (h *proxyHandler) recordEstimatedPartialUsage(reqID string, provider Provider, acc *Account, userID string, statusCode int, isSSE bool, headerPrimaryPct, headerSecondaryPct float64, copyErr error, tracker *streamUsageTracker, usageAttribution UsageAttribution) bool {
	if h == nil || provider == nil || acc == nil || tracker == nil || copyErr == nil {
		return false
	}
	if statusCode < 200 || statusCode >= 300 {
		return false
	}

	snap := tracker.snapshot()
	if snap.AuthoritativeUsageRecorded || snap.BytesWritten <= 0 || snap.ReadErr == nil {
		return false
	}

	estimateFromBytes := snap.OutputTextBytes
	if estimateFromBytes <= 0 {
		estimateFromBytes = snap.BytesWritten
	}
	outputTokens := estimateTokensFromBytes(estimateFromBytes)
	if outputTokens <= 0 {
		return false
	}

	errorClass := partialUsageErrorClass(copyErr, snap.ReadErr)
	ru := &RequestUsage{
		Timestamp:      time.Now().UTC(),
		RequestID:      reqID,
		Stream:         isSSE,
		Status:         statusCode,
		Estimated:      true,
		ErrorClass:     errorClass,
		OutputTokens:   outputTokens,
		BillableTokens: outputTokens,
	}
	record := enrichUsageRecord(acc, userID, ru, headerPrimaryPct, headerSecondaryPct)
	if record == nil {
		return false
	}
	usageAttribution.Stream = isSSE
	usageAttribution.Status = statusCode
	usageAttribution.Estimated = true
	usageAttribution.ErrorClass = errorClass
	applyUsageAttribution(record, usageAttribution)
	h.recordUsage(acc, *record)
	tracker.markAuthoritativeUsage()
	return true
}
