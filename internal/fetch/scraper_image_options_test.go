package fetch

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestExtractAnswerOptionsCapturesImageOnlyChoice(t *testing.T) {
	html := `
<div class="card-text">Pick the diagram that matches.</div>
<ul>
  <li class="multi-choice-item">A. text answer</li>
  <li class="multi-choice-item">B. <img src="//img.examtopics.com/ai-900/b.png"></li>
  <li class="multi-choice-item"><img src="https://img.examtopics.com/ai-900/c.png"></li>
  <li class="multi-choice-item">D. <img data-src="/media/ai-900/d.png"></li>
</ul>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed parsing test html: %v", err)
	}

	opts := extractAnswerOptions(doc)
	if len(opts) != 4 {
		t.Fatalf("expected 4 options, got %d: %#v", len(opts), opts)
	}

	if !strings.HasPrefix(opts[0], "A.") || strings.Contains(opts[0], "[[IMG:") {
		t.Errorf("text-only option corrupted: %q", opts[0])
	}

	checks := []struct {
		idx     int
		wantURL string
	}{
		{1, "https://img.examtopics.com/ai-900/b.png"},
		{2, "https://img.examtopics.com/ai-900/c.png"},
		{3, "https://www.examtopics.com/media/ai-900/d.png"},
	}
	for _, c := range checks {
		want := "[[IMG:" + c.wantURL + "]]"
		if !strings.Contains(opts[c.idx], want) {
			t.Errorf("option %d missing marker %q, got %q", c.idx, want, opts[c.idx])
		}
	}
}

func TestExtractAnswerOptionsFallsBackToOtherSelectors(t *testing.T) {
	html := `
<div class="question-choices-container">
  <ul>
    <li>A. first</li>
    <li>B. second</li>
  </ul>
</div>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed parsing test html: %v", err)
	}

	opts := extractAnswerOptions(doc)
	if len(opts) != 2 {
		t.Fatalf("expected fallback selector to find 2 options, got %d: %#v", len(opts), opts)
	}
}

func TestExtractAnswerOptionsReturnsNilWhenNoChoices(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<p>no answers here</p>`))
	if err != nil {
		t.Fatalf("failed parsing test html: %v", err)
	}
	if opts := extractAnswerOptions(doc); opts != nil {
		t.Fatalf("expected nil for no choices, got %#v", opts)
	}
}
