package sevdesk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const tok = "0123456789abcdef0123456789abcdef"

func noSleep(context.Context, time.Duration) error { return nil }

func TestValidateToken(t *testing.T) {
	for _, good := range []string{tok, strings.ToUpper(tok)} {
		if err := ValidateToken(good); err != nil {
			t.Errorf("%q rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"", "short", tok + "0", tok[:31] + "g", "Bearer " + tok, " " + tok, tok + "\n"} {
		err := ValidateToken(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("error echoes the token: %v", err)
		}
	}
}

func TestValidateBaseURL(t *testing.T) {
	ok := []string{"https://my.sevdesk.de/api/v1", "https://my.sevdesk.de/api/v1/", "http://127.0.0.1:8080/api/v1", "http://localhost/x", "http://[::1]:9/x"}
	bad := []string{"http://my.sevdesk.de/api/v1", "ftp://x", "https://u:p@my.sevdesk.de", "https://x/?a=b", "https://x/#f", "", "my.sevdesk.de", "http://192.168.1.1/x"}
	for _, u := range ok {
		if _, err := ValidateBaseURL(u); err != nil {
			t.Errorf("%s rejected: %v", u, err)
		}
	}
	for _, u := range bad {
		if _, err := ValidateBaseURL(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

func newTestClient(t *testing.T, h http.HandlerFunc, o Options) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	o.BaseURL = ts.URL + "/api/v1"
	o.Sleep = noSleep
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c, ts
}

func TestRawAuthorizationHeader(t *testing.T) {
	var got string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "{}")
	}, Options{})
	if _, err := c.Do(context.Background(), tok, Request{Method: "GET", Path: "/Contact"}); err != nil {
		t.Fatal(err)
	}
	if got != tok {
		t.Errorf("Authorization = %q, want the raw token without prefix", got)
	}
	if _, err := c.Do(context.Background(), "Bearer "+tok, Request{Method: "GET", Path: "/Contact"}); err == nil {
		t.Error("invalid token accepted by client")
	}
}

func TestRetry429AndNoRetryOnWritesAmbiguity(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		_, _ = io.WriteString(w, `{"objects":[]}`)
	}, Options{})
	// 429 is retried for writes too: the request was rejected before processing.
	if _, err := c.Do(context.Background(), tok, Request{Method: "POST", Path: "/Contact", Body: map[string]any{"a": 1}}); err != nil {
		t.Fatalf("POST after 429: %v", err)
	}
	if n.Load() != 3 {
		t.Errorf("attempts = %d, want 3", n.Load())
	}

	// A write whose connection dies after sending must not be retried.
	var posts atomic.Int32
	c2, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		hj := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}, Options{})
	_ = ts
	_, err := c2.Do(context.Background(), tok, Request{Method: "POST", Path: "/Contact", Body: map[string]any{"a": 1}})
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("want AmbiguousError, got %v", err)
	}
	if posts.Load() != 1 {
		t.Errorf("write was attempted %d times", posts.Load())
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("transport error leaks the URL: %v", err)
	}
	// GET is retried on transport errors.
	posts.Store(0)
	_, err = c2.Do(context.Background(), tok, Request{Method: "GET", Path: "/Contact"})
	if err == nil || posts.Load() != 3 {
		t.Errorf("GET attempts = %d (err %v), want 3", posts.Load(), err)
	}
}

func TestRetriesGiveUp(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(429) }, Options{MaxRetries: 2})
	_, err := c.Do(context.Background(), tok, Request{Method: "GET", Path: "/Contact"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 || n.Load() != 3 {
		t.Errorf("err=%v attempts=%d", err, n.Load())
	}
}

func TestRedirectsAreNeverFollowed(t *testing.T) {
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("credentials were forwarded to the redirect target")
		}
	}))
	defer evil.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, evil.URL+"/steal", code)
		}, Options{})
		for _, m := range []string{"GET", "POST", "DELETE"} {
			_, err := c.Do(context.Background(), tok, Request{Method: m, Path: "/Contact", Body: nil})
			var ae *APIError
			if !errors.As(err, &ae) || !strings.Contains(ae.Error(), "redirect not followed") {
				t.Errorf("%d %s: %v", code, m, err)
			}
		}
	}
	if hits.Load() != 0 {
		t.Errorf("redirect target contacted %d times", hits.Load())
	}
}

func TestDryRunNeverSendsWrites(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); _, _ = io.WriteString(w, "{}") }, Options{DryRun: true})
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		r, err := c.Do(context.Background(), tok, Request{Method: m, Path: "/Contact/1", Body: map[string]any{"x": 1}})
		if err != nil || !r.DryRun || strings.Contains(string(r.Body), tok) {
			t.Fatalf("%s: %v %+v", m, err, r)
		}
	}
	if n.Load() != 0 {
		t.Errorf("dry run sent %d requests", n.Load())
	}
	if _, err := c.Do(context.Background(), tok, Request{Method: "GET", Path: "/Contact"}); err != nil || n.Load() != 1 {
		t.Errorf("GET in dry run: %v %d", err, n.Load())
	}
}

func TestAPIErrorFormats(t *testing.T) {
	e := &APIError{Method: "GET", Path: "/X", Status: 400, Body: `{"error":{"message":"bad field","code":400}}`}
	if s := e.Error(); !strings.Contains(s, `"bad field"`) || !strings.Contains(s, "upstream message") {
		t.Error(s)
	}
	e = &APIError{Method: "GET", Path: "/X", Status: 500, Body: strings.Repeat("x", 5000)}
	if len(e.Error()) > 1500 {
		t.Error("error not truncated")
	}
}

func TestRateLimiterPerToken(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }, Options{RateLimit: 50})
	first := c.limiter(tok)
	if first != c.limiter(tok) {
		t.Error("limiter not reused")
	}
	if first == c.limiter(strings.Repeat("a", 32)) {
		t.Error("limiter shared between tokens")
	}
	for i := 0; i < 300; i++ {
		c.limiter(fmt.Sprintf("%032x", i))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.limiters) > 256 {
		t.Errorf("limiter map unbounded: %d", len(c.limiters))
	}
}
