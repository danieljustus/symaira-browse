package formflow

import (
	"strings"
)

// DetectBlock classifies a page as a bot-protection stop from its visible
// text and raw HTML. It returns CodeBlockedCaptcha when an interactive
// CAPTCHA widget or challenge is present, CodeBlockedBotwall for non-CAPTCHA
// walls (browser checks, rate-limit pages, access denials), and the empty
// string when no block is detected.
//
// The classification is heuristic by nature; it errs on the side of reporting
// a block, because a falsely submitted GDPR erasure request is worse than a
// falsely queued human task (issue #281).
func DetectBlock(pageText, pageHTML string) Code {
	lowerHTML := strings.ToLower(pageHTML)
	// Text markers are matched against the combined haystack: innerText is a
	// subset of the serialized document, and callers may only have one of the
	// two (unit tests feed fixture HTML without a browser).
	haystack := strings.ToLower(pageText) + "\n" + lowerHTML

	captchaMarkers := []string{
		"g-recaptcha",
		"recaptcha/api.js",
		"h-captcha",
		"hcaptcha.com",
		"cf-turnstile",
		"data-sitekey",
		"funcaptcha",
		"arkoselabs",
	}
	for _, marker := range captchaMarkers {
		if strings.Contains(lowerHTML, marker) {
			return CodeBlockedCaptcha
		}
	}
	captchaText := []string{
		"i'm not a robot",
		"ich bin kein roboter",
		"prove you are human",
		"verify that you are human",
	}
	for _, marker := range captchaText {
		if strings.Contains(haystack, marker) {
			return CodeBlockedCaptcha
		}
	}

	botwallText := []string{
		"checking your browser",
		"verify you are human",
		"unusual traffic",
		"attention required",
		"access denied",
		"please enable javascript and cookies",
		"are you a robot",
		"too many requests",
		"rate limit exceeded",
		"zugriff verweigert",
		"ungewöhnlicher datenverkehr",
	}
	for _, marker := range botwallText {
		if strings.Contains(haystack, marker) {
			return CodeBlockedBotwall
		}
	}
	return ""
}
