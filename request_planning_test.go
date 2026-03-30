package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newPlanningTestHandler(t *testing.T) *proxyHandler {
	t.Helper()

	baseURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	kimi := NewKimiProvider(baseURL)
	minimax := NewMinimaxProvider(baseURL)
	return &proxyHandler{
		registry: NewProviderRegistry(codex, claude, gemini, kimi, minimax),
	}
}

func TestBuildBufferedRequestShapeExtractsConversationAndModel(t *testing.T) {
	req := httptest.NewRequest("POST", "http://example.com/responses", nil)
	body := []byte(`{"model":"gpt-5.3-codex-spark","prompt_cache_key":"thread-123","input":"hi"}`)

	shape := buildBufferedRequestShape(req, body, nil)

	if shape.Path != "/responses" {
		t.Fatalf("path = %q", shape.Path)
	}
	if shape.ConversationID != "thread-123" {
		t.Fatalf("conversation_id = %q", shape.ConversationID)
	}
	if shape.RequestedModel != "gpt-5.3-codex-spark" {
		t.Fatalf("requested_model = %q", shape.RequestedModel)
	}
}

func TestBuildStreamedRequestShapeIsOpaque(t *testing.T) {
	req := httptest.NewRequest("POST", "http://example.com/v1/messages", nil)
	req.Header.Set("Session-Id", "stream-thread-1")

	shape := buildStreamedRequestShape(req)

	if shape.Path != "/v1/messages" {
		t.Fatalf("path = %q", shape.Path)
	}
	if shape.ConversationID != "stream-thread-1" {
		t.Fatalf("conversation_id = %q", shape.ConversationID)
	}
	if shape.RequestedModel != "" {
		t.Fatalf("requested_model = %q", shape.RequestedModel)
	}
}

func TestBuildWebSocketRequestShapeExtractsConversationID(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/responses?session_id=thread-ws-1", nil)

	shape := buildWebSocketRequestShape(req)
	if shape.Path != "/responses" {
		t.Fatalf("path = %q", shape.Path)
	}
	if shape.ConversationID != "thread-ws-1" {
		t.Fatalf("conversation_id = %q", shape.ConversationID)
	}
}

func TestPlanRoutePrefersClaudeHeadersOverPath(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/responses", nil)
	req.Header.Set("X-Api-Key", "sk-ant-api03-real-key")

	plan, _, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, buildStreamedRequestShape(req), nil)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeClaude {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
}

func TestPlanRouteOverridesProviderAndRewritesMiniMaxModel(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/responses", nil)
	body := []byte(`{"model":"minimax","input":"hi"}`)
	shape := buildBufferedRequestShape(req, body, nil)

	plan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeMinimax {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if !strings.Contains(string(rewrittenBody), "MiniMax-M2.5") {
		t.Fatalf("rewritten body = %s", string(rewrittenBody))
	}
}

func TestPlanRouteOverridesOpenCodeChatCompletionsToGemini(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	body := []byte(`{"model":"gemini-3.1-pro","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	shape := buildBufferedRequestShape(req, body, nil)

	plan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeGemini {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if plan.UpstreamPath != "/v1beta/models/gemini-3.1-pro-high:streamGenerateContent" {
		t.Fatalf("upstream_path = %q", plan.UpstreamPath)
	}
	if plan.ResponseAdapter != responseAdapterOpenAIChatCompletionsGemini {
		t.Fatalf("response_adapter = %q", plan.ResponseAdapter)
	}
	if !strings.Contains(string(rewrittenBody), `"contents"`) {
		t.Fatalf("rewritten body = %s", string(rewrittenBody))
	}
}

func TestPlanRouteKeepsOpenCodeChatCompletionsGeminiLowDirect(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	body := []byte(`{"model":"gemini-3.1-pro-low","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	shape := buildBufferedRequestShape(req, body, nil)

	plan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeGemini {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if plan.UpstreamPath != "/v1beta/models/gemini-3.1-pro-low:streamGenerateContent" {
		t.Fatalf("upstream_path = %q", plan.UpstreamPath)
	}
	if plan.ResponseAdapter != responseAdapterOpenAIChatCompletionsGemini {
		t.Fatalf("response_adapter = %q", plan.ResponseAdapter)
	}
	if !strings.Contains(string(rewrittenBody), `"contents"`) {
		t.Fatalf("rewritten body = %s", string(rewrittenBody))
	}
}

func TestPlanRouteOverridesAnthropicMessagesToGemini(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/v1/messages", nil)
	req.Header.Set("X-Api-Key", "pool-token-placeholder")
	body := []byte(`{"model":"gemini-3.1-pro","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	shape := buildBufferedRequestShape(req, body, nil)

	plan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeGemini {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if plan.UpstreamPath != "/v1beta/models/gemini-3.1-pro-high:streamGenerateContent" {
		t.Fatalf("upstream_path = %q", plan.UpstreamPath)
	}
	if plan.ResponseAdapter != responseAdapterAnthropicMessagesGeminiStream {
		t.Fatalf("response_adapter = %q", plan.ResponseAdapter)
	}
	if !strings.Contains(string(rewrittenBody), `"contents"`) {
		t.Fatalf("rewritten body = %s", string(rewrittenBody))
	}
}

func TestPlanRouteKeepsAnthropicMessagesGeminiLowDirect(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/v1/messages", nil)
	req.Header.Set("X-Api-Key", "pool-token-placeholder")
	body := []byte(`{"model":"gemini-3.1-pro-low","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	shape := buildBufferedRequestShape(req, body, nil)

	plan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeGemini {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if plan.UpstreamPath != "/v1beta/models/gemini-3.1-pro-low:generateContent" {
		t.Fatalf("upstream_path = %q", plan.UpstreamPath)
	}
	if plan.ResponseAdapter != responseAdapterAnthropicMessagesGeminiStream {
		t.Fatalf("response_adapter = %q", plan.ResponseAdapter)
	}
	if !strings.Contains(string(rewrittenBody), `"contents"`) {
		t.Fatalf("rewritten body = %s", string(rewrittenBody))
	}
	if strings.Contains(string(rewrittenBody), "thinkingConfig") {
		t.Fatalf("rewritten body should not force thinkingConfig = %s", string(rewrittenBody))
	}
}

func TestPlanRouteRequiresCodexPro(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/responses", nil)
	body := []byte(`{"model":"gpt-5.3-codex-spark","input":"hi"}`)
	shape := buildBufferedRequestShape(req, body, nil)

	plan, _, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, body)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeCodex {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if plan.RequiredPlan != "pro" {
		t.Fatalf("required_plan = %q", plan.RequiredPlan)
	}
}

func TestPlanRouteStreamedBodyDoesNotInferRequiredPlan(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest("POST", "http://example.com/responses", nil)
	shape := buildStreamedRequestShape(req)

	plan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, nil)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.RequiredPlan != "" {
		t.Fatalf("required_plan = %q", plan.RequiredPlan)
	}
	if rewrittenBody != nil {
		t.Fatalf("rewritten body should be nil")
	}
}

func TestPlanRouteForcedGitLabPlanForCodexResponses(t *testing.T) {
	h := newPlanningTestHandler(t)
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab

	req := httptest.NewRequest("POST", "http://example.com/responses", nil)
	shape := buildStreamedRequestShape(req)

	plan, rewrittenBody, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, shape, nil)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeCodex {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if plan.RequiredPlan != accountAuthModeGitLab {
		t.Fatalf("required_plan = %q", plan.RequiredPlan)
	}
	if rewrittenBody != nil {
		t.Fatalf("rewritten body should be nil")
	}
}

func TestPlanRouteForcedGitLabPlanDoesNotAffectClaude(t *testing.T) {
	h := newPlanningTestHandler(t)
	h.cfg.forceCodexRequiredPlan = accountAuthModeGitLab

	req := httptest.NewRequest("POST", "http://example.com/v1/messages", nil)
	req.Header.Set("X-Api-Key", "sk-ant-api03-real-key")

	plan, _, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "u1"}, req, buildStreamedRequestShape(req), nil)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.AccountType != AccountTypeClaude {
		t.Fatalf("account_type = %q", plan.AccountType)
	}
	if plan.RequiredPlan != "" {
		t.Fatalf("required_plan = %q", plan.RequiredPlan)
	}
}

func TestResolveDebugGeminiSeatOverrideAllowsTrustedLoopback(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8989/v1internal:generateContent", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set(debugGeminiSeatHeader, "gemini-seat-1")

	seatID, statusCode, err := h.resolveDebugGeminiSeatOverride(req, AccountTypeGemini)
	if err != nil {
		t.Fatalf("resolve override: %v", err)
	}
	if statusCode != 0 {
		t.Fatalf("status_code = %d", statusCode)
	}
	if seatID != "gemini-seat-1" {
		t.Fatalf("seat_id = %q", seatID)
	}
}

func TestResolveDebugGeminiSeatOverrideRejectsUntrustedRequest(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1internal:generateContent", nil)
	req.RemoteAddr = "203.0.113.10:4242"
	req.Header.Set(debugGeminiSeatHeader, "gemini-seat-1")

	_, statusCode, err := h.resolveDebugGeminiSeatOverride(req, AccountTypeGemini)
	if err == nil {
		t.Fatal("expected error")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("status_code = %d", statusCode)
	}
}

func TestResolveDebugGeminiSeatOverrideRejectsNonGeminiRoute(t *testing.T) {
	h := newPlanningTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8989/responses", nil)
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set(debugGeminiSeatHeader, "gemini-seat-1")

	_, statusCode, err := h.resolveDebugGeminiSeatOverride(req, AccountTypeCodex)
	if err == nil {
		t.Fatal("expected error")
	}
	if statusCode != http.StatusBadRequest {
		t.Fatalf("status_code = %d", statusCode)
	}
}
