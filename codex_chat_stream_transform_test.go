package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMaybeTransformCodexChatCompletionsResponsesResponseStreamsTextDeltaAsChatChunks(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: response.created\n" +
				`data: {"type":"response.created","response":{"id":"resp_test"}}` + "\n\n" +
				"event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","delta":"Hello"}` + "\n\n" +
				"event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","delta":" world"}` + "\n\n" +
				"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}` + "\n\n" +
				"data: [DONE]\n\n",
		)),
	}

	if err := maybeTransformCodexChatCompletionsResponsesResponse(responseAdapterCodexChatCompletionsFromResponsesStream, "gpt-5.5", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read transformed stream: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		`"object":"chat.completion.chunk"`,
		`"model":"gpt-5.5"`,
		`"role":"assistant"`,
		`"content":"Hello"`,
		`"content":" world"`,
		`"finish_reason":"stop"`,
		`"prompt_tokens":3`,
		`"completion_tokens":2`,
		`"total_tokens":5`,
		"data: [DONE]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("transformed stream missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "response.output_text.delta") || strings.Contains(text, "resp_test") {
		t.Fatalf("Responses-native event details leaked through Chat SSE transform: %s", text)
	}
}

func TestMaybeTransformCodexChatCompletionsResponsesResponseBuffersInternalStreamForNonStreamChatClient(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","delta":"Hello"}` + "\n\n" +
				"event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","delta":" buffered"}` + "\n\n" +
				"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}` + "\n\n" +
				"data: [DONE]\n\n",
		)),
	}

	if err := maybeTransformCodexChatCompletionsResponsesResponse(responseAdapterCodexChatCompletionsFromResponsesBuffered, "gpt-5.5", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	if got := strings.ToLower(resp.Header.Get("Content-Type")); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json for buffered Chat response", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read buffered response: %v", err)
	}
	var out openAIChatCompletionResponse
	if err := jsonUnmarshalCodexChatBufferedResponse(got, &out); err != nil {
		t.Fatalf("decode buffered chat response: %v; body=%s", err, string(got))
	}
	if out.Object != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", out.Object)
	}
	if out.Model != "gpt-5.5" {
		t.Fatalf("model = %q", out.Model)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message == nil {
		t.Fatalf("choices = %#v, want one message choice", out.Choices)
	}
	if out.Choices[0].Message.Role != "assistant" {
		t.Fatalf("message role = %q, want assistant", out.Choices[0].Message.Role)
	}
	if out.Choices[0].Message.Content != "Hello buffered" {
		t.Fatalf("message content = %q, want concatenated delta text", out.Choices[0].Message.Content)
	}
	if out.Choices[0].FinishReason == nil || *out.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %#v, want stop", out.Choices[0].FinishReason)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 4 || out.Usage.CompletionTokens != 2 || out.Usage.TotalTokens != 6 {
		t.Fatalf("usage = %#v, want responses usage mapped to chat usage", out.Usage)
	}
	if strings.Contains(string(got), "data:") || strings.Contains(string(got), "response.output_text.delta") {
		t.Fatalf("buffered non-stream response leaked SSE/Responses details: %s", string(got))
	}
}

func TestMaybeTransformCodexChatCompletionsResponsesResponseDetectsSSEBodyWithoutEventStreamHeader(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			"event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","delta":"Headerless"}` + "\n\n" +
				"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n" +
				"data: [DONE]\n\n",
		)),
	}

	if err := maybeTransformCodexChatCompletionsResponsesResponse(responseAdapterCodexChatCompletionsFromResponsesBuffered, "gpt-5.5", resp); err != nil {
		t.Fatalf("transform response: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read buffered response: %v", err)
	}
	var out openAIChatCompletionResponse
	if err := jsonUnmarshalCodexChatBufferedResponse(got, &out); err != nil {
		t.Fatalf("decode buffered chat response: %v; body=%s", err, string(got))
	}
	if len(out.Choices) != 1 || out.Choices[0].Message == nil || out.Choices[0].Message.Content != "Headerless" {
		t.Fatalf("choices = %#v, want headerless SSE delta mapped to chat message", out.Choices)
	}
}

func jsonUnmarshalCodexChatBufferedResponse(raw []byte, out any) error {
	return json.Unmarshal(raw, out)
}
