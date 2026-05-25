package utils

import (
	"strings"
	"testing"

	"examtopics-downloader/internal/models"
)

func TestRenderQuestionCardUsesCanonicalSolution(t *testing.T) {
	data := []models.QuestionData{
		{
			Title:        "HOTSPOT question",
			Content:      "Use the drop-down menus to select. Hot Area:",
			QuestionLink: "https://www.examtopics.com/discussions/microsoft/view/33477-exam-ai-900-topic-1-question-3-discussion/",
			ExhibitURLs: []string{
				"https://www.examtopics.com/assets/media/exam-media/04234/0000300001.png",
			},
			// Hot Area image from the question body — must NOT be used as the
			// answer when a canonical Solution is present.
			AnswerExhibitURLs: []string{
				"https://www.examtopics.com/assets/media/exam-media/04234/0000400001.png",
			},
			Solution: &models.AnswerSolution{
				Images: []string{
					"https://www.examtopics.com/assets/media/exam-media/04234/0000500001.png",
				},
				Description: "Box 1: 11 -\n[[IMG:https://www.examtopics.com/assets/media/exam-media/04234/0000600001.png]]\nTP = True Positive.\nSee https://docs.microsoft.com/x for details",
				DescImages: []string{
					"https://www.examtopics.com/assets/media/exam-media/04234/0000600001.png",
				},
				References: []string{"https://docs.microsoft.com/x"},
			},
		},
	}
	html := buildQuestionCards(data, false)
	if html == "" {
		t.Fatalf("expected card to be rendered")
	}
	// Canonical answer image must appear in the answer block.
	if !strings.Contains(html, "0000500001.png") {
		t.Fatalf("canonical answer image 0000500001.png missing, got:\n%s", html)
	}
	// Inline description image must appear too.
	if !strings.Contains(html, "0000600001.png") {
		t.Fatalf("answer description image 0000600001.png missing, got:\n%s", html)
	}
	// Hot Area image MUST NOT be presented as the answer when canonical exists.
	if strings.Contains(html, "answer-block-fallback") {
		t.Fatalf("fallback block must NOT appear when canonical solution exists")
	}
	if strings.Contains(html, "0000400001.png") {
		// The Hot Area image is part of the question body's ExhibitURLs in
		// reality, but for this test it was deliberately put into the answer
		// bucket only. Since the canonical solution wins, the Hot Area image
		// must NOT be rendered as the answer.
		t.Fatalf("Hot Area image leaked into rendered answer block:\n%s", html)
	}
	// References list should appear.
	if !strings.Contains(html, "answer-references") || !strings.Contains(html, "docs.microsoft.com/x") {
		t.Fatalf("references section missing: %s", html)
	}
	// Canonical class signature.
	if !strings.Contains(html, "answer-block-canonical") {
		t.Fatalf("canonical block CSS class missing:\n%s", html)
	}
	// Description text preserved (newline kept as <br>).
	if !strings.Contains(html, "Box 1: 11") || !strings.Contains(html, "TP = True Positive.") {
		t.Fatalf("description text missing:\n%s", html)
	}
	// data-correct must be empty for image-only HOTSPOT answers: there's no
	// selectable letter to mark correct, and the previous behaviour of
	// defaulting to "A" was misleading.
	if !strings.Contains(html, `data-correct=""`) {
		t.Fatalf("expected data-correct=\"\" for image-only HOTSPOT (no letter answer):\n%s", html)
	}
}

func TestRenderQuestionCardFallbackWhenNoSolution(t *testing.T) {
	data := []models.QuestionData{
		{
			Title:             "HOTSPOT question with no canonical answer",
			Content:           "Hot Area:",
			QuestionLink:      "https://www.examtopics.com/discussions/microsoft/view/9999",
			ExhibitURLs:       []string{"https://img.examtopics.com/q.png"},
			AnswerExhibitURLs: []string{"https://img.examtopics.com/hotarea.png"},
			Solution:          nil,
		},
	}
	html := buildQuestionCards(data, false)
	if !strings.Contains(html, "answer-block-fallback") {
		t.Fatalf("fallback block must appear when no canonical solution:\n%s", html)
	}
	if !strings.Contains(html, "Answer Area (unverified)") {
		t.Fatalf("fallback label must read 'Answer Area (unverified)':\n%s", html)
	}
	if !strings.Contains(html, "hotarea.png") {
		t.Fatalf("Hot Area image must appear in fallback block:\n%s", html)
	}
	if strings.Contains(html, "answer-block-canonical") {
		t.Fatalf("canonical block must NOT appear without solution:\n%s", html)
	}
}

func TestRenderAnswerDescriptionHTMLLinkifies(t *testing.T) {
	out := renderAnswerDescriptionHTML("See https://example.com/x for refs\nNext line")
	if !strings.Contains(out, `href="https://example.com/x"`) {
		t.Fatalf("URL should be turned into an anchor, got %q", out)
	}
	if !strings.Contains(out, "<br>") {
		t.Fatalf("newline should become <br>, got %q", out)
	}
}

func TestRenderAnswerDescriptionHTMLInlinesImageMarkers(t *testing.T) {
	out := renderAnswerDescriptionHTML("before [[IMG:https://img.example/a.png]] after")
	if !strings.Contains(out, `class="answer-desc-image"`) {
		t.Fatalf("expected answer-desc-image class, got %q", out)
	}
	if !strings.Contains(out, `src="https://img.example/a.png"`) {
		t.Fatalf("expected image src in output, got %q", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Fatalf("expected surrounding text preserved, got %q", out)
	}
}

func TestRenderAnswerDescriptionHTMLEscapesHTML(t *testing.T) {
	out := renderAnswerDescriptionHTML("a < b && c > d")
	if strings.Contains(out, "<b") || strings.Contains(out, "&&") {
		t.Fatalf("expected HTML special characters escaped, got %q", out)
	}
}

func TestCanonicalBlockSuppressedWhenOptionsExist(t *testing.T) {
	// Standard multi-choice question with a canonical solution available:
	// the interactive options must stay the source of truth so users can
	// practise without spoilers. data-correct should still reflect the
	// canonical letter.
	data := []models.QuestionData{
		{
			Title:        "Standard multi-choice",
			Content:      "Pick the best.",
			QuestionLink: "https://www.examtopics.com/discussions/microsoft/view/abc",
			Questions: []string{
				"A. first option",
				"B. second option",
				"C. third option",
			},
			Solution: &models.AnswerSolution{
				Letter:      "B",
				Description: "B is correct because reasons.",
			},
		},
	}
	html := buildQuestionCards(data, false)
	if strings.Contains(html, "answer-block-canonical") {
		t.Fatalf("canonical block must NOT render when interactive options exist (would spoil practice mode):\n%s", html)
	}
	if !strings.Contains(html, `data-correct="B"`) {
		t.Fatalf("data-correct should use canonical Solution.Letter (B), got:\n%s", html)
	}
	if !strings.Contains(html, "opt-letter") {
		t.Fatalf("interactive options should still render, got:\n%s", html)
	}
}

func TestCanonicalLetterDrivesDataCorrectOverHotAreaFallback(t *testing.T) {
	// When the discussion-page Answer text is empty/wrong but Solution.Letter
	// is known, data-correct should follow the canonical letter.
	data := []models.QuestionData{
		{
			Title:        "Q",
			QuestionLink: "https://www.examtopics.com/discussions/microsoft/view/xyz",
			Questions: []string{
				"A. a",
				"B. b",
				"C. c",
				"D. d",
			},
			Answer:   "",
			Solution: &models.AnswerSolution{Letter: "CD"},
		},
	}
	html := buildQuestionCards(data, false)
	if !strings.Contains(html, `data-correct="C,D"`) {
		t.Fatalf("expected data-correct=\"C,D\" from canonical letter CD, got:\n%s", html)
	}
}
