package fetch

import (
	"bytes"
	"fmt"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"
)

// normaliseCharset detects the charset from the Content-Type header and/or
// HTML meta tags, then transcodes the body to UTF-8.
func normaliseCharset(body []byte, contentType string) ([]byte, error) {
	enc, _, _ := charset.DetermineEncoding(body, contentType)
	if enc == nil {
		return body, nil
	}

	reader := transform.NewReader(bytes.NewReader(body), enc.NewDecoder())
	out := &bytes.Buffer{}
	if _, err := out.ReadFrom(reader); err != nil {
		return nil, fmt.Errorf("charset decode: %w", err)
	}
	return out.Bytes(), nil
}
