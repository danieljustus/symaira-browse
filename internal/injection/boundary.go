// Package injection wraps page-derived output in unforgeable content
// boundaries (ARCHITEKTUR.md §5.7). Every response that carries page content
// gets a fresh nonce; the markers are useless without the matching
// content_boundaries field delivered out-of-band (JSON mode) or the marker
// line itself in text mode, so page content that mimics the marker text
// cannot break the boundary.
package injection

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// startPrefix and endPrefix are the human-readable marker stems.
	startPrefix = "SYMBROWSE_CONTENT_START"
	endPrefix   = "SYMBROWSE_CONTENT_END"
	// nonceBytes is the per-response entropy; 16 bytes = 128 bits.
	nonceBytes = 16
)

// Boundary describes one content boundary. In JSON mode it is delivered as an
// own field of the response data (never inlined into the content); in text
// mode the markers are the two lines around the content.
type Boundary struct {
	Nonce  string `json:"nonce"`
	Origin string `json:"origin"`
	Start  string `json:"start"`
	End    string `json:"end"`
}

// New generates a fresh boundary for content originating from origin.
func New(origin string) (Boundary, error) {
	raw := make([]byte, nonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return Boundary{}, fmt.Errorf("generate boundary nonce: %w", err)
	}
	nonce := hex.EncodeToString(raw)
	return Boundary{
		Nonce:  nonce,
		Origin: origin,
		Start:  markerLine(startPrefix, nonce, origin),
		End:    markerLine(endPrefix, nonce, origin),
	}, nil
}

func markerLine(prefix, nonce, origin string) string {
	return fmt.Sprintf("──── %s nonce=%s origin=%s ────", prefix, nonce, origin)
}

// WrapText places the content between the start and end marker lines.
func (b Boundary) WrapText(content string) string {
	var builder strings.Builder
	builder.WriteString(b.Start)
	builder.WriteString("\n")
	builder.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		builder.WriteString("\n")
	}
	builder.WriteString(b.End)
	builder.WriteString("\n")
	return builder.String()
}

// ParseText extracts the content between the boundary markers, verifying that
// the start marker carries the expected nonce and that the matching end
// marker follows. Content that merely mimics marker text without the expected
// nonce is treated as content and cannot break the boundary.
func ParseText(wrapped, expectedNonce string) (string, Boundary, error) {
	lines := strings.Split(wrapped, "\n")
	startIndex := -1
	var boundary Boundary
	for index, line := range lines {
		_, nonce, origin, ok := parseMarker(line, startPrefix)
		if ok && nonce == expectedNonce {
			startIndex = index
			boundary = Boundary{Nonce: nonce, Origin: origin, Start: line}
			break
		}
	}
	if startIndex < 0 {
		return "", Boundary{}, errors.New("no content boundary start marker with the expected nonce")
	}
	for index := startIndex + 1; index < len(lines); index++ {
		_, nonce, _, ok := parseMarker(lines[index], endPrefix)
		if ok && nonce == expectedNonce {
			boundary.End = lines[index]
			content := strings.Join(lines[startIndex+1:index], "\n")
			return content, boundary, nil
		}
	}
	return "", Boundary{}, errors.New("content boundary end marker with the expected nonce not found")
}

// parseMarker recognises a marker line and returns its nonce and origin.
func parseMarker(line, prefix string) (string, string, string, bool) {
	trimmed := strings.TrimSpace(line)
	marker := "──── " + prefix + " nonce="
	if !strings.HasPrefix(trimmed, marker) {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(trimmed, marker)
	nonceEnd := strings.Index(rest, " origin=")
	if nonceEnd < 0 {
		return "", "", "", false
	}
	nonce := rest[:nonceEnd]
	origin := strings.TrimSuffix(strings.TrimPrefix(rest[nonceEnd:], " origin="), " ────")
	if nonce == "" || origin == "" {
		return "", "", "", false
	}
	return prefix, nonce, origin, true
}
