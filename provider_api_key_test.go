package main

import (
	"net/http"
	"net/url"
	"testing"
)

func TestKimiProviderLoadsAPIKeyAccount(t *testing.T) {
	baseURL, err := url.Parse("https://kimi.example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	provider := NewKimiProvider(baseURL)
	acc, err := provider.LoadAccount("kimi.json", "/tmp/kimi.json", []byte(`{"api_key":" kimi-token "}`))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account")
	}
	if acc.Type != AccountTypeKimi {
		t.Fatalf("type=%q", acc.Type)
	}
	if acc.PlanType != "kimi" {
		t.Fatalf("plan_type=%q", acc.PlanType)
	}
	if acc.ID != "kimi" {
		t.Fatalf("id=%q", acc.ID)
	}
	if acc.AccessToken != "kimi-token" {
		t.Fatalf("access_token=%q", acc.AccessToken)
	}
	if provider.UpstreamURL("/v1/responses").String() != baseURL.String() {
		t.Fatalf("upstream_url=%q", provider.UpstreamURL("/v1/responses"))
	}
}

func TestMinimaxProviderLoadsAPIKeyAccountAndUsesBearerAuth(t *testing.T) {
	baseURL, err := url.Parse("https://minimax.example.com")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	provider := NewMinimaxProvider(baseURL)
	acc, err := provider.LoadAccount("minimax.json", "/tmp/minimax.json", []byte(`{"api_key":"minimax-token"}`))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account")
	}
	if acc.Type != AccountTypeMinimax {
		t.Fatalf("type=%q", acc.Type)
	}
	if acc.PlanType != "minimax" {
		t.Fatalf("plan_type=%q", acc.PlanType)
	}

	req, err := http.NewRequest(http.MethodPost, "https://pool.example.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	provider.SetAuthHeaders(req, acc)
	if got := req.Header.Get("Authorization"); got != "Bearer minimax-token" {
		t.Fatalf("authorization=%q", got)
	}
}

func TestSimpleAPIKeyProvidersIgnoreEmptyKeys(t *testing.T) {
	kimi := NewKimiProvider(&url.URL{})
	acc, err := kimi.LoadAccount("empty.json", "/tmp/empty.json", []byte(`{"api_key":"   "}`))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc != nil {
		t.Fatalf("expected nil account, got %+v", acc)
	}
}

func TestModelAliasRouterMatchesTrimmedCaseInsensitiveAliases(t *testing.T) {
	router := newModelAliasRouter(map[string]string{
		"foo": "Foo",
	})

	if !router.matches("  FOO  ") {
		t.Fatal("expected alias match")
	}
	if router.matches("bar") {
		t.Fatal("unexpected alias match")
	}
}

func TestMinimaxCanonicalModelUsesAliasRouter(t *testing.T) {
	if got := minimaxCanonicalModel("  MINIMAX  "); got != "MiniMax-M2.5" {
		t.Fatalf("canonical=%q", got)
	}
	if got := minimaxCanonicalModel("custom-model"); got != "custom-model" {
		t.Fatalf("unexpected passthrough canonical=%q", got)
	}
}
