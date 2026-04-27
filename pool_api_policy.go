package main

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	poolAPIPolicyWindow                     = time.Minute
	defaultPoolAPIMaxOutputReservation      = int64(4096)
	poolAPIPolicyErrorTypeRateLimit         = "rate_limit_error"
	poolAPIPolicyErrorTypeInsufficientQuota = "insufficient_quota"
)

type poolAPIPolicyError struct {
	StatusCode int
	Code       string
	Type       string
	Message    string
}

func (e *poolAPIPolicyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newPoolAPIRateLimitPolicyError(code, message string) *poolAPIPolicyError {
	return &poolAPIPolicyError{
		StatusCode: http.StatusTooManyRequests,
		Code:       code,
		Type:       poolAPIPolicyErrorTypeRateLimit,
		Message:    message,
	}
}

func newPoolAPIBudgetPolicyError(code, message string) *poolAPIPolicyError {
	return &poolAPIPolicyError{
		StatusCode: http.StatusForbidden,
		Code:       code,
		Type:       poolAPIPolicyErrorTypeInsufficientQuota,
		Message:    message,
	}
}

func newPoolAPIPolicyCheckUnavailableError(message string) *poolAPIPolicyError {
	return &poolAPIPolicyError{
		StatusCode: http.StatusServiceUnavailable,
		Code:       "policy_check_unavailable",
		Type:       "server_error",
		Message:    message,
	}
}

func writeOpenAICompatiblePoolAPIPolicyError(w http.ResponseWriter, err *poolAPIPolicyError) {
	if w == nil || err == nil {
		return
	}
	status := err.StatusCode
	if status == 0 {
		status = http.StatusTooManyRequests
	}
	errType := strings.TrimSpace(err.Type)
	if errType == "" {
		errType = poolAPIPolicyErrorTypeRateLimit
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": err.Message,
			"type":    errType,
			"code":    err.Code,
		},
	})
}

func poolAPITokenPolicyFromToken(tok *PoolAPIToken) PoolAPITokenPolicy {
	if tok == nil {
		return PoolAPITokenPolicy{}
	}
	return PoolAPITokenPolicy{
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

type poolAPITokenPolicyState struct {
	Active          int
	RequestTimes    []time.Time
	TPMReservations []poolAPITPMReservation
}

type poolAPITPMReservation struct {
	At     time.Time
	Tokens int64
}

type poolAPIPolicyManager struct {
	mu     sync.Mutex
	now    func() time.Time
	states map[string]*poolAPITokenPolicyState
}

func newPoolAPIPolicyManager() *poolAPIPolicyManager {
	return &poolAPIPolicyManager{
		now:    time.Now,
		states: make(map[string]*poolAPITokenPolicyState),
	}
}

type poolAPIPolicyReservation struct {
	manager *poolAPIPolicyManager
	tokenID string
	once    sync.Once
}

func (r *poolAPIPolicyReservation) Release() {
	if r == nil || r.manager == nil || strings.TrimSpace(r.tokenID) == "" {
		return
	}
	r.once.Do(func() {
		r.manager.release(r.tokenID)
	})
}

func (m *poolAPIPolicyManager) stateLocked(tokenID string) *poolAPITokenPolicyState {
	if m.states == nil {
		m.states = make(map[string]*poolAPITokenPolicyState)
	}
	st := m.states[tokenID]
	if st == nil {
		st = &poolAPITokenPolicyState{}
		m.states[tokenID] = st
	}
	return st
}

func (m *poolAPIPolicyManager) nowUTC() time.Time {
	if m == nil || m.now == nil {
		return time.Now().UTC()
	}
	return m.now().UTC()
}

func prunePoolAPITimes(times []time.Time, cutoff time.Time) []time.Time {
	if len(times) == 0 {
		return times
	}
	kept := times[:0]
	for _, ts := range times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	return kept
}

func prunePoolAPITPMReservations(reservations []poolAPITPMReservation, cutoff time.Time) []poolAPITPMReservation {
	if len(reservations) == 0 {
		return reservations
	}
	kept := reservations[:0]
	for _, reservation := range reservations {
		if reservation.At.After(cutoff) {
			kept = append(kept, reservation)
		}
	}
	return kept
}

func sumPoolAPITPMReservations(reservations []poolAPITPMReservation) int64 {
	var total int64
	for _, reservation := range reservations {
		if reservation.Tokens > 0 {
			total += reservation.Tokens
		}
	}
	return total
}

func (m *poolAPIPolicyManager) Reserve(tokenID string, policy PoolAPITokenPolicy, estimatedTokens int64) (*poolAPIPolicyReservation, *poolAPIPolicyError) {
	if m == nil {
		m = newPoolAPIPolicyManager()
	}
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		return nil, nil
	}
	if estimatedTokens < 0 {
		estimatedTokens = 0
	}
	now := m.nowUTC()
	cutoff := now.Add(-poolAPIPolicyWindow)

	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.stateLocked(tokenID)
	st.RequestTimes = prunePoolAPITimes(st.RequestTimes, cutoff)
	st.TPMReservations = prunePoolAPITPMReservations(st.TPMReservations, cutoff)

	if policy.MaxConcurrency > 0 && st.Active >= policy.MaxConcurrency {
		return nil, newPoolAPIRateLimitPolicyError("concurrency_limit_exceeded", "pool API token concurrency limit exceeded")
	}
	if policy.MaxRPM > 0 && len(st.RequestTimes) >= policy.MaxRPM {
		return nil, newPoolAPIRateLimitPolicyError("requests_per_minute_exceeded", "pool API token requests-per-minute limit exceeded")
	}
	if policy.MaxTPM > 0 {
		reserved := sumPoolAPITPMReservations(st.TPMReservations)
		if reserved+estimatedTokens > int64(policy.MaxTPM) {
			return nil, newPoolAPIRateLimitPolicyError("tokens_per_minute_exceeded", "pool API token tokens-per-minute reservation limit exceeded")
		}
	}

	if policy.MaxConcurrency > 0 {
		st.Active++
	}
	if policy.MaxRPM > 0 {
		st.RequestTimes = append(st.RequestTimes, now)
	}
	if policy.MaxTPM > 0 && estimatedTokens > 0 {
		st.TPMReservations = append(st.TPMReservations, poolAPITPMReservation{At: now, Tokens: estimatedTokens})
	}
	return &poolAPIPolicyReservation{manager: m, tokenID: tokenID}, nil
}

func (m *poolAPIPolicyManager) release(tokenID string) {
	if m == nil {
		return
	}
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[tokenID]
	if st == nil || st.Active <= 0 {
		return
	}
	st.Active--
}

func (h *proxyHandler) poolAPIPolicyManager() *poolAPIPolicyManager {
	if h == nil {
		return newPoolAPIPolicyManager()
	}
	h.poolAPIPolicyMu.Lock()
	defer h.poolAPIPolicyMu.Unlock()
	if h.poolAPIPolicies == nil {
		h.poolAPIPolicies = newPoolAPIPolicyManager()
	}
	return h.poolAPIPolicies
}

func poolAPIDefaultMaxOutputReservationFromConfig(cfg config) int64 {
	if cfg.poolAPIDefaultMaxOutputTokens > 0 {
		return int64(cfg.poolAPIDefaultMaxOutputTokens)
	}
	return defaultPoolAPIMaxOutputReservation
}

func estimateOpenAICompatiblePoolAPITokens(path string, body []byte, defaultMaxOutput int64) int64 {
	if !isOpenAICompatibleModelRequestPath(path) {
		return 0
	}
	if defaultMaxOutput <= 0 {
		defaultMaxOutput = defaultPoolAPIMaxOutputReservation
	}
	inputTokens := estimatePoolAPIInputTokens(body)
	outputTokens := extractPoolAPIMaxOutputTokens(body)
	if outputTokens <= 0 {
		outputTokens = defaultMaxOutput
	}
	return inputTokens + outputTokens
}

func estimatePoolAPIInputTokens(body []byte) int64 {
	if len(body) == 0 {
		return 0
	}
	// Conservative, deterministic approximation: JSON/request bytes divided by
	// roughly three bytes per token, rounded up. This intentionally includes
	// message/input text and surrounding structure so policy checks never depend
	// on provider tokenizers.
	return int64(math.Ceil(float64(len(body)) / 3.0))
}

func extractPoolAPIMaxOutputTokens(body []byte) int64 {
	if len(body) == 0 {
		return 0
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return 0
	}
	var maxTokens int64
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if n := readPositiveInt64FromAny(obj[key]); n > maxTokens {
			maxTokens = n
		}
	}
	return maxTokens
}

func readPositiveInt64FromAny(v any) int64 {
	switch n := v.(type) {
	case int:
		if n > 0 {
			return int64(n)
		}
	case int64:
		if n > 0 {
			return n
		}
	case float64:
		if n > 0 {
			return int64(math.Ceil(n))
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 {
			return i
		}
		if f, err := n.Float64(); err == nil && f > 0 {
			return int64(math.Ceil(f))
		}
	}
	return 0
}

func (s *usageStore) getTokenDailyBillableUsage(tokenID string, at time.Time) (int64, error) {
	if s == nil || strings.TrimSpace(tokenID) == "" {
		return 0, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	date := at.Format("2006-01-02")
	daily, err := s.getTokenDailyUsage(tokenID, 1)
	if err != nil {
		return 0, err
	}
	for _, row := range daily {
		if row.Date == date {
			return row.BillableTokens, nil
		}
	}
	return 0, nil
}

func (s *usageStore) getTokenMonthlyBillableUsage(tokenID string, at time.Time) (int64, error) {
	if s == nil || strings.TrimSpace(tokenID) == "" {
		return 0, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	monthPrefix := at.Format("2006-01")
	daily, err := s.getTokenDailyUsage(tokenID, 32)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, row := range daily {
		if strings.HasPrefix(row.Date, monthPrefix) {
			total += row.BillableTokens
		}
	}
	return total, nil
}

func wouldExceedPoolAPITokenBudget(used, estimated int64, budget float64) bool {
	if budget <= 0 {
		return false
	}
	if estimated < 0 {
		estimated = 0
	}
	return float64(used) >= budget || float64(used+estimated) > budget
}

func (h *proxyHandler) checkPoolAPITokenBudgets(tokenID string, policy PoolAPITokenPolicy, estimatedTokens int64) *poolAPIPolicyError {
	if h == nil || h.store == nil || strings.TrimSpace(tokenID) == "" {
		return nil
	}
	now := time.Now().UTC()
	if policy.DailyBudget > 0 {
		used, err := h.store.getTokenDailyBillableUsage(tokenID, now)
		if err != nil {
			return newPoolAPIPolicyCheckUnavailableError("pool API token daily budget check unavailable")
		}
		if wouldExceedPoolAPITokenBudget(used, estimatedTokens, policy.DailyBudget) {
			return newPoolAPIBudgetPolicyError("daily_token_budget_exceeded", "pool API token daily token budget exceeded")
		}
	}
	if policy.MonthlyBudget > 0 {
		used, err := h.store.getTokenMonthlyBillableUsage(tokenID, now)
		if err != nil {
			return newPoolAPIPolicyCheckUnavailableError("pool API token monthly budget check unavailable")
		}
		if wouldExceedPoolAPITokenBudget(used, estimatedTokens, policy.MonthlyBudget) {
			return newPoolAPIBudgetPolicyError("monthly_token_budget_exceeded", "pool API token monthly token budget exceeded")
		}
	}
	return nil
}

func (h *proxyHandler) reservePoolAPITokenPolicy(w http.ResponseWriter, routePlan RoutePlan, requestBodyForInspection []byte) (*poolAPIPolicyReservation, bool) {
	if !isOpenAICompatiblePoolKeyAdmission(routePlan.Admission) {
		return nil, true
	}
	policy := routePlan.Admission.TokenPolicy
	// AdmissionResult keeps TokenAllowedModels as a compatibility field for Phase 4;
	// preserve it for callers that manually constructed an admission without TokenPolicy.
	if len(policy.AllowedModels) == 0 && len(routePlan.Admission.TokenAllowedModels) > 0 {
		policy.AllowedModels = append([]string(nil), routePlan.Admission.TokenAllowedModels...)
	}
	estimatedTokens := estimateOpenAICompatiblePoolAPITokens(routePlan.Shape.Path, requestBodyForInspection, poolAPIDefaultMaxOutputReservationFromConfig(h.cfg))
	if err := h.checkPoolAPITokenBudgets(routePlan.Admission.TokenID, policy, estimatedTokens); err != nil {
		writeOpenAICompatiblePoolAPIPolicyError(w, err)
		return nil, false
	}
	reservation, err := h.poolAPIPolicyManager().Reserve(routePlan.Admission.TokenID, policy, estimatedTokens)
	if err != nil {
		writeOpenAICompatiblePoolAPIPolicyError(w, err)
		return nil, false
	}
	return reservation, true
}
