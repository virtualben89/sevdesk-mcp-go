package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
)

// ---- inputs ----

// ContactFields are the shared contact attributes of create_contact and update_contact.
type ContactFields struct {
	Name             string         `json:"name,omitempty" jsonschema:"Organisation name (makes it an organisation). For persons use first_name and last_name instead"`
	FirstName        string         `json:"first_name,omitempty" jsonschema:"First name of a person (sevDesk field 'surename')"`
	LastName         string         `json:"last_name,omitempty" jsonschema:"Last name of a person"`
	CustomerNumber   string         `json:"customer_number,omitempty" jsonschema:"Customer number; omitted = sevDesk assigns the next free one"`
	CategoryID       int64          `json:"category_id,omitempty" jsonschema:"Contact category id (3 = customer is the sevDesk default; list with sevdesk_call GET /Category?objectType=Contact)"`
	VatNumber        string         `json:"vat_number,omitempty" jsonschema:"VAT id (USt-IdNr.)"`
	TaxNumber        string         `json:"tax_number,omitempty" jsonschema:"Tax number"`
	Description      string         `json:"description,omitempty" jsonschema:"Free text description"`
	Status           int            `json:"status,omitempty" jsonschema:"100 lead, 500 pending, 1000 active"`
	DefaultTimeToPay int            `json:"default_time_to_pay,omitempty" jsonschema:"Payment target in days"`
	Extra            map[string]any `json:"extra,omitempty" jsonschema:"Further sevDesk Contact attributes (camelCase), merged into the request"`
}

// CreateContactInput are the parameters of create_contact.
type CreateContactInput struct {
	ContactFields
}

// UpdateContactInput are the parameters of update_contact.
type UpdateContactInput struct {
	ID int64 `json:"id" jsonschema:"Contact id"`
	ContactFields
}

// CreateAddressInput are the parameters of create_contact_address.
type CreateAddressInput struct {
	ContactID  int64  `json:"contact_id" jsonschema:"Contact id"`
	Street     string `json:"street,omitempty" jsonschema:"Street and house number"`
	Zip        string `json:"zip,omitempty" jsonschema:"Postal code"`
	City       string `json:"city,omitempty" jsonschema:"City"`
	CountryID  int64  `json:"country_id,omitempty" jsonschema:"sevDesk country id (1 = Germany is the default; list with sevdesk_call GET /StaticCountry)"`
	CategoryID int64  `json:"category_id" jsonschema:"Address category id (list with sevdesk_call GET /Category?objectType=ContactAddress)"`
	Name       string `json:"name,omitempty" jsonschema:"Name line of the address"`
	Name2      string `json:"name2,omitempty" jsonschema:"Second name line"`
}

// CreateCommWayInput are the parameters of create_communication_way.
type CreateCommWayInput struct {
	ContactID int64  `json:"contact_id" jsonschema:"Contact id"`
	Type      string `json:"type" jsonschema:"EMAIL, PHONE, WEB or MOBILE"`
	Value     string `json:"value" jsonschema:"The e-mail address, phone number or URL"`
	KeyID     int64  `json:"key_id" jsonschema:"Communication way key id, e.g. work/private (list with sevdesk_call GET /CommunicationWayKey)"`
	Main      bool   `json:"main,omitempty" jsonschema:"Make this the contact's main entry of its type"`
}

// DocPosition is one line item of a new document.
type DocPosition struct {
	Name     string  `json:"name,omitempty" jsonschema:"Item name (required unless part_id is given)"`
	Text     string  `json:"text,omitempty" jsonschema:"Additional description"`
	Quantity float64 `json:"quantity" jsonschema:"Quantity"`
	Price    float64 `json:"price" jsonschema:"Unit price (net or gross, depending on the sevDesk account setting)"`
	TaxRate  float64 `json:"tax_rate" jsonschema:"Tax rate in percent, e.g. 19, 7 or 0"`
	UnityID  int64   `json:"unity_id,omitempty" jsonschema:"Unit id (1 = piece is the default; list with sevdesk_call GET /Unity)"`
	PartID   int64   `json:"part_id,omitempty" jsonschema:"Use an existing part/article"`
	Discount float64 `json:"discount,omitempty" jsonschema:"Position discount in percent"`
}

// CreateDocumentInput are the parameters of create_invoice, create_credit_note and create_order.
type CreateDocumentInput struct {
	ContactID       int64          `json:"contact_id" jsonschema:"Recipient contact id"`
	ContactPersonID int64          `json:"contact_person_id,omitempty" jsonschema:"sevDesk user id acting as contact person; omitted = the only user of the account is used (list with sevdesk_call GET /SevUser)"`
	Date            string         `json:"date" jsonschema:"Document date (YYYY-MM-DD)"`
	DeliveryDate    string         `json:"delivery_date,omitempty" jsonschema:"Delivery/service date (YYYY-MM-DD), defaults to the document date"`
	Number          string         `json:"number,omitempty" jsonschema:"Document number; omitted = sevDesk assigns it"`
	Header          string         `json:"header,omitempty" jsonschema:"Heading, e.g. 'Rechnung Nr. 1'"`
	HeadText        string         `json:"head_text,omitempty" jsonschema:"Intro text above the positions (basic HTML allowed)"`
	FootText        string         `json:"foot_text,omitempty" jsonschema:"Closing text below the positions (basic HTML allowed)"`
	Currency        string         `json:"currency,omitempty" jsonschema:"ISO 4217 code, default EUR"`
	TaxRuleID       int            `json:"tax_rule_id,omitempty" jsonschema:"sevDesk tax rule: 1 taxable revenue (default), 2 EU reverse charge delivery, 3 EU, 4 non-EU, 5 tax-free, 11 small business (§19), 17-21 further rules"`
	TaxText         string         `json:"tax_text,omitempty" jsonschema:"Tax note printed on the document, default 'Umsatzsteuer'"`
	TimeToPay       int            `json:"time_to_pay,omitempty" jsonschema:"Payment target in days"`
	Discount        int            `json:"discount,omitempty" jsonschema:"Overall discount in percent (integer)"`
	InvoiceType     string         `json:"invoice_type,omitempty" jsonschema:"Invoices only: RE normal (default), WKR recurring, SR cancellation, AR partial, TR final"`
	OrderType       string         `json:"order_type,omitempty" jsonschema:"Orders only: AN offer (default), AB order confirmation, LI delivery note"`
	BookingCategory string         `json:"booking_category,omitempty" jsonschema:"Credit notes only: PROVISION, ROYALTY_ASSIGNED, ROYALTY_UNASSIGNED, UNDERACHIEVEMENT or ACCOUNTING_TYPE"`
	CountryID       int64          `json:"country_id,omitempty" jsonschema:"Address country id (1 = Germany default)"`
	Positions       []DocPosition  `json:"positions" jsonschema:"Line items (at least one)"`
	Extra           map[string]any `json:"extra,omitempty" jsonschema:"Further attributes of the document object (camelCase). status, objectName and mapAll cannot be overridden: documents are always created as draft"`
}

// CreatePartInput are the parameters of create_part.
type CreatePartInput struct {
	Name          string  `json:"name" jsonschema:"Part name"`
	PartNumber    string  `json:"part_number" jsonschema:"Part number (unique)"`
	PriceNet      float64 `json:"price_net,omitempty" jsonschema:"Net selling price"`
	PricePurchase float64 `json:"price_purchase,omitempty" jsonschema:"Purchase price"`
	TaxRate       float64 `json:"tax_rate" jsonschema:"Tax rate in percent"`
	Stock         float64 `json:"stock,omitempty" jsonschema:"Initial stock"`
	StockEnabled  bool    `json:"stock_enabled,omitempty" jsonschema:"Track stock"`
	UnityID       int64   `json:"unity_id,omitempty" jsonschema:"Unit id (1 = piece default)"`
	Text          string  `json:"text,omitempty" jsonschema:"Description"`
	InternalNote  string  `json:"internal_comment,omitempty" jsonschema:"Internal comment (not printed)"`
}

// UpdatePartInput are the parameters of update_part.
type UpdatePartInput struct {
	ID            int64    `json:"id" jsonschema:"Part id"`
	Name          string   `json:"name,omitempty" jsonschema:"New name"`
	PartNumber    string   `json:"part_number,omitempty" jsonschema:"New part number"`
	PriceNet      *float64 `json:"price_net,omitempty" jsonschema:"New net price"`
	PricePurchase *float64 `json:"price_purchase,omitempty" jsonschema:"New purchase price"`
	TaxRate       *float64 `json:"tax_rate,omitempty" jsonschema:"New tax rate in percent"`
	Text          string   `json:"text,omitempty" jsonschema:"New description"`
	Status        int      `json:"status,omitempty" jsonschema:"50 inactive, 100 active"`
}

// UploadVoucherFileInput are the parameters of upload_voucher_file.
type UploadVoucherFileInput struct {
	Path          string `json:"path,omitempty" jsonschema:"File inside the server's SEVDESK_UPLOAD_DIR (relative path, or absolute inside it)"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"Alternative to path: the file content, base64 encoded"`
	Filename      string `json:"filename,omitempty" jsonschema:"File name with extension (.pdf .png .jpg .jpeg); needed with content_base64"`
}

// VoucherPosition is one line of a new voucher.
type VoucherPosition struct {
	AccountingTypeID int64   `json:"accounting_type_id,omitempty" jsonschema:"Booking account (AccountingType id); alternative: account_datev_id"`
	AccountDatevID   int64   `json:"account_datev_id,omitempty" jsonschema:"DATEV account id (sevDesk update 2.0)"`
	TaxRate          float64 `json:"tax_rate" jsonschema:"Tax rate in percent"`
	Amount           float64 `json:"amount" jsonschema:"Amount of the position"`
	Net              bool    `json:"net,omitempty" jsonschema:"true: amount is net, false (default): amount is gross"`
	Comment          string  `json:"comment,omitempty" jsonschema:"Comment on the position"`
	IsAsset          bool    `json:"is_asset,omitempty" jsonschema:"Position is a depreciable asset"`
}

// CreateVoucherInput are the parameters of create_voucher.
type CreateVoucherInput struct {
	CreditDebit  string            `json:"credit_debit" jsonschema:"C or D (sevDesk's credit/debit flag of the voucher)"`
	VoucherDate  string            `json:"voucher_date" jsonschema:"Voucher date (YYYY-MM-DD)"`
	SupplierID   int64             `json:"supplier_id,omitempty" jsonschema:"Supplier contact id"`
	SupplierName string            `json:"supplier_name,omitempty" jsonschema:"Supplier name when no contact is used"`
	Description  string            `json:"description,omitempty" jsonschema:"Voucher number / description"`
	PayDate      string            `json:"pay_date,omitempty" jsonschema:"Due date (YYYY-MM-DD)"`
	VoucherType  string            `json:"voucher_type,omitempty" jsonschema:"VOU normal (default) or RV recurring"`
	TaxRuleID    int               `json:"tax_rule_id,omitempty" jsonschema:"sevDesk tax rule id (1 default; 2, 3, 4, 5, 11)"`
	Currency     string            `json:"currency,omitempty" jsonschema:"ISO 4217 code, default EUR"`
	Filename     string            `json:"filename,omitempty" jsonschema:"Attachment: the filename returned by upload_voucher_file"`
	Positions    []VoucherPosition `json:"positions" jsonschema:"Voucher positions (at least one)"`
	Extra        map[string]any    `json:"extra,omitempty" jsonschema:"Further attributes of the voucher object (camelCase). status, objectName and mapAll cannot be overridden: vouchers are always created as draft"`
}

// CreateTransactionInput are the parameters of create_transaction.
type CreateTransactionInput struct {
	CheckAccountID int64   `json:"check_account_id" jsonschema:"Check account id (only for file-import/clearing accounts)"`
	ValueDate      string  `json:"value_date" jsonschema:"Booking day (YYYY-MM-DD)"`
	Amount         float64 `json:"amount" jsonschema:"Amount; negative = outgoing"`
	PayeePayerName string  `json:"payee_payer_name" jsonschema:"Name of the other party"`
	Purpose        string  `json:"purpose,omitempty" jsonschema:"Payment purpose"`
	Account        string  `json:"payee_payer_account,omitempty" jsonschema:"IBAN of the other party"`
}

// BookPaymentInput are the parameters of book_payment.
type BookPaymentInput struct {
	DocumentType   string  `json:"document_type" jsonschema:"invoice, credit_note or voucher"`
	ID             int64   `json:"id" jsonschema:"Document id"`
	Amount         float64 `json:"amount" jsonschema:"Amount to book (may be partial)"`
	Date           string  `json:"date" jsonschema:"Booking day (YYYY-MM-DD)"`
	Type           string  `json:"type,omitempty" jsonschema:"N normal payment (default), CB cashback, CF currency fluctuation, O other, OF reminder fee, MTC mark as paid via transaction"`
	CheckAccountID int64   `json:"check_account_id" jsonschema:"Check account the payment is booked on"`
	TransactionID  int64   `json:"transaction_id,omitempty" jsonschema:"Link an existing bank transaction (list_transactions)"`
	CreateFeed     bool    `json:"create_feed,omitempty" jsonschema:"Create a feed entry for the booking"`
}

// DocumentActionInput are the parameters of document_action.
type DocumentActionInput struct {
	DocumentType string `json:"document_type" jsonschema:"invoice, credit_note, order or voucher"`
	ID           int64  `json:"id" jsonschema:"Document id"`
	Action       string `json:"action" jsonschema:"enshrine (lock permanently, irreversible), mark_sent, reset_to_open, reset_to_draft or cancel (invoices: create cancellation invoice)"`
	SendType     string `json:"send_type,omitempty" jsonschema:"For mark_sent: VPR print, VP post, VM mail, VPDF download (default VPDF)"`
}

// SendEmailInput are the parameters of send_document_email.
type SendEmailInput struct {
	DocumentType string `json:"document_type" jsonschema:"invoice, credit_note or order"`
	ID           int64  `json:"id" jsonschema:"Document id"`
	To           string `json:"to" jsonschema:"Recipient e-mail address"`
	Subject      string `json:"subject" jsonschema:"Subject"`
	Text         string `json:"text" jsonschema:"Mail text (basic HTML allowed)"`
	CC           string `json:"cc,omitempty" jsonschema:"CC addresses, comma separated"`
	BCC          string `json:"bcc,omitempty" jsonschema:"BCC addresses, comma separated"`
	CopyToMe     bool   `json:"copy_to_me,omitempty" jsonschema:"Send a copy to the sevDesk user"`
}

// DeleteObjectInput are the parameters of delete_object.
type DeleteObjectInput struct {
	Entity string `json:"entity" jsonschema:"What to delete"`
	ID     int64  `json:"id" jsonschema:"Object id"`
}

var deletable = map[string]string{
	"contact":                   "/Contact/%d",
	"contact_address":           "/ContactAddress/%d",
	"communication_way":         "/CommunicationWay/%d",
	"credit_note":               "/CreditNote/%d",
	"order":                     "/Order/%d",
	"order_position":            "/OrderPos/%d",
	"check_account":             "/CheckAccount/%d",
	"check_account_transaction": "/CheckAccountTransaction/%d",
	"tag":                       "/Tag/%d",
}

func deletableKeys() []any {
	keys := make([]string, 0, len(deletable))
	for k := range deletable {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

// ---- builders ----

func putIf(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// mergeExtra copies extra attributes into dst without overriding protected keys.
func mergeExtra(dst, extra map[string]any, protected ...string) error {
	for k, v := range extra {
		for _, p := range protected {
			if strings.EqualFold(k, p) {
				return fmt.Errorf("extra.%s cannot be set (it is controlled by the tool; documents are always created as draft)", k)
			}
		}
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}
	return nil
}

func contactBody(f ContactFields, create bool) (map[string]any, error) {
	b := map[string]any{}
	putIf(b, "name", f.Name)
	putIf(b, "surename", f.FirstName)
	putIf(b, "familyname", f.LastName)
	putIf(b, "customerNumber", f.CustomerNumber)
	putIf(b, "vatNumber", f.VatNumber)
	putIf(b, "taxNumber", f.TaxNumber)
	putIf(b, "description", f.Description)
	if f.Status != 0 {
		b["status"] = f.Status
	}
	if f.DefaultTimeToPay > 0 {
		b["defaultTimeToPay"] = f.DefaultTimeToPay
	}
	if f.CategoryID > 0 {
		b["category"] = ref("Category", f.CategoryID)
	}
	if create {
		if f.Name == "" && f.LastName == "" {
			return nil, errors.New("give name (organisation) or last_name (person)")
		}
		if f.Name != "" && (f.FirstName != "" || f.LastName != "") {
			return nil, errors.New("use either name (organisation) or first_name/last_name (person), not both")
		}
		if _, ok := b["category"]; !ok {
			b["category"] = ref("Category", 3)
		}
	}
	if err := mergeExtra(b, f.Extra); err != nil {
		return nil, err
	}
	return b, nil
}

func (r *Registry) contactPerson(ctx context.Context, id int64) (int64, error) {
	if id > 0 {
		return id, nil
	}
	m, err := r.getJSON(ctx, "/SevUser", nil)
	if err != nil {
		return 0, fmt.Errorf("contact_person_id not given and the user list could not be read (%w); pass contact_person_id", err)
	}
	items := objectsOf(m)
	if len(items) != 1 {
		var ids []string
		for _, it := range items {
			if mm, ok := it.(map[string]any); ok {
				ids = append(ids, refID(mm)+" ("+fmt.Sprint(mm["fullname"])+")")
			}
		}
		return 0, fmt.Errorf("contact_person_id is required: the account has %d users (%s)", len(items), strings.Join(ids, ", "))
	}
	var n int64
	if _, err := fmt.Sscan(refID(items[0]), &n); err != nil || n <= 0 {
		return 0, errors.New("could not read the user id; pass contact_person_id")
	}
	return n, nil
}

type docSpec struct {
	kind     docKind
	factory  string // path of the Factory/save endpoint
	key      string // body key of the document
	posKey   string
	posObj   string
	dateKey  string
	numKey   string
	delivery bool
}

var (
	specInvoice    = docSpec{kInvoice, "/Invoice/Factory/saveInvoice", "invoice", "invoicePosSave", "InvoicePos", "invoiceDate", "invoiceNumber", true}
	specCreditNote = docSpec{kCreditNote, "/CreditNote/Factory/saveCreditNote", "creditNote", "creditNotePosSave", "CreditNotePos", "creditNoteDate", "creditNoteNumber", true}
	specOrder      = docSpec{kOrder, "/Order/Factory/saveOrder", "order", "orderPosSave", "OrderPos", "orderDate", "orderNumber", false}
)

func (r *Registry) createDocument(ctx context.Context, sp docSpec, in CreateDocumentInput) (*mcp.CallToolResult, error) {
	if err := needID("contact_id", in.ContactID); err != nil {
		return nil, err
	}
	if len(in.Positions) == 0 {
		return nil, errors.New("positions: at least one line item is required")
	}
	if len(in.Positions) > 200 {
		return nil, errors.New("positions: at most 200 line items per call")
	}
	date, err := germanDate(in.Date)
	if err != nil || date == "" {
		return nil, fmt.Errorf("date is required (YYYY-MM-DD): %v", err)
	}
	delivery := date
	if in.DeliveryDate != "" {
		if delivery, err = germanDate(in.DeliveryDate); err != nil {
			return nil, err
		}
	}
	switch {
	case sp.kind.key != "invoice" && in.InvoiceType != "":
		return nil, errors.New("invoice_type only applies to create_invoice")
	case sp.kind.key != "order" && in.OrderType != "":
		return nil, errors.New("order_type only applies to create_order")
	case sp.kind.key != "credit_note" && in.BookingCategory != "":
		return nil, errors.New("booking_category only applies to create_credit_note")
	}
	person, err := r.contactPerson(ctx, in.ContactPersonID)
	if err != nil {
		return nil, err
	}
	ccy := strings.ToUpper(strings.TrimSpace(in.Currency))
	if ccy == "" {
		ccy = "EUR"
	}
	taxRule := in.TaxRuleID
	if taxRule == 0 {
		taxRule = 1
	}
	taxText := in.TaxText
	if taxText == "" {
		taxText = "Umsatzsteuer"
	}
	country := in.CountryID
	if country == 0 {
		country = 1
	}
	doc := map[string]any{
		"objectName":     sp.kind.object,
		"mapAll":         true,
		"contact":        ref("Contact", in.ContactID),
		"contactPerson":  ref("SevUser", person),
		sp.dateKey:       date,
		"status":         100, // draft, always
		"addressCountry": ref("StaticCountry", country),
		"taxRate":        0,
		"taxRule":        map[string]any{"id": fmt.Sprint(taxRule), "objectName": "TaxRule"},
		"taxText":        taxText,
		"currency":       ccy,
		"discount":       in.Discount,
	}
	if sp.delivery {
		doc["deliveryDate"] = delivery
	}
	putIf(doc, sp.numKey, in.Number)
	putIf(doc, "header", in.Header)
	putIf(doc, "headText", in.HeadText)
	putIf(doc, "footText", in.FootText)
	if in.TimeToPay > 0 {
		doc["timeToPay"] = in.TimeToPay
	}
	switch sp.kind.key {
	case "invoice":
		t := strings.ToUpper(in.InvoiceType)
		if t == "" {
			t = "RE"
		}
		doc["invoiceType"] = t
	case "order":
		t := strings.ToUpper(in.OrderType)
		if t == "" {
			t = "AN"
		}
		doc["orderType"] = t
		doc["version"] = 0
	case "credit_note":
		putIf(doc, "bookingCategory", strings.ToUpper(in.BookingCategory))
	}
	if err := mergeExtra(doc, in.Extra, "status", "objectName", "mapAll", "enshrined", "id"); err != nil {
		return nil, err
	}
	pos := make([]map[string]any, 0, len(in.Positions))
	for i, p := range in.Positions {
		if p.Name == "" && p.PartID == 0 {
			return nil, fmt.Errorf("positions[%d]: name or part_id is required", i)
		}
		if p.Quantity <= 0 {
			return nil, fmt.Errorf("positions[%d]: quantity must be > 0", i)
		}
		if p.TaxRate < 0 || p.TaxRate > 100 {
			return nil, fmt.Errorf("positions[%d]: tax_rate must be 0-100", i)
		}
		u := p.UnityID
		if u == 0 {
			u = 1
		}
		e := map[string]any{
			"objectName": sp.posObj, "mapAll": true, "quantity": p.Quantity, "price": p.Price,
			"taxRate": p.TaxRate, "unity": ref("Unity", u), "positionNumber": i,
		}
		putIf(e, "name", p.Name)
		putIf(e, "text", p.Text)
		if p.PartID > 0 {
			e["part"] = ref("Part", p.PartID)
		}
		if p.Discount != 0 {
			e["discount"] = p.Discount
		}
		pos = append(pos, e)
	}
	body := map[string]any{sp.key: doc, sp.posKey: pos}
	return r.send(ctx, http.MethodPost, sp.factory, body)
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func (r *Registry) createVoucher(ctx context.Context, in CreateVoucherInput) (*mcp.CallToolResult, error) {
	cd := strings.ToUpper(in.CreditDebit)
	if cd != "C" && cd != "D" {
		return nil, errors.New("credit_debit must be C or D")
	}
	date, err := germanDate(in.VoucherDate)
	if err != nil || date == "" {
		return nil, fmt.Errorf("voucher_date is required (YYYY-MM-DD): %v", err)
	}
	if len(in.Positions) == 0 {
		return nil, errors.New("positions: at least one position is required")
	}
	if in.SupplierID == 0 && in.SupplierName == "" {
		return nil, errors.New("give supplier_id or supplier_name")
	}
	vt := strings.ToUpper(in.VoucherType)
	if vt == "" {
		vt = "VOU"
	}
	taxRule := in.TaxRuleID
	if taxRule == 0 {
		taxRule = 1
	}
	ccy := strings.ToUpper(strings.TrimSpace(in.Currency))
	if ccy == "" {
		ccy = "EUR"
	}
	v := map[string]any{
		"objectName": "Voucher", "mapAll": true, "status": 50, // draft, always
		"creditDebit": cd, "voucherType": vt, "voucherDate": date, "currency": ccy,
		"taxRule": map[string]any{"id": fmt.Sprint(taxRule), "objectName": "TaxRule"},
	}
	if in.SupplierID > 0 {
		v["supplier"] = ref("Contact", in.SupplierID)
	}
	putIf(v, "supplierName", in.SupplierName)
	putIf(v, "description", in.Description)
	if in.PayDate != "" {
		pd, err := germanDate(in.PayDate)
		if err != nil {
			return nil, err
		}
		v["payDate"] = pd
	}
	if err := mergeExtra(v, in.Extra, "status", "objectName", "mapAll", "enshrined", "id"); err != nil {
		return nil, err
	}
	pos := make([]map[string]any, 0, len(in.Positions))
	for i, p := range in.Positions {
		if p.AccountingTypeID == 0 && p.AccountDatevID == 0 {
			return nil, fmt.Errorf("positions[%d]: accounting_type_id or account_datev_id is required (see list_accounting_guidance)", i)
		}
		if p.Amount == 0 {
			return nil, fmt.Errorf("positions[%d]: amount must not be 0", i)
		}
		if p.TaxRate < 0 || p.TaxRate > 100 {
			return nil, fmt.Errorf("positions[%d]: tax_rate must be 0-100", i)
		}
		var net, gross float64
		if p.Net {
			net, gross = p.Amount, round2(p.Amount*(1+p.TaxRate/100))
		} else {
			gross, net = p.Amount, round2(p.Amount/(1+p.TaxRate/100))
		}
		e := map[string]any{
			"objectName": "VoucherPos", "mapAll": true, "taxRate": p.TaxRate, "net": p.Net,
			"sumNet": net, "sumGross": gross, "isAsset": p.IsAsset,
		}
		if p.AccountingTypeID > 0 {
			e["accountingType"] = ref("AccountingType", p.AccountingTypeID)
		}
		if p.AccountDatevID > 0 {
			e["accountDatev"] = ref("AccountDatev", p.AccountDatevID)
		}
		putIf(e, "comment", p.Comment)
		pos = append(pos, e)
	}
	body := map[string]any{"voucher": v, "voucherPosSave": pos}
	if in.Filename != "" {
		if !safeUploadName.MatchString(in.Filename) {
			return nil, errors.New("filename must be the value returned by upload_voucher_file")
		}
		body["filename"] = in.Filename
	}
	return r.send(ctx, http.MethodPost, "/Voucher/Factory/saveVoucher", body)
}

// safeUploadName matches sevDesk's temp file names (hash + extension) and our sanitised names.
var safeUploadName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var (
	uploadNameClean = regexp.MustCompile(`[^A-Za-z0-9._ -]`)
	allowedUploads  = map[string]func([]byte) bool{
		".pdf":  func(b []byte) bool { return len(b) > 5 && string(b[:5]) == "%PDF-" },
		".png":  func(b []byte) bool { return len(b) > 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" },
		".jpg":  func(b []byte) bool { return len(b) > 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff },
		".jpeg": func(b []byte) bool { return len(b) > 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff },
	}
)

func (r *Registry) uploadVoucherFile(ctx context.Context, in UploadVoucherFileInput) (*mcp.CallToolResult, error) {
	if (in.Path == "") == (in.ContentBase64 == "") {
		return nil, errors.New("give exactly one of path (inside SEVDESK_UPLOAD_DIR) or content_base64")
	}
	var data []byte
	name := in.Filename
	if in.Path != "" {
		if r.opts.UploadDir == "" {
			return nil, errors.New("file paths are disabled: the operator has not set SEVDESK_UPLOAD_DIR. Use content_base64 + filename instead")
		}
		root, err := os.OpenRoot(r.opts.UploadDir)
		if err != nil {
			return nil, fmt.Errorf("opening SEVDESK_UPLOAD_DIR: %w", err)
		}
		defer root.Close()
		rel := in.Path
		if filepath.IsAbs(rel) {
			abs, err := filepath.Abs(r.opts.UploadDir)
			if err != nil {
				return nil, err
			}
			if rel, err = filepath.Rel(abs, filepath.Clean(rel)); err != nil || strings.HasPrefix(rel, "..") {
				return nil, errors.New("path is outside SEVDESK_UPLOAD_DIR")
			}
		}
		f, err := root.Open(rel) // os.Root refuses ".." and symlinks leaving the directory
		if err != nil {
			return nil, errors.New("cannot open file inside SEVDESK_UPLOAD_DIR (missing, or path escapes the directory)")
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			return nil, errors.New("path is not a regular file")
		}
		if st.Size() > r.opts.MaxUploadBytes {
			return nil, fmt.Errorf("file is %d KiB, above SEVDESK_MAX_UPLOAD_BYTES (%d KiB)", st.Size()>>10, r.opts.MaxUploadBytes>>10)
		}
		if data, err = io.ReadAll(io.LimitReader(f, r.opts.MaxUploadBytes+1)); err != nil {
			return nil, err
		}
		if name == "" {
			name = filepath.Base(rel)
		}
	} else {
		if name == "" {
			return nil, errors.New("filename (with extension) is required with content_base64")
		}
		if int64(base64.StdEncoding.DecodedLen(len(in.ContentBase64))) > r.opts.MaxUploadBytes+3 {
			return nil, fmt.Errorf("content is larger than SEVDESK_MAX_UPLOAD_BYTES (%d KiB)", r.opts.MaxUploadBytes>>10)
		}
		var err error
		if data, err = base64.StdEncoding.DecodeString(in.ContentBase64); err != nil {
			return nil, errors.New("content_base64 is not valid base64")
		}
	}
	if int64(len(data)) > r.opts.MaxUploadBytes {
		return nil, fmt.Errorf("file is larger than SEVDESK_MAX_UPLOAD_BYTES (%d KiB)", r.opts.MaxUploadBytes>>10)
	}
	if len(data) == 0 {
		return nil, errors.New("file is empty")
	}
	name = uploadNameClean.ReplaceAllString(filepath.Base(name), "_")
	if len(name) > 100 {
		name = name[len(name)-100:]
	}
	ext := strings.ToLower(filepath.Ext(name))
	check, ok := allowedUploads[ext]
	if !ok {
		return nil, errors.New("only .pdf, .png, .jpg and .jpeg files can be uploaded")
	}
	if !check(data) {
		return nil, fmt.Errorf("file content does not look like a %s file", ext)
	}
	resp, err := r.do(ctx, sevdesk.Request{Method: http.MethodPost, Path: "/Voucher/Factory/uploadTempFile",
		File: &sevdesk.File{Field: "file", Filename: name, Data: data}})
	if err != nil {
		return nil, err
	}
	return r.writeResult(resp)
}

func (r *Registry) registerWrite() {
	add(r, toolDef{name: "create_contact", title: "Create contact", class: sevdesk.ClassWrite,
		description: "Create a customer/supplier. Organisation: name. Person: first_name + last_name. Category defaults to 3 (customer). Add addresses and phone/e-mail with create_contact_address and create_communication_way."},
		func(ctx context.Context, in CreateContactInput) (*mcp.CallToolResult, error) {
			b, err := contactBody(in.ContactFields, true)
			if err != nil {
				return nil, err
			}
			return r.send(ctx, http.MethodPost, "/Contact", b)
		})
	add(r, toolDef{name: "update_contact", title: "Update contact", class: sevdesk.ClassWrite, destructive: true, idempotent: true,
		description: "Change attributes of a contact; only the given fields are sent. Overwrites existing values."},
		func(ctx context.Context, in UpdateContactInput) (*mcp.CallToolResult, error) {
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			b, err := contactBody(in.ContactFields, false)
			if err != nil {
				return nil, err
			}
			if len(b) == 0 {
				return nil, errors.New("nothing to update: give at least one field")
			}
			return r.send(ctx, http.MethodPut, fmt.Sprintf("/Contact/%d", in.ID), b)
		})
	add(r, toolDef{name: "create_contact_address", title: "Create contact address", class: sevdesk.ClassWrite,
		description: "Add a postal address to a contact."},
		func(ctx context.Context, in CreateAddressInput) (*mcp.CallToolResult, error) {
			if err := needID("contact_id", in.ContactID); err != nil {
				return nil, err
			}
			if err := needID("category_id", in.CategoryID); err != nil {
				return nil, err
			}
			country := in.CountryID
			if country == 0 {
				country = 1
			}
			b := map[string]any{"contact": ref("Contact", in.ContactID), "category": ref("Category", in.CategoryID), "country": ref("StaticCountry", country)}
			putIf(b, "street", in.Street)
			putIf(b, "zip", in.Zip)
			putIf(b, "city", in.City)
			putIf(b, "name", in.Name)
			putIf(b, "name2", in.Name2)
			return r.send(ctx, http.MethodPost, "/ContactAddress", b)
		})
	add(r, toolDef{name: "create_communication_way", title: "Create communication way", class: sevdesk.ClassWrite,
		enums:       map[string][]any{"type": {"EMAIL", "PHONE", "WEB", "MOBILE"}},
		description: "Add an e-mail address, phone number or website to a contact."},
		func(ctx context.Context, in CreateCommWayInput) (*mcp.CallToolResult, error) {
			if err := needID("contact_id", in.ContactID); err != nil {
				return nil, err
			}
			if err := needID("key_id", in.KeyID); err != nil {
				return nil, err
			}
			t := strings.ToUpper(in.Type)
			if t != "EMAIL" && t != "PHONE" && t != "WEB" && t != "MOBILE" {
				return nil, errors.New("type must be EMAIL, PHONE, WEB or MOBILE")
			}
			if strings.TrimSpace(in.Value) == "" {
				return nil, errors.New("value is required")
			}
			b := map[string]any{"contact": ref("Contact", in.ContactID), "type": t, "value": in.Value, "key": ref("CommunicationWayKey", in.KeyID), "main": in.Main}
			return r.send(ctx, http.MethodPost, "/CommunicationWay", b)
		})

	docDesc := func(what string) string {
		return "Create a " + what + " as DRAFT (status 100) with line items. The document is never finalised here; sending, enshrining and booking need the booking tier (document_action, send_document_email, book_payment). If contact_person_id is omitted and the account has exactly one user, that user is used."
	}
	add(r, toolDef{name: "create_invoice", title: "Create invoice (draft)", class: sevdesk.ClassWrite, description: docDesc("invoice")},
		func(ctx context.Context, in CreateDocumentInput) (*mcp.CallToolResult, error) {
			return r.createDocument(ctx, specInvoice, in)
		})
	add(r, toolDef{name: "create_credit_note", title: "Create credit note (draft)", class: sevdesk.ClassWrite, description: docDesc("credit note")},
		func(ctx context.Context, in CreateDocumentInput) (*mcp.CallToolResult, error) {
			return r.createDocument(ctx, specCreditNote, in)
		})
	add(r, toolDef{name: "create_order", title: "Create order/offer (draft)", class: sevdesk.ClassWrite, description: docDesc("order or offer (order_type AN = offer, AB = confirmation, LI = delivery note)")},
		func(ctx context.Context, in CreateDocumentInput) (*mcp.CallToolResult, error) {
			return r.createDocument(ctx, specOrder, in)
		})

	add(r, toolDef{name: "create_part", title: "Create part (article)", class: sevdesk.ClassWrite,
		description: "Create a part/article in the sevDesk inventory."},
		func(ctx context.Context, in CreatePartInput) (*mcp.CallToolResult, error) {
			if in.Name == "" || in.PartNumber == "" {
				return nil, errors.New("name and part_number are required")
			}
			u := in.UnityID
			if u == 0 {
				u = 1
			}
			b := map[string]any{"name": in.Name, "partNumber": in.PartNumber, "taxRate": in.TaxRate, "stock": in.Stock,
				"stockEnabled": in.StockEnabled, "unity": ref("Unity", u), "status": 100}
			if in.PriceNet != 0 {
				b["priceNet"] = in.PriceNet
			}
			if in.PricePurchase != 0 {
				b["pricePurchase"] = in.PricePurchase
			}
			putIf(b, "text", in.Text)
			putIf(b, "internalComment", in.InternalNote)
			return r.send(ctx, http.MethodPost, "/Part", b)
		})
	add(r, toolDef{name: "update_part", title: "Update part", class: sevdesk.ClassWrite, destructive: true, idempotent: true,
		description: "Change a part; only the given fields are sent. Overwrites existing values."},
		func(ctx context.Context, in UpdatePartInput) (*mcp.CallToolResult, error) {
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			b := map[string]any{}
			putIf(b, "name", in.Name)
			putIf(b, "partNumber", in.PartNumber)
			putIf(b, "text", in.Text)
			if in.PriceNet != nil {
				b["priceNet"] = *in.PriceNet
			}
			if in.PricePurchase != nil {
				b["pricePurchase"] = *in.PricePurchase
			}
			if in.TaxRate != nil {
				b["taxRate"] = *in.TaxRate
			}
			if in.Status != 0 {
				if in.Status != 50 && in.Status != 100 {
					return nil, errors.New("status must be 50 (inactive) or 100 (active)")
				}
				b["status"] = in.Status
			}
			if len(b) == 0 {
				return nil, errors.New("nothing to update: give at least one field")
			}
			return r.send(ctx, http.MethodPut, fmt.Sprintf("/Part/%d", in.ID), b)
		})

	add(r, toolDef{name: "upload_voucher_file", title: "Upload voucher file", class: sevdesk.ClassWrite,
		description: "Upload a receipt (.pdf/.png/.jpg) to sevDesk's temporary storage and return its sevDesk filename for create_voucher(filename). Input is either path (inside the operator's SEVDESK_UPLOAD_DIR) or content_base64 + filename. Size is bounded by SEVDESK_MAX_UPLOAD_BYTES."},
		r.uploadVoucherFile)
	add(r, toolDef{name: "create_voucher", title: "Create voucher (draft)", class: sevdesk.ClassWrite,
		description: "Create a voucher (Beleg / incoming document) as DRAFT (status 50) with positions. Find booking accounts with list_accounting_guidance; attach a file via upload_voucher_file first. Booking/enshrining needs the booking tier."},
		r.createVoucher)
	add(r, toolDef{name: "create_transaction", title: "Create bank transaction", class: sevdesk.ClassWrite,
		description: "Create a bank transaction on a check account. sevDesk only allows this for file-import/clearing accounts, not for FinTS/online-banking accounts."},
		func(ctx context.Context, in CreateTransactionInput) (*mcp.CallToolResult, error) {
			if err := needID("check_account_id", in.CheckAccountID); err != nil {
				return nil, err
			}
			d, err := germanDate(in.ValueDate)
			if err != nil || d == "" {
				return nil, fmt.Errorf("value_date is required (YYYY-MM-DD): %v", err)
			}
			if in.Amount == 0 || in.PayeePayerName == "" {
				return nil, errors.New("amount (non-zero) and payee_payer_name are required")
			}
			b := map[string]any{"checkAccount": ref("CheckAccount", in.CheckAccountID), "valueDate": d, "amount": in.Amount,
				"payeePayerName": in.PayeePayerName, "status": 100}
			putIf(b, "paymtPurpose", in.Purpose)
			putIf(b, "payeePayerAcctNo", in.Account)
			return r.send(ctx, http.MethodPost, "/CheckAccountTransaction", b)
		})
}

func (r *Registry) registerBooking() {
	add(r, toolDef{name: "book_payment", title: "Book payment on a document", class: sevdesk.ClassBooking, destructive: true,
		enums:       map[string][]any{"document_type": kindEnum(kInvoice, kCreditNote, kVoucher), "type": {"N", "CB", "CF", "O", "OF", "MTC", "FULL_PAYMENT"}},
		description: "Book a payment (full or partial) against an invoice, credit note or voucher using a check account (and optionally an existing bank transaction). This changes the paid status and cannot be undone with a tool here (document_action reset_to_open may revert)."},
		func(ctx context.Context, in BookPaymentInput) (*mcp.CallToolResult, error) {
			k, err := docKindByKey(in.DocumentType, kInvoice, kCreditNote, kVoucher)
			if err != nil {
				return nil, err
			}
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			if err := needID("check_account_id", in.CheckAccountID); err != nil {
				return nil, err
			}
			if in.Amount <= 0 {
				return nil, errors.New("amount must be > 0")
			}
			t := strings.ToUpper(in.Type)
			if t == "" {
				t = "N"
			}
			b := map[string]any{"amount": in.Amount, "type": t, "checkAccount": ref("CheckAccount", in.CheckAccountID), "createFeed": in.CreateFeed}
			if k.key == "voucher" {
				d, err := germanDate(in.Date)
				if err != nil || d == "" {
					return nil, fmt.Errorf("date is required (YYYY-MM-DD): %v", err)
				}
				b["date"] = d
			} else {
				d, err := unixInt(in.Date)
				if err != nil || d == 0 {
					return nil, fmt.Errorf("date is required (YYYY-MM-DD): %v", err)
				}
				b["date"] = d
			}
			if in.TransactionID > 0 {
				b["checkAccountTransaction"] = ref("CheckAccountTransaction", in.TransactionID)
			}
			return r.send(ctx, http.MethodPut, fmt.Sprintf("%s/%d/bookAmount", k.path, in.ID), b)
		})

	add(r, toolDef{name: "document_action", title: "Change document status", class: sevdesk.ClassBooking, destructive: true,
		enums:       map[string][]any{"document_type": kindEnum(allDocKinds...), "action": {"enshrine", "mark_sent", "reset_to_open", "reset_to_draft", "cancel"}, "send_type": {"VPR", "VP", "VM", "VPDF"}},
		description: "Finalise or revert a document: enshrine (permanently lock, cannot be undone), mark_sent (invoices, credit notes, orders), reset_to_open / reset_to_draft (invoices, credit notes, vouchers), cancel (invoices: creates a cancellation invoice). Use only when the user asked for exactly this step."},
		func(ctx context.Context, in DocumentActionInput) (*mcp.CallToolResult, error) {
			return r.documentAction(ctx, in)
		})

	add(r, toolDef{name: "send_document_email", title: "Send document by e-mail", class: sevdesk.ClassBooking, destructive: true,
		enums:       map[string][]any{"document_type": kindEnum(kInvoice, kCreditNote, kOrder)},
		description: "E-mail an invoice, credit note or order to a customer through sevDesk. This marks it as sent and, for invoices, locks it against further changes. Cannot be recalled."},
		func(ctx context.Context, in SendEmailInput) (*mcp.CallToolResult, error) {
			k, err := docKindByKey(in.DocumentType, kInvoice, kCreditNote, kOrder)
			if err != nil {
				return nil, err
			}
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			if !strings.Contains(in.To, "@") || strings.ContainsAny(in.To, " \r\n,;<>") {
				return nil, errors.New("to must be a single e-mail address")
			}
			if in.Subject == "" || in.Text == "" {
				return nil, errors.New("subject and text are required")
			}
			b := map[string]any{"toEmail": in.To, "subject": in.Subject, "text": in.Text, "copy": in.CopyToMe}
			putIf(b, "ccEmail", in.CC)
			putIf(b, "bccEmail", in.BCC)
			return r.send(ctx, http.MethodPost, fmt.Sprintf("%s/%d/sendViaEmail", k.path, in.ID), b)
		})

	add(r, toolDef{name: "delete_object", title: "Delete object", class: sevdesk.ClassBooking, destructive: true,
		enums:       map[string][]any{"entity": deletableKeys()},
		description: "Delete an object permanently (contact, address, communication way, credit note, order, order position, check account, bank transaction, tag). Invoices and vouchers cannot be deleted this way: cancel invoices with document_action. sevDesk refuses deleting enshrined or booked objects."},
		func(ctx context.Context, in DeleteObjectInput) (*mcp.CallToolResult, error) {
			f, ok := deletable[in.Entity]
			if !ok {
				return nil, fmt.Errorf("unknown entity %q", in.Entity)
			}
			if err := needID("id", in.ID); err != nil {
				return nil, err
			}
			return r.send(ctx, http.MethodDelete, fmt.Sprintf(f, in.ID), nil)
		})
}

func (r *Registry) documentAction(ctx context.Context, in DocumentActionInput) (*mcp.CallToolResult, error) {
	k, err := docKindByKey(in.DocumentType)
	if err != nil {
		return nil, err
	}
	if err := needID("id", in.ID); err != nil {
		return nil, err
	}
	base := fmt.Sprintf("%s/%d", k.path, in.ID)
	switch in.Action {
	case "enshrine":
		return r.send(ctx, http.MethodPut, base+"/enshrine", nil)
	case "mark_sent":
		if k.key == "voucher" {
			return nil, errors.New("vouchers cannot be marked as sent")
		}
		st := in.SendType
		if st == "" {
			st = "VPDF"
		}
		switch st {
		case "VPR", "VP", "VM", "VPDF":
		default:
			return nil, errors.New("send_type must be VPR, VP, VM or VPDF")
		}
		b := map[string]any{"sendType": st}
		if k.key != "order" {
			b["sendDraft"] = false
		}
		return r.send(ctx, http.MethodPut, base+"/sendBy", b)
	case "reset_to_open", "reset_to_draft":
		if k.key == "order" {
			return nil, errors.New("orders have no reset action")
		}
		suffix := "/resetToOpen"
		if in.Action == "reset_to_draft" {
			suffix = "/resetToDraft"
		}
		return r.send(ctx, http.MethodPut, base+suffix, nil)
	case "cancel":
		if k.key != "invoice" {
			return nil, errors.New("cancel only applies to invoices")
		}
		return r.send(ctx, http.MethodPost, base+"/cancelInvoice", nil)
	}
	return nil, errors.New("action must be enshrine, mark_sent, reset_to_open, reset_to_draft or cancel")
}
