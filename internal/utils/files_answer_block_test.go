package utils

import (
	"strings"
	"testing"

	"examtopics-downloader/internal/models"
)

func TestBuildQuestionCardsRendersAnswerBlock(t *testing.T) {
	data := []models.QuestionData{
		{
			Title:             "HOTSPOT question",
			Content:           "Use the drop-down menus to select the answer choice. Hot Area:",
			QuestionLink:      "https://www.examtopics.com/discussions/microsoft/view/33477-exam-ai-900-topic-1-question-3-discussion/",
			ExhibitURLs:       []string{"https://www.examtopics.com/assets/media/exam-media/04234/0000300001.png"},
			AnswerExhibitURLs: []string{"https://www.examtopics.com/assets/media/exam-media/04234/0000400001.png"},
			Questions:         nil,
			Answer:            "",
		},
	}
	html := buildQuestionCards(data, false)
	if html == "" {
		t.Fatalf("expected card to be rendered")
	}
	if !strings.Contains(html, "answer-block") {
		t.Fatalf("expected answer-block class in rendered HTML, got:\n%s", html)
	}
	if !strings.Contains(html, "0000400001.png") {
		t.Fatalf("expected answer image URL in rendered HTML, got:\n%s", html)
	}
	if strings.Contains(html, "opt-empty") {
		t.Fatalf("opt-empty placeholder must NOT appear when an answer image exists, got:\n%s", html)
	}
	if !strings.Contains(html, "0000300001.png") {
		t.Fatalf("expected question-side exhibit URL still rendered, got:\n%s", html)
	}
}

func TestBuildQuestionCardsKeepsPlaceholderWhenNoAnswerImages(t *testing.T) {
	data := []models.QuestionData{
		{
			Title:        "Image-less drag-drop with no detectable answer image",
			Content:      "Drag the items to match.",
			QuestionLink: "https://www.examtopics.com/discussions/microsoft/view/xxx",
			ExhibitURLs:  []string{"https://img.examtopics.com/x/q.png"},
			Questions:    nil,
		},
	}
	html := buildQuestionCards(data, false)
	if !strings.Contains(html, "opt-empty") {
		t.Fatalf("expected opt-empty placeholder when no answer images and no options, got:\n%s", html)
	}
	if strings.Contains(html, "answer-block") {
		t.Fatalf("answer-block must NOT appear when answer-side images list is empty")
	}
}

func TestExtractQuestionExhibitURLsSubtractsAnswerSide(t *testing.T) {
	data := models.QuestionData{
		// The same URL appears in both Content (which is used by the
		// URL-from-text fallback) and AnswerExhibitURLs. The question-side
		// extractor must not re-add it.
		Content: "See https://img.examtopics.com/x/a.png for the answer.",
		ExhibitURLs: []string{
			"https://img.examtopics.com/x/q.png",
		},
		AnswerExhibitURLs: []string{
			"https://img.examtopics.com/x/a.png",
		},
	}
	answer := extractAnswerExhibitURLs(data)
	if len(answer) != 1 || answer[0] != "https://img.examtopics.com/x/a.png" {
		t.Fatalf("answer extraction wrong: %#v", answer)
	}
	question := extractQuestionExhibitURLs(data, answer)
	for _, u := range question {
		if u == "https://img.examtopics.com/x/a.png" {
			t.Fatalf("answer URL leaked into question-side list: %#v", question)
		}
	}
	if len(question) != 1 || question[0] != "https://img.examtopics.com/x/q.png" {
		t.Fatalf("expected exactly the question-side URL, got %#v", question)
	}
}

func TestExtractExhibitURLsLegacyCompatReturnsConcatenation(t *testing.T) {
	data := models.QuestionData{
		ExhibitURLs:       []string{"https://img.examtopics.com/x/q.png"},
		AnswerExhibitURLs: []string{"https://img.examtopics.com/x/a.png"},
	}
	all := extractExhibitURLs(data)
	if len(all) != 2 || all[0] != "https://img.examtopics.com/x/q.png" || all[1] != "https://img.examtopics.com/x/a.png" {
		t.Fatalf("legacy concat order wrong: %#v", all)
	}
}
