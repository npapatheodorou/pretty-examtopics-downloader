package utils

import (
	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// whitespacePattern collapses runs of whitespace. Compiled once at package
// load rather than on every CleanText call (which runs per question field).
var whitespacePattern = regexp.MustCompile(`\s+`)

func CleanText(raw string) string {
	// Remove excessive whitespace (newlines, tabs, etc.)
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "🗳️", "")
	raw = strings.ReplaceAll(raw, "🗳", "")
	raw = strings.ReplaceAll(raw, "\n", " ")
	raw = strings.ReplaceAll(raw, "\t", " ")

	cleaned := whitespacePattern.ReplaceAllString(raw, " ")
	cleaned = strings.TrimSpace(cleaned)

	// Add newline before "Suggested Answer"
	cleaned = strings.Replace(cleaned, "Suggested Answer", "\nSuggested Answer", 1)
	cleaned = strings.ReplaceAll(cleaned, "Forgot my password", "")

	return cleaned
}

type AutoCloseFile struct {
	*os.File
}

func (f *AutoCloseFile) Close() {
	if f.File != nil {
		f.File.Close()
		f.File = nil
	}
}

func CreateFile(filename string) *AutoCloseFile {
	file, err := os.Create(filename)
	if err != nil {
		panic(err)
	}

	// Callers are expected to defer Close(). AutoCloseFile.Close is idempotent,
	// so a double close is harmless. (A previous version installed a finalizer
	// here, but it was attached to a throwaway wrapper and never ran — removed.)
	return &AutoCloseFile{file}
}

func DeduplicateLinks(links []string) []string {
	seen := make(map[string]struct{})
	var unique []string
	for _, link := range links {
		if _, exists := seen[link]; !exists {
			seen[link] = struct{}{}
			unique = append(unique, link)
		}
	}
	return unique
}

func extractQuestionNum(url string) int {
	parts := strings.Split(url, "question-")
	if len(parts) < 2 {
		return 0
	}
	numStr := strings.TrimSuffix(parts[1], "/")
	numStr = strings.TrimSuffix(numStr, "-discussion")
	num, _ := strconv.Atoi(numStr)
	return num
}

func extractTopicNum(url string) int {
	parts := strings.Split(url, "topic-")
	if len(parts) < 2 {
		return 0
	}
	subParts := strings.Split(parts[1], "-")
	if len(subParts) < 1 {
		return 0
	}
	num, _ := strconv.Atoi(subParts[0])
	return num
}

func SortLinksByQuestionNumber(links []string) []string {
	// Decorate-sort-undecorate: parse each URL's topic/question number once
	// instead of re-splitting inside every comparator call.
	type keyed struct {
		link  string
		topic int
		qnum  int
	}
	decorated := make([]keyed, len(links))
	for i, link := range links {
		decorated[i] = keyed{link: link, topic: extractTopicNum(link), qnum: extractQuestionNum(link)}
	}

	sort.Slice(decorated, func(i, j int) bool {
		if decorated[i].topic != decorated[j].topic {
			return decorated[i].topic < decorated[j].topic
		}
		return decorated[i].qnum < decorated[j].qnum
	})

	for i := range decorated {
		links[i] = decorated[i].link
	}
	return links
}

func GrepString(baseString, searchString string) bool {
	return strings.Contains(
		strings.ToLower(baseString),
		strings.ToLower(searchString),
	)
}

func AddToBaseUrl(addString string) string {
	return fmt.Sprintf("https://www.examtopics.com%s", addString)
}

func CreateRateLimiter(rps float64) *time.Ticker {
	interval := time.Duration(float64(time.Second) / rps)
	return time.NewTicker(interval)
}

// AdaptiveLimiter is a thread-safe request pacer implementing AIMD
// (additive-increase / multiplicative-decrease). It begins at a conservative
// rate and only edges faster after a run of successful responses, never
// exceeding maxRPS; the instant the server signals throttling (e.g. HTTP
// 429/503) it halves its rate down toward minRPS. This preserves the polite
// default behaviour while letting tolerant servers be drained much faster than
// a fixed rate would allow.
type AdaptiveLimiter struct {
	mu         sync.Mutex
	minRPS     float64
	maxRPS     float64
	stepRPS    float64
	rps        float64
	streak     int
	streakGoal int
	next       time.Time // earliest instant the next token may be granted
}

func NewAdaptiveLimiter(startRPS, minRPS, maxRPS, stepRPS float64, streakGoal int) *AdaptiveLimiter {
	if minRPS <= 0 {
		minRPS = 0.1
	}
	if maxRPS < minRPS {
		maxRPS = minRPS
	}
	if startRPS < minRPS {
		startRPS = minRPS
	}
	if startRPS > maxRPS {
		startRPS = maxRPS
	}
	if streakGoal < 1 {
		streakGoal = 1
	}
	return &AdaptiveLimiter{
		minRPS:     minRPS,
		maxRPS:     maxRPS,
		stepRPS:    stepRPS,
		rps:        startRPS,
		streakGoal: streakGoal,
	}
}

func (a *AdaptiveLimiter) intervalLocked() time.Duration {
	return time.Duration(float64(time.Second) / a.rps)
}

// Wait blocks until the next request is allowed under the current rate. Safe
// for concurrent use: grants are serialized and evenly spaced.
func (a *AdaptiveLimiter) Wait() {
	a.mu.Lock()
	now := time.Now()
	if a.next.Before(now) {
		a.next = now
	}
	wait := a.next.Sub(now)
	a.next = a.next.Add(a.intervalLocked())
	a.mu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}
}

// OnSuccess records a successful response. After streakGoal consecutive
// successes the rate is nudged up by stepRPS (additive increase), capped at maxRPS.
func (a *AdaptiveLimiter) OnSuccess() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.streak++
	if a.streak >= a.streakGoal {
		a.streak = 0
		a.rps += a.stepRPS
		if a.rps > a.maxRPS {
			a.rps = a.maxRPS
		}
	}
}

// OnThrottle records a throttling signal. The rate is halved (multiplicative
// decrease) down to minRPS and the next grant is pushed out so in-flight
// goroutines feel the brake immediately.
func (a *AdaptiveLimiter) OnThrottle() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.streak = 0
	a.rps /= 2
	if a.rps < a.minRPS {
		a.rps = a.minRPS
	}
	brake := time.Now().Add(a.intervalLocked())
	if a.next.Before(brake) {
		a.next = brake
	}
}

// RPS returns the current target requests-per-second (for diagnostics).
func (a *AdaptiveLimiter) RPS() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rps
}

func DelayTime(backoff time.Duration) time.Duration {
	return backoff + time.Duration(rand.Intn(500))*time.Millisecond
}

func BackoffTime(backoff time.Duration, backoffFactor float64) time.Duration {
	return time.Duration(float64(backoff) * backoffFactor)
}

func Sleep(seconds time.Duration) {
	time.Sleep(seconds)
}

// NewHTTPClient creates an optimized HTTP client
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   constants.HttpTimeout,
		Transport: models.OptimizedTransport(),
	}
}

func StartTime() time.Time {
	return time.Now()
}

func TimeSince(startTime time.Time) string {
	duration := time.Since(startTime)

	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh%dm%ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
