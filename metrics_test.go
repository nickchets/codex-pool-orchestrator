package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsServe(t *testing.T) {
	m := newMetrics()
	m.inc("200", "acct1")
	m.inc("429", "acct1")
	m.incEvent("gitlab_claude_shared_tpm_canary_success")
	m.observeTTFB(AccountTypeClaude, 800*time.Millisecond)
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	m.serve(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if len(body) == 0 {
		t.Fatalf("expected metrics output")
	}
	if want := `codexpool_events_total{name="gitlab_claude_shared_tpm_canary_success"} 1`; !containsLine(body, want) {
		t.Fatalf("missing event metric %q in %s", want, body)
	}
	if want := `codexpool_ttfb_observations_total{provider="claude",bucket="le_1000ms"} 1`; !containsLine(body, want) {
		t.Fatalf("missing ttfb metric %q in %s", want, body)
	}
}

func containsLine(body, needle string) bool {
	for _, line := range strings.Split(body, "\n") {
		if line == needle {
			return true
		}
	}
	return false
}
