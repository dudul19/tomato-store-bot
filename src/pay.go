package main

// Payment gateways. All three are used by polling their status API, so the
// bot needs no webhook, domain or open port, and the gateway callback URL
// stays free for other apps (e.g. a store website).
//
//   AutoGoPay  QRIS                      https://autogopay.site
//   Midtrans   QRIS (Core API)           https://docs.midtrans.com
//   Pakasir    QRIS + Virtual Accounts   https://pakasir.com

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	payPending = "pending"
	payPaid    = "paid"
	payFailed  = "failed"
)

// Charge — what the buyer must pay.
type Charge struct {
	TrxID     string // gateway transaction id (unique)
	Total     int64  // amount the buyer pays (amount + fee)
	Fee       int64
	Code      string // QRIS string or VA number
	URL       string // checkout page, optional
	ExpiresAt int64
}

type Gateway interface {
	ID() string
	Label() string
	Enabled() bool
	Methods() []string
	// Create a payment. amount = balance credited; the fee is added by
	// the gateway (Pakasir) or by feeFor (others).
	Create(ctx context.Context, orderID, method string, amount int64) (*Charge, error)
	Status(ctx context.Context, orderID, trxID string, total int64) (string, error)
	Cancel(ctx context.Context, orderID, trxID string, total int64) error
	Test(ctx context.Context) error
}

var methodLabel = map[string]string{
	"qris":           "QRIS",
	"bri_va":         "BRI VA",
	"bni_va":         "BNI VA",
	"permata_va":     "Permata VA",
	"cimb_niaga_va":  "CIMB Niaga VA",
	"maybank_va":     "Maybank VA",
	"sampoerna_va":   "Sampoerna VA",
	"bnc_va":         "BNC VA",
	"atm_bersama_va": "ATM Bersama VA",
	"artha_graha_va": "Artha Graha VA",
}

var pakasirMethods = []string{"qris", "bri_va", "bni_va", "permata_va", "cimb_niaga_va", "maybank_va", "sampoerna_va", "bnc_va", "atm_bersama_va", "artha_graha_va"}

type Payments struct {
	s   *Settings
	gws []Gateway
}

func newPayments(s *Settings) *Payments {
	hc := &http.Client{Timeout: 20 * time.Second}
	return &Payments{s: s, gws: []Gateway{
		&AutoGoPay{s: s, hc: hc},
		&Midtrans{s: s, hc: hc},
		&Pakasir{s: s, hc: hc},
	}}
}

func (p *Payments) Get(id string) Gateway {
	for _, g := range p.gws {
		if g.ID() == id {
			return g
		}
	}
	return nil
}

func (p *Payments) Active() []Gateway {
	var out []Gateway
	for _, g := range p.gws {
		if g.Enabled() {
			out = append(out, g)
		}
	}
	return out
}

// feeFor — fee added on top of the amount for gateways that charge a
// percentage (configurable, rounded up). 0 when the store absorbs it.
func (p *Payments) feeFor(gw string, amount int64) int64 {
	if !p.s.Bool("fee_to_buyer") {
		return 0
	}
	pct := p.s.Float("gw_" + gw + "_fee")
	if pct <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(amount) * pct / 100))
}

// ---------------------------------------------------------------- helpers

func httpJSON(ctx context.Context, hc *http.Client, method, u string, hdr map[string]string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, unwrapURLErr(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			s := strings.TrimSpace(string(raw))
			if len(s) > 160 {
				s = s[:160]
			}
			return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, s)
		}
	}
	return resp.StatusCode, nil
}

// wibTime parses "2026-11-08 22:41:00" (WIB) or RFC3339.
func wibTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Unix()
	}
	wib := time.FixedZone("WIB", 7*3600)
	for _, f := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(f, s, wib); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func anyInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(math.Round(x))
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return int64(math.Round(f))
	case json.Number:
		f, _ := x.Float64()
		return int64(math.Round(f))
	}
	return 0
}

func anyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// ---------------------------------------------------------------- AutoGoPay

type AutoGoPay struct {
	s  *Settings
	hc *http.Client
}

func (g *AutoGoPay) ID() string        { return "autogopay" }
func (g *AutoGoPay) Label() string     { return "AutoGoPay" }
func (g *AutoGoPay) Methods() []string { return []string{"qris"} }
func (g *AutoGoPay) Enabled() bool {
	return g.s.Bool("gw_autogopay_on") && g.s.Str("gw_autogopay_key") != ""
}

func (g *AutoGoPay) call(ctx context.Context, path string, body any) (map[string]any, error) {
	key := g.s.Str("gw_autogopay_key")
	if key == "" {
		return nil, errors.New("AutoGoPay: API key is empty")
	}
	base := strings.TrimRight(g.s.Str("gw_autogopay_url"), "/")
	var r map[string]any
	code, err := httpJSON(ctx, g.hc, "POST", base+path, map[string]string{"Authorization": "Bearer " + key}, body, &r)
	if err != nil {
		return nil, fmt.Errorf("AutoGoPay: %w", err)
	}
	if code >= 400 || r["success"] == false {
		msg := anyStr(r["message"])
		if msg == "" {
			msg = anyStr(r["error"])
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", code)
		}
		return nil, errors.New("AutoGoPay: " + msg)
	}
	return r, nil
}

func (g *AutoGoPay) Create(ctx context.Context, orderID, method string, amount int64) (*Charge, error) {
	want := amount + (&Payments{s: g.s}).feeFor("autogopay", amount)
	r, err := g.call(ctx, "/qris/generate", map[string]any{"amount": want})
	if err != nil {
		return nil, err
	}
	d, _ := r["data"].(map[string]any)
	if d == nil || anyStr(d["transaction_id"]) == "" || anyStr(d["qr_string"]) == "" {
		return nil, errors.New("AutoGoPay: response without transaction_id/qr_string")
	}
	total := anyInt(d["amount"])
	if total < want {
		total = want
	}
	return &Charge{
		TrxID:     anyStr(d["transaction_id"]),
		Total:     total,
		Fee:       total - amount,
		Code:      anyStr(d["qr_string"]),
		URL:       anyStr(d["checkout_url"]),
		ExpiresAt: wibTime(anyStr(d["expiry_time"])),
	}, nil
}

func (g *AutoGoPay) Status(ctx context.Context, orderID, trxID string, total int64) (string, error) {
	r, err := g.call(ctx, "/qris/status", map[string]any{"transaction_id": trxID})
	if err != nil {
		return payPending, err
	}
	d, _ := r["data"].(map[string]any)
	if d == nil {
		d = r
	}
	st := anyStr(d["transaction_status"])
	if st == "" {
		st = anyStr(d["status"])
	}
	switch strings.ToLower(st) {
	case "settlement", "paid", "success", "completed":
		if amt := anyInt(d["amount"]); amt > 0 && amt < total {
			return payPending, fmt.Errorf("AutoGoPay: paid amount %d < %d", amt, total)
		}
		return payPaid, nil
	case "expire", "expired", "cancel", "canceled", "cancelled", "failed", "deny":
		return payFailed, nil
	}
	return payPending, nil
}

func (g *AutoGoPay) Cancel(ctx context.Context, orderID, trxID string, total int64) error { return nil }

func (g *AutoGoPay) Test(ctx context.Context) error {
	// No read-only endpoint: query a non-existent transaction. A key error
	// is reported as an authentication failure; "not found" means the key
	// works.
	_, err := g.call(ctx, "/qris/status", map[string]any{"transaction_id": "test-" + strconv.FormatInt(time.Now().Unix(), 10)})
	if err == nil {
		return nil
	}
	low := strings.ToLower(err.Error())
	for _, w := range []string{"401", "403", "unauthor", "api key", "invalid key", "forbidden", "unreachable", "timeout", "no such host"} {
		if strings.Contains(low, w) {
			return err
		}
	}
	return nil // e.g. "transaction not found": the key was accepted
}

// ---------------------------------------------------------------- Midtrans

type Midtrans struct {
	s  *Settings
	hc *http.Client
}

func (g *Midtrans) ID() string        { return "midtrans" }
func (g *Midtrans) Label() string     { return "Midtrans" }
func (g *Midtrans) Methods() []string { return []string{"qris"} }
func (g *Midtrans) Enabled() bool {
	return g.s.Bool("gw_midtrans_on") && g.s.Str("gw_midtrans_key") != ""
}

func (g *Midtrans) base() string {
	if midtransBase != "" {
		return midtransBase
	}
	if g.s.Bool("gw_midtrans_prod") {
		return "https://api.midtrans.com"
	}
	return "https://api.sandbox.midtrans.com"
}

func (g *Midtrans) call(ctx context.Context, method, path string, body any) (map[string]any, error) {
	key := g.s.Str("gw_midtrans_key")
	if key == "" {
		return nil, errors.New("Midtrans: Server Key is empty")
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(key+":"))
	var r map[string]any
	_, err := httpJSON(ctx, g.hc, method, g.base()+path, map[string]string{"Authorization": auth}, body, &r)
	if err != nil {
		return nil, fmt.Errorf("Midtrans: %w", err)
	}
	return r, nil
}

func (g *Midtrans) Create(ctx context.Context, orderID, method string, amount int64) (*Charge, error) {
	fee := (&Payments{s: g.s}).feeFor("midtrans", amount)
	total := amount + fee
	r, err := g.call(ctx, "POST", "/v2/charge", map[string]any{
		"payment_type":        "qris",
		"transaction_details": map[string]any{"order_id": orderID, "gross_amount": total},
		"qris":                map[string]string{"acquirer": "gopay"},
	})
	if err != nil {
		return nil, err
	}
	if anyStr(r["status_code"]) != "201" || anyStr(r["transaction_id"]) == "" {
		msg := anyStr(r["status_message"])
		if vm, ok := r["validation_messages"].([]any); ok && len(vm) > 0 {
			msg += ": " + anyStr(vm[0])
		}
		return nil, errors.New("Midtrans: " + msg)
	}
	c := &Charge{
		TrxID:     anyStr(r["transaction_id"]),
		Total:     total,
		Fee:       fee,
		Code:      anyStr(r["qr_string"]),
		ExpiresAt: wibTime(anyStr(r["expiry_time"])),
	}
	if acts, ok := r["actions"].([]any); ok {
		for _, a := range acts {
			if m, ok := a.(map[string]any); ok && anyStr(m["name"]) == "generate-qr-code" {
				c.URL = anyStr(m["url"])
			}
		}
	}
	if c.Code == "" && c.URL == "" {
		return nil, errors.New("Midtrans: response without QR code")
	}
	return c, nil
}

func (g *Midtrans) Status(ctx context.Context, orderID, trxID string, total int64) (string, error) {
	r, err := g.call(ctx, "GET", "/v2/"+url.PathEscape(orderID)+"/status", nil)
	if err != nil {
		return payPending, err
	}
	if anyStr(r["status_code"]) == "404" {
		return payPending, nil
	}
	switch strings.ToLower(anyStr(r["transaction_status"])) {
	case "settlement", "capture":
		if amt := anyInt(r["gross_amount"]); amt > 0 && amt < total {
			return payPending, fmt.Errorf("Midtrans: paid amount %d < %d", amt, total)
		}
		return payPaid, nil
	case "expire", "cancel", "deny", "failure", "refund", "partial_refund":
		return payFailed, nil
	}
	return payPending, nil
}

func (g *Midtrans) Cancel(ctx context.Context, orderID, trxID string, total int64) error {
	_, err := g.call(ctx, "POST", "/v2/"+url.PathEscape(orderID)+"/cancel", nil)
	return err
}

func (g *Midtrans) Test(ctx context.Context) error {
	r, err := g.call(ctx, "GET", "/v2/tomato-store-test-"+strconv.FormatInt(time.Now().Unix(), 10)+"/status", nil)
	if err != nil {
		return err
	}
	switch anyStr(r["status_code"]) {
	case "401", "403":
		return errors.New("Midtrans: " + anyStr(r["status_message"]))
	}
	return nil
}

// ---------------------------------------------------------------- Pakasir

type Pakasir struct {
	s  *Settings
	hc *http.Client
}

func (g *Pakasir) ID() string    { return "pakasir" }
func (g *Pakasir) Label() string { return "Pakasir" }
func (g *Pakasir) Enabled() bool {
	return g.s.Bool("gw_pakasir_on") && g.s.Str("gw_pakasir_key") != "" && g.s.Str("gw_pakasir_project") != "" && len(g.Methods()) > 0
}

func (g *Pakasir) Methods() []string {
	on := map[string]bool{}
	for _, m := range strings.Split(g.s.Str("gw_pakasir_methods"), ",") {
		on[strings.TrimSpace(m)] = true
	}
	var out []string
	for _, m := range pakasirMethods {
		if on[m] {
			out = append(out, m)
		}
	}
	return out
}

var pakasirBase = "https://app.pakasir.com"

// midtransBase overrides the Midtrans API URL (tests).
var midtransBase = ""

func (g *Pakasir) creds() (string, string, error) {
	p, k := g.s.Str("gw_pakasir_project"), g.s.Str("gw_pakasir_key")
	if p == "" || k == "" {
		return "", "", errors.New("Pakasir: project slug / API key is empty")
	}
	return p, k, nil
}

func (g *Pakasir) Create(ctx context.Context, orderID, method string, amount int64) (*Charge, error) {
	project, key, err := g.creds()
	if err != nil {
		return nil, err
	}
	var r struct {
		Payment *struct {
			Amount        any    `json:"amount"`
			Fee           any    `json:"fee"`
			TotalPayment  any    `json:"total_payment"`
			PaymentNumber string `json:"payment_number"`
			ExpiredAt     string `json:"expired_at"`
		} `json:"payment"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	code, err := httpJSON(ctx, g.hc, "POST", pakasirBase+"/api/transactioncreate/"+url.PathEscape(method), nil,
		map[string]any{"project": project, "order_id": orderID, "amount": amount, "api_key": key}, &r)
	if err != nil {
		return nil, fmt.Errorf("Pakasir: %w", err)
	}
	if r.Payment == nil || r.Payment.PaymentNumber == "" {
		msg := r.Error
		if msg == "" {
			msg = r.Message
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", code)
		}
		return nil, errors.New("Pakasir: " + msg)
	}
	total := anyInt(r.Payment.TotalPayment)
	if total == 0 {
		total = amount
	}
	return &Charge{
		TrxID:     "PKS-" + orderID,
		Total:     total,
		Fee:       anyInt(r.Payment.Fee),
		Code:      r.Payment.PaymentNumber,
		URL:       pakasirBase + "/pay/" + url.PathEscape(project) + "/" + strconv.FormatInt(amount, 10) + "?order_id=" + url.QueryEscape(orderID),
		ExpiresAt: wibTime(r.Payment.ExpiredAt),
	}, nil
}

// Pakasir looks transactions up by order_id + the original amount (without
// fee); trxID is not used.
func (g *Pakasir) Status(ctx context.Context, orderID, trxID string, total int64) (string, error) {
	return payPending, errors.New("use statusAmount")
}

func (g *Pakasir) statusAmount(ctx context.Context, orderID string, amount int64) (string, error) {
	project, key, err := g.creds()
	if err != nil {
		return payPending, err
	}
	q := url.Values{"project": {project}, "amount": {strconv.FormatInt(amount, 10)}, "order_id": {orderID}, "api_key": {key}}
	var r struct {
		Transaction *struct {
			Status string `json:"status"`
			Amount any    `json:"amount"`
		} `json:"transaction"`
	}
	if _, err := httpJSON(ctx, g.hc, "GET", pakasirBase+"/api/transactiondetail?"+q.Encode(), nil, nil, &r); err != nil {
		return payPending, fmt.Errorf("Pakasir: %w", err)
	}
	if r.Transaction == nil {
		return payPending, nil
	}
	switch strings.ToLower(r.Transaction.Status) {
	case "completed", "paid", "success":
		return payPaid, nil
	case "canceled", "cancelled", "expired", "failed":
		return payFailed, nil
	}
	return payPending, nil
}

func (g *Pakasir) Cancel(ctx context.Context, orderID, trxID string, amount int64) error {
	project, key, err := g.creds()
	if err != nil {
		return err
	}
	_, err = httpJSON(ctx, g.hc, "POST", pakasirBase+"/api/transactioncancel", nil,
		map[string]any{"project": project, "order_id": orderID, "amount": amount, "api_key": key}, nil)
	return err
}

func (g *Pakasir) Test(ctx context.Context) error {
	project, key, err := g.creds()
	if err != nil {
		return err
	}
	q := url.Values{"project": {project}, "amount": {"10000"}, "order_id": {"tomato-store-test"}, "api_key": {key}}
	var r map[string]any
	code, err := httpJSON(ctx, g.hc, "GET", pakasirBase+"/api/transactiondetail?"+q.Encode(), nil, nil, &r)
	if err != nil && code != 404 {
		return fmt.Errorf("Pakasir: %w", err)
	}
	if code == 401 || code == 403 {
		return fmt.Errorf("Pakasir: API key rejected (HTTP %d)", code)
	}
	if msg := strings.ToLower(anyStr(r["error"])); strings.Contains(msg, "key") || strings.Contains(msg, "project") {
		return errors.New("Pakasir: " + anyStr(r["error"]))
	}
	return nil
}

// status dispatches the poll; Pakasir needs the amount without fee.
func (p *Payments) status(ctx context.Context, inv *Invoice) (string, error) {
	g := p.Get(inv.Gateway)
	if g == nil {
		return payPending, errors.New("unknown gateway " + inv.Gateway)
	}
	if pk, ok := g.(*Pakasir); ok {
		return pk.statusAmount(ctx, inv.OrderRef, inv.Amount)
	}
	return g.Status(ctx, inv.OrderRef, inv.TrxID, inv.Total)
}

func (p *Payments) cancel(ctx context.Context, inv *Invoice) error {
	g := p.Get(inv.Gateway)
	if g == nil {
		return nil
	}
	if _, ok := g.(*Pakasir); ok {
		return g.Cancel(ctx, inv.OrderRef, inv.TrxID, inv.Amount)
	}
	return g.Cancel(ctx, inv.OrderRef, inv.TrxID, inv.Total)
}
