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

// KimiProvider handles Kimi API accounts.
type KimiProvider struct {
	kimiBase *url.URL
}

// NewKimiProvider creates a new Kimi provider.
func NewKimiProvider(kimiBase *url.URL) *KimiProvider {
	return &KimiProvider{
		kimiBase: kimiBase,
	}
}

func (p *KimiProvider) Type() AccountType {
	return AccountTypeKimi
}

// KimiAuthJSON is the format for Kimi auth files.
type KimiAuthJSON struct {
	APIKey string `json:"api_key"`
}

func (p *KimiProvider) LoadAccount(name, path string, data []byte) (*Account, error) {
	var kj KimiAuthJSON
	if err := json.Unmarshal(data, &kj); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if kj.APIKey == "" {
		return nil, nil
	}

	acc := &Account{
		Type:        AccountTypeKimi,
		ID:          strings.TrimSuffix(name, filepath.Ext(name)),
		File:        path,
		AccessToken: kj.APIKey,
		PlanType:    "kimi",
	}
	return acc, nil
}

func (p *KimiProvider) SetAuthHeaders(req *http.Request, acc *Account) {
	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
}

func (p *KimiProvider) RefreshToken(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	// API keys don't need refresh
	return nil
}

func (p *KimiProvider) ParseUsage(obj map[string]any) *RequestUsage {
	if ru := parseOpenAIUsagePayload(obj); ru != nil {
		return ru
	}

	eventType, _ := obj["type"].(string)
	if eventType == "message_delta" {
		return parseAnthropicMessageDeltaUsage(obj)
	}
	if eventType == "message_start" {
		return parseAnthropicMessageStartUsage(obj)
	}

	return nil
}

func (p *KimiProvider) ParseUsageHeaders(acc *Account, headers http.Header) {
	// No special header-based usage tracking for Kimi
}

func (p *KimiProvider) UpstreamURL(path string) *url.URL {
	return p.kimiBase
}

func (p *KimiProvider) MatchesPath(path string) bool {
	// Kimi is routed by model name, not by path.
	// It never wins path-based routing.
	return false
}

func (p *KimiProvider) NormalizePath(path string) string {
	return path
}

func (p *KimiProvider) DetectsSSE(path string, contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// kimiModels lists model names that should be routed to the Kimi provider.
var kimiModels = map[string]bool{
	"kimi-for-coding": true,
	"kimi":            true,
}

// isKimiModel returns true if the given model name should be routed to Kimi.
func isKimiModel(model string) bool {
	return kimiModels[strings.ToLower(strings.TrimSpace(model))]
}
