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

	// Диспетчер дайджестов: каждую минуту сверяем ЛОКАЛЬНЫЙ час каждого
	// пользователя с его настройками. Флаг last_*_day в users защищает от
	// повторов и заодно чинит пропуск при рестарте после часа X.
	dispatchDigests := func() {
		users, _ := store.DistinctUsers()
		for _, u := range users {
			ulo := services.UserLoc(store, u, cfg.TZ)
			now := time.Now().In(ulo)
			usr, err := store.GetUser(u, cfg.TZ)
			if err != nil {
				continue
			}
			day := now.Format("2006-01-02")
			if due, _ := services.DigestDue(now, cfg.MorningHour, usr.LastMorningDay); due {
				blocks, _ := store.DayBlocks(u, day)
				tasks, _ := store.OpenTasks(u)
				plansToday, _ := store.PlansForDay(u, day, true)
				text := fmt.Sprintf("☀️ Доброе утро!\n\n%s\n\nСегодня уже %d записей.\n\n%s\n\nНаговори или напиши планы на день 🎙",
					services.RenderPlans(plansToday), len(blocks), services.RenderTasks(tasks))
				if _, err := b.SendMessage(context.Background(), &bot.SendMessageParams{
					ChatID: u, Text: text, ParseMode: "HTML",
				}); err == nil {
					_ = store.MarkDigest(u, "morning", day)
				}
				// Отчёт за вчера: сверка планов с фактом (1 LLM-вызов, только если было что сверять).
				// Отдельным plain-сообщением: в отчёте может быть сырой "<", роняющий HTML-парсинг.
				yday := now.AddDate(0, 0, -1).Format("2006-01-02")
				if report, err := services.ReconcileDay(context.Background(), cfg, store, u, yday); err == nil && report != "" {
					_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
						ChatID: u, Text: "📊 Итоги вчера (" + yday + "):\n\n" + report,
					})
				}
			}
			if due, _ := services.DigestDue(now, cfg.EveningHour, usr.LastEveningDay); due {
				blocks, _ := store.DayBlocks(u, day)
				tasks, _ := store.OpenTasks(u)
				text := fmt.Sprintf("🌙 Вечер. Сегодня %d записей, открытых задач: %d.\nЧто было главным? Наговори или напиши 1–2 минуты — я сохраню как рефлексию дня.\n\nКакая энергия сегодня? Жми кнопку 👇",
					len(blocks), len(tasks))
				if _, err := b.SendMessage(context.Background(), &bot.SendMessageParams{
					ChatID: u, Text: text, ParseMode: "HTML",
					ReplyMarkup: services.EnergyKeyboard(day),
				}); err == nil {
					_ = store.MarkDigest(u, "evening", day)
				}
			}
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

	if _, err := c.AddFunc("* * * * *", dispatchDigests); err != nil {
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
