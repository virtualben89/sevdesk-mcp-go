// Package tools registers the sevDesk MCP tools.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
)

// ExtraTokenKey is the TokenInfo.Extra key under which the HTTP layer hands
// a per-request sevDesk token (header X-Sevdesk-Token) to the tools.
const ExtraTokenKey = "sevdesk_token"

// Options controls tool registration and behaviour.
type Options struct {
	// DefaultToken is the server-side sevDesk token (may be empty when clients bring their own).
	DefaultToken string
	// AllowClientToken documents that a per-request token may arrive via ExtraTokenKey.
	AllowClientToken bool

	AllowWrite   bool
	AllowBooking bool
	DryRun       bool

	DefaultLimit     int
	UploadDir        string
	MaxUploadBytes   int64
	MaxDownloadBytes int64
	// Version is reported by get_status.
	Version string
}

// Pagination limits (also enforced in the schema).
const (
	maxLimit  = 100
	maxOffset = 1_000_000
	// maxOutputBytes bounds what one tool call returns to the model.
	maxOutputBytes = 512 << 10
)

// Registry adds tools to a server.
type Registry struct {
	srv    *mcp.Server
	client *sevdesk.Client
	opts   Options
	names  []string
}

// Register adds all tools allowed by the configured tiers and returns their names.
func Register(srv *mcp.Server, client *sevdesk.Client, opts Options) []string {
	if opts.DefaultLimit <= 0 {
		opts.DefaultLimit = 50
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 10 << 20
	}
	if opts.MaxDownloadBytes <= 0 {
		opts.MaxDownloadBytes = 8 << 20
	}
	if opts.AllowBooking && !opts.AllowWrite {
		opts.AllowBooking = false // booking is never available without write
	}
	r := &Registry{srv: srv, client: client, opts: opts}
	r.registerAll()
	return r.names
}

func ptr[T any](v T) *T { return &v }

// tierAllowed reports whether tools of class c are available.
func (r *Registry) tierAllowed(c sevdesk.Class) bool {
	switch c {
	case sevdesk.ClassWrite:
		return r.opts.AllowWrite
	case sevdesk.ClassBooking:
		return r.opts.AllowBooking
	}
	return true
}

type toolDef struct {
	name        string
	title       string
	description string
	class       sevdesk.Class
	enums       map[string][]any // property name -> allowed values
	destructive bool             // non-additive write (update, status change, delete)
	idempotent  bool
	// readOnlyHint overrides the annotation for tools whose class depends on the call (sevdesk_call).
	forceReadOnlyHint *bool
}

// add registers a typed tool when its tier is enabled.
func add[In any](r *Registry, d toolDef, h func(context.Context, In) (*mcp.CallToolResult, error)) {
	if !r.tierAllowed(d.class) {
		return
	}
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("schema for %s: %v", d.name, err))
	}
	sanitizeSchema(schema)
	for prop, vals := range d.enums {
		s, ok := schema.Properties[prop]
		if !ok {
			panic(fmt.Sprintf("tool %s: enum for unknown property %s", d.name, prop))
		}
		s.Enum = vals
	}
	// Bound numeric parameters in the schema as well (validated again in code).
	for prop, s := range schema.Properties {
		switch {
		case prop == "limit":
			s.Minimum, s.Maximum = ptr(1.0), ptr(float64(maxLimit))
		case prop == "offset":
			s.Minimum, s.Maximum = ptr(0.0), ptr(float64(maxOffset))
		case prop == "id" || strings.HasSuffix(prop, "_id"):
			if s.Type == "integer" {
				s.Minimum, s.Maximum = ptr(1.0), ptr(9e15)
			}
		}
	}
	// Deliberately no schema "default" values: go-sdk panics applying defaults
	// when a client sends "arguments": null. Defaults are applied in code.

	ann := &mcp.ToolAnnotations{Title: d.title, OpenWorldHint: ptr(false)}
	if d.class == sevdesk.ClassRead {
		ann.ReadOnlyHint = true
		ann.IdempotentHint = true
	} else {
		ann.DestructiveHint = ptr(d.destructive)
		ann.IdempotentHint = d.idempotent
	}
	if d.forceReadOnlyHint != nil {
		ann.ReadOnlyHint = *d.forceReadOnlyHint
		if !ann.ReadOnlyHint {
			ann.IdempotentHint = false
			ann.DestructiveHint = ptr(d.destructive)
		}
	}
	tool := &mcp.Tool{Name: d.name, Title: d.title, Description: d.description, InputSchema: schema, Annotations: ann}
	name := d.name
	mcp.AddTool(r.srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, _ any, _ error) {
		// A bug in one tool call must never take down the whole (shared) server.
		defer func() {
			if v := recover(); v != nil {
				slog.Error("tool panic", "tool", name, "panic", v, "stack", string(debug.Stack()))
				res = errorResult(fmt.Errorf("internal error in %s; please report this", name))
			}
		}()
		if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil {
			if t, ok := req.Extra.TokenInfo.Extra[ExtraTokenKey].(string); ok && t != "" {
				ctx = context.WithValue(ctx, ctxTokenKey{}, t)
			}
		}
		out, err := h(ctx, in)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return out, nil, nil
	})
	r.names = append(r.names, name)
}

type ctxTokenKey struct{}

// token resolves the sevDesk token for this call: the client's own token
// (only ever present when enabled and validated by the HTTP layer), else the
// server-side token.
func (r *Registry) token(ctx context.Context) (string, error) {
	if t, ok := ctx.Value(ctxTokenKey{}).(string); ok && t != "" {
		return t, nil
	}
	if r.opts.DefaultToken != "" {
		return r.opts.DefaultToken, nil
	}
	if r.opts.AllowClientToken {
		return "", errors.New("no sevDesk API token for this request: send your own token in the X-Sevdesk-Token header of the MCP connection")
	}
	return "", errors.New("no sevDesk API token configured (SEVDESK_API_TOKEN)")
}

// sanitizeSchema makes generated schemas friendly to a wide range of clients
// (e.g. Gemini / OpenAI function calling reject `"type": ["null","string"]`).
func sanitizeSchema(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if len(s.Types) > 0 {
		var nonNull []string
		for _, t := range s.Types {
			if t != "null" {
				nonNull = append(nonNull, t)
			}
		}
		if len(nonNull) == 1 {
			s.Type = nonNull[0]
			s.Types = nil
		}
	}
	for _, p := range s.Properties {
		sanitizeSchema(p)
	}
	sanitizeSchema(s.Items)
	sanitizeSchema(s.AdditionalProperties)
}

// decodeExact decodes JSON keeping numbers exact.
func decodeExact(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// valueResult returns compact JSON without HTML escaping.
func valueResult(v any) (*mcp.CallToolResult, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if buf.Len() > maxOutputBytes {
		return nil, fmt.Errorf("result is %d KiB, more than the %d KiB limit: narrow the query (filters, limit/offset) or request fewer objects", buf.Len()>>10, maxOutputBytes>>10)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimRight(buf.String(), "\n")}}}, nil
}

// ---- upstream helpers ----

func (r *Registry) do(ctx context.Context, req sevdesk.Request) (*sevdesk.Response, error) {
	tok, err := r.token(ctx)
	if err != nil {
		return nil, err
	}
	return r.client.Do(ctx, tok, req)
}

// getObject runs a GET and decodes the JSON envelope.
func (r *Registry) getJSON(ctx context.Context, path string, q url.Values) (map[string]any, error) {
	resp, err := r.do(ctx, sevdesk.Request{Method: "GET", Path: path, Query: q})
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := decodeExact(resp.Body, &m); err != nil {
		return nil, fmt.Errorf("sevDesk GET %s: response is not a JSON object (is SEVDESK_BASE_URL pointing to the sevDesk API?)", path)
	}
	return m, nil
}

// objectsOf returns the "objects" payload as a list.
func objectsOf(m map[string]any) []any {
	switch v := m["objects"].(type) {
	case []any:
		return v
	case map[string]any:
		return []any{v}
	}
	return nil
}

// send performs a write request and returns its decoded result (or the dry-run preview).
func (r *Registry) send(ctx context.Context, method, path string, body any) (*mcp.CallToolResult, error) {
	resp, err := r.do(ctx, sevdesk.Request{Method: method, Path: path, Body: body})
	if err != nil {
		return nil, err
	}
	return r.writeResult(resp)
}

func (r *Registry) writeResult(resp *sevdesk.Response) (*mcp.CallToolResult, error) {
	var m any
	if err := decodeExact(resp.Body, &m); err != nil {
		return valueResult(map[string]any{"status": resp.Status})
	}
	if resp.DryRun {
		return valueResult(m)
	}
	if mm, ok := m.(map[string]any); ok {
		if obj, ok := mm["objects"]; ok {
			return valueResult(map[string]any{"ok": true, "result": obj})
		}
	}
	return valueResult(map[string]any{"ok": true, "result": m})
}

// ---- pagination and slimming ----

type page struct{ limit, offset int }

func (r *Registry) page(limit, offset int) (page, error) {
	if limit == 0 {
		limit = r.opts.DefaultLimit
	}
	if limit < 1 || limit > maxLimit {
		return page{}, fmt.Errorf("limit must be between 1 and %d", maxLimit)
	}
	if offset < 0 || offset > maxOffset {
		return page{}, fmt.Errorf("offset must be between 0 and %d", maxOffset)
	}
	return page{limit, offset}, nil
}

func (p page) apply(q url.Values) {
	q.Set("limit", strconv.Itoa(p.limit))
	q.Set("offset", strconv.Itoa(p.offset))
}

// slimFields per entity: list tools return only these keys unless full=true.
var slimFields = map[string][]string{
	"contact":       {"id", "name", "surename", "familyname", "customerNumber", "status", "category", "vatNumber", "taxNumber", "exemptVat", "defaultTimeToPay", "parent", "update"},
	"address":       {"id", "contact", "street", "zip", "city", "country", "category", "name", "name2"},
	"way":           {"id", "contact", "type", "value", "key", "main"},
	"invoice":       {"id", "invoiceNumber", "header", "status", "invoiceType", "invoiceDate", "deliveryDate", "payDate", "sendDate", "contact", "sumNet", "sumTax", "sumGross", "currency", "paidAmount", "dunningLevel", "enshrined"},
	"credit_note":   {"id", "creditNoteNumber", "header", "status", "creditNoteDate", "deliveryDate", "contact", "sumNet", "sumTax", "sumGross", "currency", "enshrined"},
	"order":         {"id", "orderNumber", "header", "status", "orderType", "orderDate", "contact", "sumNet", "sumTax", "sumGross", "currency", "version"},
	"voucher":       {"id", "description", "status", "voucherDate", "payDate", "supplier", "supplierName", "creditDebit", "voucherType", "sumNet", "sumTax", "sumGross", "currency", "paidAmount", "enshrined", "taxRule"},
	"position":      {"id", "positionNumber", "name", "text", "quantity", "price", "priceNet", "priceGross", "taxRate", "discount", "unity", "part", "net", "sumNet", "sumGross", "accountingType", "accountDatev", "comment", "isAsset"},
	"part":          {"id", "partNumber", "name", "text", "price", "priceNet", "priceGross", "pricePurchase", "taxRate", "stock", "stockEnabled", "unity", "status"},
	"check_account": {"id", "name", "type", "currency", "status", "defaultAccount", "importType", "iban", "bankServer"},
	"transaction":   {"id", "valueDate", "entryDate", "amount", "paymtPurpose", "payeePayerName", "payeePayerAcctNo", "status", "checkAccount", "enshrined"},
}

func slimItem(item any, keep []string) any {
	m, ok := item.(map[string]any)
	if !ok || keep == nil {
		return item
	}
	out := make(map[string]any, len(keep))
	for _, k := range keep {
		if v, ok := m[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

// listResult builds the standard list envelope.
func listResult(items []any, p page, entity string, full bool, total any) (*mcp.CallToolResult, error) {
	if items == nil {
		items = []any{}
	}
	if !full {
		keep := slimFields[entity]
		for i := range items {
			items[i] = slimItem(items[i], keep)
		}
	}
	out := map[string]any{"items": items, "count": len(items), "limit": p.limit, "offset": p.offset}
	if len(items) >= p.limit {
		out["next_offset"] = p.offset + len(items)
	}
	if !full {
		out["slim"] = true
	}
	if total != nil {
		out["total"] = total
	}
	return valueResult(out)
}

// listCall fetches a collection with pagination and optional client-side filter.
func (r *Registry) listCall(ctx context.Context, path string, q url.Values, p page, entity string, full bool, keep func(map[string]any) bool) (*mcp.CallToolResult, error) {
	if q == nil {
		q = url.Values{}
	}
	p.apply(q)
	m, err := r.getJSON(ctx, path, q)
	if err != nil {
		return nil, err
	}
	items := objectsOf(m)
	if keep != nil {
		filtered := items[:0:0]
		for _, it := range items {
			if mm, ok := it.(map[string]any); ok && keep(mm) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	return listResult(items, p, entity, full, nil)
}

// getOne fetches one object by path.
func (r *Registry) getOne(ctx context.Context, path string) (map[string]any, error) {
	m, err := r.getJSON(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	objs := objectsOf(m)
	if len(objs) == 0 {
		return nil, fmt.Errorf("sevDesk GET %s: no object in response", path)
	}
	obj, ok := objs[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("sevDesk GET %s: unexpected response shape", path)
	}
	return obj, nil
}

// refID returns the id of an object reference such as {"id":"5","objectName":"Contact"}.
func refID(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	switch x := m["id"].(type) {
	case json.Number:
		return x.String()
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return ""
}

func ref(object string, id int64) map[string]any {
	return map[string]any{"id": id, "objectName": object}
}

func needID(name string, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%s is required and must be a positive integer (use the matching list_* tool to find it)", name)
	}
	return nil
}
