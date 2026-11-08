package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// setting groups shown in the generic editor
var setGroups = map[string][]string{
	"store":        {"store_name", "cs_username", "group_url", "force_channel", "maintenance", "durations", "price_round", "username_suffix"},
	"topup":        {"topup_min", "topup_max", "topup_buttons", "invoice_minutes", "fee_to_buyer"},
	"trial":        {"trial_enabled", "trial_minutes", "trial_member_weekly", "trial_reseller_daily"},
	"reseller":     {"reseller_enabled", "reseller_price", "reseller_discount"},
	"account":      {"remind_days", "allow_delete", "notify_admin"},
	"backup":       {"backup_hours", "backup_keep_days", "backup_send", "broadcast_rate"},
	"cashback":     {"cashback_enabled", "cashback_percent", "cashback_fixed", "cashback_min", "cashback_max", "cashback_until"},
	"gw:autogopay": {"gw_autogopay_key", "gw_autogopay_url", "gw_autogopay_fee"},
	"gw:midtrans":  {"gw_midtrans_key", "gw_midtrans_fee"},
	"gw:pakasir":   {"gw_pakasir_project", "gw_pakasir_key"},
}

var groupOf = func() map[string]string {
	m := map[string]string{}
	for g, keys := range setGroups {
		for _, k := range keys {
			m[k] = g
		}
	}
	return m
}()

var editableTexts = []string{"welcome", "howto_buy", "howto_topup", "tos", "maintenance_msg"}

// ---------------------------------------------------------------- panel

func (b *Bot) adminMenu(chat, msgID int64) {
	srv, _ := b.db.servers(false)
	online := 0
	for _, s := range srv {
		if s.Enabled && s.Status != "offline" {
			online++
		}
	}
	var gws []string
	for _, g := range b.pay.Active() {
		gws = append(gws, g.Label())
	}
	gw := strings.Join(gws, ", ")
	if gw == "" {
		gw = b.t("none_word")
	}
	c := newCard(b.t("adm_title")).
		kv(b.t("lbl_store"), b.set.Str("store_name")).
		kv(b.t("lbl_version"), version).
		kv(b.t("lbl_gateways"), gw).
		kv("Server", b.t("servers_online", "n", len(srv), "on", online))
	if b.set.Bool("maintenance") {
		c.kv("Maintenance", "on")
	}
	c.line().text(b.t("choose_menu"))
	kb := kbd(
		pair(pri(b.t("btn_dashboard"), "a:dash"), pri("Server", "a:srv")),
		pair(pri(b.t("btn_payment"), "a:pay"), pri("User", "a:user")),
		pair(btn("Broadcast", "a:bc"), btn("Cashback", "a:cb")),
		pair(btn(b.t("btn_settings"), "a:set"), btn(b.t("btn_texts"), "a:txt")),
		pair(btn("Tools", "a:tools"), btn(b.t("btn_close"), "a:close")),
	)
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) adminCallback(u *User, q *CallbackQuery, chat, msgID int64, data string) {
	p := strings.Split(data, ":")
	answered := false
	defer func() {
		if !answered {
			b.tg.Answer(q.ID, "", false)
		}
	}()
	toast := func(msg string, alert bool) {
		b.tg.Answer(q.ID, msg, alert)
		answered = true
	}
	if len(p) == 1 {
		b.clearState(u.ID)
		b.adminMenu(chat, msgID)
		return
	}
	switch p[1] {
	case "close":
		b.clearState(u.ID)
		b.tg.Delete(chat, msgID)
	case "dash":
		b.dashboard(chat, msgID)
	case "stat":
		b.stats6(chat, msgID)
	case "pgw":
		b.statsGateway(chat, msgID)
	case "srv", "s":
		b.adminServers(u, q, chat, msgID, p, toast)
	case "pay", "g":
		b.adminPayment(u, chat, msgID, p, toast)
	case "user", "u", "ua":
		b.adminUsers(u, chat, msgID, p, toast)
	case "bc":
		b.adminBroadcast(u, chat, msgID, p, toast)
	case "cb":
		b.settingsGroup(chat, msgID, "cashback")
	case "set":
		b.adminSettings(u, chat, msgID, p, toast)
	case "txt":
		b.adminTexts(u, chat, msgID, p, toast)
	case "tools", "t":
		b.adminTools(u, q, chat, msgID, p, toast)
	default:
		b.adminMenu(chat, msgID)
	}
}

// ---------------------------------------------------------------- dashboard

func (b *Bot) dashboard(chat, msgID int64) {
	d0, m0 := dayStart(), monthStart()
	users := b.db.int(`SELECT COUNT(*) FROM users`)
	newToday := b.db.int(`SELECT COUNT(*) FROM users WHERE created_at>=?`, d0)
	res := b.db.int(`SELECT COUNT(*) FROM users WHERE role='reseller'`)
	bal := b.db.int(`SELECT COALESCE(SUM(balance),0) FROM users`)
	topD := b.db.int(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE type='topup' AND created_at>=?`, d0)
	topM := b.db.int(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE type='topup' AND created_at>=?`, m0)
	salesD := b.db.int(`SELECT COALESCE(SUM(-amount),0) FROM transactions WHERE type IN ('purchase','renew') AND created_at>=?`, d0) -
		b.db.int(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE type='refund' AND created_at>=?`, d0)
	salesM := b.db.int(`SELECT COALESCE(SUM(-amount),0) FROM transactions WHERE type IN ('purchase','renew') AND created_at>=?`, m0) -
		b.db.int(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE type='refund' AND created_at>=?`, m0)
	ordD := b.db.int(`SELECT COUNT(*) FROM orders WHERE status='done' AND created_at>=?`, d0)
	c := newCard(b.t("dash_title")).
		kv("User", fmt.Sprintf("%s (+%d %s)", num(users), newToday, b.t("today_word"))).
		kv("Reseller", num(res)).
		kv(b.t("lbl_balance_float"), rp(bal)).line().
		kv(b.t("lbl_topup_today"), rp(topD)).
		kv(b.t("lbl_topup_month"), rp(topM)).
		kv(b.t("lbl_sales_today"), fmt.Sprintf("%s (%d)", rp(salesD), ordD)).
		kv(b.t("lbl_sales_month"), rp(salesM)).line()
	for _, pr := range protoList {
		c.kv(protoLabel[pr], num(b.db.int(`SELECT COUNT(*) FROM accounts WHERE proto=? AND status='active' AND trial=0`, pr)))
	}
	b.show(chat, msgID, c.String(), kbd(pair(btn(b.t("btn_stats6"), "a:stat"), btn(b.t("btn_per_gateway"), "a:pgw")), row(btn(b.t("btn_back"), "a"))))
}

func (b *Bot) stats6(chat, msgID int64) {
	c := newCard(b.t("stats6_title"))
	t := time.Now()
	for i := 5; i >= 0; i-- {
		start := time.Date(t.Year(), t.Month()-time.Month(i), 1, 0, 0, 0, 0, t.Location())
		end := start.AddDate(0, 1, 0)
		top := b.db.int(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE type='topup' AND created_at>=? AND created_at<?`, start.Unix(), end.Unix())
		sales := b.db.int(`SELECT COALESCE(SUM(-amount),0) FROM transactions WHERE type IN ('purchase','renew') AND created_at>=? AND created_at<?`, start.Unix(), end.Unix()) -
			b.db.int(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE type='refund' AND created_at>=? AND created_at<?`, start.Unix(), end.Unix())
		c.text(start.Format("2006-01")).kv("Topup", rp(top)).kv(b.t("lbl_sales"), rp(sales)).line()
	}
	b.show(chat, msgID, c.String(), kbd(row(btn(b.t("btn_back"), "a:dash"))))
}

func (b *Bot) statsGateway(chat, msgID int64) {
	c := newCard(b.t("pgw_title")).text(b.t("this_month")).line()
	rows, err := b.db.Query(`SELECT gateway, method, COUNT(*), COALESCE(SUM(amount),0), COALESCE(SUM(fee),0) FROM invoices
		WHERE status='paid' AND paid_at>=? GROUP BY gateway, method ORDER BY 4 DESC`, monthStart())
	n := 0
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gw, m string
			var cnt, amt, fee int64
			if rows.Scan(&gw, &m, &cnt, &amt, &fee) != nil {
				continue
			}
			n++
			label := gw
			if g := b.pay.Get(gw); g != nil {
				label = g.Label()
			}
			c.text(label+" "+methodLabel[m]).kv(b.t("lbl_count"), num(cnt)).kv("Total", rp(amt)).kv(b.t("lbl_fee"), rp(fee)).line()
		}
	}
	if n == 0 {
		c.text(b.t("no_data"))
	}
	b.show(chat, msgID, c.String(), kbd(row(btn(b.t("btn_back"), "a:dash"))))
}

// ---------------------------------------------------------------- servers

func (b *Bot) adminServers(u *User, q *CallbackQuery, chat, msgID int64, p []string, toast func(string, bool)) {
	if p[1] == "srv" {
		switch {
		case len(p) == 2:
			b.serverList(chat, msgID)
		case p[2] == "add":
			id := b.show(chat, msgID, newCard(b.t("srv_add_title")).text(b.t("srv_add_name")).String(), kbd(row(bad(b.t("btn_cancel"), "a:srv"))))
			b.setState(u.ID, "adm_srv_name", id, nil)
		case p[2] == "ping":
			toast(b.t("processing"), false)
			b.checkServers(true)
			b.serverList(chat, msgID)
		}
		return
	}
	// a:s:<id>[:action[:arg]]
	if len(p) < 3 {
		return
	}
	s, err := b.db.server(atoi(p[2]))
	if err != nil {
		b.serverList(chat, msgID)
		return
	}
	act := ""
	if len(p) > 3 {
		act = p[3]
	}
	switch act {
	case "":
		b.serverDetail(s, chat, msgID)
	case "price":
		var bs []Button
		for _, pr := range protoList {
			bs = append(bs, btn(protoLabel[pr]+" · "+rp(b.db.price(s.ID, pr)), fmt.Sprintf("a:s:%d:pp:%s", s.ID, pr)))
		}
		kb := chunk(bs, 2)
		kb = append(kb, row(btn(b.t("btn_back"), fmt.Sprintf("a:s:%d", s.ID))))
		b.show(chat, msgID, newCard(b.t("srv_price_title")+" · "+s.Name).text(b.t("srv_price_body")).String(), kb)
	case "pp":
		if len(p) < 5 || !isProto(p[4]) {
			return
		}
		id := b.show(chat, msgID, newCard(b.t("srv_price_title")+" · "+protoLabel[p[4]]).kv("Server", s.Name).
			kv(b.t("lbl_now"), rp(b.db.price(s.ID, p[4]))).line().text(b.t("srv_price_ask")).String(),
			kbd(row(bad(b.t("btn_cancel"), fmt.Sprintf("a:s:%d:price", s.ID)))))
		b.setState(u.ID, "adm_price", id, map[string]string{"sid": p[2], "proto": p[4]})
	case "proto":
		b.serverProtos(s, chat, msgID)
	case "pt":
		if len(p) < 5 || !isProto(p[4]) {
			return
		}
		s.Protos[p[4]] = !s.Protos[p[4]]
		_, _ = b.db.Exec(`UPDATE servers SET protos=? WHERE id=?`, s.protoString(), s.ID)
		b.serverProtos(s, chat, msgID)
	case "qi":
		id := b.show(chat, msgID, newCard(b.t("srv_qi_title")).kv(b.t("lbl_quota"), quotaText(s)).line().text(b.t("srv_qi_ask")).String(),
			kbd(row(bad(b.t("btn_cancel"), fmt.Sprintf("a:s:%d", s.ID)))))
		b.setState(u.ID, "adm_qi", id, map[string]string{"sid": p[2]})
	case "max":
		id := b.show(chat, msgID, newCard(b.t("srv_max_title")).kv(b.t("lbl_now"), strconv.Itoa(s.MaxAccounts)).line().text(b.t("srv_max_ask")).String(),
			kbd(row(bad(b.t("btn_cancel"), fmt.Sprintf("a:s:%d", s.ID)))))
		b.setState(u.ID, "adm_max", id, map[string]string{"sid": p[2]})
	case "name":
		id := b.show(chat, msgID, newCard(b.t("srv_name_title")).kv(b.t("lbl_now"), s.Name).line().text(b.t("srv_add_name")).String(),
			kbd(row(bad(b.t("btn_cancel"), fmt.Sprintf("a:s:%d", s.ID)))))
		b.setState(u.ID, "adm_rename", id, map[string]string{"sid": p[2]})
	case "key":
		if s.Local {
			return
		}
		id := b.show(chat, msgID, newCard(b.t("srv_key_title")).kv("URL", s.URL).line().text(b.t("srv_add_key")).String(),
			kbd(row(bad(b.t("btn_cancel"), fmt.Sprintf("a:s:%d", s.ID)))))
		b.setState(u.ID, "adm_srv_rekey", id, map[string]string{"sid": p[2]})
	case "en":
		_, _ = b.db.Exec(`UPDATE servers SET enabled=? WHERE id=?`, !s.Enabled, s.ID)
		b.db.audit(u.ID, "server_toggle", s.Name, strconv.FormatBool(!s.Enabled))
		s, _ = b.db.server(s.ID)
		b.serverDetail(s, chat, msgID)
	case "del":
		n := b.db.activeCount(s.ID)
		b.show(chat, msgID, newCard(b.t("srv_del_title")).kv("Server", s.Name).kv(b.t("lbl_active_accounts"), strconv.FormatInt(n, 10)).
			line().text(b.t("srv_del_warn")).String(),
			kbd(pair(bad(b.t("btn_delete_yes"), fmt.Sprintf("a:s:%d:delok", s.ID)), btn(b.t("btn_cancel"), fmt.Sprintf("a:s:%d", s.ID)))))
	case "delok":
		_, _ = b.db.Exec(`DELETE FROM prices WHERE server_id=?`, s.ID)
		_, _ = b.db.Exec(`DELETE FROM servers WHERE id=?`, s.ID)
		_, _ = b.db.Exec(`UPDATE accounts SET status='deleted', updated_at=? WHERE server_id=? AND status='active'`, now(), s.ID)
		b.db.audit(u.ID, "server_delete", s.Name, "")
		toast(b.t("deleted"), false)
		b.serverList(chat, msgID)
	}
}

func (b *Bot) serverList(chat, msgID int64) {
	srv, _ := b.db.servers(false)
	c := newCard("SERVER")
	var kb Keyboard
	var bs []Button
	for i, s := range srv {
		st := s.Status
		switch {
		case !s.Enabled:
			st = b.t("st_disabled")
		case s.Status == "online":
			st = fmt.Sprintf("online · %d ms", s.LatencyMs)
		case s.Status == "offline":
			st = b.t("offline_since", "t", timeFmt(s.DownSince, "15:04"))
		}
		maxS := "∞"
		if s.MaxAccounts > 0 {
			maxS = strconv.Itoa(s.MaxAccounts)
		}
		c.text(fmt.Sprintf("%d  %s", i+1, s.Name)).kv("Status", st).kv(b.t("lbl_accounts"), fmt.Sprintf("%d / %s", b.db.activeCount(s.ID), maxS)).line()
		bs = append(bs, btn(fmt.Sprintf("%d  %s", i+1, s.Name), fmt.Sprintf("a:s:%d", s.ID)))
	}
	if len(srv) == 0 {
		c.text(b.t("no_servers_admin"))
	}
	kb = append(kb, chunk(bs, 2)...)
	kb = append(kb, pair(ok(b.t("btn_add_server"), "a:srv:add"), pri(b.t("btn_ping"), "a:srv:ping")), row(btn(b.t("btn_back"), "a")))
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) serverDetail(s *Server, chat, msgID int64) {
	c := newCard(s.Name)
	if s.Local {
		c.kv("API", b.t("srv_local"))
	} else {
		c.kv("API", s.URL)
	}
	maxS := b.t("unlimited_word")
	if s.MaxAccounts > 0 {
		maxS = strconv.Itoa(s.MaxAccounts)
	}
	q := "Unlimited"
	if s.QuotaGB > 0 {
		q = fmt.Sprintf("%d GB", s.QuotaGB)
	}
	c.kv(b.t("lbl_quota"), q).kv("IP Limit", ipText(s.IPLimit)).kv(b.t("lbl_max_accounts"), maxS).
		kv("Status", map[bool]string{true: s.Status, false: b.t("st_disabled")}[s.Enabled])
	if s.LastError != "" && s.Status == "offline" {
		c.kv("Error", s.LastError)
	}
	c.line()
	for _, pr := range protoList {
		sell := b.t("sell_yes")
		if !s.Protos[pr] {
			sell = b.t("sell_no")
		}
		c.kv(protoLabel[pr], rp(b.db.price(s.ID, pr))+" · "+sell)
	}
	id := strconv.FormatInt(s.ID, 10)
	en := bad(b.t("btn_disable"), "a:s:"+id+":en")
	if !s.Enabled {
		en = ok(b.t("btn_enable"), "a:s:"+id+":en")
	}
	kb := kbd(
		pair(btn(b.t("btn_set_price"), "a:s:"+id+":price"), btn(b.t("btn_set_proto"), "a:s:"+id+":proto")),
		pair(btn(b.t("btn_quota_ip"), "a:s:"+id+":qi"), btn(b.t("btn_max_accounts"), "a:s:"+id+":max")),
	)
	third := []Button{btn(b.t("btn_rename"), "a:s:"+id+":name")}
	if !s.Local {
		third = append(third, btn("API Key", "a:s:"+id+":key"))
	}
	kb = append(kb, third, pair(en, bad(b.t("btn_delete"), "a:s:"+id+":del")), row(btn(b.t("btn_back"), "a:srv")))
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) serverProtos(s *Server, chat, msgID int64) {
	var bs []Button
	for _, pr := range protoList {
		if s.Protos[pr] {
			bs = append(bs, ok(protoLabel[pr]+" · "+b.t("sell_yes"), fmt.Sprintf("a:s:%d:pt:%s", s.ID, pr)))
		} else {
			bs = append(bs, btn(protoLabel[pr]+" · "+b.t("sell_no"), fmt.Sprintf("a:s:%d:pt:%s", s.ID, pr)))
		}
	}
	kb := chunk(bs, 2)
	kb = append(kb, row(btn(b.t("btn_back"), fmt.Sprintf("a:s:%d", s.ID))))
	b.show(chat, msgID, newCard(b.t("srv_proto_title")+" · "+s.Name).text(b.t("srv_proto_body")).String(), kb)
}

// ---------------------------------------------------------------- payment

func (b *Bot) adminPayment(u *User, chat, msgID int64, p []string, toast func(string, bool)) {
	if p[1] == "pay" {
		if len(p) == 3 && p[2] == "fee" {
			b.set.Toggle("fee_to_buyer")
		}
		if len(p) == 3 && p[2] == "lim" {
			b.settingsGroup(chat, msgID, "topup")
			return
		}
		b.paymentList(chat, msgID)
		return
	}
	// a:g:<gw>[:act[:arg]]
	if len(p) < 3 {
		return
	}
	g := b.pay.Get(p[2])
	if g == nil {
		b.paymentList(chat, msgID)
		return
	}
	act := ""
	if len(p) > 3 {
		act = p[3]
	}
	switch act {
	case "on":
		on := b.set.Toggle("gw_" + g.ID() + "_on")
		b.db.audit(u.ID, "gateway_toggle", g.ID(), strconv.FormatBool(on))
		if on && !g.Enabled() {
			toast(b.t("gw_incomplete"), true)
		}
	case "test":
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err := g.Test(ctx)
		cancel()
		if err != nil {
			toast(b.t("gw_test_fail", "msg", err.Error()), true)
		} else {
			toast(b.t("gw_test_ok"), true)
		}
	case "mode":
		b.set.Toggle("gw_midtrans_prod")
	case "m": // pakasir methods
		b.pakasirMethods(chat, msgID)
		return
	case "mt":
		if len(p) < 5 {
			return
		}
		on := map[string]bool{}
		for _, m := range strings.Split(b.set.Str("gw_pakasir_methods"), ",") {
			on[strings.TrimSpace(m)] = true
		}
		on[p[4]] = !on[p[4]]
		var keep []string
		for _, m := range pakasirMethods {
			if on[m] {
				keep = append(keep, m)
			}
		}
		_ = b.set.Set("gw_pakasir_methods", strings.Join(keep, ","))
		b.pakasirMethods(chat, msgID)
		return
	case "e":
		if len(p) < 5 {
			return
		}
		b.askSetting(u, chat, msgID, p[4])
		return
	}
	b.gatewayDetail(g, chat, msgID)
}

func (b *Bot) onOff(v bool) string {
	if v {
		return b.t("st_on")
	}
	return b.t("st_off")
}

func (b *Bot) paymentList(chat, msgID int64) {
	c := newCard(b.t("pay_title")).text(b.t("pay_body")).line()
	for _, g := range b.pay.gws {
		on := b.set.Bool("gw_" + g.ID() + "_on")
		st := b.onOff(on)
		if on && !g.Enabled() {
			st = b.t("st_incomplete")
		}
		if g.ID() == "midtrans" && on {
			st += " · " + map[bool]string{true: "production", false: "sandbox"}[b.set.Bool("gw_midtrans_prod")]
		}
		var ms []string
		for _, m := range g.Methods() {
			ms = append(ms, methodLabel[m])
		}
		if g.ID() == "pakasir" {
			ms = []string{"QRIS", "VA"}
		}
		c.text(g.Label()).kv("Status", st).kv(b.t("lbl_method"), strings.Join(ms, ", ")).line()
	}
	c.kv(b.t("lbl_fee_buyer"), b.onOff(b.set.Bool("fee_to_buyer")))
	kb := kbd(
		row(btn("AutoGoPay", "a:g:autogopay"), btn("Midtrans", "a:g:midtrans"), btn("Pakasir", "a:g:pakasir")),
		pair(btn(b.t("btn_fee_buyer"), "a:pay:fee"), btn(b.t("btn_topup_limits"), "a:pay:lim")),
		row(btn(b.t("btn_back"), "a")),
	)
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) gatewayDetail(g Gateway, chat, msgID int64) {
	id := g.ID()
	on := b.set.Bool("gw_" + id + "_on")
	st := b.onOff(on)
	if on && !g.Enabled() {
		st = b.t("st_incomplete")
	}
	c := newCard(strings.ToUpper(g.Label())).kv("Status", st)
	kb := Keyboard{}
	toggle := ok(b.t("btn_enable"), "a:g:"+id+":on")
	if on {
		toggle = bad(b.t("btn_disable"), "a:g:"+id+":on")
	}
	switch id {
	case "autogopay":
		c.kv("API Key", orNone(b, mask(b.set.Str("gw_autogopay_key")))).kv("URL", b.set.Str("gw_autogopay_url")).
			kv(b.t("lbl_method"), "QRIS").kv(b.t("lbl_fee"), b.set.Str("gw_autogopay_fee")+"%").
			kv(b.t("lbl_valid"), b.t("minutes_n", "n", b.set.Int("invoice_minutes")))
		if loadLogo() != nil {
			c.kv("Logo QRIS", "logo.png")
		}
		c.line().text(b.t("gw_autogopay_help"))
		kb = kbd(pair(toggle, pri(b.t("btn_test"), "a:g:"+id+":test")),
			pair(btn(b.t("btn_set_key"), "a:g:"+id+":e:gw_autogopay_key"), btn(b.t("btn_fee"), "a:g:"+id+":e:gw_autogopay_fee")),
			pair(btn("URL API", "a:g:"+id+":e:gw_autogopay_url"), btn(b.t("btn_back"), "a:pay")))
	case "midtrans":
		mode := "sandbox"
		if b.set.Bool("gw_midtrans_prod") {
			mode = "production"
		}
		c.kv("Mode", mode).kv("Server Key", orNone(b, mask(b.set.Str("gw_midtrans_key")))).kv(b.t("lbl_method"), "QRIS (GoPay)").
			kv(b.t("lbl_fee"), b.set.Str("gw_midtrans_fee")+"% "+map[bool]string{true: b.t("to_buyer"), false: b.t("to_store")}[b.set.Bool("fee_to_buyer")]).
			kv(b.t("lbl_status_check"), b.t("polling_5s")).line().text(b.t("gw_midtrans_help"))
		modeBtn := btn(b.t("btn_mode_sandbox"), "a:g:"+id+":mode")
		if mode == "sandbox" {
			modeBtn = btn(b.t("btn_mode_prod"), "a:g:"+id+":mode")
		}
		kb = kbd(pair(toggle, pri(b.t("btn_test"), "a:g:"+id+":test")),
			pair(btn(b.t("btn_set_server_key"), "a:g:"+id+":e:gw_midtrans_key"), modeBtn),
			pair(btn(b.t("btn_fee"), "a:g:"+id+":e:gw_midtrans_fee"), btn(b.t("btn_back"), "a:pay")))
	case "pakasir":
		c.kv("Project", orNone(b, b.set.Str("gw_pakasir_project"))).kv("API Key", orNone(b, mask(b.set.Str("gw_pakasir_key")))).line()
		on := map[string]bool{}
		for _, m := range g.Methods() {
			on[m] = true
		}
		for _, m := range pakasirMethods[:5] {
			c.kv(methodLabel[m], map[bool]string{true: b.t("sell_yes"), false: b.t("sell_no")}[on[m]])
		}
		if extra := len(g.Methods()); extra > 0 {
			n := 0
			for _, m := range pakasirMethods[5:] {
				if on[m] {
					n++
				}
			}
			if n > 0 {
				c.kv(b.t("lbl_other_va"), strconv.Itoa(n))
			}
		}
		c.line().text(b.t("gw_pakasir_help"))
		kb = kbd(pair(btn(b.t("btn_set_key"), "a:g:"+id+":e:gw_pakasir_key"), btn(b.t("btn_set_project"), "a:g:"+id+":e:gw_pakasir_project")),
			pair(btn(b.t("btn_methods"), "a:g:"+id+":m"), pri(b.t("btn_test"), "a:g:"+id+":test")),
			pair(toggle, btn(b.t("btn_back"), "a:pay")))
	}
	b.show(chat, msgID, c.String(), kb)
}

func orNone(b *Bot, s string) string {
	if s == "" {
		return b.t("not_set")
	}
	return s
}

func (b *Bot) pakasirMethods(chat, msgID int64) {
	on := map[string]bool{}
	for _, m := range strings.Split(b.set.Str("gw_pakasir_methods"), ",") {
		on[strings.TrimSpace(m)] = true
	}
	var bs []Button
	for _, m := range pakasirMethods {
		if on[m] {
			bs = append(bs, ok(methodLabel[m], "a:g:pakasir:mt:"+m))
		} else {
			bs = append(bs, btn(methodLabel[m], "a:g:pakasir:mt:"+m))
		}
	}
	kb := chunk(bs, 2)
	kb = append(kb, row(btn(b.t("btn_back"), "a:g:pakasir")))
	b.show(chat, msgID, newCard(b.t("pks_methods_title")).text(b.t("pks_methods_body")).String(), kb)
}

// ---------------------------------------------------------------- users

func (b *Bot) adminUsers(u *User, chat, msgID int64, p []string, toast func(string, bool)) {
	switch p[1] {
	case "user":
		rows, err := b.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY last_seen DESC LIMIT 8`)
		c := newCard("USER").kv(b.t("lbl_total"), num(b.db.int(`SELECT COUNT(*) FROM users`))).line().text(b.t("user_search")).line()
		var bs []Button
		if err == nil {
			for rows.Next() {
				if x, err := scanUser(rows); err == nil {
					c.kv(x.Name, rp(x.Balance))
					bs = append(bs, btn(trimRunes(x.Name, 18), fmt.Sprintf("a:u:%d", x.ID)))
				}
			}
			rows.Close()
		}
		kb := chunk(bs, 2)
		kb = append(kb, row(btn(b.t("btn_back"), "a")))
		id := b.show(chat, msgID, c.String(), kb)
		b.setState(u.ID, "adm_user_find", id, nil)
	case "u":
		if len(p) < 3 {
			return
		}
		x, err := b.db.user(atoi(p[2]))
		if err != nil {
			toast(b.t("user_not_found"), true)
			return
		}
		act := ""
		if len(p) > 3 {
			act = p[3]
		}
		switch act {
		case "add", "sub":
			id := b.show(chat, msgID, newCard(map[string]string{"add": b.t("bal_add_title"), "sub": b.t("bal_sub_title")}[act]).
				kv("User", x.Name).kv(b.t("lbl_balance"), rp(x.Balance)).line().text(b.t("bal_ask")).String(),
				kbd(row(bad(b.t("btn_cancel"), fmt.Sprintf("a:u:%d", x.ID)))))
			b.setState(u.ID, "adm_bal", id, map[string]string{"uid": p[2], "op": act})
			return
		case "res":
			role := "reseller"
			if x.reseller() {
				role = "member"
			}
			_, _ = b.db.Exec(`UPDATE users SET role=? WHERE id=?`, role, x.ID)
			b.db.audit(u.ID, "role", strconv.FormatInt(x.ID, 10), role)
			x.Role = role
		case "ban":
			_, _ = b.db.Exec(`UPDATE users SET banned=? WHERE id=?`, !x.Banned, x.ID)
			b.db.audit(u.ID, "ban", strconv.FormatInt(x.ID, 10), strconv.FormatBool(!x.Banned))
			x.Banned = !x.Banned
		case "acc":
			b.adminUserAccounts(x, chat, msgID)
			return
		}
		b.userDetail(x, chat, msgID)
	case "ua":
		if len(p) < 3 {
			return
		}
		a, err := b.db.account(atoi(p[2]))
		if err != nil {
			return
		}
		s, err := b.db.server(a.ServerID)
		if err != nil {
			toast(b.t("err_server_closed"), true)
			return
		}
		act := ""
		if len(p) > 3 {
			act = p[3]
		}
		switch act {
		case "lock", "unlock":
			if err := b.api(s).Lock(a.Proto, a.Username, act == "lock"); err != nil {
				toast(err.Error(), true)
				return
			}
			b.db.audit(u.ID, act, a.Username, protoLabel[a.Proto])
			toast("OK", false)
		case "del":
			if err := b.api(s).Delete(a.Proto, a.Username); err != nil && !notFound(err) {
				toast(err.Error(), true)
				return
			}
			_, _ = b.db.Exec(`UPDATE accounts SET status='deleted', updated_at=? WHERE id=?`, now(), a.ID)
			b.db.audit(u.ID, "delete_account", a.Username, protoLabel[a.Proto])
			toast(b.t("deleted"), false)
			if x, err := b.db.user(a.UserID); err == nil {
				b.adminUserAccounts(x, chat, msgID)
			}
			return
		}
		c := newCard(protoLabel[a.Proto]+" · "+a.Username).kv("Server", s.Name).kv("Expired", b.expText(a.ExpiresAt))
		if live, err := b.api(s).Get(a.Proto, a.Username); err == nil {
			c.kv(b.t("lbl_quota"), usage(live.UsedBytes, live.QuotaBytes)).kv("Status", live.Status)
		}
		id := strconv.FormatInt(a.ID, 10)
		b.show(chat, msgID, c.String(), kbd(pair(btn("Lock", "a:ua:"+id+":lock"), btn("Unlock", "a:ua:"+id+":unlock")),
			row(bad(b.t("btn_delete"), "a:ua:"+id+":del")), row(btn(b.t("btn_back"), fmt.Sprintf("a:u:%d:acc", a.UserID)))))
	}
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (b *Bot) userDetail(x *User, chat, msgID int64) {
	act := b.db.int(`SELECT COUNT(*) FROM accounts WHERE user_id=? AND status='active'`, x.ID)
	c := newCard("USER").kv("ID", strconv.FormatInt(x.ID, 10)).kv(b.t("lbl_name"), x.Name)
	if x.Username != "" {
		c.kv("Username", "@"+x.Username)
	}
	status := map[bool]string{true: "Reseller", false: "Member"}[x.reseller()]
	if x.Banned {
		status += " · banned"
	}
	if x.Blocked {
		status += " · " + b.t("blocked_bot")
	}
	c.kv("Status", status).kv(b.t("lbl_balance"), rp(x.Balance)).kv(b.t("lbl_active_accounts"), strconv.FormatInt(act, 10)).
		kv(b.t("lbl_total_topup"), rp(x.TotalTopup)).kv(b.t("lbl_total_spent"), rp(x.TotalSpent)).kv(b.t("lbl_joined"), fmtDate(x.CreatedAt))
	id := strconv.FormatInt(x.ID, 10)
	resBtn := pri(b.t("btn_make_reseller"), "a:u:"+id+":res")
	if x.reseller() {
		resBtn = btn(b.t("btn_revoke_reseller"), "a:u:"+id+":res")
	}
	banBtn := bad("Ban", "a:u:"+id+":ban")
	if x.Banned {
		banBtn = ok("Unban", "a:u:"+id+":ban")
	}
	b.show(chat, msgID, c.String(), kbd(
		pair(ok(b.t("btn_bal_add"), "a:u:"+id+":add"), bad(b.t("btn_bal_sub"), "a:u:"+id+":sub")),
		pair(resBtn, btn(b.t("btn_user_accounts"), "a:u:"+id+":acc")),
		pair(banBtn, btn(b.t("btn_back"), "a:user"))))
}

func (b *Bot) adminUserAccounts(x *User, chat, msgID int64) {
	list, _ := b.db.userAccounts(x.ID)
	c := newCard(b.t("acc_title") + " · " + x.Name)
	var bs []Button
	for i, a := range list {
		if i >= 30 {
			break
		}
		c.kv(fmt.Sprintf("%d  %s", i+1, protoLabel[a.Proto]), a.Username+" · "+fmtDate(a.ExpiresAt))
		bs = append(bs, btn(fmt.Sprintf("%d  %s", i+1, a.Username), fmt.Sprintf("a:ua:%d", a.ID)))
	}
	if len(list) == 0 {
		c.text(b.t("acc_none"))
	}
	kb := chunk(bs, 2)
	kb = append(kb, row(btn(b.t("btn_back"), fmt.Sprintf("a:u:%d", x.ID))))
	b.show(chat, msgID, c.String(), kb)
}

// ---------------------------------------------------------------- broadcast

func (b *Bot) adminBroadcast(u *User, chat, msgID int64, p []string, toast func(string, bool)) {
	switch {
	case len(p) == 2:
		if id := b.db.int(`SELECT id FROM broadcasts WHERE status IN ('queued','running') ORDER BY id LIMIT 1`); id > 0 {
			b.broadcastStatus(id, chat, msgID)
			return
		}
		all := b.db.int(`SELECT COUNT(*) FROM users WHERE blocked=0 AND banned=0`)
		res := b.db.int(`SELECT COUNT(*) FROM users WHERE blocked=0 AND banned=0 AND role='reseller'`)
		b.show(chat, msgID, newCard("BROADCAST").kv(b.t("lbl_target"), b.t("users_n", "n", num(all))).kv("Reseller", num(res)).
			kv(b.t("lbl_speed"), fmt.Sprintf("%d / %s", b.set.Int("broadcast_rate"), b.t("second_word"))).line().text(b.t("bc_body")).String(),
			kbd(pair(pri(b.t("btn_all_users"), "a:bc:t:all"), pri(b.t("btn_resellers"), "a:bc:t:reseller")), row(btn(b.t("btn_back"), "a"))))
	case p[2] == "t" && len(p) == 4:
		id := b.show(chat, msgID, newCard("BROADCAST").kv(b.t("lbl_target"), p[3]).line().text(b.t("bc_ask")).String(), kbd(row(bad(b.t("btn_cancel"), "a:bc"))))
		b.setState(u.ID, "adm_bc", id, map[string]string{"target": p[3]})
	case p[2] == "go":
		st := b.getState(u.ID)
		if st == nil || st.Kind != "adm_bc_confirm" {
			toast(b.t("err_expired_action"), true)
			return
		}
		b.clearState(u.ID)
		target := st.Data["target"]
		q := `SELECT COUNT(*) FROM users WHERE blocked=0 AND banned=0`
		if target == "reseller" {
			q += ` AND role='reseller'`
		}
		total := b.db.int(q)
		res, err := b.db.Exec(`INSERT INTO broadcasts(kind, text, photo, status, total, created_by, created_at, target, chat_id, msg_id)
			VALUES(?,?,?, 'queued', ?,?,?,?,?,?)`, map[bool]string{true: "photo", false: "text"}[st.Data["photo"] != ""],
			st.Data["text"], st.Data["photo"], total, u.ID, now(), target, chat, msgID)
		if err != nil {
			toast(err.Error(), true)
			return
		}
		id, _ := res.LastInsertId()
		b.db.audit(u.ID, "broadcast", target, strconv.FormatInt(total, 10))
		select {
		case b.bcWake <- struct{}{}:
		default:
		}
		b.broadcastStatus(id, chat, msgID)
	case p[2] == "s" && len(p) == 4:
		b.broadcastStatus(atoi(p[3]), chat, msgID)
	case p[2] == "stop" && len(p) == 4:
		_, _ = b.db.Exec(`UPDATE broadcasts SET status='stopped', finished_at=? WHERE id=? AND status IN ('queued','running')`, now(), atoi(p[3]))
		b.broadcastStatus(atoi(p[3]), chat, msgID)
	}
}

func (b *Bot) broadcastStatus(id, chat, msgID int64) {
	var status string
	var total, sent, failed, blocked int64
	if err := b.db.QueryRow(`SELECT status, total, sent, failed, blocked FROM broadcasts WHERE id=?`, id).Scan(&status, &total, &sent, &failed, &blocked); err != nil {
		b.adminMenu(chat, msgID)
		return
	}
	title := b.t("bc_running")
	switch status {
	case "done":
		title = b.t("bc_done")
	case "stopped":
		title = b.t("bc_stopped")
	}
	c := newCard(title).kv(b.t("lbl_sent"), fmt.Sprintf("%s / %s", num(sent), num(total))).kv(b.t("lbl_failed"), num(failed)).kv(b.t("lbl_blocked"), num(blocked))
	if status == "queued" || status == "running" {
		rate := b.set.Int("broadcast_rate")
		left := (total - sent - failed - blocked) / max(rate, 1)
		c.kv(b.t("lbl_eta"), "± "+b.t("minutes_n", "n", left/60+1))
		ids := strconv.FormatInt(id, 10)
		b.show(chat, msgID, c.String(), kbd(pair(pri("Refresh", "a:bc:s:"+ids), bad(b.t("btn_stop"), "a:bc:stop:"+ids))))
		return
	}
	b.show(chat, msgID, c.String(), kbd(row(btn(b.t("btn_back"), "a"))))
}

// ---------------------------------------------------------------- settings

func (b *Bot) adminSettings(u *User, chat, msgID int64, p []string, toast func(string, bool)) {
	switch {
	case len(p) == 2:
		c := newCard(b.t("set_title"))
		for _, k := range []string{"store_name", "cs_username", "force_channel", "maintenance", "durations", "remind_days", "allow_delete", "lang"} {
			c.kv(b.setLabel(k), b.setValue(k))
		}
		b.show(chat, msgID, c.String(), kbd(
			pair(btn(b.t("grp_store"), "a:set:g:store"), btn("Topup", "a:set:g:topup")),
			pair(btn("Trial", "a:set:g:trial"), btn("Reseller", "a:set:g:reseller")),
			pair(btn(b.t("grp_account"), "a:set:g:account"), btn("Backup", "a:set:g:backup")),
			pair(btn(b.t("btn_language"), "a:set:lang"), btn(b.t("btn_back"), "a"))))
	case p[2] == "g" && len(p) == 4:
		b.settingsGroup(chat, msgID, p[3])
	case p[2] == "lang":
		if b.set.Str("lang") == "en" {
			_ = b.set.Set("lang", "id")
		} else {
			_ = b.set.Set("lang", "en")
		}
		b.adminSettings(u, chat, msgID, []string{"a", "set"}, toast)
	case p[2] == "t" && len(p) == 4:
		if sp := specByKey[p[3]]; sp != nil && sp.kind == kBool {
			v := b.set.Toggle(p[3])
			b.db.audit(u.ID, "setting", p[3], boolStr(v))
			b.settingsGroup(chat, msgID, groupOf[p[3]])
		}
	case p[2] == "e" && len(p) == 4:
		b.askSetting(u, chat, msgID, p[3])
	}
}

func (b *Bot) setLabel(k string) string {
	if sp := specByKey[k]; sp != nil {
		return sp.label[b.lang()]
	}
	return k
}

func (b *Bot) setValue(k string) string {
	sp := specByKey[k]
	if sp == nil {
		return b.set.Str(k)
	}
	v := b.set.Str(k)
	switch sp.kind {
	case kBool:
		return b.onOff(v == "1")
	case kSecret:
		return orNone(b, mask(v))
	case kInt:
		switch {
		case k == "cashback_until":
			if b.set.Int(k) == 0 {
				return b.t("no_end")
			}
			return fmtTime(b.set.Int(k))
		case strings.Contains(k, "price") && k != "price_round", strings.HasPrefix(k, "topup_m"), k == "cashback_fixed", k == "cashback_min", k == "cashback_max":
			return rp(b.set.Int(k))
		}
	case kList:
		if k == "topup_buttons" {
			var out []string
			for _, n := range b.set.List(k) {
				out = append(out, num(n))
			}
			return strings.Join(out, ", ")
		}
		return strings.ReplaceAll(v, ",", ", ")
	}
	if k == "lang" {
		return map[string]string{"id": "Indonesia", "en": "English"}[v]
	}
	return orNone(b, v)
}

func (b *Bot) settingsGroup(chat, msgID int64, g string) {
	keys := setGroups[g]
	if keys == nil {
		return
	}
	title := map[string]string{"store": b.t("grp_store"), "topup": "TOPUP", "trial": "TRIAL", "reseller": "RESELLER", "account": b.t("grp_account"),
		"backup": "BACKUP", "cashback": "CASHBACK"}[g]
	c := newCard(strings.ToUpper(title))
	var bs []Button
	for _, k := range keys {
		c.kv(b.setLabel(k), b.setValue(k))
		sp := specByKey[k]
		label := b.setLabel(k)
		if sp.kind == kBool {
			if b.set.Bool(k) {
				bs = append(bs, ok(label, "a:set:t:"+k))
			} else {
				bs = append(bs, btn(label, "a:set:t:"+k))
			}
		} else {
			bs = append(bs, btn(label, "a:set:e:"+k))
		}
	}
	if g == "cashback" {
		c.line().text(b.t("cb_help"))
	}
	kb := chunk(bs, 2)
	back := "a:set"
	switch g {
	case "cashback":
		back = "a"
	case "topup":
		back = "a:pay"
	}
	kb = append(kb, row(btn(b.t("btn_back"), back)))
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) askSetting(u *User, chat, msgID int64, key string) {
	sp := specByKey[key]
	if sp == nil {
		return
	}
	back := "a:set:g:" + groupOf[key]
	if strings.HasPrefix(groupOf[key], "gw:") {
		back = "a:g:" + strings.TrimPrefix(groupOf[key], "gw:")
	} else if groupOf[key] == "cashback" {
		back = "a:cb"
	}
	c := newCard(strings.ToUpper(b.setLabel(key))).kv(b.t("lbl_now"), b.setValue(key)).line()
	switch {
	case key == "cashback_until":
		c.text(b.t("ask_days_end"))
	case sp.kind == kList:
		c.text(b.t("ask_list"))
	case sp.kind == kInt || sp.kind == kFloat:
		c.text(b.t("ask_number", "min", sp.min, "max", sp.max))
	default:
		c.text(b.t("ask_text"))
	}
	id := b.show(chat, msgID, c.String(), kbd(row(bad(b.t("btn_cancel"), back))))
	b.setState(u.ID, "adm_set", id, map[string]string{"key": key, "back": back})
}

// ---------------------------------------------------------------- texts

func (b *Bot) adminTexts(u *User, chat, msgID int64, p []string, toast func(string, bool)) {
	if len(p) == 2 {
		c := newCard(b.t("txt_title")).text(b.t("txt_body"))
		var bs []Button
		for _, k := range editableTexts {
			bs = append(bs, btn(b.t("txtname_"+k), "a:txt:"+k))
		}
		kb := chunk(bs, 2)
		kb = append(kb, row(btn(b.t("btn_back"), "a")))
		b.show(chat, msgID, c.String(), kb)
		return
	}
	key := p[2]
	valid := false
	for _, k := range editableTexts {
		valid = valid || k == key
	}
	if !valid {
		return
	}
	if len(p) == 4 {
		switch p[3] {
		case "e":
			id := b.show(chat, msgID, newCard(b.t("txtname_"+key)).text(b.t("txt_ask")).String(), kbd(row(bad(b.t("btn_cancel"), "a:txt:"+key))))
			b.setState(u.ID, "adm_txt", id, map[string]string{"key": key})
			return
		case "r":
			b.txt.reset(key)
			toast(b.t("txt_reset_done"), false)
		}
	}
	cur := b.t(key)
	custom := b.txt.custom(key)
	b.show(chat, msgID, newCard(b.t("txtname_"+key)).kv("Status", map[bool]string{true: b.t("txt_custom"), false: b.t("txt_default")}[custom]).
		line().raw(cur).String(),
		kbd(pair(ok("Edit", "a:txt:"+key+":e"), btn("Reset", "a:txt:"+key+":r")), row(btn(b.t("btn_back"), "a:txt"))))
}

// ---------------------------------------------------------------- tools

func (b *Bot) adminTools(u *User, q *CallbackQuery, chat, msgID int64, p []string, toast func(string, bool)) {
	if p[1] == "tools" {
		var size int64
		if st, err := os.Stat(dbFile); err == nil {
			size = st.Size()
		}
		last := b.lastBackup()
		pend := b.db.int(`SELECT COUNT(*) FROM orders WHERE status='pending'`)
		errs := b.db.int(`SELECT COUNT(*) FROM error_logs WHERE created_at>?`, now()-86400)
		b.show(chat, msgID, newCard("TOOLS").kv("Database", fmt.Sprintf("%.1f MB", float64(size)/(1<<20))).
			kv(b.t("lbl_last_backup"), timeFmt(last, "02-01 15:04")).kv(b.t("lbl_pending_orders"), num(pend)).kv(b.t("lbl_errors_24h"), num(errs)).String(),
			kbd(pair(ok(b.t("btn_backup_now"), "a:t:backup"), bad("Restore", "a:t:restore")),
				pair(pri("Bot Doctor", "a:t:doctor"), pri("Reconcile", "a:t:recon")),
				pair(btn("Error Log", "a:t:err"), btn("Audit Log", "a:t:audit")),
				row(btn(b.t("btn_expiring"), "a:t:exp")),
				row(btn(b.t("btn_back"), "a"))))
		return
	}
	if len(p) < 3 {
		return
	}
	back := kbd(row(btn(b.t("btn_back"), "a:tools")))
	switch p[2] {
	case "backup":
		toast(b.t("processing"), false)
		path, err := b.backup()
		if err != nil {
			b.show(chat, msgID, newCard("BACKUP").kv("Error", err.Error()).String(), back)
			return
		}
		data, err := os.ReadFile(path)
		if err == nil {
			err = b.tg.SendDocument(chat, filepath.Base(path), data, newCard("BACKUP").kv(b.t("lbl_time"), fmtTime(now())).String())
		}
		if err != nil {
			b.show(chat, msgID, newCard("BACKUP").kv("File", path).kv("Error", err.Error()).String(), back)
		}
	case "restore":
		id := b.show(chat, msgID, newCard("RESTORE").text(b.t("restore_ask")).String(), kbd(row(bad(b.t("btn_cancel"), "a:tools"))))
		b.setState(u.ID, "adm_restore", id, nil)
	case "doctor":
		toast(b.t("processing"), false)
		b.show(chat, msgID, b.doctorReport(), back)
	case "recon":
		toast(b.t("processing"), false)
		b.show(chat, msgID, b.reconcile(u.ID), back)
	case "err":
		b.show(chat, msgID, b.logList(`SELECT scope, message, created_at FROM error_logs ORDER BY id DESC LIMIT 10`, "ERROR LOG"), back)
	case "audit":
		b.show(chat, msgID, b.logList(`SELECT action || ' ' || target, detail, created_at FROM audit_logs ORDER BY id DESC LIMIT 10`, "AUDIT LOG"), back)
	case "exp":
		rows, err := b.db.Query(`SELECT `+accountCols+` FROM accounts WHERE status='active' AND trial=0 AND expires_at<? ORDER BY expires_at LIMIT 25`, now()+3*86400)
		c := newCard(b.t("exp_title"))
		n := 0
		if err == nil {
			for rows.Next() {
				if a, err := scanAccount(rows); err == nil {
					n++
					c.kv(protoLabel[a.Proto]+" "+a.Username, b.expText(a.ExpiresAt))
				}
			}
			rows.Close()
		}
		if n == 0 {
			c.text(b.t("no_data"))
		}
		b.show(chat, msgID, c.String(), back)
	}
}

func (b *Bot) logList(q, title string) string {
	c := newCard(title)
	rows, err := b.db.Query(q)
	n := 0
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var a, m string
			var ts int64
			if rows.Scan(&a, &m, &ts) == nil {
				n++
				c.text(timeFmt(ts, "02-01 15:04") + "  " + a)
				if m != "" {
					c.raw("<code>" + esc(trimRunes(m, 160)) + "</code>")
				}
			}
		}
	}
	if n == 0 {
		c.text(b.t("no_data"))
	}
	return c.String()
}

// reconcile marks accounts the servers no longer have as deleted.
func (b *Bot) reconcile(actor int64) string {
	srv, _ := b.db.servers(false)
	c := newCard("RECONCILE")
	total := 0
	for _, s := range srv {
		missing := 0
		failed := false
		for _, pr := range protoList {
			list, err := b.api(s).List(pr)
			if err != nil {
				failed = true
				break
			}
			have := map[string]bool{}
			for _, a := range list {
				have[strings.ToLower(a.Username)] = true
			}
			rows, err := b.db.Query(`SELECT id, username FROM accounts WHERE server_id=? AND proto=? AND status='active'`, s.ID, pr)
			if err != nil {
				continue
			}
			var gone []int64
			for rows.Next() {
				var id int64
				var name string
				if rows.Scan(&id, &name) == nil && !have[strings.ToLower(name)] {
					gone = append(gone, id)
				}
			}
			rows.Close()
			for _, id := range gone {
				_, _ = b.db.Exec(`UPDATE accounts SET status='deleted', updated_at=? WHERE id=?`, now(), id)
			}
			missing += len(gone)
		}
		if failed {
			c.kv(s.Name, b.t("unreachable"))
			continue
		}
		total += missing
		c.kv(s.Name, b.t("recon_n", "n", missing))
	}
	b.db.audit(actor, "reconcile", "", strconv.Itoa(total))
	return c.String()
}

// ---------------------------------------------------------------- text input

func (b *Bot) adminText(u *User, m *Message, st *State, text string) {
	chat := m.Chat.ID
	again := func(key string, kv ...any) {
		b.send(chat, b.t(key, kv...), nil)
	}
	switch st.Kind {
	case "adm_srv_name", "adm_rename":
		name := strings.TrimSpace(text)
		if len([]rune(name)) < 2 || len([]rune(name)) > 30 {
			again("err_name")
			return
		}
		if st.Kind == "adm_rename" {
			_, _ = b.db.Exec(`UPDATE servers SET name=? WHERE id=?`, name, atoi(st.Data["sid"]))
			b.clearState(u.ID)
			if s, err := b.db.server(atoi(st.Data["sid"])); err == nil {
				b.serverDetail(s, chat, 0)
			}
			return
		}
		st.Data["name"] = name
		b.setState(u.ID, "adm_srv_url", 0, st.Data)
		b.send(chat, newCard(b.t("srv_add_title")).kv(b.t("lbl_name"), name).line().text(b.t("srv_add_url")).String(), kbd(row(bad(b.t("btn_cancel"), "a:srv"))))
	case "adm_srv_url":
		u2, err := normalizeAPIURL(text)
		if err != nil {
			again("err_url")
			return
		}
		st.Data["url"] = u2
		b.setState(u.ID, "adm_srv_key", 0, st.Data)
		b.send(chat, newCard(b.t("srv_add_title")).kv(b.t("lbl_name"), st.Data["name"]).kv("URL", u2).line().text(b.t("srv_add_key")).String(),
			kbd(row(bad(b.t("btn_cancel"), "a:srv"))))
	case "adm_srv_key", "adm_srv_rekey":
		key := strings.TrimSpace(text)
		b.tg.Delete(chat, m.MessageID) // do not leave the key in the chat
		url := st.Data["url"]
		if st.Kind == "adm_srv_rekey" {
			s, err := b.db.server(atoi(st.Data["sid"]))
			if err != nil {
				b.clearState(u.ID)
				return
			}
			url = s.URL
		}
		lat, err := newTomato(url, key, 15*time.Second).Ping()
		if err != nil {
			again("srv_test_fail", "msg", err.Error())
			return
		}
		b.clearState(u.ID)
		if st.Kind == "adm_srv_rekey" {
			_, _ = b.db.Exec(`UPDATE servers SET api_key=?, status='online', last_error='' WHERE id=?`, key, atoi(st.Data["sid"]))
			if s, err := b.db.server(atoi(st.Data["sid"])); err == nil {
				b.serverDetail(s, chat, 0)
			}
			return
		}
		var sid int64
		err = b.db.tx(func(tx *sqlTx) error {
			res, err := tx.Exec(`INSERT INTO servers(name, url, api_key, status, last_check, latency_ms, created_at) VALUES(?,?,?, 'online', ?,?,?)`,
				st.Data["name"], url, key, now(), lat.Milliseconds(), now())
			if err != nil {
				return err
			}
			sid, _ = res.LastInsertId()
			for _, pr := range protoList {
				if _, err := tx.Exec(`INSERT INTO prices(server_id, proto, price) VALUES(?,?,10000)`, sid, pr); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			again("err_generic")
			return
		}
		b.db.audit(u.ID, "server_add", st.Data["name"], url)
		b.send(chat, newCard(b.t("srv_added")).kv(b.t("lbl_name"), st.Data["name"]).kv("URL", url).kv("Ping", fmt.Sprintf("%d ms", lat.Milliseconds())).
			line().text(b.t("srv_added_body")).String(), kbd(row(pri(b.t("btn_open"), fmt.Sprintf("a:s:%d", sid)))))
	case "adm_price":
		n, err := parseMoney(text)
		if err != nil || n > 100000000 {
			again("err_amount")
			return
		}
		sid := atoi(st.Data["sid"])
		_ = b.db.setPrice(sid, st.Data["proto"], n)
		b.db.audit(u.ID, "price", st.Data["proto"], rp(n))
		b.clearState(u.ID)
		if s, err := b.db.server(sid); err == nil {
			b.serverDetail(s, chat, 0)
		}
	case "adm_qi":
		f := strings.Fields(text)
		if len(f) != 2 {
			again("srv_qi_ask")
			return
		}
		qgb, e1 := strconv.Atoi(f[0])
		ip, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil || qgb < 0 || ip < 0 || qgb > 100000 || ip > 1000 {
			again("srv_qi_ask")
			return
		}
		_, _ = b.db.Exec(`UPDATE servers SET quota_gb=?, ip_limit=? WHERE id=?`, qgb, ip, atoi(st.Data["sid"]))
		b.clearState(u.ID)
		if s, err := b.db.server(atoi(st.Data["sid"])); err == nil {
			b.serverDetail(s, chat, 0)
		}
	case "adm_max":
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 0 || n > 100000 {
			again("srv_max_ask")
			return
		}
		_, _ = b.db.Exec(`UPDATE servers SET max_accounts=? WHERE id=?`, n, atoi(st.Data["sid"]))
		b.clearState(u.ID)
		if s, err := b.db.server(atoi(st.Data["sid"])); err == nil {
			b.serverDetail(s, chat, 0)
		}
	case "adm_user_find":
		x, err := b.db.findUser(text)
		if err != nil {
			again("user_not_found")
			return
		}
		b.clearState(u.ID)
		b.userDetail(x, chat, 0)
	case "adm_bal":
		f := strings.SplitN(strings.TrimSpace(text), " ", 2)
		n, err := parseMoney(f[0])
		if err != nil || n <= 0 || n > 100000000 {
			again("err_amount")
			return
		}
		note := "admin"
		if len(f) == 2 {
			note = trimRunes(strings.TrimSpace(f[1]), 100)
		}
		uid := atoi(st.Data["uid"])
		amt, typ := n, "admin_add"
		if st.Data["op"] == "sub" {
			amt, typ = -n, "admin_sub"
		}
		var bal int64
		err = b.db.tx(func(tx *sqlTx) error {
			var e error
			bal, e = addBalance(tx, uid, amt, typ, "admin:"+strconv.FormatInt(u.ID, 10), note)
			return e
		})
		if err != nil {
			again("err_balance_sub")
			return
		}
		b.clearState(u.ID)
		b.db.audit(u.ID, typ, strconv.FormatInt(uid, 10), rp(n)+" "+note)
		if st.Data["op"] == "add" {
			b.send(uid, newCard(b.t("bal_added_user")).kv(b.t("lbl_amount"), rp(n)).kv(b.t("lbl_note"), note).kv(b.t("lbl_balance"), rp(bal)).String(), nil)
		}
		if x, err := b.db.user(uid); err == nil {
			b.userDetail(x, chat, 0)
		}
	case "adm_bc":
		if strings.TrimSpace(m.Text) == "" {
			again("bc_ask")
			return
		}
		st.Data["text"] = m.Text
		b.bcPreview(u, chat, st)
	case "adm_set":
		key := st.Data["key"]
		sp := specByKey[key]
		if sp == nil {
			b.clearState(u.ID)
			return
		}
		var val string
		okv := true
		if key == "cashback_until" {
			d, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil || d < 0 || d > 3650 {
				okv = false
			} else if d == 0 {
				val = "0"
			} else {
				t := time.Now().AddDate(0, 0, d)
				val = strconv.FormatInt(time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location()).Unix(), 10)
			}
		} else {
			val, okv = parseSetting(sp, text)
		}
		if !okv {
			again("err_setting")
			return
		}
		if sp.kind == kSecret {
			b.tg.Delete(chat, m.MessageID)
		}
		_ = b.set.Set(key, val)
		b.db.audit(u.ID, "setting", key, map[bool]string{true: "***", false: val}[sp.kind == kSecret])
		b.clearState(u.ID)
		back := st.Data["back"]
		switch {
		case strings.HasPrefix(back, "a:g:"):
			if g := b.pay.Get(strings.TrimPrefix(back, "a:g:")); g != nil {
				b.gatewayDetail(g, chat, 0)
			}
		case back == "a:cb":
			b.settingsGroup(chat, 0, "cashback")
		default:
			b.settingsGroup(chat, 0, groupOf[key])
		}
	case "adm_txt":
		key := st.Data["key"]
		if err := b.txt.setCustom(key, m.Text); err != nil {
			again("txt_invalid", "msg", err.Error())
			return
		}
		b.clearState(u.ID)
		b.db.audit(u.ID, "text", key, "")
		b.adminTexts(u, chat, 0, []string{"a", "txt", key}, func(string, bool) {})
	default:
		b.clearState(u.ID)
		b.adminMenu(chat, 0)
	}
}

// adminInput handles non-text admin input (photo for broadcast, .db file
// for restore). Returns true when consumed.
func (b *Bot) adminInput(m *Message, st *State) bool {
	switch st.Kind {
	case "adm_bc":
		if len(m.Photo) == 0 {
			return false
		}
		ph := m.Photo[len(m.Photo)-1]
		st.Data["photo"] = ph.FileID
		st.Data["text"] = m.Caption
		u, _ := b.db.user(m.From.ID)
		if u != nil {
			b.bcPreview(u, m.Chat.ID, st)
		}
		return true
	case "adm_restore":
		if m.Document == nil {
			if m.Text != "" {
				b.send(m.Chat.ID, b.t("restore_ask"), nil)
			}
			return true
		}
		b.restoreFrom(m)
		return true
	}
	return false
}

func (b *Bot) bcPreview(u *User, chat int64, st *State) {
	b.setState(u.ID, "adm_bc_confirm", 0, st.Data)
	if st.Data["photo"] != "" {
		if _, err := b.tg.SendPhotoID(chat, st.Data["photo"], st.Data["text"], nil); err != nil {
			b.send(chat, b.t("txt_invalid", "msg", err.Error()), nil)
			return
		}
	} else if _, err := b.tg.Send(chat, st.Data["text"], nil); err != nil {
		b.send(chat, b.t("txt_invalid", "msg", err.Error()), nil)
		return
	}
	b.send(chat, newCard(b.t("bc_preview")).kv(b.t("lbl_target"), st.Data["target"]).line().text(b.t("bc_confirm")).String(),
		kbd(pair(ok(b.t("btn_send"), "a:bc:go"), bad(b.t("btn_cancel"), "a:bc"))))
}

func (b *Bot) lastBackup() int64 {
	ents, _ := os.ReadDir(backupDir)
	var last int64
	for _, e := range ents {
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".db") && info.ModTime().Unix() > last {
			last = info.ModTime().Unix()
		}
	}
	return last
}

// sortedKeys helper for deterministic output.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
