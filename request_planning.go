package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

type AdmissionKind string

const (
	AdmissionKindPoolUser    AdmissionKind = "pool_user"
	AdmissionKindPassthrough AdmissionKind = "passthrough"
	AdmissionKindRejected    AdmissionKind = "rejected"
)

type AdmissionResult struct {
	Kind         AdmissionKind
	UserID       string
	ProviderType AccountType
	StatusCode   int
	Message      string
}

type RequestShape struct {
	Path           string
	ConversationID string
	RequestedModel string
}

type RoutePlan struct {
	Admission    AdmissionResult
	Shape        RequestShape
	Provider     Provider
	TargetBase   *url.URL
	AccountType  AccountType
	RequiredPlan string
}

func rejectedAdmission(statusCode int, message string) AdmissionResult {
	return AdmissionResult{
		Kind:       AdmissionKindRejected,
		StatusCode: statusCode,
		Message:    message,
	}
}

func (h *proxyHandler) resolveProxyAdmission(r *http.Request, reqID string) AdmissionResult {
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
		return AdmissionResult{
			Kind:         AdmissionKindPassthrough,
			ProviderType: providerType,
		}
	}

	return rejectedAdmission(http.StatusUnauthorized, "unauthorized: valid pool token required")
}

func (h *proxyHandler) resolvePoolUserAdmission(secret, authHeader string, r *http.Request, reqID string) (AdmissionResult, bool) {
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

	return AdmissionResult{}, false
}

func (h *proxyHandler) admitPoolUser(userID, reqID, debugMessage string) AdmissionResult {
	if h.poolUsers != nil {
		if user := h.poolUsers.Get(userID); user != nil && user.Disabled {
			return rejectedAdmission(http.StatusForbidden, "pool user disabled")
		}
	}
	if h.cfg.debug {
		log.Printf("[%s] %s: user_id=%s", reqID, debugMessage, userID)
	}
	return AdmissionResult{
		Kind:   AdmissionKindPoolUser,
		UserID: userID,
	}
}

func buildBufferedRequestShape(r *http.Request, bodyBytes, bodySample []byte) RequestShape {
	inspect := bodyBytes
	if len(inspect) == 0 {
		inspect = bodySample
	}
	inspect = bodyForInspection(r, inspect)
	return RequestShape{
		Path:           r.URL.Path,
		ConversationID: extractConversationIDFromJSON(inspect),
		RequestedModel: extractRequestedModelFromJSON(inspect),
	}
}

func requestConversationIDFromSessionInputs(r *http.Request) string {
	conversationID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if conversationID == "" {
		conversationID = extractConversationIDFromHeaders(r.Header)
	}
	return conversationID
}

func buildStreamedRequestShape(r *http.Request) RequestShape {
	return RequestShape{
		Path:           r.URL.Path,
		ConversationID: requestConversationIDFromSessionInputs(r),
	}
}

func buildWebSocketRequestShape(r *http.Request) RequestShape {
	return RequestShape{
		Path:           r.URL.Path,
		ConversationID: requestConversationIDFromSessionInputs(r),
	}
}

func (h *proxyHandler) planRoute(admission AdmissionResult, r *http.Request, shape RequestShape, bodyBytes []byte) (RoutePlan, []byte, error) {
	provider, targetBase := h.pickUpstream(shape.Path, r.Header)
	if provider == nil || targetBase == nil {
		return RoutePlan{}, nil, fmt.Errorf("no upstream for path")
	}

	accountType := provider.Type()
	rewrittenBody := bodyBytes
	if shape.RequestedModel != "" {
		if overrideProvider, overrideBase, overrideBody := h.modelRouteOverride(shape.RequestedModel, bodyBytes); overrideProvider != nil {
			provider = overrideProvider
			targetBase = overrideBase
			accountType = overrideProvider.Type()
			if overrideBody != nil {
				rewrittenBody = overrideBody
			}
		}
	}

	requiredPlan := ""
	if accountType == AccountTypeCodex && modelRequiresCodexPro(shape.RequestedModel) {
		requiredPlan = "pro"
	}

	return RoutePlan{
		Admission:    admission,
		Shape:        shape,
		Provider:     provider,
		TargetBase:   targetBase,
		AccountType:  accountType,
		RequiredPlan: requiredPlan,
	}, rewrittenBody, nil
}
