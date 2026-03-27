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
	if seatOneState.RateLimitUntil.IsZero() {
		t.Fatal("expected first Gemini seat to enter cooldown after 429")
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
