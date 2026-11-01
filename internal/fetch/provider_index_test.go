package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalutils "examtopics-downloader/internal/utils"
)

func configureProviderIndexTest(t *testing.T, serverURL string) string {
	t.Helper()
	tempDir := t.TempDir()

	oldBaseURL := examTopicsBaseURL
	oldClient := client
	oldLimiter := requestLimiter
	oldCacheEnabled := providerIndexCacheEnabled
	oldPageCacheEnabled := pageCacheEnabled
	oldCacheOverride := providerIndexCachePathOverride
	oldLegacyOverride := legacyIndexCachePathOverride

	examTopicsBaseURL = serverURL
	client = &http.Client{Timeout: 2 * time.Second}
	requestLimiter = internalutils.NewAdaptiveLimiter(1000, 1000, 1000, 1, 1)
	providerIndexCacheEnabled = true
	pageCacheEnabled = false
	providerIndexCachePathOverride = filepath.Join(tempDir, "provider-index.json")
	legacyIndexCachePathOverride = filepath.Join(tempDir, "legacy-index.json")
	providerIndexCacheMu.Lock()
	providerIndexCacheLoaded = false
	providerIndexCache = providerIndexCacheFile{Version: providerIndexCacheVersion, Providers: map[string]ProviderIndex{}}
	providerIndexCacheMu.Unlock()

	t.Cleanup(func() {
		examTopicsBaseURL = oldBaseURL
		client = oldClient
		requestLimiter = oldLimiter
		providerIndexCacheEnabled = oldCacheEnabled
		pageCacheEnabled = oldPageCacheEnabled
		providerIndexCachePathOverride = oldCacheOverride
		legacyIndexCachePathOverride = oldLegacyOverride
		providerIndexCacheMu.Lock()
		providerIndexCacheLoaded = false
		providerIndexCache = providerIndexCacheFile{Version: providerIndexCacheVersion, Providers: map[string]ProviderIndex{}}
		providerIndexCacheMu.Unlock()
	})
	return tempDir
}

func discussionIndexServer(t *testing.T, pages int, slow *atomic.Bool) (*httptest.Server, map[int]int, *sync.Mutex) {
	t.Helper()
	hits := map[int]int{}
	var hitsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discussions/acme/" {
			fmt.Fprintf(w, `<div class="discussion-list-page-indicator"><strong>1</strong><strong>%d</strong></div>`, pages)
			return
		}
		var page int
		if _, err := fmt.Sscanf(r.URL.Path, "/discussions/acme/%d", &page); err != nil || page < 1 || page > pages {
			http.NotFound(w, r)
			return
		}
		hitsMu.Lock()
		hits[page]++
		hitsMu.Unlock()
		if slow != nil && slow.Load() && page >= 2 {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprintf(w, `<a href="/discussions/acme/view/%d-exam-cert-%d-topic-1-question-%d-discussion/">Q</a>`, page, page, page)
	}))
	return server, hits, &hitsMu
}

func TestProviderDiscussionIndexCompleteThenCached(t *testing.T) {
	server, hits, hitsMu := discussionIndexServer(t, 4, nil)
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	lastCompleted := 0
	index, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{Workers: 2, Timeout: 5 * time.Second}, func(progress Progress) {
		if progress.Completed < lastCompleted {
			t.Fatalf("progress moved backwards: %d after %d", progress.Completed, lastCompleted)
		}
		lastCompleted = progress.Completed
	})
	if err != nil {
		t.Fatalf("index build failed: %v", err)
	}
	if !index.Complete || len(index.CompletedPages) != 4 || len(index.Links) != 4 || len(index.ExamSlugs) != 4 {
		t.Fatalf("unexpected complete index: %+v", index)
	}
	if !sort.StringsAreSorted(index.Links) || !sort.StringsAreSorted(index.ExamSlugs) {
		t.Fatalf("index output must be deterministic: %+v", index)
	}

	hitsMu.Lock()
	hitsBefore := 0
	for _, count := range hits {
		hitsBefore += count
	}
	hitsMu.Unlock()
	cached, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{}, nil)
	if err != nil || !cached.FromCache {
		t.Fatalf("expected complete cache hit, index=%+v err=%v", cached, err)
	}
	hitsMu.Lock()
	hitsAfter := 0
	for _, count := range hits {
		hitsAfter += count
	}
	hitsMu.Unlock()
	if hitsAfter != hitsBefore {
		t.Fatalf("cache hit made listing requests: before=%d after=%d", hitsBefore, hitsAfter)
	}
}

func TestProviderDiscussionIndexHighConcurrencyCompletesSafely(t *testing.T) {
	server, _, _ := discussionIndexServer(t, 200, nil)
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	index, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{
		Workers: 8,
		Timeout: 10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("high-concurrency index failed: %v", err)
	}
	if !index.Complete || len(index.CompletedPages) != 200 {
		t.Fatalf("unexpected high-concurrency result: complete=%v pages=%d", index.Complete, len(index.CompletedPages))
	}
}

func TestOfficialExamSlugsDoNotRequestDiscussionPages(t *testing.T) {
	var discussionHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/discussions/") {
			discussionHits.Add(1)
			http.Error(w, "discussion page must not be requested", http.StatusInternalServerError)
			return
		}
		if r.URL.Path != "/exams/acme/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<a href="/exams/acme/cert-b/">B</a><a href="/exams/acme/cert-a/">A</a>`)
	}))
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	slugs, err := GetOfficialExamSlugs(context.Background(), "acme")
	if err != nil || strings.Join(slugs, ",") != "cert-a,cert-b" {
		t.Fatalf("official exam load failed: slugs=%v err=%v", slugs, err)
	}
	if discussionHits.Load() != 0 {
		t.Fatalf("official-first load requested %d discussion page(s)", discussionHits.Load())
	}
}

func TestProviderDiscussionIndexCancellationPersistsAndResumes(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	server, hits, hitsMu := discussionIndexServer(t, 4, &slow)
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(80*time.Millisecond, cancel)
	partial, err := GetProviderDiscussionIndex(ctx, "acme", IndexOptions{Workers: 1, Timeout: 5 * time.Second}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if partial.Complete || len(partial.CompletedPages) == 0 {
		t.Fatalf("expected a persisted partial index, got %+v", partial)
	}
	hitsMu.Lock()
	pageOneHits := hits[1]
	hitsMu.Unlock()

	slow.Store(false)
	resumed, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{Workers: 2, Timeout: 5 * time.Second}, nil)
	if err != nil || !resumed.Complete {
		t.Fatalf("resume failed, index=%+v err=%v", resumed, err)
	}
	hitsMu.Lock()
	resumedPageOneHits := hits[1]
	hitsMu.Unlock()
	if resumedPageOneHits != pageOneHits {
		t.Fatalf("resume refetched completed page 1: before=%d after=%d", pageOneHits, resumedPageOneHits)
	}
}

func TestProviderDiscussionIndexDeadlineReturnsPartialResult(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	server, _, _ := discussionIndexServer(t, 4, &slow)
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	partial, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{
		Workers: 1,
		Timeout: 80 * time.Millisecond,
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected discovery deadline, got %v", err)
	}
	if partial.Complete || len(partial.CompletedPages) == 0 {
		t.Fatalf("deadline should return completed partial work: %+v", partial)
	}
}

func TestProviderDiscussionIndexCachesCompletedEmptyScan(t *testing.T) {
	var pageHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discussions/acme/":
			fmt.Fprint(w, `<div class="discussion-list-page-indicator"><strong>1</strong><strong>1</strong></div>`)
		case "/discussions/acme/1", "/discussions/acme/1/":
			pageHits.Add(1)
			fmt.Fprint(w, `<p>No discussions</p>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	index, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{}, nil)
	if err != nil || !index.Complete || len(index.ExamSlugs) != 0 {
		t.Fatalf("empty scan should be complete and valid: %+v err=%v", index, err)
	}
	_, err = GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{}, nil)
	if err != nil || pageHits.Load() != 1 {
		t.Fatalf("completed empty result was not cached; hits=%d err=%v", pageHits.Load(), err)
	}
}

func TestProviderIndexMigratesLegacySlugCache(t *testing.T) {
	tempDir := configureProviderIndexTest(t, "http://unused.invalid")
	legacy := legacyDiscussionExamCacheFile{Providers: map[string]legacyDiscussionExamCacheEntry{
		"acme": {ExamSlugs: []string{"cert-b", "cert-a"}, UpdatedAt: time.Now().Unix()},
	}}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(tempDir, "legacy-index.json")
	if err := os.WriteFile(legacyPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	slugs, ok := GetCachedProviderExamSlugs("acme")
	if !ok || strings.Join(slugs, ",") != "cert-a,cert-b" {
		t.Fatalf("legacy cache migration failed: ok=%v slugs=%v", ok, slugs)
	}
	index, ok := getCachedProviderIndex("acme")
	if !ok || index.Complete {
		t.Fatalf("migrated slug-only entry must be partial: %+v ok=%v", index, ok)
	}
}

func TestProviderIndexCacheCanBeDisabled(t *testing.T) {
	tempDir := configureProviderIndexTest(t, "http://unused.invalid")
	providerIndexCacheEnabled = false
	setCachedProviderIndex("acme", ProviderIndex{Provider: "acme", Complete: true, TotalPages: 1})
	if _, ok := GetCachedProviderExamSlugs("acme"); ok {
		t.Fatal("disabled cache returned an entry")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "provider-index.json")); !os.IsNotExist(err) {
		t.Fatalf("disabled cache wrote a file: %v", err)
	}
}

func TestProviderIndexTTLExpiresOldEntry(t *testing.T) {
	configureProviderIndexTest(t, "http://unused.invalid")
	setCachedProviderIndex("acme", ProviderIndex{Provider: "acme", Complete: true, TotalPages: 1, CompletedPages: []int{1}})
	providerIndexCacheMu.Lock()
	entry := providerIndexCache.Providers["acme"]
	entry.UpdatedAtUnix = time.Now().Add(-providerIndexCacheTTL - time.Minute).Unix()
	providerIndexCache.Providers["acme"] = entry
	providerIndexCacheMu.Unlock()
	if _, ok := getCachedProviderIndex("acme"); ok {
		t.Fatal("expired provider index was returned as a cache hit")
	}
}

func TestProviderIndexForceRefreshBypassesCompletedCache(t *testing.T) {
	server, hits, hitsMu := discussionIndexServer(t, 1, nil)
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	if _, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	hitsMu.Lock()
	before := hits[1]
	hitsMu.Unlock()
	if _, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{ForceRefresh: true}, nil); err != nil {
		t.Fatal(err)
	}
	hitsMu.Lock()
	after := hits[1]
	hitsMu.Unlock()
	if after != before+1 {
		t.Fatalf("force refresh did not fetch the listing again: before=%d after=%d", before, after)
	}
}

func TestExtractionReusesCompletedProviderIndex(t *testing.T) {
	var listingHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discussions/acme/":
			fmt.Fprint(w, `<div class="discussion-list-page-indicator"><strong>1</strong><strong>1</strong></div>`)
		case "/discussions/acme/1", "/discussions/acme/1/":
			listingHits.Add(1)
			fmt.Fprint(w, `<a href="/discussions/acme/view/1-exam-cert-1-topic-1-question-1-discussion/">Q1</a>`)
			fmt.Fprint(w, `<a href="/discussions/acme/view/2-exam-cert-1-topic-1-question-2-discussion/">Q2</a>`)
		case "/exams/acme/cert-1/view/":
			fmt.Fprint(w, `<html><body>No public solutions</body></html>`)
		case "/discussions/acme/view/1-exam-cert-1-topic-1-question-1-discussion/",
			"/discussions/acme/view/2-exam-cert-1-topic-1-question-2-discussion/":
			fmt.Fprint(w, `<html><body><h1>Exam cert-1 topic 1 question</h1><div class="question-body" data-id="1"><p class="card-text">Question text</p><ul><li class="multi-choice-item">A. yes</li><li class="multi-choice-item">B. no</li></ul><span class="correct-answer">A</span></div></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	index, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{}, nil)
	if err != nil || !index.Complete {
		t.Fatalf("index build failed: %+v err=%v", index, err)
	}
	hitsBefore := listingHits.Load()
	questions, err := GetAllPages(context.Background(), "acme", "cert-1", nil)
	if err != nil || len(questions) != 2 {
		t.Fatalf("extraction failed: questions=%d err=%v", len(questions), err)
	}
	if listingHits.Load() != hitsBefore {
		t.Fatalf("extraction repeated provider listing crawl: before=%d after=%d", hitsBefore, listingHits.Load())
	}
}

func TestExtractionReturnsRecoveredQuestionsWithPartialError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discussions/acme/":
			fmt.Fprint(w, `<div class="discussion-list-page-indicator"><strong>1</strong><strong>1</strong></div>`)
		case "/discussions/acme/1", "/discussions/acme/1/":
			fmt.Fprint(w, `<a href="/discussions/acme/view/1-exam-cert-1-topic-1-question-1-discussion/">Q1</a>`)
			fmt.Fprint(w, `<a href="/discussions/acme/view/2-exam-cert-1-topic-1-question-2-discussion/">Q2</a>`)
		case "/discussions/acme/view/1-exam-cert-1-topic-1-question-1-discussion/":
			fmt.Fprint(w, `<html><body><h1>Question 1</h1><div class="question-body" data-id="1"><p class="card-text">Question text</p><ul><li class="multi-choice-item">A. yes</li></ul><span class="correct-answer">A</span></div></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureProviderIndexTest(t, server.URL)

	if _, err := GetProviderDiscussionIndex(context.Background(), "acme", IndexOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	questions, err := GetAllPages(context.Background(), "acme", "cert-1", nil)
	if len(questions) != 1 {
		t.Fatalf("recovered %d questions, want 1", len(questions))
	}
	if err == nil || !strings.Contains(err.Error(), "question extraction is incomplete: 1 of 2") {
		t.Fatalf("partial extraction error = %v", err)
	}
}

func BenchmarkFilterProviderIndexLinks(b *testing.B) {
	links := make([]string, 0, 10000)
	for i := 0; i < 10000; i++ {
		links = append(links, fmt.Sprintf("/discussions/acme/view/%d-exam-cert-%d-topic-%d-question-%d-discussion/", i, i%50, i/100, i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches := 0
		for _, link := range links {
			if matchesExamSelection("acme", "cert-17", link) {
				matches++
			}
		}
		if matches == 0 {
			b.Fatal("fixture produced no matches")
		}
	}
}

func BenchmarkBuildProviderIndexSnapshot(b *testing.B) {
	completed := make(map[int]struct{}, 1000)
	links := make(map[string]struct{}, 10000)
	slugs := make(map[string]struct{}, 1000)
	for page := 1; page <= 1000; page++ {
		completed[page] = struct{}{}
		slugs[fmt.Sprintf("cert-%d", page)] = struct{}{}
		for question := 1; question <= 10; question++ {
			link := fmt.Sprintf("/discussions/acme/view/%d-exam-cert-%d-topic-1-question-%d-discussion/", page*10+question, page, question)
			links[link] = struct{}{}
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := buildProviderIndexSnapshot("acme", 1000, completed, links, slugs)
		if !index.Complete || len(index.Links) != 10000 {
			b.Fatal("unexpected snapshot")
		}
	}
}
