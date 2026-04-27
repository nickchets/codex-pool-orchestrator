package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	streamContinuationModeOff           = "off"
	streamContinuationModePlainTextOnly = "plain_text_only"
	streamContinuationModeExperimental  = "experimental"

	streamContinuationMaxBufferedTextBytes = 64 * 1024
	streamContinuationDedupWindowBytes     = 512
	streamContinuationBufferedEventLimit   = 256 * 1024
)

const streamContinuationInstruction = "Continue exactly where the previous assistant response stopped. Do not repeat any previous text."

func normalizeStreamContinuationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case streamContinuationModePlainTextOnly:
		return streamContinuationModePlainTextOnly
	case streamContinuationModeExperimental:
		return streamContinuationModeExperimental
	default:
		return streamContinuationModeOff
	}
}

func streamContinuationEnabled(mode string) bool {
	switch normalizeStreamContinuationMode(mode) {
	case streamContinuationModePlainTextOnly, streamContinuationModeExperimental:
		return true
	default:
		return false
	}
}

type streamContinuationTracker struct {
	mu              sync.Mutex
	maxBytes        int
	buf             strings.Builder
	bytes           int
	disabled        bool
	sawDone         bool
	sseBoundarySafe bool
}

func newStreamContinuationTracker(maxBytes int) *streamContinuationTracker {
	if maxBytes <= 0 {
		maxBytes = streamContinuationMaxBufferedTextBytes
	}
	return &streamContinuationTracker{maxBytes: maxBytes, sseBoundarySafe: true}
}

func (t *streamContinuationTracker) noteSSEBoundary(safe bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.sseBoundarySafe = safe
	t.mu.Unlock()
}

func (t *streamContinuationTracker) noteSSEEvent(data []byte) {
	if t == nil {
		return
	}
	trimmed := bytes.TrimSpace(data)
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(trimmed) == 0 {
		return
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		t.sawDone = true
		return
	}
	if streamContinuationEventUnsafe(trimmed) {
		t.disabled = true
		return
	}
	text := extractOutputTextFromSSEData(trimmed)
	if text == "" {
		return
	}
	textBytes := len([]byte(text))
	if t.bytes+textBytes > t.maxBytes {
		t.disabled = true
		return
	}
	t.buf.WriteString(text)
	t.bytes += textBytes
}

func (t *streamContinuationTracker) partialText() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

func (t *streamContinuationTracker) canAttempt(snapshot streamUsageSnapshot, managedStreamFailed bool) bool {
	if t == nil || managedStreamFailed {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.disabled || t.sawDone || !t.sseBoundarySafe || t.bytes <= 0 || strings.TrimSpace(t.buf.String()) == "" {
		return false
	}
	if snapshot.AuthoritativeUsageRecorded || snapshot.BytesWritten <= 0 || snapshot.ReadErr == nil {
		return false
	}
	return true
}

func (h *proxyHandler) newStreamContinuationTracker(routePlan RoutePlan, requestPath string, requestBody []byte, provider Provider, acc *Account) *streamContinuationTracker {
	if h == nil || !streamContinuationEnabled(h.cfg.streamContinuation) {
		return nil
	}
	if provider == nil || provider.Type() != AccountTypeCodex || acc == nil || acc.Type != AccountTypeCodex {
		return nil
	}
	if routePlan.AccountType != "" && routePlan.AccountType != AccountTypeCodex {
		return nil
	}
	if routePlan.ResponseAdapter != "" {
		return nil
	}
	if routePlan.Admission.Kind != AdmissionKindPoolUser {
		return nil
	}
	if !routePlan.IsOpenAICompatibleClient && !isCodexPlainTextContinuationPath(requestPath) {
		return nil
	}
	if !isCodexPlainTextContinuationPath(firstNonEmpty(routePlan.Shape.Path, requestPath)) && !isCodexPlainTextContinuationPath(routePlan.UpstreamPath) {
		return nil
	}
	if !plainTextContinuationRequestSafe(requestBody) {
		return nil
	}
	return newStreamContinuationTracker(streamContinuationMaxBufferedTextBytes)
}

func isCodexPlainTextContinuationPath(path string) bool {
	path = strings.TrimSpace(path)
	switch {
	case path == "/v1/chat/completions" || path == "/chat/completions":
		return true
	case path == "/v1/responses" || path == "/responses":
		return true
	case strings.HasPrefix(path, "/v1/responses/") || strings.HasPrefix(path, "/responses/"):
		return true
	case strings.HasPrefix(path, "/backend-api/codex/responses"):
		return true
	default:
		return false
	}
}

func plainTextContinuationRequestSafe(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return false
	}
	if stream, ok := m["stream"].(bool); ok && !stream {
		return false
	}
	return !plainTextContinuationValueUnsafe(v)
}

func plainTextContinuationValueUnsafe(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if contentType, ok := t["type"].(string); ok {
			if streamContinuationTypeUnsafe(contentType) {
				return true
			}
		}
		for k, child := range t {
			lk := strings.ToLower(strings.TrimSpace(k))
			switch lk {
			case "tools", "tool_choice", "parallel_tool_calls", "functions", "function_call", "response_format", "json_schema", "schema", "modalities", "audio", "image", "image_url", "input_image", "input_audio", "file", "files", "video", "videos", "input_video", "multimodal":
				return true
			}
			if plainTextContinuationValueUnsafe(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if plainTextContinuationValueUnsafe(child) {
				return true
			}
		}
	}
	return false
}

func streamContinuationEventUnsafe(data []byte) bool {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	return streamContinuationEventValueUnsafe(v)
}

func streamContinuationTypeUnsafe(name string) bool {
	l := strings.ToLower(strings.TrimSpace(name))
	if l == "" {
		return false
	}
	for _, needle := range []string{"tool", "function", "json", "image", "audio", "file", "video", "multimodal"} {
		if strings.Contains(l, needle) {
			return true
		}
	}
	return false
}

func streamContinuationMultimodalFieldUnsafe(name string) bool {
	l := strings.ToLower(strings.TrimSpace(name))
	if l == "" {
		return false
	}
	for _, needle := range []string{"image", "audio", "file", "video", "multimodal"} {
		if strings.Contains(l, needle) {
			return true
		}
	}
	return false
}

func streamContinuationEventValueUnsafe(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if typ, ok := t["type"].(string); ok {
			if streamContinuationTypeUnsafe(typ) {
				return true
			}
		}
		for k, child := range t {
			lk := strings.ToLower(strings.TrimSpace(k))
			if streamContinuationMultimodalFieldUnsafe(lk) {
				return true
			}
			switch lk {
			case "tool_calls", "function_call", "function", "arguments", "partial_json", "json_schema", "schema":
				return true
			}
			if streamContinuationEventValueUnsafe(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if streamContinuationEventValueUnsafe(child) {
				return true
			}
		}
	}
	return false
}

func buildPlainTextContinuationRequestBody(original []byte, requestPath string, partialText string) ([]byte, error) {
	partialText = strings.TrimRight(partialText, "\x00")
	if strings.TrimSpace(partialText) == "" {
		return nil, errors.New("empty continuation text")
	}
	var obj map[string]any
	if err := json.Unmarshal(original, &obj); err != nil {
		return nil, err
	}
	obj["stream"] = true
	if isChatCompletionsContinuationPath(requestPath) {
		messages, _ := obj["messages"].([]any)
		messages = append(messages,
			map[string]any{"role": "assistant", "content": partialText},
			map[string]any{"role": "user", "content": streamContinuationInstruction},
		)
		obj["messages"] = messages
		return json.Marshal(obj)
	}

	if messages, ok := obj["messages"].([]any); ok {
		messages = append(messages,
			map[string]any{"role": "assistant", "content": partialText},
			map[string]any{"role": "user", "content": streamContinuationInstruction},
		)
		obj["messages"] = messages
		return json.Marshal(obj)
	}

	input, _ := obj["input"]
	var items []any
	switch t := input.(type) {
	case []any:
		items = append(items, t...)
	case string:
		if t != "" {
			items = append(items, map[string]any{"role": "user", "content": t})
		}
	case nil:
		// Leave empty; the continuation instruction below gives the model direction.
	default:
		items = append(items, t)
	}
	items = append(items,
		map[string]any{"role": "assistant", "content": partialText},
		map[string]any{"role": "user", "content": streamContinuationInstruction},
	)
	obj["input"] = items
	return json.Marshal(obj)
}

func isChatCompletionsContinuationPath(path string) bool {
	path = strings.TrimSpace(path)
	return path == "/v1/chat/completions" || path == "/chat/completions" || strings.HasSuffix(path, "/chat/completions")
}

func markStreamContinuationSeatFailed(acc *Account, reason string) {
	if acc == nil {
		return
	}
	now := time.Now().UTC()
	until := earliestFutureTime(now, acc.Usage.PrimaryResetAt, acc.Usage.SecondaryResetAt)
	if until.IsZero() {
		until = now.Add(defaultRateLimitBackoff)
	}
	acc.mu.Lock()
	if acc.RateLimitUntil.Before(until) {
		acc.RateLimitUntil = until
	}
	if acc.HealthStatus == "" || acc.HealthStatus == "healthy" {
		acc.HealthStatus = "error"
	}
	acc.HealthError = sanitizeStatusMessage(firstNonEmpty(reason, "mid-stream continuation failure"))
	acc.HealthCheckedAt = now
	acc.Penalty += 1.0
	acc.mu.Unlock()
}

func isContextCanceledOrDeadline(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func contextDoneErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type plainTextOverlapDeduper struct {
	active bool
	prior  string
}

func newPlainTextOverlapDeduper(prior string) *plainTextOverlapDeduper {
	prior = lastValidUTF8Suffix(prior, streamContinuationDedupWindowBytes)
	return &plainTextOverlapDeduper{active: prior != "", prior: prior}
}

func (d *plainTextOverlapDeduper) trim(text string) string {
	if d == nil || !d.active || text == "" {
		return text
	}
	if d.prior == "" {
		d.active = false
		return text
	}
	d.active = false
	overlapBytes := longestRuneSuffixPrefixOverlap(d.prior, text)
	if overlapBytes <= 0 {
		return text
	}
	return text[overlapBytes:]
}

func longestRuneSuffixPrefixOverlap(prior, text string) int {
	if prior == "" || text == "" || !utf8.ValidString(prior) || !utf8.ValidString(text) {
		return 0
	}
	priorRunes := []rune(prior)
	textRunes := []rune(text)
	max := len(priorRunes)
	if len(textRunes) < max {
		max = len(textRunes)
	}
	for n := max; n > 0; n-- {
		prefix := string(textRunes[:n])
		if string(priorRunes[len(priorRunes)-n:]) == prefix {
			return len(prefix)
		}
	}
	return 0
}

func lastValidUTF8Suffix(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

type sseTextDedupReadCloser struct {
	rc      io.ReadCloser
	deduper *plainTextOverlapDeduper
	buf     []byte
	out     []byte
	eof     bool
	readErr error
}

func newSSETextDedupReadCloser(rc io.ReadCloser, priorText string) io.ReadCloser {
	if rc == nil || strings.TrimSpace(priorText) == "" {
		return rc
	}
	return &sseTextDedupReadCloser{rc: rc, deduper: newPlainTextOverlapDeduper(priorText)}
}

func (rc *sseTextDedupReadCloser) Read(p []byte) (int, error) {
	if rc == nil || rc.rc == nil {
		return 0, io.ErrClosedPipe
	}
	for len(rc.out) == 0 {
		if idx, delimLen := findSSEDelimiter(rc.buf); idx >= 0 {
			event := append([]byte(nil), rc.buf[:idx]...)
			delim := append([]byte(nil), rc.buf[idx:idx+delimLen]...)
			rc.buf = rc.buf[idx+delimLen:]
			rc.out = append(rc.out, rc.processEvent(event)...)
			rc.out = append(rc.out, delim...)
			continue
		}
		if rc.eof {
			if len(rc.buf) > 0 {
				rc.out = append(rc.out, rc.processEvent(rc.buf)...)
				rc.buf = nil
				continue
			}
			if rc.readErr != nil && !errors.Is(rc.readErr, io.EOF) {
				err := rc.readErr
				rc.readErr = nil
				return 0, err
			}
			return 0, io.EOF
		}
		if len(rc.buf) > streamContinuationBufferedEventLimit {
			rc.out = append(rc.out, rc.buf...)
			rc.buf = nil
			continue
		}
		var tmp [4096]byte
		n, err := rc.rc.Read(tmp[:])
		if n > 0 {
			rc.buf = append(rc.buf, tmp[:n]...)
		}
		if err != nil {
			rc.eof = true
			rc.readErr = err
		}
		if n == 0 && err == nil {
			return 0, nil
		}
	}
	n := copy(p, rc.out)
	rc.out = rc.out[n:]
	return n, nil
}

func (rc *sseTextDedupReadCloser) Close() error {
	if rc == nil || rc.rc == nil {
		return nil
	}
	return rc.rc.Close()
}

func (rc *sseTextDedupReadCloser) processEvent(event []byte) []byte {
	if rc == nil || rc.deduper == nil || len(event) == 0 {
		return event
	}
	data, nonData, ok := splitSSEEventData(event)
	if !ok || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return event
	}
	rewritten, changed := rewriteSSETextDeltaData(data, rc.deduper.trim)
	if !changed {
		return event
	}
	out := make([]byte, 0, len(event))
	out = append(out, nonData...)
	if len(out) > 0 && !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	out = append(out, []byte("data: ")...)
	out = append(out, rewritten...)
	return out
}

func findSSEDelimiter(buf []byte) (int, int) {
	if idx := bytes.Index(buf, []byte("\n\n")); idx >= 0 {
		return idx, 2
	}
	if idx := bytes.Index(buf, []byte("\r\n\r\n")); idx >= 0 {
		return idx, 4
	}
	return -1, 0
}

func splitSSEEventData(event []byte) ([]byte, []byte, bool) {
	lines := bytes.Split(event, []byte("\n"))
	var dataLines [][]byte
	var nonData []byte
	for _, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimPrefix(line, []byte("data:"))
			if bytes.HasPrefix(value, []byte(" ")) {
				value = value[1:]
			}
			dataLines = append(dataLines, append([]byte(nil), value...))
			continue
		}
		if len(nonData) > 0 {
			nonData = append(nonData, '\n')
		}
		nonData = append(nonData, line...)
	}
	if len(dataLines) == 0 {
		return nil, nil, false
	}
	return bytes.Join(dataLines, []byte("\n")), nonData, true
}

func rewriteSSETextDeltaData(data []byte, transform func(string) string) ([]byte, bool) {
	if transform == nil {
		return data, false
	}
	trimmed := bytes.TrimSpace(data)
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return data, false
	}
	changed := rewriteTextDeltaValue(v, transform)
	if !changed {
		return data, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return data, false
	}
	return out, true
}

func rewriteTextDeltaValue(v any, transform func(string) string) bool {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return false
	}
	changed := false
	if choices, ok := m["choices"].([]any); ok {
		for _, choice := range choices {
			cm, _ := choice.(map[string]any)
			if cm == nil {
				continue
			}
			if delta, ok := cm["delta"].(map[string]any); ok && delta != nil {
				for _, key := range []string{"content", "text"} {
					if s, ok := delta[key].(string); ok && s != "" {
						next := transform(s)
						if next != s {
							delta[key] = next
							changed = true
						}
					}
				}
			}
		}
	}
	eventType := strings.ToLower(firstStringAny(m, "type"))
	if strings.Contains(eventType, "delta") {
		for _, key := range []string{"delta", "text", "content"} {
			if s, ok := m[key].(string); ok && s != "" {
				next := transform(s)
				if next != s {
					m[key] = next
					changed = true
				}
			}
		}
	}
	if candidates, ok := m["candidates"].([]any); ok {
		for _, candidate := range candidates {
			cm, _ := candidate.(map[string]any)
			content, _ := cm["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, part := range parts {
				pm, _ := part.(map[string]any)
				if s, ok := pm["text"].(string); ok && s != "" {
					next := transform(s)
					if next != s {
						pm["text"] = next
						changed = true
					}
				}
			}
		}
	}
	return changed
}

func (h *proxyHandler) tryPlainTextStreamContinuation(
	baseWriter io.Writer,
	cancel context.CancelFunc,
	reqID string,
	trace *requestTrace,
	provider Provider,
	failedAcc *Account,
	userID string,
	firstStatusCode int,
	headerPrimaryPct float64,
	headerSecondaryPct float64,
	firstRespSample []byte,
	managedStreamFailed bool,
	usageAttribution UsageAttribution,
	streamTracker *streamUsageTracker,
	continuationTracker *streamContinuationTracker,
	continuation *streamContinuationContext,
	initialConversationID string,
	start time.Time,
) (bool, bool) {
	if h == nil || baseWriter == nil || provider == nil || failedAcc == nil || streamTracker == nil || continuationTracker == nil || continuation == nil || continuation.originalRequest == nil {
		return false, false
	}
	_ = headerPrimaryPct
	_ = headerSecondaryPct
	if firstStatusCode < http.StatusOK || firstStatusCode >= http.StatusMultipleChoices {
		return false, false
	}
	snap := streamTracker.snapshot()
	if !continuationTracker.canAttempt(snap, managedStreamFailed) {
		return false, false
	}
	partialText := continuationTracker.partialText()
	continuationBody, err := buildPlainTextContinuationRequestBody(continuation.bodyBytes, continuation.routePlan.Shape.Path, partialText)
	if err != nil {
		return false, false
	}
	if err := contextDoneErr(continuation.ctx); err != nil {
		return false, false
	}

	markStreamContinuationSeatFailed(failedAcc, "mid-stream hard failure before final SSE usage")
	if h.metrics != nil {
		h.metrics.incEvent("stream_continuation_attempt")
	}

	maxAttempts := h.cfg.streamContinuationMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	exclude := map[string]bool{failedAcc.ID: true}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := contextDoneErr(continuation.ctx); err != nil {
			return false, false
		}
		nextAcc, pickErr := h.candidateSupportingPath(
			continuation.routePlan.Shape.ConversationID,
			exclude,
			continuation.routePlan.AccountType,
			continuation.routePlan.RequiredPlan,
			continuation.routePlan.Provider,
			continuation.routePlan.UpstreamPath,
			continuation.routePlan.Shape.RequestedModel,
			continuation.routePlan.DebugGeminiSeatID,
		)
		if pickErr != nil || nextAcc == nil {
			return false, false
		}
		exclude[nextAcc.ID] = true
		atomic.AddInt64(&h.inflight, 1)
		releaseInflight := func() {
			atomic.AddInt64(&nextAcc.Inflight, -1)
			atomic.AddInt64(&h.inflight, -1)
		}
		if err := contextDoneErr(continuation.ctx); err != nil {
			releaseInflight()
			return false, false
		}

		targetBase := providerUpstreamURLForAccount(provider, continuation.routePlan.UpstreamPath, nextAcc)
		if trace != nil {
			trace.noteRoute(continuation.routePlan, nextAcc, targetBase, "stream_continuation", attempt+1, maxAttempts+1)
		}

		resp, sampleBuf, refreshFailed, tryErr := h.tryOnce(continuation.ctx, continuation.originalRequest, continuationBody, continuation.routePlan, nextAcc, reqID)
		if tryErr != nil {
			releaseInflight()
			if isContextCanceledOrDeadline(tryErr) || contextDoneErr(continuation.ctx) != nil {
				return false, false
			}
			markStreamContinuationSeatFailed(nextAcc, tryErr.Error())
			if h.recent != nil {
				h.recent.add(tryErr.Error())
			}
			continue
		}
		if resp == nil {
			releaseInflight()
			continue
		}

		h.applyPreCopyUpstreamStatusHandling(reqID, nextAcc, resp, refreshFailed, continuation.routePlan.Shape.RequestedModel, continuation.routePlan.UpstreamPath, false)
		isSSE := provider.DetectsSSE(continuation.routePlan.Shape.Path, resp.Header.Get("Content-Type"))
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !isSSE {
			_ = resp.Body.Close()
			releaseInflight()
			continue
		}

		provider.ParseUsageHeaders(nextAcc, resp.Header)
		persistUsageSnapshot(h.store, nextAcc)
		nextAcc.mu.Lock()
		nextHeaderPrimaryPct := nextAcc.Usage.PrimaryUsedPercent
		nextHeaderSecondaryPct := nextAcc.Usage.SecondaryUsedPercent
		nextAcc.mu.Unlock()

		if err := writeSSEContinuationComment(baseWriter, 2); err != nil {
			_ = resp.Body.Close()
			releaseInflight()
			return true, false
		}

		contAttr := usageAttribution
		contAttr.Stream = true
		contAttr.Status = resp.StatusCode
		contAttr.ContinuationUsed = true
		contAttr.SegmentCount = 2
		continuationManagedStreamFailed := false
		var continuationManagedStreamFailureOnce sync.Once
		writer := h.wrapUsageInterceptWriterWithAttributionAndTracker(
			reqID,
			baseWriter,
			provider,
			nextAcc,
			userID,
			trace,
			nextHeaderPrimaryPct,
			nextHeaderSecondaryPct,
			&continuationManagedStreamFailed,
			&continuationManagedStreamFailureOnce,
			contAttr,
			streamTracker,
			nil,
		)

		resp.Body = newSSETextDedupReadCloser(resp.Body, partialText)
		var idleReader *idleTimeoutReader
		if h.cfg.streamIdleTimeout > 0 {
			var onIdleTimeout func()
			if trace != nil {
				onIdleTimeout = func() { trace.noteIdleTimeout(h.cfg.streamIdleTimeout) }
			}
			idleReader = newIdleTimeoutReader(resp.Body, h.cfg.streamIdleTimeout, cancel, onIdleTimeout)
			resp.Body = idleReader
		}
		resp.Body = &streamReadTrackingCloser{rc: resp.Body, tracker: streamTracker}

		_, copyErr := io.Copy(writer, resp.Body)
		_ = resp.Body.Close()
		var secondSample []byte
		if sampleBuf != nil {
			secondSample = sampleBuf.Bytes()
		}
		combinedSample := append([]byte(nil), firstRespSample...)
		combinedSample = append(combinedSample, secondSample...)
		ok := h.finalizeCopiedProxyResponseWithAttribution(
			reqID,
			trace,
			provider,
			nextAcc,
			userID,
			resp.StatusCode,
			true,
			continuationManagedStreamFailed,
			initialConversationID,
			nextHeaderPrimaryPct,
			nextHeaderSecondaryPct,
			combinedSample,
			copyErr,
			idleReader != nil,
			start,
			"stream continuation done",
			contAttr,
			streamTracker,
		)
		releaseInflight()
		return true, ok
	}
	return false, false
}

func writeSSEContinuationComment(w io.Writer, segment int) error {
	if w == nil || segment <= 1 {
		return nil
	}
	_, err := io.WriteString(w, ": codex-pool continuation segment="+strconv.Itoa(segment)+"\n\n")
	return err
}
