package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newBufferedCodexProxyHandlerForTest(t *testing.T, upstreamURL string, accounts []*Account) *proxyHandler {
	t.Helper()

	baseURL, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)

	return &proxyHandler{
		cfg: config{
			requestTimeout:       5 * time.Second,
			maxInMemoryBodyBytes: 1024,
		},
		transport: http.DefaultTransport,
		pool:      newPoolState(accounts, false),
		registry:  NewProviderRegistry(codex, claude, gemini),
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
	}
}

func newBufferedGitLabClaudeAccountForTest(t *testing.T, dir, id, sourceToken, gatewayToken, upstreamBaseURL string) *Account {
	t.Helper()

	file := filepath.Join(dir, id+".json")
	payload := fmt.Sprintf(`{
		"plan_type":"gitlab_duo",
		"auth_mode":"gitlab_duo",
		"gitlab_token":"%s",
		"gitlab_gateway_token":"%s",
		"gitlab_gateway_headers":{"X-Gitlab-Instance-Id":"inst-1"},
		"gitlab_gateway_base_url":"%s"
	}`, sourceToken, gatewayToken, upstreamBaseURL)
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write gitlab account file %s: %v", file, err)
	}

	return &Account{
		ID:              id,
		Type:            AccountTypeClaude,
		File:            file,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    sourceToken,
		AccessToken:     gatewayToken,
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: upstreamBaseURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
	}
}

func newBufferedGitLabCodexAccountForTest(t *testing.T, dir, id, sourceToken, gatewayToken, upstreamBaseURL string) *Account {
	t.Helper()

	file := filepath.Join(dir, id+".json")
	payload := fmt.Sprintf(`{
		"plan_type":"gitlab_duo",
		"auth_mode":"gitlab_duo",
		"gitlab_token":"%s",
		"gitlab_gateway_token":"%s",
		"gitlab_gateway_headers":{"X-Gitlab-Instance-Id":"inst-1"},
		"gitlab_gateway_base_url":"%s"
	}`, sourceToken, gatewayToken, upstreamBaseURL)
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write gitlab account file %s: %v", file, err)
	}

	return &Account{
		ID:              id,
		Type:            AccountTypeCodex,
		File:            file,
		PlanType:        "gitlab_duo",
		AuthMode:        accountAuthModeGitLab,
		RefreshToken:    sourceToken,
		AccessToken:     gatewayToken,
		SourceBaseURL:   defaultGitLabInstanceURL,
		UpstreamBaseURL: upstreamBaseURL,
		ExtraHeaders:    map[string]string{"X-Gitlab-Instance-Id": "inst-1"},
		HealthStatus:    "healthy",
		LastHealthyAt:   time.Now().UTC(),
	}
}

func newPhase9CodexContinuationAccount(id, token string) *Account {
	return &Account{
		ID:          id,
		Type:        AccountTypeCodex,
		AccessToken: token,
		PlanType:    "pro",
		Usage: UsageSnapshot{
			RetrievedAt: time.Now().UTC(),
		},
	}
}

func phase9SSETextDelta(text string) string {
	return "event: response.output_text.delta\n" +
		fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", text)
}

func phase9SSEUsageDone(inputTokens, outputTokens int64) string {
	return "event: response.completed\n" +
		fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":%d,\"output_tokens\":%d}}}\n\n", inputTokens, outputTokens) +
		"data: [DONE]\n\n"
}

func phase9StreamingRequest(t *testing.T, rawKey, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return req
}

func phase9SSEFailureResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       newPhase8FailingReadCloser(body, io.ErrUnexpectedEOF),
		Request:    req,
	}
}

func phase9SSEOKResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestPhase9StreamContinuationDefaultOffFallsBackToPhase8PartialUsage(t *testing.T) {
	store := testUsageStore(t)
	seatA := newPhase9CodexContinuationAccount("seat-a", "token-a")
	seatB := newPhase9CodexContinuationAccount("seat-b", "token-b")
	var calls int64
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		call := atomic.AddInt64(&calls, 1)
		if call != 1 {
			t.Fatalf("default-off continuation should not make upstream call #%d", call)
		}
		return phase9SSEFailureResponse(req, phase9SSETextDelta("Hello partial")), nil
	}), seatA, seatB)
	h.store = store

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, phase9StreamingRequest(t, rawKey, `{"model":"gpt-4.1-mini","input":"say hello","stream":true}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Hello partial") {
		t.Fatalf("client did not receive partial stream: %q", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "continuation segment") {
		t.Fatalf("default-off stream unexpectedly emitted continuation marker: %q", rr.Body.String())
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("upstream calls=%d want 1", got)
	}

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 || !rows[0].Estimated || rows[0].ErrorClass != "upstream_unexpected_eof" {
		t.Fatalf("expected existing Phase8 estimated partial usage row, got %+v", rows)
	}
	if rows[0].ContinuationUsed || rows[0].SegmentCount != 0 {
		t.Fatalf("default-off usage row should not be marked as continuation: %+v", rows[0])
	}
}

func TestPhase9PlainTextContinuationBridgesSecondSeatAndDeduplicatesPrefix(t *testing.T) {
	store := testUsageStore(t)
	seatA := newPhase9CodexContinuationAccount("seat-a", "token-a")
	seatB := newPhase9CodexContinuationAccount("seat-b", "token-b")
	var calls int64
	var authSeen []string
	var requestBodies []string
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		call := atomic.AddInt64(&calls, 1)
		authSeen = append(authSeen, req.Header.Get("Authorization"))
		body, _ := io.ReadAll(req.Body)
		requestBodies = append(requestBodies, string(body))
		switch call {
		case 1:
			return phase9SSEFailureResponse(req, phase9SSETextDelta("Hello ")), nil
		case 2:
			return phase9SSEOKResponse(req, phase9SSETextDelta("Hello ")+phase9SSETextDelta("world")+phase9SSEUsageDone(11, 2)), nil
		default:
			t.Fatalf("unexpected upstream call #%d", call)
			return nil, nil
		}
	}), seatA, seatB)
	h.store = store
	h.cfg.streamContinuation = streamContinuationModePlainTextOnly
	h.cfg.streamContinuationMaxAttempts = 1

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, phase9StreamingRequest(t, rawKey, `{"model":"gpt-4.1-mini","input":"say hello","stream":true}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Hello ") || !strings.Contains(body, "world") {
		t.Fatalf("client did not receive bridged text: %q", body)
	}
	if strings.Count(body, `"delta":"Hello "`) != 1 {
		t.Fatalf("expected repeated prefix to be de-duplicated in downstream stream, body=%s", body)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("upstream calls=%d want 2", got)
	}
	if len(authSeen) != 2 || authSeen[0] != "Bearer token-a" || authSeen[1] != "Bearer token-b" {
		t.Fatalf("auth/seat sequence = %#v", authSeen)
	}
	if len(requestBodies) != 2 || !strings.Contains(requestBodies[1], "Hello ") || !strings.Contains(strings.ToLower(requestBodies[1]), "continue") {
		t.Fatalf("continuation request body was not built from partial assistant text: %#v", requestBodies)
	}
	if !seatA.RateLimitUntil.After(time.Now().Add(-time.Second)) {
		t.Fatalf("first seat was not marked draining after mid-stream failure: %+v", snapshotProxyTestAccount(seatA))
	}

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %+v", rows)
	}
	if rows[0].Estimated || !rows[0].ContinuationUsed || rows[0].SegmentCount != 2 || rows[0].AccountID != "seat-b" {
		t.Fatalf("expected authoritative continuation usage on second seat, got %+v", rows[0])
	}
}

func TestPhase9ContinuationSafetyRejectsToolStructuredAndPassthrough(t *testing.T) {
	baseURL, _ := url.Parse("https://upstream.example")
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	seat := newPhase9CodexContinuationAccount("seat-a", "token-a")
	h := &proxyHandler{cfg: config{streamContinuation: streamContinuationModePlainTextOnly}}
	basePlan := RoutePlan{
		Admission: AdmissionResult{
			Kind:           AdmissionKindPoolUser,
			UserID:         "user-1",
			TokenID:        "tok-1",
			CredentialKind: CredentialKindOpenAICompatiblePoolKey,
		},
		Shape:                    RequestShape{Path: "/v1/chat/completions", RequestedModel: "gpt-4.1-mini"},
		Provider:                 provider,
		UpstreamPath:             "/v1/chat/completions",
		AccountType:              AccountTypeCodex,
		IsOpenAICompatibleClient: true,
	}

	cases := []struct {
		name string
		body string
		plan RoutePlan
	}{
		{
			name: "tool_calls",
			body: `{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"stream":true}`,
			plan: basePlan,
		},
		{
			name: "structured_output",
			body: `{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}},"stream":true}`,
			plan: basePlan,
		},
		{
			name: "passthrough",
			body: `{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			plan: func() RoutePlan {
				p := basePlan
				p.Admission = AdmissionResult{Kind: AdmissionKindPassthrough, ProviderType: AccountTypeCodex}
				p.IsOpenAICompatibleClient = false
				return p
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := h.newStreamContinuationTracker(tc.plan, "/v1/chat/completions", []byte(tc.body), provider, seat)
			if tracker != nil {
				t.Fatalf("expected continuation to be disabled for %s", tc.name)
			}
		})
	}
}

func TestPhase9MaxContinuationAttemptsPreventsInfiniteLoopAndRecordsPartialUsage(t *testing.T) {
	store := testUsageStore(t)
	seatA := newPhase9CodexContinuationAccount("seat-a", "token-a")
	seatB := newPhase9CodexContinuationAccount("seat-b", "token-b")
	seatC := newPhase9CodexContinuationAccount("seat-c", "token-c")
	var calls int64
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		call := atomic.AddInt64(&calls, 1)
		switch call {
		case 1:
			return phase9SSEFailureResponse(req, phase9SSETextDelta("first ")), nil
		case 2:
			return phase9SSEFailureResponse(req, phase9SSETextDelta("second ")), nil
		default:
			t.Fatalf("max continuation attempts should prevent upstream call #%d", call)
			return nil, nil
		}
	}), seatA, seatB, seatC)
	h.store = store
	h.cfg.streamContinuation = streamContinuationModePlainTextOnly
	h.cfg.streamContinuationMaxAttempts = 1

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, phase9StreamingRequest(t, rawKey, `{"model":"gpt-4.1-mini","input":"say hello","stream":true}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("upstream calls=%d want 2", got)
	}
	if !strings.Contains(rr.Body.String(), "first ") || !strings.Contains(rr.Body.String(), "second ") {
		t.Fatalf("client should receive both failed segments before final partial accounting: %q", rr.Body.String())
	}

	rows := readStoredRequestUsageRows(t, store)
	if len(rows) != 1 || !rows[0].Estimated || rows[0].ErrorClass != "upstream_unexpected_eof" {
		t.Fatalf("expected one estimated partial usage row after failed continuation, got %+v", rows)
	}
	if !rows[0].ContinuationUsed || rows[0].SegmentCount != 2 {
		t.Fatalf("partial continuation usage metadata missing: %+v", rows[0])
	}
}

func waitForBufferedProxySuccessAccountState(t *testing.T, acc *Account, reason string) proxyTestAccountSnapshot {
	t.Helper()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		snapshot := snapshotProxyTestAccount(acc)
		if !snapshot.LastUsed.IsZero() {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}

	snapshot := snapshotProxyTestAccount(acc)
	t.Fatalf("expected %s; LastUsed=%v", reason, snapshot.LastUsed)
	return proxyTestAccountSnapshot{}
}

func TestProxyBufferedAnthropicMessagesGeminiToolLoopReinjectsThoughtSignature(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")
	t.Cleanup(clearGeminiThoughtSignatureCache)
	clearGeminiThoughtSignatureCache()
	checkedAt := time.Now().UTC().Add(-5 * time.Minute)

	var upstreamBodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer seat-access" {
			t.Fatalf("authorization=%q", got)
		}
		if got := r.URL.Path; got != "/v1internal:streamGenerateContent" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != antigravityCodeAssistUA {
			t.Fatalf("user-agent=%q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBodies = append(upstreamBodies, string(body))

		w.Header().Set("Content-Type", "text/event-stream")
		switch len(upstreamBodies) {
		case 1:
			if !strings.Contains(upstreamBodies[0], `"project":"project-1"`) {
				t.Fatalf("first upstream body missing project: %s", upstreamBodies[0])
			}
			if !strings.Contains(upstreamBodies[0], `"functionDeclarations"`) {
				t.Fatalf("first upstream body missing tools: %s", upstreamBodies[0])
			}
			_, _ = io.WriteString(w,
				"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"bash\",\"args\":{\"command\":\"pwd\"},\"id\":\"toolu_buffered_1\"},\"thoughtSignature\":\"sig-buffered-1\"}]}}]}}\n\n"+
					"data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":6}}}\n\n",
			)
		case 2:
			if !strings.Contains(upstreamBodies[1], `"thoughtSignature":"sig-buffered-1"`) {
				t.Fatalf("second upstream body missing thoughtSignature reinjection: %s", upstreamBodies[1])
			}
			if !strings.Contains(upstreamBodies[1], `"result":"/workspace/project"`) {
				t.Fatalf("second upstream body missing tool result: %s", upstreamBodies[1])
			}
			_, _ = io.WriteString(w,
				"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"TOOL_BUFFERED_OK\"}]}}]}}\n\n"+
					"data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":4}}}\n\n",
			)
		default:
			t.Fatalf("unexpected upstream call #%d", len(upstreamBodies))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	accountFile := filepath.Join(tmp, "gemini-buffered.json")
	if err := os.WriteFile(accountFile, []byte(`{
		"access_token":"seat-access",
		"refresh_token":"seat-refresh",
		"plan_type":"gemini",
		"auth_mode":"oauth",
		"oauth_profile_id":"antigravity_public",
		"operator_source":"antigravity_import",
		"antigravity_source":"browser_oauth"
	}`), 0o600); err != nil {
		t.Fatalf("write gemini account file: %v", err)
	}

	acc := &Account{
		ID:                       "gemini-seat-buffered",
		Type:                     AccountTypeGemini,
		File:                     accountFile,
		PlanType:                 "gemini",
		AuthMode:                 accountAuthModeOAuth,
		AccessToken:              "seat-access",
		RefreshToken:             "seat-refresh",
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		OperatorSource:           geminiOperatorSourceAntigravityImport,
		AntigravitySource:        "browser_oauth",
		AntigravityProjectID:     "project-1",
		GeminiProviderCheckedAt:  checkedAt,
		GeminiProviderTruthReady: true,
		GeminiProviderTruthState: geminiProviderTruthStateReady,
		HealthStatus:             "healthy",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{acc})
	h.cfg.disableRefresh = true
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	firstReqBody := []byte(`{
		"model":"gemini-3.1-pro-high",
		"max_tokens":128,
		"messages":[{"role":"user","content":"Use the bash tool exactly once with command pwd. After the tool result, reply with exactly TOOL_BUFFERED_OK."}],
		"tools":[{"name":"bash","description":"run bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]
	}`)
	firstReq, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(firstReqBody))
	if err != nil {
		t.Fatalf("new first request: %v", err)
	}
	firstReq.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gemini-tool-user"))
	firstReq.Header.Set("Content-Type", "application/json")

	firstResp, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatalf("first proxy request: %v", err)
	}
	defer firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(firstResp.Body)
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(body))
	}

	var first anthropicMessageResponse
	if err := json.NewDecoder(firstResp.Body).Decode(&first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if first.StopReason != "tool_use" {
		t.Fatalf("first stop_reason=%q", first.StopReason)
	}
	if len(first.Content) != 1 || first.Content[0].Type != "tool_use" {
		t.Fatalf("first content=%+v", first.Content)
	}

	assistantContent, err := json.Marshal(first.Content)
	if err != nil {
		t.Fatalf("marshal assistant content: %v", err)
	}
	secondReqBody := []byte(fmt.Sprintf(`{
		"model":"gemini-3.1-pro-high",
		"max_tokens":128,
		"messages":[
			{"role":"user","content":"Use the bash tool exactly once with command pwd. After the tool result, reply with exactly TOOL_BUFFERED_OK."},
			{"role":"assistant","content":%s},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"/workspace/project"}]}
		],
		"tools":[{"name":"bash","description":"run bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]
	}`, string(assistantContent), first.Content[0].ID))
	secondReq, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(secondReqBody))
	if err != nil {
		t.Fatalf("new second request: %v", err)
	}
	secondReq.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gemini-tool-user"))
	secondReq.Header.Set("Content-Type", "application/json")

	secondResp, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second proxy request: %v", err)
	}
	defer secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(secondResp.Body)
		t.Fatalf("second status=%d body=%s", secondResp.StatusCode, string(body))
	}

	var second anthropicMessageResponse
	if err := json.NewDecoder(secondResp.Body).Decode(&second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if second.StopReason != "end_turn" {
		t.Fatalf("second stop_reason=%q", second.StopReason)
	}
	if len(second.Content) != 1 || second.Content[0].Type != "text" || second.Content[0].Text != "TOOL_BUFFERED_OK" {
		t.Fatalf("second content=%+v", second.Content)
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstreamBodies=%d", len(upstreamBodies))
	}

	accState := waitForBufferedProxySuccessAccountState(t, acc, "Gemini seat to record buffered tool-loop usage")
	if accState.HealthStatus != "healthy" {
		t.Fatalf("health_status=%q", accState.HealthStatus)
	}
}

func TestProxyBufferedAnthropicMessagesGemini429PinnedConversationRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")
	checkedAt := time.Now().UTC().Add(-5 * time.Minute)

	callCounts := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		callCounts[auth]++

		if got := r.URL.Path; got != "/v1internal:streamGenerateContent" {
			t.Fatalf("unexpected path %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != antigravityCodeAssistUA {
			t.Fatalf("user-agent=%q", got)
		}

		switch auth {
		case "Bearer seat-one":
			if callCounts[auth] == 1 {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w,
					"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"FIRST_OK\"}]}}]}}\n\n"+
						"data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3}}}\n\n",
				)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `[{"error":{"code":429,"message":"quota exhausted","status":"RESOURCE_EXHAUSTED"}}]`)
		case "Bearer seat-two":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"SECOND_OK\"}]}}]}}\n\n"+
					"data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":6,\"candidatesTokenCount\":4}}}\n\n",
			)
		default:
			t.Fatalf("unexpected auth header %q", auth)
		}
	}))
	defer upstream.Close()
	originalQuotaBases := append([]string(nil), antigravityGeminiQuotaBaseURLs...)
	antigravityGeminiQuotaBaseURLs = []string{upstream.URL}
	t.Cleanup(func() {
		antigravityGeminiQuotaBaseURLs = originalQuotaBases
	})

	tmp := t.TempDir()
	accountOneFile := filepath.Join(tmp, "gemini-seat-one.json")
	if err := os.WriteFile(accountOneFile, []byte(`{
		"access_token":"seat-one",
		"refresh_token":"seat-one-refresh",
		"plan_type":"gemini",
		"auth_mode":"oauth",
		"oauth_profile_id":"antigravity_public",
		"operator_source":"antigravity_import",
		"antigravity_source":"browser_oauth"
	}`), 0o600); err != nil {
		t.Fatalf("write first gemini account file: %v", err)
	}
	accountTwoFile := filepath.Join(tmp, "gemini-seat-two.json")
	if err := os.WriteFile(accountTwoFile, []byte(`{
		"access_token":"seat-two",
		"refresh_token":"seat-two-refresh",
		"plan_type":"gemini",
		"auth_mode":"oauth",
		"oauth_profile_id":"antigravity_public",
		"operator_source":"antigravity_import",
		"antigravity_source":"browser_oauth"
	}`), 0o600); err != nil {
		t.Fatalf("write second gemini account file: %v", err)
	}

	seatOne := &Account{
		ID:                       "gemini-seat-one",
		Type:                     AccountTypeGemini,
		File:                     accountOneFile,
		PlanType:                 "gemini",
		AuthMode:                 accountAuthModeOAuth,
		AccessToken:              "seat-one",
		RefreshToken:             "seat-one-refresh",
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		OperatorSource:           geminiOperatorSourceAntigravityImport,
		AntigravitySource:        "browser_oauth",
		AntigravityProjectID:     "project-1",
		GeminiProviderCheckedAt:  checkedAt,
		GeminiProviderTruthReady: true,
		GeminiProviderTruthState: geminiProviderTruthStateReady,
		HealthStatus:             "healthy",
	}
	seatTwo := &Account{
		ID:                       "gemini-seat-two",
		Type:                     AccountTypeGemini,
		File:                     accountTwoFile,
		PlanType:                 "gemini",
		AuthMode:                 accountAuthModeOAuth,
		AccessToken:              "seat-two",
		RefreshToken:             "seat-two-refresh",
		OAuthProfileID:           geminiOAuthAntigravityProfileID,
		OperatorSource:           geminiOperatorSourceAntigravityImport,
		AntigravitySource:        "browser_oauth",
		AntigravityProjectID:     "project-2",
		GeminiProviderCheckedAt:  checkedAt,
		GeminiProviderTruthReady: true,
		GeminiProviderTruthState: geminiProviderTruthStateReady,
		HealthStatus:             "healthy",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{seatOne, seatTwo})
	h.cfg.disableRefresh = true
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{
		"model":"gemini-3.1-pro-high",
		"session_id":"conv-gemini-429",
		"max_tokens":64,
		"messages":[{"role":"user","content":"Reply with a single marker."}]
	}`)

	firstReq, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new first request: %v", err)
	}
	firstReq.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gemini-429-user"))
	firstReq.Header.Set("Content-Type", "application/json")

	firstResp, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatalf("first proxy request: %v", err)
	}
	defer firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(firstResp.Body)
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(body))
	}

	var first anthropicMessageResponse
	if err := json.NewDecoder(firstResp.Body).Decode(&first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if len(first.Content) != 1 || first.Content[0].Type != "text" || first.Content[0].Text != "FIRST_OK" {
		t.Fatalf("first content=%+v", first.Content)
	}
	if got := h.pool.convPin["conv-gemini-429"]; got != seatOne.ID {
		t.Fatalf("initial pin=%q want %q", got, seatOne.ID)
	}

	secondReq, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new second request: %v", err)
	}
	secondReq.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gemini-429-user"))
	secondReq.Header.Set("Content-Type", "application/json")

	secondResp, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second proxy request: %v", err)
	}
	defer secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(secondResp.Body)
		t.Fatalf("second status=%d body=%s", secondResp.StatusCode, string(body))
	}

	var second anthropicMessageResponse
	if err := json.NewDecoder(secondResp.Body).Decode(&second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if len(second.Content) != 1 || second.Content[0].Type != "text" || second.Content[0].Text != "SECOND_OK" {
		t.Fatalf("second content=%+v", second.Content)
	}

	seatOneState := snapshotProxyTestAccount(seatOne)
	if !seatOneState.RateLimitUntil.IsZero() {
		t.Fatalf("expected seat-wide cooldown to stay clear, got %v", seatOneState.RateLimitUntil)
	}
	if seatOneState.GeminiModelRateLimitResetTimes["gemini-3.1-pro-high"].IsZero() {
		t.Fatal("expected first Gemini seat to track a live model cooldown after 429")
	}
	seatTwoState := waitForBufferedProxySuccessAccountState(t, seatTwo, "second Gemini seat to serve retry after 429")
	if seatTwoState.HealthStatus != "healthy" {
		t.Fatalf("seatTwo health_status=%q", seatTwoState.HealthStatus)
	}
	if got := h.pool.convPin["conv-gemini-429"]; got != seatTwo.ID {
		t.Fatalf("final pin=%q want %q", got, seatTwo.ID)
	}
	if callCounts["Bearer seat-one"] != 2 {
		t.Fatalf("seat-one calls=%d", callCounts["Bearer seat-one"])
	}
	if callCounts["Bearer seat-two"] != 1 {
		t.Fatalf("seat-two calls=%d", callCounts["Bearer seat-two"])
	}
}

func TestProxyBufferedManagedAPI429RetriesNextSeatAfterQuotaFallback(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	type authCall struct {
		count int
	}
	calls := map[string]*authCall{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		call := calls[auth]
		if call == nil {
			call = &authCall{}
			calls[auth] = call
		}
		call.count++

		w.Header().Set("Content-Type", "application/json")
		switch auth {
		case "Bearer sk-proj-dead":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-dead","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`))
		case "Bearer sk-proj-live":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-live","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", auth)
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadFile := filepath.Join(tmp, "openai_api_dead.json")
	if err := os.WriteFile(deadFile, []byte(`{"OPENAI_API_KEY":"sk-proj-dead","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write dead key file: %v", err)
	}
	liveFile := filepath.Join(tmp, "openai_api_live.json")
	if err := os.WriteFile(liveFile, []byte(`{"OPENAI_API_KEY":"sk-proj-live","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write live key file: %v", err)
	}

	deadAcc := &Account{
		ID:          "openai_api_dead",
		Type:        AccountTypeCodex,
		File:        deadFile,
		AccessToken: "sk-proj-dead",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}
	liveAcc := &Account{
		ID:          "openai_api_live",
		Type:        AccountTypeCodex,
		File:        liveFile,
		AccessToken: "sk-proj-live",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-4.1-mini","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-managed-api-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected first managed api key to be marked dead")
	}
	if deadState.HealthStatus != "dead" {
		t.Fatalf("dead health status = %q", deadState.HealthStatus)
	}
	liveState := snapshotProxyTestAccount(liveAcc)
	if liveState.Dead {
		t.Fatal("expected second managed api key to stay live")
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "second managed api key to be used")
	if calls["Bearer sk-proj-dead"] == nil || calls["Bearer sk-proj-dead"].count != 2 {
		t.Fatalf("dead account calls = %+v", calls["Bearer sk-proj-dead"])
	}
	if calls["Bearer sk-proj-live"] == nil || calls["Bearer sk-proj-live"].count != 2 {
		t.Fatalf("live account calls = %+v", calls["Bearer sk-proj-live"])
	}
}

func TestProxyPoolUserChunkedCodex429RetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var firstCalls, secondCalls int
	var firstBody, secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}

		switch r.Header.Get("Authorization") {
		case "Bearer first-seat-token":
			firstCalls++
			firstBody = string(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"quota exhausted","code":"rate_limit_exceeded"}}`)
		case "Bearer second-seat-token":
			secondCalls++
			secondBody = string(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"resp_live","status":"completed"}`)
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	firstSeat := &Account{
		ID:           "codex_a_first",
		Type:         AccountTypeCodex,
		AccessToken:  "first-seat-token",
		AccountID:    "acct-first",
		PlanType:     "pro",
		HealthStatus: "healthy",
	}
	secondSeat := &Account{
		ID:           "codex_b_second",
		Type:         AccountTypeCodex,
		AccessToken:  "second-seat-token",
		AccountID:    "acct-second",
		PlanType:     "pro",
		HealthStatus: "healthy",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{firstSeat, secondSeat})
	h.cfg.disableRefresh = true

	body := `{"model":"gpt-4.1-mini","input":"chunked fallback"}`
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(body))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "chunked-pool-user"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-pool-user-chunked-429")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "resp_live") {
		t.Fatalf("response body=%s", rr.Body.String())
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("firstCalls=%d secondCalls=%d", firstCalls, secondCalls)
	}
	if firstBody != body || secondBody != body {
		t.Fatalf("upstream bodies first=%q second=%q want=%q", firstBody, secondBody, body)
	}
	if got := snapshotProxyTestAccount(firstSeat).Inflight; got != 0 {
		t.Fatalf("first seat inflight=%d", got)
	}
	if got := snapshotProxyTestAccount(secondSeat).Inflight; got != 0 {
		t.Fatalf("second seat inflight=%d", got)
	}
	if got := atomic.LoadInt64(&h.inflight); got != 0 {
		t.Fatalf("handler inflight=%d", got)
	}
}

func TestProxyPoolUserOversizedChunkedBodyRejectsBeforeUpstream(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"should-not-happen"}`)
	}))
	defer upstream.Close()

	seat := &Account{
		ID:           "codex_oversized",
		Type:         AccountTypeCodex,
		AccessToken:  "oversized-seat-token",
		AccountID:    "acct-oversized",
		PlanType:     "pro",
		HealthStatus: "healthy",
	}
	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{seat})
	h.cfg.disableRefresh = true
	h.cfg.maxInMemoryBodyBytes = 16

	body := `{"model":"gpt-4.1-mini","input":"` + strings.Repeat("x", 128) + `"}`
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(body))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "oversized-pool-user"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-pool-user-oversized-chunked")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
	if got := snapshotProxyTestAccount(seat).Inflight; got != 0 {
		t.Fatalf("seat inflight=%d", got)
	}
	if got := atomic.LoadInt64(&h.inflight); got != 0 {
		t.Fatalf("handler inflight=%d", got)
	}
}

func TestProxyBufferedManagedAPISSEQuotaMarksSeatDrainingWithoutInterruptingActiveStream(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var mu sync.Mutex
	calls := map[string]int{}
	quotaEventSent := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseFirst) })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		calls[auth]++
		mu.Unlock()

		switch auth {
		case "Bearer sk-draining-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"before quota\"}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"rate_limit_exceeded for this seat\",\"code\":\"rate_limit_exceeded\"}}}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-quotaEventSent:
			default:
				close(quotaEventSent)
			}
			<-releaseFirst
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" after quota\"}\n\n")
			_, _ = io.WriteString(w, "event: done\ndata: [DONE]\n\n")
		case "Bearer sk-live-stream":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"resp_live_stream","status":"completed"}`)
		default:
			t.Fatalf("unexpected auth header %q", auth)
		}
	}))
	defer upstream.Close()

	now := time.Now().UTC()
	drainingAcc := &Account{
		ID:              "openai_api_draining_stream",
		Type:            AccountTypeCodex,
		AccessToken:     "sk-draining-stream",
		PlanType:        "api",
		AuthMode:        accountAuthModeAPIKey,
		HealthStatus:    "healthy",
		HealthCheckedAt: now,
		LastHealthyAt:   now,
	}
	liveAcc := &Account{
		ID:              "openai_api_live_stream",
		Type:            AccountTypeCodex,
		AccessToken:     "sk-live-stream",
		PlanType:        "api",
		AuthMode:        accountAuthModeAPIKey,
		HealthStatus:    "healthy",
		HealthCheckedAt: now,
		LastHealthyAt:   now,
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{drainingAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	firstReq, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-4.1-mini","input":"stream","stream":true}`)))
	if err != nil {
		t.Fatalf("new first request: %v", err)
	}
	firstReq.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-managed-api-stream-user"))
	firstReq.Header.Set("Content-Type", "application/json")

	firstResp, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatalf("first proxy request: %v", err)
	}
	defer firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(firstResp.Body)
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(body))
	}

	firstBodyCh := make(chan []byte, 1)
	firstErrCh := make(chan error, 1)
	go func() {
		body, err := io.ReadAll(firstResp.Body)
		if err != nil {
			firstErrCh <- err
			return
		}
		firstBodyCh <- body
	}()

	select {
	case <-quotaEventSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream quota event")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := snapshotProxyTestAccount(drainingAcc)
		if state.RateLimitUntil.After(time.Now()) || state.Dead {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	drainingState := snapshotProxyTestAccount(drainingAcc)
	if !drainingState.RateLimitUntil.After(time.Now()) {
		t.Fatalf("expected active stream quota event to mark seat draining, state=%+v", drainingState)
	}
	if drainingState.Inflight != 1 {
		t.Fatalf("expected first stream to remain inflight while draining, inflight=%d", drainingState.Inflight)
	}

	secondReq, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-4.1-mini","input":"second"}`)))
	if err != nil {
		t.Fatalf("new second request: %v", err)
	}
	secondReq.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-managed-api-stream-user"))
	secondReq.Header.Set("Content-Type", "application/json")

	secondResp, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second proxy request: %v", err)
	}
	defer secondResp.Body.Close()
	secondBody, err := io.ReadAll(secondResp.Body)
	if err != nil {
		t.Fatalf("read second body: %v", err)
	}
	if secondResp.StatusCode != http.StatusOK || !bytes.Contains(secondBody, []byte("resp_live_stream")) {
		t.Fatalf("second status=%d body=%s", secondResp.StatusCode, string(secondBody))
	}

	releaseOnce.Do(func() { close(releaseFirst) })
	var firstBody []byte
	select {
	case firstBody = <-firstBodyCh:
	case err := <-firstErrCh:
		t.Fatalf("read first body: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first stream to finish")
	}
	if !bytes.Contains(firstBody, []byte("before quota")) || !bytes.Contains(firstBody, []byte(" after quota")) || !bytes.Contains(firstBody, []byte("[DONE]")) {
		t.Fatalf("first stream did not receive all chunks: %s", string(firstBody))
	}
	if got := snapshotProxyTestAccount(drainingAcc).Inflight; got != 0 {
		t.Fatalf("expected draining stream to finish and release inflight, got %d", got)
	}
	mu.Lock()
	drainingCalls := calls["Bearer sk-draining-stream"]
	liveCalls := calls["Bearer sk-live-stream"]
	mu.Unlock()
	if drainingCalls != 1 {
		t.Fatalf("draining account calls=%d", drainingCalls)
	}
	if liveCalls != 1 {
		t.Fatalf("live account calls=%d", liveCalls)
	}
}

func TestProxyBufferedManagedAPI402RetriesNextSeatAfterPaymentRequired(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	type authCall struct {
		count int
	}
	calls := map[string]*authCall{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		call := calls[auth]
		if call == nil {
			call = &authCall{}
			calls[auth] = call
		}
		call.count++

		w.Header().Set("Content-Type", "application/json")
		switch auth {
		case "Bearer sk-proj-dead":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-dead","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"billing hard limit","code":"billing_hard_limit_reached"}}`))
		case "Bearer sk-proj-live":
			if call.count == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"probe-live","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", auth)
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadFile := filepath.Join(tmp, "openai_api_dead.json")
	if err := os.WriteFile(deadFile, []byte(`{"OPENAI_API_KEY":"sk-proj-dead","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write dead key file: %v", err)
	}
	liveFile := filepath.Join(tmp, "openai_api_live.json")
	if err := os.WriteFile(liveFile, []byte(`{"OPENAI_API_KEY":"sk-proj-live","auth_mode":"api_key","plan_type":"api"}`), 0o600); err != nil {
		t.Fatalf("write live key file: %v", err)
	}

	deadAcc := &Account{
		ID:          "openai_api_dead",
		Type:        AccountTypeCodex,
		File:        deadFile,
		AccessToken: "sk-proj-dead",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}
	liveAcc := &Account{
		ID:          "openai_api_live",
		Type:        AccountTypeCodex,
		File:        liveFile,
		AccessToken: "sk-proj-live",
		PlanType:    "api",
		AuthMode:    accountAuthModeAPIKey,
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-4.1-mini","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-managed-api-402-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected first managed api key to be marked dead after 402")
	}
	liveState := snapshotProxyTestAccount(liveAcc)
	if liveState.Dead {
		t.Fatal("expected second managed api key to stay live")
	}
	if calls["Bearer sk-proj-dead"] == nil || calls["Bearer sk-proj-dead"].count != 2 {
		t.Fatalf("dead account calls = %+v", calls["Bearer sk-proj-dead"])
	}
	if calls["Bearer sk-proj-live"] == nil || calls["Bearer sk-proj-live"].count != 2 {
		t.Fatalf("live account calls = %+v", calls["Bearer sk-proj-live"])
	}
}

func TestProxyBufferedPaymentRequiredDeactivatedWorkspaceRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var deadCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer dead-seat-token":
			deadCalls++
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"deactivated_workspace"}`))
		case "Bearer live-seat-token":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	deadAcc := &Account{
		ID:          "codex_dead",
		Type:        AccountTypeCodex,
		AccessToken: "dead-seat-token",
		AccountID:   "acct-dead",
		PlanType:    "pro",
	}
	liveAcc := &Account{
		ID:          "codex_live",
		Type:        AccountTypeCodex,
		AccessToken: "live-seat-token",
		AccountID:   "acct-live",
		PlanType:    "pro",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.4","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-402-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected deactivated workspace account to be marked dead")
	}
	liveState := snapshotProxyTestAccount(liveAcc)
	if liveState.Dead {
		t.Fatal("expected fallback account to stay live")
	}
	if deadCalls != 1 || liveCalls != 1 {
		t.Fatalf("deadCalls=%d liveCalls=%d", deadCalls, liveCalls)
	}
}

func TestProxyBufferedRetryable5xxRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var deadCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer flaky-seat-token":
			deadCalls++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"server boom"}}`))
		case "Bearer live-seat-token":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	flakyAcc := &Account{
		ID:          "codex_flaky",
		Type:        AccountTypeCodex,
		AccessToken: "flaky-seat-token",
		AccountID:   "acct-flaky",
		PlanType:    "pro",
	}
	liveAcc := &Account{
		ID:          "codex_live",
		Type:        AccountTypeCodex,
		AccessToken: "live-seat-token",
		AccountID:   "acct-live",
		PlanType:    "pro",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{flakyAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.4","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-5xx-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	flakyState := snapshotProxyTestAccount(flakyAcc)
	if flakyState.Dead {
		t.Fatal("expected 5xx account to remain non-dead")
	}
	if flakyState.Penalty == 0 {
		t.Fatal("expected 5xx account penalty to increase")
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "fallback account to be used")
	if deadCalls != 1 || liveCalls != 1 {
		t.Fatalf("flakyCalls=%d liveCalls=%d", deadCalls, liveCalls)
	}
	recent := h.recent.snapshot()
	if len(recent) == 0 || !strings.Contains(recent[0], "502 Bad Gateway") {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestProxyBufferedTransientAuthFailureRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var deniedCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer denied-seat-token":
			deniedCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer live-seat-token":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","status":"completed"}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	deniedAcc := &Account{
		ID:          "codex_denied",
		Type:        AccountTypeCodex,
		AccessToken: "denied-seat-token",
		AccountID:   "acct-denied",
		PlanType:    "pro",
	}
	liveAcc := &Account{
		ID:          "codex_live",
		Type:        AccountTypeCodex,
		AccessToken: "live-seat-token",
		AccountID:   "acct-live",
		PlanType:    "pro",
	}

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deniedAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.4","input":"hi"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-403-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","status":"completed"}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deniedState := snapshotProxyTestAccount(deniedAcc)
	if deniedState.Dead {
		t.Fatal("expected transient auth failure account to remain non-dead")
	}
	if deniedState.Penalty == 0 {
		t.Fatal("expected transient auth failure to add penalty")
	}
	if deniedCalls != 1 || liveCalls != 1 {
		t.Fatalf("deniedCalls=%d liveCalls=%d", deniedCalls, liveCalls)
	}
}

func TestProxyBufferedGitLabClaude402QuotaExceededMarksDeadAndRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var quotaCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-quota":
			quotaCalls++
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"insufficient_credits","error_code":"USAGE_QUOTA_EXCEEDED"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	quotaAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_quota", "glpat-quota", "gateway-quota", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{quotaAcc, liveAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-402-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	quotaState := snapshotProxyTestAccount(quotaAcc)
	if !quotaState.Dead {
		t.Fatal("expected quota account to be marked dead")
	}
	if quotaState.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", quotaState.HealthStatus)
	}
	if !quotaState.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", quotaState.RateLimitUntil)
	}
	if quotaState.GitLabQuotaExceededCount != 0 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", quotaState.GitLabQuotaExceededCount)
	}
	if quotaCalls != 1 || liveCalls != 1 {
		t.Fatalf("quotaCalls=%d liveCalls=%d", quotaCalls, liveCalls)
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "live gitlab account to be used")
}

func TestProxyBufferedGitLabClaude403GatewayRejectedRetriesNextSeat(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var staleCalls, freshCalls, liveCalls, refreshCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-stale":
			staleCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer gateway-fresh":
			freshCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	rejectedAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_rejected", "glpat-rejected", "gateway-stale", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{rejectedAcc, liveAcc})
	h.refreshTransport = gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		refreshCalls++
		return gitlabClaudeJSONResponse(http.StatusOK, `{
			"token":"gateway-fresh",
			"base_url":"https://cloud.gitlab.com/ai/v1/proxy/anthropic",
			"expires_at":1911111111,
			"headers":{"X-Gitlab-Instance-Id":"inst-1"}
		}`), nil
	})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-403-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	rejectedState := snapshotProxyTestAccount(rejectedAcc)
	if rejectedState.Dead {
		t.Fatal("expected gateway-rejected account to remain live")
	}
	if rejectedState.HealthStatus != "gateway_rejected" {
		t.Fatalf("health_status=%q", rejectedState.HealthStatus)
	}
	if rejectedState.RateLimitUntil.IsZero() {
		t.Fatal("expected gateway rejection cooldown to be set")
	}
	if rejectedState.AccessToken != "gateway-fresh" {
		t.Fatalf("access_token=%q", rejectedState.AccessToken)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if staleCalls != 1 || freshCalls != 1 || liveCalls != 1 {
		t.Fatalf("staleCalls=%d freshCalls=%d liveCalls=%d", staleCalls, freshCalls, liveCalls)
	}
}

func TestProxyBufferedGitLabCodex402QuotaExceededRetriesNextSeatAndCooldown(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var quotaCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-quota":
			quotaCalls++
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"insufficient_credits","error_code":"USAGE_QUOTA_EXCEEDED","message":"Consumer does not have sufficient credits for this request"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	quotaAcc := newBufferedGitLabCodexAccountForTest(t, tmp, "codex_gitlab_quota", "glpat-quota", "gateway-quota", upstream.URL)
	liveAcc := newBufferedGitLabCodexAccountForTest(t, tmp, "codex_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{quotaAcc, liveAcc})
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab
	h.cfg.disableRefresh = true
	h.pool.activeCodexID = quotaAcc.ID
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"gpt-5.4-mini","input":"hi","stream":false}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/responses", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-codex-402-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`)) {
		t.Fatalf("body = %q", string(body))
	}

	quotaState := snapshotProxyTestAccount(quotaAcc)
	if quotaState.Dead {
		t.Fatal("expected quota-exceeded GitLab Codex seat to stay non-dead")
	}
	if quotaState.HealthStatus != "quota_exceeded" {
		t.Fatalf("health_status=%q", quotaState.HealthStatus)
	}
	if quotaState.GitLabQuotaExceededCount != 1 {
		t.Fatalf("gitlab_quota_exceeded_count=%d", quotaState.GitLabQuotaExceededCount)
	}
	if quotaState.RateLimitUntil.IsZero() {
		t.Fatal("expected quota-exceeded GitLab Codex seat to enter cooldown")
	}
	if quotaCalls != 1 || liveCalls != 1 {
		t.Fatalf("quotaCalls=%d liveCalls=%d", quotaCalls, liveCalls)
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "live gitlab codex account to be used")
}

func TestProxyBufferedGitLabCodex403GatewayRejectedRetriesNextSeatAndCooldown(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var rejectedCalls, liveCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-rejected":
			rejectedCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("error code: 1010"))
		case "Bearer gateway-live":
			liveCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_live","object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	rejectedAcc := newBufferedGitLabCodexAccountForTest(t, tmp, "codex_gitlab_rejected", "glpat-rejected", "gateway-rejected", upstream.URL)
	liveAcc := newBufferedGitLabCodexAccountForTest(t, tmp, "codex_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{rejectedAcc, liveAcc})
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab
	h.cfg.disableRefresh = true
	h.pool.activeCodexID = rejectedAcc.ID
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"gpt-5.4-mini","input":"hi","stream":false}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/responses", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-codex-403-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"resp_live","object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`)) {
		t.Fatalf("body = %q", string(body))
	}

	rejectedState := snapshotProxyTestAccount(rejectedAcc)
	if rejectedState.Dead {
		t.Fatal("expected gateway-rejected GitLab Codex seat to stay non-dead")
	}
	if rejectedState.HealthStatus != "gateway_rejected" {
		t.Fatalf("health_status=%q", rejectedState.HealthStatus)
	}
	if rejectedState.RateLimitUntil.IsZero() {
		t.Fatal("expected gateway-rejected GitLab Codex seat to enter cooldown")
	}
	if rejectedCalls != 1 || liveCalls != 1 {
		t.Fatalf("rejectedCalls=%d liveCalls=%d", rejectedCalls, liveCalls)
	}
	waitForBufferedProxySuccessAccountState(t, liveAcc, "live gitlab codex account to be used")
}

func TestProxyBufferedGitLabCodexAllCooldownReturns429(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	tmp := t.TempDir()
	firstAcc := newBufferedGitLabCodexAccountForTest(t, tmp, "codex_gitlab_cooldown_1", "glpat-1", "gateway-1", "https://gitlab.example.com/ai/v1/proxy/openai")
	secondAcc := newBufferedGitLabCodexAccountForTest(t, tmp, "codex_gitlab_cooldown_2", "glpat-2", "gateway-2", "https://gitlab.example.com/ai/v1/proxy/openai")
	firstAcc.HealthStatus = "quota_exceeded"
	firstAcc.HealthError = "Consumer does not have sufficient credits for this request"
	firstAcc.RateLimitUntil = time.Now().UTC().Add(2 * time.Minute)
	secondAcc.HealthStatus = "gateway_rejected"
	secondAcc.HealthError = "Forbidden"
	secondAcc.RateLimitUntil = time.Now().UTC().Add(90 * time.Second)

	h := newBufferedCodexProxyHandlerForTest(t, "https://gitlab.example.com/ai/v1/proxy/openai", []*Account{firstAcc, secondAcc})
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab
	h.cfg.disableRefresh = true
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"gpt-5.4-mini","input":"hi","stream":false}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/responses", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-codex-cooldown-user"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "Forbidden") && !strings.Contains(string(body), "credits") {
		t.Fatalf("body = %q", string(body))
	}
}

func TestProxyBufferedGitLabClaude429OrgTPMActivatesSharedCooldownWithoutSiblingFanout(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var firstCalls, secondCalls, thirdCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-tpm-1":
			firstCalls++
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"This request would exceed your organization's rate limit of 18,000,000 input tokens per minute"}}`))
		case "Bearer gateway-tpm-2":
			secondCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_unexpected","type":"message","content":[{"type":"text","text":"unexpected"}]}`))
		case "Bearer gateway-tpm-3":
			thirdCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_unexpected","type":"message","content":[{"type":"text","text":"unexpected"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	firstAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_tpm_1", "glpat-tpm-1", "gateway-tpm-1", upstream.URL)
	secondAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_tpm_2", "glpat-tpm-2", "gateway-tpm-2", upstream.URL)
	thirdAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_tpm_3", "glpat-tpm-3", "gateway-tpm-3", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{firstAcc, secondAcc, thirdAcc})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	makeRequest := func() (*http.Response, string) {
		reqBody := []byte(`{"model":"claude-opus-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-org-tpm-user"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("proxy request: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp, string(body)
	}

	resp, body := makeRequest()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got != "12" {
		t.Fatalf("retry_after=%q", got)
	}
	if !strings.Contains(body, "organization's rate limit") {
		t.Fatalf("body=%q", body)
	}
	if firstCalls != 1 || secondCalls != 0 || thirdCalls != 0 {
		t.Fatalf("firstCalls=%d secondCalls=%d thirdCalls=%d", firstCalls, secondCalls, thirdCalls)
	}

	firstState := snapshotProxyTestAccount(firstAcc)
	secondState := snapshotProxyTestAccount(secondAcc)
	thirdState := snapshotProxyTestAccount(thirdAcc)
	for _, item := range []struct {
		name  string
		state proxyTestAccountSnapshot
	}{
		{name: firstAcc.ID, state: firstState},
		{name: secondAcc.ID, state: secondState},
		{name: thirdAcc.ID, state: thirdState},
	} {
		state := item.state
		if state.RateLimitUntil.IsZero() {
			t.Fatalf("expected rate limit until for %s", item.name)
		}
		wait := time.Until(state.RateLimitUntil)
		if wait < 10*time.Second || wait > 13*time.Second {
			t.Fatalf("rate_limit_wait(%s)=%v", item.name, wait)
		}
		if state.HealthStatus != "rate_limited" {
			t.Fatalf("health_status(%s)=%q", item.name, state.HealthStatus)
		}
		if !strings.HasPrefix(state.HealthError, managedGitLabClaudeSharedOrgTPMHealthPrefix) {
			t.Fatalf("health_error(%s)=%q", item.name, state.HealthError)
		}
	}

	resp, body = makeRequest()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "organization's rate limit") {
		t.Fatalf("second body=%q", body)
	}
	if firstCalls != 1 || secondCalls != 0 || thirdCalls != 0 {
		t.Fatalf("unexpected fanout after shared cooldown: firstCalls=%d secondCalls=%d thirdCalls=%d", firstCalls, secondCalls, thirdCalls)
	}
}

func TestProxyBufferedGitLabClaude401RefreshInvalidGrantMarksDead(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var staleCalls, liveCalls, refreshCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-stale":
			staleCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"stale gateway token"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_dead", "glpat-dead", "gateway-stale", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	h.refreshTransport = gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		refreshCalls++
		return gitlabClaudeJSONResponse(http.StatusUnauthorized, `{"error":"invalid_grant"}`), nil
	})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-401-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected invalid_grant account to end dead")
	}
	if deadState.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", deadState.HealthStatus)
	}
	if !deadState.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", deadState.RateLimitUntil)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if staleCalls != 1 || liveCalls != 1 {
		t.Fatalf("staleCalls=%d liveCalls=%d", staleCalls, liveCalls)
	}
	saved, err := os.ReadFile(deadAcc.File)
	if err != nil {
		t.Fatalf("read saved dead account: %v", err)
	}
	if !strings.Contains(string(saved), `"dead": true`) {
		t.Fatalf("expected persisted dead flag, got %s", string(saved))
	}
}

func TestProxyBufferedGitLabClaude403DirectAccessForbiddenMarksDead(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	var staleCalls, liveCalls, refreshCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Gitlab-Instance-Id"); got != "inst-1" {
			t.Fatalf("missing gitlab header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer gateway-stale":
			staleCalls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"temporary denied"}`))
		case "Bearer gateway-live":
			liveCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`))
		default:
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	deadAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_forbidden", "glpat-dead", "gateway-stale", upstream.URL)
	liveAcc := newBufferedGitLabClaudeAccountForTest(t, tmp, "claude_gitlab_live", "glpat-live", "gateway-live", upstream.URL)

	h := newBufferedCodexProxyHandlerForTest(t, upstream.URL, []*Account{deadAcc, liveAcc})
	h.refreshTransport = gitlabClaudeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		refreshCalls++
		return gitlabClaudeJSONResponse(http.StatusForbidden, `{"message":"forbidden"}`), nil
	})
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "buffered-gitlab-403-direct-user"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte(`{"id":"msg_live","type":"message","content":[{"type":"text","text":"OK"}]}`)) {
		t.Fatalf("body = %q", string(body))
	}
	deadState := snapshotProxyTestAccount(deadAcc)
	if !deadState.Dead {
		t.Fatal("expected direct_access forbidden account to end dead")
	}
	if deadState.HealthStatus != "dead" {
		t.Fatalf("health_status=%q", deadState.HealthStatus)
	}
	if !deadState.RateLimitUntil.IsZero() {
		t.Fatalf("rate_limit_until=%v", deadState.RateLimitUntil)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls=%d", refreshCalls)
	}
	if staleCalls != 1 || liveCalls != 1 {
		t.Fatalf("staleCalls=%d liveCalls=%d", staleCalls, liveCalls)
	}
}
