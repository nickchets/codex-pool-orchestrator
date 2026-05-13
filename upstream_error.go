package main

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

type upstreamErrorClass string

const (
	upstreamUnknown             upstreamErrorClass = "upstream_unknown"
	upstreamCloudflareChallenge upstreamErrorClass = "upstream_cloudflare_challenge"
	upstreamProviderPolicyBlock upstreamErrorClass = "upstream_provider_policy_block"
	upstreamAuthFailure         upstreamErrorClass = "upstream_auth_failure"
	upstreamRateLimit           upstreamErrorClass = "upstream_rate_limit"
	upstreamTransient           upstreamErrorClass = "upstream_transient"
)

type upstreamErrorInfo struct {
	Class               upstreamErrorClass
	StatusCode          int
	Status              string
	Retryable           bool
	SeatPoison          bool
	EgressChallenge     bool
	ProviderPolicyBlock bool
	SafeMessage         string
}

func (c upstreamErrorClass) String() string {
	if c == "" {
		return string(upstreamUnknown)
	}
	return string(c)
}

type sanitizedLogHeader []string

func (h sanitizedLogHeader) String() string {
	return strings.Join([]string(h), " ")
}

func sanitizeHeaderForLog(headers http.Header) sanitizedLogHeader {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		canonical := http.CanonicalHeaderKey(key)
		values := headers.Values(key)
		if isSecretHeaderForLog(canonical) {
			out = append(out, fmt.Sprintf("%s=[REDACTED]", canonical))
			continue
		}
		out = append(out, fmt.Sprintf("%s=%v", canonical, values))
	}
	return sanitizedLogHeader(out)
}

func isSecretHeaderForLog(headerName string) bool {
	switch strings.ToLower(strings.TrimSpace(headerName)) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "openai-organization", "openai-project":
		return true
	default:
		return false
	}
}

func (i upstreamErrorInfo) Error() string {
	status := strings.TrimSpace(i.Status)
	if status == "" && i.StatusCode != 0 {
		status = fmt.Sprintf("%d", i.StatusCode)
	}
	if status == "" {
		return i.Class.String()
	}
	return fmt.Sprintf("%s: %s", i.Class.String(), status)
}

func upstreamErrorInfoFromError(err error) (upstreamErrorInfo, bool) {
	var info upstreamErrorInfo
	if err == nil || !errors.As(err, &info) {
		return upstreamErrorInfo{}, false
	}
	return info, true
}

func isNonRetryableUpstreamError(err error) bool {
	info, ok := upstreamErrorInfoFromError(err)
	return ok && !info.Retryable
}

func classifyUpstreamResponse(resp *http.Response, body []byte) upstreamErrorInfo {
	if resp == nil {
		return upstreamErrorInfo{Class: upstreamUnknown, SafeMessage: "upstream response unavailable"}
	}
	bodyText := strings.ToLower(string(body))
	info := upstreamErrorInfo{
		Class:       upstreamUnknown,
		StatusCode:  resp.StatusCode,
		Status:      strings.TrimSpace(resp.Status),
		SafeMessage: safeUpstreamStatusMessage(resp),
	}
	if hasCloudflareChallengeMarkers(resp, bodyText) {
		info.Class = upstreamCloudflareChallenge
		info.Retryable = true
		info.SeatPoison = false
		info.EgressChallenge = true
		info.SafeMessage = "cloudflare challenge"
		return info
	}
	if hasProviderPolicyBlockMarkers(bodyText) {
		info.Class = upstreamProviderPolicyBlock
		// Provider policy blocks are request/route scoped, not account-health
		// failures. Rotate to another eligible seat once the body proves this
		// class, but never poison the current seat.
		info.Retryable = true
		info.SeatPoison = false
		info.ProviderPolicyBlock = true
		info.SafeMessage = "provider policy block"
		return info
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		info.Class = upstreamRateLimit
		info.Retryable = true
		info.SeatPoison = false
	case http.StatusUnauthorized, http.StatusForbidden:
		info.Class = upstreamAuthFailure
		info.Retryable = false
		info.SeatPoison = true
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		info.Class = upstreamTransient
		info.Retryable = true
		info.SeatPoison = false
	default:
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			info.Class = upstreamTransient
			info.Retryable = true
			info.SeatPoison = false
		}
	}
	return info
}

func safeUpstreamStatusMessage(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	if strings.TrimSpace(resp.Status) != "" {
		return strings.TrimSpace(resp.Status)
	}
	if resp.StatusCode != 0 {
		return fmt.Sprintf("%d", resp.StatusCode)
	}
	return "upstream response"
}

func hasCloudflareChallengeMarkers(resp *http.Response, lowerBody string) bool {
	if resp == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Cf-Mitigated")), "challenge") {
		return true
	}
	for _, value := range resp.Header.Values("Server-Timing") {
		if strings.Contains(strings.ToLower(value), "chlray") {
			return true
		}
	}
	server := strings.ToLower(strings.Join(resp.Header.Values("Server"), " "))
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	setCookie := strings.ToLower(strings.Join(resp.Header.Values("Set-Cookie"), " "))
	challengeBody := strings.Contains(lowerBody, "just a moment") ||
		strings.Contains(lowerBody, "checking if the site connection is secure") ||
		strings.Contains(lowerBody, "enable javascript and cookies") ||
		strings.Contains(lowerBody, "challenge-platform") ||
		strings.Contains(lowerBody, "cf-chl")
	challengeStatus := resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable
	if challengeStatus && challengeBody {
		return true
	}
	if challengeStatus && strings.Contains(server, "cloudflare") && strings.Contains(contentType, "text/html") && strings.TrimSpace(lowerBody) != "" {
		return true
	}
	// __cf_bm alone is not enough: OpenAI API can attach it to ordinary 401
	// JSON responses. Treat it as challenge only in a 403/503 HTML/Cloudflare
	// context.
	if challengeStatus && strings.Contains(setCookie, "__cf_bm") && (strings.Contains(server, "cloudflare") || strings.Contains(contentType, "text/html")) {
		return true
	}
	return false
}

func hasProviderPolicyBlockMarkers(lowerBody string) bool {
	if lowerBody == "" {
		return false
	}
	return strings.Contains(lowerBody, "flagged for possible cybersecurity risk") ||
		strings.Contains(lowerBody, "trusted access for cyber") ||
		strings.Contains(lowerBody, "content was flagged")
}
