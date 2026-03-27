package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	localTraceHeaderID                 = "X-Pool-Trace-Id"
	localTraceHeaderMode               = "X-Pool-Wrapper-Mode"
	localTraceHeaderStartedAt          = "X-Pool-Wrapper-Started-At"
	localTraceHeaderOutputFormat       = "X-Pool-Wrapper-Output-Format"
	legacyLocalTraceHeaderID           = "X-Claude-Pool-Trace-Id"
	legacyLocalTraceHeaderMode         = "X-Claude-Wrapper-Mode"
	legacyLocalTraceHeaderStartedAt    = "X-Claude-Wrapper-Started-At"
	legacyLocalTraceHeaderOutputFormat = "X-Claude-Wrapper-Output-Format"
)

type requestTraceContextKey struct{}

type requestTrace struct {
	cfg requestTraceConfig

	reqID               string
	startedAt           time.Time
	method              string
	path                string
	contentLength       int64
	accept              string
	wrapperTraceID      string
	wrapperMode         string
	wrapperStartedAt    string
	wrapperOutputFormat string

	mu                  sync.Mutex
	admissionKind       AdmissionKind
	userID              string
	routeMode           string
	accountID           string
	providerType        AccountType
	targetHost          string
	requestedModel      string
	conversationID      string
	firstResponseByteAt time.Time
	lastResponseByteAt  time.Time
	responseBytes       int64
	responseChunks      int
	sseEvents           int
	usageEvents         int
	lastSSEEventType    string
	lastSSEEventBytes   int
	lastAttempt         int
	lastAttemptsTotal   int
	maxChunkGap         time.Duration
	idleTimedOut        bool
	idleTimeoutDuration time.Duration
}

type requestTraceConfig struct {
	requests     bool
	packets      bool
	payloads     bool
	payloadLimit int
	stallGap     time.Duration
}

type traceWriter struct {
	w     io.Writer
	trace *requestTrace
}

func newRequestTraceConfig(cfg config) requestTraceConfig {
	requests := cfg.traceRequests || cfg.tracePackets
	limit := cfg.tracePayloadLimit
	if limit <= 0 {
		limit = 256
	}
	return requestTraceConfig{
		requests:     requests,
		packets:      cfg.tracePackets,
		payloads:     cfg.tracePayloads,
		payloadLimit: limit,
		stallGap:     cfg.traceStallGap,
	}
}

func newRequestTrace(cfg config, reqID string, r *http.Request) *requestTrace {
	traceCfg := newRequestTraceConfig(cfg)
	if !traceCfg.requests && !traceCfg.packets {
		return nil
	}
	if r == nil {
		return nil
	}
	return &requestTrace{
		cfg:                 traceCfg,
		reqID:               reqID,
		startedAt:           time.Now(),
		method:              r.Method,
		path:                r.URL.Path,
		contentLength:       r.ContentLength,
		accept:              strings.TrimSpace(r.Header.Get("Accept")),
		wrapperTraceID:      readLocalTraceHeader(r.Header, localTraceHeaderID, legacyLocalTraceHeaderID),
		wrapperMode:         readLocalTraceHeader(r.Header, localTraceHeaderMode, legacyLocalTraceHeaderMode),
		wrapperStartedAt:    readLocalTraceHeader(r.Header, localTraceHeaderStartedAt, legacyLocalTraceHeaderStartedAt),
		wrapperOutputFormat: readLocalTraceHeader(r.Header, localTraceHeaderOutputFormat, legacyLocalTraceHeaderOutputFormat),
	}
}

func withRequestTrace(r *http.Request, trace *requestTrace) *http.Request {
	if r == nil || trace == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), requestTraceContextKey{}, trace))
}

func requestTraceFromContext(ctx context.Context) *requestTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(requestTraceContextKey{}).(*requestTrace)
	return trace
}

func stripLocalTraceHeaders(h http.Header) {
	if h == nil {
		return
	}
	h.Del(localTraceHeaderID)
	h.Del(localTraceHeaderMode)
	h.Del(localTraceHeaderStartedAt)
	h.Del(localTraceHeaderOutputFormat)
	h.Del(legacyLocalTraceHeaderID)
	h.Del(legacyLocalTraceHeaderMode)
	h.Del(legacyLocalTraceHeaderStartedAt)
	h.Del(legacyLocalTraceHeaderOutputFormat)
}

func newTraceWriter(w io.Writer, trace *requestTrace) io.Writer {
	if trace == nil {
		return w
	}
	return &traceWriter{w: w, trace: trace}
}

func (tw *traceWriter) Write(p []byte) (int, error) {
	n, err := tw.w.Write(p)
	if n > 0 && tw.trace != nil {
		tw.trace.noteResponseChunk(n)
	}
	return n, err
}

func (t *requestTrace) noteAdmission(admission AdmissionResult) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.admissionKind = admission.Kind
	t.userID = admission.UserID
	t.mu.Unlock()
	if !t.cfg.requests {
		return
	}
	log.Printf(
		"[%s] trace start method=%s path=%s content_length=%d accept=%q admission=%s user=%s wrapper_trace=%q wrapper_mode=%q wrapper_started_at=%q wrapper_output=%q",
		t.reqID,
		t.method,
		t.path,
		t.contentLength,
		t.accept,
		admission.Kind,
		admission.UserID,
		t.wrapperTraceID,
		t.wrapperMode,
		t.wrapperStartedAt,
		t.wrapperOutputFormat,
	)
}

func (t *requestTrace) noteRoute(routePlan RoutePlan, acc *Account, targetBase *url.URL, mode string, attempt, attempts int) {
	if t == nil || acc == nil {
		return
	}
	targetHost := ""
	if targetBase != nil {
		targetHost = targetBase.Host
	}
	t.mu.Lock()
	t.routeMode = mode
	t.accountID = acc.ID
	t.providerType = routePlan.Provider.Type()
	t.targetHost = targetHost
	t.requestedModel = routePlan.Shape.RequestedModel
	t.conversationID = routePlan.Shape.ConversationID
	t.lastAttempt = attempt
	t.lastAttemptsTotal = attempts
	t.mu.Unlock()
	if !t.cfg.requests {
		return
	}
	log.Printf(
		"[%s] trace route mode=%s attempt=%d/%d provider=%s account=%s target=%s requested_model=%q conversation_id=%q",
		t.reqID,
		mode,
		attempt,
		attempts,
		routePlan.Provider.Type(),
		acc.ID,
		targetHost,
		routePlan.Shape.RequestedModel,
		routePlan.Shape.ConversationID,
	)
}

func (t *requestTrace) noteTransportError(stage string, acc *Account, err error) {
	if t == nil || !t.cfg.requests || err == nil {
		return
	}
	accountID := ""
	if acc != nil {
		accountID = acc.ID
	}
	log.Printf("[%s] trace upstream_error stage=%s account=%s err=%v", t.reqID, stage, accountID, err)
}

func (t *requestTrace) noteResponse(statusCode int, resp *http.Response, isSSE bool) {
	if t == nil || !t.cfg.requests {
		return
	}
	contentType := ""
	contentEncoding := ""
	contentLength := int64(-1)
	if resp != nil {
		contentType = strings.TrimSpace(resp.Header.Get("Content-Type"))
		contentEncoding = strings.TrimSpace(resp.Header.Get("Content-Encoding"))
		contentLength = resp.ContentLength
	}
	log.Printf(
		"[%s] trace response status=%d sse=%v header_wait_ms=%d content_type=%q content_encoding=%q content_length=%d",
		t.reqID,
		statusCode,
		isSSE,
		time.Since(t.startedAt).Milliseconds(),
		contentType,
		contentEncoding,
		contentLength,
	)
}

func (t *requestTrace) noteResponseChunk(n int) {
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	now := time.Now()
	prevByteAt := t.lastResponseByteAt
	if t.firstResponseByteAt.IsZero() {
		t.firstResponseByteAt = now
	}
	t.lastResponseByteAt = now
	t.responseBytes += int64(n)
	t.responseChunks++
	chunkIndex := t.responseChunks
	totalBytes := t.responseBytes
	var chunkGap time.Duration
	if !prevByteAt.IsZero() {
		chunkGap = now.Sub(prevByteAt)
		if chunkGap > t.maxChunkGap {
			t.maxChunkGap = chunkGap
		}
	}
	t.mu.Unlock()
	if t.cfg.packets {
		log.Printf(
			"[%s] trace chunk idx=%d bytes=%d total_bytes=%d since_start_ms=%d",
			t.reqID,
			chunkIndex,
			n,
			totalBytes,
			time.Since(t.startedAt).Milliseconds(),
		)
		if t.cfg.stallGap > 0 && chunkGap >= t.cfg.stallGap {
			log.Printf(
				"[%s] trace chunk_gap gap_ms=%d prev_chunk_idx=%d current_chunk_idx=%d total_bytes=%d",
				t.reqID,
				chunkGap.Milliseconds(),
				chunkIndex-1,
				chunkIndex,
				totalBytes,
			)
		}
	}
}

func (t *requestTrace) noteIdleTimeout(timeout time.Duration) {
	if t == nil || timeout <= 0 {
		return
	}
	t.mu.Lock()
	t.idleTimedOut = true
	t.idleTimeoutDuration = timeout
	lastByteAt := t.lastResponseByteAt
	responseBytes := t.responseBytes
	responseChunks := t.responseChunks
	t.mu.Unlock()
	if !t.cfg.requests {
		return
	}
	lastByteMS := int64(-1)
	if !lastByteAt.IsZero() {
		lastByteMS = lastByteAt.Sub(t.startedAt).Milliseconds()
	}
	log.Printf(
		"[%s] trace idle_timeout timeout_ms=%d since_start_ms=%d last_byte_ms=%d bytes=%d chunks=%d",
		t.reqID,
		timeout.Milliseconds(),
		time.Since(t.startedAt).Milliseconds(),
		lastByteMS,
		responseBytes,
		responseChunks,
	)
}

func (t *requestTrace) noteSSEEvent(data []byte, isUsage bool) {
	if t == nil {
		return
	}
	eventType := traceEventType(data)
	t.mu.Lock()
	t.sseEvents++
	if isUsage {
		t.usageEvents++
	}
	t.lastSSEEventType = eventType
	t.lastSSEEventBytes = len(data)
	eventIndex := t.sseEvents
	usageCount := t.usageEvents
	t.mu.Unlock()
	if !t.cfg.packets {
		return
	}
	msg := "[" + t.reqID + "] trace sse_event idx="
	log.Printf(
		msg+"%d type=%q bytes=%d usage=%v usage_events=%d since_start_ms=%d",
		eventIndex,
		eventType,
		len(data),
		isUsage,
		usageCount,
		time.Since(t.startedAt).Milliseconds(),
	)
	if t.cfg.payloads && len(data) > 0 {
		sample := "[redacted non-usage event]"
		if isUsage {
			sample = tracePayloadSample(data, t.cfg.payloadLimit)
		}
		log.Printf("[%s] trace sse_payload type=%q sample=%s", t.reqID, eventType, sample)
	}
}

func (t *requestTrace) noteSSEUsageEvent(data []byte) {
	if t == nil {
		return
	}
	eventType := traceEventType(data)
	t.mu.Lock()
	t.usageEvents++
	usageCount := t.usageEvents
	t.mu.Unlock()
	if !t.cfg.packets {
		return
	}
	log.Printf(
		"[%s] trace sse_usage type=%q bytes=%d usage_events=%d since_start_ms=%d",
		t.reqID,
		eventType,
		len(data),
		usageCount,
		time.Since(t.startedAt).Milliseconds(),
	)
	if t.cfg.payloads && len(data) > 0 {
		log.Printf("[%s] trace sse_usage_payload type=%q sample=%s", t.reqID, eventType, tracePayloadSample(data, t.cfg.payloadLimit))
	}
}

func (t *requestTrace) noteFinish(statusCode int, isSSE bool, managedStreamFailed bool, copyErr error) {
	if t == nil || !t.cfg.requests {
		return
	}
	t.mu.Lock()
	firstByteAt := t.firstResponseByteAt
	lastByteAt := t.lastResponseByteAt
	responseBytes := t.responseBytes
	responseChunks := t.responseChunks
	sseEvents := t.sseEvents
	usageEvents := t.usageEvents
	accountID := t.accountID
	providerType := t.providerType
	targetHost := t.targetHost
	routeMode := t.routeMode
	lastSSEEventType := t.lastSSEEventType
	lastSSEEventBytes := t.lastSSEEventBytes
	attempt := t.lastAttempt
	attempts := t.lastAttemptsTotal
	maxChunkGap := t.maxChunkGap
	idleTimedOut := t.idleTimedOut
	idleTimeoutDuration := t.idleTimeoutDuration
	t.mu.Unlock()

	firstByteMS := int64(-1)
	lastByteMS := int64(-1)
	if !firstByteAt.IsZero() {
		firstByteMS = firstByteAt.Sub(t.startedAt).Milliseconds()
	}
	if !lastByteAt.IsZero() {
		lastByteMS = lastByteAt.Sub(t.startedAt).Milliseconds()
	}
	log.Printf(
		"[%s] trace finish mode=%s attempt=%d/%d provider=%s account=%s target=%s status=%d sse=%v bytes=%d chunks=%d sse_events=%d usage_events=%d first_byte_ms=%d last_byte_ms=%d total_ms=%d max_chunk_gap_ms=%d idle_timeout=%v idle_timeout_ms=%d last_sse_type=%q last_sse_bytes=%d managed_stream_failed=%v copy_err=%q",
		t.reqID,
		routeMode,
		attempt,
		attempts,
		providerType,
		accountID,
		targetHost,
		statusCode,
		isSSE,
		responseBytes,
		responseChunks,
		sseEvents,
		usageEvents,
		firstByteMS,
		lastByteMS,
		time.Since(t.startedAt).Milliseconds(),
		maxChunkGap.Milliseconds(),
		idleTimedOut,
		idleTimeoutDuration.Milliseconds(),
		lastSSEEventType,
		lastSSEEventBytes,
		managedStreamFailed,
		traceErrString(copyErr),
	)
}

func (t *requestTrace) noteTokenRefresh(provider AccountType, acc *Account, profile, result string, latency time.Duration, err error) {
	accountID, authMode := traceAccountMeta(acc)
	t.noteEvent(
		"token_refresh",
		"provider=%s account=%s auth_mode=%q profile=%q result=%s latency_ms=%d error=%q",
		provider,
		accountID,
		authMode,
		strings.TrimSpace(profile),
		strings.TrimSpace(result),
		latency.Milliseconds(),
		traceErrString(err),
	)
}

func (t *requestTrace) noteAuthFallback(provider AccountType, acc *Account, attemptedProfile string, fallbackable bool, nextProfile string, err error) {
	accountID, _ := traceAccountMeta(acc)
	t.noteEvent(
		"auth_fallback",
		"provider=%s account=%s attempted_profile=%q fallbackable=%v next_profile=%q error=%q",
		provider,
		accountID,
		strings.TrimSpace(attemptedProfile),
		fallbackable,
		strings.TrimSpace(nextProfile),
		traceErrString(err),
	)
}

func (t *requestTrace) noteProbe(provider AccountType, acc *Account, result string, latency time.Duration, err error) {
	accountID, authMode := traceAccountMeta(acc)
	t.noteEvent(
		"probe",
		"provider=%s account=%s auth_mode=%q result=%s latency_ms=%d error=%q",
		provider,
		accountID,
		authMode,
		strings.TrimSpace(result),
		latency.Milliseconds(),
		traceErrString(err),
	)
}

func (t *requestTrace) noteFacadeTransform(provider AccountType, acc *Account, originalPath, targetPath, requestedModel, rewrittenModel, projectID, result string, err error) {
	accountID, authMode := traceAccountMeta(acc)
	t.noteEvent(
		"facade_transform",
		"provider=%s account=%s auth_mode=%q result=%s original_path=%q target_path=%q requested_model=%q rewritten_model=%q project_id=%q error=%q",
		provider,
		accountID,
		authMode,
		strings.TrimSpace(result),
		strings.TrimSpace(originalPath),
		strings.TrimSpace(targetPath),
		strings.TrimSpace(requestedModel),
		strings.TrimSpace(rewrittenModel),
		strings.TrimSpace(projectID),
		traceErrString(err),
	)
}

func (t *requestTrace) noteRetryDisposition(provider AccountType, acc *Account, attempt, attempts, statusCode int, retryable bool, reason string, refreshFailed bool) {
	accountID, authMode := traceAccountMeta(acc)
	t.noteEvent(
		"retry_disposition",
		"provider=%s account=%s auth_mode=%q attempt=%d attempt_count=%d status=%d retryable=%v reason=%q refresh_failed=%v",
		provider,
		accountID,
		authMode,
		attempt,
		attempts,
		statusCode,
		retryable,
		strings.TrimSpace(reason),
		refreshFailed,
	)
}

func (t *requestTrace) noteCacheDecision(provider AccountType, acc *Account, state string, age, latency time.Duration, err error) {
	accountID, authMode := traceAccountMeta(acc)
	t.noteEvent(
		"models_cache",
		"provider=%s account=%s auth_mode=%q state=%s age_ms=%d latency_ms=%d error=%q",
		provider,
		accountID,
		authMode,
		strings.TrimSpace(state),
		age.Milliseconds(),
		latency.Milliseconds(),
		traceErrString(err),
	)
}

func (t *requestTrace) noteOAuthExchange(provider AccountType, lane, result, accountID string, refreshedExisting bool, latency time.Duration, err error) {
	t.noteEvent(
		"oauth_exchange",
		"provider=%s lane=%q result=%s account=%q refreshed_existing=%v latency_ms=%d error=%q",
		provider,
		strings.TrimSpace(lane),
		strings.TrimSpace(result),
		strings.TrimSpace(accountID),
		refreshedExisting,
		latency.Milliseconds(),
		traceErrString(err),
	)
}

func (t *requestTrace) noteProviderTruth(provider AccountType, stage, result, projectID, tierID, validationReason string, latency time.Duration, err error) {
	t.noteEvent(
		"provider_truth",
		"provider=%s stage=%q result=%s project_id=%q tier_id=%q validation_reason=%q latency_ms=%d error=%q",
		provider,
		strings.TrimSpace(stage),
		strings.TrimSpace(result),
		strings.TrimSpace(projectID),
		strings.TrimSpace(tierID),
		strings.TrimSpace(validationReason),
		latency.Milliseconds(),
		traceErrString(err),
	)
}

func (t *requestTrace) noteEvent(event, format string, args ...any) {
	if t == nil || !t.cfg.requests {
		return
	}
	if strings.TrimSpace(format) == "" {
		log.Printf("[%s] trace %s", t.reqID, event)
		return
	}
	prefix := fmt.Sprintf("[%s] trace %s ", t.reqID, event)
	log.Printf(prefix+format, args...)
}

func traceEventType(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var payload struct {
		Type  string `json:"type"`
		Event string `json:"event"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	if payload.Type != "" {
		return payload.Type
	}
	return payload.Event
}

func traceErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func tracePayloadSample(data []byte, limit int) string {
	if len(data) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(data) {
		limit = len(data)
	}
	return safeText(data[:limit])
}

func readLocalTraceHeader(h http.Header, names ...string) string {
	if h == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(h.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func traceAccountMeta(acc *Account) (string, string) {
	if acc == nil {
		return "", ""
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	return acc.ID, acc.AuthMode
}
