package main

// Config file (/etc/tomato-store/config.json) — only what the bot needs before
// it can open the database: the Telegram token and admin IDs. Everything else
// (store name, prices, payment gateways, texts) is stored in the database and
// edited from the bot itself (/admin).
//
// Field names are kept compatible with tomato-store 1.0.x.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var (
	etcDir      = env("TOMATO_STORE_ETC", "/etc/tomato-store")
	dataDir     = env("TOMATO_STORE_DATA", "/var/lib/tomato-store")
	configFile  = filepath.Join(etcDir, "config.json")
	textsFile   = filepath.Join(etcDir, "texts.json")
	logoFile    = filepath.Join(etcDir, "logo.png")
	dbFile      = filepath.Join(dataDir, "store.db")
	backupDir   = filepath.Join(dataDir, "backups")
	tomatoKey   = env("TOMATO_API_KEY_FILE", "/etc/tomato-api/api.key")
	tomatoXray  = env("TOMATO_DOMAIN_FILE", "/etc/xray/domain")
	defaultAPI  = "http://127.0.0.1:9090/api/v1"
	telegramAPI = env("TOMATO_STORE_TELEGRAM_API", "https://api.telegram.org")
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type Config struct {
	Token       string  `json:"bot_token"`
	Admins      []int64 `json:"admins"`
	NotifyChat  int64   `json:"notify_chat,omitempty"`   // optional log group/channel
	LocalAPIURL string  `json:"local_api_url,omitempty"` // Tomato API of this VPS
	TelegramAPI string  `json:"telegram_api,omitempty"`  // custom Bot API server

	// Only read for migration from 1.0.x (moved into the database).
	AutoGoPayKey string `json:"autogopay_key,omitempty"`
	AutoGoPayURL string `json:"autogopay_url,omitempty"`
}

func loadConfig() (*Config, error) {
	b, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, errors.New("config.json: " + err.Error())
	}
	c.Token = strings.TrimSpace(c.Token)
	if c.Token == "" {
		return nil, errors.New("config.json: bot_token is empty (run: tomato-store setup)")
	}
	if len(c.Admins) == 0 {
		return nil, errors.New("config.json: admins is empty (run: tomato-store setup)")
	}
	if c.LocalAPIURL == "" {
		c.LocalAPIURL = defaultAPI
	}
	if c.TelegramAPI == "" {
		c.TelegramAPI = telegramAPI
	}
	return &c, nil
}

func saveConfig(c *Config) error {
	if err := os.MkdirAll(etcDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := configFile + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, configFile)
}

func (c *Config) isAdmin(id int64) bool {
	for _, a := range c.Admins {
		if a == id {
			return true
		}
	}
	return false
}

// localKey — API key of the Tomato API on this VPS (re-read each call so a
// key rotated from the Tomato menu is picked up without restart).
func localKey() string {
	b, _ := os.ReadFile(tomatoKey)
	return strings.TrimSpace(string(b))
}

func localDomain() string {
	b, err := os.ReadFile(tomatoXray)
	if err != nil {
		h, _ := os.Hostname()
		return h
	}
	return strings.TrimSpace(string(b))
}
