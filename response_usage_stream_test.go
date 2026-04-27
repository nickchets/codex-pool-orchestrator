package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestWrapUsageInterceptWriterMarksQuotaEventWithoutAbortingStream(t *testing.T) {
	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	now := time.Now().UTC()
	resetAt := now.Add(20 * time.Minute).Truncate(time.Second)
	h := &proxyHandler{}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{
		ID:   "stream-draining-seat",
		Type: AccountTypeCodex,
		Usage: UsageSnapshot{
			PrimaryResetAt: resetAt,
		},
	}
	managedStreamFailed := false
	var managedStreamFailureOnce sync.Once
	var forwarded bytes.Buffer

	writer := h.wrapUsageInterceptWriter(
		"req-stream-draining",
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

	chunk := []byte("event: error\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"You've hit your usage limit. Try again later.\"}}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"still forwarded\"}\n\n" +
		"event: done\ndata: [DONE]\n\n")
	n, err := writer.Write(chunk)
	if err != nil {
		t.Fatalf("quota event hook should not abort stream: %v", err)
	}
	if n != len(chunk) {
		t.Fatalf("write len=%d want %d", n, len(chunk))
	}
	if !bytes.Equal(forwarded.Bytes(), chunk) {
		t.Fatalf("forwarded stream mismatch:\n%s", forwarded.String())
	}
	if !managedStreamFailed {
		t.Fatal("expected quota event to mark stream as failed for accounting")
	}
	if acc.HealthStatus != "rate_limited" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.Equal(resetAt) {
		t.Fatalf("rate_limit_until=%v want %v", acc.RateLimitUntil, resetAt)
	}
}

type phase8FailingReadCloser struct {
	reader *strings.Reader
	err    error
}

func newPhase8FailingReadCloser(body string, err error) *phase8FailingReadCloser {
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return &phase8FailingReadCloser{reader: strings.NewReader(body), err: err}
}

func (rc *phase8FailingReadCloser) Read(p []byte) (int, error) {
	if rc.reader.Len() > 0 {
		return rc.reader.Read(p)
	}
	return 0, rc.err
}

func (rc *phase8FailingReadCloser) Close() error { return nil }

func phase8DeliverCopiedResponse(t *testing.T, h *proxyHandler, provider Provider, acc *Account, body, contentType string, attr UsageAttribution) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       newPhase8FailingReadCloser(body, io.ErrUnexpectedEOF),
	}
	ok := h.deliverCopiedProxyResponse(
		rr,
		func() {},
		"req-phase8",
		nil,
		provider,
		acc,
		"user-phase8",
		resp,
		time.Now(),
		copiedProxyResponseDeliveryOptions{
			requestPath:           attr.ClientEndpoint,
			captureResponseSample: true,
			usageAttribution:      attr,
		},
	)
	if ok {
		t.Fatal("expected copy error path to report unsuccessful delivery")
	}
	return rr
}

func TestPhase8RecordsEstimatedPartialUsageForMidStreamSSEFailure(t *testing.T) {
	store := testUsageStore(t)
	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{ID: "seat-phase8", Type: AccountTypeCodex, PlanType: "team"}
	h := &proxyHandler{store: store, pool: newPoolState([]*Account{acc}, false), metrics: newMetrics(), recent: newRecentErrors(5)}
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-phase8",
		TokenID:        "tok-phase8",
		TokenName:      "phase8 key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/responses", true)

	body := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello partial stream\"}\n\n"
	rr := phase8DeliverCopiedResponse(t, h, provider, acc, body, "text/event-stream", attr)

	if !strings.Contains(rr.Body.String(), "Hello partial stream") {
		t.Fatalf("client did not receive partial SSE content: %q", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatalf("should not synthesize successful [DONE]: %q", rr.Body.String())
	}

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %+v", rows)
	}
	row := rows[0]
	if !row.Estimated {
		t.Fatalf("expected estimated partial row, got %+v", row)
	}
	if row.ErrorClass != "upstream_unexpected_eof" {
		t.Fatalf("error_class=%q row=%+v", row.ErrorClass, row)
	}
	if !row.Stream || row.Status != http.StatusOK || row.ClientEndpoint != "/v1/responses" {
		t.Fatalf("request attribution = %+v", row)
	}
	if row.TokenID != "tok-phase8" || row.TokenName != "phase8 key" || row.CredentialKind != CredentialKindOpenAICompatiblePoolKey {
		t.Fatalf("token attribution = %+v", row)
	}
	if row.OutputTokens <= 0 || row.BillableTokens != row.OutputTokens {
		t.Fatalf("estimated tokens = %+v", row)
	}
	tok, err := store.getTokenUsage("tok-phase8")
	if err != nil {
		t.Fatalf("load token usage: %v", err)
	}
	if tok.TokenID != "tok-phase8" || tok.EstimatedRequestCount != 1 || tok.StreamRequestCount != 1 || tok.LastErrorClass != "upstream_unexpected_eof" {
		t.Fatalf("token aggregate = %+v", tok)
	}
}

func TestPhase8DoesNotRecordEstimatedDuplicateAfterAuthoritativeSSEUsage(t *testing.T) {
	store := testUsageStore(t)
	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{ID: "seat-phase8-final", Type: AccountTypeCodex, PlanType: "team"}
	h := &proxyHandler{store: store, pool: newPoolState([]*Account{acc}, false), metrics: newMetrics(), recent: newRecentErrors(5)}
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-phase8",
		TokenID:        "tok-phase8-final",
		TokenName:      "phase8 final key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/responses", true)

	body := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello final stream\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11,\"cached_input_tokens\":3,\"output_tokens\":7}}}\n\n"
	_ = phase8DeliverCopiedResponse(t, h, provider, acc, body, "text/event-stream", attr)

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %+v", rows)
	}
	row := rows[0]
	if row.Estimated || row.ErrorClass != "" {
		t.Fatalf("expected only authoritative usage row, got %+v", row)
	}
	if row.InputTokens != 11 || row.CachedInputTokens != 3 || row.OutputTokens != 7 || row.BillableTokens != 15 {
		t.Fatalf("authoritative usage = %+v", row)
	}
}

func TestPhase8PartialUsageWithoutTokenAttributionDoesNotCreateTokenAggregate(t *testing.T) {
	store := testUsageStore(t)
	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{ID: "seat-phase8-real-key", Type: AccountTypeCodex, PlanType: "team"}
	h := &proxyHandler{store: store, pool: newPoolState([]*Account{acc}, false), metrics: newMetrics(), recent: newRecentErrors(5)}
	attr := buildUsageAttribution(AdmissionResult{Kind: AdmissionKindPassthrough}, "/v1/responses", true)

	body := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"real-key partial\"}\n\n"
	_ = phase8DeliverCopiedResponse(t, h, provider, acc, body, "text/event-stream", attr)

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %+v", rows)
	}
	if rows[0].TokenID != "" || rows[0].CredentialKind != "" {
		t.Fatalf("unexpected token attribution for passthrough/real-key row: %+v", rows[0])
	}
	tokens, err := store.listTokenUsageByUser("user-phase8")
	if err != nil {
		t.Fatalf("list token usage: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected no token aggregate without token id, got %+v", tokens)
	}
}

func TestPhase8RecordsEstimatedPartialUsageForNonSSECopyError(t *testing.T) {
	store := testUsageStore(t)
	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	acc := &Account{ID: "seat-phase8-json", Type: AccountTypeCodex, PlanType: "team"}
	h := &proxyHandler{store: store, pool: newPoolState([]*Account{acc}, false), metrics: newMetrics(), recent: newRecentErrors(5)}
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-phase8",
		TokenID:        "tok-phase8-json",
		TokenName:      "phase8 json key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/responses", false)

	_ = phase8DeliverCopiedResponse(t, h, provider, acc, `{"partial":"json body`, "application/json", attr)

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %+v", rows)
	}
	row := rows[0]
	if !row.Estimated || row.Stream {
		t.Fatalf("expected estimated non-SSE partial row, got %+v", row)
	}
	if row.OutputTokens <= 0 || row.TokenID != "tok-phase8-json" {
		t.Fatalf("estimated non-SSE row = %+v", row)
	}
}

func TestPhase8PartialFailureKeepsExistingSSECooldownClassification(t *testing.T) {
	store := testUsageStore(t)
	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	resetAt := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)
	acc := &Account{
		ID:       "seat-phase8-cooldown",
		Type:     AccountTypeCodex,
		PlanType: "team",
		Usage: UsageSnapshot{
			PrimaryResetAt: resetAt,
		},
	}
	h := &proxyHandler{store: store, pool: newPoolState([]*Account{acc}, false), metrics: newMetrics(), recent: newRecentErrors(5)}
	attr := buildUsageAttribution(AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-phase8",
		TokenID:        "tok-phase8-cooldown",
		TokenName:      "phase8 cooldown key",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}, "/v1/responses", true)

	body := "event: error\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"You've hit your usage limit. Try again later.\"}}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"cooldown partial\"}\n\n"
	_ = phase8DeliverCopiedResponse(t, h, provider, acc, body, "text/event-stream", attr)

	if acc.HealthStatus != "rate_limited" {
		t.Fatalf("health_status=%q", acc.HealthStatus)
	}
	if !acc.RateLimitUntil.Equal(resetAt) {
		t.Fatalf("rate_limit_until=%v want %v", acc.RateLimitUntil, resetAt)
	}
	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 || !rows[0].Estimated {
		t.Fatalf("expected one estimated partial usage row, got %+v", rows)
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
