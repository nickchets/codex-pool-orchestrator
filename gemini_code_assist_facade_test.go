package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMaybeBuildGeminiCodeAssistFacadeRequest(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)
	acc := &Account{
		ID:                   "gemini_antigravity",
		Type:                 AccountTypeGemini,
		AntigravityProjectID: "psyched-sphere-vj8c5",
	}

	facade, err := maybeBuildGeminiCodeAssistFacadeRequest(
		context.Background(),
		provider,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"Reply with exactly OK."}]}],"generationConfig":{"responseMimeType":"text/plain"}}`),
		acc,
		"req-123",
	)
	if err != nil {
		t.Fatalf("maybeBuildGeminiCodeAssistFacadeRequest: %v", err)
	}
	if facade == nil {
		t.Fatal("expected facade request")
	}
	if got := facade.targetBase.String(); got != geminiBase.String() {
		t.Fatalf("targetBase = %q, want %q", got, geminiBase.String())
	}
	if facade.path != "/v1internal:generateContent" {
		t.Fatalf("path = %q", facade.path)
	}

	var payload map[string]any
	if err := json.Unmarshal(facade.body, &payload); err != nil {
		t.Fatalf("unmarshal facade body: %v", err)
	}
	if got := payload["model"]; got != "gemini-2.5-flash" {
		t.Fatalf("model = %#v", got)
	}
	if got := payload["project"]; got != "psyched-sphere-vj8c5" {
		t.Fatalf("project = %#v", got)
	}
	if got := payload["user_prompt_id"]; got != "req-123" {
		t.Fatalf("user_prompt_id = %#v", got)
	}

	request, ok := payload["request"].(map[string]any)
	if !ok {
		t.Fatalf("request payload missing: %#v", payload["request"])
	}
	if got := request["session_id"]; got != "req-123" {
		t.Fatalf("session_id = %#v", got)
	}
	if _, ok := request["contents"]; !ok {
		t.Fatalf("contents missing from request: %#v", request)
	}
	if _, ok := request["metadata"]; ok {
		t.Fatalf("metadata should stay out of generateContent request: %#v", request["metadata"])
	}
}

func TestMaybeBuildGeminiCodeAssistFacadeRequestRewritesAntigravityModels(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)
	acc := &Account{
		ID:                   "gemini_antigravity",
		Type:                 AccountTypeGemini,
		AntigravityProjectID: "psyched-sphere-vj8c5",
	}

	tests := []struct {
		name      string
		reqPath   string
		wantModel string
	}{
		{
			name:      "gemini 3.1 alias",
			reqPath:   "/v1beta/models/gemini-3.1-pro:generateContent",
			wantModel: "gemini-3.1-pro-high",
		},
		{
			name:      "gemini 3.1 preview",
			reqPath:   "/v1beta/models/gemini-3.1-pro-preview:generateContent",
			wantModel: "gemini-3.1-pro-high",
		},
		{
			name:      "gemini 3.1 alias",
			reqPath:   "/v1beta/models/gemini-3.1-pro:generateContent",
			wantModel: "gemini-3.1-pro-high",
		},
		{
			name:      "gemini 3 preview",
			reqPath:   "/v1beta/models/gemini-3-pro-preview:generateContent",
			wantModel: "gemini-3.1-pro-high",
		},
		{
			name:      "customtools stays unchanged",
			reqPath:   "/v1beta/models/gemini-3.1-pro-preview-customtools:generateContent",
			wantModel: "gemini-3.1-pro-preview-customtools",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			facade, err := maybeBuildGeminiCodeAssistFacadeRequest(
				context.Background(),
				provider,
				tc.reqPath,
				[]byte(`{"contents":[{"role":"user","parts":[{"text":"Reply with exactly OK."}]}]}`),
				acc,
				"req-789",
			)
			if err != nil {
				t.Fatalf("maybeBuildGeminiCodeAssistFacadeRequest: %v", err)
			}
			if facade == nil {
				t.Fatal("expected facade request")
			}

			var payload map[string]any
			if err := json.Unmarshal(facade.body, &payload); err != nil {
				t.Fatalf("unmarshal facade body: %v", err)
			}
			if got := payload["model"]; got != tc.wantModel {
				t.Fatalf("model = %#v, want %q", got, tc.wantModel)
			}
		})
	}
}

func TestMaybeBuildGeminiCodeAssistFacadeRequestUsesHostFallbackCandidatesForGemini31(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)
	acc := &Account{
		ID:                   "gemini_antigravity",
		Type:                 AccountTypeGemini,
		AntigravityProjectID: "psyched-sphere-vj8c5",
	}

	facade, err := maybeBuildGeminiCodeAssistFacadeRequest(
		context.Background(),
		provider,
		"/v1beta/models/gemini-3.1-pro:generateContent",
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"Reply with exactly OK."}]}]}`),
		acc,
		"req-host-fallback",
	)
	if err != nil {
		t.Fatalf("maybeBuildGeminiCodeAssistFacadeRequest: %v", err)
	}
	if facade == nil {
		t.Fatal("expected facade request")
	}

	got := make([]string, 0, len(facade.targetBases))
	for _, base := range facade.targetBases {
		if base != nil {
			got = append(got, base.String())
		}
	}
	want := []string{
		"https://daily-cloudcode-pa.sandbox.googleapis.com",
		"https://daily-cloudcode-pa.googleapis.com",
		"https://cloudcode-pa.googleapis.com",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("targetBases = %v, want %v", got, want)
	}
	if facade.targetBase == nil || facade.targetBase.String() != want[0] {
		t.Fatalf("targetBase = %v, want %s", facade.targetBase, want[0])
	}
}

func TestMaybeBuildGeminiCodeAssistFacadeRequestRequiresProjectID(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)

	_, err := maybeBuildGeminiCodeAssistFacadeRequest(
		context.Background(),
		provider,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		&Account{ID: "gemini_manual", Type: AccountTypeGemini},
		"req-456",
	)
	if err == nil || !strings.Contains(err.Error(), "missing antigravity project id") {
		t.Fatalf("err = %v, want missing project id", err)
	}
}

func TestMaybeBuildGeminiCodeAssistFacadeRequestUsesFallbackProjectForAntigravitySeatWithoutProjectID(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)

	facade, err := maybeBuildGeminiCodeAssistFacadeRequest(
		context.Background(),
		provider,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		&Account{
			ID:                "gemini_antigravity_projectless",
			Type:              AccountTypeGemini,
			OAuthProfileID:    geminiOAuthAntigravityProfileID,
			AntigravitySource: "browser_oauth",
		},
		"req-fallback",
	)
	if err != nil {
		t.Fatalf("maybeBuildGeminiCodeAssistFacadeRequest: %v", err)
	}
	if facade == nil {
		t.Fatal("expected facade request")
	}

	var payload map[string]any
	if err := json.Unmarshal(facade.body, &payload); err != nil {
		t.Fatalf("unmarshal facade body: %v", err)
	}
	if got := payload["project"]; got != antigravityGeminiFallbackProject {
		t.Fatalf("project = %#v, want %q", got, antigravityGeminiFallbackProject)
	}
}

func TestMaybeBuildGeminiCodeAssistFacadeRequestUsesFallbackProjectForAllowlistedValidationBlockedSeat(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)

	facade, err := maybeBuildGeminiCodeAssistFacadeRequest(
		context.Background(),
		provider,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		&Account{
			ID:                           "gemini_antigravity_blocked",
			Type:                         AccountTypeGemini,
			OAuthProfileID:               geminiOAuthAntigravityProfileID,
			AntigravitySource:            "browser_oauth",
			AntigravityValidationBlocked: true,
			GeminiValidationReasonCode:   "INELIGIBLE_ACCOUNT",
		},
		"req-blocked-fallback",
	)
	if err != nil {
		t.Fatalf("maybeBuildGeminiCodeAssistFacadeRequest: %v", err)
	}
	if facade == nil {
		t.Fatal("expected facade request")
	}

	var payload map[string]any
	if err := json.Unmarshal(facade.body, &payload); err != nil {
		t.Fatalf("unmarshal facade body: %v", err)
	}
	if got := payload["project"]; got != antigravityGeminiFallbackProject {
		t.Fatalf("project = %#v, want %q", got, antigravityGeminiFallbackProject)
	}
}

func TestMaybePrimeGeminiCodeAssistFacadeLoadsAllowlistedValidationBlockedSeat(t *testing.T) {
	var loadReq antigravityLoadCodeAssistRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:loadCodeAssist" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("authorization=%q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&loadReq); err != nil {
			t.Fatalf("decode loadCodeAssist request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":"bamboo-precept-lgxtn"}`))
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	h := &proxyHandler{cfg: config{geminiBase: baseURL}}
	acc := &Account{
		ID:                           "gemini_antigravity_blocked",
		Type:                         AccountTypeGemini,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AccessToken:                  "access-token",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
	}

	if err := h.maybePrimeGeminiCodeAssistFacade(context.Background(), acc, &geminiCodeAssistFacadeRequest{}); err != nil {
		t.Fatalf("maybePrimeGeminiCodeAssistFacade: %v", err)
	}
	if loadReq.CloudaicompanionProject != antigravityGeminiFallbackProject {
		t.Fatalf("load_code_assist project=%q", loadReq.CloudaicompanionProject)
	}
	if loadReq.Metadata.IdeName != "antigravity-insiders" {
		t.Fatalf("metadata.ide_name=%q", loadReq.Metadata.IdeName)
	}
}

func TestMaybePrimeGeminiCodeAssistFacadeSkipsHealthySeat(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	h := &proxyHandler{cfg: config{geminiBase: baseURL}}
	acc := &Account{
		ID:                "gemini_antigravity_healthy",
		Type:              AccountTypeGemini,
		OAuthProfileID:    geminiOAuthAntigravityProfileID,
		AntigravitySource: "browser_oauth",
		AccessToken:       "access-token",
	}

	if err := h.maybePrimeGeminiCodeAssistFacade(context.Background(), acc, &geminiCodeAssistFacadeRequest{}); err != nil {
		t.Fatalf("maybePrimeGeminiCodeAssistFacade: %v", err)
	}
	if called {
		t.Fatal("expected healthy seat to skip loadCodeAssist preflight")
	}
}

func TestMaybeBuildGeminiCodeAssistFacadeRequestLogsTrace(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)
	acc := &Account{
		ID:                   "gemini_antigravity",
		Type:                 AccountTypeGemini,
		AuthMode:             accountAuthModeOAuth,
		AntigravityProjectID: "psyched-sphere-vj8c5",
	}

	logs := captureLogs(t, func() {
		facade, err := maybeBuildGeminiCodeAssistFacadeRequest(
			testTraceContext("req-facade"),
			provider,
			"/v1beta/models/gemini-3.1-pro:generateContent",
			[]byte(`{"contents":[{"role":"user","parts":[{"text":"Reply with exactly OK."}]}]}`),
			acc,
			"req-facade",
		)
		if err != nil {
			t.Fatalf("maybeBuildGeminiCodeAssistFacadeRequest: %v", err)
		}
		if facade == nil {
			t.Fatal("expected facade request")
		}
	})

	if !strings.Contains(logs, "[req-facade] trace facade_transform") {
		t.Fatalf("missing facade trace log: %s", logs)
	}
	if !strings.Contains(logs, `provider=gemini`) || !strings.Contains(logs, `result=ok`) {
		t.Fatalf("unexpected facade trace log: %s", logs)
	}
	if !strings.Contains(logs, `rewritten_model="gemini-3.1-pro-high"`) {
		t.Fatalf("missing rewritten model in facade trace log: %s", logs)
	}
}

func TestGeminiProviderSupportsAccountPathRequiresAntigravityProjectForV1Beta(t *testing.T) {
	geminiBase, _ := url.Parse("https://cloudcode-pa.googleapis.com")
	geminiAPIBase, _ := url.Parse("https://generativelanguage.googleapis.com")
	provider := NewGeminiProvider(geminiBase, geminiAPIBase)

	if provider.SupportsAccountPath("/v1beta/models/gemini-2.5-flash:generateContent", &Account{ID: "manual", Type: AccountTypeGemini}) {
		t.Fatal("expected manual Gemini seat without antigravity project to be rejected for v1beta")
	}
	if !provider.SupportsAccountPath("/v1beta/models/gemini-2.5-flash:generateContent", &Account{
		ID:                "antigravity-projectless",
		Type:              AccountTypeGemini,
		OAuthProfileID:    geminiOAuthAntigravityProfileID,
		AntigravitySource: "browser_oauth",
	}) {
		t.Fatal("expected projectless antigravity Gemini seat to be accepted for v1beta via fallback project")
	}
	if !provider.SupportsAccountPath("/v1beta/models/gemini-2.5-flash:generateContent", &Account{
		ID:                           "antigravity-blocked",
		Type:                         AccountTypeGemini,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "UNSUPPORTED_LOCATION",
	}) {
		t.Fatal("expected allowlisted validation-blocked antigravity Gemini seat to be accepted for v1beta via fallback project")
	}
	if provider.SupportsAccountPath("/v1beta/models/gemini-2.5-flash:generateContent", &Account{
		ID:                           "antigravity-still-blocked",
		Type:                         AccountTypeGemini,
		OAuthProfileID:               geminiOAuthAntigravityProfileID,
		AntigravitySource:            "browser_oauth",
		AntigravityValidationBlocked: true,
		GeminiValidationReasonCode:   "ACCOUNT_NEEDS_WORKSPACE",
	}) {
		t.Fatal("expected non-allowlisted validation-blocked antigravity Gemini seat to stay rejected for v1beta")
	}
	if !provider.SupportsAccountPath("/v1beta/models/gemini-2.5-flash:generateContent", &Account{ID: "antigravity", Type: AccountTypeGemini, AntigravityProjectID: "psyched-sphere-vj8c5"}) {
		t.Fatal("expected antigravity Gemini seat with project id to be accepted for v1beta")
	}
	if !provider.SupportsAccountPath("/v1internal:generateContent", &Account{ID: "manual", Type: AccountTypeGemini}) {
		t.Fatal("expected Gemini v1internal path to remain available without antigravity project id")
	}
}

func TestUnwrapGeminiCodeAssistResponse(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]}}],"modelVersion":"gemini-2.5-flash"},"traceId":"trace-123"}`)

	out, err := unwrapGeminiCodeAssistResponse(raw)
	if err != nil {
		t.Fatalf("unwrapGeminiCodeAssistResponse: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got := payload["responseId"]; got != "trace-123" {
		t.Fatalf("responseId = %#v", got)
	}
	if got := payload["modelVersion"]; got != "gemini-2.5-flash" {
		t.Fatalf("modelVersion = %#v", got)
	}
}

func TestTransformGeminiCodeAssistSSE(t *testing.T) {
	src := ioNopCloserString("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"OK\"}]}}]},\"traceId\":\"trace-789\"}\n\n")
	var dst bytes.Buffer

	if err := transformGeminiCodeAssistSSE(&dst, src); err != nil {
		t.Fatalf("transformGeminiCodeAssistSSE: %v", err)
	}

	got := dst.String()
	if !strings.Contains(got, `"responseId":"trace-789"`) {
		t.Fatalf("transformed SSE missing responseId: %s", got)
	}
	if !strings.HasPrefix(got, "data: ") {
		t.Fatalf("transformed SSE missing data prefix: %s", got)
	}
}

func TestMaybeTransformGeminiCodeAssistFacadeResponseBuffered(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       ioNopCloserString(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]}}]},"traceId":"trace-999"}`),
	}

	if err := maybeTransformGeminiCodeAssistFacadeResponse("/v1beta/models/gemini-2.5-flash:generateContent", resp); err != nil {
		t.Fatalf("maybeTransformGeminiCodeAssistFacadeResponse: %v", err)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read transformed body: %v", err)
	}
	if !strings.Contains(string(raw), `"responseId":"trace-999"`) {
		t.Fatalf("buffered transform missing responseId: %s", string(raw))
	}
}

func TestMaybeTransformGeminiCodeAssistFacadeResponseBufferedGzip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]}}]},"traceId":"trace-gzip"}`)); err != nil {
		t.Fatalf("write gzip body: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip body: %v", err)
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}

	if err := maybeTransformGeminiCodeAssistFacadeResponse("/v1beta/models/gemini-2.5-flash:generateContent", resp); err != nil {
		t.Fatalf("maybeTransformGeminiCodeAssistFacadeResponse: %v", err)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read transformed body: %v", err)
	}
	if !strings.Contains(string(raw), `"responseId":"trace-gzip"`) {
		t.Fatalf("gzip buffered transform missing responseId: %s", string(raw))
	}
}

func ioNopCloserString(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}
