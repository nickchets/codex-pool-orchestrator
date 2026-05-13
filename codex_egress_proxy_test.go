package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type recordingRoundTripper struct {
	name string
	seen *[]string
}

func (r recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	*r.seen = append(*r.seen, r.name+":"+req.URL.Host)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    req,
	}, nil
}

func TestCodexEgressTransportRoutesOnlyChatGPTHosts(t *testing.T) {
	seen := []string{}
	direct := recordingRoundTripper{name: "direct", seen: &seen}
	codex := recordingRoundTripper{name: "codex", seen: &seen}
	rt := codexEgressHostRoundTripper{base: direct, codex: codex}

	for _, raw := range []string{
		"https://chatgpt.com/backend-api/codex/responses",
		"https://chat.openai.com/backend-api/codex/responses",
	} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip(%s): %v", raw, err)
		}
		_ = resp.Body.Close()
	}

	for _, raw := range []string{
		"https://api.anthropic.com/v1/messages",
		"https://generativelanguage.googleapis.com/v1beta/models",
		"https://api.openai.com/v1/chat/completions",
	} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip(%s): %v", raw, err)
		}
		_ = resp.Body.Close()
	}

	got := strings.Join(seen, ",")
	want := "codex:chatgpt.com,codex:chat.openai.com,direct:api.anthropic.com,direct:generativelanguage.googleapis.com,direct:api.openai.com"
	if got != want {
		t.Fatalf("routes=%q want %q", got, want)
	}
}

func TestBuildHTTPProxyTransportRejectsBadURL(t *testing.T) {
	if _, err := buildHTTPProxyTransport("http://127.0.0.1:18189"); err != nil {
		t.Fatalf("valid proxy url rejected: %v", err)
	}
	for _, raw := range []string{"://bad", "file:///tmp/proxy.sock", "http:///missing-host"} {
		if _, err := buildHTTPProxyTransport(raw); err == nil {
			t.Fatalf("expected invalid proxy url error for %q", raw)
		}
	}
	if _, err := url.Parse("http://127.0.0.1:18189"); err != nil {
		t.Fatal(err)
	}
}

func TestSafeProxyLogHostStripsCredentials(t *testing.T) {
	got := safeProxyLogHost("http://user:password@proxy.example:8080")
	if got != "proxy.example:8080" {
		t.Fatalf("safeProxyLogHost = %q", got)
	}
	if strings.Contains(got, "user") || strings.Contains(got, "password") || strings.Contains(got, "@") {
		t.Fatalf("safe proxy log host leaked userinfo: %q", got)
	}
}
