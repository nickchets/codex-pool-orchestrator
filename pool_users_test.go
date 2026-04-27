package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSignAndValidateJWT(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	claims := map[string]any{
		"exp": time.Now().Add(1 * time.Hour).Unix(),
		"iss": "https://auth.openai.com",
		"sub": "pool|test123",
	}

	token, err := signJWT(secret, claims)
	if err != nil {
		t.Fatalf("signJWT failed: %v", err)
	}

	// Validate the token
	validated, err := validatePoolUserJWT(secret, token)
	if err != nil {
		t.Fatalf("validatePoolUserJWT failed: %v", err)
	}

	if validated["iss"] != "https://auth.openai.com" {
		t.Errorf("expected iss=https://auth.openai.com, got %v", validated["iss"])
	}
	if validated["sub"] != "pool|test123" {
		t.Errorf("expected sub=pool|test123, got %v", validated["sub"])
	}
}

func TestValidateJWTWrongSecret(t *testing.T) {
	claims := map[string]any{
		"exp": time.Now().Add(1 * time.Hour).Unix(),
		"iss": "https://auth.openai.com",
	}

	token, _ := signJWT("secret1", claims)
	_, err := validatePoolUserJWT("secret2", token)
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}

func TestValidateExpiredJWT(t *testing.T) {
	secret := "test-secret"
	claims := map[string]any{
		"exp": time.Now().Add(-1 * time.Hour).Unix(), // Expired
		"iss": "https://auth.openai.com",
	}

	token, _ := signJWT(secret, claims)
	_, err := validatePoolUserJWT(secret, token)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestIsPoolUserToken(t *testing.T) {
	secret := "test-secret-key"
	claims := map[string]any{
		"exp": time.Now().Add(1 * time.Hour).Unix(),
		"iss": "https://auth.openai.com",
		"sub": "pool|abc123",
	}

	token, _ := signJWT(secret, claims)
	authHeader := "Bearer " + token

	isPool, userID, err := isPoolUserToken(secret, authHeader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isPool {
		t.Error("expected isPool=true")
	}
	if userID != "abc123" {
		t.Errorf("expected userID=abc123, got %s", userID)
	}
}

func TestIsPoolUserTokenWrongIssuer(t *testing.T) {
	secret := "test-secret-key"
	claims := map[string]any{
		"exp": time.Now().Add(1 * time.Hour).Unix(),
		"iss": "https://example.com", // Wrong issuer (not https://auth.openai.com)
		"sub": "pool|abc123",
	}

	token, _ := signJWT(secret, claims)
	authHeader := "Bearer " + token

	isPool, _, _ := isPoolUserToken(secret, authHeader)
	if isPool {
		t.Error("expected isPool=false for wrong issuer")
	}
}

func newTestPoolUserStoreWithUser(t *testing.T) (*PoolUserStore, *PoolUser, string) {
	t.Helper()
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}
	user := &PoolUser{
		ID:        "user1234567890abcdef",
		Token:     "download-token",
		Email:     "pool@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return store, user, dir
}

func TestPoolAPITokenGenerateValidateAndPersistHashOnly(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}

	user := &PoolUser{
		ID:        "user1234567890abcdef",
		Token:     "download-token",
		Email:     "pool@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	raw, meta, err := store.CreateAPIToken(user.ID, "ci key")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if meta == nil {
		t.Fatal("expected token metadata")
	}
	if meta.UserID != user.ID {
		t.Fatalf("token user_id = %q, want %q", meta.UserID, user.ID)
	}
	if meta.Name != "ci key" {
		t.Fatalf("token name = %q", meta.Name)
	}
	if meta.ID == "" || meta.KID == "" || meta.Hash == "" {
		t.Fatalf("expected id/kid/hash to be populated: %#v", meta)
	}
	if meta.Disabled {
		t.Fatal("new token should not be disabled")
	}
	if meta.LastUsedAt != nil {
		t.Fatal("new token should not have last_used_at before validation")
	}
	if !strings.HasPrefix(raw, "sk-cpool-"+meta.KID+".") {
		t.Fatalf("raw token did not have expected sk-cpool format")
	}
	if meta.Prefix == "" || !strings.HasPrefix(raw, meta.Prefix) {
		t.Fatalf("token prefix metadata is not a prefix of the raw token")
	}
	if meta.Last4 != raw[len(raw)-4:] {
		t.Fatalf("token last4 metadata mismatch")
	}

	validated, validatedUser, err := store.ValidateAPIToken(raw)
	if err != nil {
		t.Fatalf("validate api token: %v", err)
	}
	if validated == nil || validated.ID != meta.ID {
		t.Fatalf("validated token = %#v, want id %q", validated, meta.ID)
	}
	if validatedUser == nil || validatedUser.ID != user.ID {
		t.Fatalf("validated user = %#v, want %q", validatedUser, user.ID)
	}
	if validated.LastUsedAt == nil {
		t.Fatal("expected validation to set last_used_at")
	}

	listed := store.ListAPITokens(user.ID)
	if len(listed) != 1 || listed[0].ID != meta.ID {
		t.Fatalf("listed tokens = %#v, want token %q", listed, meta.ID)
	}

	tokenPath := filepath.Join(dir, "pool_users_api_tokens.json")
	tokenFile, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if strings.Contains(string(tokenFile), raw) {
		t.Fatal("persisted token file contains the raw API token")
	}
	secretPart := strings.TrimPrefix(raw, "sk-cpool-"+meta.KID+".")
	if secretPart == "" || strings.Contains(string(tokenFile), secretPart) {
		t.Fatal("persisted token file contains the raw API token secret")
	}
	if !strings.Contains(string(tokenFile), `"hash"`) || !strings.Contains(string(tokenFile), meta.Hash) {
		t.Fatal("persisted token file does not contain the token hash")
	}

	reloaded, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	reloadedToken, reloadedUser, err := reloaded.ValidateAPIToken(raw)
	if err != nil {
		t.Fatalf("validate reloaded api token: %v", err)
	}
	if reloadedToken == nil || reloadedToken.ID != meta.ID || reloadedUser == nil || reloadedUser.ID != user.ID {
		t.Fatalf("reloaded validation got token=%#v user=%#v", reloadedToken, reloadedUser)
	}
}

func TestPoolAPITokenValidateNormalizesBearerAndWhitespace(t *testing.T) {
	store, user, _ := newTestPoolUserStoreWithUser(t)
	raw, meta, err := store.CreateAPIToken(user.ID, "normalization")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}

	tests := []struct {
		name  string
		input string
	}{
		{name: "bearer", input: "Bearer " + raw},
		{name: "whitespace", input: " \t" + raw + "\n"},
		{name: "bearer_with_whitespace", input: " \tBearer  " + raw + " \n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validated, validatedUser, err := store.ValidateAPIToken(tt.input)
			if err != nil {
				t.Fatalf("validate api token %q: %v", tt.input, err)
			}
			if validated == nil || validated.ID != meta.ID {
				t.Fatalf("validated token = %#v, want id %q", validated, meta.ID)
			}
			if validatedUser == nil || validatedUser.ID != user.ID {
				t.Fatalf("validated user = %#v, want %q", validatedUser, user.ID)
			}
		})
	}
}

func TestPoolAPITokenValidateSucceedsWhenLastUsedPersistenceFails(t *testing.T) {
	store, user, dir := newTestPoolUserStoreWithUser(t)
	raw, meta, err := store.CreateAPIToken(user.ID, "best effort")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}

	// Point token persistence at a directory so updating last_used_at cannot be written.
	store.apiTokenPath = dir
	validated, validatedUser, err := store.ValidateAPIToken(raw)
	if err != nil {
		t.Fatalf("valid api token should authenticate despite best-effort last_used_at persistence failure: %v", err)
	}
	if validated == nil || validated.ID != meta.ID {
		t.Fatalf("validated token = %#v, want id %q", validated, meta.ID)
	}
	if validatedUser == nil || validatedUser.ID != user.ID {
		t.Fatalf("validated user = %#v, want %q", validatedUser, user.ID)
	}
	if validated.LastUsedAt == nil {
		t.Fatal("expected validation to still update in-memory last_used_at")
	}
}

func TestPoolAPITokenMetadataReturnsCopies(t *testing.T) {
	store, user, _ := newTestPoolUserStoreWithUser(t)
	raw, meta, err := store.CreateAPIToken(user.ID, "copy check")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}

	meta.Name = "mutated create return"
	listed := store.ListAPITokens(user.ID)
	if len(listed) != 1 {
		t.Fatalf("listed tokens = %#v, want one token", listed)
	}
	if listed[0].Name != "copy check" {
		t.Fatalf("mutating CreateAPIToken metadata changed store: got name %q", listed[0].Name)
	}

	listed[0].Name = "mutated list return"
	again := store.ListAPITokens(user.ID)
	if len(again) != 1 || again[0].Name != "copy check" {
		t.Fatalf("mutating ListAPITokens metadata changed store: %#v", again)
	}

	validated, _, err := store.ValidateAPIToken(raw)
	if err != nil {
		t.Fatalf("validate api token: %v", err)
	}
	validated.Disabled = true
	validated.Name = "mutated validate return"
	validatedAgain, _, err := store.ValidateAPIToken(raw)
	if err != nil {
		t.Fatalf("mutating ValidateAPIToken metadata should not disable stored token: %v", err)
	}
	if validatedAgain.Disabled || validatedAgain.Name != "copy check" {
		t.Fatalf("mutating ValidateAPIToken metadata changed store: %#v", validatedAgain)
	}
}

type failingRandomReader struct{}

func (failingRandomReader) Read([]byte) (int, error) {
	return 0, errors.New("csprng unavailable")
}

func TestRandomHexFromReaderFailsClosed(t *testing.T) {
	got, err := randomHexFromReader(failingRandomReader{}, 8)
	if err == nil {
		t.Fatal("expected error from failing random reader")
	}
	if got != "" {
		t.Fatalf("expected no random output on failure, got %q", got)
	}
}

func TestPoolAPITokenDisabledRejects(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "pool_users.json")
	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}
	user := &PoolUser{ID: "user1234567890abcdef", Token: "download-token", Email: "pool@example.com", PlanType: "pro", CreatedAt: time.Now()}
	if err := store.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	raw, meta, err := store.CreateAPIToken(user.ID, "temporary")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if err := store.DisableAPIToken(meta.ID); err != nil {
		t.Fatalf("disable api token: %v", err)
	}
	if tok, gotUser, err := store.ValidateAPIToken(raw); err == nil || tok != nil || gotUser != nil {
		t.Fatalf("disabled token validation = token %#v user %#v err %v, want rejection", tok, gotUser, err)
	}

	reloaded, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if tok, gotUser, err := reloaded.ValidateAPIToken(raw); err == nil || tok != nil || gotUser != nil {
		t.Fatalf("disabled reloaded token validation = token %#v user %#v err %v, want rejection", tok, gotUser, err)
	}
}

func TestPoolAPITokenUnknownRejects(t *testing.T) {
	store, err := newPoolUserStore(filepath.Join(t.TempDir(), "pool_users.json"))
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}
	unknown := "sk-" + "cpool-" + "unknownkid" + "." + "unknownsecret"
	if tok, user, err := store.ValidateAPIToken(unknown); err == nil || tok != nil || user != nil {
		t.Fatalf("unknown token validation = token %#v user %#v err %v, want rejection", tok, user, err)
	}
	if tok, user, err := store.ValidateAPIToken("not-a-pool-api-key"); err == nil || tok != nil || user != nil {
		t.Fatalf("malformed token validation = token %#v user %#v err %v, want rejection", tok, user, err)
	}
}

func TestPoolUserStoreLoadsExistingUserArrayWithoutTokenFile(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "pool_users.json")
	user := &PoolUser{
		ID:        "legacy1234567890abcdef",
		Token:     "legacy-download-token",
		Email:     "legacy@example.com",
		PlanType:  "plus",
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal([]*PoolUser{user})
	if err != nil {
		t.Fatalf("marshal legacy users: %v", err)
	}
	if err := os.WriteFile(usersPath, data, 0o600); err != nil {
		t.Fatalf("write legacy users: %v", err)
	}

	store, err := newPoolUserStore(usersPath)
	if err != nil {
		t.Fatalf("newPoolUserStore: %v", err)
	}
	if got := store.Get(user.ID); got == nil || got.Email != user.Email {
		t.Fatalf("Get legacy user = %#v", got)
	}
	if got := store.GetByToken(user.Token); got == nil || got.ID != user.ID {
		t.Fatalf("GetByToken legacy user = %#v", got)
	}
	if got := store.ListAPITokens(user.ID); len(got) != 0 {
		t.Fatalf("legacy store listed API tokens %#v, want none", got)
	}
}

func TestGenerateCodexAuth(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	user := &PoolUser{
		ID:        "abcdef1234567890abcdef1234567890",
		Email:     "test@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}

	auth, err := generateCodexAuth(secret, user)
	if err != nil {
		t.Fatalf("generateCodexAuth failed: %v", err)
	}

	if auth.Tokens == nil {
		t.Fatal("tokens is nil")
	}
	if auth.Tokens.AccessToken == "" {
		t.Error("access_token is empty")
	}
	if auth.Tokens.IDToken == "" {
		t.Error("id_token is empty")
	}
	if auth.Tokens.RefreshToken == "" {
		t.Error("refresh_token is empty")
	}
	if auth.Tokens.AccountID == nil || *auth.Tokens.AccountID == "" {
		t.Error("account_id is empty")
	}

	// Verify the tokens are valid JWTs we can parse
	claims, err := validatePoolUserJWT(secret, auth.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("access token validation failed: %v", err)
	}
	if claims["iss"] != "https://auth.openai.com" {
		t.Errorf("expected iss=https://auth.openai.com, got %v", claims["iss"])
	}

	// Check JSON serialization
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	jsonText := string(data)
	for _, unexpected := range []string{`"auth_mode"`, `"plan_type"`, `"health_status"`, `"disabled"`} {
		if strings.Contains(jsonText, unexpected) {
			t.Fatalf("expected %s to be omitted from pool auth json: %s", unexpected, jsonText)
		}
	}
	t.Logf("Generated auth.json:\n%s", string(data))
}

func TestGenerateGeminiAuth(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	user := &PoolUser{
		ID:        "abcdef1234567890abcdef1234567890",
		Email:     "test@example.com",
		PlanType:  "pro",
		CreatedAt: time.Now(),
	}

	auth, err := generateGeminiAuth(secret, user)
	if err != nil {
		t.Fatalf("generateGeminiAuth failed: %v", err)
	}

	if auth.AccessToken == "" {
		t.Error("access_token is empty")
	}
	if auth.IDToken == "" {
		t.Error("id_token is empty")
	}
	if auth.RefreshToken == "" {
		t.Error("refresh_token is empty")
	}
	if auth.TokenType != "Bearer" {
		t.Errorf("expected token_type=Bearer, got %s", auth.TokenType)
	}
	if auth.ExpiryDate == 0 {
		t.Error("expiry_date is 0")
	}

	// Verify ID token is a pool-signed JWT with Google issuer.
	claims, err := validatePoolUserJWT(secret, auth.IDToken)
	if err != nil {
		t.Fatalf("id token validation failed: %v", err)
	}
	if claims["iss"] != "https://accounts.google.com" {
		t.Errorf("expected iss=https://accounts.google.com, got %v", claims["iss"])
	}

	// Verify access token is a pool-generated Google-like token accepted by Gemini CLI.
	if ok, uid := isGeminiOAuthPoolToken(secret, auth.AccessToken); !ok {
		t.Fatalf("access token validation failed: not a pool token")
	} else if uid != user.ID {
		t.Fatalf("access token validation failed: expected user %q, got %q", user.ID, uid)
	}

	// Check JSON serialization
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	t.Logf("Generated oauth_creds.json:\n%s", string(data))
}

func TestLooksLikeProviderCredential(t *testing.T) {
	tests := []struct {
		name         string
		authHeader   string
		wantIsValid  bool
		wantProvider AccountType
	}{
		{
			name:         "Claude API key",
			authHeader:   "Bearer sk-ant-api03-abc123xyz",
			wantIsValid:  true,
			wantProvider: AccountTypeClaude,
		},
		{
			name:         "Claude OAuth token",
			authHeader:   "Bearer sk-ant-oat01-abc123xyz",
			wantIsValid:  true,
			wantProvider: AccountTypeClaude,
		},
		{
			name:        "Pool OpenAI-compatible virtual key",
			authHeader:  "Bearer sk-cpool-kid.secret",
			wantIsValid: false,
		},
		{
			name:         "OpenAI project key",
			authHeader:   "Bearer sk-proj-abc123xyz",
			wantIsValid:  true,
			wantProvider: AccountTypeCodex,
		},
		{
			name:         "OpenAI legacy key",
			authHeader:   "Bearer sk-abc123xyz",
			wantIsValid:  true,
			wantProvider: AccountTypeCodex,
		},
		{
			name:         "Google OAuth token",
			authHeader:   "Bearer ya29.abc123xyz",
			wantIsValid:  true,
			wantProvider: AccountTypeGemini,
		},
		{
			name:        "Empty header",
			authHeader:  "",
			wantIsValid: false,
		},
		{
			name:        "No Bearer prefix",
			authHeader:  "sk-ant-api03-abc123",
			wantIsValid: false,
		},
		{
			name:        "Random JWT (pool user token)",
			authHeader:  "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJwb29sfGFiYzEyMyJ9.signature",
			wantIsValid: false,
		},
		{
			name:        "Unknown token format",
			authHeader:  "Bearer some-random-token",
			wantIsValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIsValid, gotProvider := looksLikeProviderCredential(tt.authHeader)
			if gotIsValid != tt.wantIsValid {
				t.Errorf("looksLikeProviderCredential() isValid = %v, want %v", gotIsValid, tt.wantIsValid)
			}
			if gotIsValid && gotProvider != tt.wantProvider {
				t.Errorf("looksLikeProviderCredential() provider = %v, want %v", gotProvider, tt.wantProvider)
			}
		})
	}
}

func TestClaudePoolToken_FormatAndBackwardCompatibility(t *testing.T) {
	secret := "test-secret-key-12345678901234567890"
	userID := "user123"

	tok := generateClaudePoolToken(secret, userID)
	if !strings.HasPrefix(tok, ClaudePoolTokenPrefix) {
		t.Fatalf("expected token to start with %q, got %q", ClaudePoolTokenPrefix, tok)
	}
	if !strings.HasPrefix(tok, "sk-ant-oat01-") {
		t.Fatalf("expected token to look like sk-ant-oat01-*, got %q", tok)
	}

	// Validate parse + auth header helper.
	if uid, ok := parseClaudePoolToken(secret, tok); !ok || uid != userID {
		t.Fatalf("parseClaudePoolToken failed: ok=%v uid=%q want=%q", ok, uid, userID)
	}
	if ok, uid := isClaudePoolToken(secret, "Bearer "+tok); !ok || uid != userID {
		t.Fatalf("isClaudePoolToken failed: ok=%v uid=%q want=%q", ok, uid, userID)
	}

	// Legacy prefix should continue to work for already-issued tokens.
	legacy := ClaudePoolTokenLegacyPrefix + strings.TrimPrefix(tok, ClaudePoolTokenPrefix)
	if uid, ok := parseClaudePoolToken(secret, legacy); !ok || uid != userID {
		t.Fatalf("legacy parseClaudePoolToken failed: ok=%v uid=%q want=%q", ok, uid, userID)
	}
	if ok, uid := isClaudePoolToken(secret, "Bearer "+legacy); !ok || uid != userID {
		t.Fatalf("legacy isClaudePoolToken failed: ok=%v uid=%q want=%q", ok, uid, userID)
	}
}
