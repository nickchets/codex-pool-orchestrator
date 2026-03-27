package main

import (
	"regexp"
	"strings"
)

var (
	statusMessageWhitespacePattern = regexp.MustCompile(`\s+`)
	openAIKeyLikePattern           = regexp.MustCompile(`sk-[A-Za-z0-9*._-]{10,}`)
	claudeKeyLikePattern           = regexp.MustCompile(`sk-ant-[A-Za-z0-9*._-]{10,}`)
	geminiAccessTokenLikePattern   = regexp.MustCompile(`ya29\.[A-Za-z0-9._-]{10,}`)
	geminiRefreshTokenLikePattern  = regexp.MustCompile(`1//[A-Za-z0-9._-]{10,}`)
)

func compactSecretToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 14 {
		return value
	}
	return value[:7] + "..." + value[len(value)-4:]
}

func sanitizeStatusMessage(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = openAIKeyLikePattern.ReplaceAllStringFunc(value, compactSecretToken)
	value = claudeKeyLikePattern.ReplaceAllStringFunc(value, compactSecretToken)
	value = geminiAccessTokenLikePattern.ReplaceAllStringFunc(value, compactSecretToken)
	value = geminiRefreshTokenLikePattern.ReplaceAllStringFunc(value, compactSecretToken)
	value = strings.ReplaceAll(value, "You can find your API key at https://platform.openai.com/account/api-keys.", "")
	value = strings.ReplaceAll(value, "You can find your API key at https://platform.openai.com/account/api-keys", "")
	value = statusMessageWhitespacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func clipMiddle(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 7 {
		return value[:max]
	}
	head := (max - 3) / 2
	tail := max - 3 - head
	if head < 1 {
		head = 1
	}
	if tail < 1 {
		tail = 1
	}
	return value[:head] + "..." + value[len(value)-tail:]
}

func clipOpaque(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "|")
	if len(parts) == 1 {
		return clipMiddle(value, 26)
	}
	for i := range parts {
		parts[i] = clipMiddle(strings.TrimSpace(parts[i]), 16)
	}
	return strings.Join(parts, "|")
}
