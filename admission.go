package main

import (
	"log"
	"net/http"
	"strings"
)

type proxyAdmissionKind string

const (
	proxyAdmissionPoolUser    proxyAdmissionKind = "pool_user"
	proxyAdmissionPassthrough proxyAdmissionKind = "passthrough"
	proxyAdmissionRejected    proxyAdmissionKind = "rejected"
)

type proxyAdmission struct {
	Kind         proxyAdmissionKind
	UserID       string
	ProviderType AccountType
	StatusCode   int
	Message      string
}

func rejectedProxyAdmission(statusCode int, message string) proxyAdmission {
	return proxyAdmission{
		Kind:       proxyAdmissionRejected,
		StatusCode: statusCode,
		Message:    message,
	}
}

func (h *proxyHandler) resolveProxyAdmission(r *http.Request, reqID string) proxyAdmission {
	authHeader := requestAuthHeader(r)
	secret := getPoolJWTSecret()

	if secret != "" {
		if admission, ok := h.resolvePoolUserAdmission(secret, authHeader, r, reqID); ok {
			return admission
		}
	}

	if isProviderCred, providerType := looksLikeProviderCredential(authHeader); isProviderCred {
		if h.cfg.debug {
			log.Printf("[%s] pass-through request with %s credential", reqID, providerType)
		}
		return proxyAdmission{
			Kind:         proxyAdmissionPassthrough,
			ProviderType: providerType,
		}
	}

	return rejectedProxyAdmission(http.StatusUnauthorized, "unauthorized: valid pool token required")
}

func (h *proxyHandler) resolvePoolUserAdmission(secret, authHeader string, r *http.Request, reqID string) (proxyAdmission, bool) {
	if isClaudePool, uid := isClaudePoolToken(secret, authHeader); isClaudePool {
		return h.admitPoolUser(uid, reqID, "claude pool user request"), true
	}

	geminiAPIKey := r.Header.Get("x-goog-api-key")
	if geminiAPIKey == "" {
		geminiAPIKey = r.URL.Query().Get("key")
	}
	if geminiAPIKey != "" {
		if isPoolKey, uid, _ := isPoolGeminiAPIKey(secret, geminiAPIKey); isPoolKey {
			return h.admitPoolUser(uid, reqID, "gemini api key pool user request"), true
		}
	}

	if isPoolUser, uid, _ := isPoolUserToken(secret, authHeader); isPoolUser {
		return h.admitPoolUser(uid, reqID, "pool user request"), true
	}

	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if isPoolToken, uid := isGeminiOAuthPoolToken(secret, token); isPoolToken {
			return h.admitPoolUser(uid, reqID, "gemini oauth pool user request"), true
		}
	}

	return proxyAdmission{}, false
}

func (h *proxyHandler) admitPoolUser(userID, reqID, debugMessage string) proxyAdmission {
	if h.poolUsers != nil {
		if user := h.poolUsers.Get(userID); user != nil && user.Disabled {
			return rejectedProxyAdmission(http.StatusForbidden, "pool user disabled")
		}
	}
	if h.cfg.debug {
		log.Printf("[%s] %s: user_id=%s", reqID, debugMessage, userID)
	}
	return proxyAdmission{
		Kind:   proxyAdmissionPoolUser,
		UserID: userID,
	}
}
