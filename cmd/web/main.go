// Веб-вьювер дневника: чтение + действия (задачи, напоминания, энергия).
// Пишет в те же таблицы diary.db, что и бот (иначе планировщик и бот
// не увидят созданного в вебе). Конкуренция записей ничтожна
// (клики пользователя против минутного тикера), SQLite-локов хватает.
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
	store, err := db.Open(cfg.DBPath)
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
	fmt.Printf("web listening on %s (db=%s)\n", cfg.WebListen, cfg.DBPath)
	log.Fatal(http.ListenAndServe(cfg.WebListen, srv.Routes()))
}
