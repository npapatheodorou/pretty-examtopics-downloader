package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// questionPageCacheTTL bounds how long a cached question page is trusted before
// it is re-fetched. Question content rarely changes, so a day keeps re-runs of
// the same exam near-instant without serving badly stale data.
const questionPageCacheTTL = 24 * time.Hour

var pageCacheEnabled = true

// SetCacheEnabled toggles the on-disk question-page cache. Disabled via the
// -no-cache CLI flag when the user wants guaranteed-fresh content.
func SetCacheEnabled(enabled bool) {
	pageCacheEnabled = enabled
}

var (
	pageCacheDirOnce sync.Once
	pageCacheDir     string
)

func questionPageCacheDir() string {
	pageCacheDirOnce.Do(func() {
		if base, err := os.UserCacheDir(); err == nil && strings.TrimSpace(base) != "" {
			pageCacheDir = filepath.Join(base, "examtopics-downloader", "pages")
			return
		}
		pageCacheDir = filepath.Join(".", ".examtopics_page_cache")
	})
	return pageCacheDir
}

func pageCachePath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(questionPageCacheDir(), hex.EncodeToString(sum[:])+".html")
}

// isPageCached reports whether a fresh cache entry exists for url, without
// reading its body. Used to skip rate-limiter pacing on cache hits.
func isPageCached(url string) bool {
	if !pageCacheEnabled {
		return false
	}
	info, err := os.Stat(pageCachePath(url))
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) <= questionPageCacheTTL
}

// readCachedPage returns the cached body for url when present and fresh.
// A stale entry is removed and reported as a miss.
func readCachedPage(url string) ([]byte, bool) {
	if !pageCacheEnabled {
		return nil, false
	}
	path := pageCachePath(url)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > questionPageCacheTTL {
		_ = os.Remove(path)
		return nil, false
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// writeCachedPage stores body for url. Best-effort: failures are logged in
// debug mode and otherwise ignored.
func writeCachedPage(url string, body []byte) {
	if !pageCacheEnabled || len(body) == 0 {
		return
	}
	dir := questionPageCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		debugf("page-cache: failed to create dir %q: %v", dir, err)
		return
	}
	if err := os.WriteFile(pageCachePath(url), body, 0o644); err != nil {
		debugf("page-cache: failed to write entry for %s: %v", url, err)
	}
}
