package main

// Account output — same order and content as the Tomato Autoscript 3.4 bot
// (info + ports + paths, config links, UUID/password, pubkey/payload,
// expiry, subscription link).

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func (a *Account) portList(key string) string {
	if a.Connection == nil {
		return ""
	}
	raw, ok := a.Connection.Ports[key]
	if !ok {
		return ""
	}
	var nums []int
	if json.Unmarshal(raw, &nums) == nil {
		s := make([]string, len(nums))
		for i, n := range nums {
			s[i] = strconv.Itoa(n)
		}
		return strings.Join(s, ", ")
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return ""
}

func ipText(n int) string {
	if n <= 0 {
		return "Unlimited"
	}
	return fmt.Sprintf("%d IP", n)
}

func linkTitle(proto, k string) string {
	for _, s := range [][2]string{{"_tls_link", "TLS"}, {"_nontls_link", "HTTP"}, {"_grpc_link", "gRPC"},
		{"_hu_link", "HTTPUpgrade"}, {"_xhttp_link", "XHTTP"}} {
		if strings.HasSuffix(k, s[0]) {
			return protoLabel[proto] + " " + s[1]
		}
	}
	k = strings.ReplaceAll(strings.TrimSuffix(k, "_link"), "_", " ")
	if k == "" {
		return "Link"
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

func linkOrder(links map[string]string) []string {
	rank := func(k string) int {
		for i, suf := range []string{"_tls_link", "_nontls_link", "_grpc_link", "_hu_link", "_xhttp_link"} {
			if strings.HasSuffix(k, suf) {
				return i
			}
		}
		return 9
	}
	keys := make([]string, 0, len(links))
	for k := range links {
		if k != "method" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// formatAccount renders a full config. kind = "PREMIUM" or "TRIAL".
func formatAccount(proto, kind string, a *Account, expiresAt int64) string {
	c := newCard(protoLabel[proto] + " " + kind)
	exp := a.Expired
	if exp == "" {
		exp = fmtTime(expiresAt)
	}
	limits := func() {
		c.kv("Expired", exp).kv("Quota", gb(a.QuotaBytes)).kv("IP Limit", ipText(a.IPLimit)).line()
	}
	var sub string
	switch proto {
	case "ssh":
		host := a.Domain
		conn := a.Connection
		if conn != nil && conn.Host != "" {
			host = conn.Host
		}
		c.kv("Username", a.Username).kv("Password", a.Password).kv("Domain", host)
		if conn != nil {
			c.kv("NS", conn.SlowDNSHost)
		}
		c.kv("SSH", "109, 143").
			kv("SSH WS", "80, 2086, 8080, 8880").
			kv("SSH SSL WS", "443, 8443")
		if sq := a.portList("squid"); sq != "" {
			c.kv("Squid", sq)
		}
		c.kv("SSL/TLS", "443, 8443").
			kv("SlowDNS", "53, 5300").
			kv("HCR", "2087").
			kv("BHTTP", "2082, 2096").
			kv("UDP Custom", "1-65535").
			kv("OVPN TCP", "1194").
			kv("OVPN UDP", "2200").
			kv("BadVPN UDP", "7100, 7200, 7300").
			line()
		if conn != nil {
			c.block("OpenVPN", conn.OpenVPN).
				block("PubKey SlowDNS", conn.SlowDNSPubkey).
				block("Payload", strings.ReplaceAll(conn.WSSPayload, "[crlf]", "\n"))
			sub = conn.SubLink
			if conn.OpenVPN != "" || conn.SlowDNSPubkey != "" || conn.WSSPayload != "" {
				c.line()
			}
		}
		limits()
		if sub == "" && host != "" {
			sub = "https://" + host + "/ssh/sub?id=" + url.QueryEscape(a.Username)
		}
	case "zivpn":
		c.kv("Username", a.Username).kv("Password", a.Password).
			kv("Host", a.Domain).kv("UDP Port", "5667").kv("Port Range", "6000-19999").line()
		limits()
		if a.Domain != "" {
			sub = "https://" + a.Domain + "/zivpn/sub?id=" + url.QueryEscape(a.Username)
		}
	default: // vmess / vless / trojan
		c.kv("Username", a.Username).
			kv("Domain", a.Domain).
			kv("Port TLS", "443, 8443").
			kv("Port HTTP", "80, 2086, 8080, 8880")
		if proto == "vmess" {
			c.kv("Alter ID", "0")
		}
		ws := map[string]string{"vmess": "/whatever/vmess", "vless": "/whatever/vless", "trojan": "/whatever/trojan-ws"}[proto]
		c.kv("Security", "Auto").
			kv("Network", "WS, gRPC, HTTPUpgrade, XHTTP").
			kv("Path WS", ws).
			kv("Path HTTPUpgrade", "/"+proto+"-hu").
			kv("Path XHTTP", "/"+proto+"-xh").
			kv("gRPC Service", proto+"-grpc").
			line()
		for _, k := range linkOrder(a.Links) {
			c.block(linkTitle(proto, k), a.Links[k])
		}
		if proto == "trojan" {
			c.kv("Password", a.UUID)
		} else {
			c.kv("UUID", a.UUID)
		}
		c.line()
		limits()
		if a.Domain != "" {
			sub = "https://" + a.Domain + "/" + proto + "/sub?id=" + url.QueryEscape(a.Username)
		}
	}
	c.block("Subscription", sub)
	return c.String()
}
