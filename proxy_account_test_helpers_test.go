package main

import (
	"sync/atomic"
	"time"
)

type proxyTestAccountSnapshot struct {
	Dead                           bool
	HealthStatus                   string
	HealthError                    string
	LastUsed                       time.Time
	Inflight                       int64
	Penalty                        float64
	RateLimitUntil                 time.Time
	GeminiModelRateLimitResetTimes map[string]time.Time
	GitLabQuotaExceededCount       int
	AccessToken                    string
}

func snapshotProxyTestAccount(acc *Account) proxyTestAccountSnapshot {
	if acc == nil {
		return proxyTestAccountSnapshot{}
	}

	acc.mu.Lock()
	defer acc.mu.Unlock()

	return proxyTestAccountSnapshot{
		Dead:                           acc.Dead,
		HealthStatus:                   acc.HealthStatus,
		HealthError:                    acc.HealthError,
		LastUsed:                       acc.LastUsed,
		Inflight:                       atomic.LoadInt64(&acc.Inflight),
		Penalty:                        acc.Penalty,
		RateLimitUntil:                 acc.RateLimitUntil,
		GeminiModelRateLimitResetTimes: cloneTimeMap(acc.GeminiModelRateLimitResetTimes),
		GitLabQuotaExceededCount:       acc.GitLabQuotaExceededCount,
		AccessToken:                    acc.AccessToken,
	}
}
