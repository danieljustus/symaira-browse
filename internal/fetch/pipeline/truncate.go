package pipeline

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/danieljustus/symaira-browse/internal/budget"
)

const (
	// DefaultCharLimit is the default per-page character limit for
	// truncate-and-store. Content under this limit is returned whole.
	DefaultCharLimit = 15000

	// DefaultMaxStored is the default maximum size (bytes) for stored
	// full text files. 2 MB is generous enough for very long pages.
	DefaultMaxStored = 2 * 1024 * 1024
)

// StoreOptions configures the truncate-and-store behaviour.
type StoreOptions struct {
	CharLimit int           // character budget; content over this is truncated
	StoreDir  string        // directory for storing full text files
	Cache     *budget.Cache // unified output cache; takes precedence over StoreDir
	HeadRatio float64       // fraction of CharLimit allocated to head (0-1)
	TailRatio float64       // fraction of CharLimit allocated to tail (0-1)
	MaxStored int           // max bytes for stored full text
}

func (o *StoreOptions) setDefaults() {
	if o.CharLimit <= 0 {
		o.CharLimit = DefaultCharLimit
	}
	if o.HeadRatio <= 0 || o.HeadRatio >= 1 {
		o.HeadRatio = 0.8
	}
	if o.TailRatio <= 0 || o.TailRatio >= 1 {
		o.TailRatio = 0.2
	}
	if o.MaxStored <= 0 {
		o.MaxStored = DefaultMaxStored
	}
}

// TruncateAndStore applies the Hermes-style truncate-and-store pattern:
//   - Content under CharLimit is returned unchanged (stored=false).
//   - Long content is truncated to a head+tail window and the full text
//     is stored under StoreDir. The returned string includes a footer
//     with the absolute file path and a concrete read offset.
//   - Stored full text is capped at MaxStored bytes.
//
// Returns (output, stored, error).
func TruncateAndStore(content string, opts StoreOptions) (string, bool, error) {
	opts.setDefaults()

	charCount := utf8.RuneCountInString(content)
	if charCount <= opts.CharLimit {
		return content, false, nil
	}

	if opts.StoreDir == "" && opts.Cache == nil {
		return content, false, fmt.Errorf("store directory not specified")
	}

	// Ensure the legacy direct-file store exists. The unified cache creates
	// and secures its own root in budget.Cache.Store.
	if opts.Cache == nil {
		if err := os.MkdirAll(opts.StoreDir, 0700); err != nil {
			return content, false, fmt.Errorf("create store dir: %w", err)
		}
	}

	// Compute head and tail sizes in runes.
	totalWindow := int(float64(opts.CharLimit) * (opts.HeadRatio + opts.TailRatio))
	if totalWindow <= 0 {
		totalWindow = opts.CharLimit
	}
	headSize := int(float64(totalWindow) * opts.HeadRatio / (opts.HeadRatio + opts.TailRatio))
	tailSize := totalWindow - headSize
	if headSize <= 0 {
		headSize = 1
	}
	if tailSize <= 0 {
		tailSize = 1
	}

	// Extract head (first headSize runes).
	head := truncateTo(content, headSize)
	// Extract tail (last tailSize runes).
	tail := tailOf(content, tailSize)

	// Store the full text, capped at MaxStored.
	storedText := content
	if len([]byte(storedText)) > opts.MaxStored {
		storedText = capBytes(storedText, opts.MaxStored)
	}

	cacheID := ""
	filePath := ""
	if opts.Cache != nil {
		var err error
		cacheID, err = opts.Cache.Store([]byte(storedText))
		if err != nil {
			return content, false, fmt.Errorf("store full text in output cache: %w", err)
		}
	} else {
		// Write the stored text to the legacy direct-file store.
		hash := sha256.Sum256([]byte(content))
		fileName := fmt.Sprintf("%x.txt", hash[:8])
		filePath = filepath.Join(opts.StoreDir, fileName)
		if err := os.WriteFile(filePath, []byte(storedText), 0600); err != nil {
			return content, false, fmt.Errorf("write stored text: %w", err)
		}
	}

	footer := ""
	if cacheID != "" {
		footer = fmt.Sprintf("\n\n--- Full text stored: cache_id=%s ---", cacheID)
	} else {
		// Compute the byte offset where the tail window starts in the legacy
		// direct-file store.
		tailOffset := computeTailOffset(content, tailSize)
		footer = fmt.Sprintf("\n\n--- Full text stored: %s (offset=%d) ---", filePath, tailOffset)
	}

	// Assemble: head + "\n\n...\n\n" + tail + footer
	result := head + "\n\n...\n\n" + tail + footer

	logAttrs := []any{
		"original_chars", charCount,
		"head_chars", utf8.RuneCountInString(head),
		"tail_chars", utf8.RuneCountInString(tail),
	}
	if cacheID != "" {
		logAttrs = append(logAttrs, "cache_id", cacheID)
	} else {
		logAttrs = append(logAttrs, "stored_path", filePath, "offset", computeTailOffset(content, tailSize))
	}
	slog.Debug("truncate-and-store applied", logAttrs...)

	return result, true, nil
}

// CacheIDFromOutput extracts the unified output-cache id from a
// truncate-and-store footer. Legacy absolute-path footers return no id.
func CacheIDFromOutput(output string) string {
	const marker = "cache_id="
	idx := strings.Index(output, marker)
	if idx < 0 {
		return ""
	}
	value := output[idx+len(marker):]
	if end := strings.IndexAny(value, " \n\r)"); end >= 0 {
		value = value[:end]
	}
	if len(value) != len("out_")+12 || !strings.HasPrefix(value, "out_") {
		return ""
	}
	if strings.Trim(value[len("out_"):], "0123456789abcdef") != "" {
		return ""
	}
	return value
}

// truncateTo returns the first n runes of s.
func truncateTo(s string, n int) string {
	count := 0
	for i := range s {
		count++
		if count == n {
			return s[:i+len(string(s[i]))]
		}
	}
	return s
}

// tailOf returns the last n runes of s.
func tailOf(s string, n int) string {
	charCount := utf8.RuneCountInString(s)
	if charCount <= n {
		return s
	}
	start := 0
	count := 0
	for i := range s {
		if count == charCount-n {
			start = i
			break
		}
		count++
	}
	return s[start:]
}

// computeTailOffset returns the byte offset of the last n runes.
func computeTailOffset(s string, n int) int {
	charCount := utf8.RuneCountInString(s)
	if charCount <= n {
		return 0
	}
	count := 0
	for i := range s {
		if count == charCount-n {
			return i
		}
		count++
	}
	return len(s)
}

// capBytes returns s truncated to at most maxBytes UTF-8 bytes.
func capBytes(s string, maxBytes int) string {
	if len([]byte(s)) <= maxBytes {
		return s
	}
	// Walk rune by rune, accumulating bytes until we hit the cap.
	var sb strings.Builder
	byteCount := 0
	for _, r := range s {
		runeBytes := len(string(r))
		if byteCount+runeBytes > maxBytes {
			break
		}
		sb.WriteRune(r)
		byteCount += runeBytes
	}
	return sb.String()
}
