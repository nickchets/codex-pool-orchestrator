package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMaybeBuildOpenAIChatCompletionsGeminiRequest(t *testing.T) {
	body := []byte(`{"model":"gemini-3.1-pro","messages":[{"role":"system","content":"sys-1"},{"role":"system","content":"sys-2"},{"role":"user","content":"hello"}],"max_tokens":123,"top_p":0.7,"stream":true}`)
	path, rewritten, adapter, isStream, err := maybeBuildOpenAIChatCompletionsGeminiRequest("/v1/chat/completions", "gemini-3.1-pro", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if path != "/v1beta/models/gemini-3.1-pro-high:streamGenerateContent" {
		t.Fatalf("path = %q", path)
	}
	if adapter != responseAdapterOpenAIChatCompletionsGemini {
		t.Fatalf("adapter = %q", adapter)
	}
	if !isStream {
		t.Fatal("expected stream")
	}
	text := string(rewritten)
	for _, want := range []string{`"systemInstruction"`, `"contents"`, `"maxOutputTokens":123`, `"topP":0.7`, `"sys-1\n\nsys-2"`, `"hello"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten body missing %q: %s", want, text)
		}
	}
}

func TestMaybeBuildOpenAIChatCompletionsGeminiRequestKeepsGemini31LowDirect(t *testing.T) {
	body := []byte(`{"model":"gemini-3.1-pro-low","messages":[{"role":"user","content":"hello"}],"max_tokens":96,"stream":true}`)
	path, rewritten, adapter, isStream, err := maybeBuildOpenAIChatCompletionsGeminiRequest("/v1/chat/completions", "gemini-3.1-pro-low", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if path != "/v1beta/models/gemini-3.1-pro-low:streamGenerateContent" {
		t.Fatalf("path = %q", path)
	}
	if adapter != responseAdapterOpenAIChatCompletionsGemini {
		t.Fatalf("adapter = %q", adapter)
	}
	if !isStream {
		t.Fatal("expected stream")
	}
	text := string(rewritten)
	if !strings.Contains(text, `"maxOutputTokens":96`) {
		t.Fatalf("rewritten body missing maxOutputTokens: %s", text)
	}
}

func TestMaybeBuildAnthropicMessagesGeminiRequest(t *testing.T) {
	body := []byte(`{"model":"gemini-3.1-pro","system":"sys","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"max_tokens":123,"top_p":0.7,"stream":true}`)
	path, rewritten, adapter, isStream, err := maybeBuildAnthropicMessagesGeminiRequest("/v1/messages", "gemini-3.1-pro", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if path != "/v1beta/models/gemini-3.1-pro-high:streamGenerateContent" {
		t.Fatalf("path = %q", path)
	}
	if adapter != responseAdapterAnthropicMessagesGeminiStream {
		t.Fatalf("adapter = %q", adapter)
	}
	if !isStream {
		t.Fatal("expected stream")
	}
	text := string(rewritten)
	for _, want := range []string{
		`"systemInstruction"`,
		`"contents"`,
		`"maxOutputTokens":32768`,
		`"thinkingConfig":{"thinkingBudget":24576}`,
		`"topP":0.7`,
		`"hello"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten body missing %q: %s", want, text)
		}
	}
}

func TestMaybeBuildAnthropicMessagesGeminiRequestForcesStreamTransportForGemini31NonStream(t *testing.T) {
	body := []byte(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`)
	path, rewritten, adapter, isStream, err := maybeBuildAnthropicMessagesGeminiRequest("/v1/messages", "gemini-3.1-pro-high", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if path != "/v1beta/models/gemini-3.1-pro-high:streamGenerateContent" {
		t.Fatalf("path = %q", path)
	}
	if adapter != responseAdapterAnthropicMessagesGemini {
		t.Fatalf("adapter = %q", adapter)
	}
	if isStream {
		t.Fatal("expected non-stream client mode")
	}
	text := string(rewritten)
	for _, want := range []string{`"maxOutputTokens":32768`, `"thinkingConfig":{"thinkingBudget":24576}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten body missing %q: %s", want, text)
		}
	}
}

func TestMaybeBuildAnthropicMessagesGeminiRequestKeepsGemini31LowDirect(t *testing.T) {
	body := []byte(`{"model":"gemini-3.1-pro-low","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`)
	path, rewritten, adapter, isStream, err := maybeBuildAnthropicMessagesGeminiRequest("/v1/messages", "gemini-3.1-pro-low", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if path != "/v1beta/models/gemini-3.1-pro-low:generateContent" {
		t.Fatalf("path = %q", path)
	}
	if adapter != responseAdapterAnthropicMessagesGemini {
		t.Fatalf("adapter = %q", adapter)
	}
	if isStream {
		t.Fatal("expected non-stream client mode")
	}
	text := string(rewritten)
	if strings.Contains(text, "thinkingConfig") {
		t.Fatalf("rewritten body should not force thinkingConfig: %s", text)
	}
	if !strings.Contains(text, `"maxOutputTokens":64`) {
		t.Fatalf("rewritten body missing maxOutputTokens: %s", text)
	}
}

func TestMaybeBuildAnthropicMessagesGeminiRequestWithTools(t *testing.T) {
	body := []byte(`{
		"model":"gemini-2.5-flash",
		"system":[{"type":"text","text":"sys"}],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"\/workspace"}]}
		],
		"tools":[
			{"name":"bash","description":"run bash","input_schema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}}
		],
		"tool_choice":{"type":"auto"},
		"stream":true
	}`)
	_, rewritten, _, _, err := maybeBuildAnthropicMessagesGeminiRequest("/v1/messages", "gemini-2.5-flash", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	text := string(rewritten)
	for _, want := range []string{
		`"functionDeclarations"`,
		`"functionCallingConfig":{"mode":"AUTO"}`,
		`"functionCall"`,
		`"functionResponse"`,
		`"command":"pwd"`,
		`"result":"/workspace"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten body missing %q: %s", want, text)
		}
	}
}

func TestMaybeBuildAnthropicMessagesGeminiRequestReinjectsThoughtSignatureForToolUse(t *testing.T) {
	t.Cleanup(clearGeminiThoughtSignatureCache)
	clearGeminiThoughtSignatureCache()

	raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"pwd"},"id":"toolu_1"},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}]}`)
	if _, err := buildAnthropicMessagesResponse("gemini-3.1-pro-high", raw); err != nil {
		t.Fatalf("build response: %v", err)
	}

	body := []byte(`{
		"model":"gemini-3.1-pro-high",
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"\/home\/lap"}]}
		],
		"tools":[
			{"name":"bash","description":"run bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}
		]
	}`)
	_, rewritten, _, _, err := maybeBuildAnthropicMessagesGeminiRequest("/v1/messages", "gemini-3.1-pro-high", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}
	if !strings.Contains(string(rewritten), `"thoughtSignature":"sig-1"`) {
		t.Fatalf("rewritten body missing thoughtSignature: %s", string(rewritten))
	}
}

func TestBuildOpenAIChatCompletionsResponse(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"AG_POOL_OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}`)
	out, err := buildOpenAIChatCompletionsResponse("gemini-2.5-flash", raw)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	text := string(out)
	for _, want := range []string{`"object":"chat.completion"`, `"model":"gemini-2.5-flash"`, `"content":"AG_POOL_OK"`, `"finish_reason":"stop"`, `"total_tokens":7`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %q: %s", want, text)
		}
	}
}

func TestMaybeTransformOpenAIChatCompletionsGeminiResponseStream(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n",
		)),
	}
	if err := maybeTransformOpenAIChatCompletionsGeminiResponse(responseAdapterOpenAIChatCompletionsGemini, "gemini-3.1-pro", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(got)
	for _, want := range []string{`"object":"chat.completion.chunk"`, `"role":"assistant"`, `"content":"Hello"`, `"finish_reason":"stop"`, `data: [DONE]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("stream missing %q: %s", want, text)
		}
	}
}

func TestMaybeTransformAnthropicMessagesGeminiResponseStream(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n",
		)),
	}
	if err := maybeTransformAnthropicMessagesGeminiResponse(responseAdapterAnthropicMessagesGeminiStream, "gemini-3.1-pro", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(got)
	for _, want := range []string{`event: message_start`, `"type":"content_block_delta"`, `"text":"Hello"`, `"stop_reason":"end_turn"`, `event: message_stop`} {
		if !strings.Contains(text, want) {
			t.Fatalf("stream missing %q: %s", want, text)
		}
	}
	if strings.Count(text, `"usage":{"input_tokens":0,"output_tokens":0}`) < 2 {
		t.Fatalf("expected zero usage on message_start and message_delta: %s", text)
	}
}

func TestMaybeTransformAnthropicMessagesGeminiResponseFromSSEToJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4}}\n\n",
		)),
	}
	if err := maybeTransformAnthropicMessagesGeminiResponse(responseAdapterAnthropicMessagesGemini, "gemini-3.1-pro-high", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(got)
	for _, want := range []string{`"type":"message"`, `"text":"Hello"`, `"stop_reason":"end_turn"`, `"input_tokens":3`, `"output_tokens":4`} {
		if !strings.Contains(text, want) {
			t.Fatalf("json response missing %q: %s", want, text)
		}
	}
	if gotType := resp.Header.Get("Content-Type"); gotType != "application/json" {
		t.Fatalf("content-type = %q", gotType)
	}
}

func TestMaybeTransformAnthropicMessagesGeminiResponseFromJSONArrayToJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`[` +
				`{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]},` +
				`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4}}` +
				`]`,
		)),
	}
	if err := maybeTransformAnthropicMessagesGeminiResponse(responseAdapterAnthropicMessagesGemini, "gemini-3.1-pro-high", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(got)
	for _, want := range []string{`"type":"message"`, `"text":"Hello"`, `"stop_reason":"end_turn"`, `"input_tokens":3`, `"output_tokens":4`} {
		if !strings.Contains(text, want) {
			t.Fatalf("json response missing %q: %s", want, text)
		}
	}
	if gotType := resp.Header.Get("Content-Type"); gotType != "application/json" {
		t.Fatalf("content-type = %q", gotType)
	}
}

func TestMaybeTransformAnthropicMessagesGeminiResponseStreamFromJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}]}`,
		)),
	}
	if err := maybeTransformAnthropicMessagesGeminiResponse(responseAdapterAnthropicMessagesGeminiStream, "gemini-3.1-pro", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(got)
	for _, want := range []string{`event: message_start`, `"text":"Hello"`, `event: message_stop`} {
		if !strings.Contains(text, want) {
			t.Fatalf("stream missing %q: %s", want, text)
		}
	}
}

func TestBuildAnthropicMessagesResponseToolUse(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"pwd"},"id":"toolu_1"}}]},"finishReason":"STOP"}]}`)
	out, err := buildAnthropicMessagesResponse("gemini-2.5-flash", raw)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	text := string(out)
	for _, want := range []string{`"type":"tool_use"`, `"name":"bash"`, `"id":"toolu_1"`, `"input":{"command":"pwd"}`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %q: %s", want, text)
		}
	}
}

func TestMaybeTransformAnthropicMessagesGeminiResponseStreamToolUse(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"bash\",\"args\":{\"command\":\"pwd\"},\"id\":\"toolu_1\"}}]}}]}\n\n" +
				"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4}}\n\n",
		)),
	}
	if err := maybeTransformAnthropicMessagesGeminiResponse(responseAdapterAnthropicMessagesGeminiStream, "gemini-2.5-flash", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		`event: message_start`,
		`"type":"tool_use"`,
		`"id":"toolu_1"`,
		`"type":"input_json_delta"`,
		`"partial_json":"{\"command\":\"pwd\"}"`,
		`"stop_reason":"tool_use"`,
		`"usage":{"input_tokens":3,"output_tokens":4}`,
		`event: message_stop`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("stream missing %q: %s", want, text)
		}
	}
}

func TestMaybeTransformAnthropicMessagesGeminiResponseStreamCachesThoughtSignatureForFollowup(t *testing.T) {
	t.Cleanup(clearGeminiThoughtSignatureCache)
	clearGeminiThoughtSignatureCache()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"bash\",\"args\":{\"command\":\"pwd\"},\"id\":\"toolu_stream\"},\"thoughtSignature\":\"sig-stream\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n",
		)),
	}
	if err := maybeTransformAnthropicMessagesGeminiResponse(responseAdapterAnthropicMessagesGeminiStream, "gemini-3.1-pro-high", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}

	body := []byte(`{
		"model":"gemini-3.1-pro-high",
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_stream","name":"bash","input":{"command":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_stream","content":"\/home\/lap"}]}
		],
		"tools":[
			{"name":"bash","description":"run bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}
		]
	}`)
	_, rewritten, _, _, err := maybeBuildAnthropicMessagesGeminiRequest("/v1/messages", "gemini-3.1-pro-high", body)
	if err != nil {
		t.Fatalf("adapter request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("unmarshal rewritten: %v", err)
	}
	contents, _ := payload["contents"].([]any)
	if len(contents) == 0 {
		t.Fatalf("missing contents: %s", string(rewritten))
	}
	first, _ := contents[0].(map[string]any)
	parts, _ := first["parts"].([]any)
	if len(parts) == 0 {
		t.Fatalf("missing parts: %s", string(rewritten))
	}
	part, _ := parts[0].(map[string]any)
	if got := part["thoughtSignature"]; got != "sig-stream" {
		t.Fatalf("thoughtSignature = %#v, want sig-stream; payload=%s", got, string(rewritten))
	}
}
