package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeFriendLandingIncludesAddUserControl(t *testing.T) {
	h := &proxyHandler{
		cfg: config{friendCode: "friend-code"},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	rr := httptest.NewRecorder()
	h.serveFriendLanding(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, fragment := range []string{
		`id="add-user-form"`,
		`id="add-user-email"`,
		`id="add-user-result"`,
		`createPoolUser(email)`,
		`Add Pool User`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q", fragment)
		}
	}
}

func TestServeFriendLandingUsesRequestBaseURLInMetadata(t *testing.T) {
	h := &proxyHandler{
		cfg: config{friendCode: "friend-code"},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	rr := httptest.NewRecorder()
	h.serveFriendLanding(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, fragment := range []string{
		`content="http://example.com/og-image.png"`,
		`content="http://example.com"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing fragment %q", fragment)
		}
	}
}
