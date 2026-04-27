package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveProxyAdmissionClaudePoolUser(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), "claude-user"))

	h := &proxyHandler{}
	admission := h.resolveProxyAdmission(req, "req-1")

	if admission.Kind != AdmissionKindPoolUser {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindPoolUser)
	}
	if admission.UserID != "claude-user" {
		t.Fatalf("user_id = %q, want %q", admission.UserID, "claude-user")
	}
}

func TestResolveProxyAdmissionClaudePoolUserViaXAPIKey(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("X-Api-Key", generateClaudePoolToken(getPoolJWTSecret(), "claude-user"))

	h := &proxyHandler{}
	admission := h.resolveProxyAdmission(req, "req-x-api-key")

	if admission.Kind != AdmissionKindPoolUser {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindPoolUser)
	}
	if admission.UserID != "claude-user" {
		t.Fatalf("user_id = %q, want %q", admission.UserID, "claude-user")
	}
}

func TestResolveProxyAdmissionGeminiAPIKeyPoolUser(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	user := &PoolUser{ID: "gemini-user"}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1beta/models", nil)
	req.Header.Set("x-goog-api-key", generateGeminiAPIKey(getPoolJWTSecret(), user))

	h := &proxyHandler{}
	admission := h.resolveProxyAdmission(req, "req-2")

	if admission.Kind != AdmissionKindPoolUser {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindPoolUser)
	}
	if admission.UserID != user.ID {
		t.Fatalf("user_id = %q, want %q", admission.UserID, user.ID)
	}
}

func TestResolveProxyAdmissionPoolAPITokenAuthenticatesPoolUser(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	store, user, _ := newTestPoolUserStoreWithUser(t)
	raw, meta, err := store.CreateAPIToken(user.ID, "ci admission key")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/responses", nil)
	req.Header.Set("Authorization", "Bearer "+raw)

	h := &proxyHandler{poolUsers: store}
	admission := h.resolveProxyAdmission(req, "req-cpool")

	if admission.Kind != AdmissionKindPoolUser {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindPoolUser)
	}
	if admission.ProviderType != "" {
		t.Fatalf("provider_type = %q, want empty for managed pool admission", admission.ProviderType)
	}
	if admission.UserID != user.ID {
		t.Fatalf("user_id = %q, want %q", admission.UserID, user.ID)
	}
	if admission.TokenID != meta.ID {
		t.Fatalf("token_id = %q, want %q", admission.TokenID, meta.ID)
	}
	if admission.TokenName != meta.Name {
		t.Fatalf("token_name = %q, want %q", admission.TokenName, meta.Name)
	}
	if admission.CredentialKind != CredentialKindOpenAICompatiblePoolKey {
		t.Fatalf("credential_kind = %q, want %q", admission.CredentialKind, CredentialKindOpenAICompatiblePoolKey)
	}
}

func TestResolveProxyAdmissionPoolAPITokenDisabledTokenRejects(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	store, user, _ := newTestPoolUserStoreWithUser(t)
	raw, meta, err := store.CreateAPIToken(user.ID, "temporary admission key")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if err := store.DisableAPIToken(meta.ID); err != nil {
		t.Fatalf("disable api token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/responses", nil)
	req.Header.Set("Authorization", "Bearer "+raw)

	h := &proxyHandler{poolUsers: store}
	admission := h.resolveProxyAdmission(req, "req-cpool-disabled-token")

	if admission.Kind != AdmissionKindRejected {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindRejected)
	}
	if admission.StatusCode != http.StatusForbidden {
		t.Fatalf("status_code = %d, want %d", admission.StatusCode, http.StatusForbidden)
	}
}

func TestResolveProxyAdmissionPoolAPITokenDisabledUserRejects(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	store, user, _ := newTestPoolUserStoreWithUser(t)
	raw, _, err := store.CreateAPIToken(user.ID, "user disabled admission key")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if err := store.Disable(user.ID); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/responses", nil)
	req.Header.Set("Authorization", "Bearer "+raw)

	h := &proxyHandler{poolUsers: store}
	admission := h.resolveProxyAdmission(req, "req-cpool-disabled-user")

	if admission.Kind != AdmissionKindRejected {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindRejected)
	}
	if admission.StatusCode != http.StatusForbidden {
		t.Fatalf("status_code = %d, want %d", admission.StatusCode, http.StatusForbidden)
	}
}

func TestResolveProxyAdmissionPassthrough(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/responses", nil)
	req.Header.Set("Authorization", "Bearer sk-proj-realproviderkey")

	h := &proxyHandler{}
	admission := h.resolveProxyAdmission(req, "req-3")

	if admission.Kind != AdmissionKindPassthrough {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindPassthrough)
	}
	if admission.ProviderType != AccountTypeCodex {
		t.Fatalf("provider_type = %q, want %q", admission.ProviderType, AccountTypeCodex)
	}
}

func TestResolveProxyAdmissionDisabledPoolUser(t *testing.T) {
	t.Setenv("POOL_JWT_SECRET", "test-secret-0123456789abcdef0123456789abcdef")

	userID := "disabled-user"
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+generateClaudePoolToken(getPoolJWTSecret(), userID))

	h := &proxyHandler{
		poolUsers: &PoolUserStore{
			users: map[string]*PoolUser{
				userID: {ID: userID, Disabled: true},
			},
			byTok: map[string]*PoolUser{},
		},
	}
	admission := h.resolveProxyAdmission(req, "req-4")

	if admission.Kind != AdmissionKindRejected {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindRejected)
	}
	if admission.StatusCode != http.StatusForbidden {
		t.Fatalf("status_code = %d, want %d", admission.StatusCode, http.StatusForbidden)
	}
	if admission.Message != "pool user disabled" {
		t.Fatalf("message = %q, want %q", admission.Message, "pool user disabled")
	}
}

func TestResolveProxyAdmissionUnauthorized(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/responses", nil)

	h := &proxyHandler{}
	admission := h.resolveProxyAdmission(req, "req-5")

	if admission.Kind != AdmissionKindRejected {
		t.Fatalf("kind = %q, want %q", admission.Kind, AdmissionKindRejected)
	}
	if admission.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status_code = %d, want %d", admission.StatusCode, http.StatusUnauthorized)
	}
	if admission.Message != "unauthorized: valid pool token required" {
		t.Fatalf("message = %q, want %q", admission.Message, "unauthorized: valid pool token required")
	}
}
