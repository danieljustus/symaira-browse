package main

import (
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestParseCurlCookieLine(t *testing.T) {
	line := "# Netscape HTTP Cookie File\n" // header must be skipped
	if _, ok := parseCurlCookieLine(line); ok {
		t.Fatal("comment line parsed as cookie")
	}
	cookie, ok := parseCurlCookieLine(".example.com\tTRUE\t/\tTRUE\t1767225600\tsession\tabc123")
	if !ok {
		t.Fatal("valid line rejected")
	}
	if cookie.Name != "session" || cookie.Value != "abc123" || cookie.Domain != ".example.com" || cookie.Path != "/" || !cookie.Secure {
		t.Fatalf("cookie = %#v", cookie)
	}
	if cookie.Expires != 1767225600 {
		t.Fatalf("expires = %v", cookie.Expires)
	}
	if _, ok := parseCurlCookieLine("too few fields"); ok {
		t.Fatal("malformed line accepted")
	}
}

func TestRevealCookieAllowlist(t *testing.T) {
	if !revealCookie("session", "all") {
		t.Fatal("reveal=all must reveal every cookie")
	}
	if !revealCookie("session", "session,theme") {
		t.Fatal("explicit allowlist must reveal listed cookie")
	}
	if revealCookie("session", "theme") {
		t.Fatal("unlisted cookie must stay masked")
	}
	if revealCookie("session", "") {
		t.Fatal("empty reveal must keep everything masked")
	}
}

func TestMaskSecret(t *testing.T) {
	if got := maskSecret(""); got != "" {
		t.Fatalf("empty secret = %q", got)
	}
	if got := maskSecret("short"); got != "••••" {
		t.Fatalf("short secret = %q", got)
	}
	original := "abcdefghijklmnop"
	got := maskSecret(original)
	if got == original {
		t.Fatalf("long secret not masked: %q", got)
	}
	if got != "abcd••••mnop" {
		t.Fatalf("unexpected mask shape: %q", got)
	}
}

func TestCookieFlags(t *testing.T) {
	if got := cookieFlags(engine.Cookie{}); got != "-" {
		t.Fatalf("no flags = %q", got)
	}
	flags := cookieFlags(engine.Cookie{Secure: true, HTTPOnly: true, Session: true})
	if flags != "secure,httpOnly,session" {
		t.Fatalf("flags = %q", flags)
	}
}
