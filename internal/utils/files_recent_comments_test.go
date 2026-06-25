package utils

import (
	"testing"
	"time"

	"examtopics-downloader/internal/models"
)

var filterNow = time.Date(2026, time.June, 25, 12, 0, 0, 0, time.UTC)

func qWithCommentDates(dates ...time.Time) models.QuestionData {
	q := models.QuestionData{}
	for _, d := range dates {
		q.Comments = append(q.Comments, models.CommentData{Date: d})
	}
	return q
}

func TestLatestCommentTime(t *testing.T) {
	d1 := filterNow.AddDate(0, 0, -100)
	d2 := filterNow.AddDate(0, 0, -10)
	d3 := filterNow.AddDate(0, 0, -50)

	if got := LatestCommentTime(qWithCommentDates(d1, d2, d3)); !got.Equal(d2) {
		t.Fatalf("want latest %v, got %v", d2, got)
	}
	if got := LatestCommentTime(qWithCommentDates()); !got.IsZero() {
		t.Fatalf("empty comments should yield zero, got %v", got)
	}
	// All-zero comment dates -> zero.
	if got := LatestCommentTime(qWithCommentDates(time.Time{}, time.Time{})); !got.IsZero() {
		t.Fatalf("all-zero dates should yield zero, got %v", got)
	}
	// Mixed zero and real dates -> the real one.
	if got := LatestCommentTime(qWithCommentDates(time.Time{}, d3)); !got.Equal(d3) {
		t.Fatalf("want %v from mixed, got %v", d3, got)
	}
}

func TestFilterByRecentCommentsDisabled(t *testing.T) {
	in := []models.QuestionData{
		qWithCommentDates(filterNow.AddDate(0, 0, -1000)),
		qWithCommentDates(), // no comments
	}
	out := FilterByRecentComments(in, 0, filterNow)
	if len(out) != len(in) {
		t.Fatalf("days=0 must return input unchanged, got %d of %d", len(out), len(in))
	}
}

func TestFilterByRecentCommentsRecentKept(t *testing.T) {
	in := []models.QuestionData{qWithCommentDates(filterNow.AddDate(0, 0, -10))}
	if got := FilterByRecentComments(in, 30, filterNow); len(got) != 1 {
		t.Fatalf("recent question should be kept, got %d", len(got))
	}
}

func TestFilterByRecentCommentsOldDropped(t *testing.T) {
	in := []models.QuestionData{qWithCommentDates(filterNow.AddDate(0, 0, -200))}
	if got := FilterByRecentComments(in, 180, filterNow); len(got) != 0 {
		t.Fatalf("old question should be dropped, got %d", len(got))
	}
}

func TestFilterByRecentCommentsBoundaryKept(t *testing.T) {
	// latest exactly at the cutoff (now - days) must be kept (only strictly
	// older is dropped).
	in := []models.QuestionData{qWithCommentDates(filterNow.AddDate(0, 0, -30))}
	if got := FilterByRecentComments(in, 30, filterNow); len(got) != 1 {
		t.Fatalf("boundary (== cutoff) should be kept, got %d", len(got))
	}
}

func TestFilterByRecentCommentsNoDateKept(t *testing.T) {
	// No comments at all, and comments with only unparseable (zero) dates, are
	// both kept: we never drop a question merely for lacking dated discussion.
	in := []models.QuestionData{
		qWithCommentDates(),                         // no comments
		qWithCommentDates(time.Time{}, time.Time{}), // all-zero dates
	}
	if got := FilterByRecentComments(in, 30, filterNow); len(got) != 2 {
		t.Fatalf("no-date questions should be kept, got %d", len(got))
	}
}

func TestFilterByRecentCommentsMixedPreservesOrder(t *testing.T) {
	recent := qWithCommentDates(filterNow.AddDate(0, 0, -5))
	old := qWithCommentDates(filterNow.AddDate(0, 0, -400))
	noDate := qWithCommentDates()
	recent2 := qWithCommentDates(filterNow.AddDate(0, 0, -20))

	in := []models.QuestionData{recent, old, noDate, recent2}
	out := FilterByRecentComments(in, 30, filterNow)
	if len(out) != 3 {
		t.Fatalf("expected 3 kept (recent, noDate, recent2), got %d", len(out))
	}
	// Order preserved: recent, noDate, recent2.
	if !LatestCommentTime(out[0]).Equal(LatestCommentTime(recent)) ||
		!LatestCommentTime(out[2]).Equal(LatestCommentTime(recent2)) {
		t.Fatalf("original order not preserved: %+v", out)
	}
	if !LatestCommentTime(out[1]).IsZero() {
		t.Fatalf("expected the no-date question in the middle, got %v", LatestCommentTime(out[1]))
	}
}
