package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"os"
	"strconv"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// newInvoice creates the gateway payment and stores the invoice.
func (b *Bot) newInvoice(u *User, gwID, method string, amount int64) (*Invoice, error) {
	g := b.pay.Get(gwID)
	if g == nil || !g.Enabled() {
		return nil, uerr("err_gateway_off")
	}
	okMethod := false
	for _, m := range g.Methods() {
		if m == method {
			okMethod = true
		}
	}
	if !okMethod {
		return nil, uerr("err_gateway_off")
	}
	if amount < b.set.Int("topup_min") || amount > b.set.Int("topup_max") {
		return nil, uerr("err_topup_range", "min", rp(b.set.Int("topup_min")), "max", rp(b.set.Int("topup_max")))
	}
	if b.db.int(`SELECT COUNT(*) FROM invoices WHERE user_id=? AND status='pending'`, u.ID) >= 3 {
		return nil, uerr("err_too_many_invoices")
	}
	ref := fmt.Sprintf("TS-%s-%s", time.Now().Format("20060102"), randString(6, "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ch, err := g.Create(ctx, ref, method, amount)
	if err != nil {
		b.db.logError("pay:"+gwID, err)
		return nil, uerr("err_gateway", "msg", err.Error())
	}
	exp := ch.ExpiresAt
	if exp == 0 || exp < now() {
		mins := b.set.Int("invoice_minutes")
		if method != "qris" {
			mins = 24 * 60
		}
		exp = now() + mins*60
	}
	res, err := b.db.Exec(`INSERT INTO invoices(user_id, amount, total, fee, trx_id, order_ref, checkout_url, status, gateway, method, pay_code, expires_at, created_at)
		VALUES(?,?,?,?,?,?,?, 'pending', ?,?,?,?,?)`, u.ID, amount, ch.Total, ch.Fee, ch.TrxID, ref, ch.URL, gwID, method, ch.Code, exp, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return b.db.invoice(id)
}

// cashbackFor — event cashback for a top up amount.
func (b *Bot) cashbackFor(amount int64) int64 {
	if !b.set.Bool("cashback_enabled") {
		return 0
	}
	if until := b.set.Int("cashback_until"); until > 0 && now() > until {
		return 0
	}
	if amount < b.set.Int("cashback_min") {
		return 0
	}
	cb := b.set.Int("cashback_fixed")
	if cb == 0 {
		cb = amount * b.set.Int("cashback_percent") / 100
	}
	if m := b.set.Int("cashback_max"); m > 0 && cb > m {
		cb = m
	}
	return cb
}

// creditInvoice marks a pending invoice paid and credits the balance once.
func (b *Bot) creditInvoice(inv *Invoice) (bool, int64, int64) {
	var credited bool
	var bal, cb int64
	err := b.db.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE invoices SET status='paid', paid_at=? WHERE id=? AND status IN ('pending','cancelled','expired')`, now(), inv.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		credited = true
		ref := "invoice:" + inv.OrderRef
		if bal, err = addBalance(tx, inv.UserID, inv.Amount, "topup", ref, b.pay.labelOf(inv)); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE users SET total_topup=total_topup+? WHERE id=?`, inv.Amount, inv.UserID); err != nil {
			return err
		}
		if cb = b.cashbackFor(inv.Amount); cb > 0 {
			if bal, err = addBalance(tx, inv.UserID, cb, "cashback", ref, "cashback"); err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE invoices SET cashback=? WHERE id=?`, cb, inv.ID)
		}
		return err
	})
	if err != nil {
		b.db.logError("credit", err)
		return false, 0, 0
	}
	return credited, bal, cb
}

func (p *Payments) labelOf(inv *Invoice) string {
	l := inv.Gateway
	if g := p.Get(inv.Gateway); g != nil {
		l = g.Label()
	}
	return l + " " + methodLabel[inv.Method]
}

// checkInvoice polls the gateway once and finishes the invoice when paid,
// failed or expired. Returns the new status.
func (b *Bot) checkInvoice(inv *Invoice) string {
	if inv.Status != "pending" {
		return inv.Status
	}
	_, _ = b.db.Exec(`UPDATE invoices SET checked_at=? WHERE id=?`, now(), inv.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	st, err := b.pay.status(ctx, inv)
	if err != nil {
		b.db.logError("status:"+inv.Gateway, err)
	}
	switch {
	case st == payPaid:
		if credited, bal, cb := b.creditInvoice(inv); credited {
			b.paidMessage(inv, bal, cb)
		}
		return "paid"
	case st == payFailed || (now() > inv.ExpiresAt+120 && st != payPaid):
		res, _ := b.db.Exec(`UPDATE invoices SET status='expired' WHERE id=? AND status='pending'`, inv.ID)
		if n, _ := res.RowsAffected(); n > 0 {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
			_ = b.pay.cancel(ctx2, inv)
			cancel2()
			b.closeInvoiceMsg(inv)
			if inv.ChatID != 0 {
				b.send(inv.ChatID, newCard(b.t("inv_expired")).kv("Invoice", inv.OrderRef).kv(b.t("lbl_amount"), rp(inv.Amount)).String(),
					kbd(row(pri(b.t("btn_topup"), "top"), btn(b.t("btn_menu"), "m"))))
			}
		}
		return "expired"
	}
	return "pending"
}

func (b *Bot) paidMessage(inv *Invoice, bal, cb int64) {
	c := newCard(b.t("inv_paid")).kv("Invoice", inv.OrderRef).kv(b.t("lbl_amount"), rp(inv.Amount))
	if cb > 0 {
		c.kv("Cashback", rp(cb))
	}
	c.kv(b.t("lbl_balance"), rp(bal))
	b.closeInvoiceMsg(inv)
	if inv.ChatID != 0 {
		b.send(inv.ChatID, c.String(), kbd(row(ok(b.t("btn_buy"), "buy"), btn(b.t("btn_menu"), "m"))))
	}
	if b.set.Bool("notify_admin") {
		u, _ := b.db.user(inv.UserID)
		name := strconv.FormatInt(inv.UserID, 10)
		if u != nil {
			name = u.Name + " (" + name + ")"
		}
		b.notifyAdmins(newCard(b.t("adm_topup_title")).kv("User", name).kv("Gateway", b.pay.labelOf(inv)).
			kv(b.t("lbl_amount"), rp(inv.Amount)).kv("Invoice", inv.OrderRef).kv(b.t("lbl_time"), fmtTime(now())).String())
	}
}

// closeInvoiceMsg removes the QR / VA message (or its buttons).
func (b *Bot) closeInvoiceMsg(inv *Invoice) {
	if inv.ChatID != 0 && inv.MsgID != 0 {
		b.tg.Delete(inv.ChatID, inv.MsgID)
	}
}

// ---------------------------------------------------------------- QR image

func qrPNG(content string) ([]byte, error) {
	q, err := qrcode.New(content, qrcode.High)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = false
	img := q.Image(720)
	logo := loadLogo()
	if logo == nil {
		var buf bytes.Buffer
		err := png.Encode(&buf, img)
		return buf.Bytes(), err
	}
	canvas := image.NewRGBA(img.Bounds())
	draw.Draw(canvas, canvas.Bounds(), img, image.Point{}, draw.Src)
	// Logo at 18% of the QR width on a white square (error correction H).
	size := img.Bounds().Dx() * 18 / 100
	pad := size / 8
	cx, cy := img.Bounds().Dx()/2, img.Bounds().Dy()/2
	bg := image.Rect(cx-size/2-pad, cy-size/2-pad, cx+size/2+pad, cy+size/2+pad)
	draw.Draw(canvas, bg, &image.Uniform{color.White}, image.Point{}, draw.Src)
	dst := image.Rect(cx-size/2, cy-size/2, cx+size/2, cy+size/2)
	scaleInto(canvas, dst, logo)
	var buf bytes.Buffer
	err = png.Encode(&buf, canvas)
	return buf.Bytes(), err
}

func loadLogo() image.Image {
	f, err := os.Open(logoFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// scaleInto: nearest-neighbour scaling with alpha (no extra dependency).
func scaleInto(dst *image.RGBA, r image.Rectangle, src image.Image) {
	sb := src.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		sy := sb.Min.Y + (y-r.Min.Y)*sb.Dy()/r.Dy()
		for x := r.Min.X; x < r.Max.X; x++ {
			sx := sb.Min.X + (x-r.Min.X)*sb.Dx()/r.Dx()
			c := color.NRGBAModel.Convert(src.At(sx, sy)).(color.NRGBA)
			if c.A == 0 {
				continue
			}
			o := dst.RGBAAt(x, y)
			a := uint32(c.A)
			blend := func(s, d uint8) uint8 { return uint8((uint32(s)*a + uint32(d)*(255-a)) / 255) }
			dst.SetRGBA(x, y, color.RGBA{blend(c.R, o.R), blend(c.G, o.G), blend(c.B, o.B), 255})
		}
	}
}
