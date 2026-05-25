package fetch

import (
	"fmt"
	"html"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"examtopics-downloader/internal/utils"

	"github.com/PuerkitoBio/goquery"
	"github.com/cheggaaa/pb/v3"
	xhtml "golang.org/x/net/html"
)

func getDataFromLink(link string, solutions map[string]*models.AnswerSolution) *models.QuestionData {
	doc, err := ParseHTML(link, *client)
	if err != nil {
		debugf("failed parsing HTML data from link: %v", err)
		return nil
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
	}
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

func extractDiscussionComments(doc *goquery.Document) []models.CommentData {
	var comments []models.CommentData
	answerLetterPattern := regexp.MustCompile(`\b([A-F])\b`)

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
		if m := answerLetterPattern.FindStringSubmatch(strings.ToUpper(answerText)); len(m) == 2 {
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

func fetchAllPageLinksConcurrently(providerName, selectedExam string, numPages, concurrency int, onPageProcessed func()) []string {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	results := make(chan []string, numPages)

	rateLimiter := utils.CreateRateLimiter(constants.RequestsPerSecond)
	defer rateLimiter.Stop()

	for i := 1; i <= numPages; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			<-rateLimiter.C

			url := fmt.Sprintf("https://www.examtopics.com/discussions/%s/%d", providerName, i)
			results <- getLinksFromPage(providerName, url, selectedExam)
			if onPageProcessed != nil {
				onPageProcessed()
			}
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// about 10 questions per examtopics page, we can preallocate
	all := make([]string, 0, numPages*10)
	for res := range results {
		all = append(all, res...)
	}

	return all
}

// Main concurrent page scraping logic
func GetAllPages(providerName string, selectedExam string) []models.QuestionData {
	baseURL := fmt.Sprintf("https://www.examtopics.com/discussions/%s/", providerName)
	numPages := getMaxNumPages(baseURL)
	startTime := utils.StartTime()
	bar := pb.StartNew(numPages)

	allLinks := fetchAllPageLinksConcurrently(providerName, selectedExam, numPages, constants.MaxConcurrentRequests, func() {
		bar.Increment()
	})

	unique := utils.DeduplicateLinks(allLinks)
	sortedLinks := utils.SortLinksByQuestionNumber(unique)
	if summary := buildSelectedExamVariantSummary(providerName, selectedExam, sortedLinks); summary != "" {
		fmt.Printf("\n%s\n", summary)
	}
	bar.SetTotal(int64(numPages + len(sortedLinks)))

	if len(sortedLinks) == 0 {
		bar.Finish()
		fmt.Println("No matching questions were found.")
		return nil
	}

	// Fetch the canonical "Reveal Solution" payload for whichever questions
	// ExamTopics exposes without authentication on /exams/{p}/{e}/view/.
	// This is best-effort: the typical free quota is the first ~5 questions
	// per exam. For everything else, we fall back to the discussion-page
	// data and surface the limitation in a one-line log.
	solutions := FetchViewSolutions(providerName, selectedExam)
	if len(solutions) > 0 {
		fmt.Fprintf(os.Stderr, "[INFO] Fetched canonical answers for %d question(s) from /exams/%s/%s/view/.\n", len(solutions), providerName, selectedExam)
	} else {
		debugf("view-solutions: no canonical answers retrieved for %s/%s", providerName, selectedExam)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, constants.MaxConcurrentRequests)
	results := make([]*models.QuestionData, len(sortedLinks))

	rateLimiter := utils.CreateRateLimiter(constants.RequestsPerSecond)
	defer rateLimiter.Stop()

	for i, link := range sortedLinks {
		wg.Add(1)
		url := utils.AddToBaseUrl(link)

		go func(i int, url string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			<-rateLimiter.C

			data := getDataFromLink(url, solutions)
			if data != nil {
				results[i] = data
			}
			bar.Increment()
		}(i, url)
	}

	wg.Wait()
	bar.Finish()

	// Sequentially retry any links that came back nil — most are transient
	// rate-limit or transport failures that recover under slower pacing.
	missingIdx := make([]int, 0)
	for i, entry := range results {
		if entry == nil {
			missingIdx = append(missingIdx, i)
		}
	}
	if len(missingIdx) > 0 {
		fmt.Fprintf(os.Stderr, "[INFO] %d question page(s) failed on first pass; retrying sequentially...\n", len(missingIdx))
		retryLimiter := utils.CreateRateLimiter(1.0)
		defer retryLimiter.Stop()
		for _, i := range missingIdx {
			<-retryLimiter.C
			url := utils.AddToBaseUrl(sortedLinks[i])
			if data := getDataFromLink(url, solutions); data != nil {
				results[i] = data
			}
		}
	}

	var finalData []models.QuestionData
	var failedLinks []string
	for i, entry := range results {
		if entry != nil {
			finalData = append(finalData, *entry)
		} else {
			failedLinks = append(failedLinks, sortedLinks[i])
		}
	}

	total := len(sortedLinks)
	got := len(finalData)
	if got < total {
		fmt.Fprintf(os.Stderr, "[WARN] Extracted %d of %d question pages (%d failed).\n", got, total, total-got)
		preview := failedLinks
		if len(preview) > 10 {
			preview = preview[:10]
		}
		for _, link := range preview {
			fmt.Fprintf(os.Stderr, "  - %s\n", link)
		}
		if len(failedLinks) > len(preview) {
			fmt.Fprintf(os.Stderr, "  ... and %d more.\n", len(failedLinks)-len(preview))
		}
	} else {
		fmt.Printf("Extracted %d of %d question pages.\n", got, total)
	}

	fmt.Printf("Extraction complete in %s.\n", utils.TimeSince(startTime))

	return finalData
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
