package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PoolUser represents a generated pool user who can use the proxy.
type PoolUser struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"` // Download token for /config/codex/<token>
	Email     string    `json:"email"`
	PlanType  string    `json:"plan_type"` // pro, team, plus
	CreatedAt time.Time `json:"created_at"`
	Disabled  bool      `json:"disabled"`
}

// PoolAPIToken represents a virtual OpenAI-compatible API key for a pool user.
// Raw keys are returned only once at creation time and are never persisted.
type PoolAPIToken struct {
	ID                  string        `json:"id"`
	KID                 string        `json:"kid"`
	UserID              string        `json:"user_id"`
	Name                string        `json:"name"`
	Hash                string        `json:"hash"`
	Prefix              string        `json:"prefix"`
	Last4               string        `json:"last4"`
	CreatedAt           time.Time     `json:"created_at"`
	LastUsedAt          *time.Time    `json:"last_used_at,omitempty"`
	Disabled            bool          `json:"disabled"`
	AllowedModels       []string      `json:"allowed_models,omitempty"`
	AllowedAccountTypes []AccountType `json:"allowed_account_types,omitempty"`
	MaxRPM              int           `json:"max_rpm,omitempty"`
	MaxTPM              int           `json:"max_tpm,omitempty"`
	MaxConcurrency      int           `json:"max_concurrency,omitempty"`
	DailyBudget         float64       `json:"daily_budget,omitempty"`
	MonthlyBudget       float64       `json:"monthly_budget,omitempty"`
	NoLog               bool          `json:"no_log"`
}

// PoolAPITokenPolicy is the metadata policy attached to a virtual pool API key.
// OpenAI-compatible virtual-key request admission enforces these fields before
// requests are sent upstream.
type PoolAPITokenPolicy struct {
	AllowedModels       []string      `json:"allowed_models,omitempty"`
	AllowedAccountTypes []AccountType `json:"allowed_account_types,omitempty"`
	MaxRPM              int           `json:"max_rpm,omitempty"`
	MaxTPM              int           `json:"max_tpm,omitempty"`
	MaxConcurrency      int           `json:"max_concurrency,omitempty"`
	DailyBudget         float64       `json:"daily_budget,omitempty"`
	MonthlyBudget       float64       `json:"monthly_budget,omitempty"`
	NoLog               bool          `json:"no_log"`
}

// PoolUserStore manages pool user persistence.
type PoolUserStore struct {
	mu              sync.RWMutex
	path            string
	apiTokenPath    string
	users           map[string]*PoolUser     // keyed by ID
	byTok           map[string]*PoolUser     // keyed by download token
	apiTokens       map[string]*PoolAPIToken // keyed by token ID/KID
	apiTokensByUser map[string]map[string]*PoolAPIToken
}

func newPoolUserStore(path string) (*PoolUserStore, error) {
	s := &PoolUserStore{
		path:            path,
		apiTokenPath:    poolAPITokenStoragePath(path),
		users:           make(map[string]*PoolUser),
		byTok:           make(map[string]*PoolUser),
		apiTokens:       make(map[string]*PoolAPIToken),
		apiTokensByUser: make(map[string]map[string]*PoolAPIToken),
	}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func poolAPITokenStoragePath(usersPath string) string {
	dir := filepath.Dir(usersPath)
	base := filepath.Base(usersPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if ext == "" {
		ext = ".json"
	}
	return filepath.Join(dir, name+"_api_tokens"+ext)
}

func (s *PoolUserStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var users []*PoolUser
	if err := json.Unmarshal(data, &users); err != nil {
		return err
	}

	var apiTokens []*PoolAPIToken
	tokenData, err := os.ReadFile(s.apiTokenPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(tokenData, &apiTokens); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = make(map[string]*PoolUser, len(users))
	s.byTok = make(map[string]*PoolUser, len(users))
	s.apiTokens = make(map[string]*PoolAPIToken, len(apiTokens))
	s.apiTokensByUser = make(map[string]map[string]*PoolAPIToken)
	for _, u := range users {
		s.users[u.ID] = u
		s.byTok[u.Token] = u
	}
	for _, tok := range apiTokens {
		s.indexAPITokenLocked(tok)
	}
	return nil
}

func (s *PoolUserStore) save() error {
	users := make([]*PoolUser, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, u)
	}
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *PoolUserStore) saveAPITokensLocked() error {
	apiTokens := make([]*PoolAPIToken, 0, len(s.apiTokens))
	for _, tok := range s.apiTokens {
		apiTokens = append(apiTokens, tok)
	}
	sort.Slice(apiTokens, func(i, j int) bool {
		if apiTokens[i].CreatedAt.Equal(apiTokens[j].CreatedAt) {
			return apiTokens[i].ID < apiTokens[j].ID
		}
		return apiTokens[i].CreatedAt.Before(apiTokens[j].CreatedAt)
	})
	data, err := json.MarshalIndent(apiTokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.apiTokenPath, data, 0o600)
}

func (s *PoolUserStore) indexAPITokenLocked(tok *PoolAPIToken) {
	if tok == nil {
		return
	}
	if tok.KID == "" {
		tok.KID = tok.ID
	}
	if tok.ID == "" {
		tok.ID = tok.KID
	}
	if tok.ID == "" {
		return
	}
	s.apiTokens[tok.ID] = tok
	if tok.UserID != "" {
		if s.apiTokensByUser[tok.UserID] == nil {
			s.apiTokensByUser[tok.UserID] = make(map[string]*PoolAPIToken)
		}
		s.apiTokensByUser[tok.UserID][tok.ID] = tok
	}
}

func clonePoolAPIToken(tok *PoolAPIToken) *PoolAPIToken {
	if tok == nil {
		return nil
	}
	clone := *tok
	if tok.LastUsedAt != nil {
		lastUsedAt := *tok.LastUsedAt
		clone.LastUsedAt = &lastUsedAt
	}
	if tok.AllowedModels != nil {
		clone.AllowedModels = append([]string(nil), tok.AllowedModels...)
	}
	if tok.AllowedAccountTypes != nil {
		clone.AllowedAccountTypes = append([]AccountType(nil), tok.AllowedAccountTypes...)
	}
	return &clone
}

func (s *PoolUserStore) Create(u *PoolUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
	s.byTok[u.Token] = u
	return s.save()
}

func (s *PoolUserStore) Get(id string) *PoolUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[id]
}

func (s *PoolUserStore) GetByToken(token string) *PoolUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byTok[token]
}

func (s *PoolUserStore) List() []*PoolUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*PoolUser, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	return out
}

func (s *PoolUserStore) GetByEmail(email string) *PoolUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Email == email {
			return u
		}
	}
	return nil
}

func (s *PoolUserStore) Disable(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[id]; ok {
		u.Disabled = true
		return s.save()
	}
	return fmt.Errorf("user not found: %s", id)
}

const poolAPIKeyPrefix = "sk-cpool-"

// CreateAPIToken creates a virtual OpenAI-compatible API key for a pool user.
// The raw key is returned only once and is never persisted.
func (s *PoolUserStore) CreateAPIToken(userID, name string) (string, *PoolAPIToken, error) {
	return s.CreateAPITokenWithPolicy(userID, name, PoolAPITokenPolicy{})
}

// CreateAPITokenWithPolicy creates a virtual OpenAI-compatible API key and persists
// non-secret policy metadata for admission checks and operator visibility.
func (s *PoolUserStore) CreateAPITokenWithPolicy(userID, name string, policy PoolAPITokenPolicy) (string, *PoolAPIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user := s.users[userID]
	if user == nil {
		return "", nil, fmt.Errorf("user not found: %s", userID)
	}
	if user.Disabled {
		return "", nil, fmt.Errorf("user disabled: %s", userID)
	}

	var kid string
	for i := 0; i < 10; i++ {
		candidate, err := secureRandomHex(8)
		if err != nil {
			return "", nil, fmt.Errorf("generate API token id: %w", err)
		}
		kid = candidate
		if _, exists := s.apiTokens[kid]; !exists {
			break
		}
		kid = ""
	}
	if kid == "" {
		return "", nil, fmt.Errorf("could not allocate API token id")
	}

	secret, err := secureRandomHex(24)
	if err != nil {
		return "", nil, fmt.Errorf("generate API token secret: %w", err)
	}
	raw := poolAPIKeyPrefix + kid + "." + secret
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	now := time.Now().UTC()
	meta := &PoolAPIToken{
		ID:                  kid,
		KID:                 kid,
		UserID:              userID,
		Name:                name,
		Hash:                poolAPITokenHash(raw),
		Prefix:              poolAPIKeyPrefix + kid,
		Last4:               raw[len(raw)-4:],
		CreatedAt:           now,
		AllowedModels:       append([]string(nil), policy.AllowedModels...),
		AllowedAccountTypes: append([]AccountType(nil), policy.AllowedAccountTypes...),
		MaxRPM:              policy.MaxRPM,
		MaxTPM:              policy.MaxTPM,
		MaxConcurrency:      policy.MaxConcurrency,
		DailyBudget:         policy.DailyBudget,
		MonthlyBudget:       policy.MonthlyBudget,
		NoLog:               policy.NoLog,
	}
	s.indexAPITokenLocked(meta)
	if err := s.saveAPITokensLocked(); err != nil {
		delete(s.apiTokens, kid)
		if byUser := s.apiTokensByUser[userID]; byUser != nil {
			delete(byUser, kid)
		}
		return "", nil, err
	}
	return raw, clonePoolAPIToken(meta), nil
}

// GenerateAPIToken is an alias for CreateAPIToken.
func (s *PoolUserStore) GenerateAPIToken(userID, name string) (string, *PoolAPIToken, error) {
	return s.CreateAPIToken(userID, name)
}

// CreatePoolAPIKey is an alias for CreateAPIToken.
func (s *PoolUserStore) CreatePoolAPIKey(userID, name string) (string, *PoolAPIToken, error) {
	return s.CreateAPIToken(userID, name)
}

// ValidateAPIToken validates a raw virtual pool API key and returns its token metadata and user.
func (s *PoolUserStore) ValidateAPIToken(raw string) (*PoolAPIToken, *PoolUser, error) {
	kid, normalizedRaw, err := parsePoolAPIKey(raw)
	if err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tok := s.apiTokens[kid]
	if tok == nil {
		return nil, nil, fmt.Errorf("unknown API token")
	}
	if tok.Disabled {
		return nil, nil, fmt.Errorf("API token disabled")
	}
	if !poolAPITokenHashMatches(tok.Hash, normalizedRaw) {
		return nil, nil, fmt.Errorf("invalid API token")
	}
	user := s.users[tok.UserID]
	if user == nil {
		return nil, nil, fmt.Errorf("API token user not found")
	}
	if user.Disabled {
		return nil, nil, fmt.Errorf("API token user disabled")
	}
	now := time.Now().UTC()
	tok.LastUsedAt = &now
	// last_used_at persistence is best-effort; authentication succeeded above.
	_ = s.saveAPITokensLocked()
	return clonePoolAPIToken(tok), user, nil
}

// ValidatePoolAPIKey is an alias for ValidateAPIToken.
func (s *PoolUserStore) ValidatePoolAPIKey(raw string) (*PoolAPIToken, *PoolUser, error) {
	return s.ValidateAPIToken(raw)
}

// DisableAPIToken disables a virtual pool API key by ID/KID.
func (s *PoolUserStore) DisableAPIToken(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := s.apiTokens[id]
	if tok == nil {
		return fmt.Errorf("API token not found: %s", id)
	}
	tok.Disabled = true
	return s.saveAPITokensLocked()
}

// DisablePoolAPIToken is an alias for DisableAPIToken.
func (s *PoolUserStore) DisablePoolAPIToken(id string) error {
	return s.DisableAPIToken(id)
}

// ListAPITokens lists virtual pool API key metadata for a user. Raw keys are not stored.
func (s *PoolUserStore) ListAPITokens(userID string) []*PoolAPIToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := s.apiTokensByUser[userID]
	out := make([]*PoolAPIToken, 0, len(byID))
	for _, tok := range byID {
		out = append(out, clonePoolAPIToken(tok))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// ListPoolAPITokens is an alias for ListAPITokens.
func (s *PoolUserStore) ListPoolAPITokens(userID string) []*PoolAPIToken {
	return s.ListAPITokens(userID)
}

func parsePoolAPIKey(raw string) (string, string, error) {
	normalized := strings.TrimSpace(raw)
	if strings.HasPrefix(normalized, "Bearer ") {
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "Bearer "))
	}
	if !strings.HasPrefix(normalized, poolAPIKeyPrefix) {
		return "", "", fmt.Errorf("invalid API token format")
	}
	rest := strings.TrimPrefix(normalized, poolAPIKeyPrefix)
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 || dot == len(rest)-1 {
		return "", "", fmt.Errorf("invalid API token format")
	}
	kid := rest[:dot]
	secret := rest[dot+1:]
	if strings.Contains(kid, ".") || strings.Contains(secret, ".") {
		return "", "", fmt.Errorf("invalid API token format")
	}
	return kid, normalized, nil
}

func poolAPITokenHash(raw string) string {
	if secret := getPoolJWTSecret(); secret != "" {
		return "hmac_sha256:" + hex.EncodeToString(hmacSign(secret, []byte(raw)))
	}
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func poolAPITokenHashMatches(storedHash, raw string) bool {
	var expected string
	switch {
	case strings.HasPrefix(storedHash, "hmac_sha256:"):
		secret := getPoolJWTSecret()
		if secret == "" {
			return false
		}
		expected = "hmac_sha256:" + hex.EncodeToString(hmacSign(secret, []byte(raw)))
	case strings.HasPrefix(storedHash, "sha256:"):
		sum := sha256.Sum256([]byte(raw))
		expected = "sha256:" + hex.EncodeToString(sum[:])
	default:
		sum := sha256.Sum256([]byte(raw))
		shaHash := hex.EncodeToString(sum[:])
		if hmac.Equal([]byte(storedHash), []byte(shaHash)) {
			return true
		}
		secret := getPoolJWTSecret()
		if secret == "" {
			return false
		}
		expected = hex.EncodeToString(hmacSign(secret, []byte(raw)))
	}
	return hmac.Equal([]byte(storedHash), []byte(expected))
}

// JWT generation

func randomHex(n int) string {
	out, err := secureRandomHex(n)
	if err != nil {
		panic(fmt.Sprintf("secure random hex generation failed: %v", err))
	}
	return out
}

func secureRandomHex(n int) (string, error) {
	return randomHexFromReader(rand.Reader, n)
}

func randomHexFromReader(r io.Reader, n int) (string, error) {
	if n < 0 {
		return "", fmt.Errorf("random byte count must be non-negative")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func signJWT(secret string, claims map[string]any) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := header + "." + payload

	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	return signingInput + "." + signature, nil
}

// hmacSign creates an HMAC-SHA256 signature for arbitrary data.
func hmacSign(secret string, data []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(data)
	return h.Sum(nil)
}

// validatePoolUserJWT checks if a JWT was signed with our secret and returns the claims.
func validatePoolUserJWT(secret, token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format")
	}

	signingInput := parts[0] + "." + parts[1]
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(signingInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	if !hmac.Equal([]byte(expectedSig), []byte(parts[2])) {
		return nil, fmt.Errorf("invalid signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}

	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, err
	}

	// Check expiry
	if exp, ok := claims["exp"].(float64); ok {
		if int64(exp) < time.Now().Unix() {
			return nil, fmt.Errorf("token expired")
		}
	}

	return claims, nil
}

// isPoolUserToken checks if the Authorization header contains a pool user JWT.
// Returns (isPoolUser, userID, error).
func isPoolUserToken(secret, authHeader string) (bool, string, error) {
	if secret == "" {
		return false, "", nil
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false, "", nil
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := validatePoolUserJWT(secret, token)
	if err != nil {
		return false, "", nil // Not a valid pool user token
	}

	// Check issuer - accept OpenAI (Codex), Google (Gemini), and Anthropic (Claude)
	if iss, ok := claims["iss"].(string); ok {
		validIssuers := map[string]bool{
			"https://auth.openai.com":     true, // Codex
			"https://accounts.google.com": true, // Gemini
			"https://auth.anthropic.com":  true, // Claude
		}
		if !validIssuers[iss] {
			return false, "", nil
		}
	} else {
		return false, "", nil
	}

	// Extract user ID from sub claim (pool|<user_id>)
	if sub, ok := claims["sub"].(string); ok {
		if strings.HasPrefix(sub, "pool|") {
			userID := strings.TrimPrefix(sub, "pool|")
			return true, userID, nil
		}
	}

	return false, "", nil
}

// hashUserIP creates a non-reversible ID from an IP address using SHA256.
// The salt ensures IDs are unique to this pool instance.
func hashUserIP(ip, salt string) string {
	h := sha256.New()
	h.Write([]byte(salt + "|" + ip))
	return hex.EncodeToString(h.Sum(nil))[:16] // 16 char hex ID
}

// PoolUserGeminiAuth matches the Gemini oauth_creds.json format for pool users.
// (Includes id_token which the base GeminiAuthJSON doesn't have)
type PoolUserGeminiAuth struct {
	AccessToken  string `json:"access_token"`
	ExpiryDate   int64  `json:"expiry_date"` // Unix ms
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// generateCodexAuth creates the auth.json content for a pool user.
func generateCodexAuth(secret string, user *PoolUser) (*CodexAuthJSON, error) {
	now := time.Now()
	exp := now.Add(10 * 365 * 24 * time.Hour).Unix() // 10 years

	// Generate a UUID-like account ID to match OpenAI's format
	accountID := fmt.Sprintf("%s-%s-%s-%s-%s",
		user.ID[:8],
		randomHex(2),
		randomHex(2),
		randomHex(2),
		randomHex(6))

	// Generate unique IDs
	jtiID := fmt.Sprintf("%s-%s-%s-%s-%s", randomHex(4), randomHex(2), randomHex(2), randomHex(2), randomHex(6))
	sessionID := "authsess_pool" + randomHex(12)
	chatgptUserID := "user-pool-" + user.ID[:8]
	chatgptAccountUserID := chatgptUserID + "__" + accountID

	// ID token claims - match OpenAI's format closely
	idTokenClaims := map[string]any{
		"exp":            exp,
		"iat":            now.Unix(),
		"nbf":            now.Unix(),
		"iss":            "https://auth.openai.com",
		"sub":            "pool|" + user.ID,
		"aud":            []string{"app_EMoamEEZ73f0CkXaXp7hrann"},
		"jti":            jtiID,
		"client_id":      "app_EMoamEEZ73f0CkXaXp7hrann",
		"session_id":     sessionID,
		"email":          user.Email,
		"email_verified": true,
		"scp":            []string{"openid", "profile", "email", "offline_access"},
		"pwd_auth_time":  now.UnixMilli(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":        accountID,
			"chatgpt_account_user_id":   chatgptAccountUserID,
			"chatgpt_compute_residency": "no_constraint",
			"chatgpt_plan_type":         user.PlanType,
			"chatgpt_user_id":           chatgptUserID,
			"user_id":                   chatgptUserID,
		},
		"https://api.openai.com/mfa": map[string]any{
			"required": "no",
		},
		"https://api.openai.com/profile": map[string]any{
			"email":          user.Email,
			"email_verified": true,
		},
	}

	idToken, err := signJWT(secret, idTokenClaims)
	if err != nil {
		return nil, err
	}

	// Access token claims - similar but different aud
	accessClaims := map[string]any{
		"exp":           exp,
		"iat":           now.Unix(),
		"nbf":           now.Unix(),
		"iss":           "https://auth.openai.com",
		"sub":           "pool|" + user.ID,
		"aud":           []string{"https://api.openai.com/v1"},
		"jti":           randomHex(8) + "-" + randomHex(4) + "-" + randomHex(4) + "-" + randomHex(4) + "-" + randomHex(12),
		"client_id":     "app_EMoamEEZ73f0CkXaXp7hrann",
		"session_id":    sessionID,
		"scp":           []string{"openid", "profile", "email", "offline_access"},
		"pwd_auth_time": now.UnixMilli(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":        accountID,
			"chatgpt_account_user_id":   chatgptAccountUserID,
			"chatgpt_compute_residency": "no_constraint",
			"chatgpt_plan_type":         user.PlanType,
			"chatgpt_user_id":           chatgptUserID,
			"user_id":                   chatgptUserID,
		},
		"https://api.openai.com/mfa": map[string]any{
			"required": "no",
		},
		"https://api.openai.com/profile": map[string]any{
			"email":          user.Email,
			"email_verified": true,
		},
	}
	accessToken, err := signJWT(secret, accessClaims)
	if err != nil {
		return nil, err
	}

	refreshToken := fmt.Sprintf("poolrt_%s_%s", user.ID, randomHex(16))

	return &CodexAuthJSON{
		OpenAIKey: nil,
		Tokens: &TokenData{
			IDToken:      idToken,
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			AccountID:    &accountID,
		},
		LastRefresh: &now, // Required by Codex CLI - must be non-null
	}, nil
}

// generateGeminiAuth creates the oauth_creds.json content for a pool user.
// Note: We use Google-like token formats (ya29.* and 1//*) so the Gemini CLI
// doesn't reject them during local validation. The pool validates these tokens.
func generateGeminiAuth(secret string, user *PoolUser) (*PoolUserGeminiAuth, error) {
	now := time.Now()
	exp := now.Add(365 * 24 * time.Hour).Unix() // 1 year
	expiryDateMs := now.Add(365 * 24 * time.Hour).UnixMilli()

	claims := map[string]any{
		"exp":            exp,
		"iat":            now.Unix(),
		"iss":            "https://accounts.google.com",
		"sub":            "pool|" + user.ID,
		"email":          user.Email,
		"email_verified": true,
	}

	// Create a JWT for id_token (this is expected to be a JWT)
	idToken, err := signJWT(secret, claims)
	if err != nil {
		return nil, err
	}

	// Create a Google-like access token (ya29.pool-<base64 payload>)
	// This format passes Gemini CLI's local validation while being verifiable by our pool
	payloadBytes, _ := json.Marshal(map[string]any{
		"user_id": user.ID,
		"exp":     exp,
		"iat":     now.Unix(),
	})
	sig := hmacSign(secret, payloadBytes)
	accessToken := fmt.Sprintf("ya29.pool-%s_%s", base64.RawURLEncoding.EncodeToString(payloadBytes), base64.RawURLEncoding.EncodeToString(sig))

	// Use a Google-like refresh token format
	refreshToken := fmt.Sprintf("1//pool_%s_%s", user.ID, randomHex(16))

	return &PoolUserGeminiAuth{
		AccessToken:  accessToken,
		ExpiryDate:   expiryDateMs,
		IDToken:      idToken,
		RefreshToken: refreshToken,
		Scope:        "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile openid",
		TokenType:    "Bearer",
	}, nil
}

// generateGeminiAPIKey creates a pool API key for Gemini CLI in API key mode.
// Format: AIzaSy-pool-<user_id>.<timestamp>.<signature>
// This bypasses OAuth completely and lets Gemini CLI work with our proxy.
func generateGeminiAPIKey(secret string, user *PoolUser) string {
	timestamp := time.Now().Unix()
	payload := fmt.Sprintf("%s.%d", user.ID, timestamp)
	sig := hmacSign(secret, []byte(payload))
	return fmt.Sprintf("AIzaSy-pool-%s.%d.%s", user.ID, timestamp, base64.RawURLEncoding.EncodeToString(sig)[:16])
}

// isGeminiOAuthPoolToken checks if a Bearer token is a pool-generated Gemini OAuth token.
// Returns (isPoolToken, userID).
// Pool tokens have format: ya29.pool-<base64 payload>_<base64 signature>
func isGeminiOAuthPoolToken(secret, token string) (bool, string) {
	if secret == "" || !strings.HasPrefix(token, "ya29.pool-") {
		return false, ""
	}

	// Extract rest: ya29.pool-<payload>_<signature>
	//
	// Note: payload and signature are base64url strings, and base64url *can contain* "_".
	// We therefore cannot safely split on "_" and expect exactly 2 parts.
	rest := strings.TrimPrefix(token, "ya29.pool-")
	if rest == "" {
		return false, ""
	}

	tryParse := func(payloadB64, sigB64 string) (bool, string) {
		// Decode payload
		payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
		if err != nil {
			return false, ""
		}

		// Decode signature
		providedSig, err := base64.RawURLEncoding.DecodeString(sigB64)
		if err != nil {
			return false, ""
		}

		// Verify signature
		expectedSig := hmacSign(secret, payloadBytes)
		if !hmac.Equal(expectedSig, providedSig) {
			return false, ""
		}

		// Extract user_id from payload
		var payload struct {
			UserID string `json:"user_id"`
			Exp    int64  `json:"exp"`
		}
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			return false, ""
		}

		// Check expiry
		if payload.Exp > 0 && payload.Exp < time.Now().Unix() {
			return false, "" // Expired
		}

		return true, payload.UserID
	}

	// Try every possible split position. Only the correct one will pass HMAC validation.
	for i := 0; i < len(rest); i++ {
		if rest[i] != '_' {
			continue
		}
		payloadB64 := rest[:i]
		sigB64 := rest[i+1:]
		if payloadB64 == "" || sigB64 == "" {
			continue
		}
		if ok, uid := tryParse(payloadB64, sigB64); ok {
			return true, uid
		}
	}

	return false, ""
}

// isPoolGeminiAPIKey checks if an API key is a pool-generated Gemini key.
// Returns (isPoolKey, userID, error).
func isPoolGeminiAPIKey(secret, apiKey string) (bool, string, error) {
	if secret == "" || !strings.HasPrefix(apiKey, "AIzaSy-pool-") {
		return false, "", nil
	}

	// Extract parts: AIzaSy-pool-<user_id>.<timestamp>.<signature>
	rest := strings.TrimPrefix(apiKey, "AIzaSy-pool-")
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return false, "", nil
	}

	userID := parts[0]
	timestampStr := parts[1]
	providedSig := parts[2]

	// Verify signature
	payload := fmt.Sprintf("%s.%s", userID, timestampStr)
	expectedSig := base64.RawURLEncoding.EncodeToString(hmacSign(secret, []byte(payload)))[:16]

	if providedSig != expectedSig {
		return false, "", nil
	}

	return true, userID, nil
}

// getPoolJWTSecret returns the JWT signing secret from config or env.
func getPoolJWTSecret() string {
	if v := os.Getenv("POOL_JWT_SECRET"); v != "" {
		return v
	}
	if globalConfigFile != nil && globalConfigFile.PoolUsers.JWTSecret != "" {
		return globalConfigFile.PoolUsers.JWTSecret
	}
	return ""
}

// getPoolUsersPath returns the pool users storage path from config or env.
func getPoolUsersPath() string {
	if v := os.Getenv("POOL_USERS_PATH"); v != "" {
		return v
	}
	if globalConfigFile != nil && globalConfigFile.PoolUsers.StoragePath != "" {
		return globalConfigFile.PoolUsers.StoragePath
	}
	return "./data/pool_users.json"
}

// getPublicURL returns the public URL override from config or env.
// Returns empty string if not configured (use request host instead).
func getPublicURL() string {
	if v := os.Getenv("PUBLIC_URL"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if globalConfigFile != nil && globalConfigFile.PublicURL != "" {
		return strings.TrimSuffix(globalConfigFile.PublicURL, "/")
	}
	return ""
}

// PoolUserClaudeAuth matches the Claude Code credentials format for pool users.
type PoolUserClaudeAuth struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Email        string `json:"email"`
}

// generateClaudeAuth creates the credentials JSON content for a Claude Code pool user.
// Uses a fake sk-ant-oat01-pool-* format that looks like a real Claude OAuth token
// (CLAUDE_CODE_OAUTH_TOKEN) but contains an embedded user ID and signature for pool
// authentication.
func generateClaudeAuth(secret string, user *PoolUser) (*PoolUserClaudeAuth, error) {
	// Generate a fake sk-ant-oat01 token with embedded pool user info.
	// Format: sk-ant-oat01-pool-<base64url(userID.timestamp.signature)>
	accessToken := generateClaudePoolToken(secret, user.ID)

	refreshToken := fmt.Sprintf("poolrt_%s_%s", user.ID, randomHex(16))

	return &PoolUserClaudeAuth{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      "", // Claude doesn't use ID token
		Email:        user.Email,
	}, nil
}

// ClaudePoolTokenPrefix is the prefix for pool-generated Claude tokens.
// These look like real sk-ant-oat01 tokens but have a "pool" marker for detection.
//
// Note: we keep accepting the legacy sk-ant-api-pool-* prefix for backward compatibility
// with already-issued tokens.
const ClaudePoolTokenPrefix = "sk-ant-oat01-pool-"

const ClaudePoolTokenLegacyPrefix = "sk-ant-api-pool-"

// generateClaudePoolToken creates a fake Claude OAuth token with embedded pool user info.
// Format: sk-ant-oat01-pool-<base64url(userID.timestamp.signature)>
func generateClaudePoolToken(secret, userID string) string {
	now := time.Now().Unix()
	// Create payload: userID.timestamp
	payload := fmt.Sprintf("%s.%d", userID, now)
	// Sign it
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	sig := hex.EncodeToString(h.Sum(nil))[:16] // 16 char signature
	// Combine and encode
	data := fmt.Sprintf("%s.%s", payload, sig)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(data))
	return ClaudePoolTokenPrefix + encoded
}

// parseClaudePoolToken extracts the user ID from a pool-generated Claude token.
// Returns (userID, isValid).
func parseClaudePoolToken(secret, token string) (string, bool) {
	if secret == "" {
		return "", false
	}
	var encoded string
	switch {
	case strings.HasPrefix(token, ClaudePoolTokenPrefix):
		encoded = strings.TrimPrefix(token, ClaudePoolTokenPrefix)
	case strings.HasPrefix(token, ClaudePoolTokenLegacyPrefix):
		encoded = strings.TrimPrefix(token, ClaudePoolTokenLegacyPrefix)
	default:
		return "", false
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	// Parse: userID.timestamp.signature
	parts := strings.Split(string(data), ".")
	if len(parts) != 3 {
		return "", false
	}
	userID := parts[0]
	timestamp := parts[1]
	providedSig := parts[2]
	// Verify signature
	payload := fmt.Sprintf("%s.%s", userID, timestamp)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	expectedSig := hex.EncodeToString(h.Sum(nil))[:16]
	if !hmac.Equal([]byte(expectedSig), []byte(providedSig)) {
		return "", false
	}
	return userID, true
}
