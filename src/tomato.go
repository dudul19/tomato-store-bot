package main

// Tomato API client (https://<domain>/api/v1, Bearer key).
// Docs: https://<domain>/docs on any Tomato Autoscript server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Account struct {
	Username      string            `json:"username"`
	Password      string            `json:"password"`
	UUID          string            `json:"uuid"`
	Status        string            `json:"status"`
	Locked        bool              `json:"locked"`
	Trial         bool              `json:"trial"`
	IPLimit       int               `json:"ip_limit"`
	QuotaBytes    int64             `json:"quota_bytes"`
	UsedBytes     int64             `json:"used_bytes"`
	ExpiresAt     int64             `json:"expires_at"`
	Expired       string            `json:"expired"`
	Domain        string            `json:"domain"`
	Links         map[string]string `json:"links"`
	Connection    *Connection       `json:"connection"`
	ActiveIPs     int               `json:"active_ips"`
	ActiveSession int               `json:"active_sessions"`
}

type Connection struct {
	Host          string                     `json:"host"`
	SlowDNSHost   string                     `json:"slowdns_host"`
	SlowDNSPubkey string                     `json:"slowdns_pubkey"`
	Ports         map[string]json.RawMessage `json:"ports"`
	WSSPayload    string                     `json:"wss_payload"`
	OpenVPN       string                     `json:"openvpn_config"`
	SubLink       string                     `json:"sub_link"`
}

// apiErr — the server answered with an error. Definite: the request was
// processed and rejected, so a paid order can be refunded safely.
type apiErr struct {
	Status int
	Msg    string
}

func (e *apiErr) Error() string { return e.Msg }

func isAPIErr(err error) (*apiErr, bool) {
	var e *apiErr
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

type Tomato struct {
	URL string
	Key string
	hc  *http.Client
}

func newTomato(u, key string, timeout time.Duration) *Tomato {
	return &Tomato{URL: strings.TrimRight(u, "/"), Key: key, hc: &http.Client{Timeout: timeout}}
}

// normalizeAPIURL: "sg1.example.com" → "https://sg1.example.com/api/v1".
func normalizeAPIURL(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", errors.New("empty")
	}
	if !strings.Contains(in, "://") {
		in = "https://" + in
	}
	u, err := url.Parse(in)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("invalid URL")
	}
	p := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(p, "/api/v1") {
		p += "/api/v1"
	}
	u.Path, u.RawQuery, u.Fragment = p, "", ""
	return u.String(), nil
}

func (t *Tomato) do(ctx context.Context, method string, path []string, body any, out any) error {
	u := t.URL
	for _, p := range path {
		u += "/" + url.PathEscape(p)
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tomato-store/"+version)
	resp, err := t.hc.Do(req)
	if err != nil {
		return fmt.Errorf("server unreachable: %w", unwrapURLErr(err))
	}
	defer resp.Body.Close()
	var r struct {
		Status  string          `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		if resp.StatusCode >= 500 || resp.StatusCode == 0 {
			return fmt.Errorf("server error (HTTP %d)", resp.StatusCode)
		}
		return &apiErr{resp.StatusCode, fmt.Sprintf("invalid response (HTTP %d)", resp.StatusCode)}
	}
	if r.Status != "success" {
		msg := r.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		// 5xx without a JSON error is uncertain; with a JSON error the
		// Tomato API has rolled the change back.
		return &apiErr{resp.StatusCode, msg}
	}
	if out != nil && len(r.Data) > 0 && string(r.Data) != "null" {
		return json.Unmarshal(r.Data, out)
	}
	return nil
}

func unwrapURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("timeout")
		}
		return ue.Err
	}
	return err
}

func ctxT(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func (t *Tomato) Ping() (time.Duration, error) {
	ctx, cancel := ctxT(15 * time.Second)
	defer cancel()
	t0 := time.Now()
	err := t.do(ctx, "GET", []string{"ping"}, nil, nil)
	return time.Since(t0), err
}

type ServerInfo struct {
	Domain string `json:"domain"`
	City   string `json:"city"`
	ISP    string `json:"isp"`
}

func (t *Tomato) Server() (*ServerInfo, error) {
	ctx, cancel := ctxT(20 * time.Second)
	defer cancel()
	var s ServerInfo
	return &s, t.do(ctx, "GET", []string{"server"}, nil, &s)
}

type CreateReq struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Days     int    `json:"days"`
	IPLimit  int    `json:"ip_limit"`
	QuotaGB  int    `json:"quota_gb"`
}

func (t *Tomato) Create(proto string, r CreateReq) (*Account, error) {
	ctx, cancel := ctxT(120 * time.Second)
	defer cancel()
	var a Account
	return &a, t.do(ctx, "POST", []string{proto, "accounts"}, r, &a)
}

func (t *Tomato) Trial(proto string, minutes int) (*Account, error) {
	ctx, cancel := ctxT(120 * time.Second)
	defer cancel()
	var a Account
	return &a, t.do(ctx, "POST", []string{proto, "trial"}, map[string]int{"minutes": minutes}, &a)
}

func (t *Tomato) Get(proto, user string) (*Account, error) {
	ctx, cancel := ctxT(30 * time.Second)
	defer cancel()
	var a Account
	return &a, t.do(ctx, "GET", []string{proto, "accounts", user}, nil, &a)
}

func (t *Tomato) List(proto string) ([]Account, error) {
	ctx, cancel := ctxT(60 * time.Second)
	defer cancel()
	var a []Account
	return a, t.do(ctx, "GET", []string{proto, "accounts"}, nil, &a)
}

func (t *Tomato) Renew(proto, user string, days, ipLimit, quotaGB int) (*Account, error) {
	ctx, cancel := ctxT(120 * time.Second)
	defer cancel()
	var a Account
	body := map[string]int{"days": days}
	if ipLimit >= 0 {
		body["ip_limit"] = ipLimit
	}
	if quotaGB >= 0 {
		body["quota_gb"] = quotaGB
	}
	return &a, t.do(ctx, "POST", []string{proto, "accounts", user, "renew"}, body, &a)
}

func (t *Tomato) Delete(proto, user string) error {
	ctx, cancel := ctxT(60 * time.Second)
	defer cancel()
	return t.do(ctx, "DELETE", []string{proto, "accounts", user}, nil, nil)
}

func (t *Tomato) Lock(proto, user string, lock bool) error {
	ctx, cancel := ctxT(60 * time.Second)
	defer cancel()
	act := "unlock"
	if lock {
		act = "lock"
	}
	return t.do(ctx, "POST", []string{proto, "accounts", user, act}, map[string]any{}, nil)
}

func notFound(err error) bool {
	e, ok := isAPIErr(err)
	return ok && e.Status == 404
}
