package fetch

import (
	"fmt"
	"regexp"
	"strings"

	"examtopics-downloader/internal/models"

	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
)

var imageMarkerRemovePattern = regexp.MustCompile(`\[\[IMG:[^\]]+\]\]`)

// FetchViewSolutions downloads /exams/{provider}/{examSlug}/view/ and parses
// every .question-answer block on the page into an AnswerSolution keyed by
// the question-body's data-id. ExamTopics only exposes the first ~5 questions
// per exam via this endpoint without authentication; pages 2+ are gated by a
// captcha. The returned map is therefore typically small. Callers should
// treat a missing key as "no canonical answer available, fall back".
//
// On any HTTP / parse failure the function returns an empty (non-nil) map
// rather than an error — solution data is best-effort.
func FetchViewSolutions(provider, examSlug string) map[string]*models.AnswerSolution {
	out := map[string]*models.AnswerSolution{}
	provider = strings.TrimSpace(strings.ToLower(provider))
	examSlug = strings.TrimSpace(strings.ToLower(examSlug))
	if provider == "" || examSlug == "" {
		return out
	}

	url := fmt.Sprintf("https://www.examtopics.com/exams/%s/%s/view/", provider, examSlug)
	doc, err := ParseHTML(url, *client)
	if err != nil {
		debugf("view-solutions: parse failed for %s: %v", url, err)
		return out
	}

	parseViewSolutionsFromDoc(doc, out)
	debugf("view-solutions: collected %d canonical answer(s) from %s", len(out), url)
	return out
}

// parseViewSolutionsFromDoc populates dst with one entry per question-body on
// the page. Split out from FetchViewSolutions for direct unit-testing.
func parseViewSolutionsFromDoc(doc *goquery.Document, dst map[string]*models.AnswerSolution) {
	doc.Find(".card-body.question-body").Each(func(_ int, qb *goquery.Selection) {
		id, ok := qb.Attr("data-id")
		if !ok {
			return
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		ans := qb.Find(".question-answer").First()
		if ans.Length() == 0 {
			return
		}

		solution := &models.AnswerSolution{}

		// .correct-answer holds either a letter sequence ("B"/"BD") or an image.
		correct := ans.Find(".correct-answer").First()
		if correct.Length() > 0 {
			letter := strings.TrimSpace(correct.Text())
			if letter != "" {
				solution.Letter = letter
			}
			correct.Find("img").Each(func(_ int, img *goquery.Selection) {
				for _, raw := range imageURLsFromImg(img) {
					if u := normalizeExhibitURL(raw); u != "" {
						if !containsString(solution.Images, u) {
							solution.Images = append(solution.Images, u)
						}
					}
				}
			})
		}

		// .answer-description carries the explanation text + inline images +
		// reference URLs. We walk in document order so the rendered output can
		// re-interleave images with their explanatory text.
		desc := ans.Find(".answer-description").First()
		if desc.Length() > 0 {
			descText, descImages := serializeAnswerDescription(desc)
			solution.Description = descText
			solution.DescImages = descImages
			// Extract references from the prose only — strip the inline
			// [[IMG:<url>]] markers first so image URLs don't pollute the
			// references list.
			prose := imageMarkerRemovePattern.ReplaceAllString(descText, "")
			solution.References = extractURLReferences(prose)
		}

		// Only store the solution if we recovered SOMETHING — an entirely
		// empty answer block isn't useful and would mask the fallback path.
		if solution.Letter == "" && len(solution.Images) == 0 && solution.Description == "" {
			return
		}
		dst[id] = solution
	})
}

// serializeAnswerDescription walks the .answer-description subtree in document
// order, producing a single text string where images are represented as
// [[IMG:<absolute-url>]] markers. It also returns the deduped image-URL list
// in the same order so callers can render them with proper <img> tags.
func serializeAnswerDescription(desc *goquery.Selection) (string, []string) {
	var b strings.Builder
	var images []string
	seenImg := map[string]struct{}{}

	var walk func(*goquery.Selection)
	walk = func(s *goquery.Selection) {
		s.Contents().Each(func(_ int, child *goquery.Selection) {
			node := child.Nodes[0]
			switch node.Type {
			case xhtml.TextNode:
				b.WriteString(node.Data)
			case xhtml.ElementNode:
				name := strings.ToLower(node.Data)
				switch name {
				case "img":
					for _, raw := range imageURLsFromImg(child) {
						if u := normalizeExhibitURL(raw); u != "" {
							if _, ok := seenImg[u]; !ok {
								seenImg[u] = struct{}{}
								images = append(images, u)
							}
							b.WriteString("[[IMG:")
							b.WriteString(u)
							b.WriteString("]]")
							break // one marker per <img>, even if multiple attrs resolved
						}
					}
				case "br":
					b.WriteString("\n")
				default:
					walk(child)
				}
			}
		})
	}
	walk(desc)

	// Collapse runs of whitespace within lines while preserving line breaks.
	lines := strings.Split(b.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
	}
	// Drop leading/trailing empty lines but keep internal ones (they separate
	// "Box 1: …" / "Box 2: …" blocks for the user).
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n"), images
}

// extractURLReferences pulls http(s) URLs out of the description text so the
// renderer can list them as clickable references. Trailing punctuation is
// trimmed and entries are deduped.
func extractURLReferences(text string) []string {
	if text == "" {
		return nil
	}
	raws := urlInTextPattern.FindAllString(text, -1)
	if len(raws) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		r = strings.TrimRight(r, ".,;:)\"'")
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
