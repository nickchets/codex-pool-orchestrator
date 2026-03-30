package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleAggregatedUsageReturnsClientCompatiblePlanType(t *testing.T) {
	h := &proxyHandler{
		pool: newPoolState([]*Account{
			{
				ID:          "codex-gitlab",
				Type:        AccountTypeCodex,
				PlanType:    "gitlab_duo",
				AuthMode:    accountAuthModeGitLab,
				AccessToken: "token",
			},
		}, false),
	}

	rr := httptest.NewRecorder()
	h.handleAggregatedUsage(rr, "req-usage")

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := payload["plan_type"].(string); got != "pro" {
		t.Fatalf("plan_type=%q", got)
	}
	if pooled, _ := payload["is_pooled"].(bool); !pooled {
		t.Fatalf("is_pooled=%v", payload["is_pooled"])
	}
	if got, _ := payload["pool_plan_type"].(string); got != "pool" {
		t.Fatalf("pool_plan_type=%q", got)
	}
}

func TestMaybeServeGitLabCodexAuxiliary(t *testing.T) {
	h := &proxyHandler{
		cfg: config{
			forceCodexRequiredPlan: accountAuthModeGitLab,
		},
	}

	cases := []struct {
		name     string
		method   string
		path     string
		wantBody string
	}{
		{name: "plugins list", method: http.MethodGet, path: "/backend-api/plugins/list", wantBody: `{"items":[]}`},
		{name: "plugins featured", method: http.MethodGet, path: "/backend-api/plugins/featured", wantBody: `[]`},
		{name: "connectors list", method: http.MethodGet, path: "/backend-api/connectors/directory/list", wantBody: `{"items":[]}`},
		{name: "wham apps", method: http.MethodPost, path: "/backend-api/wham/apps", wantBody: `{"items":[]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://example.com"+tc.path, nil)
			rr := httptest.NewRecorder()
			if !h.maybeServeGitLabCodexAuxiliary(rr, req, "req-aux", AdmissionResult{Kind: AdmissionKindPoolUser}) {
				t.Fatal("expected auxiliary request to be handled")
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("content-type=%q", got)
			}
			if got := rr.Body.String(); got != tc.wantBody+"\n" {
				t.Fatalf("body=%q want=%q", got, tc.wantBody+"\\n")
			}
			if got := rr.Header().Get("X-Codex-Auxiliary"); got != "gitlab-sidecar" {
				t.Fatalf("X-Codex-Auxiliary=%q", got)
			}
		})
	}
}

func TestGitLabCodexWHAMAppsResponseInitialize(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/backend-api/wham/apps", bytes.NewBufferString(`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{}}`))
	payload := gitLabCodexWHAMAppsResponse(req)

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	text := string(body)
	for _, fragment := range []string{
		`"jsonrpc":"2.0"`,
		`"id":"init-1"`,
		`"protocolVersion":"2025-03-26"`,
		`"serverInfo":{"name":"gitlab-codex-sidecar","version":"0.1.0"}`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("expected %q in %s", fragment, text)
		}
	}
}
