package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type phase4RoundTripperFunc func(*http.Request) (*http.Response, error)

func (f phase4RoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newPhase4VirtualKeyHandler(t *testing.T, policy PoolAPITokenPolicy, transport http.RoundTripper, accounts ...*Account) (*proxyHandler, string) {
	t.Helper()

	store, user, _ := newTestPoolUserStoreWithUser(t)
	rawKey, _, err := store.CreateAPITokenWithPolicy(user.ID, "phase4", policy)
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}

	baseURL, err := url.Parse("https://upstream.example")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	codex := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	claude := NewClaudeProvider(baseURL)
	gemini := NewGeminiProvider(baseURL, baseURL)
	kimi := NewKimiProvider(baseURL)
	minimax := NewMinimaxProvider(baseURL)

	if transport == nil {
		transport = phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"ok","usage":{"input_tokens":4,"output_tokens":2}}`)),
				Request:    req,
			}, nil
		})
	}
	if accounts == nil {
		accounts = []*Account{{
			ID:          "codex-seat",
			Type:        AccountTypeCodex,
			AccessToken: "seat-access",
			PlanType:    "pro",
			Usage: UsageSnapshot{
				RetrievedAt: time.Now().UTC(),
			},
		}}
	}

	return &proxyHandler{
		cfg: config{
			disableRefresh:       true,
			maxAttempts:          1,
			maxInMemoryBodyBytes: 1024,
		},
		transport: transport,
		pool:      newPoolState(accounts, false),
		poolUsers: store,
		registry:  NewProviderRegistry(codex, claude, gemini, kimi, minimax),
		metrics:   newMetrics(),
		recent:    newRecentErrors(5),
		startTime: time.Now().Add(-time.Minute),
	}, rawKey
}

func TestPhase4PoolAPITokenAdmissionCarriesAllowedModels(t *testing.T) {
	store, user, _ := newTestPoolUserStoreWithUser(t)
	rawKey, _, err := store.CreateAPITokenWithPolicy(user.ID, "limited", PoolAPITokenPolicy{AllowedModels: []string{"gpt-5-codex"}})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}

	h := &proxyHandler{poolUsers: store}
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)

	admission := h.resolveProxyAdmission(req, "req-phase4-admission")
	if admission.Kind != AdmissionKindPoolUser || admission.CredentialKind != CredentialKindOpenAICompatiblePoolKey {
		t.Fatalf("admission=%+v", admission)
	}
	if len(admission.TokenAllowedModels) != 1 || admission.TokenAllowedModels[0] != "gpt-5-codex" {
		t.Fatalf("token allowed models=%v", admission.TokenAllowedModels)
	}
}

func TestPhase4PlanRouteMarksVirtualOpenAICompatibleEndpoints(t *testing.T) {
	h := newPlanningTestHandler(t)
	admission := AdmissionResult{
		Kind:           AdmissionKindPoolUser,
		UserID:         "user-1",
		TokenID:        "tok-1",
		CredentialKind: CredentialKindOpenAICompatiblePoolKey,
	}

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{name: "responses", method: http.MethodPost, path: "/v1/responses", body: []byte(`{"model":"gpt-5-codex","input":"hi"}`)},
		{name: "chat", method: http.MethodPost, path: "/v1/chat/completions", body: []byte(`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hi"}]}`)},
		{name: "models", method: http.MethodGet, path: "/v1/models", body: nil},
		{name: "response retrieval", method: http.MethodGet, path: "/v1/responses/resp_123", body: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://pool.local"+tc.path, strings.NewReader(string(tc.body)))
			shape := RequestShape{Path: tc.path}
			if tc.body != nil {
				shape = buildBufferedRequestShape(req, tc.body, tc.body)
			}
			plan, _, err := h.planRoute(admission, req, shape, tc.body)
			if err != nil {
				t.Fatalf("plan route: %v", err)
			}
			if !plan.IsOpenAICompatibleClient {
				t.Fatalf("expected OpenAI-compatible client route: %+v", plan)
			}
			if plan.SelectionMode != SelectionOpenAICompatible {
				t.Fatalf("selection_mode=%q", plan.SelectionMode)
			}
		})
	}
}

func TestPhase4PlanRouteDoesNotMarkLegacyPoolUserAsOpenAICompatible(t *testing.T) {
	h := newPlanningTestHandler(t)
	body := []byte(`{"model":"gpt-5-codex","input":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(string(body)))

	plan, _, err := h.planRoute(AdmissionResult{Kind: AdmissionKindPoolUser, UserID: "legacy-user"}, req, buildBufferedRequestShape(req, body, body), body)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	if plan.IsOpenAICompatibleClient {
		t.Fatalf("legacy pool token should not be marked OpenAI-compatible: %+v", plan)
	}
	if plan.SelectionMode != SelectionAnyPoolSeat {
		t.Fatalf("selection_mode=%q", plan.SelectionMode)
	}
}

func TestPhase4OpenAICompatibleEndpointPathAllowlistRejectsDotSegments(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: "/v1/models", want: true},
		{path: "/v1/responses", want: true},
		{path: "/v1/chat/completions", want: true},
		{path: "/v1/responses/resp_123", want: true},
		{path: "/v1/responses/resp-123_ABC", want: true},
		{path: "/v1/responses/resp_123/cancel", want: true},
		{path: "/v1/responses/resp_123/input_items", want: true},
		{path: "/v1/responses/", want: false},
		{path: "/v1/files", want: false},
		{path: "/v1/responses/../../v1/files", want: false},
		{path: "/v1/responses/../v1/files", want: false},
		{path: "/v1/responses/./resp_123", want: false},
		{path: "/v1/responses/%2e%2e/v1/files", want: false},
		{path: "/v1/responses/resp.123", want: false},
		{path: "/v1/responses/resp_123/../../v1/files", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isOpenAICompatibleEndpointPath(tc.path); got != tc.want {
				t.Fatalf("isOpenAICompatibleEndpointPath(%q)=%v want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestPhase4VirtualKeyDotSegmentResponsesPathRejectsBeforeUpstream(t *testing.T) {
	cases := []struct {
		name    string
		rawPath string
	}{
		{name: "raw dot segments", rawPath: "/v1/responses/../../v1/files"},
		{name: "encoded dot segment", rawPath: "/v1/responses/%2e%2e/v1/files"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls int
			h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamCalls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"id":"should-not-happen"}`)),
					Request:    req,
				}, nil
			}))

			req := httptest.NewRequest(http.MethodPost, "http://pool.local"+tc.rawPath, strings.NewReader(`{"model":"gpt-5-codex","input":"hi"}`))
			if strings.Contains(tc.rawPath, "%2e") && !strings.Contains(req.URL.Path, "..") {
				t.Skipf("encoded dot segment is not exposed in URL.Path: path=%q raw_path=%q", req.URL.Path, req.URL.RawPath)
			}
			req.Header.Set("Authorization", "Bearer "+rawKey)
			rr := httptest.NewRecorder()

			h.proxyRequest(rr, req, "req-phase4-dot-segment")
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s path=%q raw_path=%q", rr.Code, rr.Body.String(), req.URL.Path, req.URL.RawPath)
			}
			if upstreamCalls != 0 {
				t.Fatalf("upstream calls=%d", upstreamCalls)
			}
		})
	}
}

func TestPhase4VirtualKeyModelAllowlistRejectsBeforeUpstream(t *testing.T) {
	var upstreamCalls int
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{AllowedModels: []string{"gpt-4.1-mini"}}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"should-not-happen"}`)),
			Request:    req,
		}, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-phase4-disallowed")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
}

func TestPhase4VirtualKeyChunkedDisallowedModelRejectsBeforeUpstream(t *testing.T) {
	var upstreamCalls int
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{AllowedModels: []string{"gpt-4.1-mini"}}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"should-not-happen"}`)),
			Request:    req,
		}, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","input":"hi"}`))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-phase4-chunked-disallowed")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
}

func TestPhase4VirtualKeyChunkedInvalidJSONWithRestrictiveAllowlistRejectsBeforeUpstream(t *testing.T) {
	var upstreamCalls int
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{AllowedModels: []string{"gpt-4.1-mini"}}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"should-not-happen"}`)),
			Request:    req,
		}, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(`{"model":"gpt-4.1-mini","input":"unterminated`))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-phase4-chunked-invalid-json")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
}

func TestPhase4VirtualKeyChunkedAllowedOrDefaultModelUsesBufferedPath(t *testing.T) {
	cases := []struct {
		name   string
		policy PoolAPITokenPolicy
		body   string
	}{
		{name: "allowed model", policy: PoolAPITokenPolicy{AllowedModels: []string{"gpt-4.1-mini"}}, body: `{"model":"gpt-4.1-mini","input":"hi"}`},
		{name: "default policy", policy: PoolAPITokenPolicy{}, body: `{"model":"gpt-5-codex","input":"hi"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls int
			var upstreamBody string
			var upstreamContentLength int64
			h, rawKey := newPhase4VirtualKeyHandler(t, tc.policy, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamCalls++
				upstreamContentLength = req.ContentLength
				bodyBytes, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read upstream body: %v", err)
				}
				upstreamBody = string(bodyBytes)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"id":"ok","usage":{"input_tokens":3,"output_tokens":1}}`)),
					Request:    req,
				}, nil
			}))

			req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(tc.body))
			req.ContentLength = -1
			req.TransferEncoding = []string{"chunked"}
			req.Header.Set("Authorization", "Bearer "+rawKey)
			rr := httptest.NewRecorder()

			h.proxyRequest(rr, req, "req-phase4-chunked-allowed")
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if upstreamCalls != 1 {
				t.Fatalf("upstream calls=%d", upstreamCalls)
			}
			if upstreamBody != tc.body {
				t.Fatalf("upstream body=%q want %q", upstreamBody, tc.body)
			}
			if upstreamContentLength != int64(len(tc.body)) {
				t.Fatalf("upstream content length=%d want %d", upstreamContentLength, len(tc.body))
			}
		})
	}
}

func TestPhase4VirtualKeyOversizedOpenAIRequestRejectsBeforeUpstream(t *testing.T) {
	var upstreamCalls int
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{AllowedModels: []string{"gpt-4.1-mini"}}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"should-not-happen"}`)),
			Request:    req,
		}, nil
	}))
	h.cfg.maxInMemoryBodyBytes = 64

	body := `{"model":"gpt-4.1-mini","input":"` + strings.Repeat("x", 128) + `"}`
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-phase4-oversized")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
}

func TestPhase4VirtualKeyNonOpenAIRouteRejectsBeforeUpstream(t *testing.T) {
	var upstreamCalls int
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"should-not-happen"}`)),
			Request:    req,
		}, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "http://pool.local/backend-api/conversation", strings.NewReader(`{"model":"gpt-4.1-mini","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-phase4-non-openai-route")
	if rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
}

func TestPhase4RealProviderPassthroughChunkedBodyNotAffected(t *testing.T) {
	var upstreamCalls int
	var upstreamBody string
	var upstreamAuthorization string
	var upstreamContentLength int64
	var upstreamGetBodySet bool
	h, _ := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		upstreamAuthorization = req.Header.Get("Authorization")
		upstreamContentLength = req.ContentLength
		upstreamGetBodySet = req.GetBody != nil
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBody = string(bodyBytes)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"ok"}`)),
			Request:    req,
		}, nil
	}))
	h.cfg.maxInMemoryBodyBytes = 16

	body := `{"model":"gpt-5-codex","input":"` + strings.Repeat("x", 128) + `"}`
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/responses", strings.NewReader(body))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Authorization", "Bearer sk-real-provider-key")
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-phase4-real-passthrough")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
	if upstreamBody != body {
		t.Fatalf("upstream body=%q want %q", upstreamBody, body)
	}
	if upstreamAuthorization != "Bearer sk-real-provider-key" {
		t.Fatalf("authorization=%q", upstreamAuthorization)
	}
	if upstreamContentLength > 0 {
		t.Fatalf("passthrough chunked request should remain streamed, content length=%d", upstreamContentLength)
	}
	if upstreamGetBodySet {
		t.Fatal("passthrough chunked request unexpectedly had replay GetBody set")
	}
}

func TestPhase4VirtualKeyModelAllowlistAllowsWildcardAndDefault(t *testing.T) {
	cases := []struct {
		name   string
		policy PoolAPITokenPolicy
	}{
		{name: "default", policy: PoolAPITokenPolicy{}},
		{name: "explicit wildcard", policy: PoolAPITokenPolicy{AllowedModels: []string{"*"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls int
			h, rawKey := newPhase4VirtualKeyHandler(t, tc.policy, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamCalls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"id":"ok","usage":{"input_tokens":3,"output_tokens":1}}`)),
					Request:    req,
				}, nil
			}))

			req := httptest.NewRequest(http.MethodPost, "http://pool.local/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+rawKey)
			rr := httptest.NewRecorder()

			h.proxyRequest(rr, req, "req-phase4-wildcard")
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if upstreamCalls != 1 {
				t.Fatalf("upstream calls=%d", upstreamCalls)
			}
		})
	}
}

func TestPhase4VirtualKeyModelsReturnsSyntheticAllowedListWithoutUpstream(t *testing.T) {
	var upstreamCalls int
	h, rawKey := newPhase4VirtualKeyHandler(t, PoolAPITokenPolicy{AllowedModels: []string{"gpt-4.1-mini", "gpt-5-codex"}}, phase4RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{
			StatusCode: http.StatusTeapot,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("upstream should not be used")),
			Request:    req,
		}, nil
	}), []*Account{}...)

	req := httptest.NewRequest(http.MethodGet, "http://pool.local/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rr := httptest.NewRecorder()

	h.proxyRequest(rr, req, "req-phase4-models")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}

	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal models response: %v body=%s", err, rr.Body.String())
	}
	if payload.Object != "list" {
		t.Fatalf("object=%q", payload.Object)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("models=%+v", payload.Data)
	}
	seen := map[string]bool{}
	for _, model := range payload.Data {
		seen[model.ID] = true
		if model.Object != "model" {
			t.Fatalf("model object for %s = %q", model.ID, model.Object)
		}
	}
	if !seen["gpt-4.1-mini"] || !seen["gpt-5-codex"] {
		t.Fatalf("models response missing allowed IDs: %+v", payload.Data)
	}
}

func TestPhase4CodexNonStreamJSONUsageRecordsViaBodyPath(t *testing.T) {
	baseURL, err := url.Parse("https://upstream.example")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	provider := NewCodexProvider(baseURL, baseURL, baseURL, baseURL)
	if provider.DetectsSSE("/v1/responses", "application/json") {
		t.Fatal("application/json /v1/responses should not be classified as SSE")
	}

	acc := &Account{ID: "codex-seat", Type: AccountTypeCodex, PlanType: "pro"}
	h := &proxyHandler{pool: newPoolState([]*Account{acc}, false)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":4}}`)),
	}
	rr := httptest.NewRecorder()

	ok := h.deliverCopiedProxyResponse(rr, func() {}, "req-phase4-json-usage", nil, provider, acc, "user-1", resp, time.Now(), copiedProxyResponseDeliveryOptions{
		requestPath:           "/v1/responses",
		captureResponseSample: true,
		closeBodyAfterCopy:    true,
	})
	if !ok {
		t.Fatal("deliverCopiedProxyResponse returned false")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if acc.Totals.RequestCount != 1 {
		t.Fatalf("request_count=%d totals=%+v", acc.Totals.RequestCount, acc.Totals)
	}
	if acc.Totals.TotalInputTokens != 10 || acc.Totals.TotalCachedTokens != 2 || acc.Totals.TotalOutputTokens != 4 {
		t.Fatalf("totals=%+v", acc.Totals)
	}
}

func TestPhase4CodexStreamingEventStreamStillDetectsSSE(t *testing.T) {
	provider := &CodexProvider{}
	if !provider.DetectsSSE("/v1/responses", "text/event-stream; charset=utf-8") {
		t.Fatal("text/event-stream should be classified as SSE")
	}
	if !provider.DetectsSSE("/responses", "") {
		t.Fatal("legacy responses without content-type should remain SSE-compatible")
	}
}
