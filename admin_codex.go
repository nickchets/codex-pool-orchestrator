package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Codex OAuth constants (from codex-rs/login/src/server.rs)
const (
	CodexOAuthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexOAuthRedirectURI  = "http://localhost:1455/auth/callback"
	CodexOAuthTokenURL     = "https://auth.openai.com/oauth/token"
	CodexOAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
)

// CodexOAuthSession stores pending OAuth state
type CodexOAuthSession struct {
	AccountID    string
	Verifier     string
	Challenge    string
	State        string
	AutoComplete bool
	InFlight     bool
	CreatedAt    time.Time
}

// In-memory store for pending Codex OAuth sessions
var codexOAuthSessions = struct {
	sync.RWMutex
	sessions map[string]*CodexOAuthSession
}{sessions: make(map[string]*CodexOAuthSession)}

var codexOAuthHTTPClient = http.DefaultClient

func findCodexSessionByState(state string) (string, *CodexOAuthSession, bool) {
	codexOAuthSessions.RLock()
	defer codexOAuthSessions.RUnlock()
	for verifier, session := range codexOAuthSessions.sessions {
		if session != nil && session.State == state {
			return verifier, session, true
		}
	}
	return "", nil, false
}

func claimCodexSession(verifier, state string) (string, *CodexOAuthSession, bool) {
	codexOAuthSessions.Lock()
	defer codexOAuthSessions.Unlock()

	if verifier != "" {
		session, ok := codexOAuthSessions.sessions[verifier]
		if !ok || session == nil || session.InFlight {
			return "", nil, false
		}
		if state != "" && session.State != state {
			return "", nil, false
		}
		session.InFlight = true
		return verifier, session, true
	}

	for sessionVerifier, session := range codexOAuthSessions.sessions {
		if session == nil {
			continue
		}
		if session.InFlight {
			continue
		}
		if state != "" && session.State != state {
			continue
		}
		session.InFlight = true
		return sessionVerifier, session, true
	}

	return "", nil, false
}

func finalizeCodexSession(verifier string) {
	codexOAuthSessions.Lock()
	delete(codexOAuthSessions.sessions, verifier)
	codexOAuthSessions.Unlock()

	scheduleStopCodexLoopbackCallbackServersIfIdle()
}

func releaseCodexSession(verifier string) {
	codexOAuthSessions.Lock()
	session, ok := codexOAuthSessions.sessions[verifier]
	if ok && session != nil {
		session.InFlight = false
	}
	codexOAuthSessions.Unlock()
}

// CodexTokenResponse is the response from the token endpoint
type CodexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

type codexExchangeRequest struct {
	Code        string
	Verifier    string
	State       string
	CallbackURL string
	Lane        string
}

type codexExchangeResult struct {
	AccountID         string
	RefreshedExisting bool
}

// serveCodexAdmin routes Codex admin requests
func (h *proxyHandler) serveCodexAdmin(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/codex")
	if path == "" {
		path = "/"
	}

	switch {
	case path == "/" && r.Method == http.MethodGet:
		h.handleCodexList(w, r)

	case path == "/add" && r.Method == http.MethodPost:
		h.handleCodexAdd(w, r)

	case path == "/exchange" && r.Method == http.MethodPost:
		h.handleCodexExchange(w, r)

	default:
		http.NotFound(w, r)
	}
}

// GET /admin/codex - list all Codex accounts
func (h *proxyHandler) handleCodexList(w http.ResponseWriter, r *http.Request) {
	accounts := h.pool.allAccounts()

	type accountInfo struct {
		ID          string    `json:"id"`
		PlanType    string    `json:"plan_type"`
		Dead        bool      `json:"dead"`
		Disabled    bool      `json:"disabled"`
		ExpiresAt   time.Time `json:"expires_at,omitempty"`
		LastRefresh time.Time `json:"last_refresh,omitempty"`
	}

	var result []accountInfo
	for _, acc := range accounts {
		if acc.Type == AccountTypeCodex {
			result = append(result, accountInfo{
				ID:          acc.ID,
				PlanType:    acc.PlanType,
				Dead:        acc.Dead,
				Disabled:    acc.Disabled,
				ExpiresAt:   acc.ExpiresAt,
				LastRefresh: acc.LastRefresh,
			})
		}
	}

	respondJSON(w, map[string]any{
		"accounts": result,
		"count":    len(result),
	})
}

// POST /admin/codex/add - start OAuth flow
func (h *proxyHandler) handleCodexAdd(w http.ResponseWriter, r *http.Request) {
	payload, err := startCodexOAuthSession(false)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, payload)
}

func (h *proxyHandler) handleOperatorCodexAdd(w http.ResponseWriter, r *http.Request) {
	if err := ensureCodexLoopbackCallbackServersForOperator(h); err != nil {
		respondJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	payload, err := startCodexOAuthSession(true)
	if err != nil {
		stopCodexLoopbackCallbackServersIfIdle()
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, payload)
}

func startCodexOAuthSession(autoComplete bool) (map[string]any, error) {
	// Generate PKCE verifier and challenge
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, fmt.Errorf("failed to generate verifier")
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	challengeHash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])

	// Generate state
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, fmt.Errorf("failed to generate state")
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	// Build OAuth URL
	u, _ := url.Parse(CodexOAuthAuthorizeURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", CodexOAuthClientID)
	q.Set("redirect_uri", CodexOAuthRedirectURI)
	q.Set("scope", "openid profile email offline_access")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("state", state)
	q.Set("originator", "codex_cli_rs")
	u.RawQuery = q.Encode()

	// Store session
	session := &CodexOAuthSession{
		Verifier:     verifier,
		Challenge:    challenge,
		State:        state,
		AutoComplete: autoComplete,
		CreatedAt:    time.Now(),
	}

	codexOAuthSessions.Lock()
	codexOAuthSessions.sessions[verifier] = session
	codexOAuthSessions.Unlock()

	// Clean up old sessions
	go cleanupOldCodexSessions()

	return map[string]any{
		"oauth_url": u.String(),
		"verifier":  verifier,
		"state":     state,
	}, nil
}

// POST /admin/codex/exchange - exchange OAuth code for tokens
func (h *proxyHandler) handleCodexExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string `json:"code"`
		Verifier    string `json:"verifier"`
		State       string `json:"state"`
		CallbackURL string `json:"callback_url"`
	}

	if r.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	} else {
		req.Code = r.FormValue("code")
		req.Verifier = r.FormValue("verifier")
	}

	result, err := h.completeCodexExchange(r.Context(), codexExchangeRequest{
		Code:        req.Code,
		Verifier:    req.Verifier,
		State:       req.State,
		CallbackURL: req.CallbackURL,
		Lane:        "admin",
	})
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "invalid callback_url") ||
			strings.Contains(err.Error(), "code and verifier are required") ||
			strings.Contains(err.Error(), "invalid or expired session") {
			status = http.StatusBadRequest
		}
		log.Printf("Codex token exchange failed: %v", err)
		respondJSONError(w, status, err.Error())
		return
	}

	respondJSON(w, map[string]any{
		"success":            true,
		"account_id":         result.AccountID,
		"refreshed_existing": result.RefreshedExisting,
	})
}

func (h *proxyHandler) completeCodexExchange(ctx context.Context, req codexExchangeRequest) (codexExchangeResult, error) {
	trace := requestTraceFromContext(ctx)
	startedAt := time.Now()
	lane := firstNonEmpty(strings.TrimSpace(req.Lane), "admin")
	fail := func(err error) (codexExchangeResult, error) {
		trace.noteOAuthExchange(AccountTypeCodex, lane, "fail", "", false, time.Since(startedAt), err)
		return codexExchangeResult{}, err
	}

	code := strings.TrimSpace(req.Code)
	verifier := strings.TrimSpace(req.Verifier)
	state := strings.TrimSpace(req.State)
	callbackURL := strings.TrimSpace(req.CallbackURL)

	if callbackURL != "" {
		parsed, err := url.Parse(callbackURL)
		if err != nil {
			return fail(fmt.Errorf("invalid callback_url: %w", err))
		}
		query := parsed.Query()
		if code == "" {
			code = strings.TrimSpace(query.Get("code"))
		}
		if state == "" {
			state = strings.TrimSpace(query.Get("state"))
		}
	}

	if verifier == "" && state != "" {
		matchedVerifier, _, ok := findCodexSessionByState(state)
		if ok {
			verifier = matchedVerifier
		}
	}

	if code == "" {
		return fail(fmt.Errorf("code and verifier are required (or provide callback_url/state for state-based lookup)"))
	}
	if verifier == "" {
		if state != "" {
			return fail(fmt.Errorf("invalid or expired session"))
		}
		return fail(fmt.Errorf("code and verifier are required (or provide callback_url/state for state-based lookup)"))
	}

	_, _, ok := claimCodexSession(verifier, state)
	if !ok {
		return fail(fmt.Errorf("invalid or expired session"))
	}
	finalizeSession := false
	defer func() {
		if finalizeSession {
			finalizeCodexSession(verifier)
			return
		}
		releaseCodexSession(verifier)
	}()

	tokens, err := codexExchangeCode(code, verifier)
	if err != nil {
		return fail(fmt.Errorf("token exchange failed: %w", err))
	}

	accountID := generateCodexAccountID(tokens.IDToken)
	poolDir := filepath.Join(h.cfg.poolDir, "codex")
	savedAccountID, _, refreshedExisting, err := saveNewCodexAccount(poolDir, accountID, tokens)
	if err != nil {
		return fail(fmt.Errorf("failed to save account: %w", err))
	}
	finalizeSession = true

	if h.pool != nil && h.registry != nil {
		h.reloadAccounts()
	}

	trace.noteOAuthExchange(AccountTypeCodex, lane, "ok", savedAccountID, refreshedExisting, time.Since(startedAt), nil)
	return codexExchangeResult{
		AccountID:         savedAccountID,
		RefreshedExisting: refreshedExisting,
	}, nil
}

// codexExchangeCode exchanges an authorization code for tokens
func codexExchangeCode(code, verifier string) (*CodexTokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", CodexOAuthClientID)
	data.Set("code", code)
	data.Set("redirect_uri", CodexOAuthRedirectURI)
	data.Set("code_verifier", verifier)

	req, err := http.NewRequest(http.MethodPost, CodexOAuthTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := codexOAuthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s: %s", resp.Status, string(body))
	}

	var tokens CodexTokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in response")
	}

	return &tokens, nil
}

// generateCodexAccountID generates an account ID from the id_token email
func generateCodexAccountID(idToken string) string {
	// Parse JWT to get email
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return fmt.Sprintf("codex_%d", time.Now().Unix())
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Sprintf("codex_%d", time.Now().Unix())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Sprintf("codex_%d", time.Now().Unix())
	}

	// Try to get email from profile claim
	email := ""
	if profile, ok := payload["https://api.openai.com/profile"].(map[string]interface{}); ok {
		if e, ok := profile["email"].(string); ok {
			email = e
		}
	}
	if email == "" {
		if e, ok := payload["email"].(string); ok {
			email = e
		}
	}

	if email == "" {
		return fmt.Sprintf("codex_%d", time.Now().Unix())
	}

	// Extract meaningful part from email
	// e.g., "dlssnetsec+1@gmail.com" -> "dlss_1"
	// e.g., "foo@bar.com" -> "foo"
	localPart := strings.Split(email, "@")[0]

	// Handle plus aliases: user+alias -> user_alias
	localPart = strings.ReplaceAll(localPart, "+", "_")

	// Truncate long prefixes, keep suffix
	// e.g., "dlssnetsec_1" -> "dlss_1"
	re := regexp.MustCompile(`^([a-zA-Z]{1,4})[a-zA-Z]*(_\d+)?$`)
	if matches := re.FindStringSubmatch(localPart); len(matches) > 0 {
		result := matches[1]
		if len(matches) > 2 && matches[2] != "" {
			result += matches[2]
		}
		return result
	}

	// Fallback: just use first 8 chars of local part
	if len(localPart) > 8 {
		localPart = localPart[:8]
	}
	return localPart
}

func findExistingCodexSeatFile(poolDir string, claims codexJWTClaims) (string, string, bool) {
	if claims.ChatGPTAccountID == "" {
		return "", "", false
	}
	entries, err := os.ReadDir(poolDir)
	if err != nil {
		return "", "", false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(poolDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var authJSON CodexAuthJSON
		if err := json.Unmarshal(data, &authJSON); err != nil || authJSON.Tokens == nil {
			continue
		}
		existingClaims := parseCodexClaims(authJSON.Tokens.IDToken)
		if existingClaims.ChatGPTAccountID == "" {
			continue
		}
		sameSeat := false
		if claims.ChatGPTUserID != "" && existingClaims.ChatGPTUserID != "" {
			sameSeat = claims.ChatGPTUserID == existingClaims.ChatGPTUserID &&
				claims.ChatGPTAccountID == existingClaims.ChatGPTAccountID
		} else if claims.Email != "" && existingClaims.Email != "" {
			sameSeat = strings.EqualFold(claims.Email, existingClaims.Email) &&
				claims.ChatGPTAccountID == existingClaims.ChatGPTAccountID
		}
		if sameSeat {
			return strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), path, true
		}
	}
	return "", "", false
}

// saveNewCodexAccount saves a new Codex account to the pool directory.
// If the same logical Codex seat is already present, the existing file is refreshed in place.
func saveNewCodexAccount(poolDir, accountID string, tokens *CodexTokenResponse) (string, string, bool, error) {
	// Ensure pool directory exists
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		return "", "", false, fmt.Errorf("create pool dir: %w", err)
	}

	filePath := filepath.Join(poolDir, accountID+".json")
	claims := parseCodexClaims(tokens.IDToken)
	reusedExistingSeat := false

	if existingID, existingPath, ok := findExistingCodexSeatFile(poolDir, claims); ok {
		accountID = existingID
		filePath = existingPath
		reusedExistingSeat = true
	}

	// Check if file already exists
	if _, err := os.Stat(filePath); err == nil && !reusedExistingSeat {
		// File exists, append a number
		for i := 2; i <= 99; i++ {
			newPath := filepath.Join(poolDir, fmt.Sprintf("%s_%d.json", accountID, i))
			if _, err := os.Stat(newPath); os.IsNotExist(err) {
				filePath = newPath
				accountID = fmt.Sprintf("%s_%d", accountID, i)
				break
			}
		}
	}

	authJSON := map[string]any{
		"tokens": map[string]any{
			"id_token":      tokens.IDToken,
			"access_token":  tokens.AccessToken,
			"refresh_token": tokens.RefreshToken,
		},
	}
	if claims.ChatGPTAccountID != "" {
		authJSON["tokens"].(map[string]any)["account_id"] = claims.ChatGPTAccountID
	}

	data, err := json.MarshalIndent(authJSON, "", "  ")
	if err != nil {
		return "", "", reusedExistingSeat, fmt.Errorf("marshal json: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return "", "", reusedExistingSeat, fmt.Errorf("write file: %w", err)
	}

	log.Printf("Saved Codex account: %s -> %s", accountID, filePath)
	return accountID, filePath, reusedExistingSeat, nil
}

func cleanupOldCodexSessions() {
	codexOAuthSessions.Lock()
	now := time.Now()
	for verifier, session := range codexOAuthSessions.sessions {
		if now.Sub(session.CreatedAt) > 10*time.Minute {
			delete(codexOAuthSessions.sessions, verifier)
		}
	}
	codexOAuthSessions.Unlock()

	scheduleStopCodexLoopbackCallbackServersIfIdle()
}
