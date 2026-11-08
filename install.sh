#!/bin/bash
# Tomato Store Bot — installer / updater (same look as the Tomato Autoscript installer)
#
#   Install : curl -fsSL https://github.com/dudul19/tomato-store-bot/releases/latest/download/install.sh | bash
#   Update  : curl -fsSL https://github.com/dudul19/tomato-store-bot/releases/latest/download/install.sh | bash -s -- --update
#             (or: menu-store -> Update bot)
#
# Works best on a VPS with Tomato Autoscript (that VPS is added as a server
# automatically). On any other Linux VPS, add Tomato servers from /admin.

set -uo pipefail

REPO="${TOMATO_STORE_REPO:-https://github.com/dudul19/tomato-store-bot/releases/latest/download}"
BIN=/usr/local/bin/tomato-store
ETC=/etc/tomato-store
DATA=/var/lib/tomato-store
UNIT=/etc/systemd/system/tomato-store.service
LOGFILE=/tmp/.tomato-store-install.log
MODE=install
[ "${1:-}" = "--update" ] && MODE=update
: >"$LOGFILE"

# ── Tomato "Struk" look: 40 columns, flush left, truecolor ──
UI_T="\e[38;2;255;122;92m"
UI_G="\e[38;2;143;209;158m"
UI_OK="\e[38;2;122;214;154m"
UI_D="\e[38;2;138;143;150m"
UI_L="\e[38;2;58;63;69m"
UI_R="\e[38;2;255;107;107m"
UI_Y="\e[38;2;232;193;112m"
UI_N="\e[0m"
UI_W=40

ui_line() { local l; printf -v l '%*s' "$UI_W" ''; echo -e "${UI_L}${l// /$1}${UI_N}"; }
ui_hr()  { ui_line ┄; }
ui_hr2() { ui_line ═; }
ui_width() {
    local LC_ALL=C.UTF-8
    if [[ "$1" == *[![:ascii:]]* ]]; then printf '%s' "$1" | wc -L; else echo ${#1}; fi
}
ui_len() { ui_width "$(printf '%b' "$1" | sed 's/\x1b\[[0-9;]*m//g')"; }
ui_row() {
    local a b
    a=$(ui_len "$1"); b=$(ui_len "$2")
    printf "%b%*s%b${UI_N}\n" "$1" $(( UI_W - a - b > 1 ? UI_W - a - b : 1 )) "" "$2"
}
ui_lead() {
    local LC_ALL=C.UTF-8 k="$1" v="$2" pv n d
    v="${v%"${v##*[! ]}"}"
    pv=$(printf '%b' "$v" | sed 's/\x1b\[[0-9;]*m//g')
    n=$(( UI_W - ${#k} - $(ui_width "$pv") - 2 ))
    if [ "$n" -lt 2 ]; then v=${pv:0:$(( UI_W - ${#k} - 5 ))}~; pv=$v; n=$(( UI_W - ${#k} - $(ui_width "$pv") - 2 )); fi
    printf -v d '%*s' "$n" ''
    printf "${UI_D}%s${UI_N} ${UI_L}%s${UI_N} %b${UI_N}\n" "$k" "${d// /·}" "$v"
}
ui_msg() {
    local l first=1
    while IFS= read -r l; do
        if [ $first = 1 ]; then printf "%b %s\n" "$1" "$l"; first=0; else printf "  %s\n" "$l"; fi
    done < <(printf '%b\n' "$2" | sed 's/\x1b\[[0-9;]*m//g' | fold -s -w 38)
}
ui_note() {
    local l
    while IFS= read -r l; do printf "${UI_D}%s${UI_N}\n" "$l"; done < <(printf '%b\n' "$1" | sed 's/\x1b\[[0-9;]*m//g' | fold -s -w 40)
}
ui_ok()   { ui_msg "${UI_OK}✓${UI_N}" "$1"; }
ui_err()  { ui_msg "${UI_R}×${UI_N}" "$1"; }
ui_warn() { ui_msg "${UI_Y}!${UI_N}" "$1"; }

inst_banner() { ui_row "${UI_T}TOMATO${UI_N}${UI_D} / $1${UI_N}" "${UI_D}${2:-}"; ui_hr2; }

# ── Steps: "label ········ 12s ✓" ──
fmt_dur() { local s=$1; if [ "$s" -ge 60 ]; then echo "$((s / 60))m$((s % 60))s"; else echo "${s}s"; fi; }
STEPS=0; STEPS_OK=0
run() {
    local msg=$1 t0=$SECONDS pid rc f='|/-\' i=0 c; shift
    "$@" >>"$LOGFILE" 2>&1 </dev/null &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        c=${f:i++ % 4:1}
        printf '\r%s' "$(ui_lead "$msg" "${UI_Y}$(fmt_dur $((SECONDS - t0))) $c")"
        sleep 0.15
    done
    wait "$pid"; rc=$?
    printf '\r\033[K'
    STEPS=$((STEPS + 1))
    if [ $rc -eq 0 ]; then
        STEPS_OK=$((STEPS_OK + 1))
        ui_lead "$msg" "${UI_D}$(fmt_dur $((SECONDS - t0))) ${UI_OK}✓"
    else
        ui_lead "$msg" "${UI_R}× failed"
    fi
    return $rc
}
step_die() { ui_hr2; ui_err "$1"; ui_note "Log: $LOGFILE"; exit 1; }

# Interactive input works even when piped from curl.
TTY=/dev/tty
{ exec 9<"$TTY"; } 2>/dev/null && exec 9<&- || TTY=/dev/stdin
ask() {
    local a
    read -r -p "$(printf "${UI_T}›${UI_N} %s " "$1")" a <"$TTY" || true
    echo "$a"
}

[ "$(id -u)" = 0 ] || { ui_err "Run this script as root."; exit 1; }
command -v systemctl >/dev/null || { ui_err "systemd is required."; exit 1; }
command -v curl >/dev/null || { ui_err "curl is required."; exit 1; }

case "$(uname -m)" in
    x86_64 | amd64) ARCH=amd64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    *) ui_err "Unsupported architecture: $(uname -m) (amd64/arm64 only)."; exit 1 ;;
esac

t_start=$SECONDS
os_name=$( (. /etc/os-release; echo "${ID^} ${VERSION_ID:-}") 2>/dev/null)
TOMATO=0
[ -x /usr/local/bin/tomato-agent ] && TOMATO=1
OLDVER=$([ -x "$BIN" ] && "$BIN" version 2>/dev/null || echo "-")

clear 2>/dev/null || true
if [ "$MODE" = update ]; then inst_banner "store bot update"; else inst_banner "store bot install"; fi
ui_note "sell VPN accounts from Telegram: buy, trial, renew, top up"
ui_hr
ui_lead os "${os_name:-linux}"
ui_lead arch "$ARCH"
if [ $TOMATO = 1 ]; then ui_lead tomato "${UI_OK}installed"; else ui_lead tomato "${UI_Y}not found"; fi
ui_lead installed "$OLDVER"
ui_hr

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
download() {
    curl -fsSL --retry 3 "$REPO/version" -o "$TMP/version" &&
        curl -fsSL --retry 3 "$REPO/SHA256SUMS" -o "$TMP/SHA256SUMS" &&
        curl -fsSL --retry 3 "$REPO/tomato-store-linux-$ARCH" -o "$TMP/tomato-store-linux-$ARCH"
}
verify() { (cd "$TMP" && grep " tomato-store-linux-$ARCH\$" SHA256SUMS | sha256sum -c --status); }
run "download" download || step_die "Download failed: $REPO"
run "checksum" verify || step_die "Checksum mismatch (corrupted download)."

NEWVER=$(tr -d '[:space:]' <"$TMP/version")
if [ "$MODE" = update ] && [ "$OLDVER" = "$NEWVER" ]; then
    ui_hr2
    ui_ok "Already up to date (v$NEWVER)."
    ui_hr2
    exit 0
fi

install_bin() {
    install -d -m 700 "$ETC" "$DATA" &&
        { [ ! -x "$BIN" ] || cp -f "$BIN" "$BIN.bak"; } &&
        install -m 755 "$TMP/tomato-store-linux-$ARCH" "$BIN.new" && mv -f "$BIN.new" "$BIN"
}
run "binary v$NEWVER" install_bin || step_die "Failed to install $BIN"

# The bot creates accounts through the Tomato API of this VPS.
api_enabled() { grep -q '"api"[[:space:]]*:[[:space:]]*true' /etc/tomato/agent.json 2>/dev/null; }
enable_api() {
    if [ ! -s /etc/tomato-api/api.key ]; then
        install -d -m 700 /etc/tomato-api
        (umask 077 && head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' >/etc/tomato-api/api.key.tmp &&
            echo >>/etc/tomato-api/api.key.tmp && mv -f /etc/tomato-api/api.key.tmp /etc/tomato-api/api.key) || return 1
    fi
    install -d -m 700 /etc/tomato
    local cfg='{}'
    [ -s /etc/tomato/agent.json ] && cfg=$(cat /etc/tomato/agent.json)
    command -v jq >/dev/null || return 1
    jq '.api = true' <<<"$cfg" >/etc/tomato/agent.json.tmp && chmod 600 /etc/tomato/agent.json.tmp &&
        mv -f /etc/tomato/agent.json.tmp /etc/tomato/agent.json || return 1
    systemctl restart tomato-agent 2>/dev/null || systemctl enable --now tomato-agent
    api_enabled
}
if [ $TOMATO = 1 ] && { ! api_enabled || [ ! -s /etc/tomato-api/api.key ]; }; then
    ui_warn "Tomato API is off. The bot needs it to create accounts."
    yn=$(ask "enable Tomato API now? [Y/n]")
    if [[ ! "$yn" =~ ^[Nn]$ ]]; then
        run "tomato api" enable_api || ui_warn "Enable it from: menu > System Settings > Tomato API"
    fi
fi

if [ "$MODE" = install ]; then
    if [ -s "$ETC/config.json" ]; then
        yn=$(ask "config found, run setup again? [y/N]")
        [[ "$yn" =~ ^[Yy]$ ]] && "$BIN" setup <"$TTY"
    else
        "$BIN" setup <"$TTY" || step_die "Setup cancelled."
    fi
fi

write_unit() {
    cat >"$UNIT" <<'EOF'
[Unit]
Description=Tomato Store Bot (Telegram VPN store)
After=network-online.target tomato-agent.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/tomato-store serve
Restart=always
RestartSec=5
# Light: VPN services always come first.
Environment=GOMEMLIMIT=40MiB
Environment=GOGC=50
MemoryMax=128M
CPUQuota=50%
Nice=10
IOSchedulingClass=idle
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/var/lib/tomato-store /etc/tomato-store

[Install]
WantedBy=multi-user.target
EOF
    cat >/usr/bin/menu-store <<'EOF'
#!/bin/bash
exec /usr/local/bin/tomato-store menu "$@"
EOF
    chmod 755 /usr/bin/menu-store
    systemctl daemon-reload && systemctl enable tomato-store
}
start_bot() {
    systemctl restart tomato-store || return 1
    sleep 4
    systemctl is-active --quiet tomato-store
}
run "service" write_unit || step_die "Failed to install the systemd service."
if ! run "start bot" start_bot; then
    if [ "$MODE" = update ] && [ -x "$BIN.bak" ]; then
        mv -f "$BIN.bak" "$BIN"
        run "rollback v$OLDVER" start_bot
    fi
    journalctl -u tomato-store -n 15 --no-pager >>"$LOGFILE" 2>&1
    step_die "The bot failed to start. Check: journalctl -u tomato-store -n 50"
fi
rm -f "$BIN.bak"

# ── Completion ──
clear 2>/dev/null || true
if [ "$MODE" = update ]; then inst_banner "updated" "v$NEWVER"; else inst_banner "installed" "v$NEWVER"; fi
ui_lead version "$NEWVER"
ui_lead os "${os_name:-linux}"
ui_lead time "$(( (SECONDS - t_start) / 60 ))m $(( (SECONDS - t_start) % 60 ))s"
if [ "$STEPS_OK" -eq "$STEPS" ]; then
    ui_lead steps "${UI_OK}${STEPS_OK} / ${STEPS} ✓"
else
    ui_lead steps "${UI_Y}${STEPS_OK} / ${STEPS}"
fi
ui_hr
ui_lead telegram "/start · /admin"
ui_lead "vps menu" "menu-store"
ui_lead logs "journalctl -u tomato-store -f"
ui_hr
if [ "$MODE" = update ]; then ui_ok "Update completed"; else ui_ok "Installation completed"; fi
[ "$MODE" = update ] || ui_note "Open the bot, send /admin, then set payment gateways and prices."
ui_hr2
