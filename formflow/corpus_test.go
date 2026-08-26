package formflow

import (
	"io"
	"net/http"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/testserver"
)

// fetchFixture downloads a testserver fixture and returns its HTML body.
func fetchFixture(t *testing.T, fixture testserver.Fixture) string {
	t.Helper()
	server := testserver.New(t)
	response, err := http.Get(server.URLFor(fixture)) //nolint:noctx // deterministic in-process fixture
	if err != nil {
		t.Fatalf("fetch fixture %s: %v", fixture, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	return string(body)
}

func TestDetectBlockHostileCorpus(t *testing.T) {
	// The hostile-form corpus (issue #281) must classify deterministically:
	// each representative fixture maps to its contract outcome.
	cases := []struct {
		fixture testserver.Fixture
		want    Code
	}{
		{testserver.HostileForm, ""},
		{testserver.Form, ""},
		{testserver.CaptchaForm, CodeBlockedCaptcha},
		{testserver.BotwallPage, CodeBlockedBotwall},
		{testserver.ConfirmationPage, ""},
		{testserver.ConfirmationDone, ""},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.fixture), func(t *testing.T) {
			html := fetchFixture(t, testCase.fixture)
			if got := DetectBlock("", html); got != testCase.want {
				t.Fatalf("DetectBlock(html) = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestDetectBlockTextMarkers(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Code
	}{
		{"captcha text", "I'm not a robot", CodeBlockedCaptcha},
		{"cloudflare check", "Checking your browser before accessing example.com", CodeBlockedBotwall},
		{"unusual traffic", "Our systems have detected unusual traffic from your computer", CodeBlockedBotwall},
		{"access denied", "Access Denied", CodeBlockedBotwall},
		{"german wall", "Ungewöhnlicher Datenverkehr erkannt", CodeBlockedBotwall},
		{"rate limit", "Too many requests, slow down", CodeBlockedBotwall},
		{"clean page", "Your request has been received", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DetectBlock(testCase.text, ""); got != testCase.want {
				t.Fatalf("DetectBlock(%q) = %q, want %q", testCase.text, got, testCase.want)
			}
		})
	}
}

func TestDetectBlockPrefersCaptchaOverWall(t *testing.T) {
	html := `<div class="g-recaptcha" data-sitekey="x"></div>`
	text := "Access denied"
	if got := DetectBlock(text, html); got != CodeBlockedCaptcha {
		t.Fatalf("captcha must win over botwall, got %q", got)
	}
}
