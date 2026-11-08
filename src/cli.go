package main

// Terminal side: setup wizard, menu-store and doctor, in the Tomato
// Autoscript look (see ui.go).

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var stdin = bufio.NewReader(os.Stdin)

func ask(prompt string) string {
	fmt.Print(prompt)
	s, _ := stdin.ReadString('\n')
	return strings.TrimSpace(s)
}

func clearScreen() { fmt.Print("\x1b[H\x1b[2J") }

func maskToken(t string) string {
	if t == "" {
		return "-"
	}
	if len(t) <= 12 {
		return t
	}
	return t[:4] + "…" + t[len(t)-4:]
}

// ---------------------------------------------------------------- setup

func setup() error {
	clearScreen()
	uiHead("store bot setup", "v"+version)
	uiNote("sell VPN accounts from Telegram: buy, trial, renew, top up")
	uiHr()
	cfg, err := loadConfig()
	if err != nil {
		cfg = &Config{}
	}
	if err := setToken(cfg, true); err != nil {
		return err
	}
	if err := setAdmins(cfg, true); err != nil {
		return err
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	db, err := openDB(dbFile)
	if err != nil {
		return err
	}
	defer db.Close()
	s := newSettings(db)
	uiHr()
	setStore(s)
	_ = db.ensureLocalServer()
	uiHr2()
	uiOk("Setup saved")
	uiNote("Open the bot and send /admin to set payment gateways, prices and texts.")
	uiHr2()
	return nil
}

func setToken(cfg *Config, required bool) error {
	if cfg.Token != "" {
		uiLead("token", maskToken(cfg.Token))
	}
	for {
		in := uiInput("bot token (@BotFather)")
		if in == "" && cfg.Token != "" {
			return nil
		}
		if in == "" && !required {
			return nil
		}
		if strings.Count(in, ":") != 1 || len(in) < 30 {
			uiErr("Invalid bot token (from @BotFather).")
			continue
		}
		api := cfg.TelegramAPI
		if api == "" {
			api = telegramAPI
		}
		me, err := newTG(api, in).GetMe()
		if err != nil {
			uiErr("Telegram: " + err.Error())
			continue
		}
		uiOk("Bot @" + me.Username + " found.")
		cfg.Token = in
		return nil
	}
}

func setAdmins(cfg *Config, required bool) error {
	if len(cfg.Admins) > 0 {
		uiLead("admins", joinIDs(cfg.Admins))
	}
	for {
		in := uiInput("admin IDs (space separated)")
		if in == "" && (len(cfg.Admins) > 0 || !required) {
			return nil
		}
		var ids []int64
		okAll := in != ""
		for _, p := range strings.FieldsFunc(in, func(r rune) bool { return r == ',' || r == ' ' }) {
			n, err := strconv.ParseInt(p, 10, 64)
			if err != nil || n <= 0 {
				uiErr("Invalid admin ID: " + p)
				okAll = false
				break
			}
			ids = append(ids, n)
		}
		if okAll && len(ids) > 0 {
			cfg.Admins = ids
			uiOk("Admins saved: " + joinIDs(ids))
			return nil
		}
		if in == "" {
			uiErr("Admin ID cannot be empty (get it from @userinfobot).")
		}
	}
}

func joinIDs(ids []int64) string {
	s := make([]string, len(ids))
	for i, a := range ids {
		s[i] = strconv.FormatInt(a, 10)
	}
	return strings.Join(s, " ")
}

func setStore(s *Settings) {
	if in := uiInput("store name [" + s.Str("store_name") + "]"); in != "" {
		_ = s.Set("store_name", trimRunes(in, 40))
	}
	cs := s.Str("cs_username")
	if cs == "" {
		cs = "-"
	}
	if in := uiInput("support username [" + cs + "]"); in != "" {
		if v, ok := parseSetting(specByKey["cs_username"], in); ok {
			_ = s.Set("cs_username", v)
		}
	}
	if in := strings.ToLower(uiInput("language id/en [" + s.Str("lang") + "]")); in == "id" || in == "en" {
		_ = s.Set("lang", in)
	}
}

// ---------------------------------------------------------------- menu

func serviceActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", "tomato-store").Run() == nil
}

func menu() {
	for {
		clearScreen()
		state := uiOff("stopped")
		if serviceActive() {
			state = uiOn("running")
		}
		uiHead("store bot", state)
		uiNote("sell VPN accounts from Telegram: buy, trial, renew, top up")
		uiHr()
		cfg, cerr := loadConfig()
		uiLead("version", version)
		if cerr != nil {
			uiLead("config", uiR+"not set")
		} else {
			uiLead("token", maskToken(cfg.Token))
			uiLead("admins", joinIDs(cfg.Admins))
		}
		if db, err := openDB(dbFile); err == nil {
			s := newSettings(db)
			srv, _ := db.servers(false)
			on := 0
			for _, x := range srv {
				if x.Enabled && x.Status != "offline" {
					on++
				}
			}
			uiLead("store", s.Str("store_name"))
			uiLead("servers", fmt.Sprintf("%d (%d online)", len(srv), on))
			var gws []string
			for _, g := range newPayments(s).Active() {
				gws = append(gws, strings.ToLower(g.Label()))
			}
			if len(gws) == 0 {
				uiLead("payment", uiY+"not set")
			} else {
				uiLead("payment", strings.Join(gws, ", "))
			}
			uiLead("users", num(db.int(`SELECT COUNT(*) FROM users`)))
			db.Close()
		}
		if fi, err := os.Stat(dbFile); err == nil {
			uiLead("database", fmt.Sprintf("%.1f MB", float64(fi.Size())/(1<<20)))
		}
		uiLead("last backup", lastBackupText())
		uiHr()
		uiItem(1, "Set bot token & admins", "")
		uiItem(2, "Store name & language", "")
		uiItem(3, "Restart bot", "")
		uiItem(4, "Status & log", "")
		uiItem(5, "Bot doctor", "")
		uiItem(6, "Backup now", "")
		uiItem(7, "Update bot", "")
		uiItem(8, "Uninstall bot", "")
		c := uiAsk("1-8")
		fmt.Println()
		switch c {
		case "1":
			if cfg == nil {
				cfg = &Config{}
			}
			if setToken(cfg, false) == nil && setAdmins(cfg, false) == nil {
				if err := saveConfig(cfg); err != nil {
					uiErr(err.Error())
				} else {
					restartQuiet()
				}
			}
			uiAnyKey()
		case "2":
			if db, err := openDB(dbFile); err == nil {
				setStore(newSettings(db))
				db.Close()
				restartQuiet()
			}
			uiAnyKey()
		case "3":
			restartQuiet()
			uiAnyKey()
		case "4":
			_ = run("journalctl", "-u", "tomato-store", "-n", "25", "--no-pager", "-o", "short")
			uiAnyKey()
		case "5":
			doctorCLI()
			uiAnyKey()
		case "6":
			cfg2, db := mustOpen()
			path, err := newBot(cfg2, db).backup()
			db.Close()
			if err != nil {
				uiErr("Backup failed: " + err.Error())
			} else {
				uiOk("Backup saved")
				uiNote(path)
			}
			uiAnyKey()
		case "7":
			repo := env("TOMATO_STORE_REPO", "https://github.com/dudul19/tomato-store-bot/releases/latest/download")
			_ = run("bash", "-c", "curl -fsSL "+repo+"/install.sh | bash -s -- --update")
			uiAnyKey()
		case "8":
			uiWarn("The bot service and binary are removed. Config and data are kept.")
			if uiInput("type yes to uninstall") == "yes" {
				_ = exec.Command("systemctl", "disable", "--now", "tomato-store").Run()
				_ = os.Remove("/etc/systemd/system/tomato-store.service")
				_ = os.Remove("/usr/bin/menu-store")
				_ = exec.Command("systemctl", "daemon-reload").Run()
				_ = os.Remove("/usr/local/bin/tomato-store")
				uiOk("Uninstalled.")
				uiNote("Kept: " + etcDir + " and " + dataDir)
				return
			}
		case "x", "X", "0", "q":
			clearScreen()
			return
		}
	}
}

func restartQuiet() {
	if err := exec.Command("systemctl", "restart", "tomato-store").Run(); err != nil {
		uiErr("Restart failed: " + err.Error())
		return
	}
	time.Sleep(2 * time.Second)
	if serviceActive() {
		uiOk("Bot restarted.")
	} else {
		uiErr("The bot did not start. Check Status & log.")
	}
}

func lastBackupText() string {
	ents, _ := os.ReadDir(backupDir)
	var last time.Time
	for _, e := range ents {
		if info, err := e.Info(); err == nil && filepath.Ext(e.Name()) == ".db" && info.ModTime().After(last) {
			last = info.ModTime()
		}
	}
	if last.IsZero() {
		return "-"
	}
	return last.Format("2006-01-02 15:04")
}

func run(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// ---------------------------------------------------------------- doctor

func doctorCLI() int {
	cfg, err := loadConfig()
	if err != nil {
		uiErr(err.Error())
		return 1
	}
	db, err := openDB(dbFile)
	if err != nil {
		uiErr("database: " + err.Error())
		return 1
	}
	defer db.Close()
	b := newBot(cfg, db)
	uiHead("bot doctor", "v"+version)
	failed := 0
	for _, c := range b.doctorChecks() {
		if !c.ok {
			failed++
			uiLead(c.name, uiR+"× failed")
			if c.detail != "" {
				uiNote(c.detail)
			}
			continue
		}
		v := uiOK + "✓"
		if c.detail != "" && c.detail != "ok" {
			v = uiD + trimRunes(c.detail, 22) + " " + v
		}
		uiLead(c.name, v)
	}
	uiHr()
	if failed > 0 {
		uiWarn(fmt.Sprintf("%d check(s) failed.", failed))
		return 1
	}
	uiOk("All checks passed.")
	return 0
}

type check struct {
	name, detail string
	ok           bool
}

func (b *Bot) doctorChecks() []check {
	var out []check
	me, err := b.tg.GetMe()
	if err != nil {
		out = append(out, check{"telegram", err.Error(), false})
	} else {
		out = append(out, check{"telegram", "@" + me.Username, true})
	}
	var ic string
	_ = b.db.QueryRow(`PRAGMA quick_check`).Scan(&ic)
	out = append(out, check{"database", ic, ic == "ok"})
	srv, _ := b.db.servers(false)
	for _, s := range srv {
		if !s.Enabled {
			continue
		}
		lat, err := b.api(s).Ping()
		name := strings.ToLower(trimRunes(s.Name, 18))
		if err != nil {
			out = append(out, check{name, err.Error(), false})
		} else {
			out = append(out, check{name, fmt.Sprintf("%d ms", lat.Milliseconds()), true})
		}
	}
	if len(srv) == 0 {
		out = append(out, check{"servers", "none added", false})
	}
	act := b.pay.Active()
	if len(act) == 0 {
		out = append(out, check{"payment", "no gateway on", false})
	}
	for _, g := range act {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err := g.Test(ctx)
		cancel()
		if err != nil {
			out = append(out, check{g.ID(), err.Error(), false})
		} else {
			out = append(out, check{g.ID(), "", true})
		}
	}
	pend := b.db.int(`SELECT COUNT(*) FROM orders WHERE status='pending'`)
	out = append(out, check{"pending orders", strconv.FormatInt(pend, 10), pend == 0})
	return out
}

func (b *Bot) doctorReport() string {
	c := newCard("BOT DOCTOR")
	for _, ch := range b.doctorChecks() {
		v := "ok"
		if !ch.ok {
			v = "FAIL"
		}
		if ch.detail != "" {
			v += " · " + trimRunes(ch.detail, 80)
		}
		c.kv(ch.name, v)
	}
	return c.String()
}
