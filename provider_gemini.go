package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	geminiOAuthTokenURL              = "https://oauth2.googleapis.com/token"
	geminiOAuthAuthorizeURL          = "https://accounts.google.com/o/oauth2/auth"
	geminiOAuthInteractiveScope      = "openid https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/appengine.admin https://www.googleapis.com/auth/sqlservice.login https://www.googleapis.com/auth/compute https://www.googleapis.com/auth/accounts.reauth"
	geminiOAuthEnvClientIDVar        = "GEMINI_OAUTH_CLIENT_ID"
	geminiOAuthEnvClientSecretVar    = "GEMINI_OAUTH_CLIENT_SECRET"
	geminiOAuthCLIClientIDVar        = "GEMINI_OAUTH_CLI_CLIENT_ID"
	geminiOAuthCLIClientSecretVar    = "GEMINI_OAUTH_CLI_CLIENT_SECRET"
	geminiOAuthGCloudClientIDVar     = "GEMINI_OAUTH_GCLOUD_CLIENT_ID"
	geminiOAuthGCloudClientSecretVar = "GEMINI_OAUTH_GCLOUD_CLIENT_SECRET"
	geminiOAuthAntigravityProfileID  = "antigravity_public"
	geminiOAuthAntigravityClientID   = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	geminiOAuthAntigravitySecretVar  = "ANTIGRAVITY_GEMINI_OAUTH_CLIENT_SECRET"
)

type geminiOAuthClientProfile struct {
	ID     string
	Secret string
	Label  string
}

func geminiOAuthProfileFromEnv(label, idVar, secretVar string) (geminiOAuthClientProfile, bool) {
	clientID := strings.TrimSpace(os.Getenv(idVar))
	clientSecret := strings.TrimSpace(os.Getenv(secretVar))
	if clientID == "" || clientSecret == "" {
		return geminiOAuthClientProfile{}, false
	}
	return geminiOAuthClientProfile{ID: clientID, Secret: clientSecret, Label: label}, true
}

func geminiOAuthConfigError() error {
	return errors.New("no configured Gemini OAuth client; set GEMINI_OAUTH_GCLOUD_CLIENT_ID/GEMINI_OAUTH_GCLOUD_CLIENT_SECRET or GEMINI_OAUTH_CLIENT_ID/GEMINI_OAUTH_CLIENT_SECRET")
}

func geminiOAuthAntigravityProfile() geminiOAuthClientProfile {
	return geminiOAuthClientProfile{
		ID:     geminiOAuthAntigravityClientID,
		Secret: strings.TrimSpace(os.Getenv(geminiOAuthAntigravitySecretVar)),
		Label:  geminiOAuthAntigravityProfileID,
	}
}

func geminiOAuthProfileByID(id string) (geminiOAuthClientProfile, bool) {
	switch strings.TrimSpace(id) {
	case "env":
		return geminiOAuthProfileFromEnv("env", geminiOAuthEnvClientIDVar, geminiOAuthEnvClientSecretVar)
	case "gemini_cli":
		return geminiOAuthProfileFromEnv("gemini_cli", geminiOAuthCLIClientIDVar, geminiOAuthCLIClientSecretVar)
	case "gcloud":
		return geminiOAuthProfileFromEnv("gcloud", geminiOAuthGCloudClientIDVar, geminiOAuthGCloudClientSecretVar)
	case geminiOAuthAntigravityProfileID:
		return geminiOAuthAntigravityProfile(), true
	}
	return geminiOAuthClientProfile{}, false
}

func geminiOAuthProfileIDForLabel(label string) string {
	switch strings.TrimSpace(label) {
	case "env", "gemini_cli", "gcloud", geminiOAuthAntigravityProfileID:
		return strings.TrimSpace(label)
	default:
		return ""
	}
}

// GeminiProvider handles Google Gemini accounts.
type GeminiProvider struct {
	geminiBase    *url.URL // OAuth/Code Assist endpoint (cloudcode-pa.googleapis.com)
	geminiAPIBase *url.URL // API key mode endpoint (generativelanguage.googleapis.com)
}

// NewGeminiProvider creates a new Gemini provider.
func NewGeminiProvider(geminiBase, geminiAPIBase *url.URL) *GeminiProvider {
	return &GeminiProvider{
		geminiBase:    geminiBase,
		geminiAPIBase: geminiAPIBase,
	}
}

func (p *GeminiProvider) Type() AccountType {
	return AccountTypeGemini
}

func (p *GeminiProvider) SupportsAccountPath(path string, acc *Account) bool {
	if !strings.HasPrefix(path, geminiAPIModelPrefix) {
		return true
	}
	if acc == nil {
		return false
	}
	return strings.TrimSpace(acc.AntigravityProjectID) != ""
}

func (p *GeminiProvider) LoadAccount(name, path string, data []byte) (*Account, error) {
	var gj GeminiAuthJSON
	if err := json.Unmarshal(data, &gj); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if gj.AccessToken == "" {
		return nil, nil
	}
	planType := gj.PlanType
	if planType == "" {
		planType = "gemini" // default
	}
	oauthProfileID := strings.TrimSpace(gj.OAuthProfileID)
	if oauthProfileID == "" && strings.TrimSpace(gj.AntigravitySource) != "" {
		oauthProfileID = geminiOAuthAntigravityProfileID
	}
	operatorSource := storedGeminiOperatorSource(gj.OperatorSource, gj.OAuthProfileID, AccountTypeGemini)
	if operatorSource == "" && strings.TrimSpace(gj.AntigravitySource) != "" {
		operatorSource = geminiOperatorSourceAntigravityImport
	}
	if operatorSource == "" && strings.TrimSpace(gj.OperatorEmail) != "" {
		operatorSource = geminiOperatorSourceManagedOAuth
	}
	acc := &Account{
		OAuthProfileID:               oauthProfileID,
		Type:                         AccountTypeGemini,
		ID:                           strings.TrimSuffix(name, filepath.Ext(name)),
		File:                         path,
		AccessToken:                  gj.AccessToken,
		RefreshToken:                 gj.RefreshToken,
		OAuthClientID:                strings.TrimSpace(gj.ClientID),
		OAuthClientSecret:            strings.TrimSpace(gj.ClientSecret),
		OperatorSource:               operatorSource,
		OperatorEmail:                firstNonEmpty(strings.TrimSpace(gj.OperatorEmail), strings.TrimSpace(gj.AntigravityEmail)),
		PlanType:                     planType,
		AuthMode:                     accountAuthModeOAuth,
		Disabled:                     gj.Disabled,
		Dead:                         gj.Dead,
		AntigravitySource:            strings.TrimSpace(gj.AntigravitySource),
		AntigravityAccountID:         strings.TrimSpace(gj.AntigravityAccountID),
		AntigravityEmail:             strings.TrimSpace(gj.AntigravityEmail),
		AntigravityName:              strings.TrimSpace(gj.AntigravityName),
		AntigravityProjectID:         strings.TrimSpace(gj.AntigravityProjectID),
		AntigravityFile:              strings.TrimSpace(gj.AntigravityFile),
		AntigravityCurrent:           gj.AntigravityCurrent,
		AntigravityProxyDisabled:     gj.AntigravityProxyDisabled,
		AntigravityValidationBlocked: gj.AntigravityValidationBlocked,
		AntigravityQuota:             gj.AntigravityQuota,
		GeminiSubscriptionTierID:     strings.TrimSpace(gj.GeminiSubscriptionTierID),
		GeminiSubscriptionTierName:   strings.TrimSpace(gj.GeminiSubscriptionTierName),
		GeminiValidationReasonCode:   strings.TrimSpace(gj.GeminiValidationReasonCode),
		GeminiValidationMessage:      strings.TrimSpace(gj.GeminiValidationMessage),
		GeminiValidationURL:          strings.TrimSpace(gj.GeminiValidationURL),
	}
	acc.AntigravityQuotaForbidden, acc.AntigravityQuotaForbiddenReason = antigravityQuotaDisposition(acc.AntigravityQuota)
	// expiry_date is Unix timestamp in milliseconds
	if gj.ExpiryDate > 0 {
		acc.ExpiresAt = time.UnixMilli(gj.ExpiryDate)
	}
	// Load last_refresh from disk to preserve refresh rate limiting across restarts
	if gj.LastRefresh != "" {
		if t, err := time.Parse(time.RFC3339Nano, gj.LastRefresh); err == nil {
			acc.LastRefresh = t
		} else if t, err := time.Parse(time.RFC3339, gj.LastRefresh); err == nil {
			acc.LastRefresh = t
		}
	}
	if gj.RateLimitUntil != nil {
		acc.RateLimitUntil = gj.RateLimitUntil.UTC()
	}
	if gj.HealthCheckedAt != nil {
		acc.HealthCheckedAt = gj.HealthCheckedAt.UTC()
	}
	if gj.LastHealthyAt != nil {
		acc.LastHealthyAt = gj.LastHealthyAt.UTC()
	}
	if gj.DeadSince != nil {
		acc.DeadSince = gj.DeadSince.UTC()
	}
	if gj.GeminiProviderCheckedAt != nil {
		acc.GeminiProviderCheckedAt = gj.GeminiProviderCheckedAt.UTC()
	}
	acc.HealthStatus = strings.TrimSpace(gj.HealthStatus)
	acc.HealthError = strings.TrimSpace(gj.HealthError)
	return acc, nil
}

func (p *GeminiProvider) SetAuthHeaders(req *http.Request, acc *Account) {
	// Gemini uses Bearer token
	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
}

func geminiOAuthDefaultProfile() geminiOAuthClientProfile {
	if profile, ok := geminiOAuthProfileByID("gcloud"); ok {
		return profile
	}
	if profile, ok := geminiOAuthProfileByID("env"); ok {
		return profile
	}
	if profile, ok := geminiOAuthProfileByID("gemini_cli"); ok {
		return profile
	}
	return geminiOAuthClientProfile{}
}

func geminiOAuthRefreshProfiles(operatorSource, profileID, explicitID, explicitSecret string) []geminiOAuthClientProfile {
	seen := make(map[string]struct{})
	profiles := make([]geminiOAuthClientProfile, 0, 5)
	add := func(profile geminiOAuthClientProfile) {
		profile.ID = strings.TrimSpace(profile.ID)
		profile.Secret = strings.TrimSpace(profile.Secret)
		if profile.ID == "" {
			return
		}
		key := profile.ID + "\x00" + profile.Secret
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		profiles = append(profiles, profile)
	}
	addManagedDefaults := func() {
		add(geminiOAuthDefaultProfile())
		for _, candidateID := range []string{"gcloud", "env", "gemini_cli"} {
			if profile, ok := geminiOAuthProfileByID(candidateID); ok {
				add(profile)
			}
		}
	}
	normalizedSource := normalizeGeminiOperatorSource(operatorSource, profileID, AccountTypeGemini)

	if resolved, ok := geminiOAuthProfileByID(profileID); ok {
		add(resolved)
	}

	if normalizedSource == geminiOperatorSourceManagedOAuth {
		addManagedDefaults()
	}

	add(geminiOAuthClientProfile{
		ID:     explicitID,
		Secret: explicitSecret,
		Label:  "raw",
	})

	if normalizedSource == geminiOperatorSourceAntigravityImport || strings.TrimSpace(profileID) == geminiOAuthAntigravityProfileID {
		return profiles
	}

	if normalizedSource != geminiOperatorSourceManagedOAuth {
		if profile, ok := geminiOAuthProfileByID("env"); ok {
			add(profile)
		}
		if profile, ok := geminiOAuthProfileByID("gemini_cli"); ok {
			add(profile)
		}
		if profile, ok := geminiOAuthProfileByID("gcloud"); ok {
			add(profile)
		}
	}

	return profiles
}

func refreshGeminiTokenWithClient(ctx context.Context, refreshTok string, profile geminiOAuthClientProfile, transport http.RoundTripper) (struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}, bool, error) {
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
	}

	form := url.Values{}
	form.Set("client_id", profile.ID)
	if strings.TrimSpace(profile.Secret) != "" {
		form.Set("client_secret", profile.Secret)
	}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshTok)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return payload, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-pool-proxy")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return payload, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		if len(bytes.TrimSpace(msg)) > 0 {
			return payload, true, fmt.Errorf("gemini refresh unauthorized: %s: %s", resp.Status, safeText(msg))
		}
		return payload, true, fmt.Errorf("gemini refresh unauthorized: %s", resp.Status)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		msgText := strings.ToLower(safeText(msg))
		if resp.StatusCode == http.StatusBadRequest && (strings.Contains(msgText, "invalid_grant") || strings.Contains(msgText, "invalid_client")) {
			if len(bytes.TrimSpace(msg)) > 0 {
				return payload, true, fmt.Errorf("gemini refresh failed: %s: %s", resp.Status, safeText(msg))
			}
			return payload, true, fmt.Errorf("gemini refresh failed: %s", resp.Status)
		}
		if len(bytes.TrimSpace(msg)) > 0 {
			return payload, false, fmt.Errorf("gemini refresh failed: %s: %s", resp.Status, safeText(msg))
		}
		return payload, false, fmt.Errorf("gemini refresh failed: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return payload, false, err
	}
	if payload.AccessToken == "" {
		return payload, false, errors.New("empty access token after gemini refresh")
	}
	return payload, false, nil
}

func (p *GeminiProvider) RefreshToken(ctx context.Context, acc *Account, transport http.RoundTripper) error {
	acc.mu.Lock()
	refreshTok := acc.RefreshToken
	operatorSource := acc.OperatorSource
	explicitProfileID := acc.OAuthProfileID
	explicitClientID := acc.OAuthClientID
	explicitClientSecret := acc.OAuthClientSecret
	acc.mu.Unlock()

	if refreshTok == "" {
		return errors.New("no refresh token")
	}

	var (
		payload struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			TokenType    string `json:"token_type"`
			Scope        string `json:"scope"`
		}
		lastFallbackable error
	)
	for _, profile := range geminiOAuthRefreshProfiles(operatorSource, explicitProfileID, explicitClientID, explicitClientSecret) {
		nextPayload, fallbackable, err := refreshGeminiTokenWithClient(ctx, refreshTok, profile, transport)
		if err == nil {
			payload = nextPayload
			acc.mu.Lock()
			if profileID := geminiOAuthProfileIDForLabel(profile.Label); profileID != "" {
				acc.OAuthProfileID = profileID
				acc.OAuthClientID = ""
				acc.OAuthClientSecret = ""
			} else {
				acc.OAuthProfileID = ""
				acc.OAuthClientID = profile.ID
				acc.OAuthClientSecret = profile.Secret
			}
			acc.OperatorSource = normalizeGeminiOperatorSource(acc.OperatorSource, acc.OAuthProfileID, acc.Type)
			acc.AccessToken = payload.AccessToken
			if payload.RefreshToken != "" {
				acc.RefreshToken = payload.RefreshToken
			}
			if payload.ExpiresIn > 0 {
				acc.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
			}
			acc.LastRefresh = time.Now().UTC()
			setAccountDeadStateLocked(acc, false, acc.LastRefresh)
			acc.mu.Unlock()
			return saveAccount(acc)
		}
		if fallbackable {
			lastFallbackable = err
			continue
		}
		return err
	}
	if lastFallbackable != nil {
		return lastFallbackable
	}
	return geminiOAuthConfigError()

}

func (p *GeminiProvider) ParseUsage(obj map[string]any) *RequestUsage {
	return parseGeminiUsagePayload(obj)
}

func (p *GeminiProvider) ParseUsageHeaders(acc *Account, headers http.Header) {
	// Gemini doesn't currently expose usage via response headers
	// This is a no-op for now
}

func (p *GeminiProvider) UpstreamURL(path string) *url.URL {
	// API key mode (/v1beta/) uses the standard Gemini API with OAuth Bearer auth
	// The generativelanguage.googleapis.com endpoint accepts OAuth tokens with cloud-platform scope
	if strings.HasPrefix(path, "/v1beta/") {
		return p.geminiAPIBase
	}
	// OAuth/Code Assist mode (/v1internal:) uses cloudcode-pa.googleapis.com
	return p.geminiBase
}

func (p *GeminiProvider) MatchesPath(path string) bool {
	// Code Assist paths: /v1internal:generateContent, /v1internal:streamGenerateContent
	// API Key mode paths: /v1beta/models/{model}:generateContent, /v1beta/models/{model}:streamGenerateContent
	return strings.HasPrefix(path, "/v1internal:") || strings.HasPrefix(path, "/v1beta/")
}

func (p *GeminiProvider) NormalizePath(path string) string {
	// Paths are used as-is - each endpoint type gets routed to its matching upstream
	return path
}

func (p *GeminiProvider) DetectsSSE(path string, contentType string) bool {
	// Gemini streaming uses streamGenerateContent
	return strings.Contains(path, "stream")
}
