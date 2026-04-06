package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

type UsageDelta struct {
	Usage    *RequestUsage
	Snapshot *UsageSnapshot
}

func (d UsageDelta) HasData() bool {
	return d.Usage != nil || d.Snapshot != nil
}

// clampNonNegative ensures a value is never negative.
// This prevents issues where CachedInputTokens > InputTokens produces negative billable tokens.
func clampNonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

func parseUsageEventObject(data []byte) (map[string]any, bool) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err == nil {
		return obj, true
	}

	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		return arr[0], true
	}
	return nil, false
}

func applyUsageSnapshot(a *Account, snap *UsageSnapshot) {
	if a == nil || snap == nil {
		return
	}
	a.mu.Lock()
	a.Usage = mergeUsage(a.Usage, *snap)
	a.mu.Unlock()
}

func enrichUsageRecord(a *Account, userID string, ru *RequestUsage, fallbackPrimaryPct, fallbackSecondaryPct float64) *RequestUsage {
	if a == nil || ru == nil {
		return nil
	}
	ru.AccountID = a.ID
	ru.UserID = userID
	ru.AccountType = a.Type
	a.mu.Lock()
	ru.PlanType = a.PlanType
	a.mu.Unlock()
	if ru.PrimaryUsedPct == 0 && fallbackPrimaryPct > 0 {
		ru.PrimaryUsedPct = fallbackPrimaryPct
	}
	if ru.SecondaryUsedPct == 0 && fallbackSecondaryPct > 0 {
		ru.SecondaryUsedPct = fallbackSecondaryPct
	}
	return ru
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func readFirstInt64(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if v := readInt64(m, key); v != 0 {
			return v
		}
	}
	return 0
}

func usageMapFromOpenAIEnvelope(obj map[string]any) (map[string]any, map[string]any) {
	if usageMap, ok := obj["usage"].(map[string]any); ok && usageMap != nil {
		return obj, usageMap
	}
	if resp, ok := obj["response"].(map[string]any); ok && resp != nil {
		if usageMap, ok := resp["usage"].(map[string]any); ok && usageMap != nil {
			return resp, usageMap
		}
	}
	return nil, nil
}

func parseOpenAIUsagePayload(obj map[string]any) *RequestUsage {
	owner, usageMap := usageMapFromOpenAIEnvelope(obj)
	if usageMap == nil {
		return nil
	}

	ru := &RequestUsage{Timestamp: time.Now()}
	ru.InputTokens = readFirstInt64(usageMap, "input_tokens", "prompt_tokens")
	ru.OutputTokens = readFirstInt64(usageMap, "output_tokens", "completion_tokens")

	if details, ok := usageMap["input_tokens_details"].(map[string]any); ok {
		ru.CachedInputTokens = readInt64(details, "cached_tokens")
	}
	if ru.CachedInputTokens == 0 {
		ru.CachedInputTokens = readFirstInt64(usageMap, "cached_input_tokens", "cache_read_input_tokens", "cached_tokens")
	}

	if details, ok := usageMap["output_tokens_details"].(map[string]any); ok {
		ru.ReasoningTokens = readInt64(details, "reasoning_tokens")
	}
	if ru.ReasoningTokens == 0 {
		ru.ReasoningTokens = readInt64(usageMap, "reasoning_output_tokens")
	}

	ru.BillableTokens = readInt64(usageMap, "billable_tokens")
	if ru.BillableTokens == 0 {
		ru.BillableTokens = clampNonNegative(ru.InputTokens - ru.CachedInputTokens + ru.OutputTokens)
	}
	if ru.InputTokens == 0 && ru.OutputTokens == 0 && ru.BillableTokens == 0 {
		return nil
	}

	ru.PromptCacheKey = firstStringField(owner, "prompt_cache_key")
	if ru.PromptCacheKey == "" {
		ru.PromptCacheKey = firstStringField(obj, "prompt_cache_key")
	}
	return ru
}

func parseAnthropicMessageDeltaUsage(obj map[string]any) *RequestUsage {
	usageMap, ok := obj["usage"].(map[string]any)
	if !ok || usageMap == nil {
		return nil
	}
	ru := &RequestUsage{Timestamp: time.Now()}
	ru.OutputTokens = readInt64(usageMap, "output_tokens")
	if ru.OutputTokens == 0 {
		return nil
	}
	ru.BillableTokens = ru.OutputTokens
	return ru
}

func parseAnthropicMessageStartUsage(obj map[string]any) *RequestUsage {
	msg, ok := obj["message"].(map[string]any)
	if !ok || msg == nil {
		return nil
	}
	usageMap, ok := msg["usage"].(map[string]any)
	if !ok || usageMap == nil {
		return nil
	}
	ru := &RequestUsage{Timestamp: time.Now()}
	ru.InputTokens = readInt64(usageMap, "input_tokens")
	ru.CachedInputTokens = readInt64(usageMap, "cache_read_input_tokens")
	if ru.InputTokens == 0 {
		return nil
	}
	if model, ok := msg["model"].(string); ok {
		ru.Model = model
	}
	ru.BillableTokens = clampNonNegative(ru.InputTokens - ru.CachedInputTokens)
	return ru
}

func parseGeminiUsagePayload(obj map[string]any) *RequestUsage {
	usageMap, ok := obj["usageMetadata"].(map[string]any)
	if !ok || usageMap == nil {
		usageMap, ok = obj["usage"].(map[string]any)
		if !ok || usageMap == nil {
			return nil
		}
		ru := &RequestUsage{Timestamp: time.Now()}
		ru.InputTokens = readInt64(usageMap, "prompt_tokens")
		ru.OutputTokens = readInt64(usageMap, "completion_tokens")
		ru.BillableTokens = readInt64(usageMap, "total_tokens")
		if details, ok := usageMap["prompt_tokens_details"].(map[string]any); ok {
			ru.CachedInputTokens = readInt64(details, "cached_tokens")
		}
		if ru.BillableTokens == 0 {
			ru.BillableTokens = clampNonNegative(ru.InputTokens - ru.CachedInputTokens + ru.OutputTokens)
		}
		if ru.InputTokens == 0 && ru.OutputTokens == 0 {
			return nil
		}
		if model, ok := obj["model"].(string); ok {
			ru.Model = model
		}
		return ru
	}
	ru := &RequestUsage{Timestamp: time.Now()}
	ru.InputTokens = readInt64(usageMap, "promptTokenCount")
	ru.OutputTokens = readInt64(usageMap, "candidatesTokenCount")
	ru.CachedInputTokens = readInt64(usageMap, "cachedContentTokenCount")
	ru.BillableTokens = clampNonNegative(ru.InputTokens - ru.CachedInputTokens + ru.OutputTokens)
	if ru.InputTokens == 0 && ru.OutputTokens == 0 {
		return nil
	}
	return ru
}

func parseTokenCountUsageDelta(obj map[string]any) UsageDelta {
	if objType, _ := obj["type"].(string); objType != "token_count" {
		return UsageDelta{}
	}
	info, ok := obj["info"].(map[string]any)
	if !ok || info == nil {
		return UsageDelta{}
	}

	var usageMap map[string]any
	if ltu, ok := info["last_token_usage"].(map[string]any); ok {
		usageMap = ltu
	} else if ttu, ok := info["total_token_usage"].(map[string]any); ok {
		usageMap = ttu
	}
	if usageMap == nil {
		return UsageDelta{}
	}

	now := time.Now()
	ru := &RequestUsage{Timestamp: now}
	ru.InputTokens = readInt64(usageMap, "input_tokens")
	ru.CachedInputTokens = readInt64(usageMap, "cached_input_tokens")
	ru.OutputTokens = readInt64(usageMap, "output_tokens")
	ru.ReasoningTokens = readInt64(usageMap, "reasoning_output_tokens")
	ru.BillableTokens = clampNonNegative(ru.InputTokens - ru.CachedInputTokens + ru.OutputTokens)

	if ru.InputTokens == 0 && ru.OutputTokens == 0 {
		return UsageDelta{}
	}

	snapshot := usageSnapshotFromTokenCountRateLimits(obj, now)
	if snapshot != nil {
		ru.PrimaryUsedPct = snapshot.PrimaryUsedPercent
		ru.SecondaryUsedPct = snapshot.SecondaryUsedPercent
	}
	return UsageDelta{Usage: ru, Snapshot: snapshot}
}

func usageSnapshotFromTokenCountRateLimits(obj map[string]any, now time.Time) *UsageSnapshot {
	if rl, ok := obj["rate_limits"].(map[string]any); ok {
		primaryPct := 0.0
		if primary, ok := rl["primary"].(map[string]any); ok {
			primaryPct = readFloat64(primary, "used_percent") / 100.0
		}
		secondaryPct := 0.0
		if secondary, ok := rl["secondary"].(map[string]any); ok {
			secondaryPct = readFloat64(secondary, "used_percent") / 100.0
		}
		if primaryPct == 0 && secondaryPct == 0 {
			return nil
		}
		return &UsageSnapshot{
			PrimaryUsed:          primaryPct,
			SecondaryUsed:        secondaryPct,
			PrimaryUsedPercent:   primaryPct,
			SecondaryUsedPercent: secondaryPct,
			RetrievedAt:          now,
			Source:               "token_count",
		}
	}
	return nil
}

func usageSnapshotFromLegacyRateLimit(rl map[string]any, now time.Time) *UsageSnapshot {
	if rl == nil {
		return nil
	}

	primaryUsed := readUsedPercentMap(rl, "primary_window")
	secondaryUsed := readUsedPercentMap(rl, "secondary_window")
	if primaryUsed == 0 && secondaryUsed == 0 {
		return nil
	}
	return &UsageSnapshot{
		PrimaryUsed:          primaryUsed,
		SecondaryUsed:        secondaryUsed,
		PrimaryUsedPercent:   primaryUsed,
		SecondaryUsedPercent: secondaryUsed,
		RetrievedAt:          now,
		Source:               "body",
	}
}

func usageSnapshotFromLegacyRateLimitEnvelope(obj map[string]any, now time.Time) *UsageSnapshot {
	if rl, ok := obj["rate_limit"].(map[string]any); ok {
		return usageSnapshotFromLegacyRateLimit(rl, now)
	}
	if resp, ok := obj["response"].(map[string]any); ok && resp != nil {
		if rl, ok := resp["rate_limit"].(map[string]any); ok {
			return usageSnapshotFromLegacyRateLimit(rl, now)
		}
	}
	return nil
}

func parseCodexUsageDelta(obj map[string]any) UsageDelta {
	if delta := parseTokenCountUsageDelta(obj); delta.HasData() {
		return delta
	}
	now := time.Now()
	return UsageDelta{
		Usage:    parseOpenAIUsagePayload(obj),
		Snapshot: usageSnapshotFromLegacyRateLimitEnvelope(obj, now),
	}
}

func parseCodexUsageHeadersSnapshot(headers http.Header, now time.Time) *UsageSnapshot {
	primaryStr := headers.Get("X-Codex-Primary-Used-Percent")
	secondaryStr := headers.Get("X-Codex-Secondary-Used-Percent")
	if primaryStr == "" && secondaryStr == "" {
		return nil
	}

	snap := UsageSnapshot{
		RetrievedAt: now,
		Source:      "headers",
	}
	if primaryStr != "" {
		if f, err := readHeaderFloat64(primaryStr); err == nil {
			snap.PrimaryUsedPercent = f / 100.0
			snap.PrimaryUsed = snap.PrimaryUsedPercent
		}
	}
	if secondaryStr != "" {
		if f, err := readHeaderFloat64(secondaryStr); err == nil {
			snap.SecondaryUsedPercent = f / 100.0
			snap.SecondaryUsed = snap.SecondaryUsedPercent
		}
	}
	if v := headers.Get("X-Codex-Primary-Window-Minutes"); v != "" {
		snap.PrimaryWindowMinutes, _ = readHeaderInt(v)
	}
	if v := headers.Get("X-Codex-Secondary-Window-Minutes"); v != "" {
		snap.SecondaryWindowMinutes, _ = readHeaderInt(v)
	}
	if v := headers.Get("X-Codex-Primary-Reset-At"); v != "" {
		if ts, err := readHeaderInt64(v); err == nil {
			snap.PrimaryResetAt = time.Unix(ts, 0)
		}
	}
	if v := headers.Get("X-Codex-Secondary-Reset-At"); v != "" {
		if ts, err := readHeaderInt64(v); err == nil {
			snap.SecondaryResetAt = time.Unix(ts, 0)
		}
	}
	if v := headers.Get("X-Codex-Credits-Balance"); v != "" {
		if f, err := readHeaderFloat64(v); err == nil {
			snap.CreditsBalance = f
		}
	}
	snap.HasCredits = headers.Get("X-Codex-Credits-Has-Credits") == "true" || headers.Get("X-Codex-Credits-Has-Credits") == "TRUE"
	snap.CreditsUnlimited = headers.Get("X-Codex-Credits-Unlimited") == "true" || headers.Get("X-Codex-Credits-Unlimited") == "TRUE"
	return &snap
}

func readHeaderFloat64(v string) (float64, error) {
	return json.Number(v).Float64()
}

func readHeaderInt(v string) (int, error) {
	n, err := json.Number(v).Int64()
	return int(n), err
}

func readHeaderInt64(v string) (int64, error) {
	return json.Number(v).Int64()
}

func readUsedPercentMap(rl map[string]any, key string) float64 {
	v, ok := rl[key]
	if !ok {
		return 0
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	return readFloat64(obj, "used_percent") / 100.0
}

func (h *proxyHandler) recordUsage(a *Account, ru RequestUsage) {
	if a == nil {
		return
	}
	a.applyRequestUsage(ru)
	if h.store != nil {
		_ = h.store.record(ru)
	}
	if h.cfg.debug {
		log.Printf("token_count: account=%s plan=%s user=%s in=%d cached=%d out=%d reasoning=%d billable=%d primary=%.1f%% secondary=%.1f%%",
			ru.AccountID, ru.PlanType, ru.UserID, ru.InputTokens, ru.CachedInputTokens, ru.OutputTokens, ru.ReasoningTokens, ru.BillableTokens,
			ru.PrimaryUsedPct*100, ru.SecondaryUsedPct*100)
	}
}

func parseRequestUsage(obj map[string]any) *RequestUsage {
	return parseOpenAIUsagePayload(obj)
}

func readInt64(m map[string]any, key string) int64 {
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case float64:
			return int64(t)
		case int64:
			return t
		case int:
			return int64(t)
		case json.Number:
			if n, err := t.Int64(); err == nil {
				return n
			}
		}
	}
	return 0
}

func readFloat64(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case float64:
			return t
		case int64:
			return float64(t)
		case int:
			return float64(t)
		case json.Number:
			if f, err := t.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}
