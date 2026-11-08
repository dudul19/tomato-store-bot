package main

// Bot texts in Indonesian and English. The store language is chosen in
// /admin > Settings > Language. Any key can be overridden:
//   1. from the bot (/admin > Texts) — stored in the texts table;
//   2. from /etc/tomato-store/texts.json ({"key": "text"}).
// Placeholders: {store} (store name) and the {name} pairs given by the code.
// Format: Telegram HTML (<b> <i> <u> <s> <code> <pre> <a href> <blockquote>).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Texts struct {
	db  *DB
	set *Settings
	mu  sync.Mutex
	db0 map[string]string
	fj  map[string]string
	fjT time.Time
}

func newTexts(db *DB, s *Settings) *Texts {
	t := &Texts{db: db, set: s}
	t.load()
	return t
}

func (t *Texts) load() {
	m := map[string]string{}
	rows, err := t.db.Query(`SELECT key, value FROM texts`)
	if err == nil {
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				m[k] = v
			}
		}
		rows.Close()
	}
	t.mu.Lock()
	t.db0 = m
	t.mu.Unlock()
}

// fileTexts re-reads texts.json when it changes.
func (t *Texts) fileTexts() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, err := os.Stat(textsFile)
	if err != nil {
		t.fj = nil
		return nil
	}
	if st.ModTime().Equal(t.fjT) {
		return t.fj
	}
	t.fjT = st.ModTime()
	b, err := os.ReadFile(textsFile)
	m := map[string]string{}
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	t.fj = m
	return m
}

func (t *Texts) get(key string, kv ...any) string {
	t.mu.Lock()
	v, ok := t.db0[key]
	t.mu.Unlock()
	if !ok {
		v, ok = t.fileTexts()[key]
	}
	if !ok {
		d, found := defaultTexts[key]
		if !found {
			return key
		}
		v = d[0]
		if t.set.Str("lang") == "en" && d[1] != "" {
			v = d[1]
		}
	}
	r := []string{"{store}", t.set.Str("store_name")}
	for i := 0; i+1 < len(kv); i += 2 {
		r = append(r, "{"+fmt.Sprint(kv[i])+"}", fmt.Sprint(kv[i+1]))
	}
	return strings.NewReplacer(r...).Replace(v)
}

func (t *Texts) custom(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.db0[key]
	return ok
}

var reTag = regexp.MustCompile(`</?([a-zA-Z0-9-]+)[^>]*>`)

var allowedTags = map[string]bool{"b": true, "strong": true, "i": true, "em": true, "u": true, "ins": true, "s": true,
	"strike": true, "del": true, "code": true, "pre": true, "a": true, "blockquote": true, "tg-spoiler": true, "span": true}

func (t *Texts) setCustom(key, v string) error {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 3500 {
		return errors.New("1-3500 characters")
	}
	for _, m := range reTag.FindAllStringSubmatch(v, -1) {
		if !allowedTags[strings.ToLower(m[1])] {
			return errors.New("tag <" + m[1] + "> is not allowed")
		}
	}
	if _, err := t.db.Exec(`INSERT INTO texts(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, v); err != nil {
		return err
	}
	t.load()
	return nil
}

func (t *Texts) reset(key string) {
	_, _ = t.db.Exec(`DELETE FROM texts WHERE key=?`, key)
	t.load()
}

const hl = "\n" + cardLine + "\n"

var defaultTexts = map[string][2]string{
	// ---- editable long texts (HTML)
	"welcome": {"Pilih menu di bawah.", "Choose a menu below."},
	"howto_buy": {"<b>CARA BELI</b>" + hl +
		"1. Topup saldo lewat menu Topup Saldo.\n2. Buka Beli Akun, pilih protokol, server dan durasi.\n3. Kirim username, lalu tekan Bayar.\n4. Config langsung dikirim. Lihat lagi kapan saja di Akun Saya.",
		"<b>HOW TO BUY</b>" + hl +
			"1. Top up your balance from Top Up.\n2. Open Buy Account, choose protocol, server and duration.\n3. Send a username, then press Pay.\n4. The config is sent right away. Open it again any time from My Accounts."},
	"howto_topup": {"<b>CARA TOPUP</b>" + hl +
		"1. Buka Topup Saldo dan pilih nominal.\n2. Pilih metode bayar bila ada lebih dari satu.\n3. Scan QRIS dengan e-wallet / m-banking, atau transfer ke nomor Virtual Account.\n4. Saldo masuk otomatis setelah pembayaran berhasil.",
		"<b>HOW TO TOP UP</b>" + hl +
			"1. Open Top Up and choose an amount.\n2. Choose a payment method when there is more than one.\n3. Scan the QRIS with any e-wallet / mobile banking app, or transfer to the Virtual Account number.\n4. The balance is added automatically once paid."},
	"tos": {"<b>SYARAT &amp; KETENTUAN</b>" + hl +
		"1. Dilarang untuk aktivitas ilegal, spam, DDoS dan torrent.\n2. Akun dipakai sesuai limit IP yang tertera.\n3. Saldo tidak dapat diuangkan kembali.\n4. Pelanggaran dapat membuat akun dikunci tanpa refund.",
		"<b>TERMS OF SERVICE</b>" + hl +
			"1. No illegal activity, spam, DDoS or torrents.\n2. Use each account within its IP limit.\n3. Balance cannot be withdrawn.\n4. Violations may lock the account without refund."},
	"maintenance_msg": {"<b>MAINTENANCE</b>" + hl + "Toko sedang dalam perawatan. Silakan coba lagi nanti.",
		"<b>MAINTENANCE</b>" + hl + "The store is under maintenance. Please try again later."},

	// ---- general
	"rate_limited":    {"Terlalu banyak aksi. Coba lagi dalam 5 menit.", "Too many actions. Try again in 5 minutes."},
	"banned":          {"Akun Telegram kamu diblokir dari toko ini.", "Your Telegram account is banned from this store."},
	"busy":            {"Masih diproses, tunggu sebentar.", "Still processing, please wait."},
	"processing":      {"Memproses...", "Processing..."},
	"deleted":         {"Dihapus.", "Deleted."},
	"join_title":      {"WAJIB JOIN", "JOIN REQUIRED"},
	"join_body":       {"Join channel di atas dulu, lalu tekan Sudah Join.", "Join the channel above, then press Joined."},
	"choose_menu":     {"Pilih menu di bawah.", "Choose a menu below."},
	"choose_proto":    {"Pilih protokol.", "Choose a protocol."},
	"choose_server":   {"Pilih server.", "Choose a server."},
	"choose_duration": {"Pilih durasi.", "Choose a duration."},
	"choose_topic":    {"Pilih topik.", "Choose a topic."},
	"servers_online":  {"{n} ({on} online)", "{n} ({on} online)"},
	"days_n":          {"{n} hari", "{n} days"},
	"hours_n":         {"{n} jam", "{n} hours"},
	"minutes_n":       {"{n} menit", "{n} minutes"},
	"users_n":         {"{n} user", "{n} users"},
	"per_week":        {"/ minggu", "/ week"},
	"per_day":         {"hari", "day"},
	"left_word":       {"sisa", "left"},
	"today_word":      {"hari ini", "today"},
	"second_word":     {"detik", "second"},
	"expired_word":    {"expired", "expired"},
	"none_word":       {"belum ada", "none"},
	"not_set":         {"belum diisi", "not set"},
	"no_end":          {"tanpa batas", "no end"},
	"unlimited_word":  {"tanpa batas", "unlimited"},
	"no_data":         {"Belum ada data.", "No data yet."},
	"this_month":      {"Bulan ini.", "This month."},
	"unreachable":     {"tidak terjangkau", "unreachable"},
	"slot_full":       {"penuh", "full"},
	"sell_yes":        {"jual", "on sale"},
	"sell_no":         {"tidak", "off"},
	"st_on":           {"aktif", "on"},
	"st_off":          {"nonaktif", "off"},
	"st_active":       {"aktif", "active"},
	"st_locked":       {"terkunci", "locked"},
	"st_missing":      {"tidak ada di server", "missing on server"},
	"st_disabled":     {"nonaktif", "disabled"},
	"st_incomplete":   {"aktif, data belum lengkap", "on, incomplete"},
	"offline_since":   {"offline sejak {t}", "offline since {t}"},
	"blocked_bot":     {"blokir bot", "blocked the bot"},
	"to_buyer":        {"ke pembeli", "paid by buyer"},
	"to_store":        {"ditanggung toko", "paid by store"},
	"polling_5s":      {"polling 5 detik", "polling every 5 s"},
	"fee_pakasir":     {"sesuai Pakasir, tampil di tagihan", "set by Pakasir, shown on the bill"},

	// ---- labels
	"lbl_name":            {"Nama", "Name"},
	"lbl_balance":         {"Saldo", "Balance"},
	"lbl_active_accounts": {"Akun aktif", "Active accounts"},
	"lbl_accounts":        {"Akun", "Accounts"},
	"lbl_price":           {"Harga", "Price"},
	"lbl_quota":           {"Kuota", "Quota"},
	"lbl_quota_trial":     {"Jatah", "Quota"},
	"lbl_duration":        {"Durasi", "Duration"},
	"lbl_proto":           {"Protokol", "Protocol"},
	"lbl_username":        {"Username", "Username"},
	"lbl_left":            {"Sisa", "Remaining"},
	"lbl_amount":          {"Nominal", "Amount"},
	"lbl_fee":             {"Biaya", "Fee"},
	"lbl_valid":           {"Berlaku", "Valid for"},
	"lbl_va":              {"No. VA", "VA number"},
	"lbl_min":             {"Minimal", "Minimum"},
	"lbl_max":             {"Maksimal", "Maximum"},
	"lbl_method":          {"Metode", "Method"},
	"lbl_time":            {"Waktu", "Time"},
	"lbl_product":         {"Produk", "Product"},
	"lbl_channel":         {"Channel", "Channel"},
	"lbl_discount":        {"Diskon", "Discount"},
	"lbl_upgrade_price":   {"Biaya upgrade", "Upgrade price"},
	"lbl_month_spend":     {"Belanja bulan ini", "Spent this month"},
	"lbl_month_orders":    {"Order bulan ini", "Orders this month"},
	"lbl_store":           {"Toko", "Store"},
	"lbl_version":         {"Versi", "Version"},
	"lbl_gateways":        {"Gateway aktif", "Active gateways"},
	"lbl_balance_float":   {"Saldo beredar", "Balance in wallets"},
	"lbl_topup_today":     {"Topup hari ini", "Top ups today"},
	"lbl_topup_month":     {"Topup bulan ini", "Top ups this month"},
	"lbl_sales_today":     {"Penjualan hari ini", "Sales today"},
	"lbl_sales_month":     {"Penjualan bulan ini", "Sales this month"},
	"lbl_sales":           {"Penjualan", "Sales"},
	"lbl_count":           {"Jumlah", "Count"},
	"lbl_now":             {"Sekarang", "Current"},
	"lbl_max_accounts":    {"Maks akun", "Max accounts"},
	"lbl_fee_buyer":       {"Biaya ke pembeli", "Fee paid by buyer"},
	"lbl_status_check":    {"Cek status", "Status check"},
	"lbl_other_va":        {"VA lain", "Other VA"},
	"lbl_total":           {"Total", "Total"},
	"lbl_total_topup":     {"Total topup", "Total top up"},
	"lbl_total_spent":     {"Total belanja", "Total spent"},
	"lbl_joined":          {"Gabung", "Joined"},
	"lbl_note":            {"Catatan", "Note"},
	"lbl_target":          {"Target", "Target"},
	"lbl_speed":           {"Kecepatan", "Speed"},
	"lbl_sent":            {"Terkirim", "Sent"},
	"lbl_failed":          {"Gagal", "Failed"},
	"lbl_blocked":         {"Blokir bot", "Blocked the bot"},
	"lbl_eta":             {"Sisa waktu", "Time left"},
	"lbl_last_backup":     {"Backup terakhir", "Last backup"},
	"lbl_pending_orders":  {"Order pending", "Pending orders"},
	"lbl_errors_24h":      {"Error 24 jam", "Errors 24h"},
	"lbl_down_for":        {"Offline selama", "Down for"},
	"lbl_since":           {"Sejak", "Since"},

	// ---- buttons
	"btn_buy":             {"Beli Akun", "Buy Account"},
	"btn_trial":           {"Trial", "Trial"},
	"btn_my_accounts":     {"Akun Saya", "My Accounts"},
	"btn_topup":           {"Topup Saldo", "Top Up"},
	"btn_history":         {"Riwayat", "History"},
	"btn_reseller":        {"Reseller", "Reseller"},
	"btn_help":            {"Bantuan", "Help"},
	"btn_group":           {"Grup", "Group"},
	"btn_admin":           {"Admin Panel", "Admin Panel"},
	"btn_back":            {"Kembali", "Back"},
	"btn_menu":            {"Menu", "Menu"},
	"btn_cancel":          {"Batal", "Cancel"},
	"btn_pay":             {"Bayar", "Pay"},
	"btn_join":            {"Join Channel", "Join Channel"},
	"btn_joined":          {"Sudah Join", "Joined"},
	"btn_config":          {"Lihat Config", "View Config"},
	"btn_renew":           {"Perpanjang", "Renew"},
	"btn_delete":          {"Hapus", "Delete"},
	"btn_delete_yes":      {"Ya, hapus", "Yes, delete"},
	"btn_prev":            {"Sebelumnya", "Previous"},
	"btn_next":            {"Berikutnya", "Next"},
	"btn_upgrade":         {"Upgrade Reseller", "Upgrade to Reseller"},
	"btn_howto_buy":       {"Cara Beli", "How to Buy"},
	"btn_howto_topup":     {"Cara Topup", "How to Top Up"},
	"btn_tos":             {"Syarat & Ketentuan", "Terms of Service"},
	"btn_chat_cs":         {"Chat CS", "Contact Support"},
	"btn_other_amount":    {"Nominal lain", "Other amount"},
	"btn_check":           {"Cek Status", "Check Status"},
	"btn_pay_page":        {"Halaman Bayar", "Payment Page"},
	"btn_dashboard":       {"Dashboard", "Dashboard"},
	"btn_payment":         {"Payment", "Payment"},
	"btn_settings":        {"Pengaturan", "Settings"},
	"btn_texts":           {"Teks Bot", "Bot Texts"},
	"btn_close":           {"Tutup", "Close"},
	"btn_stats6":          {"Statistik 6 Bulan", "6-Month Stats"},
	"btn_per_gateway":     {"Per Gateway", "Per Gateway"},
	"btn_add_server":      {"Tambah Server", "Add Server"},
	"btn_ping":            {"Tes Ping", "Ping Test"},
	"btn_set_price":       {"Atur Harga", "Set Prices"},
	"btn_set_proto":       {"Atur Protokol", "Protocols"},
	"btn_quota_ip":        {"Kuota & IP", "Quota & IP"},
	"btn_max_accounts":    {"Maks Akun", "Max Accounts"},
	"btn_rename":          {"Ganti Nama", "Rename"},
	"btn_enable":          {"Aktifkan", "Enable"},
	"btn_disable":         {"Nonaktifkan", "Disable"},
	"btn_open":            {"Buka", "Open"},
	"btn_test":            {"Tes Koneksi", "Test Connection"},
	"btn_set_key":         {"Isi API Key", "Set API Key"},
	"btn_set_server_key":  {"Ganti Server Key", "Set Server Key"},
	"btn_set_project":     {"Ganti Project", "Set Project"},
	"btn_methods":         {"Atur Metode", "Methods"},
	"btn_mode_sandbox":    {"Mode Sandbox", "Sandbox Mode"},
	"btn_mode_prod":       {"Mode Production", "Production Mode"},
	"btn_fee":             {"Biaya", "Fee"},
	"btn_fee_buyer":       {"Biaya ke Pembeli", "Fee to Buyer"},
	"btn_topup_limits":    {"Batas Topup", "Top Up Limits"},
	"btn_bal_add":         {"Tambah Saldo", "Add Balance"},
	"btn_bal_sub":         {"Kurangi Saldo", "Deduct Balance"},
	"btn_make_reseller":   {"Jadikan Reseller", "Make Reseller"},
	"btn_revoke_reseller": {"Cabut Reseller", "Revoke Reseller"},
	"btn_user_accounts":   {"Akun User", "User Accounts"},
	"btn_all_users":       {"Semua User", "All Users"},
	"btn_resellers":       {"Reseller Saja", "Resellers Only"},
	"btn_send":            {"Kirim", "Send"},
	"btn_stop":            {"Hentikan", "Stop"},
	"btn_language":        {"Bahasa", "Language"},
	"btn_backup_now":      {"Backup Sekarang", "Backup Now"},
	"btn_expiring":        {"Akun Hampir Expired", "Expiring Accounts"},

	// ---- buy
	"buy_title":            {"BELI AKUN", "BUY ACCOUNT"},
	"buy_proto_title":      {"BELI {proto}", "BUY {proto}"},
	"price_per_30":         {"Harga per 30 hari.", "Price per 30 days."},
	"no_servers":           {"Belum ada server yang dijual.", "No server is on sale yet."},
	"ask_username":         {"Kirim username (3-20 huruf/angka).", "Send a username (3-20 letters/digits)."},
	"ask_password_hint":    {"Password: kirim format user:pass\natau username saja (password acak).", "Password: send user:pass\nor only the username (random password)."},
	"confirm_title":        {"KONFIRMASI", "CONFIRMATION"},
	"failed_title":         {"GAGAL", "FAILED"},
	"pending_title":        {"SEDANG DIPROSES", "PROCESSING"},
	"pending_body":         {"Server belum menjawab. Pesanan dicek otomatis: akun dikirim ke sini bila jadi, saldo dikembalikan bila gagal.", "The server did not answer yet. The order is checked automatically: the account is sent here when ready, or the balance is refunded."},
	"err_balance_title":    {"SALDO KURANG", "INSUFFICIENT BALANCE"},
	"err_balance_body":     {"Topup saldo dulu untuk melanjutkan.", "Top up your balance to continue."},
	"err_balance":          {"Saldo kurang. Butuh {need}, saldo {balance}.", "Insufficient balance. Need {need}, balance {balance}."},
	"err_username":         {"Username harus 3-20 karakter: huruf, angka, _ atau -.", "Username must be 3-20 characters: letters, digits, _ or -."},
	"err_password":         {"Password harus 3-32 karakter: huruf, angka, _ atau -.", "Password must be 3-32 characters: letters, digits, _ or -."},
	"err_username_taken":   {"Username sudah dipakai di server ini. Kirim username lain.", "This username is taken on this server. Send another one."},
	"err_server_closed":    {"Server / protokol ini sedang tidak dijual.", "This server / protocol is not on sale."},
	"err_server_full":      {"Server penuh. Pilih server lain.", "The server is full. Choose another one."},
	"err_server_offline":   {"Server sedang offline. Pilih server lain.", "The server is offline. Choose another one."},
	"err_server_reject":    {"Server menolak: {msg}. Saldo tidak dipotong.", "The server refused: {msg}. Your balance was not charged."},
	"err_account_gone":     {"Akun tidak ditemukan.", "Account not found."},
	"err_expired_action":   {"Sesi habis, ulangi dari menu.", "Session expired, start again from the menu."},
	"err_generic":          {"Terjadi kesalahan. Coba lagi.", "Something went wrong. Please try again."},
	"order_recovered":      {"PESANAN SELESAI", "ORDER COMPLETED"},
	"order_recovered_body": {"Pesanan yang tertunda sudah berhasil.", "Your delayed order is complete."},
	"order_refunded":       {"PESANAN DIBATALKAN", "ORDER CANCELLED"},
	"order_refunded_body":  {"Server tidak memproses pesanan. Saldo sudah dikembalikan.", "The server did not process the order. Your balance was refunded."},

	// ---- accounts
	"acc_title":       {"AKUN SAYA", "MY ACCOUNTS"},
	"acc_none":        {"Belum ada akun aktif.", "No active accounts."},
	"acc_count":       {"{n} akun aktif.", "{n} active accounts."},
	"renew_title":     {"PERPANJANG", "RENEW"},
	"renew_done":      {"PERPANJANG BERHASIL", "RENEWED"},
	"delete_title":    {"HAPUS AKUN", "DELETE ACCOUNT"},
	"delete_warn":     {"Akun dihapus dari server dan tidak bisa dikembalikan. Saldo tidak dikembalikan.", "The account is removed from the server and cannot be restored. No refund."},
	"err_delete_off":  {"Hapus akun dimatikan oleh admin.", "Deleting accounts is disabled by the admin."},
	"err_trial_renew": {"Akun trial tidak bisa diperpanjang.", "Trial accounts cannot be renewed."},
	"remind_title":    {"AKUN HAMPIR EXPIRED", "ACCOUNT EXPIRING SOON"},
	"expired_title":   {"AKUN EXPIRED", "ACCOUNT EXPIRED"},
	"expired_body":    {"Masa aktif akun sudah habis. Beli akun baru untuk lanjut.", "The account has expired. Buy a new one to continue."},

	// ---- trial
	"trial_title":    {"TRIAL", "TRIAL"},
	"err_trial_off":  {"Trial sedang ditutup.", "Trial is closed."},
	"err_trial_used": {"Jatah trial sudah habis.", "Your trial quota is used up."},

	// ---- top up
	"topup_title":           {"TOPUP SALDO", "TOP UP"},
	"topup_choose":          {"Pilih nominal atau kirim angka.", "Choose an amount or type one."},
	"topup_type":            {"Kirim nominal, contoh: 35000", "Send an amount, e.g. 35000"},
	"method_title":          {"METODE BAYAR", "PAYMENT METHOD"},
	"creating_invoice":      {"Membuat tagihan...", "Creating the bill..."},
	"inv_qris_title":        {"TAGIHAN QRIS", "QRIS BILL"},
	"inv_va_title":          {"TAGIHAN VA", "VA BILL"},
	"inv_qris_body":         {"Scan pakai e-wallet / m-banking.\nSaldo masuk otomatis.", "Scan with any e-wallet / mobile banking.\nThe balance is added automatically."},
	"inv_va_body":           {"Transfer tepat sesuai total.\nSaldo masuk otomatis.", "Transfer exactly the total.\nThe balance is added automatically."},
	"inv_paid":              {"TOPUP BERHASIL", "TOP UP SUCCESSFUL"},
	"inv_expired":           {"TAGIHAN KEDALUWARSA", "BILL EXPIRED"},
	"inv_waiting":           {"Belum dibayar.", "Not paid yet."},
	"inv_cancelled":         {"Tagihan dibatalkan.", "Bill cancelled."},
	"inv_already_paid":      {"Tagihan sudah dibayar.", "This bill is already paid."},
	"err_amount":            {"Nominal tidak valid.", "Invalid amount."},
	"err_topup_range":       {"Nominal topup {min} - {max}.", "Top up amount {min} - {max}."},
	"err_gateway_off":       {"Pembayaran otomatis belum aktif. Hubungi CS.", "Automatic payment is not available. Contact support."},
	"err_gateway":           {"Gagal membuat tagihan: {msg}", "Could not create the bill: {msg}"},
	"err_too_many_invoices": {"Masih ada 3 tagihan aktif. Bayar atau batalkan dulu.", "You have 3 open bills. Pay or cancel them first."},

	// ---- history / reseller / help
	"hist_title":       {"RIWAYAT", "HISTORY"},
	"hist_sub":         {"10 transaksi terakhir.", "Last 10 transactions."},
	"hist_none":        {"Belum ada transaksi.", "No transactions yet."},
	"tx_topup":         {"topup", "top up"},
	"tx_cashback":      {"cashback", "cashback"},
	"tx_purchase":      {"beli akun", "purchase"},
	"tx_renew":         {"perpanjang", "renewal"},
	"tx_refund":        {"refund", "refund"},
	"tx_admin_add":     {"saldo dari admin", "added by admin"},
	"tx_admin_sub":     {"dikurangi admin", "deducted by admin"},
	"tx_reseller":      {"upgrade reseller", "reseller upgrade"},
	"res_title":        {"RESELLER", "RESELLER"},
	"res_body":         {"Biaya upgrade dipotong dari saldo.", "The upgrade price is taken from your balance."},
	"res_upgrade":      {"Upgrade Reseller", "Reseller upgrade"},
	"res_done":         {"KAMU SEKARANG RESELLER", "YOU ARE NOW A RESELLER"},
	"discount_all":     {"{n}% semua protokol", "{n}% on every protocol"},
	"err_reseller_off": {"Upgrade reseller sedang ditutup.", "Reseller upgrades are closed."},
	"help_title":       {"BANTUAN", "HELP"},

	// ---- admin notifications
	"adm_sale_title":     {"PENJUALAN", "SALE"},
	"adm_renew_title":    {"PERPANJANGAN", "RENEWAL"},
	"adm_topup_title":    {"TOPUP MASUK", "TOP UP RECEIVED"},
	"adm_reseller_title": {"RESELLER BARU", "NEW RESELLER"},
	"adm_pending_title":  {"ORDER TERTUNDA", "ORDER STILL PENDING"},
	"adm_pending_body":   {"Server belum bisa dihubungi selama 24 jam. Cek server lalu jalankan Tools > Bot Doctor.", "The server has been unreachable for 24 hours. Check it, then run Tools > Bot Doctor."},
	"srv_down_title":     {"SERVER OFFLINE", "SERVER OFFLINE"},
	"srv_down_body":      {"Pembelian di server ini ditutup sementara.", "Purchases on this server are paused."},
	"srv_up_title":       {"SERVER ONLINE", "SERVER ONLINE"},

	// ---- admin panel
	"adm_title":               {"ADMIN PANEL", "ADMIN PANEL"},
	"dash_title":              {"DASHBOARD", "DASHBOARD"},
	"stats6_title":            {"STATISTIK 6 BULAN", "6-MONTH STATISTICS"},
	"pgw_title":               {"PER GATEWAY", "PER GATEWAY"},
	"no_servers_admin":        {"Belum ada server. Tekan Tambah Server.", "No servers yet. Press Add Server."},
	"srv_local":               {"VPS ini (lokal)", "this VPS (local)"},
	"srv_add_title":           {"TAMBAH SERVER", "ADD SERVER"},
	"srv_add_name":            {"Kirim nama server (contoh: SG Digital Ocean).", "Send the server name (e.g. SG Digital Ocean)."},
	"srv_add_url":             {"Kirim domain server Tomato (contoh: sg1.domain.com).", "Send the Tomato server domain (e.g. sg1.domain.com)."},
	"srv_add_key":             {"Kirim API key Tomato server itu.\nAmbil di menu Tomato > System Settings > Tomato API.", "Send the Tomato API key of that server.\nFind it in the Tomato menu > System Settings > Tomato API."},
	"srv_test_fail":           {"Koneksi gagal: {msg}\nKirim API key lagi atau tekan Batal.", "Connection failed: {msg}\nSend the API key again or press Cancel."},
	"srv_added":               {"SERVER DITAMBAHKAN", "SERVER ADDED"},
	"srv_added_body":          {"Harga awal Rp10.000 / 30 hari tiap protokol. Atur di menu server.", "Default price Rp10,000 / 30 days per protocol. Change it in the server menu."},
	"srv_price_title":         {"ATUR HARGA", "PRICES"},
	"srv_price_body":          {"Harga per 30 hari. Durasi lain dihitung proporsional.", "Price per 30 days. Other durations are prorated."},
	"srv_price_ask":           {"Kirim harga baru per 30 hari (0 = tidak dijual).", "Send the new price per 30 days (0 = not for sale)."},
	"srv_proto_title":         {"PROTOKOL", "PROTOCOLS"},
	"srv_proto_body":          {"Tekan untuk jual / tidak jual.", "Press to toggle on sale / off."},
	"srv_qi_title":            {"KUOTA & IP", "QUOTA & IP"},
	"srv_qi_ask":              {"Kirim: kuota_GB limit_IP (contoh: 0 2). 0 = unlimited.", "Send: quota_GB IP_limit (e.g. 0 2). 0 = unlimited."},
	"srv_max_title":           {"MAKS AKUN", "MAX ACCOUNTS"},
	"srv_max_ask":             {"Kirim jumlah maksimal akun aktif (0 = tanpa batas).", "Send the maximum active accounts (0 = unlimited)."},
	"srv_name_title":          {"GANTI NAMA", "RENAME"},
	"srv_key_title":           {"API KEY", "API KEY"},
	"srv_del_title":           {"HAPUS SERVER", "DELETE SERVER"},
	"srv_del_warn":            {"Akun di VPS TIDAK dihapus, hanya data di bot. Akun user di server ini jadi tidak bisa diperpanjang.", "Accounts on the VPS are NOT deleted, only the bot records. Users can no longer renew accounts on this server."},
	"pay_title":               {"PAYMENT GATEWAY", "PAYMENT GATEWAY"},
	"pay_body":                {"Pembeli memilih jika lebih dari satu aktif.", "Buyers choose when more than one is on."},
	"gw_incomplete":           {"Gateway aktif tapi data belum lengkap. Isi API key dulu.", "The gateway is on but incomplete. Set the API key first."},
	"gw_test_ok":              {"Koneksi berhasil.", "Connection OK."},
	"gw_test_fail":            {"Gagal: {msg}", "Failed: {msg}"},
	"gw_autogopay_help":       {"API key dari dashboard autogopay.site.", "API key from the autogopay.site dashboard."},
	"gw_midtrans_help":        {"Tidak perlu webhook / domain.\nServer Key dari dashboard Midtrans\n> Settings > Access Keys.", "No webhook / domain needed.\nServer Key from the Midtrans dashboard\n> Settings > Access Keys."},
	"gw_pakasir_help":         {"Project slug & API key dari\napp.pakasir.com > Proyek.", "Project slug & API key from\napp.pakasir.com > Projects."},
	"pks_methods_title":       {"METODE PAKASIR", "PAKASIR METHODS"},
	"pks_methods_body":        {"Hijau = dijual. Tekan untuk mengubah.", "Green = enabled. Press to toggle."},
	"user_search":             {"Kirim ID Telegram atau @username untuk mencari.", "Send a Telegram ID or @username to search."},
	"user_not_found":          {"User tidak ditemukan.", "User not found."},
	"bal_add_title":           {"TAMBAH SALDO", "ADD BALANCE"},
	"bal_sub_title":           {"KURANGI SALDO", "DEDUCT BALANCE"},
	"bal_ask":                 {"Kirim nominal dan catatan (contoh: 50000 bonus).", "Send the amount and a note (e.g. 50000 bonus)."},
	"bal_added_user":          {"SALDO MASUK", "BALANCE ADDED"},
	"err_balance_sub":         {"Gagal: saldo user tidak cukup.", "Failed: the user balance is too low."},
	"bc_body":                 {"Kirim teks atau foto + caption.\nPesan dikirim lewat antrean.", "Send text or a photo with caption.\nMessages are sent through a queue."},
	"bc_ask":                  {"Kirim pesan broadcast (teks atau foto + caption, format HTML).", "Send the broadcast message (text or photo with caption, HTML)."},
	"bc_preview":              {"PREVIEW", "PREVIEW"},
	"bc_confirm":              {"Pesan di atas akan dikirim. Lanjut?", "The message above will be sent. Continue?"},
	"bc_running":              {"BROADCAST BERJALAN", "BROADCAST RUNNING"},
	"bc_done":                 {"BROADCAST SELESAI", "BROADCAST FINISHED"},
	"bc_stopped":              {"BROADCAST DIHENTIKAN", "BROADCAST STOPPED"},
	"set_title":               {"PENGATURAN", "SETTINGS"},
	"grp_store":               {"Toko", "Store"},
	"grp_account":             {"Akun & Notifikasi", "Accounts & Alerts"},
	"cb_help":                 {"Cashback tetap > 0 dipakai bila diisi, selain itu persen.", "A fixed cashback > 0 is used when set, otherwise the percentage."},
	"ask_number":              {"Kirim angka ({min} - {max}).", "Send a number ({min} - {max})."},
	"ask_list":                {"Kirim angka dipisah koma (contoh: 7,15,30).", "Send numbers separated by commas (e.g. 7,15,30)."},
	"ask_text":                {"Kirim nilai baru. Kirim - untuk mengosongkan.", "Send the new value. Send - to clear it."},
	"ask_days_end":            {"Kirim jumlah hari sampai event berakhir (0 = tanpa batas).", "Send the number of days until the event ends (0 = no end)."},
	"err_setting":             {"Nilai tidak valid, kirim lagi.", "Invalid value, send it again."},
	"err_name":                {"Nama 2-30 karakter.", "Name must be 2-30 characters."},
	"err_url":                 {"Domain / URL tidak valid.", "Invalid domain / URL."},
	"txt_title":               {"TEKS BOT", "BOT TEXTS"},
	"txt_body":                {"Pilih teks yang ingin diubah. Format HTML Telegram.", "Choose a text to change. Telegram HTML format."},
	"txt_ask":                 {"Kirim teks baru (HTML: b, i, u, s, code, pre, a, blockquote).", "Send the new text (HTML: b, i, u, s, code, pre, a, blockquote)."},
	"txt_invalid":             {"Ditolak: {msg}", "Rejected: {msg}"},
	"txt_custom":              {"diubah", "custom"},
	"txt_default":             {"bawaan", "default"},
	"txt_reset_done":          {"Kembali ke bawaan.", "Reset to default."},
	"txtname_welcome":         {"Sambutan", "Welcome"},
	"txtname_howto_buy":       {"Cara Beli", "How to Buy"},
	"txtname_howto_topup":     {"Cara Topup", "How to Top Up"},
	"txtname_tos":             {"Syarat & Ketentuan", "Terms"},
	"txtname_maintenance_msg": {"Pesan Maintenance", "Maintenance Message"},
	"restore_ask":             {"Kirim file backup .db (hasil menu Backup).", "Send a .db backup file (from Backup)."},
	"restore_bad":             {"File bukan backup database bot.", "This file is not a bot database backup."},
	"restore_done":            {"Database lama dibackup. Bot restart dengan data baru dalam beberapa detik.", "The old database was backed up. The bot restarts with the new data in a few seconds."},
	"recon_n":                 {"{n} akun hilang ditandai terhapus", "{n} missing accounts marked deleted"},
	"exp_title":               {"HAMPIR EXPIRED (3 HARI)", "EXPIRING (3 DAYS)"},
}
