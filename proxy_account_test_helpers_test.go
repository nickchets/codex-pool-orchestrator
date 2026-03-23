package main

import "time"

type proxyTestAccountSnapshot struct {
	Dead                     bool
	HealthStatus             string
	HealthError              string
	LastUsed                 time.Time
	Penalty                  float64
	RateLimitUntil           time.Time
	GitLabQuotaExceededCount int
	AccessToken              string
}

func snapshotProxyTestAccount(acc *Account) proxyTestAccountSnapshot {
	if acc == nil {
		return proxyTestAccountSnapshot{}
	}

	acc.mu.Lock()
	defer acc.mu.Unlock()

	return proxyTestAccountSnapshot{
		Dead:                     acc.Dead,
		HealthStatus:             acc.HealthStatus,
		HealthError:              acc.HealthError,
		LastUsed:                 acc.LastUsed,
		Penalty:                  acc.Penalty,
		RateLimitUntil:           acc.RateLimitUntil,
		GitLabQuotaExceededCount: acc.GitLabQuotaExceededCount,
		AccessToken:              acc.AccessToken,
	}
}
