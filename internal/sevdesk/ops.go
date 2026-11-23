package sevdesk

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Class is the safety tier an operation belongs to.
type Class int

const (
	// ClassRead only reads data.
	ClassRead Class = iota
	// ClassWrite creates or changes data without finalising anything (drafts).
	ClassWrite
	// ClassBooking finalises or removes data: booking, sending, enshrining,
	// resetting status, deleting, payments.
	ClassBooking
)

func (c Class) String() string {
	switch c {
	case ClassRead:
		return "read"
	case ClassWrite:
		return "write"
	default:
		return "booking"
	}
}

// ParseClass converts the String form back.
func ParseClass(s string) (Class, bool) {
	switch strings.ToLower(s) {
	case "read":
		return ClassRead, true
	case "write":
		return ClassWrite, true
	case "booking":
		return ClassBooking, true
	}
	return 0, false
}

// Operation is one entry of the embedded operation index.
type Operation struct {
	Method     string   `json:"m"`
	Path       string   `json:"p"`
	ID         string   `json:"id"`
	Tag        string   `json:"t"`
	Summary    string   `json:"s"`
	Query      []string `json:"q"`
	HasBody    bool     `json:"b"`
	Multipart  bool     `json:"mp"`
	Supplement bool     `json:"-"` // not part of the official spec (see supplements)

	Class Class `json:"-"`
	segs  []string
}

//go:embed operations.json
var operationsJSON []byte

type indexFile struct {
	SpecVersion string      `json:"spec_version"`
	Operations  []Operation `json:"operations"`
}

// supplements are well-known read-only endpoints that the official OpenAPI
// spec does not describe but that are needed to fill in create requests
// (contact person, country, unit, categories, accounting types). They are
// flagged in the operation index; see README for the caveat. The first block was verified read-only against a
// live account, the DELETE entries too.
var supplements = []Operation{
	{Method: "GET", Path: "/SevUser", ID: "supplement.getSevUsers", Tag: "SevUser", Summary: "List users (ids for contactPerson)"},
	{Method: "GET", Path: "/SevUser/{id}", ID: "supplement.getSevUserById", Tag: "SevUser", Summary: "Find user by ID"},
	{Method: "GET", Path: "/Category", ID: "supplement.getCategories", Tag: "Category", Summary: "List categories (use objectType=Contact|ContactAddress|Part|...)", Query: []string{"objectType"}},
	{Method: "GET", Path: "/StaticCountry", ID: "supplement.getCountries", Tag: "StaticCountry", Summary: "List countries"},
	{Method: "GET", Path: "/Unity", ID: "supplement.getUnities", Tag: "Unity", Summary: "List units (Stk, h, ...)"},
	{Method: "GET", Path: "/AccountingType", ID: "supplement.getAccountingTypes", Tag: "AccountingType", Summary: "List accounting types / booking accounts (voucher positions)"},
	{Method: "GET", Path: "/AccountDatev", ID: "supplement.getAccountDatev", Tag: "AccountDatev", Summary: "List DATEV accounts (voucher positions, sevdesk 2.0)"},
	{Method: "GET", Path: "/PaymentMethod", ID: "supplement.getPaymentMethods", Tag: "PaymentMethod", Summary: "List payment methods"},
	{Method: "GET", Path: "/CostCentre", ID: "supplement.getCostCentres", Tag: "CostCentre", Summary: "List cost centres"},
	// Undocumented but working (verified against a live trial account): the official spec has no delete
	// for these. Classified like every DELETE: booking tier.
	{Method: "DELETE", Path: "/Invoice/{id}", ID: "supplement.deleteInvoice", Tag: "Invoice", Summary: "Delete an invoice (drafts; undocumented)"},
	{Method: "DELETE", Path: "/Part/{id}", ID: "supplement.deletePart", Tag: "Part", Summary: "Delete a part (undocumented)"},
	{Method: "DELETE", Path: "/Voucher/{id}", ID: "supplement.deleteVoucher", Tag: "Voucher", Summary: "Delete a voucher (undocumented)"},
}

// globalQuery are query parameters sevDesk accepts on (nearly) every list
// endpoint but that the spec does not list per operation.
var globalQuery = []string{"limit", "offset", "embed", "countAll", "depth"}

var (
	indexOnce sync.Once
	index     []*Operation
	specVer   string
	indexErr  error
)

func loadIndex() {
	var f indexFile
	if err := json.Unmarshal(operationsJSON, &f); err != nil {
		indexErr = fmt.Errorf("embedded operation index is corrupt: %w", err)
		return
	}
	specVer = f.SpecVersion
	for i := range f.Operations {
		o := f.Operations[i]
		o.Class = classify(&o)
		o.segs = strings.Split(strings.TrimPrefix(o.Path, "/"), "/")
		index = append(index, &o)
	}
	for i := range supplements {
		o := supplements[i]
		o.Supplement = true
		o.Class = classify(&o)
		o.segs = strings.Split(strings.TrimPrefix(o.Path, "/"), "/")
		index = append(index, &o)
	}
	sort.SliceStable(index, func(i, j int) bool {
		if index[i].Path != index[j].Path {
			return index[i].Path < index[j].Path
		}
		return index[i].Method < index[j].Method
	})
}

// Operations returns the operation index (official spec plus supplements).
func Operations() []*Operation {
	indexOnce.Do(loadIndex)
	return index
}

// SpecVersion is the version string of the embedded OpenAPI spec.
func SpecVersion() string {
	indexOnce.Do(loadIndex)
	return specVer
}

// IndexError reports a corrupt embedded index (never expected at runtime).
func IndexError() error {
	indexOnce.Do(loadIndex)
	return indexErr
}

// bookingSuffixes mark POST/PUT operations that finalise, send, book or
// reset a document.
var bookingSuffixes = []string{
	"/enshrine", "/bookAmount", "/sendBy", "/sendViaEmail", "/resetToOpen", "/resetToDraft",
	"/cancelInvoice", "/sendByWithRender", "/Factory/createInvoiceReminder",
}

func classify(o *Operation) Class {
	switch o.Method {
	case "GET":
		switch o.ID {
		case "sendCreditNoteByPrinting": // GET, but marks the document as sent
			return ClassBooking
		case "exportDatevCSV", "exportDatevXML", "exportDatevDepricated": // start jobs, may enshrine
			return ClassWrite
		}
		return ClassRead
	case "DELETE":
		return ClassBooking
	}
	for _, s := range bookingSuffixes {
		if strings.HasSuffix(o.Path, s) {
			return ClassBooking
		}
	}
	return ClassWrite
}

// exactTrue accepts only bool true or the exact string "true" (the form
// sevDesk is known to honour). Everything else counts as "not set".
func exactTrue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true"
	}
	return false
}

// clearlyFalse reports whether a flag value is unambiguously "off".
// Unknown types and any other value count as "on" (fail closed).
func clearlyFalse(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == "" || x == "false" || x == "0"
	case json.Number:
		return x.String() == "0"
	case float64:
		return x == 0
	case int:
		return x == 0
	}
	return false
}

// statusThreshold: statuses above it leave the draft state of a document.
var statusThreshold = map[string]float64{"Invoice": 100, "CreditNote": 100, "Order": 100, "Voucher": 50}

var docObjectKey = map[string]string{"Invoice": "invoice", "CreditNote": "creditNote", "Order": "order", "Voucher": "voucher"}

func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		var n json.Number = json.Number(strings.TrimSpace(x))
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// EffectiveClass raises the static class of an operation when query or body
// carry state-changing content: PDF fetches without preventSendBy=true mark
// documents as sent, enshrine flags, and document statuses beyond draft.
func EffectiveClass(o *Operation, query map[string]any, body map[string]any) Class {
	c := o.Class
	up := func(n Class) {
		if n > c {
			c = n
		}
	}
	// Query flags are judged on the exact value that is sent upstream and fail
	// closed: sevDesk marks a document as sent (and enshrines it) for anything
	// but a real "true" in preventSendBy (verified live: " true" and "2" send),
	// and any enshrine value that is not clearly false may enshrine.
	if o.Method == "GET" && strings.HasSuffix(o.Path, "/getPdf") && !exactTrue(query["preventSendBy"]) {
		up(ClassBooking)
	}
	for _, k := range []string{"enshrine", "enshrineDocuments"} {
		if v, ok := query[k]; ok && !clearlyFalse(v) {
			up(ClassBooking)
		}
	}
	if o.Method == "POST" || o.Method == "PUT" {
		if thr, ok := statusThreshold[o.Tag]; ok && body != nil {
			check := func(m map[string]any) {
				if m == nil {
					return
				}
				if s, ok := m["status"]; ok {
					if f, ok := numberOf(s); !ok || f > thr {
						up(ClassBooking)
					}
				}
				if e, ok := m["enshrined"]; ok && e != nil && e != "" && e != false {
					up(ClassBooking)
				}
			}
			check(body)
			if inner, ok := body[docObjectKey[o.Tag]].(map[string]any); ok {
				check(inner)
			}
		}
	}
	return c
}

var (
	idRe      = regexp.MustCompile(`^[0-9]{1,18}$`)
	segmentRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// ErrPath is returned for syntactically invalid or unknown paths.
var ErrPath = errors.New("invalid sevDesk path")

// CleanPath validates a caller supplied path and returns its segments.
// It rejects empty and dot segments, percent encoding, query/fragment
// characters, backslashes and anything outside [A-Za-z0-9_-].
func CleanPath(p string) ([]string, error) {
	if len(p) == 0 || len(p) > 256 || p[0] != '/' {
		return nil, fmt.Errorf("%w: must start with '/' (e.g. /Invoice/123/getPdf), max 256 chars", ErrPath)
	}
	segs := strings.Split(p[1:], "/")
	for _, s := range segs {
		switch {
		case s == "":
			return nil, fmt.Errorf("%w: empty segment (double or trailing slash)", ErrPath)
		case s == "." || s == "..":
			return nil, fmt.Errorf("%w: dot segments are not allowed", ErrPath)
		case !segmentRe.MatchString(s):
			return nil, fmt.Errorf("%w: segment %q contains characters outside A-Za-z0-9_-; pass query parameters in 'query', not in the path", ErrPath, s)
		}
	}
	return segs, nil
}

// Match finds the indexed operation for method + path. IDs must be numeric.
func Match(method, path string) (*Operation, error) {
	if err := IndexError(); err != nil {
		return nil, err
	}
	segs, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	pathKnown := false
	for _, o := range Operations() {
		if len(o.segs) != len(segs) {
			continue
		}
		ok := true
		for i, ts := range o.segs {
			if strings.HasPrefix(ts, "{") {
				if !idRe.MatchString(segs[i]) {
					ok = false
					break
				}
				continue
			}
			if ts != segs[i] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		pathKnown = true
		if o.Method == method {
			return o, nil
		}
	}
	if pathKnown {
		return nil, fmt.Errorf("%w: method %s is not defined for %s; use sevdesk_list_operations to see the allowed methods", ErrPath, method, path)
	}
	return nil, fmt.Errorf("%w: %s %s is not in the operation index (embedded OpenAPI spec %s); use sevdesk_list_operations to find the endpoint, IDs must be numeric", ErrPath, method, path, SpecVersion())
}

// AllowedQuery reports whether name may be sent as query parameter of o.
func (o *Operation) AllowedQuery(name string) bool {
	for _, q := range o.Query {
		if q == name {
			return true
		}
	}
	if o.Method == "GET" {
		for _, q := range globalQuery {
			if q == name {
				return true
			}
		}
	}
	return false
}

// BuildPath substitutes numeric IDs into a path template such as
// /Invoice/{id}/getPdf. Placeholders are filled in order.
func BuildPath(template string, ids ...int64) (string, error) {
	var b strings.Builder
	i := 0
	for _, seg := range strings.Split(strings.TrimPrefix(template, "/"), "/") {
		b.WriteByte('/')
		if strings.HasPrefix(seg, "{") {
			if i >= len(ids) || ids[i] <= 0 {
				return "", fmt.Errorf("%w: missing or non-positive id for %s", ErrPath, seg)
			}
			fmt.Fprintf(&b, "%d", ids[i])
			i++
			continue
		}
		b.WriteString(seg)
	}
	if i != len(ids) {
		return "", fmt.Errorf("%w: %d ids for %d placeholders in %s", ErrPath, len(ids), i, template)
	}
	return b.String(), nil
}
