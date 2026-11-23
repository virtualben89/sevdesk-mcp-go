package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
)

func sortStrings(s []string) { sort.Strings(s) }

func jsonBytes(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func textResult(b []byte) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

func (r *Registry) registerAll() {
	r.registerRead()
	r.registerGeneric()
	r.registerWrite()
	r.registerBooking()
}

// CallInput are the parameters of sevdesk_call.
type CallInput struct {
	Method string         `json:"method" jsonschema:"HTTP method: GET, POST, PUT or DELETE"`
	Path   string         `json:"path" jsonschema:"API path below /api/v1 with numeric ids, e.g. /Invoice/123/getPdf (see sevdesk_list_operations)"`
	Query  map[string]any `json:"query,omitempty" jsonschema:"Query parameters (only those known for the operation, plus limit/offset/embed/countAll/depth on GET)"`
	Body   map[string]any `json:"body,omitempty" jsonschema:"JSON request body for POST/PUT, as described in the sevDesk API reference"`
}

// ListOperationsInput are the parameters of sevdesk_list_operations.
type ListOperationsInput struct {
	Search string `json:"search,omitempty" jsonschema:"Case-insensitive text matched against path, summary, operation id and tag"`
	Tag    string `json:"tag,omitempty" jsonschema:"Only operations of this tag, e.g. Invoice, Contact, Voucher, CheckAccount"`
	Method string `json:"method,omitempty" jsonschema:"Only this HTTP method"`
	Class  string `json:"class,omitempty" jsonschema:"Only this safety class: read, write or booking"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset int    `json:"offset,omitempty" jsonschema:"Number of items to skip"`
}

func (r *Registry) registerGeneric() {
	add(r, toolDef{name: "sevdesk_list_operations", title: "List sevDesk API operations", class: sevdesk.ClassRead,
		description: "Search the embedded index of sevDesk API operations (from the official OpenAPI spec) that sevdesk_call accepts. Each entry shows method, path template, summary, known query parameters, whether a JSON body is expected, its safety class (read, write, booking) and whether this server currently allows it. Entries marked supplement=true are well-known endpoints missing from the official spec."},
		r.listOperations)

	// The generic tool is registered in every mode; it enforces the tiers per call.
	readOnly := !r.opts.AllowWrite
	desc := "Call any indexed sevDesk API operation (see sevdesk_list_operations); the path is validated against the index and ids must be numeric. Operations are classified: read (GET), write (create/update drafts) and booking (book, send, enshrine, reset, delete); a class is only allowed when the operator enabled its tier, and document status changes beyond draft count as booking. Prefer the dedicated tools when one exists."
	if readOnly {
		desc += " This server is READ-ONLY: only GET operations work."
	}
	add(r, toolDef{name: "sevdesk_call", title: "Call sevDesk API", class: sevdesk.ClassRead, destructive: true,
		enums:             map[string][]any{"method": {"GET", "POST", "PUT", "DELETE"}},
		forceReadOnlyHint: ptr(readOnly), description: desc},
		r.call)
}

func (r *Registry) allowedNow(c sevdesk.Class) bool { return r.tierAllowed(c) }

func (r *Registry) listOperations(_ context.Context, in ListOperationsInput) (*mcp.CallToolResult, error) {
	if err := sevdesk.IndexError(); err != nil {
		return nil, err
	}
	p, err := r.page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	var wantClass *sevdesk.Class
	if in.Class != "" {
		c, ok := sevdesk.ParseClass(in.Class)
		if !ok {
			return nil, errors.New("class must be read, write or booking")
		}
		wantClass = &c
	}
	needle := strings.ToLower(strings.TrimSpace(in.Search))
	method := strings.ToUpper(in.Method)
	var items []any
	skipped := 0
	for _, o := range sevdesk.Operations() {
		if method != "" && o.Method != method {
			continue
		}
		if in.Tag != "" && !strings.EqualFold(o.Tag, in.Tag) {
			continue
		}
		if wantClass != nil && o.Class != *wantClass {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(o.Path+" "+o.Summary+" "+o.ID+" "+o.Tag), needle) {
			continue
		}
		if skipped < p.offset {
			skipped++
			continue
		}
		if len(items) >= p.limit {
			break
		}
		items = append(items, map[string]any{
			"method": o.Method, "path": o.Path, "summary": o.Summary, "tag": o.Tag, "operation_id": o.ID,
			"class": o.Class.String(), "allowed_now": r.allowedNow(o.Class), "query": o.Query,
			"json_body": o.HasBody, "supplement": o.Supplement, "multipart": o.Multipart,
		})
	}
	out := map[string]any{"items": nonNil(items), "count": len(items), "limit": p.limit, "offset": p.offset, "spec_version": sevdesk.SpecVersion()}
	if len(items) >= p.limit {
		out["next_offset"] = p.offset + len(items)
	}
	return valueResult(out)
}

func nonNil(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

// queryValues converts the model-supplied query map to url.Values.
func queryValues(op *sevdesk.Operation, in map[string]any) (url.Values, error) {
	q := url.Values{}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !op.AllowedQuery(k) {
			allowed := append([]string{}, op.Query...)
			if op.Method == "GET" {
				allowed = append(allowed, "limit", "offset", "embed", "countAll", "depth")
			}
			return nil, fmt.Errorf("query parameter %q is not known for %s %s (allowed: %s)", k, op.Method, op.Path, strings.Join(allowed, ", "))
		}
		var vals []string
		switch v := in[k].(type) {
		case string:
			vals = []string{v}
		case bool:
			vals = []string{strconv.FormatBool(v)}
		case float64:
			vals = []string{strconv.FormatFloat(v, 'f', -1, 64)}
		case json.Number:
			vals = []string{v.String()}
		case []any:
			var parts []string
			for _, e := range v {
				switch s := e.(type) {
				case string:
					parts = append(parts, s)
				case float64:
					parts = append(parts, strconv.FormatFloat(s, 'f', -1, 64))
				case bool:
					parts = append(parts, strconv.FormatBool(s))
				default:
					return nil, fmt.Errorf("query parameter %q: array items must be strings, numbers or booleans", k)
				}
			}
			vals = []string{strings.Join(parts, ",")}
		case nil:
			continue
		default:
			return nil, fmt.Errorf("query parameter %q must be a string, number, boolean or array of those", k)
		}
		for _, s := range vals {
			if len(s) > 1024 {
				return nil, fmt.Errorf("query parameter %q is too long", k)
			}
			q.Add(k, s)
		}
	}
	return q, nil
}

func (r *Registry) call(ctx context.Context, in CallInput) (*mcp.CallToolResult, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	switch method {
	case "GET", "POST", "PUT", "DELETE":
	default:
		return nil, errors.New("method must be GET, POST, PUT or DELETE")
	}
	op, err := sevdesk.Match(method, in.Path)
	if err != nil {
		return nil, err
	}
	if op.Multipart {
		return nil, errors.New("this operation uploads a file: use upload_voucher_file instead")
	}
	q, err := queryValues(op, in.Query)
	if err != nil {
		return nil, err
	}
	if len(in.Body) > 0 && !op.HasBody {
		return nil, fmt.Errorf("%s %s takes no JSON body", op.Method, op.Path)
	}
	if len(in.Body) > 0 {
		if b, err := json.Marshal(in.Body); err != nil || len(b) > 1<<20 {
			return nil, errors.New("body is invalid or larger than 1 MiB")
		}
	}
	// Class check on the effective content (query flags, document status in the body).
	qm := make(map[string]any, len(in.Query))
	for k, v := range in.Query {
		qm[k] = v
	}
	class := sevdesk.EffectiveClass(op, qm, in.Body)
	if !r.tierAllowed(class) {
		return nil, tierError(op, class)
	}
	// Collection endpoints return everything without a limit: bound them.
	autoLimit := false
	if op.Method == "GET" && strings.Count(op.Path, "/") == 1 && q.Get("limit") == "" && op.AllowedQuery("limit") {
		q.Set("limit", strconv.Itoa(r.opts.DefaultLimit))
		autoLimit = true
	}
	var body any
	if len(in.Body) > 0 {
		body = in.Body
	} else if op.HasBody && op.Method != "GET" {
		body = map[string]any{}
	}
	resp, err := r.do(ctx, sevdesk.Request{Method: op.Method, Path: in.Path, Query: q, Body: body})
	if err != nil {
		return nil, err
	}
	var v any
	if err := decodeExact(resp.Body, &v); err != nil {
		return valueResult(map[string]any{"status": resp.Status, "text": truncate(string(resp.Body), 4000)})
	}
	out := map[string]any{"class": class.String(), "result": v}
	if resp.DryRun {
		return valueResult(v)
	}
	if autoLimit {
		out["note"] = fmt.Sprintf("limit=%d was added automatically; pass limit/offset to page", r.opts.DefaultLimit)
	}
	return valueResult(out)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func tierError(op *sevdesk.Operation, c sevdesk.Class) error {
	switch c {
	case sevdesk.ClassWrite:
		return fmt.Errorf("%s %s is a write operation, but this server is read-only. The operator must set SEVDESK_READ_ONLY=false to allow it", op.Method, op.Path)
	default:
		return fmt.Errorf("%s %s is a booking-class operation (finalises, sends, books, resets or deletes). The operator must enable it with SEVDESK_READ_ONLY=false, SEVDESK_ALLOW_WRITE=true and SEVDESK_ALLOW_BOOKING=true; a status beyond draft in the body or flags like enshrine / getPdf without preventSendBy=true count as booking", op.Method, op.Path)
	}
}

var _ = http.MethodGet
