package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newAdminPoolUserAPIKeysTestHandler(t *testing.T) (*proxyHandler, *PoolUser) {
	t.Helper()
	store, err := newPoolUserStore(filepath.Join(t.TempDir(), "pool_users.json"))
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}
	user := &PoolUser{
		ID:        "pool-user-admin-api-keys",
		Token:     "download-token-admin-api-keys",
		Email:     "keys@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	h := &proxyHandler{
		cfg:       config{adminToken: "secret"},
		pool:      newPoolState(nil, false),
		poolUsers: store,
		startTime: time.Now().Add(-time.Minute),
	}
	return h, user
}

func decodeJSONMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	return payload
}

func TestAdminPoolUserAPIKeysRequireAdminOrLocalOperatorAuth(t *testing.T) {
	h, user := newAdminPoolUserAPIKeysTestHandler(t)

	paths := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/admin/pool-users/" + user.ID + "/api-keys"},
		{method: http.MethodPost, path: "/admin/pool-users/" + user.ID + "/api-keys", body: `{"name":"ci"}`},
		{method: http.MethodDelete, path: "/admin/pool-users/" + user.ID + "/api-keys/not-real"},
	}

	for _, tc := range paths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://example.com"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAdminPoolUserAPIKeysCreateListDisableWithAdminToken(t *testing.T) {
	h, user := newAdminPoolUserAPIKeysTestHandler(t)

	createReq := httptest.NewRequest(http.MethodPost, "http://example.com/admin/pool-users/"+user.ID+"/api-keys", strings.NewReader(`{"name":"ci key","allowed_models":["gpt-5-codex"],"max_rpm":120}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Admin-Token", "secret")
	createRR := httptest.NewRecorder()
	h.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated && createRR.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createRR.Code, createRR.Body.String())
	}
	createPayload := decodeJSONMap(t, createRR)
	rawKey, _ := createPayload["api_key"].(string)
	if !strings.HasPrefix(rawKey, "sk-cpool-") {
		t.Fatalf("api_key=%q", rawKey)
	}
	if strings.Count(createRR.Body.String(), rawKey) != 1 {
		t.Fatalf("raw key should appear exactly once in create response: %s", createRR.Body.String())
	}
	if warning, _ := createPayload["warning"].(string); !strings.Contains(strings.ToLower(warning), "only shown once") {
		t.Fatalf("warning=%q", warning)
	}
	keyMeta, _ := createPayload["key"].(map[string]any)
	tokenID, _ := keyMeta["id"].(string)
	if tokenID == "" {
		t.Fatalf("created key metadata missing id: %#v", createPayload)
	}
	if keyMeta["hash"] != nil {
		t.Fatalf("created key metadata should not expose hash: %#v", keyMeta)
	}
	policy, _ := keyMeta["policy"].(map[string]any)
	if got := int(policy["max_rpm"].(float64)); got != 120 {
		t.Fatalf("policy.max_rpm=%d", got)
	}

	admissionReq := httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/completions", nil)
	admissionReq.Header.Set("Authorization", "Bearer "+rawKey)
	beforeDisable := h.resolveProxyAdmission(admissionReq, "req-before-disable")
	if beforeDisable.Kind != AdmissionKindPoolUser || beforeDisable.UserID != user.ID || beforeDisable.TokenID != tokenID {
		t.Fatalf("admission before disable=%+v", beforeDisable)
	}

	listReq := httptest.NewRequest(http.MethodGet, "http://example.com/admin/pool-users/"+user.ID+"/api-keys", nil)
	listReq.Header.Set("X-Admin-Token", "secret")
	listRR := httptest.NewRecorder()
	h.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRR.Code, listRR.Body.String())
	}
	listBody := listRR.Body.String()
	if strings.Contains(listBody, rawKey) || strings.Contains(listBody, strings.TrimPrefix(rawKey, keyMeta["prefix"].(string)+".")) {
		t.Fatalf("list response leaked raw key material: %s", listBody)
	}
	if strings.Contains(listBody, `"hash"`) {
		t.Fatalf("list response leaked token hash: %s", listBody)
	}
	listPayload := decodeJSONMap(t, listRR)
	keys, _ := listPayload["api_keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("api_keys=%#v", listPayload["api_keys"])
	}
	listedKey, _ := keys[0].(map[string]any)
	for _, field := range []string{"id", "name", "prefix", "last4", "created_at", "last_used_at", "disabled", "policy"} {
		if _, ok := listedKey[field]; !ok {
			t.Fatalf("listed key missing %s: %#v", field, listedKey)
		}
	}

	disableReq := httptest.NewRequest(http.MethodPost, "http://example.com/admin/pool-users/"+user.ID+"/api-keys/"+tokenID+"/disable", nil)
	disableReq.Header.Set("X-Admin-Token", "secret")
	disableRR := httptest.NewRecorder()
	h.ServeHTTP(disableRR, disableReq)
	if disableRR.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disableRR.Code, disableRR.Body.String())
	}

	afterDisable := h.resolveProxyAdmission(admissionReq, "req-after-disable")
	if afterDisable.Kind != AdmissionKindRejected || afterDisable.StatusCode != http.StatusForbidden {
		t.Fatalf("admission after disable=%+v", afterDisable)
	}

	listAfterDisableReq := httptest.NewRequest(http.MethodGet, "http://example.com/admin/pool-users/"+user.ID+"/api-keys", nil)
	listAfterDisableReq.Header.Set("X-Admin-Token", "secret")
	listAfterDisableRR := httptest.NewRecorder()
	h.ServeHTTP(listAfterDisableRR, listAfterDisableReq)
	if listAfterDisableRR.Code != http.StatusOK {
		t.Fatalf("list after disable status=%d body=%s", listAfterDisableRR.Code, listAfterDisableRR.Body.String())
	}
	listAfterDisablePayload := decodeJSONMap(t, listAfterDisableRR)
	keysAfterDisable, _ := listAfterDisablePayload["api_keys"].([]any)
	if len(keysAfterDisable) != 1 {
		t.Fatalf("api_keys after disable=%#v", keysAfterDisable)
	}
	disabledKey, _ := keysAfterDisable[0].(map[string]any)
	if disabled, _ := disabledKey["disabled"].(bool); !disabled {
		t.Fatalf("disabled key metadata=%#v", disabledKey)
	}
}

func TestAdminPoolUserAPIKeysAllowTrustedLocalOperator(t *testing.T) {
	h, user := newAdminPoolUserAPIKeysTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8989/admin/pool-users/"+user.ID+"/api-keys", strings.NewReader(`{"name":"local"}`))
	req.Host = "127.0.0.1:8989"
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("local operator create status=%d body=%s", rr.Code, rr.Body.String())
	}
	createPayload := decodeJSONMap(t, rr)
	keyMeta, _ := createPayload["key"].(map[string]any)
	tokenID, _ := keyMeta["id"].(string)
	if tokenID == "" {
		t.Fatalf("missing token id in response: %#v", createPayload)
	}

	listReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/admin/pool-users/"+user.ID+"/api-keys", nil)
	listReq.Host = "127.0.0.1:8989"
	listReq.RemoteAddr = "127.0.0.1:4242"
	listRR := httptest.NewRecorder()
	h.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("local operator list status=%d body=%s", listRR.Code, listRR.Body.String())
	}

	disableReq := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:8989/admin/pool-users/"+user.ID+"/api-keys/"+tokenID, nil)
	disableReq.Host = "127.0.0.1:8989"
	disableReq.RemoteAddr = "127.0.0.1:4242"
	disableRR := httptest.NewRecorder()
	h.ServeHTTP(disableRR, disableReq)
	if disableRR.Code != http.StatusOK {
		t.Fatalf("local operator disable status=%d body=%s", disableRR.Code, disableRR.Body.String())
	}
}

func TestAdminPoolUserAPIKeysStatusAndDashboardDoNotLeakRawKey(t *testing.T) {
	h, user := newAdminPoolUserAPIKeysTestHandler(t)

	createReq := httptest.NewRequest(http.MethodPost, "http://example.com/admin/pool-users/"+user.ID+"/api-keys", strings.NewReader(`{"name":"leak check"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Admin-Token", "secret")
	createRR := httptest.NewRecorder()
	h.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated && createRR.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createRR.Code, createRR.Body.String())
	}
	createPayload := decodeJSONMap(t, createRR)
	rawKey, _ := createPayload["api_key"].(string)
	keyMeta, _ := createPayload["key"].(map[string]any)
	secretPart := strings.TrimPrefix(rawKey, keyMeta["prefix"].(string)+".")

	statusReq := httptest.NewRequest(http.MethodGet, "http://example.com/status?format=json", nil)
	statusReq.Header.Set("Accept", "application/json")
	statusRR := httptest.NewRecorder()
	h.serveStatusPage(statusRR, statusReq)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status page status=%d body=%s", statusRR.Code, statusRR.Body.String())
	}
	if strings.Contains(statusRR.Body.String(), rawKey) || strings.Contains(statusRR.Body.String(), secretPart) {
		t.Fatalf("status json leaked raw key material: %s", statusRR.Body.String())
	}

	dashboardReq := httptest.NewRequest(http.MethodGet, "http://example.com/admin/pool/dashboard", nil)
	dashboardReq.Header.Set("X-Admin-Token", "secret")
	dashboardRR := httptest.NewRecorder()
	h.ServeHTTP(dashboardRR, dashboardReq)
	if dashboardRR.Code != http.StatusOK {
		t.Fatalf("dashboard status=%d body=%s", dashboardRR.Code, dashboardRR.Body.String())
	}
	if strings.Contains(dashboardRR.Body.String(), rawKey) || strings.Contains(dashboardRR.Body.String(), secretPart) {
		t.Fatalf("dashboard json leaked raw key material: %s", dashboardRR.Body.String())
	}
}

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
	if got, _ := setup["clcode_setup"].(string); got != "http://pool.local/setup/clcode/"+token {
		t.Fatalf("clcode_setup=%q", got)
	}
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
