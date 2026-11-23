// Package sevdesk is a small client for the sevDesk REST API (v1).
package sevdesk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// DefaultBaseURL is sevDesk's production API.
const DefaultBaseURL = "https://my.sevdesk.de/api/v1"

// MaxResponseBytes caps what is read from sevDesk (PDFs arrive base64 encoded in JSON).
const MaxResponseBytes = 48 << 20

const maxRetryAfter = 30 * time.Second

var tokenRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// ValidateToken checks the sevDesk API token format (32 hex characters).
// The error never contains the token.
func ValidateToken(t string) error {
	if !tokenRe.MatchString(t) {
		return errors.New("sevDesk API token must be 32 hexadecimal characters (sevDesk: Settings > Users > API token)")
	}
	return nil
}

// Digest returns a short, non-reversible identifier of a secret (for session binding and rate limiting).
func Digest(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:8])
}

// ValidateBaseURL accepts https URLs, or http only for loopback hosts (tests).
func ValidateBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid SEVDESK_BASE_URL (expected https://host/api/v1)")
	}
	if u.User != nil {
		return "", errors.New("SEVDESK_BASE_URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("SEVDESK_BASE_URL must not contain a query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		ip := net.ParseIP(strings.Trim(u.Hostname(), "[]"))
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("SEVDESK_BASE_URL must use https (http is only accepted for loopback hosts)")
		}
	default:
		return "", errors.New("SEVDESK_BASE_URL must use https")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Options configures a Client.
type Options struct {
	BaseURL   string
	Timeout   time.Duration
	RateLimit float64 // requests per second per token, 0 disables
	DryRun    bool
	UserAgent string
	// MaxRetries is the number of retries after HTTP 429 (default 3).
	MaxRetries int
	// Sleep replaces the backoff wait (tests).
	Sleep func(context.Context, time.Duration) error
}

// Client talks to sevDesk. The API token is supplied per request so one
// client can serve several tenants.
type Client struct {
	base string
	http *http.Client
	opts Options

	mu       sync.Mutex
	limiters map[string]*limiterEntry
}

// New creates a client.
func New(o Options) (*Client, error) {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	base, err := ValidateBaseURL(o.BaseURL)
	if err != nil {
		return nil, err
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.UserAgent == "" {
		o.UserAgent = "sevdesk-mcp-go"
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 3
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	return &Client{
		base: base,
		http: &http.Client{
			Timeout: o.Timeout,
			// The raw API token travels in the Authorization header: never
			// follow redirects, so it can only reach the configured origin.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		opts:     o,
		limiters: map[string]*limiterEntry{},
	}, nil
}

// BaseURL returns the API base URL.
func (c *Client) BaseURL() string { return c.base }

// DryRun reports whether write requests are only previewed.
func (c *Client) DryRun() bool { return c.opts.DryRun }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// File is a multipart upload.
type File struct {
	Field    string
	Filename string
	Data     []byte
}

// Request is one API call. Path must already be validated (see Match/BuildPath).
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Body   any   // JSON encoded when non-nil
	File   *File // multipart upload, exclusive with Body
}

// Response is a raw API response. For dry runs DryRun is set and Body is the preview.
type Response struct {
	Status int
	Body   []byte
	DryRun bool
}

// APIError is returned for non-2xx responses.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	var m struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(e.Body), &m) == nil {
		if m.Error.Message != "" {
			msg = m.Error.Message
		} else if m.Message != "" {
			msg = m.Message
		}
	}
	if len(msg) > 1000 {
		msg = msg[:1000] + "…"
	}
	hint := ""
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = " (the sevDesk API token was rejected: check SEVDESK_API_TOKEN / the client token; it must belong to an admin user)"
	case http.StatusNotFound:
		hint = " (object not found: check the id with the matching list_* tool)"
	case http.StatusTooManyRequests:
		hint = " (sevDesk rate limit: wait a moment and retry with fewer parallel calls)"
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		hint = " (sevDesk rejected the request body or parameters: check required fields against the operation's schema at https://api.sevdesk.de)"
	}
	// The message is untrusted upstream text; label it as such.
	return fmt.Sprintf("sevDesk %s %s: HTTP %d, upstream message: %q%s", e.Method, e.Path, e.Status, msg, hint)
}

// IsNotFound reports whether err is a 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// AmbiguousError means a non-idempotent request may or may not have been executed.
type AmbiguousError struct {
	Method, Path string
	Err          error
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("sevDesk %s %s failed after the request was sent (%v). The change may or may not have been applied and was NOT retried automatically: look it up with the matching list_*/get_* tool before trying again", e.Method, e.Path, e.Err)
}

func (e *AmbiguousError) Unwrap() error { return e.Err }

func (c *Client) limiter(token string) *rate.Limiter {
	if c.opts.RateLimit <= 0 {
		return nil
	}
	key := Digest(token)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.limiters[key]; ok {
		e.last = now
		return e.l
	}
	// Bound memory when many client tokens are seen: evict the least recently
	// used entry only, so minting tokens cannot reset the limits of active ones.
	if len(c.limiters) >= maxLimiters {
		var oldest string
		var ot time.Time
		for k, e := range c.limiters {
			if oldest == "" || e.last.Before(ot) {
				oldest, ot = k, e.last
			}
		}
		delete(c.limiters, oldest)
	}
	burst := max(1, int(c.opts.RateLimit))
	l := rate.NewLimiter(rate.Limit(c.opts.RateLimit), burst)
	c.limiters[key] = &limiterEntry{l: l, last: now}
	return l
}

const maxLimiters = 256

type limiterEntry struct {
	l    *rate.Limiter
	last time.Time
}

func (c *Client) build(ctx context.Context, token string, r Request) (*http.Request, error) {
	u := c.base + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	var body io.Reader
	ctype := ""
	switch {
	case r.File != nil:
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(r.File.Field), escapeQuotes(r.File.Filename)))
		h.Set("Content-Type", "application/octet-stream")
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(r.File.Data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		body, ctype = &buf, w.FormDataContentType()
	case r.Body != nil:
		b, err := json.Marshal(r.Body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		body, ctype = bytes.NewReader(b), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	// sevDesk expects the raw token, without a "Bearer " prefix.
	req.Header.Set("Authorization", token)
	return req, nil
}

func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "_", "\"", "_", "\r", "_", "\n", "_").Replace(s)
}

// Do sends a request. GET requests are retried on 429, 502/503/504 and
// transport errors; other methods only on 429 (the request was rejected before
// processing). Ambiguous failures of writes are reported, never retried.
func (c *Client) Do(ctx context.Context, token string, r Request) (*Response, error) {
	if err := ValidateToken(token); err != nil {
		return nil, err
	}
	if c.opts.DryRun && r.Method != http.MethodGet {
		return &Response{DryRun: true, Body: c.preview(r)}, nil
	}
	idempotent := r.Method == http.MethodGet
	for attempt := 0; ; attempt++ {
		if l := c.limiter(token); l != nil {
			if err := l.Wait(ctx); err != nil {
				return nil, err
			}
		}
		req, err := c.build(ctx, token, r)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			err = redactErr(err)
			if idempotent && attempt < 2 && ctx.Err() == nil {
				if serr := c.opts.Sleep(ctx, backoff(attempt, 0)); serr != nil {
					return nil, serr
				}
				continue
			}
			if !idempotent {
				return nil, &AmbiguousError{Method: r.Method, Path: r.Path, Err: err}
			}
			return nil, fmt.Errorf("sevDesk %s %s: %w", r.Method, r.Path, err)
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
		_ = resp.Body.Close()
		if rerr != nil {
			rerr = redactErr(rerr)
			if !idempotent {
				return nil, &AmbiguousError{Method: r.Method, Path: r.Path, Err: rerr}
			}
			return nil, fmt.Errorf("sevDesk %s %s: reading response: %w", r.Method, r.Path, rerr)
		}
		if len(data) > MaxResponseBytes {
			return nil, fmt.Errorf("sevDesk %s %s: response larger than %d MiB", r.Method, r.Path, MaxResponseBytes>>20)
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt < c.opts.MaxRetries:
			if serr := c.opts.Sleep(ctx, backoff(attempt, retryAfter(resp))); serr != nil {
				return nil, serr
			}
			continue
		case idempotent && attempt < 2 && (resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504):
			if serr := c.opts.Sleep(ctx, backoff(attempt, retryAfter(resp))); serr != nil {
				return nil, serr
			}
			continue
		case resp.StatusCode >= 300 && resp.StatusCode <= 399:
			return nil, &APIError{Method: r.Method, Path: r.Path, Status: resp.StatusCode,
				Body: "redirect not followed (the API token is only sent to the configured URL); check SEVDESK_BASE_URL"}
		case !idempotent && resp.StatusCode >= 500:
			// A 5xx (e.g. a gateway timeout) can arrive after the write was
			// committed: report it as ambiguous so nobody blindly retries.
			return nil, &AmbiguousError{Method: r.Method, Path: r.Path,
				Err: &APIError{Method: r.Method, Path: r.Path, Status: resp.StatusCode, Body: string(data)}}
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return nil, &APIError{Method: r.Method, Path: r.Path, Status: resp.StatusCode, Body: string(data)}
		}
		return &Response{Status: resp.StatusCode, Body: data}, nil
	}
}

// redactErr strips the URL from transport errors (net/http errors embed it).
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
			return min(time.Duration(n)*time.Second, maxRetryAfter)
		}
	}
	return 0
}

func backoff(attempt int, floor time.Duration) time.Duration {
	d := time.Duration(500*(1<<attempt)) * time.Millisecond
	d += time.Duration(rand.Int64N(int64(250 * time.Millisecond)))
	return min(max(d, floor), maxRetryAfter)
}

// preview describes a request that was not sent (dry run). It contains no credentials.
func (c *Client) preview(r Request) []byte {
	p := map[string]any{
		"dry_run": true,
		"note":    "SEVDESK_DRY_RUN=true: this request was NOT sent to sevDesk",
		"method":  r.Method,
		"url":     c.base + r.Path,
	}
	if len(r.Query) > 0 {
		p["query"] = r.Query
	}
	if r.Body != nil {
		p["body"] = r.Body
	}
	if r.File != nil {
		p["file"] = map[string]any{"field": r.File.Field, "filename": r.File.Filename, "bytes": len(r.File.Data)}
	}
	b, _ := json.Marshal(p)
	return b
}
