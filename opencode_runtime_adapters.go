package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const responseAdapterOpenAIChatCompletionsGemini = "openai_chat_completions_gemini"
const responseAdapterAnthropicMessagesGemini = "anthropic_messages_gemini"
const responseAdapterAnthropicMessagesGeminiStream = "anthropic_messages_gemini_stream"
const opencodeAnthropicBetaHeader = "context-1m-2025-08-07"
const geminiAnthropicThinkingBudgetHigh = 24576
const geminiAnthropicThinkingOutputFloor = 32768
const geminiThoughtSignatureTTL = 30 * time.Minute

var geminiThoughtSignatureCache = struct {
	mu      sync.Mutex
	entries map[string]geminiThoughtSignatureEntry
}{
	entries: map[string]geminiThoughtSignatureEntry{},
}

type geminiThoughtSignatureEntry struct {
	Signature string
	SeenAt    time.Time
}

type modelRouteDecision struct {
	Provider        Provider
	TargetBase      *url.URL
	UpstreamPath    string
	RewrittenBody   []byte
	ResponseAdapter string
}

type anthropicMessagesRequest struct {
	Model       string               `json:"model"`
	Messages    []anthropicMessage   `json:"messages"`
	System      any                  `json:"system,omitempty"`
	Tools       []anthropicTool      `json:"tools,omitempty"`
	ToolChoice  *anthropicToolChoice `json:"tool_choice,omitempty"`
	Stream      bool                 `json:"stream,omitempty"`
	MaxTokens   int                  `json:"max_tokens,omitempty"`
	Temperature *float64             `json:"temperature,omitempty"`
	TopP        *float64             `json:"top_p,omitempty"`
	TopK        *int                 `json:"top_k,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicToolChoice struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type openAIChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIChatCompletionRequestCompat struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	System      string              `json:"system,omitempty"`
	Stream      bool                `json:"stream,omitempty"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Temperature *float64            `json:"temperature,omitempty"`
	TopP        *float64            `json:"top_p,omitempty"`
	TopK        *int                `json:"top_k,omitempty"`
}

type anthropicMessageResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role,omitempty"`
	Model        string                  `json:"model,omitempty"`
	Content      []anthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason,omitempty"`
	StopSequence any                     `json:"stop_sequence"`
	Usage        anthropicUsage          `json:"usage,omitempty"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

type openAIChatCompletionResponse struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"`
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []openAIChatCompletionChoice `json:"choices"`
	Usage   *openAIChatCompletionUsage   `json:"usage,omitempty"`
}

type openAIChatCompletionChoice struct {
	Index        int                          `json:"index"`
	Message      *openAIChatCompletionMessage `json:"message,omitempty"`
	Delta        *openAIChatCompletionMessage `json:"delta,omitempty"`
	FinishReason *string                      `json:"finish_reason"`
}

type openAIChatCompletionMessage struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type openAIChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type geminiGenerateContentResponse struct {
	Candidates    []geminiGenerateCandidate `json:"candidates"`
	UsageMetadata *geminiUsageMetadata      `json:"usageMetadata,omitempty"`
}

type geminiGenerateCandidate struct {
	Content      geminiGenerateContent `json:"content"`
	FinishReason string                `json:"finishReason,omitempty"`
}

type geminiGenerateContent struct {
	Parts []geminiGeneratedPart `json:"parts"`
}

type geminiGeneratedPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount      int `json:"totalTokenCount,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	ID   string          `json:"id,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string          `json:"name,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	ID       string          `json:"id,omitempty"`
}

type geminiGenerateContentEnvelope struct {
	Response json.RawMessage `json:"response"`
}

func maybeBuildOpenAIChatCompletionsGeminiRequest(reqPath, model string, body []byte) (string, []byte, string, bool, error) {
	if reqPath != "/v1/chat/completions" || !strings.HasPrefix(strings.TrimSpace(model), "gemini-") {
		return "", nil, "", false, nil
	}
	var in openAIChatCompletionRequestCompat
	if err := json.Unmarshal(body, &in); err != nil {
		return "", nil, "", false, fmt.Errorf("parse openai chat completions request: %w", err)
	}
	req := geminiAPIRequestPayload{GenerationConfig: buildGeminiGenerationConfigCompat(in.MaxTokens, in.Temperature, in.TopP, in.TopK)}
	contents, systemInstruction := buildGeminiContentsFromOpenAICompat(in.Messages, in.System)
	req.Contents = contents
	req.SystemInstruction = systemInstruction
	rewritten, err := json.Marshal(req)
	if err != nil {
		return "", nil, "", false, err
	}
	method := "generateContent"
	if in.Stream {
		method = "streamGenerateContent"
	}
	path := fmt.Sprintf("%s%s:%s", geminiAPIModelPrefix, rewriteGeminiCodeAssistFacadeModel(in.Model), method)
	return path, rewritten, responseAdapterOpenAIChatCompletionsGemini, in.Stream, nil
}

func maybeBuildAnthropicMessagesGeminiRequest(reqPath, model string, body []byte) (string, []byte, string, bool, error) {
	if reqPath != "/v1/messages" || !strings.HasPrefix(strings.TrimSpace(model), "gemini-") {
		return "", nil, "", false, nil
	}
	var in anthropicMessagesRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return "", nil, "", false, fmt.Errorf("parse anthropic messages request: %w", err)
	}
	rewrittenModel := rewriteGeminiCodeAssistFacadeModel(in.Model)
	req := geminiAPIRequestPayload{GenerationConfig: buildGeminiAnthropicGenerationConfigCompat(rewrittenModel, in.MaxTokens, in.Temperature, in.TopP, in.TopK)}
	contents, systemInstruction, tools, toolConfig := buildGeminiContentsFromAnthropic(in.Messages, in.System, in.Tools, in.ToolChoice)
	req.Contents = contents
	req.SystemInstruction = systemInstruction
	req.Tools = tools
	req.ToolConfig = toolConfig
	rewritten, err := json.Marshal(req)
	if err != nil {
		return "", nil, "", false, err
	}
	method := "generateContent"
	if shouldForceAnthropicGeminiStreamTransport(rewrittenModel) {
		method = "streamGenerateContent"
	}
	path := fmt.Sprintf("%s%s:%s", geminiAPIModelPrefix, rewrittenModel, method)
	adapter := responseAdapterAnthropicMessagesGemini
	if in.Stream {
		adapter = responseAdapterAnthropicMessagesGeminiStream
	}
	return path, rewritten, adapter, in.Stream, nil
}

func buildGeminiGenerationConfigCompat(maxTokens int, temperature, topP *float64, topK *int) json.RawMessage {
	cfg := map[string]any{}
	if maxTokens > 0 {
		cfg["maxOutputTokens"] = maxTokens
	}
	if temperature != nil {
		cfg["temperature"] = *temperature
	}
	if topP != nil {
		cfg["topP"] = *topP
	}
	if topK != nil {
		cfg["topK"] = *topK
	}
	if len(cfg) == 0 {
		return nil
	}
	raw, _ := json.Marshal(cfg)
	return raw
}

func shouldForceAnthropicGeminiStreamTransport(model string) bool {
	return strings.TrimSpace(model) == "gemini-3.1-pro-high"
}

func defaultAnthropicGeminiThinkingBudget(model string) int {
	if strings.TrimSpace(model) == "gemini-3.1-pro-high" {
		return geminiAnthropicThinkingBudgetHigh
	}
	return 0
}

func buildGeminiAnthropicGenerationConfigCompat(model string, maxTokens int, temperature, topP *float64, topK *int) json.RawMessage {
	cfg := map[string]any{}
	if budget := defaultAnthropicGeminiThinkingBudget(model); budget > 0 {
		cfg["thinkingConfig"] = map[string]any{
			"thinkingBudget": budget,
		}
		if maxTokens < geminiAnthropicThinkingOutputFloor {
			maxTokens = geminiAnthropicThinkingOutputFloor
		}
	}
	if maxTokens > 0 {
		cfg["maxOutputTokens"] = maxTokens
	}
	if temperature != nil {
		cfg["temperature"] = *temperature
	}
	if topP != nil {
		cfg["topP"] = *topP
	}
	if topK != nil {
		cfg["topK"] = *topK
	}
	if len(cfg) == 0 {
		return nil
	}
	raw, _ := json.Marshal(cfg)
	return raw
}

func buildGeminiContentsFromOpenAICompat(messages []openAIChatMessage, system string) (json.RawMessage, json.RawMessage) {
	if strings.TrimSpace(system) == "" {
		systemParts := make([]string, 0)
		for _, msg := range messages {
			if strings.EqualFold(strings.TrimSpace(msg.Role), "system") {
				if text := strings.TrimSpace(flattenOpenAICompatContent(msg.Content)); text != "" {
					systemParts = append(systemParts, text)
				}
			}
		}
		system = strings.Join(systemParts, "\n\n")
	}
	if strings.TrimSpace(system) != "" {
		sys, _ := json.Marshal(map[string]any{"parts": []map[string]any{{"text": system}}})
		return buildGeminiContents(messages), sys
	}
	return buildGeminiContents(messages), nil
}

func buildGeminiContents(messages []openAIChatMessage) json.RawMessage {
	contents := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role := "user"
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			role = "model"
		case "system":
			continue
		}
		text := flattenOpenAICompatContent(msg.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": []map[string]any{{"text": text}},
		})
	}
	raw, _ := json.Marshal(contents)
	return raw
}

func buildGeminiContentsFromAnthropic(messages []anthropicMessage, system any, tools []anthropicTool, toolChoice *anthropicToolChoice) (json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage) {
	contents := make([]map[string]any, 0, len(messages))
	toolNameByID := make(map[string]string)
	for _, msg := range messages {
		role := "user"
		if strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			role = "model"
		}
		parts := buildGeminiAnthropicParts(msg.Content, role, toolNameByID)
		if len(parts) == 0 {
			continue
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": parts,
		})
	}
	rawContents, _ := json.Marshal(contents)
	systemText := flattenAnthropicCompatContent(system)
	rawTools := buildGeminiAnthropicToolsRaw(tools)
	rawToolConfig := buildGeminiAnthropicToolConfigRaw(toolChoice, len(tools) > 0)
	if strings.TrimSpace(systemText) == "" {
		return rawContents, nil, rawTools, rawToolConfig
	}
	rawSystem, _ := json.Marshal(map[string]any{"parts": []map[string]any{{"text": systemText}}})
	return rawContents, rawSystem, rawTools, rawToolConfig
}

func flattenOpenAICompatContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err == nil {
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if fmt.Sprint(part["type"]) == "text" {
				if value, ok := part["text"].(string); ok {
					out = append(out, value)
				}
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

func flattenAnthropicCompatContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if part, ok := item.(map[string]any); ok && fmt.Sprint(part["type"]) == "text" {
				if value, ok := part["text"].(string); ok {
					out = append(out, value)
				}
			}
		}
		return strings.Join(out, "\n")
	default:
		return ""
	}
}

func buildGeminiAnthropicParts(raw any, role string, toolNameByID map[string]string) []map[string]any {
	switch v := raw.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil
		}
		return []map[string]any{{"text": v}}
	case []any:
		parts := make([]map[string]any, 0, len(v))
		for _, item := range v {
			switch typed := item.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					parts = append(parts, map[string]any{"text": typed})
				}
			case map[string]any:
				switch strings.TrimSpace(fmt.Sprint(typed["type"])) {
				case "text":
					if text := strings.TrimSpace(compatString(typed["text"])); text != "" {
						parts = append(parts, map[string]any{"text": typed["text"]})
					}
				case "tool_use":
					if role != "model" {
						continue
					}
					name := strings.TrimSpace(compatString(typed["name"]))
					if name == "" {
						continue
					}
					call := map[string]any{
						"name": name,
						"args": normalizeGeminiFunctionArgs(typed["input"]),
					}
					if id := strings.TrimSpace(compatString(typed["id"])); id != "" {
						call["id"] = id
						toolNameByID[id] = name
					}
					part := map[string]any{"functionCall": call}
					if id := strings.TrimSpace(compatString(typed["id"])); id != "" {
						if signature := lookupGeminiThoughtSignature(id); signature != "" {
							part["thoughtSignature"] = signature
						}
					}
					parts = append(parts, part)
				case "tool_result":
					if role != "user" {
						continue
					}
					toolUseID := strings.TrimSpace(compatString(typed["tool_use_id"]))
					name := strings.TrimSpace(compatString(typed["name"]))
					if name == "" && toolUseID != "" {
						name = toolNameByID[toolUseID]
					}
					if name == "" {
						name = toolUseID
					}
					if name == "" {
						continue
					}
					resp := map[string]any{
						"name": name,
						"response": map[string]any{
							"result": flattenAnthropicToolResultContent(typed["content"], typed["is_error"] == true),
						},
					}
					if toolUseID != "" {
						resp["id"] = toolUseID
					}
					parts = append(parts, map[string]any{"functionResponse": resp})
				}
			}
		}
		return parts
	default:
		return nil
	}
}

func normalizeGeminiFunctionArgs(raw any) any {
	if raw == nil {
		return map[string]any{}
	}
	return raw
}

func flattenAnthropicToolResultContent(raw any, isError bool) any {
	switch v := raw.(type) {
	case nil:
		if isError {
			return "Tool execution failed."
		}
		return "Tool executed successfully."
	case string:
		if strings.TrimSpace(v) != "" {
			return v
		}
	case []any:
		if text := flattenAnthropicCompatContent(v); strings.TrimSpace(text) != "" {
			return text
		}
		if len(v) > 0 {
			return v
		}
	default:
		return v
	}
	if isError {
		return "Tool execution failed."
	}
	return "Tool executed successfully."
}

func compatString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func buildGeminiAnthropicToolsRaw(tools []anthropicTool) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		decl := map[string]any{
			"name":       name,
			"parameters": convertAnthropicJSONSchemaToGeminiSchema(tool.InputSchema),
		}
		if description := strings.TrimSpace(tool.Description); description != "" {
			decl["description"] = description
		}
		declarations = append(declarations, decl)
	}
	if len(declarations) == 0 {
		return nil
	}
	raw, _ := json.Marshal([]map[string]any{{"functionDeclarations": declarations}})
	return raw
}

func buildGeminiAnthropicToolConfigRaw(choice *anthropicToolChoice, hasTools bool) json.RawMessage {
	if !hasTools {
		return nil
	}
	mode := "AUTO"
	cfg := map[string]any{}
	if choice != nil {
		switch strings.ToLower(strings.TrimSpace(choice.Type)) {
		case "any":
			mode = "ANY"
		case "tool":
			mode = "ANY"
			if name := strings.TrimSpace(choice.Name); name != "" {
				cfg["allowedFunctionNames"] = []string{name}
			}
		case "none":
			mode = "NONE"
		}
	}
	cfg["mode"] = mode
	raw, _ := json.Marshal(map[string]any{
		"functionCallingConfig": cfg,
	})
	return raw
}

func convertAnthropicJSONSchemaToGeminiSchema(raw json.RawMessage) map[string]any {
	var value any
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &value) != nil {
		return defaultGeminiFunctionSchema()
	}
	schema, ok := normalizeGeminiFunctionSchema(value).(map[string]any)
	if !ok || len(schema) == 0 {
		return defaultGeminiFunctionSchema()
	}
	if _, ok := schema["type"]; !ok {
		if _, hasProps := schema["properties"]; hasProps {
			schema["type"] = "OBJECT"
		}
	}
	if strings.TrimSpace(fmt.Sprint(schema["type"])) == "" {
		schema["type"] = "OBJECT"
	}
	return schema
}

func defaultGeminiFunctionSchema() map[string]any {
	return map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "STRING",
				"description": "The raw tool input.",
			},
		},
		"required": []string{"content"},
	}
}

func normalizeGeminiFunctionSchema(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		nullable := false
		for key, item := range v {
			switch key {
			case "$schema", "$defs", "$ref", "definitions", "additionalProperties", "default", "examples", "example", "title", "allOf", "anyOf", "oneOf", "not", "const", "patternProperties", "unevaluatedProperties":
				continue
			case "type":
				mapped, isNullable := geminiSchemaType(item)
				if mapped != "" {
					out["type"] = mapped
				}
				nullable = nullable || isNullable
			case "properties":
				props, ok := item.(map[string]any)
				if !ok {
					continue
				}
				normalizedProps := map[string]any{}
				for name, prop := range props {
					if converted := normalizeGeminiFunctionSchema(prop); converted != nil {
						normalizedProps[name] = converted
					}
				}
				if len(normalizedProps) > 0 {
					out["properties"] = normalizedProps
				}
			case "items":
				if converted := normalizeGeminiFunctionSchema(item); converted != nil {
					out["items"] = converted
				}
			case "required":
				if required := normalizeStringSliceFromAny(item); len(required) > 0 {
					out["required"] = required
				}
			case "description", "enum", "format", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "pattern", "nullable":
				out[key] = item
			}
		}
		if nullable {
			out["nullable"] = true
		}
		if _, ok := out["type"]; !ok {
			if _, hasProps := out["properties"]; hasProps {
				out["type"] = "OBJECT"
			} else if _, hasItems := out["items"]; hasItems {
				out["type"] = "ARRAY"
			}
		}
		return out
	default:
		return value
	}
}

func geminiSchemaType(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return geminiSchemaTypeName(typed), false
	case []any:
		nullable := false
		for _, item := range typed {
			name := strings.ToLower(strings.TrimSpace(fmt.Sprint(item)))
			if name == "null" {
				nullable = true
				continue
			}
			if mapped := geminiSchemaTypeName(name); mapped != "" {
				return mapped, nullable
			}
		}
		return "", nullable
	default:
		return "", false
	}
}

func geminiSchemaTypeName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "object":
		return "OBJECT"
	case "array":
		return "ARRAY"
	case "string":
		return "STRING"
	case "number":
		return "NUMBER"
	case "integer":
		return "INTEGER"
	case "boolean":
		return "BOOLEAN"
	default:
		return ""
	}
}

func replaceBufferedHTTPResponseBody(resp *http.Response, contentType string, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp.Header.Set("Content-Type", contentType)
}

func replaceStreamingHTTPResponseBody(resp *http.Response, src io.ReadCloser, transform func(io.Writer, io.ReadCloser) error) {
	pr, pw := io.Pipe()
	go func() {
		err := transform(pw, src)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	resp.Body = pr
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
}

func maybeTransformOpenAIChatCompletionsGeminiResponse(adapter, model string, resp *http.Response) error {
	if adapter != responseAdapterOpenAIChatCompletionsGemini || resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		src, err := decodeMaybeGzipResponseBody(resp)
		if err != nil {
			return err
		}
		replaceStreamingHTTPResponseBody(resp, src, func(dst io.Writer, stream io.ReadCloser) error {
			return transformGeminiSSEToOpenAIChatCompletions(model, dst, stream)
		})
		return nil
	}
	raw, err := readMaybeGzipResponseBody(resp)
	if err != nil {
		return err
	}
	if rawGeminiResponseLooksArray(raw) {
		out, err := buildOpenAIChatCompletionsStreamResponse(model, raw)
		if err != nil {
			return err
		}
		replaceBufferedHTTPResponseBody(resp, "text/event-stream", out)
		return nil
	}
	out, err := buildOpenAIChatCompletionsResponse(model, raw)
	if err != nil {
		return err
	}
	replaceBufferedHTTPResponseBody(resp, "application/json", out)
	return nil
}

func maybeTransformAnthropicMessagesGeminiResponse(adapter, model string, resp *http.Response) error {
	if adapter != responseAdapterAnthropicMessagesGemini && adapter != responseAdapterAnthropicMessagesGeminiStream || resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		src, err := decodeMaybeGzipResponseBody(resp)
		if err != nil {
			return err
		}
		if adapter == responseAdapterAnthropicMessagesGemini {
			responses, err := collectGeminiResponsesFromSSE(src)
			if err != nil {
				return err
			}
			out, err := buildAnthropicMessagesResponseFromResponses(model, responses)
			if err != nil {
				return err
			}
			replaceBufferedHTTPResponseBody(resp, "application/json", out)
			return nil
		}
		replaceStreamingHTTPResponseBody(resp, src, func(dst io.Writer, stream io.ReadCloser) error {
			return transformGeminiSSEToAnthropicMessages(model, dst, stream)
		})
		return nil
	}
	raw, err := readMaybeGzipResponseBody(resp)
	if err != nil {
		return err
	}
	if adapter == responseAdapterAnthropicMessagesGeminiStream {
		out, err := buildAnthropicMessagesStreamResponse(model, raw)
		if err != nil {
			return err
		}
		replaceBufferedHTTPResponseBody(resp, "text/event-stream", out)
		return nil
	}
	out, err := buildAnthropicMessagesResponse(model, raw)
	if err != nil {
		return err
	}
	replaceBufferedHTTPResponseBody(resp, "application/json", out)
	return nil
}

func buildAnthropicMessagesResponseFromResponses(model string, responses []geminiGenerateContentResponse) ([]byte, error) {
	if len(responses) == 0 {
		return nil, fmt.Errorf("empty gemini response stream")
	}
	content := make([]anthropicContentBlock, 0)
	var usage *geminiUsageMetadata
	for _, response := range responses {
		content = append(content, buildAnthropicContentBlocksFromGemini(response.Candidates)...)
		if response.UsageMetadata != nil {
			usage = response.UsageMetadata
		}
	}
	out := anthropicMessageResponse{
		ID:           fmt.Sprintf("msg_gemini_%d", time.Now().UnixNano()),
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      content,
		StopReason:   anthropicStopReasonForResponses(responses),
		StopSequence: nil,
		Usage: anthropicUsage{
			InputTokens:  0,
			OutputTokens: 0,
		},
	}
	if usage != nil {
		out.Usage.InputTokens = usage.PromptTokenCount
		out.Usage.OutputTokens = usage.CandidatesTokenCount
	}
	return json.Marshal(out)
}

func buildOpenAIChatCompletionsResponse(model string, raw []byte) ([]byte, error) {
	responses, err := parseGeminiGenerateContentResponsesCompat(raw)
	if err != nil {
		return nil, err
	}
	in := responses[len(responses)-1]
	finishReason := mapGeminiFinishReasonOpenAI(firstGeminiFinishReason(in.Candidates))
	out := openAIChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openAIChatCompletionChoice{{
			Index: 0,
			Message: &openAIChatCompletionMessage{
				Role:    "assistant",
				Content: geminiCandidateText(in.Candidates),
			},
			FinishReason: finishReason,
		}},
	}
	if in.UsageMetadata != nil {
		out.Usage = &openAIChatCompletionUsage{
			PromptTokens:     in.UsageMetadata.PromptTokenCount,
			CompletionTokens: in.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      in.UsageMetadata.TotalTokenCount,
		}
	}
	return json.Marshal(out)
}

func buildAnthropicMessagesResponse(model string, raw []byte) ([]byte, error) {
	responses, err := parseGeminiGenerateContentResponsesCompat(raw)
	if err != nil {
		return nil, err
	}
	return buildAnthropicMessagesResponseFromResponses(model, responses)
}

func buildAnthropicMessagesStreamResponse(model string, raw []byte) ([]byte, error) {
	responses, err := parseGeminiGenerateContentResponsesCompat(raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeAnthropicMessagesStream(&buf, model, responses); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildOpenAIChatCompletionsStreamResponse(model string, raw []byte) ([]byte, error) {
	responses, err := parseGeminiGenerateContentResponsesCompat(raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	sentRole := false
	for _, in := range responses {
		text := geminiCandidateText(in.Candidates)
		if text != "" {
			payload, err := json.Marshal(openAIChatCompletionResponse{
				ID:      fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano()),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []openAIChatCompletionChoice{{
					Index: 0,
					Delta: &openAIChatCompletionMessage{
						Role:    firstRole(&sentRole),
						Content: text,
					},
					FinishReason: nil,
				}},
			})
			if err != nil {
				return nil, err
			}
			if _, err := buf.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
				return nil, err
			}
		}
	}
	last := responses[len(responses)-1]
	payload, err := json.Marshal(openAIChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openAIChatCompletionChoice{{
			Index:        0,
			Delta:        &openAIChatCompletionMessage{},
			FinishReason: mapGeminiFinishReasonOpenAI(firstGeminiFinishReason(last.Candidates)),
		}},
		Usage: usageFromGeminiMetadata(last.UsageMetadata),
	})
	if err != nil {
		return nil, err
	}
	if _, err := buf.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
		return nil, err
	}
	if _, err := buf.Write([]byte("data: [DONE]\n\n")); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func transformGeminiSSEToOpenAIChatCompletions(model string, dst io.Writer, src io.ReadCloser) error {
	defer src.Close()
	reader := bufio.NewReader(src)
	var dataLines []string
	sentRole := false
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		chunk := strings.Join(dataLines, "\n")
		dataLines = nil
		var in geminiGenerateContentResponse
		if err := json.Unmarshal([]byte(chunk), &in); err != nil {
			return err
		}
		payload, err := json.Marshal(openAIChatCompletionResponse{
			ID:      fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano()),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []openAIChatCompletionChoice{{
				Index: 0,
				Delta: &openAIChatCompletionMessage{
					Role:    firstRole(&sentRole),
					Content: geminiCandidateText(in.Candidates),
				},
				FinishReason: mapGeminiFinishReasonOpenAI(firstGeminiFinishReason(in.Candidates)),
			}},
		})
		if err != nil {
			return err
		}
		_, err = dst.Write([]byte("data: " + string(payload) + "\n\n"))
		return err
	}
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimSpace(line[len("data: "):]))
		} else if line == "" {
			if err := flush(); err != nil {
				return err
			}
		}
		if err != nil {
			if err == io.EOF {
				if err := flush(); err != nil {
					return err
				}
				_, err = dst.Write([]byte("data: [DONE]\n\n"))
				return err
			}
			return err
		}
	}
}

func transformGeminiSSEToAnthropicMessages(model string, dst io.Writer, src io.ReadCloser) error {
	defer src.Close()
	responses, err := collectGeminiResponsesFromSSE(src)
	if err != nil {
		return err
	}
	return writeAnthropicMessagesStream(dst, model, responses)
}

func geminiCandidateText(candidates []geminiGenerateCandidate) string {
	out := make([]string, 0)
	for _, candidate := range candidates {
		for _, part := range candidate.Content.Parts {
			if strings.TrimSpace(part.Text) != "" {
				out = append(out, part.Text)
			}
		}
	}
	return strings.Join(out, "")
}

func firstGeminiFinishReason(candidates []geminiGenerateCandidate) string {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.FinishReason) != "" {
			return candidate.FinishReason
		}
	}
	return ""
}

func buildAnthropicContentBlocksFromGemini(candidates []geminiGenerateCandidate) []anthropicContentBlock {
	blocks := make([]anthropicContentBlock, 0)
	toolSeq := 0
	for _, candidate := range candidates {
		for _, part := range candidate.Content.Parts {
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: part.Text})
				continue
			}
			if part.FunctionCall != nil {
				toolSeq++
				callID := geminiFunctionCallID(part.FunctionCall, toolSeq)
				rememberGeminiThoughtSignature(callID, part.ThoughtSignature)
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    callID,
					Name:  strings.TrimSpace(part.FunctionCall.Name),
					Input: normalizeGeminiFunctionArgsRaw(part.FunctionCall.Args),
				})
			}
		}
	}
	return blocks
}

func anthropicStopReasonForGemini(candidates []geminiGenerateCandidate) string {
	if geminiCandidatesHaveFunctionCall(candidates) {
		return "tool_use"
	}
	return mapGeminiFinishReasonAnthropic(firstGeminiFinishReason(candidates))
}

func anthropicStopReasonForResponses(responses []geminiGenerateContentResponse) string {
	for _, response := range responses {
		if geminiCandidatesHaveFunctionCall(response.Candidates) {
			return "tool_use"
		}
	}
	if len(responses) == 0 {
		return "end_turn"
	}
	return anthropicStopReasonForGemini(responses[len(responses)-1].Candidates)
}

func geminiCandidatesHaveFunctionCall(candidates []geminiGenerateCandidate) bool {
	for _, candidate := range candidates {
		for _, part := range candidate.Content.Parts {
			if part.FunctionCall != nil {
				return true
			}
		}
	}
	return false
}

func geminiFunctionCallID(call *geminiFunctionCall, seq int) string {
	if call != nil && strings.TrimSpace(call.ID) != "" {
		return strings.TrimSpace(call.ID)
	}
	return fmt.Sprintf("toolu_gemini_%d_%d", time.Now().UnixNano(), seq)
}

func normalizeGeminiFunctionArgsRaw(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}

func rememberGeminiThoughtSignature(toolUseID, signature string) {
	toolUseID = strings.TrimSpace(toolUseID)
	signature = strings.TrimSpace(signature)
	if toolUseID == "" || signature == "" {
		return
	}
	now := time.Now()
	geminiThoughtSignatureCache.mu.Lock()
	geminiThoughtSignatureCache.entries[toolUseID] = geminiThoughtSignatureEntry{
		Signature: signature,
		SeenAt:    now,
	}
	pruneGeminiThoughtSignatureCacheLocked(now)
	geminiThoughtSignatureCache.mu.Unlock()
}

func lookupGeminiThoughtSignature(toolUseID string) string {
	toolUseID = strings.TrimSpace(toolUseID)
	if toolUseID == "" {
		return ""
	}
	now := time.Now()
	geminiThoughtSignatureCache.mu.Lock()
	defer geminiThoughtSignatureCache.mu.Unlock()
	entry, ok := geminiThoughtSignatureCache.entries[toolUseID]
	if !ok {
		return ""
	}
	if now.Sub(entry.SeenAt) > geminiThoughtSignatureTTL {
		delete(geminiThoughtSignatureCache.entries, toolUseID)
		return ""
	}
	return entry.Signature
}

func clearGeminiThoughtSignatureCache() {
	geminiThoughtSignatureCache.mu.Lock()
	clear(geminiThoughtSignatureCache.entries)
	geminiThoughtSignatureCache.mu.Unlock()
}

func pruneGeminiThoughtSignatureCacheLocked(now time.Time) {
	for toolUseID, entry := range geminiThoughtSignatureCache.entries {
		if now.Sub(entry.SeenAt) > geminiThoughtSignatureTTL {
			delete(geminiThoughtSignatureCache.entries, toolUseID)
		}
	}
}

func anthropicStreamUsage(meta *geminiUsageMetadata) map[string]int {
	usage := map[string]int{
		"input_tokens":  0,
		"output_tokens": 0,
	}
	if meta == nil {
		return usage
	}
	usage["input_tokens"] = meta.PromptTokenCount
	usage["output_tokens"] = meta.CandidatesTokenCount
	return usage
}

func writeAnthropicMessagesStream(dst io.Writer, model string, responses []geminiGenerateContentResponse) error {
	if len(responses) == 0 {
		return fmt.Errorf("empty gemini response stream")
	}
	writeEvent := func(event string, payload any) error {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(dst, "event: "+event+"\n"); err != nil {
			return err
		}
		if _, err := io.WriteString(dst, "data: "); err != nil {
			return err
		}
		if _, err := dst.Write(body); err != nil {
			return err
		}
		_, err = io.WriteString(dst, "\n\n")
		return err
	}

	messageID := fmt.Sprintf("msg_gemini_%d", time.Now().UnixNano())
	if err := writeEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []anthropicContentBlock{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         anthropicStreamUsage(nil),
		},
	}); err != nil {
		return err
	}

	blockIndex := 0
	toolSeq := 0
	for _, response := range responses {
		for _, candidate := range response.Candidates {
			for _, part := range candidate.Content.Parts {
				switch {
				case strings.TrimSpace(part.Text) != "":
					idx := blockIndex
					blockIndex++
					if err := writeEvent("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]any{
							"type": "text",
							"text": "",
						},
					}); err != nil {
						return err
					}
					if err := writeEvent("content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": idx,
						"delta": map[string]any{
							"type": "text_delta",
							"text": part.Text,
						},
					}); err != nil {
						return err
					}
					if err := writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx}); err != nil {
						return err
					}
				case part.FunctionCall != nil:
					toolSeq++
					idx := blockIndex
					blockIndex++
					callID := geminiFunctionCallID(part.FunctionCall, toolSeq)
					rememberGeminiThoughtSignature(callID, part.ThoughtSignature)
					if err := writeEvent("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]any{
							"type":  "tool_use",
							"id":    callID,
							"name":  strings.TrimSpace(part.FunctionCall.Name),
							"input": map[string]any{},
						},
					}); err != nil {
						return err
					}
					args := normalizeGeminiFunctionArgsRaw(part.FunctionCall.Args)
					if len(args) > 2 {
						if err := writeEvent("content_block_delta", map[string]any{
							"type":  "content_block_delta",
							"index": idx,
							"delta": map[string]any{
								"type":         "input_json_delta",
								"partial_json": string(args),
							},
						}); err != nil {
							return err
						}
					}
					if err := writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx}); err != nil {
						return err
					}
				}
			}
		}
	}

	last := responses[len(responses)-1]
	if err := writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   anthropicStopReasonForResponses(responses),
			"stop_sequence": nil,
		},
		"usage": anthropicStreamUsage(last.UsageMetadata),
	}); err != nil {
		return err
	}
	return writeEvent("message_stop", map[string]any{"type": "message_stop"})
}

func collectGeminiResponsesFromSSE(src io.Reader) ([]geminiGenerateContentResponse, error) {
	reader := bufio.NewReader(src)
	var dataLines []string
	responses := make([]geminiGenerateContentResponse, 0)
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		chunk := strings.Join(dataLines, "\n")
		dataLines = nil
		var in geminiGenerateContentResponse
		if err := json.Unmarshal([]byte(chunk), &in); err != nil {
			return err
		}
		responses = append(responses, in)
		return nil
	}
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimSpace(line[len("data: "):]))
		} else if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		if err != nil {
			if err == io.EOF {
				if err := flush(); err != nil {
					return nil, err
				}
				return responses, nil
			}
			return nil, err
		}
	}
}

func maybeApplyOpencodeGeminiAnthropicHeaders(headers http.Header, responseAdapter string) {
	if headers == nil {
		return
	}
	if responseAdapter != responseAdapterAnthropicMessagesGemini && responseAdapter != responseAdapterAnthropicMessagesGeminiStream {
		return
	}
	if !strings.Contains(strings.ToLower(headers.Get("User-Agent")), "opencode") {
		return
	}
	if strings.TrimSpace(headers.Get("anthropic-beta")) == "" {
		headers.Set("anthropic-beta", opencodeAnthropicBetaHeader)
	}
}

func mapGeminiFinishReasonOpenAI(reason string) *string {
	switch strings.TrimSpace(reason) {
	case "":
		return nil
	case "STOP":
		value := "stop"
		return &value
	case "MAX_TOKENS":
		value := "length"
		return &value
	default:
		value := strings.ToLower(strings.TrimSpace(reason))
		return &value
	}
}

func mapGeminiFinishReasonAnthropic(reason string) string {
	switch strings.TrimSpace(reason) {
	case "", "STOP":
		return "end_turn"
	case "MAX_TOKENS":
		return "max_tokens"
	default:
		return strings.ToLower(strings.TrimSpace(reason))
	}
}

func decodeMaybeGzipResponseBody(resp *http.Response) (io.ReadCloser, error) {
	if resp == nil || resp.Body == nil {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		return resp.Body, nil
	}
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	resp.Header.Del("Content-Encoding")
	return struct {
		io.Reader
		io.Closer
	}{Reader: gr, Closer: multiCloser{gr, resp.Body}}, nil
}

func readMaybeGzipResponseBody(resp *http.Response) ([]byte, error) {
	body, err := decodeMaybeGzipResponseBody(resp)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var firstErr error
	for _, closer := range m {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func firstRole(sent *bool) string {
	if sent == nil || *sent {
		return ""
	}
	*sent = true
	return "assistant"
}

func rawGeminiResponseLooksArray(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}

func parseGeminiGenerateContentResponsesCompat(raw []byte) ([]geminiGenerateContentResponse, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty gemini response")
	}

	var direct geminiGenerateContentResponse
	if err := json.Unmarshal(trimmed, &direct); err == nil && (len(direct.Candidates) > 0 || direct.UsageMetadata != nil) {
		return []geminiGenerateContentResponse{direct}, nil
	}

	var envelope geminiGenerateContentEnvelope
	if err := json.Unmarshal(trimmed, &envelope); err == nil && len(bytes.TrimSpace(envelope.Response)) > 0 {
		return parseGeminiGenerateContentResponsesCompat(envelope.Response)
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawItems); err == nil && len(rawItems) > 0 {
		out := make([]geminiGenerateContentResponse, 0, len(rawItems))
		for _, item := range rawItems {
			responses, err := parseGeminiGenerateContentResponsesCompat(item)
			if err != nil {
				return nil, err
			}
			out = append(out, responses...)
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	return nil, fmt.Errorf("parse gemini response: unsupported payload shape")
}

func usageFromGeminiMetadata(meta *geminiUsageMetadata) *openAIChatCompletionUsage {
	if meta == nil {
		return nil
	}
	return &openAIChatCompletionUsage{
		PromptTokens:     meta.PromptTokenCount,
		CompletionTokens: meta.CandidatesTokenCount,
		TotalTokens:      meta.TotalTokenCount,
	}
}
