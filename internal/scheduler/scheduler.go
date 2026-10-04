package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"diarybot/internal/config"
	"diarybot/internal/db"
	"diarybot/internal/services"

	"github.com/go-telegram/bot"
	"github.com/robfig/cron/v3"
)

const backupKeepDays = 14

func Start(ctx context.Context, b *bot.Bot, cfg config.Config, store *db.Store) (*cron.Cron, error) {
	loc, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		loc = time.Local
	}
	c := cron.New(cron.WithLocation(loc))

	morning := func() {
		users, _ := store.DistinctUsers()
		day := time.Now().In(loc).Format("2006-01-02")
		for _, u := range users {
			blocks, _ := store.DayBlocks(u, day)
			tasks, _ := store.OpenTasks(u)
			text := fmt.Sprintf("☀️ Доброе утро! Сегодня уже %d записей.\n\n%s\n\nНаговори или напиши планы на день 🎙",
				len(blocks), services.RenderTasks(tasks))
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: u, Text: text, ParseMode: "HTML",
			})
		}
	}
	evening := func() {
		users, _ := store.DistinctUsers()
		day := time.Now().In(loc).Format("2006-01-02")
		for _, u := range users {
			blocks, _ := store.DayBlocks(u, day)
			tasks, _ := store.OpenTasks(u)
			text := fmt.Sprintf("🌙 Вечер. Сегодня %d записей, открытых задач: %d.\nЧто было главным? Наговори или напиши 1–2 минуты — я сохраню как рефлексию дня.\n\nКакая энергия сегодня? Жми кнопку 👇",
				len(blocks), len(tasks))
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: u, Text: text, ParseMode: "HTML",
				ReplyMarkup: services.EnergyKeyboard(day),
			})
		}
	}

	checkReminders := func() {
		for _, r := range mustDue(store) {
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: r.UserID, Text: "⏰ Напоминание: " + r.Text, ParseMode: "HTML",
			})
			_ = store.MarkReminderSent(r.ID)
		}
	}

	if _, err := c.AddFunc(fmt.Sprintf("0 %d * * *", cfg.MorningHour), morning); err != nil {
		return nil, err
	}
	if _, err := c.AddFunc(fmt.Sprintf("0 %d * * *", cfg.EveningHour), evening); err != nil {
		return nil, err
	}
	if _, err := c.AddFunc("* * * * *", checkReminders); err != nil {
		return nil, err
	}
	// Ежедневный бэкап живой БД в 3:00. VACUUM INTO даёт консистентный
	// снапшот без остановки бота; храним последние backupKeepDays штук.
	if _, err := c.AddFunc("0 3 * * *", func() { DailyBackup(store, "data/backups") }); err != nil {
		return nil, err
	}
	c.Start()
	go func() {
		<-ctx.Done()
		c.Stop()
	}()
	return c, nil
}

// mustDue — due-напоминания без проброса ошибки: тихий пропуск при сбое БД,
// следующая минутная проверка повторит попытку.
func mustDue(store *db.Store) []db.Reminder {
	out, err := store.DueReminders(time.Now())
	if err != nil {
		return nil
	}
	return out
}

// DailyBackup — снапшот БД + ротация (оставляет backupKeepDays последних).
// Ошибки только логируются в stdout systemd — бот из-за бэкапа не падает.
func DailyBackup(store *db.Store, dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Println("backup mkdir:", err)
		return
	}
	name := "diary-" + time.Now().Format("20060102") + ".db"
	if err := store.VacuumInto(filepath.Join(dir, name)); err != nil {
		fmt.Println("backup vacuum:", err)
		return
	}
	cutoff := time.Now().AddDate(0, 0, -backupKeepDays).Format("20060102")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if len(n) != len("diary-20060102.db") || n[:6] != "diary-" || n[len(n)-3:] != ".db" {
			continue
		}
		if d := n[6 : len(n)-3]; d < cutoff {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	fmt.Println("backup done:", name)
}
