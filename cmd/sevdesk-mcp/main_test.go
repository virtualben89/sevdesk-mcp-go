package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/virtualben89/sevdesk-mcp-go/internal/config"
	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
	"github.com/virtualben89/sevdesk-mcp-go/internal/tools"
)

const (
	mcpToken    = "mcp-bearer-token-0123456789"
	serverToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	clientTokA  = "1111111111111111111111111111111a"
	clientTokB  = "2222222222222222222222222222222b"
)

type upstream struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
	fail  bool
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.auths = append(u.auths, r.Header.Get("Authorization"))
		fail := u.fail
		u.mu.Unlock()
		if fail {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"objects":[]}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) lastAuth() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.auths) == 0 {
		return ""
	}
	return u.auths[len(u.auths)-1]
}

func (u *upstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.auths)
}

func testConfig(up *upstream) *config.Config {
	return &config.Config{
		Transport: "http", HTTPPath: "/mcp", HTTPEnableSSE: true, HTTPSessionTimeout: time.Minute,
		AuthTokens: []string{mcpToken}, BaseURL: up.URL + "/api/v1", APIToken: serverToken,
		Timeout: 5 * time.Second, DefaultLimit: 50, ReadOnly: true, MaxUploadBytes: 1 << 20, MaxDownloadBytes: 1 << 20,
	}
}

func startServer(t *testing.T, cfg *config.Config) *httptest.Server {
	t.Helper()
	client, err := sevdesk.New(sevdesk.Options{BaseURL: cfg.BaseURL, Timeout: cfg.Timeout})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, _ := NewServer(client, toolOptions(cfg), log)
	ts := httptest.NewServer(buildHandler(cfg, server, client, log))
	t.Cleanup(ts.Close)
	return ts
}

type headerRT map[string]string

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func raw(t *testing.T, method, url string, hdr map[string]string, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func connectHTTP(t *testing.T, ts *httptest.Server, hdr map[string]string) (*mcp.ClientSession, error) {
	t.Helper()
	tr := &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: headerRT(hdr)}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(context.Background(), tr, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestAuthRequiredAndHealth(t *testing.T) {
	up := newUpstream(t)
	ts := startServer(t, testConfig(up))
	for _, p := range []string{"/mcp", "/sse"} {
		if resp, _ := raw(t, "POST", ts.URL+p, nil, "{}"); resp.StatusCode != 401 {
			t.Errorf("%s without token: %d", p, resp.StatusCode)
		}
		if resp, _ := raw(t, "POST", ts.URL+p, map[string]string{"Authorization": "Bearer wrong-token-0123456789"}, "{}"); resp.StatusCode != 401 {
			t.Errorf("%s wrong token: %d", p, resp.StatusCode)
		}
	}
	if resp, body := raw(t, "GET", ts.URL+"/healthz", nil, ""); resp.StatusCode != 200 || !strings.Contains(body, "ok") {
		t.Errorf("healthz: %d %s", resp.StatusCode, body)
	}
	if resp, body := raw(t, "GET", ts.URL+"/readyz", nil, ""); resp.StatusCode != 200 || !strings.Contains(body, "ok") {
		t.Errorf("readyz: %d %s", resp.StatusCode, body)
	}
	up.mu.Lock()
	up.fail = true
	up.mu.Unlock()
	readyCacheTTL = 0
	t.Cleanup(func() { readyCacheTTL = 5 * time.Second })
	resp, body := raw(t, "GET", ts.URL+"/readyz", nil, "")
	if resp.StatusCode != 503 || strings.Contains(body, serverToken) || strings.Contains(body, up.URL) {
		t.Errorf("readyz with failing upstream: %d %s", resp.StatusCode, body)
	}
}

func TestStreamableWithServerToken(t *testing.T) {
	up := newUpstream(t)
	ts := startServer(t, testConfig(up))
	cs, err := connectHTTP(t, ts, map[string]string{"Authorization": "Bearer " + mcpToken})
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callTool(t, cs, "list_contacts", nil); isErr {
		t.Fatal(out)
	}
	if up.lastAuth() != serverToken {
		t.Errorf("upstream saw %q, want server token", up.lastAuth())
	}
}

func TestClientTokenDisabledRejectsHeader(t *testing.T) {
	up := newUpstream(t)
	ts := startServer(t, testConfig(up))
	resp, body := raw(t, "POST", ts.URL+"/mcp", map[string]string{"Authorization": "Bearer " + mcpToken, config.ClientTokenHeader: clientTokA, "Content-Type": "application/json", "Accept": "application/json, text/event-stream"}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.StatusCode != 400 || !strings.Contains(body, "SEVDESK_ALLOW_CLIENT_TOKEN") || strings.Contains(body, clientTokA) {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
	// without the bearer the header must not even be looked at: still 401
	if resp, _ := raw(t, "POST", ts.URL+"/mcp", map[string]string{config.ClientTokenHeader: clientTokA}, "{}"); resp.StatusCode != 401 {
		t.Errorf("unauthenticated request with header: %d", resp.StatusCode)
	}
	if up.calls() != 0 {
		t.Error("upstream was contacted")
	}
}

func TestClientTokenMode(t *testing.T) {
	up := newUpstream(t)
	cfg := testConfig(up)
	cfg.AllowClientToken = true
	ts := startServer(t, cfg)
	bearer := map[string]string{"Authorization": "Bearer " + mcpToken}

	// own token wins over the server's
	cs, err := connectHTTP(t, ts, map[string]string{"Authorization": "Bearer " + mcpToken, config.ClientTokenHeader: clientTokA})
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callTool(t, cs, "list_contacts", nil); isErr {
		t.Fatal(out)
	}
	if up.lastAuth() != clientTokA {
		t.Errorf("upstream saw %q, want client token A", up.lastAuth())
	}
	// two clients, two tenants, same server
	cs2, err := connectHTTP(t, ts, map[string]string{"Authorization": "Bearer " + mcpToken, config.ClientTokenHeader: clientTokB})
	if err != nil {
		t.Fatal(err)
	}
	callTool(t, cs2, "list_contacts", nil)
	if up.lastAuth() != clientTokB {
		t.Errorf("upstream saw %q, want client token B", up.lastAuth())
	}
	// without header: server token
	cs3, _ := connectHTTP(t, ts, bearer)
	callTool(t, cs3, "list_contacts", nil)
	if up.lastAuth() != serverToken {
		t.Errorf("upstream saw %q, want server token", up.lastAuth())
	}
	// malformed or duplicate header: refused, never silently ignored
	for name, h := range map[string]map[string]string{
		"short":  {config.ClientTokenHeader: "abc"},
		"prefix": {config.ClientTokenHeader: "Bearer " + clientTokA},
	} {
		n := up.calls()
		hdr := map[string]string{"Authorization": "Bearer " + mcpToken, "Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
		for k, v := range h {
			hdr[k] = v
		}
		resp, body := raw(t, "POST", ts.URL+"/mcp", hdr, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
		if resp.StatusCode != 400 || strings.Contains(body, clientTokA) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
		if up.calls() != n {
			t.Errorf("%s: upstream contacted", name)
		}
	}
}

func TestClientTokenOnlyServerWithoutDefault(t *testing.T) {
	up := newUpstream(t)
	cfg := testConfig(up)
	cfg.AllowClientToken = true
	cfg.APIToken = ""
	ts := startServer(t, cfg)
	cs, _ := connectHTTP(t, ts, map[string]string{"Authorization": "Bearer " + mcpToken})
	out, isErr := callTool(t, cs, "list_contacts", nil)
	if !isErr || !strings.Contains(out, "X-Sevdesk-Token") || up.calls() != 0 {
		t.Errorf("%v %s calls=%d", isErr, out, up.calls())
	}
	if resp, _ := raw(t, "GET", ts.URL+"/readyz", nil, ""); resp.StatusCode != 200 {
		t.Errorf("readyz without server token: %d", resp.StatusCode)
	}
}

func TestStreamableSessionBoundToBothTokens(t *testing.T) {
	up := newUpstream(t)
	cfg := testConfig(up)
	cfg.AllowClientToken = true
	ts := startServer(t, cfg)
	h := func(sd string, extra map[string]string) map[string]string {
		m := map[string]string{"Authorization": "Bearer " + mcpToken, "Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
		if sd != "" {
			m[config.ClientTokenHeader] = sd
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	resp, _ := raw(t, "POST", ts.URL+"/mcp", h(clientTokA, nil), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`)
	sid := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != 200 || sid == "" {
		t.Fatalf("init: %d sid=%q", resp.StatusCode, sid)
	}
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	if resp, body := raw(t, "POST", ts.URL+"/mcp", h(clientTokB, map[string]string{"Mcp-Session-Id": sid}), list); resp.StatusCode != 403 {
		t.Errorf("switching sevDesk token inside a session: %d %s", resp.StatusCode, body)
	}
	if resp, body := raw(t, "POST", ts.URL+"/mcp", h("", map[string]string{"Mcp-Session-Id": sid}), list); resp.StatusCode != 403 {
		t.Errorf("dropping the sevDesk token inside a session: %d %s", resp.StatusCode, body)
	}
	if resp, body := raw(t, "POST", ts.URL+"/mcp", h(clientTokA, map[string]string{"Mcp-Session-Id": sid, "Mcp-Protocol-Version": "2025-06-18"}), list); resp.StatusCode != 200 {
		t.Errorf("same tokens must keep working: %d %s", resp.StatusCode, body)
	}
}

func TestSSEBoundToTokens(t *testing.T) {
	up := newUpstream(t)
	cfg := testConfig(up)
	otherToken := "other-mcp-token-0123456789"
	cfg.AuthTokens = []string{mcpToken, otherToken}
	ts := startServer(t, cfg)
	req, _ := http.NewRequest("GET", ts.URL+"/sse", nil)
	req.Header.Set("Authorization", "Bearer "+mcpToken)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	endpoint := ""
	for sc.Scan() {
		if l := sc.Text(); strings.HasPrefix(l, "data: ") {
			endpoint = strings.TrimPrefix(l, "data: ")
			break
		}
	}
	if endpoint == "" {
		t.Fatal("no endpoint event")
	}
	post := func(tok string) int {
		hdr := map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/json"}
		r, _ := raw(t, "POST", ts.URL+endpoint, hdr, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`)
		return r.StatusCode
	}
	if c := post(mcpToken); c != 202 {
		t.Fatalf("first post: %d", c)
	}
	if c := post(otherToken); c != 403 {
		t.Errorf("MCP token switch in SSE session: %d", c)
	}
}

// Legacy SSE cannot carry per-client sevDesk tokens to tool calls (audit
// finding): with client tokens enabled it must not be served at all, so a
// client token is never silently replaced by the server token.
func TestSSEDisabledWithClientTokens(t *testing.T) {
	up := newUpstream(t)
	cfg := testConfig(up)
	cfg.AllowClientToken = true
	ts := startServer(t, cfg)
	resp, _ := raw(t, "GET", ts.URL+"/sse", map[string]string{"Authorization": "Bearer " + mcpToken, config.ClientTokenHeader: clientTokA}, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/sse with client tokens enabled: %d, want 404", resp.StatusCode)
	}
	// Streamable still delivers the client token upstream.
	cs, err := connectHTTP(t, ts, map[string]string{"Authorization": "Bearer " + mcpToken, config.ClientTokenHeader: clientTokA})
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callTool(t, cs, "list_contacts", nil); isErr {
		t.Fatal(out)
	}
	if got := up.lastAuth(); got != clientTokA {
		t.Errorf("upstream got %q, want the client token", got)
	}
}

func TestNoAuthModeHostGuard(t *testing.T) {
	up := newUpstream(t)
	cfg := testConfig(up)
	cfg.AuthTokens = nil
	ts := startServer(t, cfg)
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader("{}"))
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("foreign Host in no-auth mode: %d", resp.StatusCode)
	}
}

func TestBearerVerifierUserID(t *testing.T) {
	v := bearerVerifier([]string{mcpToken, "second-token-0123456789"}, true)
	mk := func(sd string) *http.Request {
		r := httptest.NewRequest("POST", "/mcp", nil)
		if sd != "" {
			r.Header.Set(config.ClientTokenHeader, sd)
		}
		return r
	}
	a, err := v(context.Background(), mcpToken, mk(clientTokA))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := v(context.Background(), mcpToken, mk(clientTokB))
	c, _ := v(context.Background(), "second-token-0123456789", mk(clientTokA))
	d, _ := v(context.Background(), mcpToken, mk(""))
	ids := map[string]bool{a.UserID: true, b.UserID: true, c.UserID: true, d.UserID: true}
	if len(ids) != 4 {
		t.Errorf("user ids must differ per (mcp token, sevdesk token): %v", ids)
	}
	for id := range ids {
		if strings.Contains(id, mcpToken) || strings.Contains(id, clientTokA) {
			t.Errorf("user id leaks a token: %s", id)
		}
	}
	if a.Extra[tools.ExtraTokenKey] != clientTokA || d.Extra != nil {
		t.Errorf("extra: %v / %v", a.Extra, d.Extra)
	}
	if _, err := v(context.Background(), "wrong", mk("")); err == nil {
		t.Error("wrong bearer accepted")
	}
	off := bearerVerifier([]string{mcpToken}, false)
	if info, _ := off(context.Background(), mcpToken, mk(clientTokA)); info.Extra != nil {
		t.Error("client token passed on although disabled")
	}
}

func TestHealthcheckSubcommandLogic(t *testing.T) {
	t.Setenv("MCP_TRANSPORT", "stdio")
	if healthcheck() != 0 {
		t.Error("stdio healthcheck must succeed")
	}
	up := newUpstream(t)
	ts := startServer(t, testConfig(up))
	t.Setenv("MCP_TRANSPORT", "http")
	t.Setenv("MCP_HTTP_ADDR", strings.TrimPrefix(ts.URL, "http://"))
	if healthcheck() != 0 {
		t.Error("healthcheck against running server failed")
	}
	t.Setenv("MCP_HTTP_ADDR", "127.0.0.1:1")
	if healthcheck() == 0 {
		t.Error("healthcheck against closed port succeeded")
	}
}
