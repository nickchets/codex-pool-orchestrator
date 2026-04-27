package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Pool user admin handlers - JSON API only

// servePoolUsersAdmin routes pool user admin requests (auth already checked by router)
func (h *proxyHandler) servePoolUsersAdmin(w http.ResponseWriter, r *http.Request) {
	if h.poolUsers == nil {
		http.Error(w, "pool users not configured", http.StatusServiceUnavailable)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/admin/pool-users")
	if path == "" {
		path = "/"
	}

	switch {
	case h.routePoolUserAPIKeysAdmin(w, r, path):
		return

	case path == "/" && r.Method == http.MethodGet:
		h.handlePoolUsersList(w, r)

	case path == "/" && r.Method == http.MethodPost:
		h.handlePoolUsersCreate(w, r)

	case strings.HasPrefix(path, "/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(path, "/")
		id = strings.TrimSuffix(id, "/")
		h.handlePoolUserDelete(w, r, id)

	// Support POST with /disable suffix for backwards compatibility
	case strings.HasSuffix(path, "/disable") && r.Method == http.MethodPost:
		id := strings.TrimPrefix(path, "/")
		id = strings.TrimSuffix(id, "/disable")
		h.handlePoolUserDelete(w, r, id)

	default:
		http.NotFound(w, r)
	}
}

// GET /admin/pool-users - list all pool users
func (h *proxyHandler) handlePoolUsersList(w http.ResponseWriter, r *http.Request) {
	users := h.poolUsers.List()

	type userInfo struct {
		ID        string    `json:"id"`
		Email     string    `json:"email"`
		PlanType  string    `json:"plan_type"`
		CreatedAt time.Time `json:"created_at"`
		Disabled  bool      `json:"disabled"`
	}

	var result []userInfo
	for _, u := range users {
		result = append(result, userInfo{
			ID:        u.ID,
			Email:     u.Email,
			PlanType:  u.PlanType,
			CreatedAt: u.CreatedAt,
			Disabled:  u.Disabled,
		})
	}

	respondJSON(w, map[string]any{
		"users": result,
		"count": len(result),
	})
}

// POST /admin/pool-users - create a new pool user
func (h *proxyHandler) handlePoolUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		PlanType string `json:"plan_type"`
	}

	if r.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		req.Email = r.FormValue("email")
		req.PlanType = r.FormValue("plan_type")
	}

	email := strings.TrimSpace(req.Email)
	planType := req.PlanType
	if planType == "" {
		planType = "pro"
	}

	if email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}

	user := &PoolUser{
		ID:        randomHex(16),
		Token:     randomHex(32),
		Email:     email,
		PlanType:  planType,
		CreatedAt: time.Now(),
	}

	if err := h.poolUsers.Create(user); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	baseURL := h.getEffectivePublicURL(r)

	respondJSON(w, map[string]any{
		"user": map[string]any{
			"id":         user.ID,
			"email":      user.Email,
			"plan_type":  user.PlanType,
			"created_at": user.CreatedAt,
		},
		"token": user.Token,
		"setup": map[string]string{
			"codex_config":    baseURL + "/config/codex/" + user.Token,
			"clcode_setup":    baseURL + "/setup/clcode/" + user.Token,
			"gemini_config":   baseURL + "/config/gemini/" + user.Token,
			"gemini_setup":    baseURL + "/setup/gemini/" + user.Token,
			"claude_config":   baseURL + "/config/claude/" + user.Token,
			"opencode_config": baseURL + "/config/opencode/" + user.Token,
			"opencode_setup":  baseURL + "/setup/opencode/" + user.Token,
		},
	})
}

// DELETE /admin/pool-users/:id - disable/delete a pool user
func (h *proxyHandler) handlePoolUserDelete(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.poolUsers.Disable(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, map[string]any{
		"success": true,
		"id":      id,
	})
}

type poolAPIKeyPolicyView struct {
	AllowedModels       []string      `json:"allowed_models,omitempty"`
	AllowedAccountTypes []AccountType `json:"allowed_account_types,omitempty"`
	MaxRPM              int           `json:"max_rpm,omitempty"`
	MaxTPM              int           `json:"max_tpm,omitempty"`
	MaxConcurrency      int           `json:"max_concurrency,omitempty"`
	DailyBudget         float64       `json:"daily_budget,omitempty"`
	MonthlyBudget       float64       `json:"monthly_budget,omitempty"`
	NoLog               bool          `json:"no_log"`
}

type poolAPIKeyMetadataView struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Prefix     string               `json:"prefix"`
	Last4      string               `json:"last4"`
	CreatedAt  time.Time            `json:"created_at"`
	LastUsedAt *time.Time           `json:"last_used_at"`
	Disabled   bool                 `json:"disabled"`
	Policy     poolAPIKeyPolicyView `json:"policy"`
}

type poolAPIKeyCreateRequest struct {
	Name                string               `json:"name"`
	Policy              poolAPIKeyPolicyView `json:"policy"`
	AllowedModels       []string             `json:"allowed_models"`
	AllowedAccountTypes []AccountType        `json:"allowed_account_types"`
	MaxRPM              int                  `json:"max_rpm"`
	MaxTPM              int                  `json:"max_tpm"`
	MaxConcurrency      int                  `json:"max_concurrency"`
	DailyBudget         float64              `json:"daily_budget"`
	MonthlyBudget       float64              `json:"monthly_budget"`
	NoLog               bool                 `json:"no_log"`
}

func (req poolAPIKeyCreateRequest) mergedPolicy() PoolAPITokenPolicy {
	policy := PoolAPITokenPolicy{
		AllowedModels:       append([]string(nil), req.Policy.AllowedModels...),
		AllowedAccountTypes: append([]AccountType(nil), req.Policy.AllowedAccountTypes...),
		MaxRPM:              req.Policy.MaxRPM,
		MaxTPM:              req.Policy.MaxTPM,
		MaxConcurrency:      req.Policy.MaxConcurrency,
		DailyBudget:         req.Policy.DailyBudget,
		MonthlyBudget:       req.Policy.MonthlyBudget,
		NoLog:               req.Policy.NoLog,
	}
	if req.AllowedModels != nil {
		policy.AllowedModels = append([]string(nil), req.AllowedModels...)
	}
	if req.AllowedAccountTypes != nil {
		policy.AllowedAccountTypes = append([]AccountType(nil), req.AllowedAccountTypes...)
	}
	if req.MaxRPM != 0 {
		policy.MaxRPM = req.MaxRPM
	}
	if req.MaxTPM != 0 {
		policy.MaxTPM = req.MaxTPM
	}
	if req.MaxConcurrency != 0 {
		policy.MaxConcurrency = req.MaxConcurrency
	}
	if req.DailyBudget != 0 {
		policy.DailyBudget = req.DailyBudget
	}
	if req.MonthlyBudget != 0 {
		policy.MonthlyBudget = req.MonthlyBudget
	}
	if req.NoLog {
		policy.NoLog = true
	}
	policy.AllowedModels = cleanStringList(policy.AllowedModels)
	policy.AllowedAccountTypes = cleanAccountTypeList(policy.AllowedAccountTypes)
	return policy
}

func cleanStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func cleanAccountTypeList(values []AccountType) []AccountType {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[AccountType]struct{}, len(values))
	out := make([]AccountType, 0, len(values))
	for _, value := range values {
		value = AccountType(strings.TrimSpace(string(value)))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func poolAPIKeyPolicyViewFromToken(tok *PoolAPIToken) poolAPIKeyPolicyView {
	if tok == nil {
		return poolAPIKeyPolicyView{}
	}
	return poolAPIKeyPolicyView{
		AllowedModels:       append([]string(nil), tok.AllowedModels...),
		AllowedAccountTypes: append([]AccountType(nil), tok.AllowedAccountTypes...),
		MaxRPM:              tok.MaxRPM,
		MaxTPM:              tok.MaxTPM,
		MaxConcurrency:      tok.MaxConcurrency,
		DailyBudget:         tok.DailyBudget,
		MonthlyBudget:       tok.MonthlyBudget,
		NoLog:               tok.NoLog,
	}
}

func poolAPIKeyMetadataViewFromToken(tok *PoolAPIToken) poolAPIKeyMetadataView {
	if tok == nil {
		return poolAPIKeyMetadataView{}
	}
	return poolAPIKeyMetadataView{
		ID:         tok.ID,
		Name:       tok.Name,
		Prefix:     tok.Prefix,
		Last4:      tok.Last4,
		CreatedAt:  tok.CreatedAt,
		LastUsedAt: tok.LastUsedAt,
		Disabled:   tok.Disabled,
		Policy:     poolAPIKeyPolicyViewFromToken(tok),
	}
}

func (h *proxyHandler) routePoolUserAPIKeysAdmin(w http.ResponseWriter, r *http.Request, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[1] != "api-keys" {
		return false
	}
	userID := strings.TrimSpace(parts[0])
	if userID == "" {
		http.NotFound(w, r)
		return true
	}

	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		h.handlePoolUserAPIKeysList(w, r, userID)
	case len(parts) == 2 && r.Method == http.MethodPost:
		h.handlePoolUserAPIKeyCreate(w, r, userID)
	case len(parts) == 3 && (r.Method == http.MethodDelete || r.Method == http.MethodPost):
		h.handlePoolUserAPIKeyDisable(w, r, userID, parts[2])
	case len(parts) == 4 && parts[3] == "disable" && r.Method == http.MethodPost:
		h.handlePoolUserAPIKeyDisable(w, r, userID, parts[2])
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
	return true
}

func (h *proxyHandler) handlePoolUserAPIKeysList(w http.ResponseWriter, r *http.Request, userID string) {
	if h.poolUsers.Get(userID) == nil {
		http.Error(w, "user not found: "+userID, http.StatusNotFound)
		return
	}
	tokens := h.poolUsers.ListAPITokens(userID)
	keys := make([]poolAPIKeyMetadataView, 0, len(tokens))
	for _, tok := range tokens {
		keys = append(keys, poolAPIKeyMetadataViewFromToken(tok))
	}
	respondJSON(w, map[string]any{
		"user_id":  userID,
		"api_keys": keys,
		"count":    len(keys),
	})
}

func (h *proxyHandler) handlePoolUserAPIKeyCreate(w http.ResponseWriter, r *http.Request, userID string) {
	var req poolAPIKeyCreateRequest
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		req.Name = r.FormValue("name")
	}

	raw, meta, err := h.poolUsers.CreateAPITokenWithPolicy(userID, req.Name, req.mergedPolicy())
	if err != nil {
		code := http.StatusInternalServerError
		errText := strings.ToLower(err.Error())
		if strings.Contains(errText, "not found") {
			code = http.StatusNotFound
		} else if strings.Contains(errText, "disabled") {
			code = http.StatusForbidden
		}
		http.Error(w, err.Error(), code)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"api_key": raw,
		"key":     poolAPIKeyMetadataViewFromToken(meta),
		"warning": "This API key is only shown once. Copy it now; only prefix, last4, and policy metadata are stored.",
	})
}

func (h *proxyHandler) handlePoolUserAPIKeyDisable(w http.ResponseWriter, r *http.Request, userID, tokenID string) {
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		http.Error(w, "token id required", http.StatusBadRequest)
		return
	}
	if h.poolUsers.Get(userID) == nil {
		http.Error(w, "user not found: "+userID, http.StatusNotFound)
		return
	}
	disableID := ""
	for _, tok := range h.poolUsers.ListAPITokens(userID) {
		if tok.ID == tokenID || tok.KID == tokenID {
			disableID = tok.ID
			break
		}
	}
	if disableID == "" {
		http.Error(w, "API token not found: "+tokenID, http.StatusNotFound)
		return
	}
	if err := h.poolUsers.DisableAPIToken(disableID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, map[string]any{
		"success":  true,
		"user_id":  userID,
		"token_id": disableID,
	})
}

// Config download endpoints (no auth - token IS the auth)

func (h *proxyHandler) serveConfigDownload(w http.ResponseWriter, r *http.Request) {
	if h.poolUsers == nil {
		http.Error(w, "pool users not configured", http.StatusServiceUnavailable)
		return
	}

	path := r.URL.Path
	var configType string
	var token string

	switch {
	case strings.HasPrefix(path, "/config/codex/"):
		configType = "codex"
		token = strings.TrimPrefix(path, "/config/codex/")
	case strings.HasPrefix(path, "/config/opencode/"):
		configType = "opencode"
		token = strings.TrimPrefix(path, "/config/opencode/")
	case strings.HasPrefix(path, "/config/gemini/"):
		configType = "gemini"
		token = strings.TrimPrefix(path, "/config/gemini/")
	case strings.HasPrefix(path, "/config/claude/"):
		configType = "claude"
		token = strings.TrimPrefix(path, "/config/claude/")
	default:
		http.NotFound(w, r)
		return
	}

	token = strings.TrimSuffix(token, "/")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}

	user := h.poolUsers.GetByToken(token)
	if user == nil {
		http.Error(w, "invalid token", http.StatusNotFound)
		return
	}
	if user.Disabled {
		http.Error(w, "user disabled", http.StatusForbidden)
		return
	}

	secret := getPoolJWTSecret()
	if secret == "" {
		http.Error(w, "JWT secret not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch configType {
	case "codex":
		auth, err := generateCodexAuth(secret, user)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(auth)
	case "gemini":
		auth, err := generateGeminiAuth(secret, user)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(auth)
	case "claude":
		auth, err := generateClaudeAuth(secret, user)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(auth)
	case "opencode":
		bundle, err := h.buildOpenCodeConfigBundle(r, user, secret)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(bundle)
	}
}
