package fetch

import (
	"strings"
	"testing"

	"examtopics-downloader/internal/models"

	"github.com/PuerkitoBio/goquery"
)

func TestParseViewSolutionsHotspotQ3(t *testing.T) {
	// Mirrors the real /exams/microsoft/ai-900/view/ payload for question 3:
	// .correct-answer is an <img>, .answer-description carries text + an
	// explanatory image + a reference URL.
	const html = `
<div class="card-body question-body" data-id="806658">
  <p class="card-text">HOTSPOT - You are developing...</p>
  <a href="#" class="btn btn-primary reveal-solution">Reveal Solution</a>
  <p class="card-text question-answer bg-light white-text">
    <span class="correct-answer-box"><strong>Correct Answer:</strong>
      <span class="correct-answer">
        <img src="/assets/media/exam-media/04234/0000500001.png" class="in-exam-image" />
      </span>
      <br/>
    </span>
    <span class="answer-description">
      Box 1: 11 -<br>
      <img src="/assets/media/exam-media/04234/0000600001.png" class="in-exam-image" /><br>
      TP = True Positive.<br>
      Box 2: 1,033 -<br>
      FN = False Negative -<br>
      Reference:<br>
      https://docs.microsoft.com/en-us/azure/machine-learning/studio/evaluate-model-performance
    </span>
  </p>
</div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	dst := map[string]*models.AnswerSolution{}
	parseViewSolutionsFromDoc(doc, dst)

	sol, ok := dst["806658"]
	if !ok {
		t.Fatalf("expected solution for data-id 806658, got %#v", dst)
	}
	if sol.Letter != "" {
		t.Fatalf("expected empty letter for image-only correct answer, got %q", sol.Letter)
	}
	if len(sol.Images) != 1 || !strings.HasSuffix(sol.Images[0], "/0000500001.png") {
		t.Fatalf("expected correct-answer image 0000500001.png, got %#v", sol.Images)
	}
	if !strings.Contains(sol.Description, "Box 1: 11") {
		t.Fatalf("description missing Box 1 text: %q", sol.Description)
	}
	if !strings.Contains(sol.Description, "TP = True Positive") {
		t.Fatalf("description missing TP explanation: %q", sol.Description)
	}
	if !strings.Contains(sol.Description, "[[IMG:") || !strings.Contains(sol.Description, "0000600001.png") {
		t.Fatalf("description must keep inline IMG marker for 0000600001.png: %q", sol.Description)
	}
	if len(sol.DescImages) != 1 || !strings.HasSuffix(sol.DescImages[0], "/0000600001.png") {
		t.Fatalf("expected one DescImage 0000600001.png, got %#v", sol.DescImages)
	}
	if len(sol.References) != 1 ||
		sol.References[0] != "https://docs.microsoft.com/en-us/azure/machine-learning/studio/evaluate-model-performance" {
		t.Fatalf("expected MS docs reference URL, got %#v", sol.References)
	}
}

func TestParseViewSolutionsStandardLetter(t *testing.T) {
	const html = `
<div class="card-body question-body" data-id="703690">
  <p class="card-text question-answer bg-light white-text">
    <span class="correct-answer-box"><strong>Correct Answer:</strong>
      <span class="correct-answer">B</span>
      <br/>
    </span>
    <span class="answer-description"></span>
  </p>
</div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dst := map[string]*models.AnswerSolution{}
	parseViewSolutionsFromDoc(doc, dst)
	sol, ok := dst["703690"]
	if !ok {
		t.Fatalf("missing solution: %#v", dst)
	}
	if sol.Letter != "B" {
		t.Fatalf("expected letter B, got %q", sol.Letter)
	}
	if len(sol.Images) != 0 || len(sol.DescImages) != 0 {
		t.Fatalf("expected no images, got %#v / %#v", sol.Images, sol.DescImages)
	}
}

func TestParseViewSolutionsSkipsEmptyAnswerBlocks(t *testing.T) {
	const html = `
<div class="card-body question-body" data-id="999">
  <p class="card-text question-answer">
    <span class="correct-answer-box"><strong>Correct Answer:</strong>
      <span class="correct-answer"></span>
    </span>
    <span class="answer-description"></span>
  </p>
</div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dst := map[string]*models.AnswerSolution{}
	parseViewSolutionsFromDoc(doc, dst)
	if _, ok := dst["999"]; ok {
		t.Fatalf("empty answer block should not register a solution")
	}
}

func TestParseViewSolutionsExtractsMultipleQuestions(t *testing.T) {
	const html = `
<div class="card-body question-body" data-id="1">
  <p class="card-text question-answer">
    <span class="correct-answer">A</span>
    <span class="answer-description">x</span>
  </p>
</div>
<div class="card-body question-body" data-id="2">
  <p class="card-text question-answer">
    <span class="correct-answer">CD</span>
    <span class="answer-description">y</span>
  </p>
</div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dst := map[string]*models.AnswerSolution{}
	parseViewSolutionsFromDoc(doc, dst)
	if len(dst) != 2 || dst["1"].Letter != "A" || dst["2"].Letter != "CD" {
		t.Fatalf("expected two solutions A and CD, got %#v", dst)
	}
}
