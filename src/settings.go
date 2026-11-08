package main

// Store settings (table settings). Every setting has a spec: type, default,
// limits and a label for the admin editor, so the editor is generic.

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

type setKind int

const (
	kInt setKind = iota
	kBool
	kStr
	kList   // comma separated integers
	kSecret // shown masked
	kFloat
)

type spec struct {
	key      string
	kind     setKind
	def      string
	min, max int64
	label    [2]string // id, en
}

var specs = []spec{
	// Store
	{"store_name", kStr, "Tomato Store", 1, 40, [2]string{"Nama toko", "Store name"}},
	{"cs_username", kStr, "", 0, 40, [2]string{"Username CS", "Support username"}},
	{"group_url", kStr, "", 0, 200, [2]string{"Link grup", "Group link"}},
	{"force_channel", kStr, "", 0, 64, [2]string{"Channel wajib join", "Required channel"}},
	{"maintenance", kBool, "0", 0, 1, [2]string{"Maintenance", "Maintenance"}},
	{"durations", kList, "7,15,30,60", 1, 365, [2]string{"Pilihan durasi (hari)", "Durations (days)"}},
	{"price_round", kInt, "100", 1, 10000, [2]string{"Pembulatan harga", "Price rounding"}},
	{"lang", kStr, "id", 2, 2, [2]string{"Bahasa", "Language"}},
	// Top up
	{"topup_min", kInt, "10000", 1000, 100000000, [2]string{"Topup minimal", "Minimum top up"}},
	{"topup_max", kInt, "5000000", 1000, 100000000, [2]string{"Topup maksimal", "Maximum top up"}},
	{"topup_buttons", kList, "10000,25000,50000,100000,200000", 1000, 100000000, [2]string{"Tombol nominal", "Amount buttons"}},
	{"invoice_minutes", kInt, "15", 5, 1440, [2]string{"Masa berlaku QRIS (menit)", "QRIS lifetime (minutes)"}},
	// Trial
	{"trial_enabled", kBool, "1", 0, 1, [2]string{"Trial aktif", "Trial enabled"}},
	{"trial_minutes", kInt, "60", 10, 1440, [2]string{"Durasi trial (menit)", "Trial length (minutes)"}},
	{"trial_member_weekly", kInt, "1", 0, 100, [2]string{"Jatah member / minggu", "Member quota / week"}},
	{"trial_reseller_daily", kInt, "3", 0, 100, [2]string{"Jatah reseller / hari", "Reseller quota / day"}},
	// Reseller
	{"reseller_enabled", kBool, "1", 0, 1, [2]string{"Upgrade reseller", "Reseller upgrade"}},
	{"reseller_price", kInt, "50000", 0, 100000000, [2]string{"Biaya upgrade", "Upgrade price"}},
	{"reseller_discount", kInt, "20", 0, 90, [2]string{"Diskon reseller (%)", "Reseller discount (%)"}},
	// Accounts
	{"remind_days", kList, "3,1", 0, 30, [2]string{"Pengingat (hari sebelum)", "Reminder (days before)"}},
	{"allow_delete", kBool, "1", 0, 1, [2]string{"User boleh hapus akun", "Users may delete accounts"}},
	{"username_suffix", kBool, "0", 0, 1, [2]string{"Tambah akhiran acak", "Random username suffix"}},
	// Backup & broadcast
	{"backup_hours", kInt, "24", 0, 168, [2]string{"Backup tiap (jam, 0=off)", "Backup every (hours, 0=off)"}},
	{"backup_keep_days", kInt, "7", 1, 90, [2]string{"Simpan backup (hari)", "Keep backups (days)"}},
	{"backup_send", kBool, "1", 0, 1, [2]string{"Kirim backup ke admin", "Send backup to admins"}},
	{"broadcast_rate", kInt, "25", 1, 30, [2]string{"Kecepatan broadcast (/detik)", "Broadcast speed (/second)"}},
	{"notify_admin", kBool, "1", 0, 1, [2]string{"Notifikasi penjualan ke admin", "Sales notifications to admins"}},
	// Cashback
	{"cashback_enabled", kBool, "0", 0, 1, [2]string{"Cashback aktif", "Cashback enabled"}},
	{"cashback_percent", kInt, "5", 0, 100, [2]string{"Cashback (%)", "Cashback (%)"}},
	{"cashback_fixed", kInt, "0", 0, 100000000, [2]string{"Cashback tetap (Rp)", "Fixed cashback (Rp)"}},
	{"cashback_min", kInt, "50000", 0, 100000000, [2]string{"Minimal topup", "Minimum top up"}},
	{"cashback_max", kInt, "0", 0, 100000000, [2]string{"Cashback maksimal (0=tanpa batas)", "Maximum cashback (0=no limit)"}},
	{"cashback_until", kInt, "0", 0, 1 << 40, [2]string{"Berakhir", "Ends"}},
	// Payment gateways
	{"fee_to_buyer", kBool, "1", 0, 1, [2]string{"Biaya gateway ke pembeli", "Gateway fee paid by buyer"}},
	{"gw_autogopay_on", kBool, "0", 0, 1, [2]string{"AutoGoPay aktif", "AutoGoPay enabled"}},
	{"gw_autogopay_key", kSecret, "", 0, 200, [2]string{"API Key AutoGoPay", "AutoGoPay API key"}},
	{"gw_autogopay_url", kStr, "https://v1-gateway.autogopay.site", 0, 200, [2]string{"URL API AutoGoPay", "AutoGoPay API URL"}},
	{"gw_autogopay_fee", kFloat, "0", 0, 10, [2]string{"Biaya AutoGoPay (%)", "AutoGoPay fee (%)"}},
	{"gw_midtrans_on", kBool, "0", 0, 1, [2]string{"Midtrans aktif", "Midtrans enabled"}},
	{"gw_midtrans_key", kSecret, "", 0, 200, [2]string{"Server Key Midtrans", "Midtrans Server Key"}},
	{"gw_midtrans_prod", kBool, "1", 0, 1, [2]string{"Mode production", "Production mode"}},
	{"gw_midtrans_fee", kFloat, "0.7", 0, 10, [2]string{"Biaya Midtrans (%)", "Midtrans fee (%)"}},
	{"gw_pakasir_on", kBool, "0", 0, 1, [2]string{"Pakasir aktif", "Pakasir enabled"}},
	{"gw_pakasir_project", kStr, "", 0, 100, [2]string{"Project slug Pakasir", "Pakasir project slug"}},
	{"gw_pakasir_key", kSecret, "", 0, 200, [2]string{"API Key Pakasir", "Pakasir API key"}},
	{"gw_pakasir_methods", kStr, "qris", 0, 300, [2]string{"Metode Pakasir", "Pakasir methods"}},
}

var specByKey = func() map[string]*spec {
	m := map[string]*spec{}
	for i := range specs {
		m[specs[i].key] = &specs[i]
	}
	return m
}()

type Settings struct {
	db *DB
	mu sync.RWMutex
	m  map[string]string
}

func newSettings(db *DB) *Settings {
	s := &Settings{db: db, m: map[string]string{}}
	s.reload()
	return s
}

func (s *Settings) reload() {
	m := map[string]string{}
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err == nil {
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				m[k] = v
			}
		}
		rows.Close()
	}
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
}

func (s *Settings) Str(key string) string {
	s.mu.RLock()
	v, ok := s.m[key]
	s.mu.RUnlock()
	if ok {
		return v
	}
	if sp := specByKey[key]; sp != nil {
		return sp.def
	}
	return ""
}

func (s *Settings) Int(key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s.Str(key)), 10, 64)
	return n
}

func (s *Settings) Float(key string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s.Str(key)), 64)
	return f
}

func (s *Settings) Bool(key string) bool { return s.Str(key) == "1" }

func (s *Settings) List(key string) []int64 {
	var out []int64
	for _, p := range strings.Split(s.Str(key), ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (s *Settings) Set(key, value string) error {
	if _, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
		return err
	}
	s.mu.Lock()
	s.m[key] = value
	s.mu.Unlock()
	return nil
}

func (s *Settings) Toggle(key string) bool {
	v := !s.Bool(key)
	_ = s.Set(key, boolStr(v))
	return v
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// parseSetting validates an admin input against the spec and returns the
// normalized value to store, or an error message key.
func parseSetting(sp *spec, in string) (string, bool) {
	in = strings.TrimSpace(in)
	switch sp.kind {
	case kInt:
		n, err := parseMoney(in)
		if err != nil || n < sp.min || n > sp.max {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	case kFloat:
		f, err := strconv.ParseFloat(strings.ReplaceAll(in, ",", "."), 64)
		if err != nil || f < float64(sp.min) || f > float64(sp.max) {
			return "", false
		}
		return strconv.FormatFloat(f, 'f', -1, 64), true
	case kList:
		var nums []int64
		seen := map[int64]bool{}
		for _, p := range strings.FieldsFunc(in, func(r rune) bool { return r == ',' || r == ' ' }) {
			n, err := parseMoney(p)
			if err != nil || n < sp.min || n > sp.max {
				return "", false
			}
			if !seen[n] {
				seen[n] = true
				nums = append(nums, n)
			}
		}
		if len(nums) == 0 && sp.min > 0 {
			return "", false
		}
		if len(nums) > 8 {
			return "", false
		}
		if sp.key == "remind_days" {
			sort.Slice(nums, func(i, j int) bool { return nums[i] > nums[j] })
		} else {
			sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
		}
		parts := make([]string, len(nums))
		for i, n := range nums {
			parts[i] = strconv.FormatInt(n, 10)
		}
		return strings.Join(parts, ","), true
	case kBool:
		switch strings.ToLower(in) {
		case "1", "on", "ya", "yes", "true":
			return "1", true
		case "0", "off", "tidak", "no", "false":
			return "0", true
		}
		return "", false
	default:
		if in == "-" {
			in = ""
		}
		if int64(len([]rune(in))) > sp.max || int64(len([]rune(in))) < sp.min {
			return "", false
		}
		if sp.key == "cs_username" || sp.key == "force_channel" {
			if in != "" && !strings.HasPrefix(in, "@") && !strings.HasPrefix(in, "-100") {
				in = "@" + in
			}
		}
		return in, true
	}
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-3:]
}
