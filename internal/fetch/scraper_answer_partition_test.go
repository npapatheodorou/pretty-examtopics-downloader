package fetch

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

const hotspotFixture = `
<div class="question-body">
  <p class="card-text">
    HOTSPOT -<br>You are developing a model.<br>
    <img src="/assets/media/exam-media/04234/0000300001.png" class="in-exam-image" /><br>
    Use the drop-down menus to select the answer.<br>Hot Area:<br>
    <img src="/assets/media/exam-media/04234/0000400001.png" class="in-exam-image" /><br>
  </p>
</div>`

func TestExtractQuestionAndAnswerExhibitsHotspot(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(hotspotFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q, a := extractQuestionAndAnswerExhibits(doc)
	if len(q) != 1 || !strings.HasSuffix(q[0], "/0000300001.png") {
		t.Fatalf("expected one question-side image ending in 0000300001.png, got %#v", q)
	}
	if len(a) != 1 || !strings.HasSuffix(a[0], "/0000400001.png") {
		t.Fatalf("expected one answer-side image ending in 0000400001.png, got %#v", a)
	}
}

func TestExtractQuestionAndAnswerExhibitsDragDrop(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<div class="question-body">
  <p class="card-text">
    DRAG DROP -<br>You need to arrange the steps.<br>
    <img src="https://img.examtopics.com/aa/q1.png" /><br>
    Answer Area:<br>
    <img src="https://img.examtopics.com/aa/a1.png" /><br>
  </p>
</div>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q, a := extractQuestionAndAnswerExhibits(doc)
	if len(q) != 1 || q[0] != "https://img.examtopics.com/aa/q1.png" {
		t.Fatalf("question bucket wrong: %#v", q)
	}
	if len(a) != 1 || a[0] != "https://img.examtopics.com/aa/a1.png" {
		t.Fatalf("answer bucket wrong: %#v", a)
	}
}

func TestExtractQuestionAndAnswerExhibitsNoMarkerKeepsBackCompat(t *testing.T) {
	// Plain question with multi-choice options should leave all images on
	// the question side (back-compat: previous flat ExhibitURLs).
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<div class="question-body">
  <p class="card-text">
    Which of the following is true?<br>
    <img src="https://img.examtopics.com/plain/diagram.png" /><br>
  </p>
</div>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q, a := extractQuestionAndAnswerExhibits(doc)
	if len(q) != 1 || q[0] != "https://img.examtopics.com/plain/diagram.png" {
		t.Fatalf("question bucket wrong: %#v", q)
	}
	if len(a) != 0 {
		t.Fatalf("answer bucket should be empty for no-marker case, got %#v", a)
	}
}

func TestExtractQuestionAndAnswerExhibitsIgnoresLoginModalCardText(t *testing.T) {
	// The login modal also uses .card-text divs but contains no images. We
	// scope to .question-body .card-text first; this fixture verifies that.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<div class="question-body">
  <p class="card-text">
    HOTSPOT -<br>
    <img src="https://img.examtopics.com/scoped/q.png" /><br>
    Hot Area:<br>
    <img src="https://img.examtopics.com/scoped/a.png" /><br>
  </p>
</div>
<div class="card-text mb-30">
  <input type="text" name="email">
</div>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q, a := extractQuestionAndAnswerExhibits(doc)
	if len(q) != 1 || !strings.HasSuffix(q[0], "/q.png") {
		t.Fatalf("question bucket wrong: %#v", q)
	}
	if len(a) != 1 || !strings.HasSuffix(a[0], "/a.png") {
		t.Fatalf("answer bucket wrong: %#v", a)
	}
}

func TestExtractExhibitImageURLsBackCompatConcatenation(t *testing.T) {
	// The legacy helper returns a single flat list (question first, then answer).
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(hotspotFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	all := extractExhibitImageURLs(doc)
	if len(all) != 2 {
		t.Fatalf("expected 2 total exhibits, got %d: %#v", len(all), all)
	}
	if !strings.HasSuffix(all[0], "/0000300001.png") || !strings.HasSuffix(all[1], "/0000400001.png") {
		t.Fatalf("flat list order wrong: %#v", all)
	}
}

func TestCorrectHiddenLetterCapturedWhenAnswerTextEmpty(t *testing.T) {
	// Simulates a standard multi-choice page where .correct-answer text is
	// empty but the correct option is marked with class="correct-hidden".
	const html = `
<html><body>
<div class="question-body">
  <p class="card-text">Which option is correct?</p>
  <div class="question-choices-container">
    <ul>
      <li class="multi-choice-item"><span class="multi-choice-letter" data-choice-letter="A">A.</span> first</li>
      <li class="multi-choice-item correct-hidden" data-choice-letter="B"><span class="multi-choice-letter" data-choice-letter="B">B.</span> second</li>
    </ul>
  </div>
</div>
</body></html>`
	// We can't call getDataFromLink directly without HTTP, but we exercise
	// the same logic by parsing inline and replicating the small block.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	answerText := strings.TrimSpace(doc.Find(".correct-answer").Text())
	if answerText != "" {
		t.Fatalf("setup error: .correct-answer should be empty in fixture, got %q", answerText)
	}
	var letters []string
	doc.Find("li.multi-choice-item.correct-hidden").Each(func(i int, s *goquery.Selection) {
		if l, ok := s.Attr("data-choice-letter"); ok {
			letters = append(letters, strings.ToUpper(strings.TrimSpace(l)))
		}
	})
	if len(letters) != 1 || letters[0] != "B" {
		t.Fatalf("expected to recover letter B from correct-hidden, got %#v", letters)
	}
}
