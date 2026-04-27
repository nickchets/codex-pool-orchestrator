package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlainTextOverlapDeduperTrimsSuffixPrefixOverlap(t *testing.T) {
	d := newPlainTextOverlapDeduper("Hello world")
	if got := d.trim("world again"); got != " again" {
		t.Fatalf("trimmed continuation = %q, want %q", got, " again")
	}
}

func TestPlainTextOverlapDeduperUnicodeOverlapIsUTF8Safe(t *testing.T) {
	prior := strings.Repeat("a", streamContinuationDedupWindowBytes-1) + "🌍"
	d := newPlainTextOverlapDeduper(prior)
	if !utf8.ValidString(d.prior) {
		t.Fatalf("dedupe prior suffix is not valid UTF-8: %q", d.prior)
	}
	got := d.trim("🌍 again")
	if got != " again" {
		t.Fatalf("trimmed unicode continuation = %q, want %q", got, " again")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed unicode continuation is not valid UTF-8: %q", got)
	}
}
