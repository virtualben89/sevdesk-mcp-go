package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
)

// docKind describes a bookkeeping document type.
type docKind struct {
	key      string // enum value
	object   string // sevDesk object name
	path     string
	posPath  string
	hasPDF   bool
	pdfLabel string
}

var (
	kInvoice    = docKind{"invoice", "Invoice", "/Invoice", "/InvoicePos", true, "invoice"}
	kCreditNote = docKind{"credit_note", "CreditNote", "/CreditNote", "/CreditNotePos", true, "credit note"}
	kOrder      = docKind{"order", "Order", "/Order", "/OrderPos", true, "order/offer"}
	kVoucher    = docKind{"voucher", "Voucher", "/Voucher", "/VoucherPos", false, "voucher"}
)

var allDocKinds = []docKind{kInvoice, kCreditNote, kOrder, kVoucher}

func docKindByKey(key string, allowed ...docKind) (docKind, error) {
	if len(allowed) == 0 {
		allowed = allDocKinds
	}
	var names []string
	for _, k := range allowed {
		if k.key == key {
			return k, nil
		}
		names = append(names, k.key)
	}
	return docKind{}, fmt.Errorf("invalid document_type %q (use %s)", key, strings.Join(names, ", "))
}

func kindEnum(ks ...docKind) []any {
	out := make([]any, len(ks))
	for i, k := range ks {
		out[i] = k.key
	}
	return out
}

// ---- inputs ----

// GetStatusInput are the parameters of get_status.
type GetStatusInput struct {
	Check *bool `json:"check,omitempty" jsonschema:"Also call sevDesk (bookkeeping system version) to verify the token (default true)"`
}

// ListContactsInput are the parameters of list_contacts.
type ListContactsInput struct {
	CustomerNumber string `json:"customer_number,omitempty" jsonschema:"Exact customer number"`
	Depth          *int   `json:"depth,omitempty" jsonschema:"0 = organisations only, 1 = organisations and persons (sevDesk default is 0)"`
	Limit          int    `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset         int    `json:"offset,omitempty" jsonschema:"Number of items to skip"`
	Full           bool   `json:"full,omitempty" jsonschema:"Return complete objects instead of the slim view"`
}

// GetContactInput are the parameters of get_contact.
type GetContactInput struct {
	ID                   int64 `json:"id" jsonschema:"Contact id"`
	IncludeAddresses     bool  `json:"include_addresses,omitempty" jsonschema:"Also fetch the contact's addresses"`
	IncludeCommunication bool  `json:"include_communication_ways,omitempty" jsonschema:"Also fetch phone numbers, e-mail addresses, websites"`
}

// ByContactInput are the parameters of the per-contact list tools.
type ByContactInput struct {
	ContactID int64  `json:"contact_id" jsonschema:"Contact id"`
	Type      string `json:"type,omitempty" jsonschema:"Only communication ways of this type (list_communication_ways)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset    int    `json:"offset,omitempty" jsonschema:"Number of items to skip"`
	Full      bool   `json:"full,omitempty" jsonschema:"Return complete objects instead of the slim view"`
}

// ListDocumentsInput are the parameters of list_invoices, list_credit_notes, list_orders and list_vouchers.
type ListDocumentsInput struct {
	Status          *int   `json:"status,omitempty" jsonschema:"Status code, e.g. invoices: 100 draft, 200 open, 750 partially paid, 1000 paid; vouchers: 50 draft, 100 unpaid, 1000 paid"`
	Number          string `json:"number,omitempty" jsonschema:"Exact document number (invoices, credit notes, orders)"`
	ContactID       int64  `json:"contact_id,omitempty" jsonschema:"Only documents of this contact"`
	StartDate       string `json:"start_date,omitempty" jsonschema:"Documents dated on or after this day (YYYY-MM-DD)"`
	EndDate         string `json:"end_date,omitempty" jsonschema:"Documents dated on or before this day (YYYY-MM-DD)"`
	CreditDebit     string `json:"credit_debit,omitempty" jsonschema:"Vouchers only: C or D"`
	DescriptionLike string `json:"description_like,omitempty" jsonschema:"Vouchers only: description contains this text"`
	Limit           int    `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset          int    `json:"offset,omitempty" jsonschema:"Number of items to skip"`
	Full            bool   `json:"full,omitempty" jsonschema:"Return complete objects instead of the slim view"`
}

// IDInput is the input of the simple get_* tools.
type IDInput struct {
	ID int64 `json:"id" jsonschema:"Object id"`
}

// PositionsInput are the parameters of list_document_positions.
type PositionsInput struct {
	DocumentType string `json:"document_type" jsonschema:"Which document type the id belongs to"`
	ID           int64  `json:"id" jsonschema:"Document id"`
	Limit        int    `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset       int    `json:"offset,omitempty" jsonschema:"Number of items to skip"`
	Full         bool   `json:"full,omitempty" jsonschema:"Return complete objects instead of the slim view"`
}

// PDFInput are the parameters of download_document_pdf.
type PDFInput struct {
	DocumentType string `json:"document_type" jsonschema:"Which document type the id belongs to"`
	ID           int64  `json:"id" jsonschema:"Document id"`
}

// ListPartsInput are the parameters of list_parts.
type ListPartsInput struct {
	PartNumber string `json:"part_number,omitempty" jsonschema:"Exact part number"`
	Name       string `json:"name,omitempty" jsonschema:"Exact part name"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset     int    `json:"offset,omitempty" jsonschema:"Number of items to skip"`
	Full       bool   `json:"full,omitempty" jsonschema:"Return complete objects instead of the slim view"`
}

// GuidanceInput are the parameters of list_accounting_guidance.
type GuidanceInput struct {
	Kind          string `json:"kind,omitempty" jsonschema:"all (default), revenue, expense, account_number or tax_rule"`
	AccountNumber string `json:"account_number,omitempty" jsonschema:"Required for kind=account_number (e.g. 8400)"`
	TaxRule       string `json:"tax_rule,omitempty" jsonschema:"Required for kind=tax_rule (tax rule id, e.g. 1)"`
}

// ListCheckAccountsInput are the parameters of list_check_accounts.
type ListCheckAccountsInput struct {
	Limit  int  `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset int  `json:"offset,omitempty" jsonschema:"Number of items to skip"`
	Full   bool `json:"full,omitempty" jsonschema:"Return complete objects instead of the slim view"`
}

// GetCheckAccountInput are the parameters of get_check_account.
type GetCheckAccountInput struct {
	ID          int64  `json:"id" jsonschema:"Check account (bank account) id"`
	BalanceDate string `json:"balance_date,omitempty" jsonschema:"Also return the balance at this day (YYYY-MM-DD)"`
}

// ListTransactionsInput are the parameters of list_transactions.
type ListTransactionsInput struct {
	CheckAccountID int64  `json:"check_account_id,omitempty" jsonschema:"Only transactions of this check account"`
	IsBooked       *bool  `json:"is_booked,omitempty" jsonschema:"true = only booked, false = only unbooked transactions"`
	Purpose        string `json:"purpose,omitempty" jsonschema:"Payment purpose contains this text"`
	PayeePayerName string `json:"payee_payer_name,omitempty" jsonschema:"Name of the other party contains this text"`
	StartDate      string `json:"start_date,omitempty" jsonschema:"On or after this day (YYYY-MM-DD)"`
	EndDate        string `json:"end_date,omitempty" jsonschema:"On or before this day (YYYY-MM-DD)"`
	OnlyCredit     bool   `json:"only_credit,omitempty" jsonschema:"Only incoming payments"`
	OnlyDebit      bool   `json:"only_debit,omitempty" jsonschema:"Only outgoing payments"`
	Limit          int    `json:"limit,omitempty" jsonschema:"Items per page (1-100, default 50)"`
	Offset         int    `json:"offset,omitempty" jsonschema:"Number of items to skip"`
	Full           bool   `json:"full,omitempty" jsonschema:"Return complete objects instead of the slim view"`
}

// ---- helpers ----

func contactFilter(q url.Values, key string, id int64) {
	if id > 0 {
		q.Set(key+"[id]", strconv.FormatInt(id, 10))
		q.Set(key+"[objectName]", "Contact")
	}
}

func (r *Registry) registerRead() {
	add(r, toolDef{name: "get_status", title: "Server status", class: sevdesk.ClassRead,
		description: "Shows which safety tiers are active (read-only / write / booking / dry run), how the sevDesk token is supplied, and optionally verifies the token against sevDesk. Call this first to learn what this server may do."},
		r.getStatus)

	add(r, toolDef{name: "list_contacts", title: "List contacts", class: sevdesk.ClassRead,
		description: "List customers/suppliers (Contact), paginated and slim by default. Filter by exact customer_number. Use get_contact for addresses and communication ways."},
		func(ctx context.Context, in ListContactsInput) (*mcp.CallToolResult, error) {
			p, err := r.page(in.Limit, in.Offset)
			if err != nil {
				return nil, err
			}
			q := url.Values{}
			if in.CustomerNumber != "" {
				q.Set("customerNumber", in.CustomerNumber)
			}
			if in.Depth != nil {
				if *in.Depth != 0 && *in.Depth != 1 {
					return nil, errors.New("depth must be 0 or 1")
				}
				q.Set("depth", strconv.Itoa(*in.Depth))
			}
			return r.listCall(ctx, "/Contact", q, p, "contact", in.Full, nil)
		})

	add(r, toolDef{name: "get_contact", title: "Get contact", class: sevdesk.ClassRead,
		description: "Get one contact by id, optionally with its addresses and communication ways (phone, e-mail, web)."},
		func(ctx context.Context, in GetContactInput) (*mcp.CallToolResult, error) {
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			obj, err := r.getOne(ctx, "/Contact/"+strconv.FormatInt(in.ID, 10))
			if err != nil {
				return nil, err
			}
			out := map[string]any{"contact": obj}
			if in.IncludeAddresses {
				items, err := r.byContact(ctx, "/ContactAddress", in.ID, nil)
				if err != nil {
					return nil, err
				}
				out["addresses"] = items
			}
			if in.IncludeCommunication {
				items, err := r.byContact(ctx, "/CommunicationWay", in.ID, nil)
				if err != nil {
					return nil, err
				}
				out["communication_ways"] = items
			}
			return valueResult(out)
		})

	add(r, toolDef{name: "list_contact_addresses", title: "List contact addresses", class: sevdesk.ClassRead,
		description: "List the postal addresses of one contact."},
		func(ctx context.Context, in ByContactInput) (*mcp.CallToolResult, error) {
			return r.listByContact(ctx, in, "/ContactAddress", "address")
		})

	add(r, toolDef{name: "list_communication_ways", title: "List communication ways", class: sevdesk.ClassRead,
		description: "List phone numbers, e-mail addresses and websites of one contact. Optional type filter: EMAIL, PHONE, WEB, MOBILE."},
		func(ctx context.Context, in ByContactInput) (*mcp.CallToolResult, error) {
			return r.listByContact(ctx, in, "/CommunicationWay", "way")
		})

	for _, k := range []docKind{kInvoice, kCreditNote, kOrder, kVoucher} {
		r.registerDocRead(k)
	}

	add(r, toolDef{name: "list_document_positions", title: "List document positions", class: sevdesk.ClassRead,
		enums:       map[string][]any{"document_type": kindEnum(allDocKinds...)},
		description: "List the line items of an invoice, credit note, order or voucher (quantity, price, tax rate, part; vouchers: booking account)."},
		func(ctx context.Context, in PositionsInput) (*mcp.CallToolResult, error) {
			k, err := docKindByKey(in.DocumentType)
			if err != nil {
				return nil, err
			}
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			p, err := r.page(in.Limit, in.Offset)
			if err != nil {
				return nil, err
			}
			q := url.Values{}
			field := strings.ToLower(k.object[:1]) + k.object[1:]
			q.Set(field+"[id]", strconv.FormatInt(in.ID, 10))
			q.Set(field+"[objectName]", k.object)
			return r.listCall(ctx, k.posPath, q, p, "position", in.Full, nil)
		})

	add(r, toolDef{name: "download_document_pdf", title: "Download document PDF", class: sevdesk.ClassRead,
		enums:       map[string][]any{"document_type": kindEnum(kInvoice, kCreditNote, kOrder)},
		description: "Fetch the rendered PDF of an invoice, credit note or order as base64 (content_base64). Never marks the document as sent (preventSendBy=true). The result can be large; it is refused above the configured download limit."},
		r.downloadPDF)

	add(r, toolDef{name: "list_parts", title: "List parts (articles)", class: sevdesk.ClassRead,
		description: "List parts/articles (Part), paginated and slim by default. Filter by exact part_number or name."},
		func(ctx context.Context, in ListPartsInput) (*mcp.CallToolResult, error) {
			p, err := r.page(in.Limit, in.Offset)
			if err != nil {
				return nil, err
			}
			q := url.Values{}
			if in.PartNumber != "" {
				q.Set("partNumber", in.PartNumber)
			}
			if in.Name != "" {
				q.Set("name", in.Name)
			}
			return r.listCall(ctx, "/Part", q, p, "part", in.Full, nil)
		})
	r.addGetByID("get_part", "Get part", "Get one part/article by id.", "/Part/%d")

	add(r, toolDef{name: "get_part_stock", title: "Get part stock", class: sevdesk.ClassRead,
		description: "Current stock amount of a part."},
		func(ctx context.Context, in IDInput) (*mcp.CallToolResult, error) {
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			m, err := r.getJSON(ctx, fmt.Sprintf("/Part/%d/getStock", in.ID), nil)
			if err != nil {
				return nil, err
			}
			return valueResult(map[string]any{"part_id": in.ID, "stock": m["objects"]})
		})

	add(r, toolDef{name: "list_accounting_guidance", title: "List booking accounts / guidance", class: sevdesk.ClassRead,
		enums:       map[string][]any{"kind": {"all", "revenue", "expense", "account_number", "tax_rule"}},
		description: "Booking accounts (accounting types / Buchungskonten) and receipt guidance: which accounts and tax rules fit when creating a voucher. kind: all, revenue, expense, account_number (needs account_number) or tax_rule (needs tax_rule). Output is the upstream guidance list and can be long."},
		r.guidance)

	add(r, toolDef{name: "list_check_accounts", title: "List check accounts", class: sevdesk.ClassRead,
		description: "List bank/check accounts (CheckAccount)."},
		func(ctx context.Context, in ListCheckAccountsInput) (*mcp.CallToolResult, error) {
			p, err := r.page(in.Limit, in.Offset)
			if err != nil {
				return nil, err
			}
			return r.listCall(ctx, "/CheckAccount", nil, p, "check_account", in.Full, nil)
		})

	add(r, toolDef{name: "get_check_account", title: "Get check account", class: sevdesk.ClassRead,
		description: "Get one check (bank) account, optionally with its balance at a given day."},
		func(ctx context.Context, in GetCheckAccountInput) (*mcp.CallToolResult, error) {
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			obj, err := r.getOne(ctx, fmt.Sprintf("/CheckAccount/%d", in.ID))
			if err != nil {
				return nil, err
			}
			out := map[string]any{"check_account": obj}
			if in.BalanceDate != "" {
				ts, err := unixStart(in.BalanceDate)
				if err != nil {
					return nil, err
				}
				m, err := r.getJSON(ctx, fmt.Sprintf("/CheckAccount/%d/getBalanceAtDate", in.ID), url.Values{"date": {ts}})
				if err != nil {
					return nil, err
				}
				out["balance_at"] = map[string]any{"date": in.BalanceDate, "balance": m["objects"]}
			}
			return valueResult(out)
		})

	add(r, toolDef{name: "list_transactions", title: "List bank transactions", class: sevdesk.ClassRead,
		description: "List bank transactions (CheckAccountTransaction) with filters for account, booked state, purpose, counterparty, dates and direction."},
		func(ctx context.Context, in ListTransactionsInput) (*mcp.CallToolResult, error) {
			p, err := r.page(in.Limit, in.Offset)
			if err != nil {
				return nil, err
			}
			q := url.Values{}
			if in.CheckAccountID > 0 {
				q.Set("checkAccount[id]", strconv.FormatInt(in.CheckAccountID, 10))
				q.Set("checkAccount[objectName]", "CheckAccount")
			}
			if in.IsBooked != nil {
				q.Set("isBooked", strconv.FormatBool(*in.IsBooked))
			}
			if in.Purpose != "" {
				q.Set("paymtPurpose", in.Purpose)
			}
			if in.PayeePayerName != "" {
				q.Set("payeePayerName", in.PayeePayerName)
			}
			if q2, err := unixStart(in.StartDate); err != nil {
				return nil, err
			} else if q2 != "" {
				q.Set("startDate", q2)
			}
			if q2, err := unixEnd(in.EndDate); err != nil {
				return nil, err
			} else if q2 != "" {
				q.Set("endDate", q2)
			}
			if in.OnlyCredit {
				q.Set("onlyCredit", "true")
			}
			if in.OnlyDebit {
				q.Set("onlyDebit", "true")
			}
			return r.listCall(ctx, "/CheckAccountTransaction", q, p, "transaction", in.Full, nil)
		})
	r.addGetByID("get_transaction", "Get bank transaction", "Get one bank transaction by id.", "/CheckAccountTransaction/%d")
}

func (r *Registry) addGetByID(name, title, desc, pathFmt string) {
	add(r, toolDef{name: name, title: title, class: sevdesk.ClassRead, description: desc},
		func(ctx context.Context, in IDInput) (*mcp.CallToolResult, error) {
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			obj, err := r.getOne(ctx, fmt.Sprintf(pathFmt, in.ID))
			if err != nil {
				return nil, err
			}
			return valueResult(obj)
		})
}

func (r *Registry) registerDocRead(k docKind) {
	plural := map[string]string{"invoice": "invoices", "credit_note": "credit_notes", "order": "orders", "voucher": "vouchers"}[k.key]
	label := map[string]string{"invoice": "invoices", "credit_note": "credit notes", "order": "orders and offers", "voucher": "vouchers (Belege / incoming documents)"}[k.key]
	add(r, toolDef{name: "list_" + plural, title: "List " + label, class: sevdesk.ClassRead,
		description: "List " + label + ", paginated and slim by default (full=true for everything). Dates are YYYY-MM-DD (interpreted in Europe/Berlin). Filter by status, number, contact and date range."},
		func(ctx context.Context, in ListDocumentsInput) (*mcp.CallToolResult, error) {
			p, err := r.page(in.Limit, in.Offset)
			if err != nil {
				return nil, err
			}
			q := url.Values{}
			if in.Status != nil {
				q.Set("status", strconv.Itoa(*in.Status))
			}
			if in.Number != "" {
				switch k.key {
				case "invoice":
					q.Set("invoiceNumber", in.Number)
				case "credit_note":
					q.Set("creditNoteNumber", in.Number)
				case "order":
					q.Set("orderNumber", in.Number)
				default:
					return nil, errors.New("vouchers have no number filter; use description_like")
				}
			}
			contactFilter(q, "contact", in.ContactID)
			if s, err := unixStart(in.StartDate); err != nil {
				return nil, err
			} else if s != "" {
				q.Set("startDate", s)
			}
			if s, err := unixEnd(in.EndDate); err != nil {
				return nil, err
			} else if s != "" {
				q.Set("endDate", s)
			}
			if k.key == "voucher" {
				if in.CreditDebit != "" {
					cd := strings.ToUpper(in.CreditDebit)
					if cd != "C" && cd != "D" {
						return nil, errors.New("credit_debit must be C or D")
					}
					q.Set("creditDebit", cd)
				}
				if in.DescriptionLike != "" {
					q.Set("descriptionLike", in.DescriptionLike)
				}
			} else if in.CreditDebit != "" || in.DescriptionLike != "" {
				return nil, errors.New("credit_debit and description_like only apply to list_vouchers")
			}
			return r.listCall(ctx, k.path, q, p, k.key, in.Full, nil)
		})
	r.addGetByID("get_"+k.key, "Get "+k.pdfLabel, "Get one "+k.pdfLabel+" by id (full object). Use list_document_positions for the line items.", k.path+"/%d")
}

// byContact fetches ContactAddress/CommunicationWay items of a contact (filtered client-side as well,
// in case the upstream ignores the filter).
func (r *Registry) byContact(ctx context.Context, path string, contactID int64, extra url.Values) ([]any, error) {
	q := url.Values{}
	for k, v := range extra {
		q[k] = v
	}
	contactFilter(q, "contact", contactID)
	q.Set("limit", strconv.Itoa(maxLimit))
	m, err := r.getJSON(ctx, path, q)
	if err != nil {
		return nil, err
	}
	want := strconv.FormatInt(contactID, 10)
	var out []any
	for _, it := range objectsOf(m) {
		if mm, ok := it.(map[string]any); ok && refID(mm["contact"]) == want {
			out = append(out, it)
		}
	}
	if out == nil {
		out = []any{}
	}
	return out, nil
}

func (r *Registry) listByContact(ctx context.Context, in ByContactInput, path, entity string) (*mcp.CallToolResult, error) {
	if err := needID("contact_id", in.ContactID); err != nil {
		return nil, err
	}
	p, err := r.page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	contactFilter(q, "contact", in.ContactID)
	if in.Type != "" {
		t := strings.ToUpper(in.Type)
		switch t {
		case "EMAIL", "PHONE", "WEB", "MOBILE":
			q.Set("type", t)
		default:
			return nil, errors.New("type must be EMAIL, PHONE, WEB or MOBILE")
		}
	}
	want := strconv.FormatInt(in.ContactID, 10)
	return r.listCall(ctx, path, q, p, entity, in.Full, func(m map[string]any) bool { return refID(m["contact"]) == want })
}

func (r *Registry) getStatus(ctx context.Context, in GetStatusInput) (*mcp.CallToolResult, error) {
	tiers := []string{"read"}
	if r.opts.AllowWrite {
		tiers = append(tiers, "write")
	}
	if r.opts.AllowBooking {
		tiers = append(tiers, "booking")
	}
	source := "server"
	if _, ok := ctx.Value(ctxTokenKey{}).(string); ok {
		source = "client (X-Sevdesk-Token)"
	} else if r.opts.DefaultToken == "" {
		source = "none"
	}
	out := map[string]any{
		"server_version":     r.opts.Version,
		"api_base":           r.client.BaseURL(),
		"enabled_tiers":      tiers,
		"read_only":          !r.opts.AllowWrite,
		"dry_run":            r.client.DryRun(),
		"token_source":       source,
		"client_tokens":      r.opts.AllowClientToken,
		"openapi_spec":       sevdesk.SpecVersion(),
		"indexed_operations": len(sevdesk.Operations()),
		"tier_help":          "write tools need SEVDESK_READ_ONLY=false; booking tools (book/send/enshrine/delete/reset) additionally SEVDESK_ALLOW_BOOKING=true",
	}
	if in.Check == nil || *in.Check {
		m, err := r.getJSON(ctx, "/Tools/bookkeepingSystemVersion", nil)
		if err != nil {
			out["sevdesk_reachable"] = false
			out["sevdesk_error"] = err.Error()
		} else {
			out["sevdesk_reachable"] = true
			out["bookkeeping_system_version"] = m["objects"]
		}
	}
	return valueResult(out)
}

func (r *Registry) guidance(ctx context.Context, in GuidanceInput) (*mcp.CallToolResult, error) {
	path := ""
	q := url.Values{}
	switch strings.ToLower(in.Kind) {
	case "", "all":
		path = "/ReceiptGuidance/forAllAccounts"
	case "revenue":
		path = "/ReceiptGuidance/forRevenue"
	case "expense":
		path = "/ReceiptGuidance/forExpense"
	case "account_number":
		if in.AccountNumber == "" {
			return nil, errors.New("account_number is required for kind=account_number")
		}
		path = "/ReceiptGuidance/forAccountNumber"
		q.Set("accountNumber", in.AccountNumber)
	case "tax_rule":
		if in.TaxRule == "" {
			return nil, errors.New("tax_rule is required for kind=tax_rule")
		}
		path = "/ReceiptGuidance/forTaxRule"
		q.Set("taxRule", in.TaxRule)
	default:
		return nil, errors.New("kind must be all, revenue, expense, account_number or tax_rule")
	}
	m, err := r.getJSON(ctx, path, q)
	if err != nil {
		return nil, err
	}
	return valueResult(m["objects"])
}

func (r *Registry) downloadPDF(ctx context.Context, in PDFInput) (*mcp.CallToolResult, error) {
	k, err := docKindByKey(in.DocumentType, kInvoice, kCreditNote, kOrder)
	if err != nil {
		return nil, err
	}
	if err := needID("id", in.ID); err != nil {
		return nil, err
	}
	q := url.Values{"download": {"true"}, "preventSendBy": {"true"}}
	resp, err := r.do(ctx, sevdesk.Request{Method: "GET", Path: fmt.Sprintf("%s/%d/getPdf", k.path, in.ID), Query: q})
	if err != nil {
		return nil, err
	}
	var env struct {
		Objects struct {
			Filename string `json:"filename"`
			Mimetype string `json:"mimetype"`
			Content  string `json:"content"`
			B64      *bool  `json:"base64Encoded"`
		} `json:"objects"`
	}
	if err := decodeExact(resp.Body, &env); err != nil || env.Objects.Content == "" {
		return nil, errors.New("sevDesk returned no PDF content for this document (is it still a draft without a rendered PDF?)")
	}
	raw := env.Objects.Content
	size := base64.StdEncoding.DecodedLen(len(raw))
	if int64(size) > r.opts.MaxDownloadBytes {
		return nil, fmt.Errorf("PDF is about %d KiB, above SEVDESK_MAX_DOWNLOAD_BYTES (%d KiB)", size>>10, r.opts.MaxDownloadBytes>>10)
	}
	if env.Objects.B64 != nil && !*env.Objects.B64 {
		return nil, errors.New("sevDesk returned PDF content that is not base64 encoded; cannot pass it through safely")
	}
	// The PDF payload is deliberately exempt from the generic output size cap.
	out := map[string]any{"filename": env.Objects.Filename, "mimetype": env.Objects.Mimetype, "size_bytes": size, "content_base64": raw}
	b, err := jsonBytes(out)
	if err != nil {
		return nil, err
	}
	return textResult(b), nil
}
