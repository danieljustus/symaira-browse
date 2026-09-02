package output

import (
	"bytes"
	"fmt"
	"testing"
)

func BenchmarkWriteHumanCookies1000(b *testing.B) {
	cookies := make([]any, 1000)
	for i := range cookies {
		cookies[i] = map[string]any{
			"name":   fmt.Sprintf("cookie-%04d", i),
			"domain": ".example.com",
			"path":   "/",
			"secure": true,
		}
	}
	data := map[string]any{
		"origin":  "https://example.com",
		"cookies": cookies,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buffer bytes.Buffer
		if err := Write(&buffer, OK(data, nil), FormatText); err != nil {
			b.Fatal(err)
		}
	}
}
