package main

import (
	"net/url"
)

// MinimaxProvider handles MiniMax API accounts.
type MinimaxProvider struct {
	simpleAPIKeyProviderBase
}

// NewMinimaxProvider creates a new MiniMax provider.
func NewMinimaxProvider(minimaxBase *url.URL) *MinimaxProvider {
	return &MinimaxProvider{
		simpleAPIKeyProviderBase: newSimpleAPIKeyProviderBase(AccountTypeMinimax, "minimax", minimaxBase),
	}
}

func (p *MinimaxProvider) ParseUsage(obj map[string]any) *RequestUsage {
	return parseAnthropicMessageUsage(obj)
}

var minimaxModelRouter = newModelAliasRouter(map[string]string{
	"minimax-m2.5": "MiniMax-M2.5",
	"minimax":      "MiniMax-M2.5",
})

// isMinimaxModel returns true if the given model name should be routed to MiniMax.
func isMinimaxModel(model string) bool {
	return minimaxModelRouter.matches(model)
}

// minimaxCanonicalModel returns the canonical upstream model name for a MiniMax alias.
func minimaxCanonicalModel(model string) string {
	return minimaxModelRouter.canonical(model)
}
