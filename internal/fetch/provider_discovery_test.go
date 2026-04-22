package fetch

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestExtractProvidersFromExamsDoc(t *testing.T) {
	html := `
<div>
  <a href="/exams/cisco/">Cisco</a>
  <a href="/exams/oracle/">Oracle</a>
  <a href="/exams/cisco/">Cisco Duplicate</a>
  <a href="/discussions/cisco/">ignore</a>
</div>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed to parse html: %v", err)
	}

	got := extractProvidersFromExamsDoc(doc)
	want := []string{"cisco", "oracle"}
	if len(got) != len(want) {
		t.Fatalf("expected %d providers, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected provider at index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestExtractProvidersFromDiscussionsDocPositiveCountsOnly(t *testing.T) {
	html := `
<div class="discussion-row">
  <a class="discussion-link" href="/discussions/cisco/">Cisco</a>
  <div class="discussion-stats-replies">42<br/>Discussions</div>
</div>
<div class="discussion-row">
  <a class="discussion-link" href="/discussions/oracle/">Oracle</a>
  <div class="discussion-stats-replies">0<br/>Discussions</div>
</div>
<div class="discussion-row">
  <a class="discussion-link" href="/discussions/test-prep/">Test Prep</a>
  <div class="discussion-stats-replies">12,304<br/>Discussions</div>
</div>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed to parse html: %v", err)
	}

	got := extractProvidersFromDiscussionsDoc(doc)
	want := []string{"cisco", "test-prep"}
	if len(got) != len(want) {
		t.Fatalf("expected %d providers, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected provider at index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestExtractProvidersFromDiscussionsDocWithoutLinkClass(t *testing.T) {
	html := `
<div class="row discussion-row">
  <h2><a href="/discussions/oracle/">Oracle</a></h2>
  <div class="discussion-stats-replies">17548<br/>Discussions</div>
</div>
<div class="row discussion-row">
  <h2><a href="/discussions/cisco/">Cisco</a></h2>
  <div class="discussion-stats-replies">0<br/>Discussions</div>
</div>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed to parse html: %v", err)
	}

	got := extractProvidersFromDiscussionsDoc(doc)
	want := []string{"oracle"}
	if len(got) != len(want) {
		t.Fatalf("expected %d providers, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected provider at index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestExtractProvidersFromDiscussionsDocFallbackNoRows(t *testing.T) {
	html := `
<div>
  <a href="/discussions/oracle/">Oracle</a>
  <a href="/discussions/cisco/">Cisco</a>
  <a href="/discussions/cisco/">Cisco dup</a>
</div>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed to parse html: %v", err)
	}

	got := extractProvidersFromDiscussionsDoc(doc)
	want := []string{"cisco", "oracle"}
	if len(got) != len(want) {
		t.Fatalf("expected %d providers, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected provider at index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestExtractDiscussionCategoryCount(t *testing.T) {
	html := `
<span class="discussion-list-page-indicator">
  <span class="mr-4 d-none d-md-inline-block">
    181 Categories
  </span>
</span>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed to parse html: %v", err)
	}

	got := extractDiscussionCategoryCount(doc)
	if got != 181 {
		t.Fatalf("expected category count 181, got %d", got)
	}
}

func TestExtractExamSlugFromDiscussionURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{
			url:  "/discussions/oracle/view/315137-exam-1z0-1072-25-topic-1-question-4-discussion/",
			want: "1z0-1072-25",
		},
		{
			url:  "/discussions/amazon/view/112809-exam-aws-certified-solutions-architect-professional-sap-c02/",
			want: "aws-certified-solutions-architect-professional-sap-c02",
		},
		{
			url:  "/discussions/something/view/100-topic-1-question-2-discussion/",
			want: "",
		},
	}

	for _, tc := range tests {
		got := extractExamSlugFromDiscussionURL(tc.url)
		if got != tc.want {
			t.Fatalf("unexpected slug for %q: want %q, got %q", tc.url, tc.want, got)
		}
	}
}

func TestNormalizeExamSlugVersionCollapse(t *testing.T) {
	tests := []struct {
		provider string
		input    string
		want     string
	}{
		{provider: "oracle", input: "1z0-1042-20", want: "1z0-1042"},
		{provider: "oracle", input: "1z0-1042-23", want: "1z0-1042"},
		{provider: "oracle", input: "1z0-915-1", want: "1z0-915"},
		{provider: "oracle", input: "1z0-1106-1", want: "1z0-1106"},
		{provider: "oracle", input: "1z0-083", want: "1z0-083"},
		{provider: "some-vendor", input: "exam-core-v2", want: "exam-core"},
		{provider: "some-vendor", input: "exam-core-2024", want: "exam-core"},
		{provider: "cisco", input: "200-301", want: "200-301"},
	}

	for _, tc := range tests {
		got := normalizeExamSlug(tc.provider, tc.input)
		if got != tc.want {
			t.Fatalf("normalizeExamSlug(%q, %q): want %q, got %q", tc.provider, tc.input, tc.want, got)
		}
	}
}

func TestMatchesExamSelectionUsesNormalizedVariants(t *testing.T) {
	tests := []struct {
		provider string
		selected string
		link     string
		want     bool
	}{
		{
			provider: "oracle",
			selected: "1z0-1042",
			link:     "/discussions/oracle/view/111-exam-1z0-1042-20-topic-1-question-1-discussion/",
			want:     true,
		},
		{
			provider: "oracle",
			selected: "1z0-1042",
			link:     "/discussions/oracle/view/222-exam-1z0-1042-23-topic-4-question-17-discussion/",
			want:     true,
		},
		{
			provider: "oracle",
			selected: "1z0-1042-20",
			link:     "/discussions/oracle/view/333-exam-1z0-1042-23-topic-2-question-8-discussion/",
			want:     true,
		},
		{
			provider: "oracle",
			selected: "1z0-1042",
			link:     "/discussions/oracle/view/444-exam-1z0-1106-1-topic-1-question-1-discussion/",
			want:     false,
		},
		{
			provider: "oracle",
			selected: "",
			link:     "/discussions/oracle/view/555-exam-1z0-1106-1-topic-1-question-1-discussion/",
			want:     true,
		},
		{
			provider: "oracle",
			selected: "1z0-1042",
			link:     "/discussions/oracle/view/666-1z0-1042-general-discussion/",
			want:     true,
		},
		{
			provider: "oracle",
			selected: "1z0-1042",
			link:     "/discussions/oracle/view/777-1z0-1042-23-general-discussion/",
			want:     true,
		},
	}

	for _, tc := range tests {
		got := matchesExamSelection(tc.provider, tc.selected, tc.link)
		if got != tc.want {
			t.Fatalf("matchesExamSelection(%q, %q, %q): want %v, got %v", tc.provider, tc.selected, tc.link, tc.want, got)
		}
	}
}

func TestNormalizeDiscussionViewHref(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "/discussions/oracle/view/305691-exam-1z0-1042-23-topic-1-question-3-discussion/",
			want:  "/discussions/oracle/view/305691-exam-1z0-1042-23-topic-1-question-3-discussion/",
		},
		{
			input: "https://www.examtopics.com/discussions/oracle/view/305691-exam-1z0-1042-23-topic-1-question-3-discussion/",
			want:  "/discussions/oracle/view/305691-exam-1z0-1042-23-topic-1-question-3-discussion/",
		},
		{
			input: "https://www.examtopics.com/discussions/oracle/view/305691-exam-1z0-1042-23-topic-1-question-3-discussion/?foo=bar#x",
			want:  "/discussions/oracle/view/305691-exam-1z0-1042-23-topic-1-question-3-discussion/",
		},
		{
			input: "https://example.com/discussions/oracle/view/305691-exam-1z0-1042-23-topic-1-question-3-discussion/",
			want:  "",
		},
		{
			input: "/exams/oracle/1z0-1042-23/",
			want:  "",
		},
	}

	for _, tc := range tests {
		got := normalizeDiscussionViewHref(tc.input)
		if got != tc.want {
			t.Fatalf("normalizeDiscussionViewHref(%q): want %q, got %q", tc.input, tc.want, got)
		}
	}
}

func TestBuildProviderExamSlugsOfficialOnly(t *testing.T) {
	official := []string{
		"/exams/oracle/1z0-1042-20/",
		"/exams/oracle/1z0-1042-23/",
		"/exams/oracle/1z0-1106-1/",
	}

	got := buildProviderExamSlugs("oracle", official, []string{"1z0-9999-1"}, false)
	want := []string{"1z0-1042", "1z0-1106"}

	if len(got) != len(want) {
		t.Fatalf("expected %d exams, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected exam at index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestBuildProviderExamSlugsIncludesDiscussionVariantsWhenEnabled(t *testing.T) {
	official := []string{
		"/exams/microsoft/az-900/",
	}
	inferred := []string{
		"az-104",
		"az-900",
	}

	got := buildProviderExamSlugs("microsoft", official, inferred, true)
	want := []string{"az-104", "az-900"}

	if len(got) != len(want) {
		t.Fatalf("expected %d exams, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected exam at index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestBuildProviderExamSlugsReturnsFallbackOnlyWhenDiscussionsEnabled(t *testing.T) {
	got := buildProviderExamSlugs("microsoft", nil, nil, true)
	want := []string{"all-discussions"}

	if len(got) != len(want) {
		t.Fatalf("expected %d exams, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected exam at index %d: want %q, got %q", i, want[i], got[i])
		}
	}

	got = buildProviderExamSlugs("microsoft", nil, nil, false)
	if len(got) != 0 {
		t.Fatalf("expected no exams when discussions are disabled, got %v", got)
	}
}
