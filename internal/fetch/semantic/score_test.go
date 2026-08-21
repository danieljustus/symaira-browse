package semantic_test

import (
	"testing"

	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
)

func TestBestBlock_Empty(t *testing.T) {
	doc := parseHTML(t, `<html><body></body></html>`)
	body := findFirst(doc, "body")
	got := semantic.BestBlock(body, 100)
	if got == nil {
		t.Fatal("BestBlock returned nil")
	}
}

func TestBestBlock_SingleBlock(t *testing.T) {
	doc := parseHTML(t, `<html><body><article><p>This is a test article with enough content to pass the threshold.</p></article></body></html>`)
	body := findFirst(doc, "body")
	got := semantic.BestBlock(body, 50)
	if got == nil {
		t.Fatal("BestBlock returned nil")
	}
	if got.Data != "article" {
		t.Errorf("expected article node, got %s", got.Data)
	}
}

func TestBestBlock_MultipleBlocks(t *testing.T) {
	doc := parseHTML(t, `<html><body>
		<div id="sidebar"><p>Short</p></div>
		<article class="content"><p>This is the main content with enough text to be selected as the best block.</p></article>
		<footer><p>Footer</p></footer>
	</body></html>`)
	body := findFirst(doc, "body")
	got := semantic.BestBlock(body, 30)
	if got == nil {
		t.Fatal("BestBlock returned nil")
	}
}

func TestBestBlock_AdaptiveRetry(t *testing.T) {
	doc := parseHTML(t, `<html><body>
		<article><p>Short text.</p></article>
		<main><p>This is longer content that should be selected in the adaptive retry phase.</p></main>
	</body></html>`)
	body := findFirst(doc, "body")
	got := semantic.BestBlock(body, 1000)
	if got == nil {
		t.Fatal("BestBlock returned nil")
	}
}

func TestBestBlock_FallbackToRoot(t *testing.T) {
	doc := parseHTML(t, `<html><body><p>Tiny</p></body></html>`)
	body := findFirst(doc, "body")
	got := semantic.BestBlock(body, 10000)
	if got == nil {
		t.Fatal("BestBlock returned nil")
	}
}

func TestScoreBlocks(t *testing.T) {
	doc := parseHTML(t, `<html><body>
		<article><p>This is a test article with enough content to be scored.</p></article>
		<div><p>Another block with some text.</p></div>
	</body></html>`)
	body := findFirst(doc, "body")
	blocks := semantic.ScoreBlocks(body)
	if len(blocks) == 0 {
		t.Fatal("ScoreBlocks returned no blocks")
	}
	if blocks[0].Score < blocks[len(blocks)-1].Score {
		t.Error("blocks should be sorted by score descending")
	}
}

func TestScoreBlocks_TextLen(t *testing.T) {
	doc := parseHTML(t, `<html><body><article><p>Hello World Test Content</p></article></body></html>`)
	body := findFirst(doc, "body")
	blocks := semantic.ScoreBlocks(body)
	if len(blocks) == 0 {
		t.Fatal("ScoreBlocks returned no blocks")
	}
	if blocks[0].TextLen == 0 {
		t.Error("TextLen should be > 0")
	}
}

func TestScoreBlocks_LinkDensity(t *testing.T) {
	doc := parseHTML(t, `<html><body><div><a href="#">Link text more text here</a><p>Content</p></div></body></html>`)
	body := findFirst(doc, "body")
	blocks := semantic.ScoreBlocks(body)
	if len(blocks) == 0 {
		t.Fatal("ScoreBlocks returned no blocks")
	}
}
