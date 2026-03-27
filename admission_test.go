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

func TestResolveProxyAdmissionPassthrough(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/responses", nil)
	req.Header.Set("Authorization", "Bearer sk-proj-test-passthrough")

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
