// ИИ-Диктофон Дневника — Telegram-бот на Go (voice-first, текст равноправен).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"diarybot/internal/config"
	"diarybot/internal/db"
	"diarybot/internal/handlers"
	"diarybot/internal/scheduler"

	"github.com/go-telegram/bot"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cfg := config.Load()
	if cfg.BotToken == "" || strings.Contains(cfg.BotToken, "put-your") {
		fmt.Fprintln(os.Stderr, "Впиши BOT_TOKEN в .env (см. .env.example)")
		os.Exit(1)
	}
	if cfg.OpenAIKey == "" {
		// Разбор мыслей — только через LLM API, heuristic fallback убран.
		fmt.Fprintln(os.Stderr, "Впиши OPENAI_API_KEY в .env — без него разбор не работает")
		os.Exit(1)
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer store.Close()

	app := handlers.NewApp(cfg, store)
	b, err := bot.New(cfg.BotToken, bot.WithDefaultHandler(app.Handle))
	if err != nil {
		log.Fatalf("bot init: %v", err)
	}
	app.Bot = b

	if _, err := scheduler.Start(ctx, b, cfg, store); err != nil {
		log.Fatalf("scheduler: %v", err)
	}
	fmt.Printf("Bot started in GPT+Whisper mode, db=%s\n", cfg.DBPath)
	b.Start(ctx)
}
