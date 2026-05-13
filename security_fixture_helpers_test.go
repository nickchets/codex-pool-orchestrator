package main

import (
	"crypto/sha1"
	"encoding/base64"
	"strings"
	"testing"
)

func TestLowEntropyTestSecretUsesFixturePrefix(t *testing.T) {
	got := lowEntropyTestSecret(t)
	if !strings.HasPrefix(got, "test fixture secret ") {
		t.Fatalf("secret fixture prefix = %q", got)
	}
	if strings.Contains(got, "1234567890") || strings.Contains(got, "abcdef") {
		t.Fatalf("secret fixture should not embed synthetic high-entropy markers: %q", got)
	}
}

func TestValidTestWebSocketHandshakeKeyEncodesSixteenByteNonce(t *testing.T) {
	got := validTestWebSocketHandshakeKey(t, "fixture-one")
	nonce, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("handshake key is not valid base64: %v", err)
	}
	if len(nonce) != 16 {
		t.Fatalf("handshake nonce length = %d, want 16", len(nonce))
	}
}

func TestValidTestWebSocketHandshakeKeyVariesBySeed(t *testing.T) {
	first := validTestWebSocketHandshakeKey(t, "fixture-one")
	second := validTestWebSocketHandshakeKey(t, "fixture-two")
	if first == second {
		t.Fatalf("handshake keys should vary by seed")
	}
}

func lowEntropyTestSecret(t *testing.T) string {
	t.Helper()
	return lowEntropyTestSecretForName(t.Name())
}

func lowEntropyTestSecretForName(name string) string {
	cleaned := strings.NewReplacer("/", "-", " ", "-", "_", "-", ":", "-").Replace(strings.ToLower(name))
	return "test fixture secret " + cleaned
}

func validTestWebSocketHandshakeKey(t *testing.T, seed string) string {
	t.Helper()
	sum := sha1.Sum([]byte("websocket fixture key " + seed))
	return base64.StdEncoding.EncodeToString(sum[:16])
}
