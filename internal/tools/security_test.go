package tools

import (
	"strings"
	"testing"
)

// Regression tests for the security audit (run-1, 2026-11-23).

// enshrine flags must need the booking tier for every value that is not
// clearly false, judged on the serialised value that is sent upstream.
func TestEnshrineFlagVariantsNeedBooking(t *testing.T) {
	f := newFake(t)
	wr := connect(t, f, writeOpts, false)
	bk := connect(t, f, bookingOpts, false)
	for _, v := range []any{true, "true", []any{true}, []any{"true"}, "yes", "on", "2", "TRUE", 1, " true"} {
		for _, c := range []struct{ path, key string }{{"/Export/datevCSV", "enshrine"}, {"/Export/createDatevCsvZipExportJob", "enshrineDocuments"}} {
			args := map[string]any{"method": "GET", "path": c.path, "query": map[string]any{c.key: v}}
			n := f.count()
			if out, isErr := call(t, wr, "sevdesk_call", args); !isErr {
				t.Errorf("%s=%#v allowed in write tier: %s", c.key, v, out)
			}
			if f.count() != n {
				t.Errorf("%s=%#v reached upstream in write tier", c.key, v)
			}
			if out, isErr := call(t, bk, "sevdesk_call", args); isErr {
				t.Errorf("%s=%#v refused in booking tier: %s", c.key, v, out)
			}
		}
	}
	for _, v := range []any{false, "false", 0, "0", ""} {
		if out, isErr := call(t, wr, "sevdesk_call", map[string]any{"method": "GET", "path": "/Export/datevCSV", "query": map[string]any{"enshrine": v}}); isErr {
			t.Errorf("enshrine=%#v refused in write tier: %s", v, out)
		}
	}
}

// getPdf counts as read only with exactly preventSendBy=true: sevDesk marked
// drafts as sent AND enshrined them for " true" and "2" (live, trial account).
func TestPreventSendByMustBeExactlyTrue(t *testing.T) {
	f := newFake(t)
	ro := connect(t, f, readOpts, false)
	for _, v := range []any{" true", 2, "TRUE", "1", "yes", "true,true", []any{true, true}, false} {
		n := f.count()
		if out, isErr := call(t, ro, "sevdesk_call", map[string]any{"method": "GET", "path": "/Invoice/1/getPdf", "query": map[string]any{"preventSendBy": v}}); !isErr {
			t.Errorf("preventSendBy=%#v allowed in read-only: %s", v, out)
		}
		if f.count() != n {
			t.Errorf("preventSendBy=%#v reached upstream", v)
		}
	}
	for _, v := range []any{true, "true"} {
		if out, isErr := call(t, ro, "sevdesk_call", map[string]any{"method": "GET", "path": "/Invoice/1/getPdf", "query": map[string]any{"preventSendBy": v}}); isErr {
			t.Errorf("preventSendBy=%#v refused: %s", v, out)
		}
		if !strings.Contains(f.last().RawQuery, "preventSendBy=true") {
			t.Errorf("sent %q", f.last().RawQuery)
		}
	}
}

// DRY_RUN must not send GETs that the server itself classifies above read.
func TestDryRunBlocksSideEffectGETs(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, bookingOpts, true)
	for _, a := range []map[string]any{
		{"method": "GET", "path": "/Invoice/1/getPdf"},
		{"method": "GET", "path": "/CreditNote/2/sendByWithRender", "query": map[string]any{"sendType": "VPDF"}},
		{"method": "GET", "path": "/Export/datevCSV", "query": map[string]any{"enshrine": true}},
	} {
		out, isErr := call(t, cs, "sevdesk_call", a)
		if isErr || !strings.Contains(out, `"dry_run":true`) {
			t.Errorf("%v: %v %s", a, isErr, out)
		}
	}
	if f.count() != 0 {
		t.Fatalf("dry run sent %d requests: %+v", f.count(), f.reqs)
	}
	call(t, cs, "sevdesk_call", map[string]any{"method": "GET", "path": "/Invoice/1/getPdf", "query": map[string]any{"preventSendBy": true}})
	if f.count() != 1 {
		t.Errorf("plain read in dry run: %d requests", f.count())
	}
}

func TestSendEmailCCBCCValidated(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, bookingOpts, false)
	base := map[string]any{"document_type": "invoice", "id": 3, "to": "a@b.de", "subject": "s", "text": "t"}
	with := func(k, v string) map[string]any {
		m := map[string]any{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	if out, isErr := call(t, cs, "send_document_email", with("cc", "c@d.de, e@f.de")); isErr {
		t.Errorf("valid cc refused: %s", out)
	}
	for _, bad := range []string{"nobody", "a@b.de\r\nBcc: x@y.de", "Name <a@b.de>", "a@b.de;c@d.de", strings.Repeat("a@b.de,", 11) + "a@b.de"} {
		for _, k := range []string{"cc", "bcc"} {
			if _, isErr := call(t, cs, "send_document_email", with(k, bad)); !isErr {
				t.Errorf("%s=%q accepted", k, bad)
			}
		}
	}
}

func TestContactExtraProtectedKeys(t *testing.T) {
	f := newFake(t)
	cs := connect(t, f, writeOpts, false)
	for _, k := range []string{"id", "objectName", "MAPALL"} {
		if _, isErr := call(t, cs, "create_contact", map[string]any{"name": "ACME", "extra": map[string]any{k: "x"}}); !isErr {
			t.Errorf("extra.%s accepted", k)
		}
	}
	// a differently cased duplicate of a tool-set key must not be added
	if out, isErr := call(t, cs, "create_contact", map[string]any{"name": "ACME", "extra": map[string]any{"NAME": "Evil"}}); isErr {
		t.Fatal(out)
	}
	if strings.Contains(f.last().Body, "Evil") {
		t.Errorf("case variant of an existing key was merged: %s", f.last().Body)
	}
}
