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

// MinimaxProvider handles MiniMax API accounts.
type MinimaxProvider struct {
	minimaxBase *url.URL
}

// NewMinimaxProvider creates a new MiniMax provider.
func NewMinimaxProvider(minimaxBase *url.URL) *MinimaxProvider {
	return &MinimaxProvider{
		minimaxBase: minimaxBase,
	}
}

func (p *MinimaxProvider) Type() AccountType {
	return AccountTypeMinimax
}

// MinimaxAuthJSON is the format for MiniMax auth files.
type MinimaxAuthJSON struct {
	APIKey string `json:"api_key"`
}

func (p *MinimaxProvider) LoadAccount(name, path string, data []byte) (*Account, error) {
	var mj MinimaxAuthJSON
	if err := json.Unmarshal(data, &mj); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if mj.APIKey == "" {
		return nil, nil
	}

	acc := &Account{
		Type:        AccountTypeMinimax,
		ID:          strings.TrimSuffix(name, filepath.Ext(name)),
		File:        path,
		AccessToken: mj.APIKey,
		PlanType:    "minimax",
	}
	return acc, nil
}

func (p *MinimaxProvider) SetAuthHeaders(req *http.Request, acc *Account) {
	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
}

func (p *MinimaxProvider) RefreshToken(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	return nil
}

func (p *MinimaxProvider) ParseUsage(obj map[string]any) *RequestUsage {
	eventType, _ := obj["type"].(string)

	if eventType == "message_delta" {
		return parseAnthropicMessageDeltaUsage(obj)
	}

	if eventType == "message_start" {
		return parseAnthropicMessageStartUsage(obj)
	}

	return nil
}

func (p *MinimaxProvider) ParseUsageHeaders(acc *Account, headers http.Header) {
}

func (p *MinimaxProvider) UpstreamURL(path string) *url.URL {
	return p.minimaxBase
}

func (p *MinimaxProvider) MatchesPath(path string) bool {
	// MiniMax is routed by model name, not by path.
	return false
}

func (p *MinimaxProvider) NormalizePath(path string) string {
	return path
}

func (p *MinimaxProvider) DetectsSSE(path string, contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// minimaxModels maps request model names to the canonical model name sent upstream.
var minimaxModels = map[string]string{
	"minimax-m2.5": "MiniMax-M2.5",
	"minimax":      "MiniMax-M2.5",
}

// isMinimaxModel returns true if the given model name should be routed to MiniMax.
func isMinimaxModel(model string) bool {
	_, ok := minimaxModels[strings.ToLower(strings.TrimSpace(model))]
	return ok
}

// minimaxCanonicalModel returns the canonical upstream model name for a MiniMax alias.
func minimaxCanonicalModel(model string) string {
	if canonical, ok := minimaxModels[strings.ToLower(strings.TrimSpace(model))]; ok {
		return canonical
	}
	return model
}
