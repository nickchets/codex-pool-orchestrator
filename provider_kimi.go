package main

import (
	"net/url"
)

// KimiProvider handles Kimi API accounts.
type KimiProvider struct {
	simpleAPIKeyProviderBase
}

// NewKimiProvider creates a new Kimi provider.
func NewKimiProvider(kimiBase *url.URL) *KimiProvider {
	return &KimiProvider{
		simpleAPIKeyProviderBase: newSimpleAPIKeyProviderBase(AccountTypeKimi, "kimi", kimiBase),
	}
}

func (p *KimiProvider) ParseUsage(obj map[string]any) *RequestUsage {
	if ru := parseOpenAIUsagePayload(obj); ru != nil {
		return ru
	}
	return parseAnthropicMessageUsage(obj)
}

var kimiModelRouter = newModelAliasRouter(map[string]string{
	"kimi-for-coding": "kimi-for-coding",
	"kimi":            "kimi",
})

// isKimiModel returns true if the given model name should be routed to Kimi.
func isKimiModel(model string) bool {
	return kimiModelRouter.matches(model)
}
