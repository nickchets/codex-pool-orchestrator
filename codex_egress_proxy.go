package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

type codexEgressHostRoundTripper struct {
	base  http.RoundTripper
	codex http.RoundTripper
}

func (rt codexEgressHostRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && shouldUseCodexEgressProxy(req.URL) && rt.codex != nil {
		return rt.codex.RoundTrip(req)
	}
	if rt.base != nil {
		return rt.base.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func shouldUseCodexEgressProxy(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch host {
	case "chatgpt.com", "chat.openai.com":
		return true
	default:
		return strings.HasSuffix(host, ".chatgpt.com") || strings.HasSuffix(host, ".chat.openai.com")
	}
}

func safeProxyLogHost(rawProxyURL string) string {
	proxyURL, err := url.Parse(strings.TrimSpace(rawProxyURL))
	if err != nil {
		return "configured"
	}
	host := strings.TrimSpace(proxyURL.Hostname())
	if host == "" {
		return "configured"
	}
	if port := proxyURL.Port(); port != "" {
		return net.JoinHostPort(host, port)
	}
	return host
}

func buildHTTPProxyTransport(rawProxyURL string) (http.RoundTripper, error) {
	proxyURL, err := url.Parse(strings.TrimSpace(rawProxyURL))
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(proxyURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("unsupported proxy URL scheme %q", proxyURL.Scheme)
	}
	if strings.TrimSpace(proxyURL.Hostname()) == "" {
		return nil, fmt.Errorf("proxy URL host is required")
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0,
		ExpectContinueTimeout: 5 * time.Second,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   20,
	}
	_ = http2.ConfigureTransport(transport)
	return transport, nil
}
