package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaybeBuildCodexChatCompletionsResponsesRequestMapsTextMessagesAndForcesResponsesStream(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"messages":[
			{"role":"system","content":"system guidance"},
			{"role":"developer","content":"developer note"},
			{"role":"user","content":"hello pool"},
			{"role":"assistant","content":"hello human"},
			{"role":"user","content":[{"type":"text","text":"next question"}]}
		],
		"stream":false,
		"max_tokens":123,
		"max_completion_tokens":456
	}`)

	upstreamPath, rewritten, adapter, clientStream, err := maybeBuildCodexChatCompletionsResponsesRequest("/v1/chat/completions", "gpt-5.5", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if upstreamPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", upstreamPath)
	}
	if adapter != responseAdapterCodexChatCompletionsFromResponsesBuffered {
		t.Fatalf("response adapter = %q, want %q", adapter, responseAdapterCodexChatCompletionsFromResponsesBuffered)
	}
	if clientStream {
		t.Fatal("client stream flag = true, want false for buffered chat client")
	}

	payload := decodeCodexResponsesAdapterJSON(t, rewritten)
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("model = %v", payload["model"])
	}
	if payload["store"] != false {
		t.Fatalf("store = %v, want forced false", payload["store"])
	}
	if payload["stream"] != true {
		t.Fatalf("stream = %v, want forced true for upstream Codex Responses", payload["stream"])
	}
	for _, stripped := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := payload[stripped]; ok {
			t.Fatalf("%s was forwarded to Codex Responses body: %s", stripped, string(rewritten))
		}
	}

	instructions, ok := payload["instructions"].(string)
	if !ok || !strings.Contains(instructions, "system guidance") || !strings.Contains(instructions, "developer note") {
		t.Fatalf("instructions = %#v, want system/developer guidance", payload["instructions"])
	}
	input, ok := payload["input"].([]any)
	if !ok {
		t.Fatalf("input has type %T, want []any in %s", payload["input"], string(rewritten))
	}
	if len(input) != 3 {
		t.Fatalf("input length = %d, want 3 user/assistant messages only: %#v", len(input), input)
	}
	requireCodexResponsesTextInput(t, input[0], "user", "hello pool")
	requireCodexResponsesTextInput(t, input[1], "assistant", "hello human")
	requireCodexResponsesTextInput(t, input[2], "user", "next question")
}

func TestMaybeBuildCodexChatCompletionsResponsesRequestPreservesClientStreamTargetWhileForcingUpstreamStream(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"stream please"}],"stream":true}`)

	upstreamPath, rewritten, adapter, clientStream, err := maybeBuildCodexChatCompletionsResponsesRequest("/v1/chat/completions", "gpt-5.5", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if upstreamPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", upstreamPath)
	}
	if adapter != responseAdapterCodexChatCompletionsFromResponsesStream {
		t.Fatalf("response adapter = %q, want %q", adapter, responseAdapterCodexChatCompletionsFromResponsesStream)
	}
	if !clientStream {
		t.Fatal("client stream flag = false, want true for streaming chat client")
	}
	payload := decodeCodexResponsesAdapterJSON(t, rewritten)
	if payload["stream"] != true {
		t.Fatalf("upstream stream = %v, want forced true", payload["stream"])
	}
	if payload["store"] != false {
		t.Fatalf("upstream store = %v, want forced false", payload["store"])
	}
	if payload["instructions"] != "You are a helpful assistant." {
		t.Fatalf("default instructions = %#v", payload["instructions"])
	}
}

func TestMaybeBuildCodexChatCompletionsResponsesRequestForcesUpstreamStreamWhenClientOmitsStream(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"plain please"}]}`)

	_, rewritten, adapter, clientStream, err := maybeBuildCodexChatCompletionsResponsesRequest("/v1/chat/completions", "gpt-5.5", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if adapter != responseAdapterCodexChatCompletionsFromResponsesBuffered {
		t.Fatalf("response adapter = %q, want buffered", adapter)
	}
	if clientStream {
		t.Fatal("client stream flag = true, want false when client omits stream")
	}
	payload := decodeCodexResponsesAdapterJSON(t, rewritten)
	if payload["stream"] != true {
		t.Fatalf("upstream stream = %v, want forced true", payload["stream"])
	}
}

func TestMaybeBuildCodexChatCompletionsResponsesRequestRejectsUnsupportedWave1Features(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "tools",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`,
			want: "tools",
		},
		{
			name: "tool_choice",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"tool_choice":"auto"}`,
			want: "tool_choice",
		},
		{
			name: "legacy_functions",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"functions":[{"name":"lookup"}]}`,
			want: "functions",
		},
		{
			name: "parallel_tool_calls",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"parallel_tool_calls":true}`,
			want: "parallel_tool_calls",
		},
		{
			name: "response_format",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{}}}}`,
			want: "response_format",
		},
		{
			name: "temperature",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"temperature":0.2}`,
			want: "temperature",
		},
		{
			name: "n",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"n":2}`,
			want: "n",
		},
		{
			name: "image_url_content",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`,
			want: "image_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := maybeBuildCodexChatCompletionsResponsesRequest("/v1/chat/completions", "gpt-5.5", []byte(tt.body))
			if err == nil {
				t.Fatal("adapter accepted unsupported Wave 1 chat feature; want deterministic local error")
			}
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, tt.want) {
				t.Fatalf("error = %q, want it to name unsupported field %q", err.Error(), tt.want)
			}
			if strings.Contains(message, "hello") || strings.Contains(message, "example.invalid") {
				t.Fatalf("error leaks request content instead of only field names: %q", err.Error())
			}
		})
	}
}

func decodeCodexResponsesAdapterJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode rewritten JSON: %v; body=%s", err, string(raw))
	}
	return out
}

func requireCodexResponsesTextInput(t *testing.T, item any, wantRole, wantText string) {
	t.Helper()
	obj, ok := item.(map[string]any)
	if !ok {
		t.Fatalf("input item has type %T, want object: %#v", item, item)
	}
	if got := obj["role"]; got != wantRole {
		t.Fatalf("role = %v, want %q in %#v", got, wantRole, obj)
	}
	content, ok := obj["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one text content item", obj["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] has type %T, want object: %#v", content[0], content[0])
	}
	if got := part["type"]; got != "input_text" {
		t.Fatalf("content type = %v, want input_text in %#v", got, part)
	}
	if got := part["text"]; got != wantText {
		t.Fatalf("content text = %v, want %q in %#v", got, wantText, part)
	}
}
