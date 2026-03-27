package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const geminiAPIModelPrefix = "/v1beta/models/"

type geminiAPIRequestPayload struct {
	Contents          json.RawMessage `json:"contents,omitempty"`
	SystemInstruction json.RawMessage `json:"systemInstruction,omitempty"`
	CachedContent     json.RawMessage `json:"cachedContent,omitempty"`
	Tools             json.RawMessage `json:"tools,omitempty"`
	ToolConfig        json.RawMessage `json:"toolConfig,omitempty"`
	Labels            json.RawMessage `json:"labels,omitempty"`
	SafetySettings    json.RawMessage `json:"safetySettings,omitempty"`
	GenerationConfig  json.RawMessage `json:"generationConfig,omitempty"`
}

type geminiCodeAssistFacadeRequest struct {
	targetBase  *url.URL
	targetBases []*url.URL
	path        string
	body        []byte
}

type geminiCodeAssistRequestPayload struct {
	Model        string                              `json:"model"`
	Project      string                              `json:"project,omitempty"`
	UserPromptID string                              `json:"user_prompt_id,omitempty"`
	Request      geminiCodeAssistInnerRequestPayload `json:"request"`
}

type geminiCodeAssistInnerRequestPayload struct {
	Contents          json.RawMessage `json:"contents,omitempty"`
	SystemInstruction json.RawMessage `json:"systemInstruction,omitempty"`
	CachedContent     json.RawMessage `json:"cachedContent,omitempty"`
	Tools             json.RawMessage `json:"tools,omitempty"`
	ToolConfig        json.RawMessage `json:"toolConfig,omitempty"`
	Labels            json.RawMessage `json:"labels,omitempty"`
	SafetySettings    json.RawMessage `json:"safetySettings,omitempty"`
	GenerationConfig  json.RawMessage `json:"generationConfig,omitempty"`
	SessionID         string          `json:"session_id,omitempty"`
}

type geminiCodeAssistResponseEnvelope struct {
	Response json.RawMessage `json:"response"`
	TraceID  string          `json:"traceId"`
}

func parseGeminiAPIPath(path string) (model string, method string, ok bool) {
	if !strings.HasPrefix(path, geminiAPIModelPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, geminiAPIModelPrefix)
	idx := strings.LastIndex(rest, ":")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	model = rest[:idx]
	method = rest[idx+1:]
	switch method {
	case "generateContent", "streamGenerateContent":
		return model, method, true
	default:
		return "", "", false
	}
}

func rewriteGeminiCodeAssistFacadeModel(model string) string {
	switch strings.TrimSpace(model) {
	case "gemini-3.1-pro", "gemini-3.1-pro-preview":
		return "gemini-3.1-pro-high"
	case "gemini-3-pro-preview":
		return "gemini-3.1-pro-high"
	default:
		return strings.TrimSpace(model)
	}
}

func shouldUseAntigravityGeminiCodeAssistBaseFallback(model string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), "gemini-3.1-pro")
}

func maybeBuildGeminiCodeAssistFacadeRequest(ctx context.Context, provider Provider, reqPath string, bodyBytes []byte, acc *Account, reqID string) (*geminiCodeAssistFacadeRequest, error) {
	geminiProvider, ok := provider.(*GeminiProvider)
	if !ok {
		return nil, nil
	}
	trace := requestTraceFromContext(ctx)
	model, method, ok := parseGeminiAPIPath(reqPath)
	if !ok {
		return nil, nil
	}
	if acc == nil {
		err := fmt.Errorf("gemini v1beta facade requires an account")
		trace.noteFacadeTransform(AccountTypeGemini, nil, reqPath, "", model, "", "", "error", err)
		return nil, err
	}
	projectID := effectiveGeminiCodeAssistProjectID(acc)
	if projectID == "" {
		err := fmt.Errorf("gemini account %s missing antigravity project id for v1beta facade", acc.ID)
		trace.noteFacadeTransform(AccountTypeGemini, acc, reqPath, "", model, "", projectID, "error", err)
		return nil, err
	}
	if len(bodyBytes) == 0 {
		err := fmt.Errorf("gemini v1beta facade requires a JSON body")
		trace.noteFacadeTransform(AccountTypeGemini, acc, reqPath, "", model, "", projectID, "error", err)
		return nil, err
	}

	var in geminiAPIRequestPayload
	if err := json.Unmarshal(bodyBytes, &in); err != nil {
		wrappedErr := fmt.Errorf("parse gemini v1beta request: %w", err)
		trace.noteFacadeTransform(AccountTypeGemini, acc, reqPath, "", model, "", projectID, "error", wrappedErr)
		return nil, wrappedErr
	}

	upstreamMethod := "/v1internal:" + method
	rewrittenModel := rewriteGeminiCodeAssistFacadeModel(model)
	targetBases := []*url.URL{geminiProvider.geminiBase}
	if shouldUseAntigravityGeminiCodeAssistBaseFallback(rewrittenModel) {
		if candidates := antigravityGeminiCodeAssistBaseCandidates(geminiProvider.geminiBase); len(candidates) > 0 {
			targetBases = candidates
		}
	}
	out := geminiCodeAssistRequestPayload{
		Model:        rewrittenModel,
		Project:      projectID,
		UserPromptID: reqID,
		Request: geminiCodeAssistInnerRequestPayload{
			Contents:          in.Contents,
			SystemInstruction: in.SystemInstruction,
			CachedContent:     in.CachedContent,
			Tools:             in.Tools,
			ToolConfig:        in.ToolConfig,
			Labels:            in.Labels,
			SafetySettings:    in.SafetySettings,
			GenerationConfig:  in.GenerationConfig,
			SessionID:         reqID,
		},
	}

	outBody, err := json.Marshal(out)
	if err != nil {
		wrappedErr := fmt.Errorf("marshal gemini code assist request: %w", err)
		trace.noteFacadeTransform(AccountTypeGemini, acc, reqPath, "", model, out.Model, projectID, "error", wrappedErr)
		return nil, wrappedErr
	}
	trace.noteFacadeTransform(AccountTypeGemini, acc, reqPath, upstreamMethod, model, out.Model, projectID, "ok", nil)
	return &geminiCodeAssistFacadeRequest{
		targetBase:  targetBases[0],
		targetBases: targetBases,
		path:        upstreamMethod,
		body:        outBody,
	}, nil
}

func (h *proxyHandler) maybePrimeGeminiCodeAssistFacade(ctx context.Context, acc *Account, facadeReq *geminiCodeAssistFacadeRequest) error {
	if h == nil || acc == nil || facadeReq == nil || !canRouteValidationBlockedAntigravityGemini(acc) {
		return nil
	}
	projectID := effectiveGeminiCodeAssistProjectID(acc)
	if projectID == "" {
		return fmt.Errorf("gemini account %s missing antigravity project id for blocked facade preflight", acc.ID)
	}
	acc.mu.Lock()
	accessToken := strings.TrimSpace(acc.AccessToken)
	acc.mu.Unlock()
	if accessToken == "" {
		return fmt.Errorf("account %s has empty access token", acc.ID)
	}
	if _, err := h.loadAntigravityGeminiCodeAssist(ctx, accessToken, projectID, ""); err != nil {
		return fmt.Errorf("prime blocked gemini facade account %s: %w", acc.ID, err)
	}
	return nil
}

func maybeTransformGeminiCodeAssistFacadeResponse(reqPath string, resp *http.Response) error {
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	_, method, ok := parseGeminiAPIPath(reqPath)
	if !ok {
		return nil
	}
	if method == "streamGenerateContent" {
		pr, pw := io.Pipe()
		src, err := decodeMaybeGzipResponseBody(resp)
		if err != nil {
			return err
		}
		go func() {
			err := transformGeminiCodeAssistSSE(pw, src)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			_ = pw.Close()
		}()
		resp.Body = pr
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return nil
	}

	raw, err := readMaybeGzipResponseBody(resp)
	if err != nil {
		return err
	}
	unwrapped, err := unwrapGeminiCodeAssistResponse(raw)
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(unwrapped))
	resp.ContentLength = int64(len(unwrapped))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(unwrapped)))
	return nil
}

func transformGeminiCodeAssistSSE(dst io.Writer, src io.ReadCloser) error {
	defer src.Close()

	reader := bufio.NewReader(src)
	var dataLines []string

	flushChunk := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		chunk := strings.Join(dataLines, "\n")
		dataLines = nil
		unwrapped, err := unwrapGeminiCodeAssistResponse([]byte(chunk))
		if err != nil {
			return err
		}
		if _, err := dst.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err := dst.Write(unwrapped); err != nil {
			return err
		}
		_, err = dst.Write([]byte("\n\n"))
		return err
	}

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimSpace(line[len("data: "):]))
		} else if line == "" {
			if err := flushChunk(); err != nil {
				return err
			}
		} else if len(dataLines) == 0 {
			if _, writeErr := dst.Write([]byte(line + "\n")); writeErr != nil {
				return writeErr
			}
		}

		if err != nil {
			if err == io.EOF {
				return flushChunk()
			}
			return err
		}
	}
}

func unwrapGeminiCodeAssistResponse(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return trimmed, nil
	}

	var envelope geminiCodeAssistResponseEnvelope
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(envelope.Response)) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Response), []byte("null")) {
		return trimmed, nil
	}

	var response map[string]any
	if err := json.Unmarshal(envelope.Response, &response); err != nil {
		return bytes.TrimSpace(envelope.Response), nil
	}
	if strings.TrimSpace(envelope.TraceID) != "" {
		if _, ok := response["responseId"]; !ok {
			response["responseId"] = strings.TrimSpace(envelope.TraceID)
		}
	}
	return json.Marshal(response)
}
