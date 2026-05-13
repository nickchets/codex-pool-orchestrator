package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyRequestAdaptsCodexChatCompletionsRouteToResponses(t *testing.T) {
	var seenRequests atomic.Int32
	var upstreamPath string
	var upstreamBody string

	transport := phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenRequests.Add(1)
		upstreamPath = req.URL.Path
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"event: response.output_text.delta\n" +
					`data: {"type":"response.output_text.delta","delta":"adapted ok"}` + "\n\n" +
					"event: response.completed\n" +
					`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}` + "\n\n" +
					"data: [DONE]\n\n",
			)),
			Request: req,
		}, nil
	})
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{}, transport)
	h.cfg.codexChatCompletionsResponsesAdapter = true

	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.5","messages":[{"role":"user","content":"canary"}],"stream":false}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-codex-chat-adapter")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if seenRequests.Load() != 1 {
		t.Fatalf("upstream requests=%d, want 1", seenRequests.Load())
	}
	if upstreamPath != "/responses" {
		t.Fatalf("provider-normalized upstream path=%q, want /responses", upstreamPath)
	}
	for _, want := range []string{`"model":"gpt-5.5"`, `"input"`, `"stream":true`} {
		if !strings.Contains(upstreamBody, want) {
			t.Fatalf("upstream adapted body missing %q: %s", want, upstreamBody)
		}
	}
	if strings.Contains(upstreamBody, `"messages"`) {
		t.Fatalf("upstream body still contains chat messages envelope: %s", upstreamBody)
	}

	var out openAIChatCompletionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode chat response: %v; body=%s", err, rr.Body.String())
	}
	if out.Object != "chat.completion" || out.Model != "gpt-5.5" {
		t.Fatalf("unexpected chat envelope: object=%q model=%q body=%s", out.Object, out.Model, rr.Body.String())
	}
	if len(out.Choices) != 1 || out.Choices[0].Message == nil || out.Choices[0].Message.Content != "adapted ok" {
		t.Fatalf("unexpected choices: %#v body=%s", out.Choices, rr.Body.String())
	}
	if out.Usage == nil || out.Usage.PromptTokens != 3 || out.Usage.CompletionTokens != 2 || out.Usage.TotalTokens != 5 {
		t.Fatalf("usage=%#v", out.Usage)
	}
	if strings.Contains(rr.Body.String(), "response.output_text.delta") || strings.Contains(rr.Body.String(), "data:") {
		t.Fatalf("chat response leaked responses SSE details: %s", rr.Body.String())
	}
}
