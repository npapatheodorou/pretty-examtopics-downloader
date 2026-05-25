package models

type CommentData struct {
	User   string
	Answer string
	Text   string
}

// AnswerSolution holds the canonical "Reveal Solution" answer payload scraped
// from /exams/{provider}/{slug}/view/. When non-nil, it overrides any
// best-effort answer derived from the question's own discussion page.
type AnswerSolution struct {
	// Letter is the textual correct-answer content (e.g. "B" or "BD"). Empty
	// when the correct-answer is image-only (see Images).
	Letter string
	// Images are URLs found inside .correct-answer (typically one for HOTSPOT).
	Images []string
	// Description is the .answer-description text content. Image URLs that
	// were inline in the description are preserved as [[IMG:<url>]] markers
	// in document order so the renderer can splice them back in.
	Description string
	// DescImages is the deduplicated list of image URLs from .answer-description,
	// in document order. Always a subset of the markers embedded in Description.
	DescImages []string
	// References are http(s) URLs cited inside .answer-description (e.g. MS Learn).
	References []string
}

type QuestionData struct {
	Title             string
	Header            string
	Content           string
	ExhibitURLs       []string
	AnswerExhibitURLs []string
	Questions         []string
	Answer            string
	Timestamp         string
	QuestionLink      string
	QuestionID        string
	Comments          []CommentData
	Solution          *AnswerSolution
}
