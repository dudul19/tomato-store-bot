package main

// card — Telegram message in the Tomato Autoscript 3.4 style: UPPERCASE title,
// a line, then "Label : value" rows where only the value is monospace (tap to
// copy). No emoji. The line is 27 characters so every bubble is drawn at full
// width and the inline keyboard below it always has the same width.

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

const cardLine = "───────────────────────────"

type card struct{ b strings.Builder }

func newCard(title string) *card {
	c := &card{}
	c.b.WriteString("<b>" + esc(title) + "</b>\n" + cardLine + "\n")
	return c
}

// kv: "Label : <code>value</code>"; empty values are skipped.
func (c *card) kv(k, v string) *card {
	if v != "" {
		fmt.Fprintf(&c.b, "%s : <code>%s</code>\n", esc(k), esc(v))
	}
	return c
}

func (c *card) text(s string) *card {
	c.b.WriteString(esc(s) + "\n")
	return c
}

// raw: HTML line as is (caller escapes).
func (c *card) raw(s string) *card {
	c.b.WriteString(s + "\n")
	return c
}

// block: "[Title]" and a monospace value below (config links, payloads).
func (c *card) block(title, v string) *card {
	if v != "" {
		fmt.Fprintf(&c.b, "[%s]\n<code>%s</code>\n", esc(title), esc(v))
	}
	return c
}

func (c *card) line() *card {
	s := c.b.String()
	if !strings.HasSuffix(s, cardLine+"\n") {
		c.b.WriteString(cardLine + "\n")
	}
	return c
}

func (c *card) String() string {
	s := strings.TrimRight(c.b.String(), "\n")
	return strings.TrimSuffix(s, "\n"+cardLine)
}

func esc(s string) string { return html.EscapeString(s) }

// rp formats rupiah: Rp 10.000
func rp(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, s[i])
	}
	if neg {
		return "- Rp " + string(out)
	}
	return "Rp " + string(out)
}

// num formats a plain number with thousand separators: 1.284
func num(n int64) string { return strings.TrimPrefix(rp(n), "Rp ") }

// parseMoney accepts 10000, 10.000, 10k, 10rb, 1jt.
func parseMoney(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "rp")
	s = strings.TrimSpace(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "jt"):
		mult, s = 1000000, strings.TrimSuffix(s, "jt")
	case strings.HasSuffix(s, "rb"):
		mult, s = 1000, strings.TrimSuffix(s, "rb")
	case strings.HasSuffix(s, "k"):
		mult, s = 1000, strings.TrimSuffix(s, "k")
	}
	s = strings.NewReplacer(".", "", ",", "", " ", "", "_", "").Replace(s)
	if s == "" || len(s) > 12 {
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, strconv.ErrSyntax
	}
	return n * mult, nil
}

func fmtTime(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

func fmtDate(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02")
}

func gb(bytes int64) string {
	if bytes <= 0 {
		return "Unlimited"
	}
	g := float64(bytes) / (1 << 30)
	if g == float64(int64(g)) {
		return fmt.Sprintf("%d GB", int64(g))
	}
	return fmt.Sprintf("%.1f GB", g)
}

func usage(used, quota int64) string {
	u := float64(used) / (1 << 30)
	if quota <= 0 {
		return fmt.Sprintf("%.1f GB / Unlimited", u)
	}
	return fmt.Sprintf("%.1f / %s", u, gb(quota))
}

func btn(text, data string) Button { return Button{Text: text, CallbackData: data} }
func btnS(text, data, style string) Button {
	return Button{Text: text, CallbackData: data, Style: style}
}
func btnURL(text, url string) Button { return Button{Text: text, URL: url} }
func row(b ...Button) []Button       { return b }
func kbd(rows ...[]Button) Keyboard  { return Keyboard(rows) }
func pair(a, b Button) []Button      { return []Button{a, b} }
func ok(text, data string) Button    { return btnS(text, data, "success") }
func pri(text, data string) Button   { return btnS(text, data, "primary") }
func bad(text, data string) Button   { return btnS(text, data, "danger") }
func chunk(bs []Button, per int) Keyboard {
	var kb Keyboard
	for i := 0; i < len(bs); i += per {
		j := i + per
		if j > len(bs) {
			j = len(bs)
		}
		kb = append(kb, bs[i:j])
	}
	return kb
}

var protoList = []string{"ssh", "vmess", "vless", "trojan", "zivpn"}
var protoLabel = map[string]string{"ssh": "SSH", "vmess": "VMess", "vless": "VLESS", "trojan": "Trojan", "zivpn": "ZiVPN"}

func isProto(p string) bool { _, ok := protoLabel[p]; return ok }
