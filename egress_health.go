package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

const upstreamErrorSummaryWindow = 10 * time.Minute

type UpstreamErrorSummary struct {
	LastErrorClass              string `json:"last_error_class,omitempty"`
	LastErrorAt                 string `json:"last_error_at,omitempty"`
	CloudflareChallengeCount10m int    `json:"cloudflare_challenge_count_10m"`
	ProviderPolicyBlockCount10m int    `json:"provider_policy_block_count_10m"`
	LastCFRayHash               string `json:"last_cf_ray_hash,omitempty"`
}

type upstreamErrorEvent struct {
	at        time.Time
	class     upstreamErrorClass
	cfRayHash string
}

type upstreamErrorTracker struct {
	mu     sync.Mutex
	max    int
	events []upstreamErrorEvent
}

func newUpstreamErrorTracker(max int) *upstreamErrorTracker {
	if max <= 0 {
		max = 128
	}
	return &upstreamErrorTracker{max: max}
}

func (t *upstreamErrorTracker) record(info upstreamErrorInfo, resp *http.Response, at time.Time) {
	if t == nil || info.Class == "" || info.Class == upstreamUnknown || at.IsZero() {
		return
	}
	event := upstreamErrorEvent{at: at, class: info.Class}
	if info.Class == upstreamCloudflareChallenge {
		event.cfRayHash = hashCFRayForStatus(resp)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
	if len(t.events) > t.max {
		t.events = append([]upstreamErrorEvent(nil), t.events[len(t.events)-t.max:]...)
	}
}

func (t *upstreamErrorTracker) summary(now time.Time) *UpstreamErrorSummary {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	events := append([]upstreamErrorEvent(nil), t.events...)
	t.mu.Unlock()
	if len(events) == 0 {
		return &UpstreamErrorSummary{}
	}
	windowStart := now.Add(-upstreamErrorSummaryWindow)
	summary := &UpstreamErrorSummary{}
	for _, event := range events {
		if summary.LastErrorAt == "" || event.at.After(parseStatusTime(summary.LastErrorAt)) {
			summary.LastErrorClass = event.class.String()
			summary.LastErrorAt = event.at.UTC().Format(time.RFC3339)
		}
		if event.at.Before(windowStart) || event.at.After(now.Add(time.Second)) {
			continue
		}
		switch event.class {
		case upstreamCloudflareChallenge:
			summary.CloudflareChallengeCount10m++
			if event.cfRayHash != "" {
				summary.LastCFRayHash = event.cfRayHash
			}
		case upstreamProviderPolicyBlock:
			summary.ProviderPolicyBlockCount10m++
		}
	}
	return summary
}

func parseStatusTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func hashCFRayForStatus(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	cfRay := strings.TrimSpace(resp.Header.Get("Cf-Ray"))
	if cfRay == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cfRay))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}
