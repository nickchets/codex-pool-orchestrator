package main

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

type metrics struct {
	mu        sync.Mutex
	requests  map[string]int64            // status -> count
	accStatus map[string]map[string]int64 // account -> status -> count
	events    map[string]int64            // named runtime events
	ttfb      map[string]map[string]int64 // provider -> bucket -> count
}

func newMetrics() *metrics {
	return &metrics{
		requests:  make(map[string]int64),
		accStatus: make(map[string]map[string]int64),
		events:    make(map[string]int64),
		ttfb:      make(map[string]map[string]int64),
	}
}

func (m *metrics) inc(status string, account string) {
	m.mu.Lock()
	m.requests[status]++
	if account != "" {
		mp, ok := m.accStatus[account]
		if !ok {
			mp = make(map[string]int64)
			m.accStatus[account] = mp
		}
		mp[status]++
	}
	m.mu.Unlock()
}

func (m *metrics) incEvent(name string) {
	if m == nil || name == "" {
		return
	}
	m.mu.Lock()
	m.events[name]++
	m.mu.Unlock()
}

func ttfbBucketName(latency time.Duration) string {
	switch {
	case latency <= 250*time.Millisecond:
		return "le_250ms"
	case latency <= time.Second:
		return "le_1000ms"
	case latency <= 5*time.Second:
		return "le_5000ms"
	default:
		return "gt_5000ms"
	}
}

func (m *metrics) observeTTFB(provider AccountType, latency time.Duration) {
	if m == nil || provider == "" || latency < 0 {
		return
	}
	bucket := ttfbBucketName(latency)
	m.mu.Lock()
	providerKey := string(provider)
	byBucket, ok := m.ttfb[providerKey]
	if !ok {
		byBucket = make(map[string]int64)
		m.ttfb[providerKey] = byBucket
	}
	byBucket[bucket]++
	m.mu.Unlock()
}

func (m *metrics) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	m.mu.Lock()
	defer m.mu.Unlock()
	// overall
	statuses := make([]string, 0, len(m.requests))
	for s := range m.requests {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	for _, s := range statuses {
		fmt.Fprintf(w, "codexpool_requests_total{status=\"%s\"} %d\n", s, m.requests[s])
	}
	events := make([]string, 0, len(m.events))
	for name := range m.events {
		events = append(events, name)
	}
	sort.Strings(events)
	for _, name := range events {
		fmt.Fprintf(w, "codexpool_events_total{name=\"%s\"} %d\n", name, m.events[name])
	}
	providers := make([]string, 0, len(m.ttfb))
	for provider := range m.ttfb {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		buckets := make([]string, 0, len(m.ttfb[provider]))
		for bucket := range m.ttfb[provider] {
			buckets = append(buckets, bucket)
		}
		sort.Strings(buckets)
		for _, bucket := range buckets {
			fmt.Fprintf(w, "codexpool_ttfb_observations_total{provider=\"%s\",bucket=\"%s\"} %d\n", provider, bucket, m.ttfb[provider][bucket])
		}
	}
	// per account
	accs := make([]string, 0, len(m.accStatus))
	for a := range m.accStatus {
		accs = append(accs, a)
	}
	sort.Strings(accs)
	for _, a := range accs {
		st := m.accStatus[a]
		sts := make([]string, 0, len(st))
		for s := range st {
			sts = append(sts, s)
		}
		sort.Strings(sts)
		for _, s := range sts {
			fmt.Fprintf(w, "codexpool_account_requests_total{account=\"%s\",status=\"%s\"} %d\n", a, s, st[s])
		}
	}
}
