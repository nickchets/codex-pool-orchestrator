package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// simpleAPIKeyProviderBase covers providers that load one static api_key,
// use bearer auth, never refresh, and only participate in model-based routing.
type simpleAPIKeyProviderBase struct {
	accountType AccountType
	planType    string
	baseURL     *url.URL
}

type modelAliasRouter struct {
	aliases map[string]string
}

func newSimpleAPIKeyProviderBase(accountType AccountType, planType string, baseURL *url.URL) simpleAPIKeyProviderBase {
	return simpleAPIKeyProviderBase{
		accountType: accountType,
		planType:    planType,
		baseURL:     baseURL,
	}
}

func newModelAliasRouter(aliases map[string]string) modelAliasRouter {
	return modelAliasRouter{aliases: aliases}
}

func normalizeModelAliasKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func (r modelAliasRouter) matches(model string) bool {
	_, ok := r.aliases[normalizeModelAliasKey(model)]
	return ok
}

func (r modelAliasRouter) canonical(model string) string {
	if canonical, ok := r.aliases[normalizeModelAliasKey(model)]; ok {
		return canonical
	}
	return model
}

func (p simpleAPIKeyProviderBase) Type() AccountType {
	return p.accountType
}

func (p simpleAPIKeyProviderBase) LoadAccount(name, path string, data []byte) (*Account, error) {
	var payload struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(payload.APIKey) == "" {
		return nil, nil
	}

	return &Account{
		Type:        p.accountType,
		ID:          strings.TrimSuffix(name, filepath.Ext(name)),
		File:        path,
		AccessToken: strings.TrimSpace(payload.APIKey),
		PlanType:    p.planType,
	}, nil
}

func (p simpleAPIKeyProviderBase) SetAuthHeaders(req *http.Request, acc *Account) {
	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
}

func (p simpleAPIKeyProviderBase) RefreshToken(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	return nil
}

func (p simpleAPIKeyProviderBase) ParseUsageHeaders(acc *Account, headers http.Header) {
}

func (p simpleAPIKeyProviderBase) UpstreamURL(path string) *url.URL {
	return p.baseURL
}

func (p simpleAPIKeyProviderBase) MatchesPath(path string) bool {
	return false
}

func (p simpleAPIKeyProviderBase) NormalizePath(path string) string {
	return path
}

func (p simpleAPIKeyProviderBase) DetectsSSE(path string, contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

func parseAnthropicMessageUsage(obj map[string]any) *RequestUsage {
	switch eventType, _ := obj["type"].(string); eventType {
	case "message_delta":
		return parseAnthropicMessageDeltaUsage(obj)
	case "message_start":
		return parseAnthropicMessageStartUsage(obj)
	default:
		return nil
	}
}
