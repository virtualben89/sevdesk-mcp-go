package sevdesk

import (
	"strings"
	"testing"
)

func TestIndexIntegrity(t *testing.T) {
	if err := IndexError(); err != nil {
		t.Fatal(err)
	}
	ops := Operations()
	if len(ops) < 150 {
		t.Fatalf("only %d operations indexed", len(ops))
	}
	if SpecVersion() == "" {
		t.Error("spec version missing")
	}
	seen := map[string]bool{}
	for _, o := range ops {
		k := o.Method + " " + o.Path
		if seen[k] {
			t.Errorf("duplicate %s", k)
		}
		seen[k] = true
		if !strings.HasPrefix(o.Path, "/") || o.Method == "" {
			t.Errorf("bad op %+v", o)
		}
		if _, err := CleanPath(strings.NewReplacer("{", "", "}", "").Replace(o.Path)); err != nil {
			t.Errorf("indexed path %s fails own validation: %v", o.Path, err)
		}
		if o.Method == "DELETE" && o.Class != ClassBooking {
			t.Errorf("DELETE %s not booking", o.Path)
		}
		if o.Method == "GET" && o.Class == ClassBooking && o.ID != "sendCreditNoteByPrinting" {
			t.Errorf("unexpected booking GET %s", o.Path)
		}
	}
}

func TestClassification(t *testing.T) {
	want := map[string]Class{
		"GET /Invoice":                                 ClassRead,
		"GET /Invoice/1/getPdf":                        ClassRead,
		"POST /Contact":                                ClassWrite,
		"PUT /Contact/1":                               ClassWrite,
		"POST /Invoice/Factory/saveInvoice":            ClassWrite,
		"POST /Invoice/Factory/createInvoiceFromOrder": ClassWrite,
		"PUT /Invoice/1/enshrine":                      ClassBooking,
		"PUT /Invoice/1/bookAmount":                    ClassBooking,
		"PUT /Invoice/1/sendBy":                        ClassBooking,
		"POST /Invoice/1/sendViaEmail":                 ClassBooking,
		"PUT /Invoice/1/resetToOpen":                   ClassBooking,
		"PUT /Invoice/1/resetToDraft":                  ClassBooking,
		"POST /Invoice/1/cancelInvoice":                ClassBooking,
		"POST /Invoice/Factory/createInvoiceReminder":  ClassBooking,
		"PUT /CheckAccountTransaction/1/enshrine":      ClassBooking,
		"PUT /Voucher/1/bookAmount":                    ClassBooking,
		"PUT /CreditNote/1/bookAmount":                 ClassBooking,
		"GET /CreditNote/1/sendByWithRender":           ClassBooking,
		"DELETE /Contact/1":                            ClassBooking,
		"DELETE /Invoice/1":                            ClassBooking,
		"DELETE /Voucher/1":                            ClassBooking,
		"DELETE /Part/1":                               ClassBooking,
		"GET /Export/createDatevCsvZipExportJob":       ClassWrite,
		"GET /SevUser":                                 ClassRead,
	}
	for k, c := range want {
		m, p, _ := strings.Cut(k, " ")
		op, err := Match(m, p)
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		if op.Class != c {
			t.Errorf("%s: class %s, want %s", k, op.Class, c)
		}
	}
}

func TestEffectiveClass(t *testing.T) {
	inv, _ := Match("POST", "/Invoice/Factory/saveInvoice")
	pdf, _ := Match("GET", "/Invoice/1/getPdf")
	vou, _ := Match("PUT", "/Voucher/1")
	cases := []struct {
		name string
		op   *Operation
		q, b map[string]any
		want Class
	}{
		{"draft", inv, nil, map[string]any{"invoice": map[string]any{"status": 100.0}}, ClassWrite},
		{"no status", inv, nil, map[string]any{"invoice": map[string]any{}}, ClassWrite},
		{"open string", inv, nil, map[string]any{"invoice": map[string]any{"status": "200"}}, ClassBooking},
		{"garbage status", inv, nil, map[string]any{"invoice": map[string]any{"status": "x"}}, ClassBooking},
		{"enshrined", inv, nil, map[string]any{"invoice": map[string]any{"enshrined": "2026-01-01"}}, ClassBooking},
		{"top-level status", vou, nil, map[string]any{"status": 100.0}, ClassBooking},
		{"voucher draft", vou, nil, map[string]any{"status": 50.0}, ClassWrite},
		{"pdf plain", pdf, nil, nil, ClassBooking},
		{"pdf prevent", pdf, map[string]any{"preventSendBy": true}, nil, ClassRead},
		{"pdf prevent string", pdf, map[string]any{"preventSendBy": "true"}, nil, ClassRead},
		{"pdf prevent false", pdf, map[string]any{"preventSendBy": false}, nil, ClassBooking},
	}
	for _, c := range cases {
		if got := EffectiveClass(c.op, c.q, c.b); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestMatchRejects(t *testing.T) {
	good := [][2]string{{"GET", "/Invoice/123"}, {"get", "/Invoice/123/getPdf"}, {"GET", "/Tools/bookkeepingSystemVersion"}, {"DELETE", "/PrivateTransactionRule/5"}}
	for _, g := range good {
		if _, err := Match(g[0], g[1]); err != nil {
			t.Errorf("%v: %v", g, err)
		}
	}
	bad := []string{"", "/", "Invoice", "/Invoice/", "//Invoice", "/Invoice//1", "/Invoice/.", "/Invoice/..", "/Invoice/1/..", "/../Invoice",
		"/Invoice/%2e%2e", "/Invoice/1%2f", "/Invoice/1;x", "/Invoice/1?a=b", "/Invoice/1#f", "/Invoice\\1", "/Invoice/1\n", "/Invoice/ 1",
		"/Invoice/x", "/Invoice/1.5", "/Invoice/" + strings.Repeat("1", 19), "/NoSuch", "/Invoice/1/noSuch", "/Contact/1/getPdf", "/" + strings.Repeat("a", 300)}
	for _, p := range bad {
		if op, err := Match("GET", p); err == nil {
			t.Errorf("%q matched %s %s", p, op.Method, op.Path)
		}
	}
	if _, err := Match("PATCH", "/Invoice/1"); err == nil {
		t.Error("PATCH matched")
	}
}

func TestBuildPath(t *testing.T) {
	if p, err := BuildPath("/Invoice/{invoiceId}/getPdf", 12); err != nil || p != "/Invoice/12/getPdf" {
		t.Errorf("%s %v", p, err)
	}
	for _, id := range []int64{0, -3} {
		if _, err := BuildPath("/Invoice/{x}", id); err == nil {
			t.Errorf("id %d accepted", id)
		}
	}
	if _, err := BuildPath("/Invoice/{x}"); err == nil {
		t.Error("missing id accepted")
	}
	if _, err := BuildPath("/Invoice/{x}", 1, 2); err == nil {
		t.Error("extra id accepted")
	}
}

func TestAllowedQuery(t *testing.T) {
	list, _ := Match("GET", "/Invoice")
	for _, q := range []string{"status", "contact[id]", "limit", "offset", "embed"} {
		if !list.AllowedQuery(q) {
			t.Errorf("%s not allowed", q)
		}
	}
	if list.AllowedQuery("enshrine") || list.AllowedQuery("foo") {
		t.Error("unknown query allowed")
	}
	post, _ := Match("POST", "/Contact")
	if post.AllowedQuery("limit") {
		t.Error("limit allowed on POST")
	}
}
