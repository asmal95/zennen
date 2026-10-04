// Веб-вьювер дневника: только чтение.
// diary.db открывается read-only; записи (сессии, кэш ревью) идут
// в отдельный sessions.db, который бэкапить не нужно.
// Наружу — только через reverse proxy с TLS, слушает localhost.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"diarybot/internal/config"
	"diarybot/internal/db"
	"diarybot/internal/web"
)

func main() {
	cfg := config.Load()
	store, err := db.OpenReadOnly(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db open:", err)
		os.Exit(1)
	}
	defer store.Close()
	sess, err := db.Open(cfg.SessDBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions db open:", err)
		os.Exit(1)
	}
	defer sess.Close()
	srv := web.New(store, sess, cfg)
	fmt.Printf("web listening on %s (read-only db=%s)\n", cfg.WebListen, cfg.DBPath)
	log.Fatal(http.ListenAndServe(cfg.WebListen, srv.Routes()))
}
