package render

import (
	"strings"
	"testing"
)

func TestStripBase64Images_MarkdownBase64(t *testing.T) {
	input := `# Hello

![Logo](data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==)

Some text here.`
	got := StripBase64Images(input)

	if strings.Contains(got, "data:image") {
		t.Errorf("expected base64 data URI to be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE: Logo]") {
		t.Errorf("expected [IMAGE: Logo] placeholder, got:\n%s", got)
	}
	if !strings.Contains(got, "# Hello") {
		t.Errorf("expected heading preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "Some text here.") {
		t.Errorf("expected text preserved, got:\n%s", got)
	}
}

func TestStripBase64Images_MarkdownNoAlt(t *testing.T) {
	input := `![ ](data:image/jpeg;base64,/9j/4AAQSkZJRg==)`
	got := StripBase64Images(input)

	if strings.Contains(got, "data:image") {
		t.Errorf("expected base64 data URI to be stripped, got:\n%s", got)
	}
	// Empty alt (just space) should become [IMAGE]
	if !strings.Contains(got, "[IMAGE]") {
		t.Errorf("expected [IMAGE] placeholder for empty alt, got:\n%s", got)
	}
}

func TestStripBase64Images_MarkdownEmptyAlt(t *testing.T) {
	input := `![](data:image/png;base64,abc123==)`
	got := StripBase64Images(input)

	if strings.Contains(got, "data:image") {
		t.Errorf("expected base64 data URI to be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE]") {
		t.Errorf("expected [IMAGE] placeholder for empty alt, got:\n%s", got)
	}
}

func TestStripBase64Images_RealURLPreserved(t *testing.T) {
	input := `![Photo](https://example.com/photo.jpg)

![Another](http://cdn.example.com/img.png)`
	got := StripBase64Images(input)

	if !strings.Contains(got, "https://example.com/photo.jpg") {
		t.Errorf("expected real URL preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "http://cdn.example.com/img.png") {
		t.Errorf("expected real URL preserved, got:\n%s", got)
	}
}

func TestStripBase64Images_HTMLImgBase64(t *testing.T) {
	input := `<p>Text before</p>
<img src="data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7" alt="Spacer" />
<p>Text after</p>`
	got := StripBase64Images(input)

	if strings.Contains(got, "data:image") {
		t.Errorf("expected base64 data URI to be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE: Spacer]") {
		t.Errorf("expected [IMAGE: Spacer] placeholder, got:\n%s", got)
	}
	if !strings.Contains(got, "Text before") {
		t.Errorf("expected text before preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "Text after") {
		t.Errorf("expected text after preserved, got:\n%s", got)
	}
}

func TestStripBase64Images_HTMLImgNoAlt(t *testing.T) {
	input := `<img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=" />`
	got := StripBase64Images(input)

	if strings.Contains(got, "data:image") {
		t.Errorf("expected base64 data URI to be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE]") {
		t.Errorf("expected [IMAGE] placeholder for no alt, got:\n%s", got)
	}
}

func TestStripBase64Images_NoBase64Content(t *testing.T) {
	input := `# No Images Here

Just plain text with [links](https://example.com).`
	got := StripBase64Images(input)

	if got != input {
		t.Errorf("expected no changes when no base64 images present, got:\n%s", got)
	}
}

func TestStripBase64Images_MultipleBase64(t *testing.T) {
	input := `![First](data:image/png;base64,AAAA)

Text between.

![Second](data:image/jpeg;base64,BBBB)`
	got := StripBase64Images(input)

	if strings.Contains(got, "data:image") {
		t.Errorf("expected all base64 data URIs to be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE: First]") {
		t.Errorf("expected [IMAGE: First] placeholder, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE: Second]") {
		t.Errorf("expected [IMAGE: Second] placeholder, got:\n%s", got)
	}
}

func TestStripBase64Images_EmptyContent(t *testing.T) {
	got := StripBase64Images("")
	if got != "" {
		t.Errorf("expected empty string, got: %q", got)
	}
}

func TestStripBase64Images_MixedContent(t *testing.T) {
	input := `# Article

![Hero](data:image/webp;base64,UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdA)

Regular paragraph.

<img src="https://example.com/real.jpg" alt="Real Image" />

![Diagram](data:image/svg+xml;base64,PHN2Zz4=)

Final text.`
	got := StripBase64Images(input)

	if strings.Contains(got, "data:image") {
		t.Errorf("expected all base64 data URIs to be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE: Hero]") {
		t.Errorf("expected [IMAGE: Hero] placeholder, got:\n%s", got)
	}
	if !strings.Contains(got, "[IMAGE: Diagram]") {
		t.Errorf("expected [IMAGE: Diagram] placeholder, got:\n%s", got)
	}
	// Real URL img should be preserved
	if !strings.Contains(got, "https://example.com/real.jpg") {
		t.Errorf("expected real URL preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "Regular paragraph.") {
		t.Errorf("expected paragraph preserved, got:\n%s", got)
	}
}
