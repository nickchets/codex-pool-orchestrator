package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const responseAdapterCodexChatCompletionsFromResponsesBuffered = "codex_chat_completions_from_responses_buffered"
const responseAdapterCodexChatCompletionsFromResponsesStream = "codex_chat_completions_from_responses_stream"

type codexChatCompletionResponsesRequest struct {
	Model    string              `json:"model"`
	Messages []openAIChatMessage `json:"messages"`
	Stream   bool                `json:"stream,omitempty"`
}

type codexResponsesChatSummary struct {
	Text         string
	Usage        *openAIChatCompletionUsage
	FinishReason string
}

type codexResponsesParsedSSEEvent struct {
	Done         bool
	Delta        string
	Usage        *openAIChatCompletionUsage
	FinishReason string
}

func maybeBuildCodexChatCompletionsResponsesRequest(reqPath, model string, body []byte) (string, []byte, string, bool, error) {
	if strings.TrimSpace(reqPath) != "/v1/chat/completions" {
		return "", nil, "", false, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", nil, "", false, fmt.Errorf("parse openai chat completions request: %w", err)
	}
	allowedFields := map[string]bool{
		"model":                 true,
		"messages":              true,
		"stream":                true,
		"max_tokens":            true,
		"max_completion_tokens": true,
		"max_output_tokens":     true,
	}
	for field, rawValue := range raw {
		if jsonRawIsNull(rawValue) {
			continue
		}
		if !allowedFields[field] {
			return "", nil, "", false, fmt.Errorf("unsupported chat/completions field for responses adapter: %s", field)
		}
	}

	var in codexChatCompletionResponsesRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return "", nil, "", false, fmt.Errorf("parse openai chat completions request: %w", err)
	}
	if strings.TrimSpace(in.Model) == "" {
		in.Model = strings.TrimSpace(model)
	}
	if strings.TrimSpace(in.Model) == "" {
		return "", nil, "", false, fmt.Errorf("chat/completions responses adapter requires model")
	}
	if len(in.Messages) == 0 {
		return "", nil, "", false, fmt.Errorf("chat/completions responses adapter requires messages")
	}

	instructions := make([]string, 0, 2)
	input := make([]map[string]any, 0, len(in.Messages))
	for _, msg := range in.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system", "developer", "user", "assistant":
		case "":
			return "", nil, "", false, fmt.Errorf("unsupported chat/completions message without role for responses adapter")
		default:
			return "", nil, "", false, fmt.Errorf("unsupported chat/completions message role for responses adapter: %s", role)
		}
		text, err := codexChatCompletionsTextContent(msg.Content)
		if err != nil {
			return "", nil, "", false, err
		}
		if role == "system" || role == "developer" {
			if strings.TrimSpace(text) != "" {
				instructions = append(instructions, text)
			}
			continue
		}
		input = append(input, map[string]any{
			"role": role,
			"content": []map[string]string{{
				"type": "input_text",
				"text": text,
			}},
		})
	}
	if len(input) == 0 {
		return "", nil, "", false, fmt.Errorf("chat/completions responses adapter requires at least one user or assistant message")
	}
	instructionText := strings.TrimSpace(strings.Join(instructions, "\n\n"))
	if instructionText == "" {
		instructionText = "You are a helpful assistant."
	}

	out := map[string]any{
		"model":        strings.TrimSpace(in.Model),
		"instructions": instructionText,
		"input":        input,
		"store":        false,
		"stream":       true,
	}
	rewritten, err := json.Marshal(out)
	if err != nil {
		return "", nil, "", false, err
	}

	adapter := responseAdapterCodexChatCompletionsFromResponsesBuffered
	if in.Stream {
		adapter = responseAdapterCodexChatCompletionsFromResponsesStream
	}
	return "/v1/responses", rewritten, adapter, in.Stream, nil
}

func jsonRawIsNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func codexChatCompletionsTextContent(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}

	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return text, nil
	}

	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return "", fmt.Errorf("unsupported chat/completions content shape for responses adapter")
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		partType := strings.TrimSpace(jsonRawString(part["type"]))
		switch partType {
		case "text", "input_text":
			partText := jsonRawString(part["text"])
			out = append(out, partText)
		case "":
			return "", fmt.Errorf("unsupported chat/completions content part without type for responses adapter")
		default:
			return "", fmt.Errorf("unsupported chat/completions content type for responses adapter: %s", partType)
		}
	}
	return strings.Join(out, "\n"), nil
}

func jsonRawString(raw json.RawMessage) string {
	var out string
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	if err := json.Unmarshal(raw, &out); err == nil {
		return out
	}
	return ""
}

func maybeTransformCodexChatCompletionsResponsesResponse(adapter, model string, resp *http.Response) error {
	if adapter != responseAdapterCodexChatCompletionsFromResponsesBuffered && adapter != responseAdapterCodexChatCompletionsFromResponsesStream {
		return nil
	}
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		if adapter == responseAdapterCodexChatCompletionsFromResponsesStream {
			src, err := decodeMaybeGzipResponseBody(resp)
			if err != nil {
				return err
			}
			replaceStreamingHTTPResponseBody(resp, src, func(dst io.Writer, stream io.ReadCloser) error {
				return transformCodexResponsesSSEToOpenAIChatCompletions(model, dst, stream)
			})
			resp.Header.Set("Content-Type", "text/event-stream")
			return nil
		}

		raw, err := readMaybeGzipResponseBody(resp)
		if err != nil {
			return err
		}
		summary, err := collectCodexResponsesChatSummaryFromSSE(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		out, err := buildCodexChatCompletionsBufferedResponse(model, summary)
		if err != nil {
			return err
		}
		replaceBufferedHTTPResponseBody(resp, "application/json", out)
		return nil
	}

	raw, err := readMaybeGzipResponseBody(resp)
	if err != nil {
		return err
	}
	if codexResponsesBodyLooksLikeSSE(raw) {
		summary, err := collectCodexResponsesChatSummaryFromSSE(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		if adapter == responseAdapterCodexChatCompletionsFromResponsesStream {
			out, err := buildCodexChatCompletionsStreamResponseFromSummary(model, summary)
			if err != nil {
				return err
			}
			replaceBufferedHTTPResponseBody(resp, "text/event-stream", out)
			return nil
		}
		out, err := buildCodexChatCompletionsBufferedResponse(model, summary)
		if err != nil {
			return err
		}
		replaceBufferedHTTPResponseBody(resp, "application/json", out)
		return nil
	}
	summary, err := buildCodexResponsesChatSummaryFromJSON(raw)
	if err != nil {
		return err
	}
	if adapter == responseAdapterCodexChatCompletionsFromResponsesStream {
		out, err := buildCodexChatCompletionsStreamResponseFromSummary(model, summary)
		if err != nil {
			return err
		}
		replaceBufferedHTTPResponseBody(resp, "text/event-stream", out)
		return nil
	}
	out, err := buildCodexChatCompletionsBufferedResponse(model, summary)
	if err != nil {
		return err
	}
	replaceBufferedHTTPResponseBody(resp, "application/json", out)
	return nil
}

func codexResponsesBodyLooksLikeSSE(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:"))
}

func collectCodexResponsesChatSummaryFromSSE(src io.Reader) (codexResponsesChatSummary, error) {
	summary := codexResponsesChatSummary{FinishReason: "stop"}
	var text strings.Builder
	if err := scanCodexResponsesSSE(src, func(event codexResponsesParsedSSEEvent) error {
		if event.Delta != "" {
			text.WriteString(event.Delta)
		}
		if event.Usage != nil {
			summary.Usage = event.Usage
		}
		if event.FinishReason != "" {
			summary.FinishReason = event.FinishReason
		}
		return nil
	}); err != nil {
		return codexResponsesChatSummary{}, err
	}
	summary.Text = text.String()
	return summary, nil
}

func transformCodexResponsesSSEToOpenAIChatCompletions(model string, dst io.Writer, src io.ReadCloser) error {
	defer src.Close()
	sentRole := false
	summary := codexResponsesChatSummary{FinishReason: "stop"}
	if err := scanCodexResponsesSSE(src, func(event codexResponsesParsedSSEEvent) error {
		if event.Delta != "" {
			if err := writeCodexChatCompletionsChunk(dst, model, event.Delta, nil, nil, &sentRole); err != nil {
				return err
			}
		}
		if event.Usage != nil {
			summary.Usage = event.Usage
		}
		if event.FinishReason != "" {
			summary.FinishReason = event.FinishReason
		}
		return nil
	}); err != nil {
		return err
	}
	finishReason := summary.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := writeCodexChatCompletionsChunk(dst, model, "", &finishReason, summary.Usage, &sentRole); err != nil {
		return err
	}
	_, err := dst.Write([]byte("data: [DONE]\n\n"))
	return err
}

func scanCodexResponsesSSE(src io.Reader, handle func(codexResponsesParsedSSEEvent) error) error {
	reader := bufio.NewReader(src)
	var eventName string
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		parsed, err := parseCodexResponsesSSEData(eventName, data)
		eventName = ""
		if err != nil {
			return err
		}
		if parsed.Done {
			return nil
		}
		return handle(parsed)
	}
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		}
		if err != nil {
			if err == io.EOF {
				return flush()
			}
			return err
		}
	}
}

func parseCodexResponsesSSEData(eventName, data string) (codexResponsesParsedSSEEvent, error) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return codexResponsesParsedSSEEvent{}, nil
	}
	if trimmed == "[DONE]" {
		return codexResponsesParsedSSEEvent{Done: true}, nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return codexResponsesParsedSSEEvent{}, err
	}
	eventType := firstStringField(obj, "type")
	if eventType == "" {
		eventType = strings.TrimSpace(eventName)
	}
	parsed := codexResponsesParsedSSEEvent{}
	switch eventType {
	case "response.output_text.delta":
		if delta, ok := obj["delta"].(string); ok {
			parsed.Delta = delta
		}
	case "response.completed":
		parsed.FinishReason = "stop"
		parsed.Usage = openAIChatCompletionUsageFromResponsesEvent(obj)
	default:
		parsed.Usage = openAIChatCompletionUsageFromResponsesEvent(obj)
	}
	return parsed, nil
}

func buildCodexChatCompletionsBufferedResponse(model string, summary codexResponsesChatSummary) ([]byte, error) {
	finishReason := summary.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	out := openAIChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-codex-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openAIChatCompletionChoice{{
			Index: 0,
			Message: &openAIChatCompletionMessage{
				Role:    "assistant",
				Content: summary.Text,
			},
			FinishReason: &finishReason,
		}},
		Usage: summary.Usage,
	}
	return json.Marshal(out)
}

func buildCodexChatCompletionsStreamResponseFromSummary(model string, summary codexResponsesChatSummary) ([]byte, error) {
	var buf bytes.Buffer
	sentRole := false
	if summary.Text != "" {
		if err := writeCodexChatCompletionsChunk(&buf, model, summary.Text, nil, nil, &sentRole); err != nil {
			return nil, err
		}
	}
	finishReason := summary.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := writeCodexChatCompletionsChunk(&buf, model, "", &finishReason, summary.Usage, &sentRole); err != nil {
		return nil, err
	}
	if _, err := buf.Write([]byte("data: [DONE]\n\n")); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCodexChatCompletionsChunk(dst io.Writer, model, content string, finishReason *string, usage *openAIChatCompletionUsage, sentRole *bool) error {
	payload, err := json.Marshal(openAIChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-codex-%d", time.Now().UnixNano()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openAIChatCompletionChoice{{
			Index: 0,
			Delta: &openAIChatCompletionMessage{
				Role:    firstRole(sentRole),
				Content: content,
			},
			FinishReason: finishReason,
		}},
		Usage: usage,
	})
	if err != nil {
		return err
	}
	_, err = dst.Write([]byte("data: " + string(payload) + "\n\n"))
	return err
}

func openAIChatCompletionUsageFromResponsesEvent(obj map[string]any) *openAIChatCompletionUsage {
	_, usageMap := usageMapFromOpenAIEnvelope(obj)
	if usageMap == nil {
		return nil
	}
	promptTokens := readFirstInt64(usageMap, "input_tokens", "prompt_tokens")
	completionTokens := readFirstInt64(usageMap, "output_tokens", "completion_tokens")
	totalTokens := readInt64(usageMap, "total_tokens")
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}
	if promptTokens == 0 && completionTokens == 0 && totalTokens == 0 {
		return nil
	}
	return &openAIChatCompletionUsage{
		PromptTokens:     int(promptTokens),
		CompletionTokens: int(completionTokens),
		TotalTokens:      int(totalTokens),
	}
}

func buildCodexResponsesChatSummaryFromJSON(raw []byte) (codexResponsesChatSummary, error) {
	var obj map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &obj); err != nil {
		return codexResponsesChatSummary{}, err
	}
	finishReason := "stop"
	if status := firstStringField(obj, "status"); strings.EqualFold(status, "incomplete") {
		finishReason = "length"
	}
	return codexResponsesChatSummary{
		Text:         extractCodexResponsesOutputText(obj),
		Usage:        openAIChatCompletionUsageFromResponsesEvent(obj),
		FinishReason: finishReason,
	}, nil
}

func extractCodexResponsesOutputText(obj map[string]any) string {
	if text := firstStringField(obj, "output_text"); text != "" {
		return text
	}
	items, ok := obj["output"].([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0)
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		contentItems, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, contentItem := range contentItems {
			contentObj, ok := contentItem.(map[string]any)
			if !ok {
				continue
			}
			if contentType := firstStringField(contentObj, "type"); contentType != "output_text" && contentType != "text" {
				continue
			}
			if text := firstStringField(contentObj, "text"); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "")
}
