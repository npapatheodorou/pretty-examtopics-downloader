package fetch

import (
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func commentDoc(t *testing.T, dateMarkup string) *goquery.Document {
	t.Helper()
	html := `
<div class="discussion-container">
  <div class="media comment-container">
    <div class="media-body">
      <div class="comment-head">
        <h5 class="comment-username">alice</h5>
        ` + dateMarkup + `
      </div>
      <div class="comment-body">
        <div class="comment-content">a comment</div>
      </div>
    </div>
  </div>
</div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestExtractDiscussionCommentsParsesDate(t *testing.T) {
	doc := commentDoc(t, `<span class="comment-date align-middle" title="Tue 27 Jun 2023 10:27">2 years, 12 months ago</span>`)
	comments := extractDiscussionComments(doc)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	want := time.Date(2023, time.June, 27, 10, 27, 0, 0, time.UTC)
	if !comments[0].Date.Equal(want) {
		t.Fatalf("want date %v, got %v", want, comments[0].Date)
	}
}

func TestExtractDiscussionCommentsMissingDate(t *testing.T) {
	doc := commentDoc(t, ``) // no .comment-date element
	comments := extractDiscussionComments(doc)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if !comments[0].Date.IsZero() {
		t.Fatalf("expected zero date when .comment-date absent, got %v", comments[0].Date)
	}
}

func TestExtractDiscussionCommentsBadDate(t *testing.T) {
	doc := commentDoc(t, `<span class="comment-date" title="garbage not a date">whenever</span>`)
	comments := extractDiscussionComments(doc)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if !comments[0].Date.IsZero() {
		t.Fatalf("expected zero date for unparseable title, got %v", comments[0].Date)
	}
}
