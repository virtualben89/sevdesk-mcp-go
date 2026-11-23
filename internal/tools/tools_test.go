package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
)

const testToken = "0123456789abcdef0123456789abcdef"

// recorded is one request seen by the fake sevDesk.
type recorded struct {
	Method, Path, RawQuery, Auth, Body string
}

type fakeSevdesk struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
	// handler decides the response; default returns {"objects":[]}.
	handler func(r *http.Request, body string) (int, string)
}

func newFake(t *testing.T) *fakeSevdesk {
	t.Helper()
	f := &fakeSevdesk{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		f.mu.Unlock()
		code, out := 200, `{"objects":[]}`
		if f.handler != nil {
			code, out = f.handler(r, string(b))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSevdesk) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return recorded{}
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeSevdesk) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func connect(t *testing.T, f *fakeSevdesk, opts Options, dry bool) *mcp.ClientSession {
	t.Helper()
	client, err := sevdesk.New(sevdesk.Options{BaseURL: f.URL + "/api/v1", DryRun: dry, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if opts.DefaultToken == "" {
		opts.DefaultToken = testToken
	}
	opts.DryRun = dry
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	Register(srv, client, opts)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

var (
	readOpts    = Options{}
	writeOpts   = Options{AllowWrite: true}
	bookingOpts = Options{AllowWrite: true, AllowBooking: true}
)

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		m[tool.Name] = tool
	}
	return m
}

func TestTierVisibility(t *testing.T) {
	f := newFake(t)
	ro := toolNames(t, connect(t, f, readOpts, false))
	wr := toolNames(t, connect(t, f, writeOpts, false))
	bk := toolNames(t, connect(t, f, bookingOpts, false))
	for _, n := range []string{"create_invoice", "create_contact", "upload_voucher_file", "book_payment", "document_action", "send_document_email", "delete_object"} {
		if ro[n] != nil {
			t.Errorf("read-only mode exposes %s", n)
		}
	}
	for _, n := range []string{"list_invoices", "get_status", "sevdesk_call", "sevdesk_list_operations", "download_document_pdf"} {
		if ro[n] == nil || !ro[n].Annotations.ReadOnlyHint && n != "sevdesk_call" {
			t.Errorf("read-only mode lacks read tool %s", n)
		}
	}
	if !ro["sevdesk_call"].Annotations.ReadOnlyHint {
		t.Error("sevdesk_call must be readOnlyHint in read-only mode")
	}
	for _, n := range []string{"create_invoice", "create_voucher", "update_contact"} {
		if wr[n] == nil {
			t.Errorf("write mode lacks %s", n)
		}
	}
	for _, n := range []string{"book_payment", "document_action", "send_document_email", "delete_object"} {
		if wr[n] != nil {
			t.Errorf("write mode (no booking) exposes %s", n)
		}
		if bk[n] == nil {
			t.Errorf("booking mode lacks %s", n)
		}
	}
	if wr["sevdesk_call"].Annotations.ReadOnlyHint {
		t.Error("sevdesk_call must not be readOnlyHint when writes are enabled")
	}
	if len(ro) < 25 || len(bk) < 40 {
		t.Errorf("unexpected tool counts: ro=%d booking=%d", len(ro), len(bk))
	}
}

func TestSchemasPortable(t *testing.T) {
	f := newFake(t)
	for name, tool := range toolNames(t, connect(t, f, bookingOpts, false)) {
		b, _ := json.Marshal(tool.InputSchema)
		s := string(b)
		if strings.Contains(s, "$ref") {
			t.Errorf("%s: schema contains $ref: %s", name, s)
		}
		if strings.Contains(s, `"type":[`) {
			t.Errorf("%s: schema contains a type array: %s", name, s)
		}
		if strings.Contains(s, `"default"`) {
			t.Errorf("%s: schema contains default: %s", name, s)
		}
		if tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s: annotations missing or openWorld", name)
		}
		if tool.Description == "" || tool.Title == "" {
			t.Errorf("%s: missing description/title", name)
		}
	}
}

// Every tool must survive "arguments": null (go-sdk panics on schema defaults).
func TestNilArgsEveryTool(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, bookingOpts, false)
	names := toolNames(t, cs)
	for name := range names {
		out, _ := call(t, cs, name, nil)
		if strings.Contains(out, "internal error") {
			t.Errorf("%s: panic recovered on nil args: %s", name, out)
		}
	}
	// The session must still work afterwards.
	if _, isErr := call(t, cs, "get_status", map[string]any{"check": false}); isErr {
		t.Error("get_status failed after nil-args calls")
	}
}

func TestReadToolsNeverWrite(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, readOpts, false)
	for name := range toolNames(t, cs) {
		call(t, cs, name, map[string]any{"id": 1, "document_type": "invoice", "contact_id": 1, "method": "GET", "path": "/Invoice"})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if r.Method != "GET" {
			t.Errorf("read-only mode sent %s %s", r.Method, r.Path)
		}
	}
}

func TestListSlimAndPagination(t *testing.T) {
	f := newFake(t)
	f.handler = func(r *http.Request, _ string) (int, string) {
		return 200, `{"objects":[{"id":"1","objectName":"Invoice","invoiceNumber":"RE-1","status":"200","sumGross":"119.00","contact":{"id":"5","objectName":"Contact"},"sevClient":{"id":"1"},"hugeNoise":"x"},` +
			`{"id":"2","invoiceNumber":"RE-2","status":"100"}]}`
	}
	cs := connect(t, f, readOpts, false)
	out, isErr := call(t, cs, "list_invoices", map[string]any{"limit": 2, "start_date": "2026-01-01", "end_date": "2026-01-31", "contact_id": 5, "status": 200})
	if isErr {
		t.Fatal(out)
	}
	var res struct {
		Items      []map[string]any `json:"items"`
		Count      int              `json:"count"`
		NextOffset int              `json:"next_offset"`
		Slim       bool             `json:"slim"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Count != 2 || res.NextOffset != 2 || !res.Slim {
		t.Errorf("unexpected envelope: %s", out)
	}
	if _, ok := res.Items[0]["hugeNoise"]; ok {
		t.Error("slim view still has noisy fields")
	}
	req := f.last()
	for _, want := range []string{"limit=2", "offset=0", "contact%5Bid%5D=5", "contact%5BobjectName%5D=Contact", "status=200", "startDate=", "endDate="} {
		if !strings.Contains(req.RawQuery, want) {
			t.Errorf("query %q lacks %s", req.RawQuery, want)
		}
	}
	if req.Auth != testToken {
		t.Errorf("Authorization must be the raw token, got %q", req.Auth)
	}
	full, _ := call(t, cs, "list_invoices", map[string]any{"full": true})
	if !strings.Contains(full, "hugeNoise") {
		t.Error("full=true must return everything")
	}
	for _, bad := range []map[string]any{{"limit": 101}, {"limit": -1}, {"offset": -5}, {"start_date": "yesterday"}} {
		if out, isErr := call(t, cs, "list_invoices", bad); !isErr {
			t.Errorf("%v accepted: %s", bad, out)
		}
	}
}

func TestTransactionDirectionMapping(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, readOpts, false)
	for dir, want := range map[string]string{"incoming": "onlyDebit=true", "outgoing": "onlyCredit=true"} {
		if out, isErr := call(t, cs, "list_transactions", map[string]any{"direction": dir, "is_booked": false, "check_account_id": 3}); isErr {
			t.Fatal(out)
		}
		q := f.last().RawQuery
		if !strings.Contains(q, want) || !strings.Contains(q, "isBooked=false") || !strings.Contains(q, "checkAccount%5Bid%5D=3") {
			t.Errorf("%s: query %q lacks %s", dir, q, want)
		}
	}
	if _, isErr := call(t, cs, "list_transactions", map[string]any{"direction": "sideways"}); !isErr {
		t.Error("bad direction accepted")
	}
}

func TestEndDateIsEndOfDay(t *testing.T) {
	s, _ := unixStart("2026-01-31")
	e, _ := unixEnd("2026-01-31")
	if s == e || e <= s {
		t.Fatalf("end %s must be after start %s", e, s)
	}
	if _, err := germanDate("2026-02-30"); err == nil {
		t.Error("invalid date accepted")
	}
	if d, _ := germanDate("2026-03-05"); d != "05.03.2026" {
		t.Errorf("germanDate = %s", d)
	}
}

func TestGetContactUnwrapsAndIncludes(t *testing.T) {
	f := newFake(t)
	f.handler = func(r *http.Request, _ string) (int, string) {
		switch r.URL.Path {
		case "/api/v1/Contact/7":
			return 200, `{"objects":[{"id":"7","name":"ACME"}]}`
		case "/api/v1/ContactAddress":
			return 200, `{"objects":[{"id":"1","contact":{"id":"7"}},{"id":"2","contact":{"id":"99"}}]}`
		case "/api/v1/CommunicationWay":
			return 200, `{"objects":[{"id":"3","contact":{"id":"7"},"value":"a@b.de"}]}`
		}
		return 404, `{"error":{"message":"nope"}}`
	}
	cs := connect(t, f, readOpts, false)
	out, isErr := call(t, cs, "get_contact", map[string]any{"id": 7, "include_addresses": true, "include_communication_ways": true})
	if isErr {
		t.Fatal(out)
	}
	if !strings.Contains(out, `"name":"ACME"`) || !strings.Contains(out, `"a@b.de"`) {
		t.Errorf("missing data: %s", out)
	}
	if strings.Contains(out, `"id":"2"`) {
		t.Errorf("address of another contact leaked: %s", out)
	}
	out, isErr = call(t, cs, "get_contact", map[string]any{"id": 8})
	if !isErr || !strings.Contains(out, "HTTP 404") || !strings.Contains(out, "list_") {
		t.Errorf("expected actionable 404 error, got %q", out)
	}
}

func TestPDFDownload(t *testing.T) {
	f := newFake(t)
	f.handler = func(r *http.Request, _ string) (int, string) {
		return 200, `{"objects":{"filename":"RE-1.pdf","mimetype":"application/pdf","base64Encoded":true,"content":"JVBERi0xLjQK"}}`
	}
	cs := connect(t, f, readOpts, false)
	out, isErr := call(t, cs, "download_document_pdf", map[string]any{"document_type": "invoice", "id": 3})
	if isErr || !strings.Contains(out, `"content_base64":"JVBERi0xLjQK"`) || !strings.Contains(out, `"size_bytes":9`) {
		t.Fatalf("%v %s", isErr, out)
	}
	if q := f.last().RawQuery; q != "preventSendBy=true" {
		t.Errorf("query must be exactly preventSendBy=true (download=true switches sevDesk to raw bytes): %s", q)
	}
	if _, isErr := call(t, cs, "download_document_pdf", map[string]any{"document_type": "voucher", "id": 3}); !isErr {
		t.Error("voucher PDF should be rejected")
	}
	cs2 := connect(t, f, Options{MaxDownloadBytes: 4}, false)
	if out, isErr := call(t, cs2, "download_document_pdf", map[string]any{"document_type": "invoice", "id": 3}); !isErr || !strings.Contains(out, "MAX_DOWNLOAD") {
		t.Errorf("download limit not enforced: %s", out)
	}
}

func TestMissingTokenIsActionable(t *testing.T) {
	f := newFake(t)
	client, _ := sevdesk.New(sevdesk.Options{BaseURL: f.URL})
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	Register(srv, client, Options{AllowClientToken: true})
	ct, st := mcp.NewInMemoryTransports()
	_, _ = srv.Connect(context.Background(), st, nil)
	cs, _ := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	defer cs.Close()
	out, isErr := call(t, cs, "list_contacts", nil)
	if !isErr || !strings.Contains(out, "X-Sevdesk-Token") {
		t.Errorf("got %q", out)
	}
	if f.count() != 0 {
		t.Error("request sent without token")
	}
}
