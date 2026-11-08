package main

// Terminal look of Tomato Autoscript 3.4 ("Struk", src/bash/lib/ui.sh):
// max 40 columns, flush left, one column, truecolor. Header
// "TOMATO / title ····· info" + ═, thin ┄ separators, grey labels with dot
// leaders and values right-aligned at column 40, menu items "[1] Label".

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	uiT  = "\x1b[38;2;255;122;92m"  // #ff7a5c tomato accent
	uiG  = "\x1b[38;2;143;209;158m" // #8fd19e menu numbers in prompt
	uiOK = "\x1b[38;2;122;214;154m" // #7ad69a ok
	uiD  = "\x1b[38;2;138;143;150m" // #8a8f96 labels
	uiL  = "\x1b[38;2;58;63;69m"    // #3a3f45 lines
	uiR  = "\x1b[38;2;255;107;107m" // #ff6b6b error
	uiY  = "\x1b[38;2;232;193;112m" // #e8c170 warning
	uiN  = "\x1b[0m"
	uiW  = 40
)

var reANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func uiLen(s string) int { return utf8.RuneCountInString(reANSI.ReplaceAllString(s, "")) }

func uiLine(ch string) { fmt.Println(uiL + strings.Repeat(ch, uiW) + uiN) }
func uiHr()            { uiLine("┄") }
func uiHr2()           { uiLine("═") }

// uiRow: right part right-aligned at column 40.
func uiRow(a, b string) {
	pad := uiW - uiLen(a) - uiLen(b)
	if pad < 1 {
		pad = 1
	}
	fmt.Println(a + strings.Repeat(" ", pad) + b + uiN)
}

// uiHead: "TOMATO / title ········ info" + ═
func uiHead(title, info string) {
	t := strings.ToLower(title)
	if 9+uiLen(t)+uiLen(info)+1 <= uiW {
		t = uiT + "TOMATO" + uiN + uiD + " / " + t + uiN
	} else {
		t = uiT + t + uiN
	}
	if info != "" {
		uiRow(t, uiD+info)
	} else {
		fmt.Println(t)
	}
	uiHr2()
}

// uiLead: "label ······ value" (value right-aligned at column 40).
func uiLead(k, v string) {
	v = strings.TrimRight(v, " ")
	n := uiW - uiLen(k) - uiLen(v) - 2
	if n < 2 {
		plain := []rune(reANSI.ReplaceAllString(v, ""))
		keep := uiW - uiLen(k) - 5
		if keep < 1 {
			keep = 1
		}
		if len(plain) > keep {
			plain = plain[:keep]
		}
		v = string(plain) + "~"
		n = uiW - uiLen(k) - uiLen(v) - 2
		if n < 1 {
			n = 1
		}
	}
	fmt.Printf("%s%s%s %s%s%s %s%s\n", uiD, k, uiN, uiL, strings.Repeat("·", n), uiN, v, uiN)
}

// uiItem: "[1]  Label" with optional grey info on the right.
func uiItem(n int, label, info string) {
	b := fmt.Sprintf("%s[%d]%s", uiT, n, uiN)
	if n < 10 {
		b += " "
	}
	if info != "" {
		uiRow(b+" "+label, uiD+info)
	} else {
		fmt.Println(b + " " + label)
	}
}

// uiMsg: icon + text folded at 38 columns with a hanging indent.
func uiMsg(icon, text string) {
	for i, l := range fold(reANSI.ReplaceAllString(text, ""), 38) {
		if i == 0 {
			fmt.Println(icon + " " + l)
		} else {
			fmt.Println("  " + l)
		}
	}
}

func uiOk(s string)   { uiMsg(uiOK+"✓"+uiN, s) }
func uiErr(s string)  { uiMsg(uiR+"×"+uiN, s) }
func uiWarn(s string) { uiMsg(uiY+"!"+uiN, s) }

func uiNote(s string) {
	for _, l := range fold(s, uiW) {
		fmt.Println(uiD + l + uiN)
	}
}

// uiAsk: prompt of a menu ("› choose 1-7, x to go back").
func uiAsk(rng string) string {
	uiHr2()
	return ask(fmt.Sprintf("\n%s›%s choose %s%s%s, %sx%s to go back ", uiT, uiN, uiG, rng, uiN, uiT, uiN))
}

// uiInput: "› label: "
func uiInput(label string) string {
	return ask(fmt.Sprintf("%s›%s %s: ", uiT, uiN, label))
}

func uiAnyKey() {
	ask(fmt.Sprintf("\n%s›%s %spress enter to return%s", uiT, uiN, uiD, uiN))
}

func uiOn(s string) string  { return uiOK + "● " + s + uiN }
func uiOff(s string) string { return uiR + "● " + s + uiN }

// fold splits text into lines of at most w runes at spaces.
func fold(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := ""
		for _, wd := range words {
			switch {
			case cur == "":
				cur = wd
			case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(wd) <= w:
				cur += " " + wd
			default:
				out = append(out, cur)
				cur = wd
			}
		}
		out = append(out, cur)
	}
	return out
}
