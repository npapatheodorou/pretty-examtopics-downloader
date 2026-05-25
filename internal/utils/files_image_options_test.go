package utils

import (
	"strings"
	"testing"

	"examtopics-downloader/internal/models"
)

func TestSplitImageMarkersExtractsURLs(t *testing.T) {
	clean, urls := splitImageMarkers("Some text [[IMG:https://img.examtopics.com/x.png]] and [[IMG:https://img.examtopics.com/y.png]]")
	if clean != "Some text and" {
		t.Fatalf("unexpected clean text: %q", clean)
	}
	if len(urls) != 2 || urls[0] != "https://img.examtopics.com/x.png" || urls[1] != "https://img.examtopics.com/y.png" {
		t.Fatalf("unexpected urls: %#v", urls)
	}
}

func TestSplitImageMarkersDedupes(t *testing.T) {
	_, urls := splitImageMarkers("[[IMG:https://a.example/1.png]] [[IMG:https://a.example/1.png]]")
	if len(urls) != 1 {
		t.Fatalf("expected dedupe to 1 url, got %#v", urls)
	}
}

func TestSplitImageMarkersNoMarkers(t *testing.T) {
	clean, urls := splitImageMarkers("plain text")
	if clean != "plain text" {
		t.Fatalf("unexpected clean text: %q", clean)
	}
	if urls != nil {
		t.Fatalf("expected nil urls, got %#v", urls)
	}
}

func TestParseOptionsAssignsLettersForImageOnlyOptions(t *testing.T) {
	raw := []string{
		"[[IMG:https://img.examtopics.com/a.png]]",
		"[[IMG:https://img.examtopics.com/b.png]]",
		"[[IMG:https://img.examtopics.com/c.png]]",
		"[[IMG:https://img.examtopics.com/d.png]]",
	}
	opts := parseOptions(raw)
	if len(opts) != 4 {
		t.Fatalf("expected 4 options from image-only fallback, got %d: %#v", len(opts), opts)
	}
	want := []string{"A", "B", "C", "D"}
	for i, opt := range opts {
		if opt.Letter != want[i] {
			t.Fatalf("option %d: want letter %s, got %s", i, want[i], opt.Letter)
		}
		if !strings.HasPrefix(opt.Text, "[[IMG:") {
			t.Fatalf("option %d: expected image marker payload, got %q", i, opt.Text)
		}
	}
}

func TestParseOptionsLetterPrefixedImageOptions(t *testing.T) {
	raw := []string{
		"A. [[IMG:https://img.examtopics.com/a.png]]",
		"B. [[IMG:https://img.examtopics.com/b.png]]",
		"C. real text",
		"D. [[IMG:https://img.examtopics.com/d.png]] and trailing",
	}
	opts := parseOptions(raw)
	if len(opts) != 4 {
		t.Fatalf("expected 4 options, got %d: %#v", len(opts), opts)
	}
	if opts[0].Letter != "A" || !strings.Contains(opts[0].Text, "[[IMG:") {
		t.Fatalf("option 0 not parsed correctly: %#v", opts[0])
	}
	if opts[2].Letter != "C" || opts[2].Text != "real text" {
		t.Fatalf("text option C parsed incorrectly: %#v", opts[2])
	}
}

func TestBuildQuestionCardsRendersImageOnlyOptions(t *testing.T) {
	data := []models.QuestionData{
		{
			Title:        "Sample image question",
			Content:      "Which image best represents the architecture?",
			QuestionLink: "https://www.examtopics.com/discussions/microsoft/view/0-exam-ai-900-topic-1-question-1-discussion/",
			Questions: []string{
				"[[IMG:https://img.examtopics.com/ai-900/a.png]]",
				"[[IMG:https://img.examtopics.com/ai-900/b.png]]",
				"[[IMG:https://img.examtopics.com/ai-900/c.png]]",
				"[[IMG:https://img.examtopics.com/ai-900/d.png]]",
			},
			Answer: "Suggested Answer: B",
		},
	}
	html := buildQuestionCards(data, false)
	if html == "" {
		t.Fatalf("expected an HTML card to be rendered for image-only options")
	}
	// All four images should appear inside the rendered card as opt-image tags.
	wantTags := []string{
		"opt-image",
		"https://img.examtopics.com/ai-900/a.png",
		"https://img.examtopics.com/ai-900/b.png",
		"https://img.examtopics.com/ai-900/c.png",
		"https://img.examtopics.com/ai-900/d.png",
	}
	for _, tag := range wantTags {
		if !strings.Contains(html, tag) {
			t.Fatalf("expected rendered HTML to contain %q, output:\n%s", tag, html)
		}
	}
}

func TestBuildQuestionCardsKeepsCardWhenOnlyExhibitPresent(t *testing.T) {
	// Question whose options are unparseable but which has an exhibit image
	// must NOT be silently dropped (the historical bug that lost ~half the
	// questions on image-heavy exams).
	data := []models.QuestionData{
		{
			Title:        "Image-only question",
			Content:      "",
			QuestionLink: "https://www.examtopics.com/discussions/microsoft/view/0-exam-ai-900-topic-1-question-2-discussion/",
			Questions:    nil,
			ExhibitURLs:  []string{"https://img.examtopics.com/ai-900/exhibit1.png"},
			Answer:       "B",
		},
	}
	html := buildQuestionCards(data, false)
	if html == "" {
		t.Fatalf("expected card to be rendered even with no parseable options when an exhibit is present")
	}
	if !strings.Contains(html, "exhibit1.png") {
		t.Fatalf("expected exhibit URL in rendered HTML, got:\n%s", html)
	}
	if !strings.Contains(html, "opt-empty") {
		t.Fatalf("expected opt-empty placeholder when no options were parsed, got:\n%s", html)
	}
}

func TestBuildQuestionCardsDropsTotallyEmptyEntries(t *testing.T) {
	data := []models.QuestionData{
		{}, // nothing at all — should be dropped
	}
	html := buildQuestionCards(data, false)
	if html != "" {
		t.Fatalf("expected empty record to be dropped, got:\n%s", html)
	}
}
