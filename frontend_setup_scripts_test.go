package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeCodexSetupScript_PowerShell(t *testing.T) {
	h := &proxyHandler{}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/setup/codex/testtoken?shell=powershell", nil)
	rr := httptest.NewRecorder()
	h.serveCodexSetupScript(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain*", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Set-StrictMode -Version Latest") {
		t.Fatalf("expected PowerShell script body, got:\n%s", body)
	}
	if !strings.Contains(body, "Join-Path $HOME '.codex'") {
		t.Fatalf("expected codex paths in script body, got:\n%s", body)
	}
	if !strings.Contains(body, "model_catalog_json = ") {
		t.Fatalf("expected model catalog config in script body, got:\n%s", body)
	}
	if !strings.Contains(body, "[mcp_servers.model_sync]") {
		t.Fatalf("expected MCP sidecar config in script body, got:\n%s", body)
	}
	if !strings.Contains(body, "model_sync.ps1") {
		t.Fatalf("expected MCP sidecar script install in PowerShell body, got:\n%s", body)
	}
	if !strings.Contains(body, "$firstLine = [Console]::In.ReadLine()") {
		t.Fatalf("expected MCP JSONL transport support in PowerShell body, got:\n%s", body)
	}
}

func TestServeCodexSetupScript_Bash(t *testing.T) {
	h := &proxyHandler{}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/setup/codex/testtoken", nil)
	rr := httptest.NewRecorder()
	h.serveCodexSetupScript(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Fatalf("Content-Type = %q, want text/x-shellscript*", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "model_sync.sh") {
		t.Fatalf("expected MCP sidecar script install in bash body, got:\n%s", body)
	}
	if !strings.Contains(body, "model_catalog_json = ") {
		t.Fatalf("expected model catalog config in bash script body, got:\n%s", body)
	}
	if !strings.Contains(body, "[mcp_servers.model_sync]") {
		t.Fatalf("expected MCP sidecar config in bash script body, got:\n%s", body)
	}
	if !strings.Contains(body, "MCP_TRANSPORT_MODE=\"jsonl\"") {
		t.Fatalf("expected MCP JSONL transport support in bash body, got:\n%s", body)
	}
}

func TestServeGeminiSetupScript_PowerShell(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	t.Setenv("POOL_JWT_SECRET", secret)

	tmpDir := t.TempDir()
	usersPath := filepath.Join(tmpDir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}

	user := &PoolUser{
		ID:        "user123",
		Token:     "tok123",
		Email:     "test@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	h := &proxyHandler{poolUsers: store}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/setup/gemini/tok123?shell=powershell", nil)
	rr := httptest.NewRecorder()
	h.serveGeminiSetupScript(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain*", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "$env:GEMINI_API_KEY = $GeminiApiKey") {
		t.Fatalf("expected PowerShell Gemini API key env setup in body, got:\n%s", body)
	}
	if !strings.Contains(body, "$env:GOOGLE_GEMINI_BASE_URL = $BaseUrl") {
		t.Fatalf("expected PowerShell Gemini base URL env setup in body, got:\n%s", body)
	}
	if !strings.Contains(body, "selectedType -Value 'gemini-api-key'") {
		t.Fatalf("expected PowerShell settings.json auth mode update in body, got:\n%s", body)
	}
	if !strings.Contains(body, "useExternal -Value $true") {
		t.Fatalf("expected PowerShell settings.json external auth update in body, got:\n%s", body)
	}
	if strings.Contains(body, "`") {
		t.Fatalf("PowerShell script should not contain backticks (Go raw string safety), got:\n%s", body)
	}
}

func TestServeOpenCodeSetupScript_PowerShell(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	t.Setenv("POOL_JWT_SECRET", secret)

	tmpDir := t.TempDir()
	usersPath := filepath.Join(tmpDir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}

	user := &PoolUser{
		ID:        "user789",
		Token:     "tok789",
		Email:     "opencode@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	h := &proxyHandler{poolUsers: store}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/setup/opencode/tok789?shell=powershell", nil)
	rr := httptest.NewRecorder()
	h.serveOpenCodeSetupScript(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain*", ct)
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Invoke-RestMethod -Uri $ConfigUrl -Method Get",
		"opencode.json",
		"antigravity-accounts.json",
		".codex-pool.bak",
		"Antigravity pool line via /v1",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected %q in PowerShell body, got:\n%s", fragment, body)
		}
	}
	if strings.Contains(body, "`") {
		t.Fatalf("PowerShell script should not contain backticks (Go raw string safety), got:\n%s", body)
	}
}

func TestServeOpenCodeSetupScript_Bash(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	t.Setenv("POOL_JWT_SECRET", secret)

	tmpDir := t.TempDir()
	usersPath := filepath.Join(tmpDir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}

	user := &PoolUser{
		ID:        "user789",
		Token:     "tok789",
		Email:     "opencode@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	h := &proxyHandler{poolUsers: store}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/setup/opencode/tok789", nil)
	rr := httptest.NewRecorder()
	h.serveOpenCodeSetupScript(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Fatalf("Content-Type = %q, want text/x-shellscript*", ct)
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"curl -fsSL \"$CONFIG_URL\" -o \"$TMP_JSON\"",
		"opencode.json",
		"antigravity-accounts.json",
		".codex-pool.bak",
		"OpenCode will use the Antigravity pool line via /v1.",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected %q in bash body, got:\n%s", fragment, body)
		}
	}
}

func TestServeGeminiSetupScript_Bash(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	t.Setenv("POOL_JWT_SECRET", secret)

	tmpDir := t.TempDir()
	usersPath := filepath.Join(tmpDir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}

	user := &PoolUser{
		ID:        "user123",
		Token:     "tok123",
		Email:     "test@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	h := &proxyHandler{poolUsers: store}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/setup/gemini/tok123", nil)
	rr := httptest.NewRecorder()
	h.serveGeminiSetupScript(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Fatalf("Content-Type = %q, want text/x-shellscript*", ct)
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"export GEMINI_API_KEY=",
		"export GOOGLE_GEMINI_BASE_URL=",
		"settings.security.auth.selectedType = 'gemini-api-key';",
		"settings.security.auth.useExternal = true;",
		"settings.codeAssistEndpoint = baseUrl;",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected %q in bash body, got:\n%s", fragment, body)
		}
	}
}

func TestServeClaudeSetupScript_PowerShell(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	t.Setenv("POOL_JWT_SECRET", secret)

	// Ensure env is not contaminated by user-specific settings during test runs.
	t.Setenv("PUBLIC_URL", "")

	tmpDir := t.TempDir()
	usersPath := filepath.Join(tmpDir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}

	user := &PoolUser{
		ID:        "user456",
		Token:     "tok456",
		Email:     "test2@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	h := &proxyHandler{poolUsers: store}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/setup/claude/tok456?shell=powershell", nil)
	rr := httptest.NewRecorder()
	h.serveClaudeSetupScript(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain*", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "$env:ANTHROPIC_BASE_URL = $BaseUrl") {
		t.Fatalf("expected PowerShell env setup in body, got:\n%s", body)
	}
	if !strings.Contains(body, "ConvertTo-Json -Depth 10") {
		t.Fatalf("expected PowerShell JSON update logic in body, got:\n%s", body)
	}
	if strings.Contains(body, "`") {
		t.Fatalf("PowerShell script should not contain backticks (Go raw string safety), got:\n%s", body)
	}
}

func TestServeFriendLanding_LocalTemplateIncludesCodexOAuthAction(t *testing.T) {
	setGeminiOAuthTestProfiles(t)

	h := &proxyHandler{}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	rr := httptest.NewRecorder()
	h.serveFriendLanding(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, fragment := range []string{
		"Local Operator Dashboard",
		"Dashboard-first operator surface",
		"Codex Dashboard",
		"Claude Dashboard",
		"Gemini Dashboard",
		"Provider truth, operational proof",
		"Best eligible is derived from the live Gemini rows below",
		"overview-quarantine-card",
		"overview-quarantine-detail",
		"Long-dead seats moved out of active rotation",
		"dead since",
		"Antigravity Gemini browser auth lands seats directly in the shared Gemini pool here",
		"Gemini CLI / OpenCode Setup",
		"Configures the Gemini CLI endpoint and points you to the same dashboard/operator flow used for seat onboarding",
		"OpenCode Recommended Path",
		"opencode run -m antigravity-manager/gemini-3.1-pro \"Reply with exactly OK.\"",
		"OpenCode Manual Config",
		"shared snippet intentionally does not",
		"per-user /setup/opencode/... URL",
		"transport aligned for OpenCode via this Gemini pool",
		"Start Antigravity Gemini Auth",
		"/operator/gemini/antigravity/oauth-start",
		"gemini_oauth_result",
		"python3 -m webbrowser",
		"Antigravity browser auth is the only supported Gemini seat onboarding flow for this pool.",
		"Fallback API Pool",
		"GitLab Claude Pool",
		"Start Codex OAuth",
		"/operator/codex/oauth-start",
		"/operator/codex/api-key-add",
		"/operator/claude/gitlab-token-add",
		"/operator/account-delete",
		"operator <code>codex-oauth-start</code> command",
		"Open OAuth Page",
		"fetch('/status?format=json', {",
		"'Accept': 'application/json'",
		"/status?format=json",
		"keeps the popup opener attached",
		"refreshes this page automatically when pool seat state changes",
		"Waiting for pool seat state to change...",
		"Waiting for pool seat state to change.",
		"providerLastUsedSeat(",
		"providerBestEligibleSeat(",
		"Fresh Routing / Total",
		"degraded-enabled",
		"Ready / Operational",
		"Restricted / Missing",
		"codex-oauth-result",
		"acc.provider_quota_summary",
		"providerTruth.project_id",
		"compatibility_lane",
		"gemini_pool",
		"auth_expires_at",
		"last_refresh_at",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q in local landing body", fragment)
		}
	}
	for _, forbidden := range []string{
		"/hero.png",
		"hero-art",
		"hero-wrapper",
		"/admin/codex/add",
		"/admin/accounts",
		"Downloads credentials and configures Gemini CLI endpoint",
		"open http://127.0.0.1:8989/status",
		"cp pool/gemini_ACCOUNT.json ~/.gemini/oauth_creds.json",
		"Import oauth_creds.json",
		"gemini-seat-json-input",
		"/operator/gemini/import-oauth-creds",
		"If you already have a real Gemini oauth_creds.json or Antigravity account JSON",
		"import it into the Gemini manual-import field on / or /status",
		"noopener noreferrer",
		"auth_expires_in || ''",
		"local_last_used || ''",
		"local_tokens || ''",
		"<download-token>",
		"run the pool on",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unexpected fragment %q in local landing body", forbidden)
		}
	}
}
