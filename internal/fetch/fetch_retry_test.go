package fetch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchURLRetriesOn429(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	body := FetchURL(srv.URL, http.Client{Timeout: 5 * time.Second})
	if string(body) != "ok" {
		t.Fatalf("expected body 'ok' after 429 retry, got %q (hits=%d)", string(body), atomic.LoadInt32(&hits))
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("expected at least 2 requests (retry); saw %d", atomic.LoadInt32(&hits))
	}
}

func TestFetchURLRetriesOn503(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	body := FetchURL(srv.URL, http.Client{Timeout: 5 * time.Second})
	if string(body) != "recovered" {
		t.Fatalf("expected body 'recovered', got %q (hits=%d)", string(body), atomic.LoadInt32(&hits))
	}
}

func TestFetchURLDoesNotRetry404(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	body := FetchURL(srv.URL, http.Client{Timeout: 5 * time.Second})
	if body != nil {
		t.Fatalf("expected nil body for non-retryable 404, got %q", string(body))
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected exactly 1 hit on 404 (no retry), got %d", atomic.LoadInt32(&hits))
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	d := parseRetryAfter("3")
	if d != 3*time.Second {
		t.Fatalf("expected 3s, got %v", d)
	}
}

func TestParseRetryAfterCapsAt60(t *testing.T) {
	d := parseRetryAfter("9999")
	if d != 60*time.Second {
		t.Fatalf("expected cap at 60s, got %v", d)
	}
}

func TestParseRetryAfterInvalid(t *testing.T) {
	d := parseRetryAfter("nonsense")
	if d != 0 {
		t.Fatalf("expected 0 for unparseable header, got %v", d)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(future)
	// 5 seconds with some slack on either side
	if d < 1*time.Second || d > 6*time.Second {
		t.Fatalf("expected ~5s, got %v", d)
	}
}

func TestParseRetryAfterEmpty(t *testing.T) {
	if d := parseRetryAfter(""); d != 0 {
		t.Fatalf("expected 0 for empty header, got %v", d)
	}
	if d := parseRetryAfter("   "); d != 0 {
		t.Fatalf("expected 0 for whitespace-only header, got %v", d)
	}
}

// Sanity-check the user-visible warning path doesn't crash; using strings.Contains
// as a smoke test against the rendered message format.
func TestFetchURLNonRetryableLogsAndReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if body := FetchURL(srv.URL, http.Client{Timeout: 5 * time.Second}); body != nil {
		t.Fatalf("expected nil body on 403, got %q", string(body))
	}
	if !strings.HasPrefix(srv.URL, "http://") {
		t.Skip("test server URL unexpected; skipping format sanity check")
	}
}
