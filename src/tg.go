package main

// Minimal Telegram Bot API client (long polling, no dependencies).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
	Style        string `json:"style,omitempty"` // success | primary | danger
}

type Keyboard [][]Button

type TGUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u TGUser) name() string {
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if n == "" {
		n = u.Username
	}
	return n
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type PhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

type Message struct {
	MessageID int64       `json:"message_id"`
	From      *TGUser     `json:"from"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	Photo     []PhotoSize `json:"photo"`
	Document  *Document   `json:"document"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    TGUser   `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type ChatMemberUpdated struct {
	Chat          Chat   `json:"chat"`
	From          TGUser `json:"from"`
	NewChatMember struct {
		Status string `json:"status"`
	} `json:"new_chat_member"`
}

type Update struct {
	UpdateID      int64              `json:"update_id"`
	Message       *Message           `json:"message"`
	CallbackQuery *CallbackQuery     `json:"callback_query"`
	MyChatMember  *ChatMemberUpdated `json:"my_chat_member"`
}

type TG struct {
	base   string
	token  string
	hc     *http.Client
	poll   *http.Client
	noSty  bool // client rejected button "style" → stop sending it
	styMu  sync.Mutex
	BotTag string
}

type tgError struct {
	Code       int
	Desc       string
	RetryAfter int
}

func (e *tgError) Error() string { return fmt.Sprintf("telegram %d: %s", e.Code, e.Desc) }

func isForbidden(err error) bool {
	var te *tgError
	return errors.As(err, &te) && te.Code == 403
}

func notModified(err error) bool {
	var te *tgError
	return errors.As(err, &te) && strings.Contains(te.Desc, "message is not modified")
}

func newTG(base, token string) *TG {
	return &TG{
		base:  strings.TrimRight(base, "/"),
		token: token,
		hc:    &http.Client{Timeout: 30 * time.Second},
		poll:  &http.Client{Timeout: 70 * time.Second},
	}
}

func (t *TG) url(method string) string { return t.base + "/bot" + t.token + "/" + method }

func (t *TG) do(ctx context.Context, hc *http.Client, req *http.Request, out any) error {
	resp, err := hc.Do(req.WithContext(ctx))
	if err != nil {
		// Never leak the token in logs.
		return errors.New(strings.ReplaceAll(err.Error(), t.token, "<token>"))
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram: invalid response (HTTP %d)", resp.StatusCode)
	}
	if !r.OK {
		return &tgError{r.ErrorCode, r.Description, r.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// call sends a JSON request. Flood limits (429) are retried once after the
// requested delay (max 30 s).
func (t *TG) call(ctx context.Context, method string, payload any, out any) error {
	b, _ := json.Marshal(payload)
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequest("POST", t.url(method), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		err := t.do(ctx, t.hc, req, out)
		var te *tgError
		if attempt == 0 && errors.As(err, &te) && te.Code == 429 && te.RetryAfter > 0 && te.RetryAfter <= 30 {
			select {
			case <-time.After(time.Duration(te.RetryAfter) * time.Second):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
}

// markup converts a keyboard to reply_markup, dropping button styles when
// the Bot API in use does not know them.
func (t *TG) markup(kb Keyboard) any {
	if kb == nil {
		return nil
	}
	t.styMu.Lock()
	strip := t.noSty
	t.styMu.Unlock()
	rows := make([][]Button, 0, len(kb))
	for _, r := range kb {
		row := make([]Button, len(r))
		copy(row, r)
		if strip {
			for i := range row {
				row[i].Style = ""
			}
		}
		rows = append(rows, row)
	}
	return map[string]any{"inline_keyboard": rows}
}

func (t *TG) styleRejected(err error, kb Keyboard) bool {
	var te *tgError
	if kb == nil || !errors.As(err, &te) || te.Code != 400 || !strings.Contains(strings.ToLower(te.Desc), "style") {
		return false
	}
	t.styMu.Lock()
	t.noSty = true
	t.styMu.Unlock()
	return true
}

func (t *TG) Send(chat int64, text string, kb Keyboard) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	p := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true}}
	if kb != nil {
		p["reply_markup"] = t.markup(kb)
	}
	var m Message
	err := t.call(ctx, "sendMessage", p, &m)
	if t.styleRejected(err, kb) {
		p["reply_markup"] = t.markup(kb)
		err = t.call(ctx, "sendMessage", p, &m)
	}
	return m.MessageID, err
}

func (t *TG) Edit(chat, msgID int64, text string, kb Keyboard) error {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	p := map[string]any{"chat_id": chat, "message_id": msgID, "text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true}}
	if kb != nil {
		p["reply_markup"] = t.markup(kb)
	}
	err := t.call(ctx, "editMessageText", p, nil)
	if t.styleRejected(err, kb) {
		p["reply_markup"] = t.markup(kb)
		err = t.call(ctx, "editMessageText", p, nil)
	}
	if notModified(err) {
		return nil
	}
	return err
}

func (t *TG) EditMarkup(chat, msgID int64, kb Keyboard) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := map[string]any{"chat_id": chat, "message_id": msgID}
	if kb != nil {
		p["reply_markup"] = t.markup(kb)
	} else {
		p["reply_markup"] = map[string]any{"inline_keyboard": [][]Button{}}
	}
	err := t.call(ctx, "editMessageReplyMarkup", p, nil)
	if notModified(err) {
		return nil
	}
	return err
}

func (t *TG) EditCaption(chat, msgID int64, caption string, kb Keyboard) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := map[string]any{"chat_id": chat, "message_id": msgID, "caption": caption, "parse_mode": "HTML"}
	if kb != nil {
		p["reply_markup"] = t.markup(kb)
	}
	err := t.call(ctx, "editMessageCaption", p, nil)
	if notModified(err) {
		return nil
	}
	return err
}

func (t *TG) Delete(chat, msgID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = t.call(ctx, "deleteMessage", map[string]any{"chat_id": chat, "message_id": msgID}, nil)
}

func (t *TG) Answer(id, text string, alert bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := map[string]any{"callback_query_id": id}
	if text != "" {
		p["text"] = text
		p["show_alert"] = alert
	}
	_ = t.call(ctx, "answerCallbackQuery", p, nil)
}

// upload sends a file with multipart/form-data (sendPhoto / sendDocument).
func (t *TG) upload(method, field string, chat int64, filename string, data []byte, caption string, kb Keyboard) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	build := func() (*http.Request, error) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("chat_id", fmt.Sprint(chat))
		if caption != "" {
			_ = w.WriteField("caption", caption)
			_ = w.WriteField("parse_mode", "HTML")
		}
		if kb != nil {
			b, _ := json.Marshal(t.markup(kb))
			_ = w.WriteField("reply_markup", string(b))
		}
		fw, err := w.CreateFormFile(field, filename)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		req, err := http.NewRequest("POST", t.url(method), &buf)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		return req, nil
	}
	req, err := build()
	if err != nil {
		return 0, err
	}
	var m Message
	hc := &http.Client{Timeout: 120 * time.Second}
	err = t.do(ctx, hc, req, &m)
	if t.styleRejected(err, kb) {
		if req, err = build(); err == nil {
			err = t.do(ctx, hc, req, &m)
		}
	}
	return m.MessageID, err
}

func (t *TG) SendPhoto(chat int64, png []byte, caption string, kb Keyboard) (int64, error) {
	return t.upload("sendPhoto", "photo", chat, "qris.png", png, caption, kb)
}

func (t *TG) SendDocument(chat int64, name string, data []byte, caption string) error {
	_, err := t.upload("sendDocument", "document", chat, name, data, caption, nil)
	return err
}

// SendPhotoID re-sends an already uploaded photo (broadcast).
func (t *TG) SendPhotoID(chat int64, fileID, caption string, kb Keyboard) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	p := map[string]any{"chat_id": chat, "photo": fileID, "caption": caption, "parse_mode": "HTML"}
	if kb != nil {
		p["reply_markup"] = t.markup(kb)
	}
	var m Message
	err := t.call(ctx, "sendPhoto", p, &m)
	return m.MessageID, err
}

func (t *TG) GetMe() (TGUser, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var u TGUser
	err := t.call(ctx, "getMe", map[string]any{}, &u)
	return u, err
}

func (t *TG) MemberStatus(chat string, user int64) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var r struct {
		Status string `json:"status"`
	}
	err := t.call(ctx, "getChatMember", map[string]any{"chat_id": chat, "user_id": user}, &r)
	return r.Status, err
}

// Download fetches a file sent to the bot (max 50 MB by Bot API).
func (t *TG) Download(fileID string, max int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := t.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", t.base+"/file/bot"+t.token+"/"+f.FilePath, nil)
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, errors.New("download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download failed (HTTP %d)", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("file too large")
	}
	return b, nil
}

func (t *TG) Updates(ctx context.Context, offset int64) ([]Update, error) {
	b, _ := json.Marshal(map[string]any{
		"offset":          offset,
		"timeout":         50,
		"allowed_updates": []string{"message", "callback_query", "my_chat_member"},
	})
	req, _ := http.NewRequest("POST", t.url("getUpdates"), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	var ups []Update
	err := t.do(ctx, t.poll, req, &ups)
	return ups, err
}
