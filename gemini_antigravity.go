package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type antigravityGeminiToken struct {
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	ExpiryTimestamp int64  `json:"expiry_timestamp"`
	TokenType       string `json:"token_type"`
	Email           string `json:"email"`
	ProjectID       string `json:"project_id"`
}

type antigravityGeminiAccount struct {
	ID                string                 `json:"id"`
	Email             string                 `json:"email"`
	Name              string                 `json:"name"`
	Disabled          bool                   `json:"disabled"`
	ProxyDisabled     bool                   `json:"proxy_disabled"`
	ValidationBlocked bool                   `json:"validation_blocked"`
	Token             antigravityGeminiToken `json:"token"`
	Quota             map[string]any         `json:"quota"`
}

func looksLikeAntigravityGeminiAccount(root map[string]any) bool {
	if root == nil {
		return false
	}
	if _, ok := root["token"].(map[string]any); ok {
		return true
	}
	if _, ok := root["access_token"]; ok {
		return false
	}
	for _, key := range []string{"proxy_disabled", "validation_blocked", "quota"} {
		if _, ok := root[key]; ok {
			return true
		}
	}
	return false
}

func normalizeAntigravityGeminiAccount(root map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}

	var account antigravityGeminiAccount
	if err := json.Unmarshal(raw, &account); err != nil {
		return nil, fmt.Errorf("parse antigravity account: %w", err)
	}
	if strings.TrimSpace(account.Token.AccessToken) == "" {
		return nil, fmt.Errorf("antigravity token.access_token is required")
	}
	if strings.TrimSpace(account.Token.RefreshToken) == "" {
		return nil, fmt.Errorf("antigravity token.refresh_token is required")
	}

	out := map[string]any{
		"access_token":       strings.TrimSpace(account.Token.AccessToken),
		"refresh_token":      strings.TrimSpace(account.Token.RefreshToken),
		"plan_type":          "gemini",
		"operator_source":    geminiOperatorSourceAntigravityImport,
		"oauth_profile_id":   geminiOAuthAntigravityProfileID,
		"antigravity_source": "antigravity_tools",
	}
	if tokenType := strings.TrimSpace(account.Token.TokenType); tokenType != "" {
		out["token_type"] = tokenType
	}
	if account.Token.ExpiryTimestamp > 0 {
		out["expiry_date"] = account.Token.ExpiryTimestamp * 1000
	}
	if email := firstNonEmpty(strings.TrimSpace(account.Email), strings.TrimSpace(account.Token.Email)); email != "" {
		out["operator_email"] = email
		out["antigravity_email"] = email
	}
	if name := strings.TrimSpace(account.Name); name != "" {
		out["operator_name"] = name
	}
	if account.Disabled {
		out["disabled"] = true
	}
	if id := strings.TrimSpace(account.ID); id != "" {
		out["antigravity_account_id"] = id
	}
	if name := strings.TrimSpace(account.Name); name != "" {
		out["antigravity_name"] = name
	}
	if projectID := strings.TrimSpace(account.Token.ProjectID); projectID != "" {
		out["antigravity_project_id"] = projectID
	}
	if account.ProxyDisabled {
		out["antigravity_proxy_disabled"] = true
	}
	if account.ValidationBlocked {
		out["antigravity_validation_blocked"] = true
	}
	if len(account.Quota) > 0 {
		out["antigravity_quota"] = account.Quota
	}
	if protectedModels := normalizeStringSliceFromAny(root["protected_models"]); len(protectedModels) > 0 {
		out["gemini_protected_models"] = protectedModels
	}
	if source, _ := root["antigravity_source"].(string); strings.TrimSpace(source) != "" {
		out["antigravity_source"] = strings.TrimSpace(source)
	}
	if profileID, _ := root["oauth_profile_id"].(string); strings.TrimSpace(profileID) != "" {
		out["oauth_profile_id"] = strings.TrimSpace(profileID)
	}
	if clientID, _ := root["client_id"].(string); strings.TrimSpace(clientID) != "" {
		out["client_id"] = strings.TrimSpace(clientID)
	}
	if clientSecret, _ := root["client_secret"].(string); strings.TrimSpace(clientSecret) != "" {
		out["client_secret"] = strings.TrimSpace(clientSecret)
	}
	if sourceFile, _ := root["antigravity_file"].(string); strings.TrimSpace(sourceFile) != "" {
		out["antigravity_file"] = strings.TrimSpace(sourceFile)
	}
	if current, ok := root["antigravity_current"].(bool); ok && current {
		out["antigravity_current"] = true
	}
	for _, key := range []string{
		"gemini_subscription_tier_id",
		"gemini_subscription_tier_name",
		"gemini_validation_reason_code",
		"gemini_validation_message",
		"gemini_validation_url",
		"gemini_provider_checked_at",
	} {
		if value, ok := root[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func normalizeGeminiImportPayload(payload, operatorSource string) (map[string]any, GeminiAuthJSON, string, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return nil, GeminiAuthJSON{}, "", fmt.Errorf("parse auth_json: %w", err)
	}
	if root == nil {
		return nil, GeminiAuthJSON{}, "", fmt.Errorf("auth_json must be a JSON object")
	}

	normalizedSource := operatorSource
	if looksLikeAntigravityGeminiAccount(root) {
		var err error
		root, err = normalizeAntigravityGeminiAccount(root)
		if err != nil {
			return nil, GeminiAuthJSON{}, "", err
		}
		normalizedSource = geminiOperatorSourceAntigravityImport
	}

	raw, err := json.Marshal(root)
	if err != nil {
		return nil, GeminiAuthJSON{}, "", err
	}
	var gj GeminiAuthJSON
	if err := json.Unmarshal(raw, &gj); err != nil {
		return nil, GeminiAuthJSON{}, "", fmt.Errorf("parse auth_json: %w", err)
	}
	if strings.TrimSpace(gj.AccessToken) == "" {
		return nil, GeminiAuthJSON{}, "", fmt.Errorf("gemini access_token is required")
	}
	if strings.TrimSpace(gj.RefreshToken) == "" {
		return nil, GeminiAuthJSON{}, "", fmt.Errorf("gemini refresh_token is required")
	}
	return root, gj, normalizedSource, nil
}

func antigravityQuotaDisposition(quota map[string]any) (bool, string) {
	if len(quota) == 0 {
		return false, ""
	}
	forbidden, _ := quota["is_forbidden"].(bool)
	reason, _ := quota["forbidden_reason"].(string)
	return forbidden, strings.TrimSpace(reason)
}
