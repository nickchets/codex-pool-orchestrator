package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	managedGeminiSubdir           = "gemini"
	managedGeminiProbeTimeout     = 8 * time.Second
	managedGeminiRateLimitWait    = 45 * time.Second
	managedGeminiOAuthAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	managedGeminiOAuthUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	managedGeminiOAuthSessionTTL  = 15 * time.Minute
)

var managedGeminiOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

type managedGeminiSeatAddOutcome struct {
	AccountID     string
	Created       bool
	ProbeOK       bool
	ProbeError    string
	HealthStatus  string
	HealthError   string
	Dead          bool
	AuthExpiresAt string
}

type managedGeminiOAuthSession struct {
	State        string
	CodeVerifier string
	RedirectURI  string
	ProfileID    string
	ClientID     string
	ClientSecret string
	CreatedAt    time.Time
}

type managedGeminiOAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

type managedGeminiOAuthUserInfo struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

var managedGeminiOAuthSessions = struct {
	sync.Mutex
	sessions map[string]*managedGeminiOAuthSession
}{
	sessions: make(map[string]*managedGeminiOAuthSession),
}

func managedGeminiSeatID(refreshToken, accessToken string) string {
	seed := strings.TrimSpace(refreshToken)
	if seed == "" {
		seed = strings.TrimSpace(accessToken)
	}
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("gemini_seat_%x", sum[:6])
}

func saveManagedGeminiSeat(poolDir, rawAuthJSON string) (*Account, bool, error) {
	payload := strings.TrimSpace(rawAuthJSON)
	if payload == "" {
		return nil, false, fmt.Errorf("auth_json is empty")
	}

	var root map[string]any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return nil, false, fmt.Errorf("parse auth_json: %w", err)
	}
	if root == nil {
		return nil, false, fmt.Errorf("auth_json must be a JSON object")
	}

	var gj GeminiAuthJSON
	if err := json.Unmarshal([]byte(payload), &gj); err != nil {
		return nil, false, fmt.Errorf("parse auth_json: %w", err)
	}
	if strings.TrimSpace(gj.AccessToken) == "" {
		return nil, false, fmt.Errorf("gemini access_token is required")
	}
	if strings.TrimSpace(gj.RefreshToken) == "" {
		return nil, false, fmt.Errorf("gemini refresh_token is required")
	}

	dir := filepath.Join(poolDir, managedGeminiSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}

	accountID := managedGeminiSeatID(gj.RefreshToken, gj.AccessToken)
	path := filepath.Join(dir, accountID+".json")
	_, statErr := os.Stat(path)
	created := os.IsNotExist(statErr)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, false, statErr
	}

	root["auth_mode"] = accountAuthModeOAuth
	root["plan_type"] = firstNonEmpty(strings.TrimSpace(gj.PlanType), "gemini")
	root["health_status"] = "unknown"
	delete(root, "disabled")
	delete(root, "dead")
	delete(root, "rate_limit_until")
	delete(root, "last_refresh")
	delete(root, "health_checked_at")
	delete(root, "last_healthy_at")
	delete(root, "health_error")
	if err := atomicWriteJSON(path, root); err != nil {
		return nil, false, err
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	acc, err := (&GeminiProvider{}).LoadAccount(filepath.Base(path), path, updated)
	if err != nil {
		return nil, false, err
	}
	if acc == nil {
		return nil, false, fmt.Errorf("gemini seat could not be loaded after save")
	}
	acc.AuthMode = accountAuthModeOAuth
	return acc, created, nil
}

func (h *proxyHandler) probeManagedGeminiSeat(ctx context.Context, acc *Account) error {
	if h == nil || acc == nil || acc.Type != AccountTypeGemini {
		return nil
	}

	provider := h.registry.ForType(AccountTypeGemini)
	if provider == nil {
		return fmt.Errorf("no provider for account type %s", AccountTypeGemini)
	}

	transport := h.operatorGeminiTransport()
	probeCtx, cancel := context.WithTimeout(ctx, managedGeminiProbeTimeout)
	defer cancel()

	err := provider.RefreshToken(probeCtx, acc, transport)
	now := time.Now().UTC()
	if err == nil {
		acc.mu.Lock()
		acc.AuthMode = accountAuthModeOAuth
		setAccountDeadStateLocked(acc, false, now)
		acc.HealthStatus = "healthy"
		acc.HealthError = ""
		acc.HealthCheckedAt = now
		acc.LastHealthyAt = now
		acc.RateLimitUntil = time.Time{}
		acc.mu.Unlock()
		if saveErr := saveAccount(acc); saveErr != nil {
			log.Printf("warning: failed to persist managed gemini seat %s probe success: %v", acc.ID, saveErr)
		}
		return nil
	}

	msg := strings.ToLower(err.Error())
	acc.mu.Lock()
	acc.AuthMode = accountAuthModeOAuth
	setAccountDeadStateLocked(acc, false, now)
	acc.HealthCheckedAt = now
	acc.HealthError = sanitizeStatusMessage(err.Error())
	switch {
	case isRateLimitError(err):
		acc.HealthStatus = "rate_limited"
		until := now.Add(managedGeminiRateLimitWait)
		if acc.RateLimitUntil.Before(until) {
			acc.RateLimitUntil = until
		}
	case strings.Contains(msg, "invalid_grant"),
		strings.Contains(msg, "refresh_token_reused"),
		strings.Contains(msg, "gemini refresh unauthorized"),
		strings.Contains(msg, "no refresh token"):
		setAccountDeadStateLocked(acc, true, now)
		acc.HealthStatus = "dead"
		acc.RateLimitUntil = time.Time{}
	default:
		acc.HealthStatus = "error"
	}
	acc.mu.Unlock()

	if saveErr := saveAccount(acc); saveErr != nil {
		log.Printf("warning: failed to persist managed gemini seat %s probe failure: %v", acc.ID, saveErr)
	}
	return fmt.Errorf("managed gemini seat probe failed: %w", err)
}

func (h *proxyHandler) addManagedGeminiSeat(ctx context.Context, rawAuthJSON string) (*managedGeminiSeatAddOutcome, error) {
	acc, created, err := saveManagedGeminiSeat(h.cfg.poolDir, rawAuthJSON)
	if err != nil {
		return nil, err
	}

	probeErr := h.probeManagedGeminiSeat(ctx, acc)
	if probeErr != nil {
		log.Printf("managed gemini seat %s probe failed during add: %v", acc.ID, probeErr)
	}

	h.reloadAccounts()

	live, liveOK := h.snapshotAccountByID(acc.ID, time.Now())
	outcome := &managedGeminiSeatAddOutcome{
		AccountID: acc.ID,
		Created:   created,
		ProbeOK:   probeErr == nil,
		ProbeError: sanitizeStatusMessage(func() string {
			if probeErr == nil {
				return ""
			}
			return probeErr.Error()
		}()),
		HealthStatus: firstNonEmpty(strings.TrimSpace(acc.HealthStatus), "unknown"),
		HealthError:  sanitizeStatusMessage(acc.HealthError),
		Dead:         acc.Dead,
	}
	if liveOK {
		outcome.HealthStatus = firstNonEmpty(strings.TrimSpace(live.HealthStatus), "unknown")
		outcome.HealthError = sanitizeStatusMessage(live.HealthError)
		outcome.Dead = live.Dead
		if !live.ExpiresAt.IsZero() {
			outcome.AuthExpiresAt = live.ExpiresAt.UTC().Format(time.RFC3339)
		}
	} else if !acc.ExpiresAt.IsZero() {
		outcome.AuthExpiresAt = acc.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return outcome, nil
}

func (h *proxyHandler) handleOperatorGeminiSeatAdd(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		AuthJSON string `json:"auth_json"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&payload); err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	authJSON := strings.TrimSpace(payload.AuthJSON)
	if authJSON == "" {
		respondJSONError(w, http.StatusBadRequest, "auth_json is required")
		return
	}

	outcome, err := h.addManagedGeminiSeat(r.Context(), authJSON)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondJSON(w, map[string]any{
		"status":          "ok",
		"account_id":      outcome.AccountID,
		"created":         outcome.Created,
		"probe_ok":        outcome.ProbeOK,
		"probe_error":     outcome.ProbeError,
		"health_status":   outcome.HealthStatus,
		"health_error":    outcome.HealthError,
		"dead":            outcome.Dead,
		"auth_expires_at": outcome.AuthExpiresAt,
	})
}

func (h *proxyHandler) handleOperatorGeminiOAuthStart(w http.ResponseWriter, r *http.Request) {
	redirectURI, err := managedGeminiRedirectURI(r)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	codeVerifier, codeChallenge, state, err := generateManagedGeminiOAuthPKCE()
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	profile := geminiOAuthDefaultProfile()
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Secret) == "" {
		respondJSONError(w, http.StatusServiceUnavailable, geminiOAuthConfigError().Error())
		return
	}
	oauthURL, err := buildManagedGeminiOAuthURL(profile, redirectURI, codeChallenge, state)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	storeManagedGeminiOAuthSession(&managedGeminiOAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
		ProfileID:    geminiOAuthProfileIDForLabel(profile.Label),
		ClientID:     profile.ID,
		ClientSecret: profile.Secret,
		CreatedAt:    time.Now().UTC(),
	})
	go cleanupManagedGeminiOAuthSessions()

	respondJSON(w, map[string]any{
		"status":    "ok",
		"oauth_url": oauthURL,
		"state":     state,
	})
}

func (h *proxyHandler) handleOperatorGeminiOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if errCode := strings.TrimSpace(r.URL.Query().Get("error")); errCode != "" {
		desc := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if desc == "" {
			desc = "Google OAuth was cancelled or rejected."
		}
		serveManagedGeminiOAuthPopupResult(w, false, nil, fmt.Sprintf("Google OAuth error: %s. %s", errCode, desc))
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		serveManagedGeminiOAuthPopupResult(w, false, nil, "Missing OAuth state or authorization code.")
		return
	}

	session, ok := claimManagedGeminiOAuthSession(state)
	if !ok {
		serveManagedGeminiOAuthPopupResult(w, false, nil, "The Gemini OAuth session is missing or expired. Start the flow again.")
		return
	}

	outcome, err := h.completeManagedGeminiOAuth(r.Context(), code, session)
	if err != nil {
		serveManagedGeminiOAuthPopupResult(w, false, nil, err.Error())
		return
	}

	serveManagedGeminiOAuthPopupResult(w, true, outcome, "")
}

func (h *proxyHandler) completeManagedGeminiOAuth(ctx context.Context, code string, session *managedGeminiOAuthSession) (*managedGeminiSeatAddOutcome, error) {
	tokens, err := h.exchangeManagedGeminiOAuthCode(ctx, code, session)
	if err != nil {
		return nil, err
	}

	userInfo, _ := h.fetchManagedGeminiOAuthUserInfo(ctx, tokens.AccessToken)
	authJSON, err := buildManagedGeminiOAuthAuthJSON(tokens, userInfo, session)
	if err != nil {
		return nil, err
	}
	return h.addManagedGeminiSeat(ctx, authJSON)
}

func (h *proxyHandler) exchangeManagedGeminiOAuthCode(ctx context.Context, code string, session *managedGeminiOAuthSession) (*managedGeminiOAuthTokens, error) {
	profile, ok := geminiOAuthProfileByID(session.ProfileID)
	if !ok {
		profile = geminiOAuthClientProfile{
			ID:     strings.TrimSpace(session.ClientID),
			Secret: strings.TrimSpace(session.ClientSecret),
			Label:  "raw",
		}
	}
	if profile.ID == "" || profile.Secret == "" {
		profile = geminiOAuthDefaultProfile()
	}
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Secret) == "" {
		return nil, geminiOAuthConfigError()
	}

	form := url.Values{}
	form.Set("client_id", profile.ID)
	form.Set("client_secret", profile.Secret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", session.RedirectURI)
	if strings.TrimSpace(session.CodeVerifier) != "" {
		form.Set("code_verifier", session.CodeVerifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-pool-proxy")

	resp, err := h.operatorGeminiTransport().RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		if len(strings.TrimSpace(string(body))) > 0 {
			return nil, fmt.Errorf("gemini oauth exchange failed: %s: %s", resp.Status, safeText(body))
		}
		return nil, fmt.Errorf("gemini oauth exchange failed: %s", resp.Status)
	}

	var payload managedGeminiOAuthTokens
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("gemini oauth exchange returned an empty access token")
	}
	if strings.TrimSpace(payload.RefreshToken) == "" {
		return nil, fmt.Errorf("gemini oauth exchange did not return a refresh token; retry and approve offline access")
	}
	payload.TokenType = firstNonEmpty(strings.TrimSpace(payload.TokenType), "Bearer")
	return &payload, nil
}

func (h *proxyHandler) fetchManagedGeminiOAuthUserInfo(ctx context.Context, accessToken string) (*managedGeminiOAuthUserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("access token is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, managedGeminiOAuthUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-pool-proxy")

	resp, err := h.operatorGeminiTransport().RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		if len(strings.TrimSpace(string(body))) > 0 {
			return nil, fmt.Errorf("gemini userinfo failed: %s: %s", resp.Status, safeText(body))
		}
		return nil, fmt.Errorf("gemini userinfo failed: %s", resp.Status)
	}

	var payload managedGeminiOAuthUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func buildManagedGeminiOAuthAuthJSON(tokens *managedGeminiOAuthTokens, userInfo *managedGeminiOAuthUserInfo, session *managedGeminiOAuthSession) (string, error) {
	if tokens == nil {
		return "", fmt.Errorf("gemini oauth tokens are required")
	}

	root := map[string]any{
		"access_token":  strings.TrimSpace(tokens.AccessToken),
		"refresh_token": strings.TrimSpace(tokens.RefreshToken),
		"token_type":    firstNonEmpty(strings.TrimSpace(tokens.TokenType), "Bearer"),
		"scope":         strings.TrimSpace(tokens.Scope),
		"plan_type":     "gemini",
	}
	if session != nil {
		if profileID := strings.TrimSpace(session.ProfileID); profileID != "" {
			root["oauth_profile_id"] = profileID
		} else {
			if clientID := strings.TrimSpace(session.ClientID); clientID != "" {
				root["client_id"] = clientID
			}
			if clientSecret := strings.TrimSpace(session.ClientSecret); clientSecret != "" {
				root["client_secret"] = clientSecret
			}
		}
	}
	if tokens.ExpiresIn > 0 {
		root["expiry_date"] = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()
	}
	if userInfo != nil {
		if email := strings.TrimSpace(userInfo.Email); email != "" {
			root["operator_email"] = email
		}
		if name := strings.TrimSpace(userInfo.Name); name != "" {
			root["operator_name"] = name
		}
	}

	payload, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (h *proxyHandler) operatorGeminiTransport() http.RoundTripper {
	if h != nil && h.refreshTransport != nil {
		return h.refreshTransport
	}
	if h != nil && h.transport != nil {
		return h.transport
	}
	return http.DefaultTransport
}

func generateManagedGeminiOAuthPKCE() (string, string, string, error) {
	verifierBytes := make([]byte, 48)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", "", fmt.Errorf("failed to generate gemini oauth verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	stateBytes := make([]byte, 24)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", "", fmt.Errorf("failed to generate gemini oauth state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	return verifier, challenge, state, nil
}

func managedGeminiRedirectURI(r *http.Request) (string, error) {
	if r == nil {
		return "", fmt.Errorf("request is required")
	}
	host := strings.TrimSpace(r.Host)
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("loopback host required for Gemini OAuth")
	}

	redirectHost := host
	port := ""
	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		redirectHost = parsedHost
		port = strings.TrimSpace(parsedPort)
	} else if r.TLS != nil {
		port = "443"
	} else {
		port = "80"
	}
	if port == "" {
		return "", fmt.Errorf("loopback port required for Gemini OAuth")
	}

	redirectHost = strings.TrimSpace(strings.Trim(redirectHost, "[]"))
	if redirectHost == "" {
		return "", fmt.Errorf("loopback host required for Gemini OAuth")
	}
	if strings.Contains(redirectHost, ":") {
		redirectHost = "[" + redirectHost + "]"
	}

	return "http://" + redirectHost + ":" + port + "/operator/gemini/oauth-callback", nil
}

func buildManagedGeminiOAuthURL(profile geminiOAuthClientProfile, redirectURI, codeChallenge, state string) (string, error) {
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Secret) == "" {
		return "", geminiOAuthConfigError()
	}
	u, err := url.Parse(managedGeminiOAuthAuthURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", profile.ID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("access_type", "offline")
	q.Set("scope", strings.Join(managedGeminiOAuthScopes, " "))
	q.Set("state", state)
	q.Set("prompt", "consent select_account")
	q.Set("include_granted_scopes", "true")
	if strings.TrimSpace(codeChallenge) != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func storeManagedGeminiOAuthSession(session *managedGeminiOAuthSession) {
	if session == nil || strings.TrimSpace(session.State) == "" {
		return
	}
	managedGeminiOAuthSessions.Lock()
	managedGeminiOAuthSessions.sessions[session.State] = session
	managedGeminiOAuthSessions.Unlock()
}

func claimManagedGeminiOAuthSession(state string) (*managedGeminiOAuthSession, bool) {
	key := strings.TrimSpace(state)
	if key == "" {
		return nil, false
	}

	managedGeminiOAuthSessions.Lock()
	defer managedGeminiOAuthSessions.Unlock()

	session, ok := managedGeminiOAuthSessions.sessions[key]
	if !ok || session == nil {
		return nil, false
	}
	delete(managedGeminiOAuthSessions.sessions, key)
	if session.CreatedAt.IsZero() || session.CreatedAt.Before(time.Now().Add(-managedGeminiOAuthSessionTTL)) {
		return nil, false
	}
	return session, true
}

func cleanupManagedGeminiOAuthSessions() {
	cutoff := time.Now().Add(-managedGeminiOAuthSessionTTL)
	managedGeminiOAuthSessions.Lock()
	for state, session := range managedGeminiOAuthSessions.sessions {
		if session == nil || session.CreatedAt.Before(cutoff) {
			delete(managedGeminiOAuthSessions.sessions, state)
		}
	}
	managedGeminiOAuthSessions.Unlock()
}

func serveManagedGeminiOAuthPopupResult(w http.ResponseWriter, ok bool, outcome *managedGeminiSeatAddOutcome, errMessage string) {
	payload := map[string]any{
		"type": "gemini_oauth_result",
		"ok":   ok,
	}

	title := "Gemini OAuth Failed"
	heading := "Gemini OAuth failed"
	body := sanitizeStatusMessage(errMessage)
	if ok && outcome != nil {
		title = "Gemini OAuth Complete"
		heading = "Gemini seat added"
		body = "Gemini OAuth completed. Reloading the operator dashboard."
		if !outcome.Created {
			heading = "Gemini seat refreshed"
			body = "Gemini OAuth completed. An existing Gemini seat was refreshed."
		}
		payload["account_id"] = outcome.AccountID
		payload["created"] = outcome.Created
		payload["probe_ok"] = outcome.ProbeOK
		payload["health_status"] = outcome.HealthStatus
		payload["health_error"] = outcome.HealthError
		payload["dead"] = outcome.Dead
		payload["auth_expires_at"] = outcome.AuthExpiresAt
		payload["message"] = body
	} else {
		if body == "" {
			body = "Gemini OAuth did not complete."
		}
		payload["message"] = body
	}

	rawPayload, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>%s</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #0d1117; color: #c9d1d9; margin: 0; padding: 24px; }
    .card { max-width: 640px; margin: 48px auto; background: #161b22; border: 1px solid #30363d; border-radius: 12px; padding: 24px; }
    h1 { margin: 0 0 12px; font-size: 24px; }
    p { margin: 0; line-height: 1.5; }
  </style>
</head>
<body>
  <div class="card">
    <h1>%s</h1>
    <p>%s</p>
  </div>
  <script>
    const payload = %s;
    try {
      if (window.opener && !window.opener.closed) {
        window.opener.postMessage(payload, '*');
      }
    } catch (error) {}
    window.setTimeout(() => {
      try { window.close(); } catch (error) {}
    }, 80);
  </script>
</body>
</html>`,
		template.HTMLEscapeString(title),
		template.HTMLEscapeString(heading),
		template.HTMLEscapeString(body),
		string(rawPayload),
	)
}
