// Tomato Store Bot — open source Telegram VPN store for Tomato Autoscript.
//
//	tomato-store serve     run the bot (systemd)
//	tomato-store setup     first-time wizard (token, admins, store name)
//	tomato-store menu      VPS menu (also: menu-store)
//	tomato-store doctor    check token, database, servers and gateways
//	tomato-store backup    write a database backup
//	tomato-store version
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var version = "2.0.0"

func main() {
	log.SetFlags(0)
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		serve()
	case "setup":
		if err := setup(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "menu":
		menu()
	case "doctor":
		os.Exit(doctorCLI())
	case "backup":
		cfg, db := mustOpen()
		b := newBot(cfg, db)
		path, err := b.backup()
		if err != nil {
			uiErr("Backup failed: " + err.Error())
			os.Exit(1)
		}
		uiOk("Backup saved")
		uiNote(path)
	case "version", "-v", "--version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, "usage: tomato-store [serve|setup|menu|doctor|backup|version]")
		os.Exit(2)
	}
}

func mustOpen() (*Config, *DB) {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	applyPendingRestore()
	db, err := openDB(dbFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database:", err)
		os.Exit(1)
	}
	return cfg, db
}

func serve() {
	log.SetFlags(log.LstdFlags)
	cfg, db := mustOpen()
	defer db.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	b := newBot(cfg, db)
	b.stop = cancel
	if err := b.run(ctx); err != nil {
		log.Println(err)
		os.Exit(1)
	}
	// Exit code 1 after a restore so systemd (Restart=always) restarts us.
	if _, err := os.Stat(dbFile + ".upload"); err == nil {
		os.Exit(1)
	}
}
