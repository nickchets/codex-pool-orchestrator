package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHandlePoolUserCreateIncludesGeminiAndOpenCodeSetupURLs(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	store, err := newPoolUserStore(filepath.Join(t.TempDir(), "pool_users.json"))
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}

	h := &proxyHandler{poolUsers: store}
	req := httptest.NewRequest(http.MethodPost, "http://pool.local/admin/pool-users/", bytes.NewBufferString(`{"email":"pool@example.com","plan_type":"pro"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.handlePoolUsersCreate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	token, _ := payload["token"].(string)
	if token == "" {
		t.Fatalf("token missing from response: %v", payload)
	}
	setup, _ := payload["setup"].(map[string]any)
	if got, _ := setup["gemini_setup"].(string); got != "http://pool.local/setup/gemini/"+token {
		t.Fatalf("gemini_setup=%q", got)
	}
	if got, _ := setup["gemini_config"].(string); got != "http://pool.local/config/gemini/"+token {
		t.Fatalf("gemini_config=%q", got)
	}
	if got, _ := setup["opencode_setup"].(string); got != "http://pool.local/setup/opencode/"+token {
		t.Fatalf("opencode_setup=%q", got)
	}
	if got, _ := setup["opencode_config"].(string); got != "http://pool.local/config/opencode/"+token {
		t.Fatalf("opencode_config=%q", got)
	}
}
