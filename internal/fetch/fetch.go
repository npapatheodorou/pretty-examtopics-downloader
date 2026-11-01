package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/utils"

	"github.com/PuerkitoBio/goquery"
)

var client = utils.NewHTTPClient()
var examTopicsBaseURL = "https://www.examtopics.com"

// requestLimiter paces the concurrent fan-out phases and adapts to the server:
// it speeds up while responses are healthy and brakes hard on throttling. Wired
// for feedback inside FetchURL (the single place that sees status codes).
var requestLimiter = utils.NewAdaptiveLimiter(
	constants.StartRequestsPerSecond,
	constants.MinRequestsPerSecond,
	constants.MaxRequestsPerSecond,
	constants.RateIncreaseStep,
	constants.SuccessStreakForSpeedup,
)

var (
	providerHrefPattern           = regexp.MustCompile(`(?i)^/exams/([a-z0-9-]+)/?$`)
	discussionProviderHrefPattern = regexp.MustCompile(`(?i)^/discussions/([a-z0-9-]+)/?$`)
	discussionViewLinkPattern     = regexp.MustCompile(`(?i)^/discussions/[a-z0-9-]+/view/`)
	examFromDiscussionURLPattern  = regexp.MustCompile(`(?i)-exam-([a-z0-9_-]+?)(?:-topic-|-question-|/|$)`)
	digitsPattern                 = regexp.MustCompile(`\D+`)
	nonAlnumPattern               = regexp.MustCompile(`[^a-z0-9]`)
	oracleVersionedPattern        = regexp.MustCompile(`(?i)^(1z\d-\d{3,4})-\d{1,2}$`)
	oracleBaseCodePattern         = regexp.MustCompile(`(?i)^1z\d-\d{3,4}$`)
	trailingVersionTokenPattern   = regexp.MustCompile(`(?i)^(?:\d{2}|\d{4}|v\d+|ver\d+|rev\d+)$`)
	urlInTextPattern              = regexp.MustCompile(`https?://[^\s"'<>\]]+`)
)

// retryableStatuses are HTTP status codes that justify a retry. Anti-bot
// rate-limiting and gateway hiccups are transient and almost always recover
// on a second pass; they used to be silently dropped.
var retryableStatuses = map[int]struct{}{
	http.StatusTooManyRequests:    {}, // 429
	http.StatusBadGateway:         {}, // 502
	http.StatusServiceUnavailable: {}, // 503
	http.StatusGatewayTimeout:     {}, // 504
}

type RequestPolicy struct {
	Timeout    time.Duration
	MaxRetries int
	Pace       bool
}

type FetchMetrics struct {
	Retries int
	Status  int
}

var (
	QuestionRequestPolicy = RequestPolicy{
		Timeout:    constants.HttpTimeout,
		MaxRetries: constants.MaxRetries,
		Pace:       true,
	}
	MetadataRequestPolicy = RequestPolicy{
		Timeout:    constants.MetadataRequestTimeout,
		MaxRetries: constants.MetadataMaxRetries,
		Pace:       true,
	}
)

func FetchURL(url string, client http.Client) []byte {
	policy := QuestionRequestPolicy
	// Compatibility callers already decide when pacing is appropriate.
	policy.Pace = false
	body, _, err := FetchURLContext(context.Background(), url, client, policy)
	if err != nil {
		return nil
	}
	return body
}

// FetchURLContext performs a bounded, cancellable request. Retry-After and
// exponential backoff are combined into one delay so a throttled response does
// not incur two consecutive sleeps before the next attempt.
func FetchURLContext(ctx context.Context, url string, client http.Client, policy RequestPolicy) ([]byte, FetchMetrics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if policy.Timeout <= 0 {
		policy.Timeout = constants.HttpTimeout
	}
	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}

	backoff := constants.InitalBackoff
	var lastStatus int
	var metrics FetchMetrics
	var nextDelay time.Duration

	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		if policy.Pace {
			if err := requestLimiter.WaitContextAfter(ctx, nextDelay); err != nil {
				return nil, metrics, err
			}
		} else if nextDelay > 0 {
			if err := sleepContext(ctx, nextDelay); err != nil {
				return nil, metrics, err
			}
		}
		if attempt > 0 {
			debugf("Retry attempt %d for URL: %s after one combined delay of %v", attempt, url, nextDelay)
		}

		requestCtx, cancel := context.WithTimeout(ctx, policy.Timeout)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			debugf("failed to create request for URL %s: %v", url, err)
			return nil, metrics, err
		}
		// Reduce anti-bot 403s by mimicking a normal browser request.
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Referer", "https://www.examtopics.com/")

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			debugf("failed to fetch URL (attempt %d): %v", attempt, err)
			if ctx.Err() != nil {
				return nil, metrics, ctx.Err()
			}
			metrics.Retries = attempt
			if attempt < policy.MaxRetries {
				metrics.Retries = attempt + 1
				nextDelay = utils.DelayTime(backoff)
				backoff = utils.BackoffTime(backoff, constants.BackoffFactor)
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			cancel()
			if err != nil {
				debugf("failed to read response body: %v", err)
				return nil, metrics, err
			}
			requestLimiter.OnSuccess()
			metrics.Status = resp.StatusCode
			metrics.Retries = attempt
			return body, metrics, nil
		}

		lastStatus = resp.StatusCode
		metrics.Status = resp.StatusCode
		if _, retryable := retryableStatuses[resp.StatusCode]; retryable {
			// Server is pushing back — slow the whole fan-out, not just this call.
			requestLimiter.OnThrottle()
			// Honour Retry-After when the server provides one (common on 429).
			metrics.Retries = attempt
			if attempt < policy.MaxRetries {
				metrics.Retries = attempt + 1
				nextDelay = utils.DelayTime(backoff)
				if retryAfter := parseRetryAfter(resp.Header.Get("Retry-After")); retryAfter > nextDelay {
					nextDelay = retryAfter
					debugf("status %d; using Retry-After=%v for %s", resp.StatusCode, retryAfter, url)
				}
				backoff = utils.BackoffTime(backoff, constants.BackoffFactor)
			}
			resp.Body.Close()
			cancel()
			continue
		}

		resp.Body.Close()
		cancel()
		debugf("request failed with non-retryable status code: %d for %s", resp.StatusCode, url)
		fmt.Fprintf(os.Stderr, "[WARN] HTTP %d for %s (not retried)\n", resp.StatusCode, url)
		return nil, metrics, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	if lastStatus != 0 {
		fmt.Fprintf(os.Stderr, "[WARN] Gave up after %d retries (last status %d) for %s\n", policy.MaxRetries, lastStatus, url)
	} else {
		fmt.Fprintf(os.Stderr, "[WARN] Gave up after %d retries (transport errors) for %s\n", policy.MaxRetries, url)
	}
	debugf("exhausted retries for URL: %s", url)
	return nil, metrics, fmt.Errorf("exhausted retries for %s", url)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseRetryAfter accepts either a delay in seconds or an HTTP date and
// returns the duration to wait. Returns 0 when unparseable.
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs < 0 {
			return 0
		}
		if secs > 60 {
			secs = 60 // cap to keep the loop responsive
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0
		}
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		return d
	}
	return 0
}

func ParseHTML(url string, client http.Client) (*goquery.Document, error) {
	policy := QuestionRequestPolicy
	policy.Pace = false
	doc, _, err := ParseHTMLContext(context.Background(), url, client, policy)
	return doc, err
}

func ParseHTMLContext(ctx context.Context, url string, client http.Client, policy RequestPolicy) (*goquery.Document, FetchMetrics, error) {
	body, metrics, err := FetchURLContext(ctx, url, client, policy)
	if err != nil {
		return nil, metrics, err
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, metrics, fmt.Errorf("failed to parse HTML from URL %q: %w", url, err)
	}

	return doc, metrics, nil
}

// ParseHTMLCached behaves like ParseHTML but serves and stores the raw response
// body in the on-disk question-page cache. Re-running the same exam then skips
// the network (and rate limiting) for unchanged question pages. A corrupt or
// unparseable cache entry transparently falls back to a fresh fetch.
func ParseHTMLCached(url string, client http.Client) (*goquery.Document, error) {
	policy := QuestionRequestPolicy
	policy.Pace = false
	doc, _, _, err := ParseHTMLCachedContext(context.Background(), url, client, policy)
	return doc, err
}

func ParseHTMLCachedContext(ctx context.Context, url string, client http.Client, policy RequestPolicy) (*goquery.Document, bool, FetchMetrics, error) {
	if body, ok := readCachedPage(url); ok {
		if doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body)); err == nil {
			debugf("page-cache: hit for %s", url)
			return doc, true, FetchMetrics{}, nil
		} else {
			debugf("page-cache: corrupt entry for %s, refetching: %v", url, err)
		}
	}

	body, metrics, err := FetchURLContext(ctx, url, client, policy)
	if err != nil {
		return nil, false, metrics, err
	}
	writeCachedPage(url, body)

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, false, metrics, fmt.Errorf("failed to parse HTML from URL %q: %w", url, err)
	}
	return doc, false, metrics, nil
}

func getMaxNumPagesContext(ctx context.Context, url string) (int, FetchMetrics, error) {
	doc, metrics, err := ParseHTMLContext(ctx, url, *client, MetadataRequestPolicy)
	if err != nil {
		return 0, metrics, err
	}

	var pageCount int
	doc.Find(".discussion-list-page-indicator strong").Each(func(i int, s *goquery.Selection) {
		if i == 1 {
			pageCount, _ = strconv.Atoi(strings.TrimSpace(s.Text()))
		}
	})

	// Handle the null case
	if pageCount == 0 {
		pageCount = 1
	}

	return pageCount, metrics, nil
}

func GetAllProviders() []string {
	providers, _ := GetAllProvidersContext(context.Background())
	return providers
}

// GetAllProvidersContext fetches the two provider metadata sources in
// parallel. Each request uses the short metadata policy, so provider selection
// cannot inherit the much longer question-page retry budget.
func GetAllProvidersContext(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type providerResult struct {
		providers []string
		err       error
	}
	results := make(chan providerResult, 2)
	for _, source := range []func(context.Context) ([]string, error){
		getProvidersFromExamsContext,
		getProvidersFromDiscussionsContext,
	} {
		go func(load func(context.Context) ([]string, error)) {
			providers, err := load(ctx)
			results <- providerResult{providers: providers, err: err}
		}(source)
	}

	seen := map[string]struct{}{}
	providers := make([]string, 0, 64)
	var lastErr error
	for range 2 {
		result := <-results
		if result.err != nil {
			lastErr = result.err
			debugf("provider metadata source failed: %v", result.err)
		}
		for _, provider := range result.providers {
			if _, exists := seen[provider]; exists {
				continue
			}
			seen[provider] = struct{}{}
			providers = append(providers, provider)
		}
	}

	sort.Strings(providers)
	if len(providers) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return providers, nil
}

func getProvidersFromExams() []string {
	providers, _ := getProvidersFromExamsContext(context.Background())
	return providers
}

func getProvidersFromExamsContext(ctx context.Context) ([]string, error) {
	doc, _, err := ParseHTMLContext(ctx, examTopicsBaseURL+"/exams/", *client, MetadataRequestPolicy)
	if err != nil {
		debugf("failed to parse HTML for providers from exams: %v", err)
		return nil, err
	}
	return extractProvidersFromExamsDoc(doc), nil
}

func getProvidersFromDiscussions() []string {
	providers, _ := getProvidersFromDiscussionsContext(context.Background())
	return providers

}

func getProvidersFromDiscussionsContext(ctx context.Context) ([]string, error) {
	doc, _, err := ParseHTMLContext(ctx, examTopicsBaseURL+"/discussions/", *client, MetadataRequestPolicy)
	if err != nil {
		debugf("failed to parse HTML for providers from discussions: %v", err)
		return nil, err
	}
	return extractProvidersFromDiscussionsDoc(doc), nil
}

func extractProvidersFromExamsDoc(doc *goquery.Document) []string {
	seen := map[string]struct{}{}
	providers := make([]string, 0, 32)

	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}

		href = toRelativePath(strings.TrimSpace(strings.ToLower(href)))
		matches := providerHrefPattern.FindStringSubmatch(href)
		if len(matches) != 2 {
			return
		}

		provider := strings.TrimSpace(matches[1])
		if provider == "" {
			return
		}
		if _, exists := seen[provider]; exists {
			return
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	})

	sort.Strings(providers)
	return providers
}

func extractProvidersFromDiscussionsDoc(doc *goquery.Document) []string {
	seen := map[string]struct{}{}
	providers := make([]string, 0, 32)

	addProvider := func(provider string) {
		provider = strings.TrimSpace(strings.ToLower(provider))
		if provider == "" {
			return
		}
		if _, exists := seen[provider]; exists {
			return
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}

	// Primary strategy: parse provider rows and respect "discussions > 0".
	doc.Find(".discussion-row").Each(func(i int, row *goquery.Selection) {
		provider := ""
		row.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
			href, exists := a.Attr("href")
			if !exists {
				return true
			}

			href = toRelativePath(strings.TrimSpace(strings.ToLower(href)))
			matches := discussionProviderHrefPattern.FindStringSubmatch(href)
			if len(matches) != 2 {
				return true
			}

			provider = strings.TrimSpace(matches[1])
			return false
		})

		if provider == "" {
			return
		}

		// Prefer rows with discussions > 0; if count is missing/unparseable, keep provider.
		if countNode := row.Find(".discussion-stats-replies").First(); countNode.Length() > 0 {
			countText := countNode.Text()
			count := parseDiscussionCount(countText)
			if count == 0 && strings.TrimSpace(countText) != "" {
				return
			}
		}

		addProvider(provider)
	})

	// Fallback: if row parsing yields nothing (markup drift / anti-bot HTML),
	// collect providers from plain provider links.
	if len(providers) == 0 {
		doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
			href, exists := s.Attr("href")
			if !exists {
				return
			}

			href = strings.TrimSpace(strings.ToLower(href))
			href = toRelativePath(href)
			matches := discussionProviderHrefPattern.FindStringSubmatch(href)
			if len(matches) != 2 {
				return
			}

			addProvider(matches[1])
		})
	}

	sort.Strings(providers)
	return providers
}

func parseDiscussionCount(raw string) int {
	clean := digitsPattern.ReplaceAllString(raw, "")
	if clean == "" {
		return 0
	}

	count, err := strconv.Atoi(clean)
	if err != nil {
		return 0
	}
	return count
}

func extractDiscussionCategoryCount(doc *goquery.Document) int {
	if doc == nil {
		return 0
	}

	count := 0
	doc.Find(".discussion-list-page-indicator").First().Find("span").EachWithBreak(func(i int, s *goquery.Selection) bool {
		n := parseDiscussionCount(s.Text())
		if n <= 0 {
			return true
		}
		count = n
		return false
	})

	return count
}

func GetProviderExams(providerName string) []string {
	links, err := getProviderExamsContext(context.Background(), providerName)
	if err != nil {
		debugf("failed to parse HTML for provider exams: %v", err)
		return nil
	}
	return links
}

func getProviderExamsContext(ctx context.Context, providerName string) ([]string, error) {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	if providerName == "" {
		return nil, fmt.Errorf("provider is required")
	}
	baseURL := fmt.Sprintf("%s/exams/%s/", examTopicsBaseURL, providerName)
	doc, _, err := ParseHTMLContext(ctx, baseURL, *client, MetadataRequestPolicy)
	if err != nil {
		return nil, err
	}

	examHrefPattern := regexp.MustCompile(fmt.Sprintf(`(?i)^/exams/%s/([a-z0-9-]+)/?$`, regexp.QuoteMeta(providerName)))
	seen := map[string]struct{}{}
	allExams := make([]string, 0, 32)

	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}

		cleanHref := toRelativePath(strings.TrimSpace(strings.ToLower(href)))
		matches := examHrefPattern.FindStringSubmatch(cleanHref)
		if len(matches) != 2 {
			return
		}

		examSlug := strings.TrimSpace(matches[1])
		if examSlug == "" {
			return
		}

		normalized := fmt.Sprintf("/exams/%s/%s/", providerName, examSlug)
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		allExams = append(allExams, normalized)
	})

	sort.Strings(allExams)
	return allExams, nil
}

// GetOfficialExamSlugs fetches only the provider's official exam page. It does
// not touch discussion listings, which keeps the initial exam menu bounded.
func GetOfficialExamSlugs(ctx context.Context, providerName string) ([]string, error) {
	links, err := getProviderExamsContext(ctx, providerName)
	if err != nil {
		return nil, err
	}
	return extractExamSlugsFromExamLinks(providerName, links), nil
}

func GetProviderExamSlugs(providerName string, includeDiscussionExams bool) []string {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	if providerName == "" {
		return nil
	}

	officialExamLinks := GetProviderExams(providerName)
	var inferredFromDiscussions []string
	if includeDiscussionExams {
		index, err := GetProviderDiscussionIndex(context.Background(), providerName, IndexOptions{}, nil)
		if err != nil {
			debugf("discussion exam discovery incomplete for %q: %v", providerName, err)
		}
		inferredFromDiscussions = index.ExamSlugs
	}

	return buildProviderExamSlugs(providerName, officialExamLinks, inferredFromDiscussions, includeDiscussionExams)
}

func buildProviderExamSlugs(providerName string, officialExamLinks, inferredFromDiscussions []string, includeDiscussionExams bool) []string {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	if providerName == "" {
		return nil
	}

	seen := map[string]struct{}{}
	examSlugs := make([]string, 0, 32)
	add := func(raw string) {
		normalized := normalizeExamSlug(providerName, raw)
		if normalized == "" {
			return
		}
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		examSlugs = append(examSlugs, normalized)
	}

	officialExamSlugs := extractExamSlugsFromExamLinks(providerName, officialExamLinks)
	for _, exam := range officialExamSlugs {
		add(exam)
	}

	if includeDiscussionExams {
		for _, exam := range inferredFromDiscussions {
			add(exam)
		}
	}

	sort.Strings(examSlugs)
	if len(examSlugs) == 0 {
		if includeDiscussionExams {
			// last-resort fallback to still ingest provider content
			return []string{"all-discussions"}
		}
		return nil
	}

	return examSlugs
}

// MergeProviderExamSlugs normalizes, groups, deduplicates, and sorts any number
// of official/cached/discussion-derived slug sets for the CLI menu.
func MergeProviderExamSlugs(providerName string, sources ...[]string) []string {
	seen := map[string]struct{}{}
	var merged []string
	for _, source := range sources {
		for _, raw := range source {
			slug := normalizeExamSlug(providerName, raw)
			if slug == "" {
				continue
			}
			if _, exists := seen[slug]; exists {
				continue
			}
			seen[slug] = struct{}{}
			merged = append(merged, slug)
		}
	}
	sort.Strings(merged)
	return merged
}

func extractExamSlugsFromExamLinks(providerName string, examLinks []string) []string {
	pattern := regexp.MustCompile(fmt.Sprintf(`(?i)^/exams/%s/([a-z0-9-]+)/?$`, regexp.QuoteMeta(strings.ToLower(strings.TrimSpace(providerName)))))
	seen := map[string]struct{}{}
	out := make([]string, 0, len(examLinks))

	for _, link := range examLinks {
		matches := pattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(link)))
		if len(matches) != 2 {
			continue
		}
		examSlug := strings.TrimSpace(matches[1])
		if examSlug == "" {
			continue
		}
		if _, exists := seen[examSlug]; exists {
			continue
		}
		seen[examSlug] = struct{}{}
		out = append(out, examSlug)
	}

	sort.Strings(out)
	return out
}

func inferExamSlugsFromDiscussionPages(providerName string) []string {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	if providerName == "" {
		return nil
	}

	index, err := GetProviderDiscussionIndex(context.Background(), providerName, IndexOptions{}, nil)
	if err != nil {
		debugf("discussion exam discovery incomplete for %q: %v", providerName, err)
	}
	return index.ExamSlugs
}

func normalizeExamSlug(providerName, examSlug string) string {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	examSlug = strings.TrimSpace(strings.ToLower(examSlug))
	if examSlug == "" {
		return ""
	}

	// Oracle version collapsing: 1z0-1042-20 -> 1z0-1042
	if providerName == "oracle" {
		if m := oracleVersionedPattern.FindStringSubmatch(examSlug); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}

	// Generic version collapsing for common vendor variants:
	// <base>-v2, <base>-2024, <base>-23, <base>-rev3
	parts := strings.Split(examSlug, "-")
	if len(parts) >= 3 {
		last := strings.TrimSpace(parts[len(parts)-1])
		if trailingVersionTokenPattern.MatchString(last) {
			return strings.Join(parts[:len(parts)-1], "-")
		}
	}

	return examSlug
}

func extractExamSlugFromDiscussionURL(link string) string {
	link = strings.TrimSpace(strings.ToLower(link))
	if link == "" {
		return ""
	}

	matches := examFromDiscussionURLPattern.FindStringSubmatch(link)
	if len(matches) != 2 {
		return ""
	}

	slug := strings.Trim(matches[1], "- ")
	if slug == "" {
		return ""
	}
	return slug
}

// canonicalExamKey lowercases and strips every non-alphanumeric character so
// exam slugs that differ only in separators or version punctuation compare
// equal. ExamTopics uses different slug formats in different URL spaces — e.g.
// the official exam page /exams/fortinet/nse6-ots-ar-7-6/ versus the discussion
// URL .../-exam-nse6_ots_ar-76-topic-... — and both reduce to "nse6otsar76".
// Digits are preserved, so distinct versions (…-7-6 vs …-7-7) stay distinct.
func canonicalExamKey(s string) string {
	return nonAlnumPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "")
}

func getDiscussionLinksFromPage(url string) []string {
	links, _ := getDiscussionLinksFromPageWithStatus(url)
	return links
}

// getDiscussionLinksFromPageWithStatus is getDiscussionLinksFromPage but also
// reports whether the page was fetched successfully. The ok flag lets callers
// distinguish "fetched, no links here" from "fetch failed" so transient
// failures can be retried instead of silently dropping ~10 questions per page.
func getDiscussionLinksFromPageWithStatus(url string) (links []string, ok bool) {
	doc, err := ParseHTML(url, *client)
	if err != nil {
		debugf("failed to parse HTML for %s: %v", url, err)
		return nil, false
	}

	return extractDiscussionLinksFromDoc(doc), true
}

func extractDiscussionLinksFromDoc(doc *goquery.Document) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 64)
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}

		clean := normalizeDiscussionViewHref(href)
		if clean == "" {
			return
		}
		if _, exists := seen[clean]; exists {
			return
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	})

	return out
}

func normalizeDiscussionViewHref(rawHref string) string {
	rawHref = strings.TrimSpace(strings.ToLower(rawHref))
	if rawHref == "" {
		return ""
	}

	switch {
	case strings.HasPrefix(rawHref, "https://www.examtopics.com/"):
		rawHref = strings.TrimPrefix(rawHref, "https://www.examtopics.com")
	case strings.HasPrefix(rawHref, "http://www.examtopics.com/"):
		rawHref = strings.TrimPrefix(rawHref, "http://www.examtopics.com")
	case strings.HasPrefix(rawHref, "https://") || strings.HasPrefix(rawHref, "http://"):
		parsed, err := url.Parse(rawHref)
		if err != nil {
			return ""
		}
		host := strings.TrimSpace(strings.ToLower(parsed.Hostname()))
		if host != "www.examtopics.com" && host != "examtopics.com" {
			return ""
		}
		rawHref = parsed.EscapedPath()
	}

	if !strings.HasPrefix(rawHref, "/") {
		rawHref = "/" + rawHref
	}

	if cut := strings.IndexAny(rawHref, "?#"); cut >= 0 {
		rawHref = rawHref[:cut]
	}
	rawHref = strings.TrimSpace(rawHref)
	if rawHref == "" {
		return ""
	}

	if !discussionViewLinkPattern.MatchString(rawHref) {
		return ""
	}

	return rawHref
}

// toRelativePath converts an absolute examtopics.com URL (e.g.
// "https://www.examtopics.com/exams/amazon/") into a path rooted at "/"
// (e.g. "/exams/amazon/") so the relative-path provider/exam patterns match.
// Already-relative paths are returned unchanged; external hosts are left
// untouched (they won't match the provider/exam patterns anyway).
func toRelativePath(href string) string {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "https://www.examtopics.com") {
		href = strings.TrimPrefix(href, "https://www.examtopics.com")
	} else if strings.HasPrefix(href, "http://www.examtopics.com") {
		href = strings.TrimPrefix(href, "http://www.examtopics.com")
	} else if strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "http://") {
		parsed, err := url.Parse(href)
		if err != nil {
			return href
		}
		host := strings.TrimSpace(strings.ToLower(parsed.Hostname()))
		if host != "www.examtopics.com" && host != "examtopics.com" {
			return href
		}
		href = parsed.EscapedPath()
	}

	if !strings.HasPrefix(href, "/") {
		href = "/" + href
	}
	if cut := strings.IndexAny(href, "?#"); cut >= 0 {
		href = href[:cut]
	}
	return strings.TrimSpace(href)
}

func matchesExamSelection(providerName, selectedExam, link string) bool {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	selectedExam = strings.TrimSpace(strings.ToLower(selectedExam))
	if selectedExam == "" {
		return true
	}
	selectedNormalized := normalizeExamSlug(providerName, selectedExam)
	selectedCanonical := canonicalExamKey(selectedExam)

	link = strings.TrimSpace(strings.ToLower(link))
	if link == "" {
		return false
	}

	// Primary strategy: compare the exam slug extracted from the discussion link
	// against the user selection. Try the normalized comparison first (preserves
	// vendor variant grouping, e.g. Oracle), then a separator-insensitive
	// canonical comparison so format differences between the official exam slug
	// and the discussion slug (Fortinet's nse6-ots-ar-7-6 vs nse6_ots_ar-76)
	// still match.
	if linkExamSlug := extractExamSlugFromDiscussionURL(link); linkExamSlug != "" {
		if normalizeExamSlug(providerName, linkExamSlug) == selectedNormalized {
			return true
		}
		if selectedCanonical != "" && canonicalExamKey(linkExamSlug) == selectedCanonical {
			return true
		}
		return false
	}

	// Oracle fallback: match variant-like URLs even when the "exam-..." segment
	// is missing or formatted unusually in discussion links.
	if providerName == "oracle" && oracleBaseCodePattern.MatchString(selectedNormalized) {
		variantPattern := regexp.MustCompile(`(?i)(?:^|[-/])` + regexp.QuoteMeta(selectedNormalized) + `-\d{1,2}(?:[-/]|$)`)
		if variantPattern.MatchString(link) {
			return true
		}
	}

	// Fallback for unusual URL formats where exam slug extraction fails.
	return utils.GrepString(link, selectedExam) || utils.GrepString(link, selectedNormalized)
}
