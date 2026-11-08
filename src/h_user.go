package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- main menu

func (b *Bot) roleLabel(u *User) string {
	switch {
	case b.cfg.isAdmin(u.ID):
		return "Admin"
	case u.reseller():
		return "Reseller"
	}
	return "Member"
}

func (b *Bot) mainMenu(u *User, chat, msgID int64) {
	if fresh, err := b.db.user(u.ID); err == nil {
		u = fresh
	}
	srv, _ := b.db.servers(true)
	online := 0
	for _, s := range srv {
		if s.Status != "offline" {
			online++
		}
	}
	active := b.db.int(`SELECT COUNT(*) FROM accounts WHERE user_id=? AND status='active'`, u.ID)
	c := newCard(strings.ToUpper(b.set.Str("store_name"))).
		kv("ID", strconv.FormatInt(u.ID, 10)).
		kv(b.t("lbl_name"), u.Name).
		kv("Status", b.roleLabel(u)).
		kv(b.t("lbl_balance"), rp(u.Balance)).
		line().
		kv(b.t("lbl_active_accounts"), strconv.FormatInt(active, 10)).
		kv("Server", b.t("servers_online", "n", len(srv), "on", online)).
		line().
		raw(b.t("welcome"))
	kb := Keyboard{}
	first := []Button{ok(b.t("btn_buy"), "buy")}
	if b.set.Bool("trial_enabled") {
		first = append(first, ok(b.t("btn_trial"), "trial"))
	}
	kb = append(kb, first, pair(pri(b.t("btn_my_accounts"), "acc"), pri(b.t("btn_topup"), "top")))
	second := []Button{btn(b.t("btn_history"), "hist:0")}
	if b.set.Bool("reseller_enabled") || u.reseller() {
		second = append(second, btn(b.t("btn_reseller"), "res"))
	}
	kb = append(kb, second)
	third := []Button{btn(b.t("btn_help"), "help")}
	if g := b.set.Str("group_url"); g != "" {
		third = append(third, btnURL(b.t("btn_group"), g))
	}
	kb = append(kb, third)
	if b.cfg.isAdmin(u.ID) {
		kb = append(kb, row(btn(b.t("btn_admin"), "a")))
	}
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) alert(q *CallbackQuery, key string, kv ...any) {
	b.tg.Answer(q.ID, b.t(key, kv...), true)
}

// errText converts an error to the buyer message.
func (b *Bot) errText(err error) string {
	var ue *userErr
	if errors.As(err, &ue) {
		return b.t(ue.key, ue.kv...)
	}
	b.db.logError("user", err)
	return b.t("err_generic")
}

// ---------------------------------------------------------------- callbacks

func (b *Bot) userCallback(u *User, q *CallbackQuery, chat, msgID int64, data string) {
	p := strings.Split(data, ":")
	answered := false
	answer := func() {
		if !answered {
			b.tg.Answer(q.ID, "", false)
			answered = true
		}
	}
	defer answer()
	switch p[0] {
	case "m":
		b.clearState(u.ID)
		b.mainMenu(u, chat, msgID)
	case "noop":
	case "buy":
		if len(p) == 2 && p[1] == "ok" {
			b.buyExecute(u, q, chat, msgID, &answered)
			return
		}
		b.clearState(u.ID)
		switch {
		case len(p) == 1:
			b.buyProtos(chat, msgID, "buy")
		case len(p) == 3 && p[1] == "p" && isProto(p[2]):
			b.buyServers(u, q, chat, msgID, p[2], &answered)
		case len(p) == 4 && p[1] == "s" && isProto(p[2]):
			b.buyDurations(u, q, chat, msgID, p[2], atoi(p[3]), &answered)
		case len(p) == 5 && p[1] == "d" && isProto(p[2]):
			b.buyAskUser(u, q, chat, msgID, p[2], atoi(p[3]), atoi(p[4]), &answered)
		}
	case "acc":
		b.accountsCallback(u, q, chat, msgID, p, &answered)
	case "trial":
		b.trialCallback(u, q, chat, msgID, p, &answered)
	case "top", "inv":
		b.topupCallback(u, q, chat, msgID, p, &answered)
	case "hist":
		off := int64(0)
		if len(p) > 1 {
			off = atoi(p[1])
		}
		b.history(u, chat, msgID, off)
	case "res":
		b.resellerCallback(u, q, chat, msgID, p, &answered)
	case "help":
		b.helpCallback(chat, msgID, p)
	default:
		b.mainMenu(u, chat, msgID)
	}
}

// userInput handles typed text for the current state.
func (b *Bot) userInput(u *User, m *Message, st *State, text string) {
	switch st.Kind {
	case "buy_user":
		b.buyGotUser(u, m.Chat.ID, st, text)
	case "top_amount":
		n, err := parseMoney(text)
		if err != nil || n <= 0 {
			b.send(m.Chat.ID, b.t("err_amount"), kbd(row(bad(b.t("btn_cancel"), "m"))))
			return
		}
		b.clearState(u.ID)
		b.topupMethods(u, nil, m.Chat.ID, 0, n, nil)
	default:
		if b.cfg.isAdmin(u.ID) {
			b.adminText(u, m, st, text)
			return
		}
		b.clearState(u.ID)
		b.mainMenu(u, m.Chat.ID, 0)
	}
}

// ---------------------------------------------------------------- buy

// buyProtos — protocols sold by at least one enabled server.
func (b *Bot) buyProtos(chat, msgID int64, prefix string) {
	srv, _ := b.db.servers(true)
	var bs []Button
	for _, p := range protoList {
		for _, s := range srv {
			if s.Protos[p] {
				bs = append(bs, ok(protoLabel[p], prefix+":p:"+p))
				break
			}
		}
	}
	title := b.t("buy_title")
	if prefix == "trial" {
		title = b.t("trial_title")
	}
	if len(bs) == 0 {
		b.show(chat, msgID, newCard(title).text(b.t("no_servers")).String(), kbd(row(btn(b.t("btn_back"), "m"))))
		return
	}
	kb := chunk(bs, 2)
	kb = append(kb, row(btn(b.t("btn_back"), "m")))
	b.show(chat, msgID, newCard(title).text(b.t("choose_proto")).String(), kb)
}

func (b *Bot) protoServers(proto string) []*Server {
	srv, _ := b.db.servers(true)
	var out []*Server
	for _, s := range srv {
		if s.Protos[proto] {
			out = append(out, s)
		}
	}
	return out
}

func (b *Bot) slotText(s *Server) string {
	switch b.slotState(s) {
	case "full":
		return b.t("slot_full")
	case "offline":
		return "offline"
	}
	n := b.db.activeCount(s.ID)
	if s.MaxAccounts <= 0 {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%d / %d", n, s.MaxAccounts)
}

func quotaText(s *Server) string {
	q := "Unlimited"
	if s.QuotaGB > 0 {
		q = fmt.Sprintf("%d GB", s.QuotaGB)
	}
	return q + " · " + ipText(s.IPLimit)
}

func (b *Bot) buyServers(u *User, q *CallbackQuery, chat, msgID int64, proto string, answered *bool) {
	srv := b.protoServers(proto)
	if len(srv) == 0 {
		b.alert(q, "no_servers")
		*answered = true
		return
	}
	c := newCard(b.t("buy_proto_title", "proto", protoLabel[proto])).text(b.t("price_per_30")).line()
	var kb Keyboard
	for i, s := range srv {
		c.text(fmt.Sprintf("%d  %s", i+1, s.Name)).
			kv(b.t("lbl_price"), rp(b.unitPrice(u, s, proto))).
			kv("Slot", b.slotText(s)).
			kv(b.t("lbl_quota"), quotaText(s)).
			line()
		label := fmt.Sprintf("%d  %s", i+1, s.Name)
		switch b.slotState(s) {
		case "full":
			label += " (" + b.t("slot_full") + ")"
		case "offline":
			label += " (offline)"
		}
		kb = append(kb, row(btn(label, fmt.Sprintf("buy:s:%s:%d", proto, s.ID))))
	}
	kb = append(kb, row(btn(b.t("btn_back"), "buy")))
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) buyDurations(u *User, q *CallbackQuery, chat, msgID int64, proto string, sid int64, answered *bool) {
	s, err := b.db.server(sid)
	if err != nil || !s.Enabled || !s.Protos[proto] {
		b.alert(q, "err_server_closed")
		*answered = true
		return
	}
	if st := b.slotState(s); st != "" {
		b.alert(q, map[string]string{"full": "err_server_full", "offline": "err_server_offline"}[st])
		*answered = true
		return
	}
	c := newCard(b.t("buy_proto_title", "proto", protoLabel[proto])+" · "+s.Name).
		kv(b.t("lbl_balance"), rp(u.Balance)).line().text(b.t("choose_duration"))
	var bs []Button
	for _, d := range b.set.List("durations") {
		bs = append(bs, btn(fmt.Sprintf("%s · %s", b.t("days_n", "n", d), rp(b.priceFor(u, s, proto, d))), fmt.Sprintf("buy:d:%s:%d:%d", proto, sid, d)))
	}
	kb := chunk(bs, 2)
	kb = append(kb, row(btn(b.t("btn_back"), "buy:p:"+proto)))
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) buyAskUser(u *User, q *CallbackQuery, chat, msgID int64, proto string, sid, days int64, answered *bool) {
	s, err := b.db.server(sid)
	valid := false
	for _, d := range b.set.List("durations") {
		valid = valid || d == days
	}
	if err != nil || !valid || !s.Enabled || !s.Protos[proto] {
		b.alert(q, "err_server_closed")
		*answered = true
		return
	}
	price := b.priceFor(u, s, proto, days)
	if u.Balance < price {
		b.show(chat, msgID, newCard(b.t("err_balance_title")).kv(b.t("lbl_price"), rp(price)).kv(b.t("lbl_balance"), rp(u.Balance)).
			line().text(b.t("err_balance_body")).String(),
			kbd(row(pri(b.t("btn_topup"), "top")), row(btn(b.t("btn_back"), fmt.Sprintf("buy:s:%s:%d", proto, sid)))))
		return
	}
	c := newCard(b.t("buy_proto_title", "proto", protoLabel[proto])+" · "+b.t("days_n", "n", days)).
		kv("Server", s.Name).kv(b.t("lbl_price"), rp(price)).line().text(b.t("ask_username"))
	switch proto {
	case "ssh", "zivpn":
		c.text(b.t("ask_password_hint"))
	}
	id := b.show(chat, msgID, c.String(), kbd(row(bad(b.t("btn_cancel"), "m"))))
	b.setState(u.ID, "buy_user", id, map[string]string{"proto": proto, "sid": strconv.FormatInt(sid, 10), "days": strconv.FormatInt(days, 10)})
}

func (b *Bot) buyGotUser(u *User, chat int64, st *State, text string) {
	proto := st.Data["proto"]
	username, password := text, ""
	if i := strings.IndexAny(text, ": "); i > 0 {
		username, password = strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+1:])
	}
	if !validUsername(username) {
		b.send(chat, b.t("err_username"), kbd(row(bad(b.t("btn_cancel"), "m"))))
		return
	}
	if password != "" && (proto == "ssh" || proto == "zivpn") && !validPassword(password) {
		b.send(chat, b.t("err_password"), kbd(row(bad(b.t("btn_cancel"), "m"))))
		return
	}
	if proto != "ssh" && proto != "zivpn" {
		password = ""
	}
	if b.set.Bool("username_suffix") {
		username = username + randString(3, "0123456789")
	}
	sid, days := atoi(st.Data["sid"]), atoi(st.Data["days"])
	s, err := b.db.server(sid)
	if err != nil {
		b.clearState(u.ID)
		b.send(chat, b.t("err_server_closed"), nil)
		return
	}
	if b.db.usernameTaken(sid, proto, username) {
		b.send(chat, b.t("err_username_taken"), kbd(row(bad(b.t("btn_cancel"), "m"))))
		return
	}
	price := b.priceFor(u, s, proto, days)
	c := newCard(b.t("confirm_title")).
		kv(b.t("lbl_proto"), protoLabel[proto]).
		kv("Server", s.Name).
		kv(b.t("lbl_username"), username)
	if password != "" {
		c.kv("Password", password)
	}
	c.kv(b.t("lbl_duration"), b.t("days_n", "n", days)).line().
		kv(b.t("lbl_price"), rp(price)).
		kv(b.t("lbl_balance"), rp(u.Balance)).
		kv(b.t("lbl_left"), rp(u.Balance-price))
	st.Data["username"], st.Data["password"] = username, password
	b.setState(u.ID, "buy_confirm", 0, st.Data)
	if st.MsgID > 0 {
		_ = b.tg.EditMarkup(chat, st.MsgID, nil)
	}
	b.send(chat, c.String(), kbd(pair(ok(b.t("btn_pay"), "buy:ok"), bad(b.t("btn_cancel"), "m"))))
}

func (b *Bot) buyExecute(u *User, q *CallbackQuery, chat, msgID int64, answered *bool) {
	st := b.getState(u.ID)
	if st == nil || st.Kind != "buy_confirm" {
		b.alert(q, "err_expired_action")
		*answered = true
		b.mainMenu(u, chat, msgID)
		return
	}
	b.clearState(u.ID)
	b.tg.Answer(q.ID, b.t("processing"), false)
	*answered = true
	_ = b.tg.Edit(chat, msgID, newCard(b.t("confirm_title")).text(b.t("processing")).String(), nil)
	s, err := b.db.server(atoi(st.Data["sid"]))
	if err != nil {
		b.show(chat, msgID, b.t("err_server_closed"), kbd(row(btn(b.t("btn_menu"), "m"))))
		return
	}
	res, err := b.buy(u, s, st.Data["proto"], st.Data["username"], st.Data["password"], atoi(st.Data["days"]))
	if err != nil {
		b.show(chat, msgID, newCard(b.t("failed_title")).text(b.errText(err)).String(), kbd(row(btn(b.t("btn_menu"), "m"))))
		return
	}
	if res.Pending {
		b.show(chat, msgID, newCard(b.t("pending_title")).text(b.t("pending_body")).String(), kbd(row(btn(b.t("btn_menu"), "m"))))
		return
	}
	b.show(chat, msgID, formatAccount(res.Account.Proto, "PREMIUM", res.Live, res.Account.ExpiresAt),
		kbd(pair(pri(b.t("btn_my_accounts"), "acc"), btn(b.t("btn_menu"), "m"))))
	if b.set.Bool("notify_admin") {
		b.notifyAdmins(newCard(b.t("adm_sale_title")).
			kv("User", fmt.Sprintf("%s (%d)", u.Name, u.ID)).
			kv(b.t("lbl_product"), protoLabel[res.Account.Proto]+" "+b.t("days_n", "n", atoi(st.Data["days"]))).
			kv("Server", s.Name).kv(b.t("lbl_username"), res.Account.Username).
			kv(b.t("lbl_price"), rp(res.Price)).kv(b.t("lbl_time"), fmtTime(now())).String())
	}
}

// ---------------------------------------------------------------- my accounts

func (b *Bot) accountsCallback(u *User, q *CallbackQuery, chat, msgID int64, p []string, answered *bool) {
	if len(p) == 1 {
		b.accountList(u, chat, msgID)
		return
	}
	var id int64
	if len(p) >= 3 {
		id = atoi(p[2])
	} else {
		id = atoi(p[1])
	}
	a, err := b.db.account(id)
	if err != nil || a.UserID != u.ID || a.Status != "active" {
		b.alert(q, "err_account_gone")
		*answered = true
		b.accountList(u, chat, msgID)
		return
	}
	s, err := b.db.server(a.ServerID)
	if err != nil {
		b.alert(q, "err_server_closed")
		*answered = true
		return
	}
	act := ""
	if len(p) >= 3 {
		act = p[1]
	}
	switch act {
	case "": // detail
		b.accountDetail(u, a, s, chat, msgID)
	case "c": // config
		live, err := b.api(s).Get(a.Proto, a.Username)
		if err != nil {
			b.alert(q, "err_server_reject", "msg", err.Error())
			*answered = true
			return
		}
		kind := "PREMIUM"
		if a.Trial {
			kind = "TRIAL"
		}
		b.show(chat, msgID, formatAccount(a.Proto, kind, live, a.ExpiresAt), kbd(row(btn(b.t("btn_back"), fmt.Sprintf("acc:%d", a.ID)))))
	case "r": // renew: durations
		if a.Trial {
			b.alert(q, "err_trial_renew")
			*answered = true
			return
		}
		c := newCard(b.t("renew_title")+" · "+a.Username).kv("Expired", fmtTime(a.ExpiresAt)).kv(b.t("lbl_balance"), rp(u.Balance)).line().text(b.t("choose_duration"))
		var bs []Button
		for _, d := range b.set.List("durations") {
			bs = append(bs, btn(fmt.Sprintf("%s · %s", b.t("days_n", "n", d), rp(b.priceFor(u, s, a.Proto, d))), fmt.Sprintf("acc:rd:%d:%d", a.ID, d)))
		}
		kb := chunk(bs, 2)
		kb = append(kb, row(btn(b.t("btn_back"), fmt.Sprintf("acc:%d", a.ID))))
		b.show(chat, msgID, c.String(), kb)
	case "rd": // renew confirm
		days := int64(0)
		if len(p) == 4 {
			days = atoi(p[3])
		}
		valid := false
		for _, d := range b.set.List("durations") {
			valid = valid || d == days
		}
		if !valid {
			return
		}
		price := b.priceFor(u, s, a.Proto, days)
		c := newCard(b.t("renew_title")).kv(b.t("lbl_proto"), protoLabel[a.Proto]).kv(b.t("lbl_username"), a.Username).
			kv("Server", s.Name).kv(b.t("lbl_duration"), "+"+b.t("days_n", "n", days)).line().
			kv(b.t("lbl_price"), rp(price)).kv(b.t("lbl_balance"), rp(u.Balance)).kv(b.t("lbl_left"), rp(u.Balance-price))
		kb := kbd(pair(ok(b.t("btn_pay"), fmt.Sprintf("acc:ro:%d:%d", a.ID, days)), bad(b.t("btn_cancel"), fmt.Sprintf("acc:%d", a.ID))))
		if u.Balance < price {
			c.line().text(b.t("err_balance_body"))
			kb = kbd(row(pri(b.t("btn_topup"), "top")), row(btn(b.t("btn_back"), fmt.Sprintf("acc:%d", a.ID))))
		}
		b.show(chat, msgID, c.String(), kb)
	case "ro": // renew execute
		days := int64(0)
		if len(p) == 4 {
			days = atoi(p[3])
		}
		valid := false
		for _, d := range b.set.List("durations") {
			valid = valid || d == days
		}
		if !valid {
			return
		}
		b.tg.Answer(q.ID, b.t("processing"), false)
		*answered = true
		na, _, price, pending, err := b.renew(u, a, days)
		if err != nil {
			b.show(chat, msgID, newCard(b.t("failed_title")).text(b.errText(err)).String(), kbd(row(btn(b.t("btn_back"), "acc"))))
			return
		}
		if pending {
			b.show(chat, msgID, newCard(b.t("pending_title")).text(b.t("pending_body")).String(), kbd(row(btn(b.t("btn_menu"), "m"))))
			return
		}
		fu, _ := b.db.user(u.ID)
		bal := u.Balance - price
		if fu != nil {
			bal = fu.Balance
		}
		b.show(chat, msgID, newCard(b.t("renew_done")).kv(b.t("lbl_proto"), protoLabel[na.Proto]).kv(b.t("lbl_username"), na.Username).
			kv("Server", s.Name).kv(b.t("lbl_duration"), "+"+b.t("days_n", "n", days)).kv("Expired", fmtTime(na.ExpiresAt)).
			kv(b.t("lbl_price"), rp(price)).kv(b.t("lbl_balance"), rp(bal)).String(),
			kbd(pair(pri(b.t("btn_my_accounts"), "acc"), btn(b.t("btn_menu"), "m"))))
		if b.set.Bool("notify_admin") {
			b.notifyAdmins(newCard(b.t("adm_renew_title")).kv("User", fmt.Sprintf("%s (%d)", u.Name, u.ID)).
				kv(b.t("lbl_product"), protoLabel[na.Proto]+" +"+b.t("days_n", "n", days)).kv("Server", s.Name).
				kv(b.t("lbl_username"), na.Username).kv(b.t("lbl_price"), rp(price)).String())
		}
	case "x": // delete confirm
		if !b.set.Bool("allow_delete") && !b.cfg.isAdmin(u.ID) {
			b.alert(q, "err_delete_off")
			*answered = true
			return
		}
		b.show(chat, msgID, newCard(b.t("delete_title")).kv(b.t("lbl_proto"), protoLabel[a.Proto]).kv(b.t("lbl_username"), a.Username).
			kv("Expired", fmtTime(a.ExpiresAt)).line().text(b.t("delete_warn")).String(),
			kbd(pair(bad(b.t("btn_delete_yes"), fmt.Sprintf("acc:xo:%d", a.ID)), btn(b.t("btn_cancel"), fmt.Sprintf("acc:%d", a.ID)))))
	case "xo":
		if !b.set.Bool("allow_delete") && !b.cfg.isAdmin(u.ID) {
			return
		}
		if err := b.api(s).Delete(a.Proto, a.Username); err != nil && !notFound(err) {
			b.alert(q, "err_server_reject", "msg", err.Error())
			*answered = true
			return
		}
		_, _ = b.db.Exec(`UPDATE accounts SET status='deleted', updated_at=? WHERE id=?`, now(), a.ID)
		b.db.audit(u.ID, "delete_account", a.Username, protoLabel[a.Proto])
		b.tg.Answer(q.ID, b.t("deleted"), false)
		*answered = true
		b.accountList(u, chat, msgID)
	}
}

func (b *Bot) accountList(u *User, chat, msgID int64) {
	list, _ := b.db.userAccounts(u.ID)
	c := newCard(b.t("acc_title"))
	if len(list) == 0 {
		c.text(b.t("acc_none"))
		b.show(chat, msgID, c.String(), kbd(row(ok(b.t("btn_buy"), "buy")), row(btn(b.t("btn_back"), "m"))))
		return
	}
	c.text(b.t("acc_count", "n", len(list))).line()
	srvName := map[int64]string{}
	var bs []Button
	for i, a := range list {
		if i >= 30 {
			break
		}
		if _, ok := srvName[a.ServerID]; !ok {
			if s, err := b.db.server(a.ServerID); err == nil {
				srvName[a.ServerID] = s.Name
			}
		}
		title := fmt.Sprintf("%d  %s · %s", i+1, protoLabel[a.Proto], a.Username)
		if a.Trial {
			title += " (trial)"
		}
		c.text(title).kv("Server", srvName[a.ServerID]).kv("Expired", b.expText(a.ExpiresAt)).line()
		bs = append(bs, btn(fmt.Sprintf("%d  %s", i+1, a.Username), fmt.Sprintf("acc:%d", a.ID)))
	}
	kb := chunk(bs, 3)
	kb = append(kb, row(btn(b.t("btn_back"), "m")))
	b.show(chat, msgID, c.String(), kb)
}

func (b *Bot) expText(ts int64) string {
	left := ts - now()
	switch {
	case left <= 0:
		return fmtDate(ts) + " (" + b.t("expired_word") + ")"
	case left < 86400:
		return fmtTime(ts) + " (" + b.t("hours_n", "n", left/3600) + ")"
	}
	return fmtDate(ts) + " (" + b.t("days_n", "n", left/86400) + ")"
}

func (b *Bot) accountDetail(u *User, a *AccountRow, s *Server, chat, msgID int64) {
	c := newCard(protoLabel[a.Proto]+" · "+a.Username).kv("Server", s.Name).kv("Expired", b.expText(a.ExpiresAt))
	if live, err := b.api(s).Get(a.Proto, a.Username); err == nil {
		c.kv(b.t("lbl_quota"), usage(live.UsedBytes, live.QuotaBytes)).kv("IP Limit", ipText(live.IPLimit))
		status := b.t("st_active")
		if live.Locked {
			status = b.t("st_locked")
		}
		c.kv("Status", status)
	} else if notFound(err) {
		c.kv("Status", b.t("st_missing"))
	}
	var kb Keyboard
	if a.Trial {
		kb = kbd(row(pri(b.t("btn_config"), fmt.Sprintf("acc:c:%d", a.ID))))
	} else {
		kb = kbd(pair(pri(b.t("btn_config"), fmt.Sprintf("acc:c:%d", a.ID)), ok(b.t("btn_renew"), fmt.Sprintf("acc:r:%d", a.ID))))
		if b.set.Bool("allow_delete") || b.cfg.isAdmin(u.ID) {
			kb = append(kb, row(bad(b.t("btn_delete"), fmt.Sprintf("acc:x:%d", a.ID))))
		}
	}
	kb = append(kb, row(btn(b.t("btn_back"), "acc")))
	b.show(chat, msgID, c.String(), kb)
}

// ---------------------------------------------------------------- trial

func (b *Bot) trialCallback(u *User, q *CallbackQuery, chat, msgID int64, p []string, answered *bool) {
	if !b.set.Bool("trial_enabled") {
		b.alert(q, "err_trial_off")
		*answered = true
		return
	}
	left, quota, perDay := b.trialLeft(u)
	switch {
	case len(p) == 1:
		per := b.t("per_week")
		if perDay {
			per = b.t("per_day")
		}
		srv, _ := b.db.servers(true)
		var bs []Button
		for _, pr := range protoList {
			for _, s := range srv {
				if s.Protos[pr] {
					bs = append(bs, ok(protoLabel[pr], "trial:p:"+pr))
					break
				}
			}
		}
		kb := chunk(bs, 2)
		kb = append(kb, row(btn(b.t("btn_back"), "m")))
		b.show(chat, msgID, newCard(b.t("trial_title")).
			kv(b.t("lbl_duration"), b.t("minutes_n", "n", b.set.Int("trial_minutes"))).
			kv(b.t("lbl_quota_trial"), fmt.Sprintf("%d %s (%s %d)", quota, per, b.t("left_word"), left)).
			line().text(b.t("choose_proto")).String(), kb)
	case len(p) == 3 && p[1] == "p" && isProto(p[2]):
		srv := b.protoServers(p[2])
		if len(srv) == 1 {
			b.trialRun(u, q, chat, msgID, p[2], srv[0], answered)
			return
		}
		var kb Keyboard
		for i, s := range srv {
			kb = append(kb, row(btn(fmt.Sprintf("%d  %s", i+1, s.Name), fmt.Sprintf("trial:s:%s:%d", p[2], s.ID))))
		}
		kb = append(kb, row(btn(b.t("btn_back"), "trial")))
		b.show(chat, msgID, newCard(b.t("trial_title")+" · "+protoLabel[p[2]]).text(b.t("choose_server")).String(), kb)
	case len(p) == 4 && p[1] == "s" && isProto(p[2]):
		s, err := b.db.server(atoi(p[3]))
		if err != nil {
			return
		}
		b.trialRun(u, q, chat, msgID, p[2], s, answered)
	}
}

func (b *Bot) trialRun(u *User, q *CallbackQuery, chat, msgID int64, proto string, s *Server, answered *bool) {
	b.tg.Answer(q.ID, b.t("processing"), false)
	*answered = true
	live, exp, err := b.trial(u, s, proto)
	if err != nil {
		b.show(chat, msgID, newCard(b.t("failed_title")).text(b.errText(err)).String(), kbd(row(btn(b.t("btn_back"), "trial"))))
		return
	}
	b.show(chat, msgID, formatAccount(proto, "TRIAL", live, exp), kbd(pair(ok(b.t("btn_buy"), "buy"), btn(b.t("btn_menu"), "m"))))
}

// ---------------------------------------------------------------- history

func (b *Bot) history(u *User, chat, msgID int64, off int64) {
	if off < 0 {
		off = 0
	}
	rows, err := b.db.Query(`SELECT type, amount, note, created_at FROM transactions WHERE user_id=? ORDER BY id DESC LIMIT 11 OFFSET ?`, u.ID, off)
	c := newCard(b.t("hist_title"))
	n := 0
	if err == nil {
		defer rows.Close()
		first := true
		for rows.Next() {
			var typ, note string
			var amt, ts int64
			if rows.Scan(&typ, &amt, &note, &ts) != nil {
				continue
			}
			n++
			if n > 10 {
				break
			}
			if first {
				c.text(b.t("hist_sub")).line()
				first = false
			}
			sign := "+ "
			if amt < 0 {
				sign, amt = "- ", -amt
			}
			label := b.t("tx_" + typ)
			if strings.HasPrefix(label, "tx_") {
				label = typ
			}
			if note != "" && typ != "topup" && typ != "cashback" {
				label = note
			}
			c.kv(fmtShort(ts), sign+rp(amt)+" "+label)
		}
	}
	if n == 0 && off == 0 {
		c.text(b.t("hist_none"))
	}
	var nav []Button
	if off > 0 {
		nav = append(nav, btn(b.t("btn_prev"), fmt.Sprintf("hist:%d", off-10)))
	}
	if n > 10 {
		nav = append(nav, btn(b.t("btn_next"), fmt.Sprintf("hist:%d", off+10)))
	}
	kb := Keyboard{}
	if len(nav) > 0 {
		kb = append(kb, nav)
	}
	kb = append(kb, row(btn(b.t("btn_back"), "m")))
	b.show(chat, msgID, c.String(), kb)
}

func fmtShort(ts int64) string { return timeFmt(ts, "02-01 15:04") }

// ---------------------------------------------------------------- reseller

func (b *Bot) resellerCallback(u *User, q *CallbackQuery, chat, msgID int64, p []string, answered *bool) {
	price := b.set.Int("reseller_price")
	disc := b.set.Int("reseller_discount")
	if u.reseller() {
		sales := b.db.int(`SELECT COALESCE(SUM(-amount),0) FROM transactions WHERE user_id=? AND type IN ('purchase','renew') AND created_at>?`, u.ID, monthStart())
		cnt := b.db.int(`SELECT COUNT(*) FROM orders WHERE user_id=? AND status='done' AND created_at>?`, u.ID, monthStart())
		left, quota, _ := b.trialLeft(u)
		b.show(chat, msgID, newCard(b.t("res_title")).kv("Status", "Reseller").
			kv(b.t("lbl_discount"), fmt.Sprintf("%d%%", disc)).
			kv("Trial", fmt.Sprintf("%d / %s (%s %d)", quota, b.t("per_day"), b.t("left_word"), left)).line().
			kv(b.t("lbl_month_spend"), rp(sales)).kv(b.t("lbl_month_orders"), strconv.FormatInt(cnt, 10)).String(),
			kbd(row(btn(b.t("btn_back"), "m"))))
		return
	}
	if !b.set.Bool("reseller_enabled") {
		b.alert(q, "err_reseller_off")
		*answered = true
		return
	}
	switch {
	case len(p) == 1:
		b.show(chat, msgID, newCard(b.t("res_title")).kv("Status", "Member").
			kv(b.t("lbl_upgrade_price"), rp(price)).
			kv(b.t("lbl_discount"), b.t("discount_all", "n", disc)).
			kv("Trial", fmt.Sprintf("%d / %s", b.set.Int("trial_reseller_daily"), b.t("per_day"))).
			line().text(b.t("res_body")).String(),
			kbd(row(ok(b.t("btn_upgrade"), "res:up")), row(btn(b.t("btn_back"), "m"))))
	case p[1] == "up":
		if u.Balance < price {
			b.show(chat, msgID, newCard(b.t("err_balance_title")).kv(b.t("lbl_price"), rp(price)).kv(b.t("lbl_balance"), rp(u.Balance)).
				line().text(b.t("err_balance_body")).String(), kbd(row(pri(b.t("btn_topup"), "top")), row(btn(b.t("btn_back"), "res"))))
			return
		}
		b.show(chat, msgID, newCard(b.t("confirm_title")).kv(b.t("lbl_product"), b.t("res_upgrade")).kv(b.t("lbl_price"), rp(price)).
			kv(b.t("lbl_balance"), rp(u.Balance)).kv(b.t("lbl_left"), rp(u.Balance-price)).String(),
			kbd(pair(ok(b.t("btn_pay"), "res:ok"), bad(b.t("btn_cancel"), "res"))))
	case p[1] == "ok":
		err := b.db.tx(func(tx *sqlTx) error {
			var role string
			if err := tx.QueryRow(`SELECT role FROM users WHERE id=?`, u.ID).Scan(&role); err != nil {
				return err
			}
			if role == "reseller" {
				return nil
			}
			if price > 0 {
				if _, err := addBalance(tx, u.ID, -price, "reseller", "", "upgrade reseller"); err != nil {
					return err
				}
			}
			_, err := tx.Exec(`UPDATE users SET role='reseller' WHERE id=?`, u.ID)
			return err
		})
		if err != nil {
			if errors.Is(err, errInsufficient) {
				b.alert(q, "err_balance", "need", rp(price), "balance", rp(u.Balance))
			} else {
				b.alert(q, "err_generic")
				b.db.logError("reseller", err)
			}
			*answered = true
			return
		}
		b.db.audit(u.ID, "reseller_upgrade", strconv.FormatInt(u.ID, 10), rp(price))
		if b.set.Bool("notify_admin") {
			b.notifyAdmins(newCard(b.t("adm_reseller_title")).kv("User", fmt.Sprintf("%s (%d)", u.Name, u.ID)).kv(b.t("lbl_price"), rp(price)).String())
		}
		u.Role = "reseller"
		b.show(chat, msgID, newCard(b.t("res_done")).kv(b.t("lbl_discount"), fmt.Sprintf("%d%%", disc)).String(), kbd(row(btn(b.t("btn_menu"), "m"))))
	}
}

// ---------------------------------------------------------------- help

func (b *Bot) helpCallback(chat, msgID int64, p []string) {
	back := kbd(row(btn(b.t("btn_back"), "help")))
	if len(p) == 2 {
		switch p[1] {
		case "buy":
			b.show(chat, msgID, b.t("howto_buy"), back)
			return
		case "topup":
			b.show(chat, msgID, b.t("howto_topup"), back)
			return
		case "tos":
			b.show(chat, msgID, b.t("tos"), back)
			return
		}
	}
	c := newCard(b.t("help_title"))
	cs := b.set.Str("cs_username")
	c.kv("CS", cs).line().text(b.t("choose_topic"))
	kb := kbd(pair(btn(b.t("btn_howto_buy"), "help:buy"), btn(b.t("btn_howto_topup"), "help:topup")), row(btn(b.t("btn_tos"), "help:tos")))
	if strings.HasPrefix(cs, "@") {
		kb = append(kb, row(btnURL(b.t("btn_chat_cs"), "https://t.me/"+strings.TrimPrefix(cs, "@"))))
	}
	kb = append(kb, row(btn(b.t("btn_back"), "m")))
	b.show(chat, msgID, c.String(), kb)
}
