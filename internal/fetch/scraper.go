package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"examtopics-downloader/internal/utils"

	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
)

func getDataFromLink(link string, solutions map[string]*models.AnswerSolution) *models.QuestionData {
	data, _, _, err := getDataFromLinkContext(context.Background(), link, solutions)
	if err != nil {
		debugf("failed parsing HTML data from link: %v", err)
		return nil
	}
	return data
}

func getDataFromLinkContext(ctx context.Context, link string, solutions map[string]*models.AnswerSolution) (*models.QuestionData, bool, int, error) {
	doc, fromCache, metrics, err := ParseHTMLCachedContext(ctx, link, *client, QuestionRequestPolicy)
	if err != nil {
		return nil, false, metrics.Retries, err
	}

	allQuestions := extractAnswerOptions(doc)

	answer := strings.TrimSpace(doc.Find(".correct-answer").Text())
	if answer == "" {
		// The "Show Suggested Answer" reveal in examview.js works by toggling
		// the .correct-hidden class on the correct <li>. The authoritative
		// letter lives in the data-choice-letter attribute, typically on the
		// <span class="multi-choice-letter"> child (sometimes mirrored on the
		// <li> itself). Read both, deduped.
		seenLetters := map[string]struct{}{}
		var letters []string
		add := func(raw string) {
			raw = strings.TrimSpace(strings.ToUpper(raw))
			if raw == "" {
				return
			}
			if _, ok := seenLetters[raw]; ok {
				return
			}
			seenLetters[raw] = struct{}{}
			letters = append(letters, raw)
		}
		doc.Find("li.multi-choice-item.correct-hidden").Each(func(i int, s *goquery.Selection) {
			if l, ok := s.Attr("data-choice-letter"); ok {
				add(l)
			}
			s.Find("[data-choice-letter]").Each(func(j int, c *goquery.Selection) {
				if l, ok := c.Attr("data-choice-letter"); ok {
					add(l)
				}
			})
		})
		if len(letters) > 0 {
			// Avoid the literal word "Answer" in the prefix so a naive
			// \b([A-F])\b extraction downstream can't match the "A".
			answer = "Correct: " + strings.Join(letters, "")
		}
	}

	// Last-resort fallback: the community "Most Voted" answer. Every
	// discussion page embeds the vote tally as JSON in a hidden
	// .voted-answers-tally <script>; this is the same data behind the green
	// "Most Voted" badges. It's community-sourced rather than the official
	// suggested answer, so we only use it when neither .correct-answer nor the
	// correct-hidden markup yielded anything — otherwise the page would have
	// no answer at all and the quiz would default to a bogus first option.
	if answer == "" {
		if voted := extractMostVotedAnswer(doc); voted != "" {
			answer = "Correct: " + voted
		}
	}

	questionExhibits, answerExhibits := extractQuestionAndAnswerExhibits(doc)

	// The .question-body wrapper carries a stable numeric data-id that we
	// can use to look up the canonical "Reveal Solution" payload scraped
	// from /exams/{provider}/{slug}/view/ at the start of the run.
	questionID := strings.TrimSpace(doc.Find(".question-body").First().AttrOr("data-id", ""))

	var solution *models.AnswerSolution
	if questionID != "" && solutions != nil {
		if s, ok := solutions[questionID]; ok {
			solution = s
		}
	}

	return &models.QuestionData{
		Title:             utils.CleanText(doc.Find("h1").Text()),
		Header:            strings.ReplaceAll(strings.TrimSpace(doc.Find(".question-discussion-header").Text()), "\t", ""),
		Content:           utils.CleanText(doc.Find(".card-text").Text()),
		ExhibitURLs:       questionExhibits,
		AnswerExhibitURLs: answerExhibits,
		Questions:         allQuestions,
		Answer:            answer,
		Timestamp:         utils.CleanText(doc.Find(".discussion-meta-data > i").Text()),
		QuestionLink:      link,
		QuestionID:        questionID,
		Comments:          extractDiscussionComments(doc),
		Solution:          solution,
	}, fromCache, metrics.Retries, nil
}

// answerOptionSelectors lists CSS selectors to try (in order) when scraping
// answer-choice elements. ExamTopics primarily uses li.multi-choice-item, but
// the fallback selectors guard against markup drift across exams.
var answerOptionSelectors = []string{
	"li.multi-choice-item",
	".question-choices-container li",
	".question-body .multi-choice-item",
	"ul.answers li",
}

// extractAnswerOptions returns one string per answer choice on the page.
// For image-only choices, it embeds an [[IMG:<url>]] marker so the option
// survives downstream parsing and can be rendered as an image in the output.
func extractAnswerOptions(doc *goquery.Document) []string {
	for _, selector := range answerOptionSelectors {
		var opts []string
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			opts = append(opts, extractOptionLine(s))
		})
		nonEmpty := 0
		for _, o := range opts {
			if strings.TrimSpace(o) != "" {
				nonEmpty++
			}
		}
		if nonEmpty > 0 {
			return opts
		}
	}
	return nil
}

// extractOptionLine builds a single option string from one option <li>/<div>.
// Text content is kept; <img> children are captured as [[IMG:<absolute-url>]]
// markers appended to the text (or used as the sole content if no text is
// present), so image-only options aren't lost as empty strings.
func extractOptionLine(s *goquery.Selection) string {
	text := utils.CleanText(s.Text())

	var imageMarkers []string
	seen := map[string]struct{}{}
	addImage := func(raw string) {
		normalized := normalizeExhibitURL(raw)
		if normalized == "" {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		imageMarkers = append(imageMarkers, "[[IMG:"+normalized+"]]")
	}

	s.Find("img").Each(func(i int, img *goquery.Selection) {
		if src, ok := img.Attr("src"); ok {
			addImage(src)
		}
		if src, ok := img.Attr("data-src"); ok {
			addImage(src)
		}
		if src, ok := img.Attr("data-original"); ok {
			addImage(src)
		}
		if src, ok := img.Attr("data-lazy-src"); ok {
			addImage(src)
		}
		if srcSet, ok := img.Attr("srcset"); ok {
			addImage(firstURLFromSrcset(srcSet))
		}
	})

	if len(imageMarkers) == 0 {
		return text
	}
	if text == "" {
		return strings.Join(imageMarkers, " ")
	}
	return text + " " + strings.Join(imageMarkers, " ")
}

// answerSectionMarkers are case-insensitive substrings that, when found inside
// the question's .card-text, indicate the start of the answer/interaction area
// (HOTSPOT or DRAG-DROP). Images that appear after the first occurrence are
// treated as answer-side exhibits rather than question-side exhibits.
var answerSectionMarkers = []string{
	"hot area:",
	"answer area:",
	"correct answer:",
}

// imageURLsFromImg returns every URL referenced by an <img> element across the
// known attributes (src, data-src, data-original, data-lazy-src, srcset).
// URLs are returned raw (not normalised); the caller is responsible for
// normalising and deduping.
func imageURLsFromImg(img *goquery.Selection) []string {
	var out []string
	if src, ok := img.Attr("src"); ok && strings.TrimSpace(src) != "" {
		out = append(out, src)
	}
	if src, ok := img.Attr("data-src"); ok && strings.TrimSpace(src) != "" {
		out = append(out, src)
	}
	if src, ok := img.Attr("data-original"); ok && strings.TrimSpace(src) != "" {
		out = append(out, src)
	}
	if src, ok := img.Attr("data-lazy-src"); ok && strings.TrimSpace(src) != "" {
		out = append(out, src)
	}
	if srcSet, ok := img.Attr("srcset"); ok {
		if first := firstURLFromSrcset(srcSet); first != "" {
			out = append(out, first)
		}
	}
	return out
}

// extractQuestionAndAnswerExhibits walks .card-text in document order and
// partitions image URLs into question-side and answer-side buckets. The split
// flips the first time a text node inside .card-text contains one of the
// known answer-section markers (e.g. "Hot Area:"). When no marker is present,
// every image stays in the question bucket — preserving the previous
// behaviour for ordinary multi-choice questions.
func extractQuestionAndAnswerExhibits(doc *goquery.Document) (questionURLs []string, answerURLs []string) {
	seen := map[string]struct{}{}
	addInto := func(bucket *[]string, raw string) {
		normalized := normalizeExhibitURL(raw)
		if normalized == "" {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		*bucket = append(*bucket, normalized)
	}

	// Scope to the question's card-text only (avoid the login modal's
	// unrelated .card-text divs that exist later in the document).
	card := doc.Find(".question-body .card-text").First()
	if card.Length() == 0 {
		card = doc.Find(".card-text").First()
	}
	if card.Length() == 0 {
		return nil, nil
	}

	inAnswer := false
	var walk func(*goquery.Selection)
	walk = func(s *goquery.Selection) {
		s.Contents().Each(func(_ int, child *goquery.Selection) {
			node := child.Nodes[0]
			switch node.Type {
			case xhtml.TextNode:
				if !inAnswer {
					lower := strings.ToLower(node.Data)
					for _, m := range answerSectionMarkers {
						if strings.Contains(lower, m) {
							inAnswer = true
							break
						}
					}
				}
			case xhtml.ElementNode:
				if strings.EqualFold(node.Data, "img") {
					bucket := &questionURLs
					if inAnswer {
						bucket = &answerURLs
					}
					for _, raw := range imageURLsFromImg(child) {
						addInto(bucket, raw)
					}
					return
				}
				walk(child)
			}
		})
	}
	walk(card)
	return questionURLs, answerURLs
}

// extractExhibitImageURLs preserves the original flat-list contract for any
// callers that don't care about the question/answer partition. It returns the
// concatenation (question-side first, then answer-side).
func extractExhibitImageURLs(doc *goquery.Document) []string {
	q, a := extractQuestionAndAnswerExhibits(doc)
	if len(a) == 0 {
		return q
	}
	out := make([]string, 0, len(q)+len(a))
	out = append(out, q...)
	out = append(out, a...)
	return out
}

func normalizeExhibitURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" || strings.HasPrefix(raw, "data:") {
		return ""
	}

	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	} else if strings.HasPrefix(raw, "/") {
		raw = "https://www.examtopics.com" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}

	return u.String()
}

func firstURLFromSrcset(srcset string) string {
	items := strings.Split(srcset, ",")
	if len(items) == 0 {
		return ""
	}

	first := strings.TrimSpace(items[0])
	if first == "" {
		return ""
	}
	parts := strings.Fields(first)
	if len(parts) == 0 {
		return ""
	}

	return parts[0]
}

// votedAnswerEntry mirrors the JSON ExamTopics embeds in the hidden
// .voted-answers-tally <script> on every discussion page, e.g.
//
//	[{"voted_answers": "DE", "vote_count": 3, "is_most_voted": true}]
type votedAnswerEntry struct {
	VotedAnswers string `json:"voted_answers"`
	VoteCount    int    `json:"vote_count"`
	IsMostVoted  bool   `json:"is_most_voted"`
}

var votedAnswerLetterPattern = regexp.MustCompile(`(?i)[A-F]`)

// extractMostVotedAnswer returns the community most-voted answer letters (e.g.
// "AC") for the question, derived from the .voted-answers-tally JSON. It prefers
// the entry flagged is_most_voted; failing that, the entry with the highest
// vote_count. Returns "" when no usable letter answer is present (image-only
// votes, empty tally, or malformed JSON).
func extractMostVotedAnswer(doc *goquery.Document) string {
	// Scope to the question body so we never pick up an unrelated tally.
	sel := doc.Find(".question-body .voted-answers-tally script")
	if sel.Length() == 0 {
		sel = doc.Find(".voted-answers-tally script")
	}

	best := ""
	bestVotes := -1
	bestMostVoted := false

	sel.EachWithBreak(func(_ int, s *goquery.Selection) bool {
		raw := strings.TrimSpace(s.Text())
		if raw == "" {
			return true
		}
		var entries []votedAnswerEntry
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			debugf("voted-answers-tally: unmarshal failed: %v", err)
			return true
		}
		for _, e := range entries {
			letters := strings.ToUpper(strings.Join(votedAnswerLetterPattern.FindAllString(e.VotedAnswers, -1), ""))
			if letters == "" {
				continue
			}
			// An explicit is_most_voted flag wins outright.
			if e.IsMostVoted {
				best = letters
				bestMostVoted = true
				return false
			}
			if !bestMostVoted && e.VoteCount > bestVotes {
				best = letters
				bestVotes = e.VoteCount
			}
		}
		return true
	})

	return best
}

var commentAnswerLetterPattern = regexp.MustCompile(`\b([A-F])\b`)

// commentDateLayout matches the absolute timestamp ExamTopics stores in the
// title attribute of .comment-date, e.g. "Tue 27 Jun 2023 10:27". The visible
// text is a relative string ("2 years ago") and is intentionally not parsed.
const commentDateLayout = "Mon 02 Jan 2006 15:04"

// parseCommentDate reads the .comment-date title attribute from a comment
// element and parses it. Returns the zero time.Time when the attribute is
// absent, blank, or unparseable.
func parseCommentDate(s *goquery.Selection) time.Time {
	title, ok := s.Find(".comment-date").First().Attr("title")
	if !ok {
		return time.Time{}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return time.Time{}
	}
	t, err := time.Parse(commentDateLayout, title)
	if err != nil {
		debugf("comment-date: unparseable title %q: %v", title, err)
		return time.Time{}
	}
	return t
}

func extractDiscussionComments(doc *goquery.Document) []models.CommentData {
	var comments []models.CommentData

	doc.Find(".discussion-container .comment-container").Each(func(i int, s *goquery.Selection) {
		user := strings.TrimSpace(s.Find(".comment-username").First().Text())
		if user == "" {
			user = "Anonymous"
		}

		answer := ""
		answerText := strings.TrimSpace(s.Find(".comment-selected-answers strong").First().Text())
		if answerText == "" {
			answerText = strings.TrimSpace(s.Find(".comment-selected-answers").First().Text())
		}
		if m := commentAnswerLetterPattern.FindStringSubmatch(strings.ToUpper(answerText)); len(m) == 2 {
			answer = m[1]
		}

		content := normalizeCommentText(s.Find(".comment-content").First().Text())
		if content == "" {
			return
		}

		comments = append(comments, models.CommentData{
			User:   user,
			Answer: answer,
			Text:   content,
			Date:   parseCommentDate(s),
		})
	})

	return comments
}

func normalizeCommentText(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	lines := strings.Split(raw, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, trimmed)
	}

	return strings.Join(cleaned, "\n")
}

func listingPageURL(providerName string, page int) string {
	return fmt.Sprintf("%s/discussions/%s/%d/", examTopicsBaseURL, providerName, page)
}

func questionPageURL(link string) string {
	return strings.TrimRight(examTopicsBaseURL, "/") + "/" + strings.TrimLeft(link, "/")
}

type questionPageResult struct {
	index     int
	data      *models.QuestionData
	fromCache bool
	retries   int
	err       error
}

// GetAllPages reuses the provider discussion index, filters it locally, and
// processes question pages through a fixed worker pool. Result order follows
// topic/question order rather than network completion order.
func GetAllPages(ctx context.Context, providerName string, selectedExam string, report ProgressFunc) ([]models.QuestionData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	startTime := utils.StartTime()
	index, indexErr := GetProviderDiscussionIndex(ctx, providerName, IndexOptions{}, report)
	if indexErr != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Question discovery is partial: scanned %d of %d provider discussion pages.\n", len(index.CompletedPages), index.TotalPages)
		fmt.Fprintf(os.Stderr, "[WARN] Run the downloader again (or use /scan again) to resume the missing pages. Completed pages are cached.\n")
	}

	allLinks := make([]string, 0, len(index.Links))
	for _, link := range index.Links {
		if matchesExamSelection(providerName, selectedExam, link) {
			allLinks = append(allLinks, link)
		}
	}
	unique := utils.DeduplicateLinks(allLinks)
	sortedLinks := utils.SortLinksByQuestionNumber(unique)
	if strings.TrimSpace(selectedExam) == "" {
		fmt.Printf("[INFO] Found %d indexed public discussion page(s) for %s.\n", len(sortedLinks), providerName)
	} else {
		fmt.Printf("[INFO] Found %d public discussion-backed question page(s) matching %s.\n", len(sortedLinks), selectedExam)
	}
	if strings.TrimSpace(selectedExam) != "" {
		if advertised, err := GetOfficialExamQuestionCount(ctx, providerName, selectedExam); err == nil {
			fmt.Printf("[INFO] ExamTopics currently advertises %d total question(s) for %s.\n", advertised, strings.ToUpper(selectedExam))
			if advertised > len(sortedLinks) {
				if index.Complete {
					fmt.Fprintf(os.Stderr, "[WARN] %d advertised question(s) have no public discussion page in the complete provider index or require site access.\n", advertised-len(sortedLinks))
				} else {
					fmt.Fprintf(os.Stderr, "[WARN] The current HTML cannot be considered complete until all %d provider discussion pages have been scanned.\n", index.TotalPages)
				}
			}
		}
	}
	if summary := buildSelectedExamVariantSummary(providerName, selectedExam, sortedLinks); summary != "" {
		fmt.Printf("\n%s\n", summary)
	}
	if len(sortedLinks) == 0 {
		if indexErr != nil {
			return nil, fmt.Errorf("no matching questions found in partial provider index: %w", indexErr)
		}
		return nil, fmt.Errorf("no matching questions were found")
	}

	solutions, solutionErr := FetchViewSolutionsContext(ctx, providerName, selectedExam)
	if solutionErr != nil {
		debugf("view-solutions: %v", solutionErr)
	}
	if len(solutions) > 0 {
		fmt.Fprintf(os.Stderr, "[INFO] The anonymous ExamTopics viewer exposed site-provided answers for %d question(s).\n", len(solutions))
	}

	jobs := make(chan int)
	pageResults := make(chan questionPageResult, constants.MaxConcurrentRequests)
	results := make([]*models.QuestionData, len(sortedLinks))
	var workers sync.WaitGroup
	for worker := 0; worker < constants.MaxConcurrentRequests; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				url := questionPageURL(sortedLinks[i])
				data, fromCache, retries, err := getDataFromLinkContext(ctx, url, solutions)
				select {
				case pageResults <- questionPageResult{index: i, data: data, fromCache: fromCache, retries: retries, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range sortedLinks {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(pageResults)
	}()

	completed := 0
	found := 0
	retries := 0
	failures := 0
	cacheHits := 0
	for result := range pageResults {
		completed++
		retries += result.retries
		if result.fromCache {
			cacheHits++
		}
		if result.err != nil || result.data == nil {
			failures++
		} else {
			results[result.index] = result.data
			found++
		}
		reportProgress(report, Progress{
			Phase: "questions", Completed: completed, Total: len(sortedLinks), Found: found,
			Retries: retries, Failures: failures, CacheHits: cacheHits,
			Complete: completed == len(sortedLinks), Rate: requestLimiter.RPS(), Elapsed: time.Since(startTime),
		})
	}

	finalData := make([]models.QuestionData, 0, found)
	failedLinks := make([]string, 0, failures)
	for i, entry := range results {
		if entry != nil {
			finalData = append(finalData, *entry)
		} else {
			failedLinks = append(failedLinks, sortedLinks[i])
		}
	}

	if len(failedLinks) > 0 {
		fmt.Fprintf(os.Stderr, "[WARN] Extracted %d of %d question pages (%d failed).\n", len(finalData), len(sortedLinks), len(failedLinks))
		for i, link := range failedLinks {
			if i == 10 {
				fmt.Fprintf(os.Stderr, "  ... and %d more.\n", len(failedLinks)-i)
				break
			}
			fmt.Fprintf(os.Stderr, "  - %s\n", link)
		}
	} else {
		fmt.Printf("Extracted %d of %d question pages.\n", len(finalData), len(sortedLinks))
	}
	fmt.Printf("Extraction complete in %s (%d cache hits, %d retries).\n", utils.TimeSince(startTime), cacheHits, retries)

	if err := ctx.Err(); err != nil {
		return finalData, err
	}
	if len(finalData) == 0 {
		return nil, fmt.Errorf("all %d question pages failed", len(sortedLinks))
	}
	if indexErr != nil {
		return finalData, fmt.Errorf("question discovery is incomplete (%d of %d provider pages scanned): %w", len(index.CompletedPages), index.TotalPages, indexErr)
	}
	if len(failedLinks) > 0 {
		return finalData, fmt.Errorf("question extraction is incomplete: %d of %d discovered pages failed", len(failedLinks), len(sortedLinks))
	}
	return finalData, nil
}

func buildSelectedExamVariantSummary(providerName, selectedExam string, links []string) string {
	selectedExam = strings.TrimSpace(strings.ToLower(selectedExam))
	if selectedExam == "" {
		return ""
	}

	selectedNormalized := normalizeExamSlug(providerName, selectedExam)
	if selectedNormalized == "" {
		return ""
	}

	variantCounts := map[string]int{}
	for _, link := range links {
		raw := extractExamSlugFromDiscussionURL(link)
		if raw == "" {
			continue
		}
		if normalizeExamSlug(providerName, raw) != selectedNormalized {
			continue
		}
		variantCounts[raw]++
	}

	if len(variantCounts) == 0 {
		return ""
	}

	var variants []string
	for slug := range variantCounts {
		variants = append(variants, slug)
	}
	sort.Strings(variants)

	if len(variants) == 1 && variants[0] == selectedNormalized {
		return ""
	}

	summary := make([]string, 0, len(variants))
	for _, slug := range variants {
		summary = append(summary, fmt.Sprintf("%s (%d)", slug, variantCounts[slug]))
	}

	return fmt.Sprintf("Including grouped variants for %s: %s", selectedNormalized, strings.Join(summary, ", "))
}
