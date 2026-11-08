package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (b *Bot) topupCallback(u *User, q *CallbackQuery, chat, msgID int64, p []string, answered *bool) {
	if p[0] == "inv" {
		b.invoiceCallback(u, q, chat, msgID, p, answered)
		return
	}
	switch {
	case len(p) == 1:
		b.clearState(u.ID)
		if len(b.pay.Active()) == 0 {
			b.show(chat, msgID, newCard(b.t("topup_title")).text(b.t("err_gateway_off")).kv("CS", b.set.Str("cs_username")).String(),
				kbd(row(btn(b.t("btn_back"), "m"))))
			return
		}
		c := newCard(b.t("topup_title")).kv(b.t("lbl_balance"), rp(u.Balance)).kv(b.t("lbl_min"), rp(b.set.Int("topup_min")))
		if b.set.Bool("cashback_enabled") && (b.set.Int("cashback_until") == 0 || now() < b.set.Int("cashback_until")) {
			cb := fmt.Sprintf("%d%%", b.set.Int("cashback_percent"))
			if f := b.set.Int("cashback_fixed"); f > 0 {
				cb = rp(f)
			}
			if m := b.set.Int("cashback_min"); m > 0 {
				cb += " (min " + rp(m) + ")"
			}
			c.kv("Cashback", cb)
		}
		c.line().text(b.t("topup_choose"))
		var bs []Button
		for _, n := range b.set.List("topup_buttons") {
			bs = append(bs, btn(num(n), fmt.Sprintf("top:a:%d", n)))
		}
		bs = append(bs, btn(b.t("btn_other_amount"), "top:o"))
		kb := chunk(bs, 3)
		kb = append(kb, row(btn(b.t("btn_back"), "m")))
		b.show(chat, msgID, c.String(), kb)
		b.setState(u.ID, "top_amount", msgID, nil)
	case len(p) == 2 && p[1] == "o":
		id := b.show(chat, msgID, newCard(b.t("topup_title")).kv(b.t("lbl_min"), rp(b.set.Int("topup_min"))).
			kv(b.t("lbl_max"), rp(b.set.Int("topup_max"))).line().text(b.t("topup_type")).String(),
			kbd(row(bad(b.t("btn_cancel"), "top"))))
		b.setState(u.ID, "top_amount", id, nil)
	case len(p) == 3 && p[1] == "a":
		b.clearState(u.ID)
		b.topupMethods(u, q, chat, msgID, atoi(p[2]), answered)
	case len(p) == 5 && p[1] == "g":
		b.clearState(u.ID)
		b.createInvoice(u, q, chat, msgID, p[2], p[3], atoi(p[4]), answered)
	}
}

// topupMethods — choose gateway/method; skipped when only one exists.
func (b *Bot) topupMethods(u *User, q *CallbackQuery, chat, msgID, amount int64, answered *bool) {
	min, max := b.set.Int("topup_min"), b.set.Int("topup_max")
	if amount < min || amount > max {
		msg := b.t("err_topup_range", "min", rp(min), "max", rp(max))
		if q != nil {
			b.tg.Answer(q.ID, msg, true)
			*answered = true
		} else {
			b.send(chat, msg, kbd(row(btn(b.t("btn_back"), "top"))))
		}
		return
	}
	type opt struct{ gw, method, label string }
	var opts []opt
	for _, g := range b.pay.Active() {
		for _, m := range g.Methods() {
			label := methodLabel[m] + " " + g.Label()
			if g.ID() == "pakasir" {
				label = g.Label() + " " + methodLabel[m]
			}
			opts = append(opts, opt{g.ID(), m, label})
		}
	}
	if len(opts) == 0 {
		b.show(chat, msgID, b.t("err_gateway_off"), kbd(row(btn(b.t("btn_back"), "m"))))
		return
	}
	if len(opts) == 1 {
		b.createInvoice(u, q, chat, msgID, opts[0].gw, opts[0].method, amount, answered)
		return
	}
	c := newCard(b.t("method_title")).kv(b.t("lbl_amount"), rp(amount)).line()
	seen := map[string]bool{}
	for _, g := range b.pay.Active() {
		if seen[g.ID()] {
			continue
		}
		seen[g.ID()] = true
		c.text(g.Label())
		switch g.ID() {
		case "pakasir":
			c.kv(b.t("lbl_fee"), b.t("fee_pakasir"))
		default:
			c.kv(b.t("lbl_fee"), rp(b.pay.feeFor(g.ID(), amount)))
		}
		c.line()
	}
	var bs []Button
	for _, o := range opts {
		bs = append(bs, ok(o.label, fmt.Sprintf("top:g:%s:%s:%d", o.gw, o.method, amount)))
	}
	per := 1
	if len(bs) > 3 {
		per = 2
	}
	kb := chunk(bs, per)
	kb = append(kb, row(bad(b.t("btn_cancel"), "top")))
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) createInvoice(u *User, q *CallbackQuery, chat, msgID int64, gw, method string, amount int64, answered *bool) {
	if q != nil {
		b.tg.Answer(q.ID, b.t("creating_invoice"), false)
		*answered = true
	}
	inv, err := b.newInvoice(u, gw, method, amount)
	if err != nil {
		b.show(chat, msgID, newCard(b.t("failed_title")).text(b.errText(err)).String(), kbd(row(btn(b.t("btn_back"), "top"))))
		return
	}
	if msgID > 0 {
		b.tg.Delete(chat, msgID)
	}
	caption, kb := b.invoiceText(inv)
	var id int64
	if method == "qris" && inv.Code != "" {
		png, err := qrPNG(inv.Code)
		if err == nil {
			id, err = b.tg.SendPhoto(chat, png, caption, kb)
		}
		if err != nil {
			b.db.logError("qris", err)
		}
	}
	if id == 0 {
		// VA, or the QR image failed: text with the code / checkout link.
		id = b.send(chat, caption, kb)
	}
	_, _ = b.db.Exec(`UPDATE invoices SET chat_id=?, msg_id=? WHERE id=?`, chat, id, inv.ID)
}

func (b *Bot) invoiceText(inv *Invoice) (string, Keyboard) {
	g := b.pay.Get(inv.Gateway)
	gl := inv.Gateway
	if g != nil {
		gl = g.Label()
	}
	var c *card
	if inv.Method == "qris" {
		c = newCard(b.t("inv_qris_title"))
	} else {
		c = newCard(b.t("inv_va_title"))
	}
	c.kv("Invoice", inv.OrderRef).kv("Gateway", gl)
	if inv.Method != "qris" {
		c.kv("Bank", methodLabel[inv.Method]).kv(b.t("lbl_va"), inv.Code)
	}
	c.kv(b.t("lbl_amount"), rp(inv.Amount))
	if inv.Fee > 0 {
		c.kv(b.t("lbl_fee"), rp(inv.Fee))
	}
	c.kv("Total", rp(inv.Total)).kv(b.t("lbl_valid"), b.validText(inv.ExpiresAt)).line()
	if inv.Method == "qris" {
		c.text(b.t("inv_qris_body"))
	} else {
		c.text(b.t("inv_va_body"))
	}
	kb := Keyboard{}
	if inv.URL != "" && (inv.Method != "qris" || inv.Code == "") {
		kb = append(kb, row(btnURL(b.t("btn_pay_page"), inv.URL)))
	}
	kb = append(kb, pair(pri(b.t("btn_check"), fmt.Sprintf("inv:c:%d", inv.ID)), bad(b.t("btn_cancel"), fmt.Sprintf("inv:x:%d", inv.ID))))
	return c.String(), kb
}

func (b *Bot) validText(exp int64) string {
	left := exp - now()
	if left <= 0 {
		return b.t("expired_word")
	}
	t := time.Unix(exp, 0).Format("15:04")
	if left > 3600 {
		return b.t("hours_n", "n", (left+1800)/3600) + " (" + timeFmt(exp, "02-01 15:04") + ")"
	}
	return b.t("minutes_n", "n", (left+59)/60) + " (" + t + ")"
}

func (b *Bot) invoiceCallback(u *User, q *CallbackQuery, chat, msgID int64, p []string, answered *bool) {
	if len(p) != 3 {
		return
	}
	inv, err := b.db.invoice(atoi(p[2]))
	if err != nil || inv.UserID != u.ID {
		b.tg.Delete(chat, msgID)
		return
	}
	*answered = true
	switch p[1] {
	case "c":
		if inv.Status == "paid" {
			b.tg.Answer(q.ID, b.t("inv_already_paid"), true)
			return
		}
		if inv.Status != "pending" {
			b.tg.Answer(q.ID, b.t("inv_expired"), true)
			b.tg.Delete(chat, msgID)
			return
		}
		if now()-inv.CheckedAt < 3 {
			b.tg.Answer(q.ID, b.t("inv_waiting"), false)
			return
		}
		inv.ChatID, inv.MsgID = chat, msgID
		switch b.checkInvoice(inv) {
		case "paid":
			b.tg.Answer(q.ID, b.t("inv_paid"), false)
		case "expired":
			b.tg.Answer(q.ID, b.t("inv_expired"), false)
		default:
			b.tg.Answer(q.ID, b.t("inv_waiting"), false)
		}
	case "x":
		// Paid just before cancelling? Credit it instead.
		if inv.Status == "pending" {
			inv.ChatID, inv.MsgID = chat, msgID
			if b.checkInvoice(inv) == "paid" {
				b.tg.Answer(q.ID, b.t("inv_paid"), false)
				return
			}
		}
		res, _ := b.db.Exec(`UPDATE invoices SET status='cancelled' WHERE id=? AND status='pending'`, inv.ID)
		if n, _ := res.RowsAffected(); n > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := b.pay.cancel(ctx, inv); err != nil && !strings.Contains(err.Error(), "404") {
				b.db.logError("cancel:"+inv.Gateway, err)
			}
			cancel()
		}
		b.tg.Answer(q.ID, b.t("inv_cancelled"), false)
		b.tg.Delete(chat, msgID)
		b.mainMenu(u, chat, 0)
	}
}
