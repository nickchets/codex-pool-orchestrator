package main

import (
	"strings"
	"testing"
)

func TestSanitizeStatusMessageRedactsGeminiAndClaudeTokens(t *testing.T) {
	input := "Bearer ya29.abcdefghijklmnopqrstuvwxyz012345 Bearer 1//abcdefghijklmnopqrstuvwxyz012345 sk-ant-oat01-pool-abcdefghijklmnopqrstuvwxyz012345 sk-proj-abcdefghijklmnopqrstuvwxyz012345"
	got := sanitizeStatusMessage(input)

	for _, raw := range []string{
		"ya29.abcdefghijklmnopqrstuvwxyz012345",
		"1//abcdefghijklmnopqrstuvwxyz012345",
		"sk-ant-oat01-pool-abcdefghijklmnopqrstuvwxyz012345",
		"sk-proj-abcdefghijklmnopqrstuvwxyz012345",
	} {
		if strings.Contains(got, raw) {
			t.Fatalf("sanitizeStatusMessage leaked %q in %q", raw, got)
		}
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("expected compact redaction markers in %q", got)
	}
}
