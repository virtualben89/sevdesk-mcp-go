package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func bodyOf(t *testing.T, f *fakeSevdesk) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(f.last().Body), &m); err != nil {
		t.Fatalf("last body %q: %v", f.last().Body, err)
	}
	return m
}

func TestCreateInvoiceIsAlwaysDraft(t *testing.T) {
	f := newFake(t)
	f.handler = func(r *http.Request, _ string) (int, string) {
		if r.URL.Path == "/api/v1/SevUser" {
			return 200, `{"objects":[{"id":"42","fullname":"Ben"}]}`
		}
		return 200, `{"objects":{"invoice":{"id":"1","status":"100"}}}`
	}
	cs := connect(t, f, writeOpts, false)
	args := map[string]any{"contact_id": 5, "date": "2026-03-05", "positions": []any{
		map[string]any{"name": "Beratung", "quantity": 2, "price": 100, "tax_rate": 19}}}
	out, isErr := call(t, cs, "create_invoice", args)
	if isErr {
		t.Fatal(out)
	}
	if f.last().Method != "POST" || f.last().Path != "/api/v1/Invoice/Factory/saveInvoice" {
		t.Fatalf("wrong request %+v", f.last())
	}
	inv := bodyOf(t, f)["invoice"].(map[string]any)
	if inv["status"].(float64) != 100 || inv["invoiceDate"] != "05.03.2026" || inv["invoiceType"] != "RE" {
		t.Errorf("unexpected invoice %v", inv)
	}
	if inv["contactPerson"].(map[string]any)["id"].(float64) != 42 {
		t.Errorf("contact person not resolved: %v", inv["contactPerson"])
	}
	pos := bodyOf(t, f)["invoicePosSave"].([]any)
	if len(pos) != 1 || pos[0].(map[string]any)["unity"].(map[string]any)["id"].(float64) != 1 {
		t.Errorf("positions %v", pos)
	}
	// extra may add fields but never raise the status or lock the document.
	for _, k := range []string{"status", "enshrined", "objectName", "mapAll"} {
		a := map[string]any{"contact_id": 5, "date": "2026-03-05", "positions": args["positions"], "extra": map[string]any{k: 200}}
		if out, isErr := call(t, cs, "create_invoice", a); !isErr || !strings.Contains(out, "extra."+k) {
			t.Errorf("extra.%s accepted: %s", k, out)
		}
	}
	a := map[string]any{"contact_id": 5, "date": "2026-03-05", "positions": args["positions"], "extra": map[string]any{"customerInternalNote": "PO 4711"}}
	if out, isErr := call(t, cs, "create_invoice", a); isErr {
		t.Fatal(out)
	}
	if bodyOf(t, f)["invoice"].(map[string]any)["customerInternalNote"] != "PO 4711" {
		t.Error("extra not merged")
	}
	// validation
	for _, bad := range []map[string]any{
		{"contact_id": 5, "date": "2026-03-05", "positions": []any{}},
		{"contact_id": 5, "date": "nope", "positions": args["positions"]},
		{"contact_id": 5, "date": "2026-03-05", "positions": []any{map[string]any{"quantity": 1, "price": 1, "tax_rate": 19}}},
		{"contact_id": 5, "date": "2026-03-05", "order_type": "AN", "positions": args["positions"], "contact_person_id": 1},
	} {
		if out, isErr := call(t, cs, "create_invoice", bad); !isErr {
			t.Errorf("accepted %v: %s", bad, out)
		}
	}
}

func TestCreateVoucherDraftAndSums(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, writeOpts, false)
	out, isErr := call(t, cs, "create_voucher", map[string]any{"credit_debit": "D", "voucher_date": "2026-03-05", "supplier_name": "Lieferant",
		"filename": "abc123.pdf", "positions": []any{map[string]any{"accounting_type_id": 9, "tax_rate": 19, "amount": 119}}})
	if isErr {
		t.Fatal(out)
	}
	b := bodyOf(t, f)
	v := b["voucher"].(map[string]any)
	p := b["voucherPosSave"].([]any)[0].(map[string]any)
	if v["status"].(float64) != 50 || p["sumNet"].(float64) != 100 || p["sumGross"].(float64) != 119 || b["filename"] != "abc123.pdf" {
		t.Errorf("unexpected body %v", b)
	}
	if out, isErr := call(t, cs, "create_voucher", map[string]any{"credit_debit": "D", "voucher_date": "2026-03-05", "supplier_name": "x",
		"filename": "../../etc/passwd", "positions": []any{map[string]any{"accounting_type_id": 9, "tax_rate": 19, "amount": 1}}}); !isErr {
		t.Errorf("unsafe filename accepted: %s", out)
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, bookingOpts, true)
	out, isErr := call(t, cs, "create_contact", map[string]any{"name": "ACME"})
	if isErr || !strings.Contains(out, `"dry_run":true`) || !strings.Contains(out, `"name":"ACME"`) {
		t.Fatalf("%v %s", isErr, out)
	}
	call(t, cs, "delete_object", map[string]any{"entity": "contact", "id": 1})
	call(t, cs, "document_action", map[string]any{"document_type": "invoice", "id": 1, "action": "enshrine"})
	call(t, cs, "sevdesk_call", map[string]any{"method": "DELETE", "path": "/Contact/1"})
	if f.count() != 0 {
		t.Errorf("dry run sent %d requests: %+v", f.count(), f.reqs)
	}
	if strings.Contains(out, testToken) {
		t.Error("preview leaks the token")
	}
	// reads still go through in dry run
	call(t, cs, "list_contacts", nil)
	if f.count() != 1 {
		t.Errorf("read in dry run: %d requests", f.count())
	}
}

func TestBookingTools(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, bookingOpts, false)
	if out, isErr := call(t, cs, "book_payment", map[string]any{"document_type": "invoice", "id": 3, "amount": 119, "date": "2026-03-05", "check_account_id": 9, "transaction_id": 11}); isErr {
		t.Fatal(out)
	}
	if f.last().Method != "PUT" || f.last().Path != "/api/v1/Invoice/3/bookAmount" {
		t.Errorf("%+v", f.last())
	}
	b := bodyOf(t, f)
	if b["type"] != "N" || b["checkAccount"].(map[string]any)["id"].(float64) != 9 || b["checkAccountTransaction"] == nil {
		t.Errorf("%v", b)
	}
	if _, ok := b["date"].(float64); !ok {
		t.Errorf("invoice booking date must be a unix timestamp number: %v", b["date"])
	}
	call(t, cs, "book_payment", map[string]any{"document_type": "voucher", "id": 3, "amount": 5, "date": "2026-03-05", "check_account_id": 9})
	if d, ok := bodyOf(t, f)["date"].(string); !ok || d != "05.03.2026" {
		t.Errorf("voucher booking date must be a string: %v", bodyOf(t, f)["date"])
	}
	for action, want := range map[string]string{"enshrine": "/Invoice/3/enshrine", "mark_sent": "/Invoice/3/sendBy", "reset_to_open": "/Invoice/3/resetToOpen", "reset_to_draft": "/Invoice/3/resetToDraft", "cancel": "/Invoice/3/cancelInvoice"} {
		if out, isErr := call(t, cs, "document_action", map[string]any{"document_type": "invoice", "id": 3, "action": action}); isErr {
			t.Fatalf("%s: %s", action, out)
		}
		if f.last().Path != "/api/v1"+want {
			t.Errorf("%s -> %s", action, f.last().Path)
		}
	}
	if _, isErr := call(t, cs, "document_action", map[string]any{"document_type": "voucher", "id": 3, "action": "cancel"}); !isErr {
		t.Error("cancel on voucher accepted")
	}
	if out, isErr := call(t, cs, "send_document_email", map[string]any{"document_type": "invoice", "id": 3, "to": "a@b.de", "subject": "s", "text": "t"}); isErr {
		t.Fatal(out)
	}
	if _, isErr := call(t, cs, "send_document_email", map[string]any{"document_type": "invoice", "id": 3, "to": "a@b.de, evil@x.de", "subject": "s", "text": "t"}); !isErr {
		t.Error("multiple recipients accepted in 'to'")
	}
	if _, isErr := call(t, cs, "delete_object", map[string]any{"entity": "invoice", "id": 3}); !isErr {
		t.Error("invoice delete accepted")
	}
	if out, isErr := call(t, cs, "delete_object", map[string]any{"entity": "contact", "id": 3}); isErr || f.last().Method != "DELETE" {
		t.Errorf("%s", out)
	}
}

func TestSevdeskCallTiers(t *testing.T) {
	f := newFake(t)
	ro := connect(t, f, readOpts, false)
	wr := connect(t, f, writeOpts, false)
	bk := connect(t, f, bookingOpts, false)

	// reads work everywhere; unlimited collection gets a limit
	if out, isErr := call(t, ro, "sevdesk_call", map[string]any{"method": "GET", "path": "/Tag"}); isErr || !strings.Contains(out, "limit=50") {
		t.Fatalf("%v %s", isErr, out)
	}
	if !strings.Contains(f.last().RawQuery, "limit=50") {
		t.Errorf("auto limit missing: %s", f.last().RawQuery)
	}
	cases := []struct {
		name   string
		args   map[string]any
		wantRO bool // allowed in read-only
		wantWR bool // allowed with write only
		wantBK bool
	}{
		{"read", map[string]any{"method": "GET", "path": "/Invoice/12"}, true, true, true},
		{"create contact", map[string]any{"method": "POST", "path": "/Contact", "body": map[string]any{"name": "x"}}, false, true, true},
		{"draft invoice", map[string]any{"method": "POST", "path": "/Invoice/Factory/saveInvoice", "body": map[string]any{"invoice": map[string]any{"status": 100}}}, false, true, true},
		{"open invoice", map[string]any{"method": "POST", "path": "/Invoice/Factory/saveInvoice", "body": map[string]any{"invoice": map[string]any{"status": "200"}}}, false, false, true},
		{"voucher open", map[string]any{"method": "POST", "path": "/Voucher/Factory/saveVoucher", "body": map[string]any{"voucher": map[string]any{"status": 100}}}, false, false, true},
		{"voucher draft", map[string]any{"method": "POST", "path": "/Voucher/Factory/saveVoucher", "body": map[string]any{"voucher": map[string]any{"status": 50}}}, false, true, true},
		{"enshrine", map[string]any{"method": "PUT", "path": "/Invoice/1/enshrine"}, false, false, true},
		{"book", map[string]any{"method": "PUT", "path": "/Invoice/1/bookAmount", "body": map[string]any{"amount": 1}}, false, false, true},
		{"send mail", map[string]any{"method": "POST", "path": "/Invoice/1/sendViaEmail", "body": map[string]any{"toEmail": "a@b.de"}}, false, false, true},
		{"delete", map[string]any{"method": "DELETE", "path": "/Contact/1"}, false, false, true},
		{"pdf plain", map[string]any{"method": "GET", "path": "/Invoice/1/getPdf"}, false, false, true},
		{"pdf safe", map[string]any{"method": "GET", "path": "/Invoice/1/getPdf", "query": map[string]any{"preventSendBy": true}}, true, true, true},
		{"enshrine flag", map[string]any{"method": "GET", "path": "/Export/createDatevCsvZipExportJob", "query": map[string]any{"startDate": 1, "endDate": 2, "scope": "E", "enshrineDocuments": true}}, false, false, true},
		{"print send", map[string]any{"method": "GET", "path": "/CreditNote/1/sendByWithRender", "query": map[string]any{"sendType": "VPR"}}, false, false, true},
	}
	for _, c := range cases {
		for _, m := range []struct {
			mode string
			cs   *mcp.ClientSession
			want bool
		}{{"ro", ro, c.wantRO}, {"wr", wr, c.wantWR}, {"bk", bk, c.wantBK}} {
			res, err := m.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sevdesk_call", Arguments: c.args})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError == m.want {
				t.Errorf("%s in %s mode: isError=%v, want allowed=%v: %v", c.name, m.mode, res.IsError, m.want, res.Content[0].(*mcp.TextContent).Text)
			}
		}
	}
}

func TestSevdeskCallPathValidation(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, bookingOpts, false)
	for _, p := range []string{"/Invoice/../Contact", "/Invoice/./1", "//Invoice", "/Invoice/", "Invoice", "/Invoice/1%2f..%2f", "/Invoice/abc", "/Invoice/1?x=1", "/Invoice/1#x", "/Invoice\\1", "/Unknown/1", "/Invoice/1/../../Contact", "/Contact/1/getPdf", "/Invoice/" + strings.Repeat("9", 30), "/Invoice/-1", "/Invoice/1 "} {
		out, isErr := call(t, cs, "sevdesk_call", map[string]any{"method": "GET", "path": p})
		if !isErr {
			t.Errorf("path %q accepted: %s", p, out)
		}
	}
	if f.count() != 0 {
		t.Errorf("invalid paths reached the upstream: %+v", f.reqs)
	}
	out, isErr := call(t, cs, "sevdesk_call", map[string]any{"method": "GET", "path": "/Invoice", "query": map[string]any{"enshrine": "true"}})
	if !isErr || !strings.Contains(out, "not known") {
		t.Errorf("unknown query accepted: %s", out)
	}
	out, isErr = call(t, cs, "sevdesk_call", map[string]any{"method": "POST", "path": "/Voucher/Factory/uploadTempFile"})
	if !isErr || !strings.Contains(out, "upload_voucher_file") {
		t.Errorf("multipart op via generic: %s", out)
	}
	if out, isErr := call(t, cs, "sevdesk_call", map[string]any{"method": "PATCH", "path": "/Invoice"}); !isErr {
		t.Errorf("PATCH accepted: %s", out)
	}
}

func TestListOperations(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, readOpts, false)
	out, isErr := call(t, cs, "sevdesk_list_operations", map[string]any{"search": "bookAmount"})
	if isErr || !strings.Contains(out, `"/Invoice/{invoiceId}/bookAmount"`) || !strings.Contains(out, `"allowed_now":false`) || !strings.Contains(out, `"class":"booking"`) {
		t.Errorf("%v %s", isErr, out)
	}
	out, _ = call(t, cs, "sevdesk_list_operations", map[string]any{"tag": "SevUser"})
	if !strings.Contains(out, `"supplement":true`) {
		t.Errorf("supplement not flagged: %s", out)
	}
	if _, isErr := call(t, cs, "sevdesk_list_operations", map[string]any{"class": "bogus"}); !isErr {
		t.Error("bad class accepted")
	}
	if f.count() != 0 {
		t.Error("operation index must not call upstream")
	}
}

func TestUploadVoucherFile(t *testing.T) {
	f := newFake(t)
	f.handler = func(r *http.Request, _ string) (int, string) {
		return 200, `{"objects":{"filename":"deadbeef.pdf","pages":1}}`
	}
	dir := t.TempDir()
	outside := t.TempDir()
	pdf := []byte("%PDF-1.4\nhello")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "beleg.pdf"), pdf, 0o600))
	must(os.WriteFile(filepath.Join(dir, "fake.pdf"), []byte("not a pdf at all"), 0o600))
	must(os.WriteFile(filepath.Join(dir, "big.pdf"), append([]byte("%PDF-1.4\n"), make([]byte, 2000)...), 0o600))
	must(os.WriteFile(filepath.Join(outside, "secret.pdf"), pdf, 0o600))
	must(os.Symlink(filepath.Join(outside, "secret.pdf"), filepath.Join(dir, "link.pdf")))
	must(os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hello"), 0o600))

	cs := connect(t, f, Options{AllowWrite: true, UploadDir: dir, MaxUploadBytes: 1000}, false)
	out, isErr := call(t, cs, "upload_voucher_file", map[string]any{"path": "beleg.pdf"})
	if isErr || !strings.Contains(out, "deadbeef.pdf") {
		t.Fatalf("%v %s", isErr, out)
	}
	if !strings.HasPrefix(f.last().Body, "--") || !strings.Contains(f.last().Body, `filename="beleg.pdf"`) || !strings.Contains(f.last().Body, "%PDF-1.4") {
		t.Errorf("multipart body wrong: %q", f.last().Body)
	}
	if _, isErr := call(t, cs, "upload_voucher_file", map[string]any{"path": filepath.Join(dir, "beleg.pdf")}); isErr {
		t.Error("absolute path inside the dir rejected")
	}
	n := f.count()
	for name, args := range map[string]map[string]any{
		"traversal":   {"path": "../" + filepath.Base(outside) + "/secret.pdf"},
		"absolute":    {"path": filepath.Join(outside, "secret.pdf")},
		"symlink":     {"path": "link.pdf"},
		"magic":       {"path": "fake.pdf"},
		"too big":     {"path": "big.pdf"},
		"extension":   {"path": "x.txt"},
		"both":        {"path": "beleg.pdf", "content_base64": "aGk="},
		"neither":     {},
		"b64 no name": {"content_base64": base64.StdEncoding.EncodeToString(pdf)},
		"b64 ext":     {"content_base64": base64.StdEncoding.EncodeToString(pdf), "filename": "a.exe"},
		"b64 bad":     {"content_base64": "%%%", "filename": "a.pdf"},
		"b64 big":     {"content_base64": base64.StdEncoding.EncodeToString(make([]byte, 5000)), "filename": "a.pdf"},
	} {
		if out, isErr := call(t, cs, "upload_voucher_file", args); !isErr {
			t.Errorf("%s accepted: %s", name, out)
		}
	}
	if f.count() != n {
		t.Error("rejected uploads reached the upstream")
	}
	if out, isErr := call(t, cs, "upload_voucher_file", map[string]any{"content_base64": base64.StdEncoding.EncodeToString(pdf), "filename": "../../a b.pdf"}); isErr {
		t.Fatal(out)
	} else if !strings.Contains(f.last().Body, `filename="a b.pdf"`) {
		t.Errorf("filename not sanitised: %q", f.last().Body)
	}
	cs2 := connect(t, f, Options{AllowWrite: true}, false)
	if out, isErr := call(t, cs2, "upload_voucher_file", map[string]any{"path": "beleg.pdf"}); !isErr || !strings.Contains(out, "SEVDESK_UPLOAD_DIR") {
		t.Errorf("paths must be disabled without upload dir: %s", out)
	}
}

func TestUpstreamErrorsAreActionable(t *testing.T) {
	f := newFake(t)
	f.handler = func(r *http.Request, _ string) (int, string) {
		return 401, `{"error":{"message":"Authentication required","code":401}}`
	}
	cs := connect(t, f, readOpts, false)
	out, isErr := call(t, cs, "list_contacts", nil)
	if !isErr || !strings.Contains(out, "token was rejected") || strings.Contains(out, testToken) {
		t.Errorf("%s", out)
	}
}
