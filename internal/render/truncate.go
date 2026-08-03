package render

import (
	"strings"
	"unicode/utf8"
)

// DefaultMaxChars is the per-page character budget for truncation. It matches
// symfetch's default char_limit (15000).
const DefaultMaxChars = 15000

// Truncate applies symfetch's truncate-and-store window semantics to a body:
// when the rune count exceeds maxChars the head and tail halves of the budget
// are kept and the omitted middle is marked. The result reports whether the
// body was truncated. (Persisting the full text to the cache is owned by the
// token-budget issue, B-19.)
func Truncate(text string, maxChars int) (string, bool) {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text, false
	}
	head := takeRunes(text, maxChars/2)
	tail := takeRunesFromEnd(text, maxChars-maxChars/2)
	omitted := utf8.RuneCountInString(text) - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	marker := "\n\n… [truncated: " + itoa(omitted) + " runes omitted] …\n\n"
	return head + marker + tail, true
}

func takeRunes(text string, count int) string {
	if count <= 0 {
		return ""
	}
	var builder strings.Builder
	seen := 0
	for _, r := range text {
		if seen >= count {
			break
		}
		builder.WriteRune(r)
		seen++
	}
	return builder.String()
}

func takeRunesFromEnd(text string, count int) string {
	if count <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= count {
		return text
	}
	return string(runes[len(runes)-count:])
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
