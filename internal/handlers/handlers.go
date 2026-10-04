package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"diarybot/internal/config"
	"diarybot/internal/db"
	"diarybot/internal/services"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type App struct {
	Cfg   config.Config
	Store *db.Store
	Sess  *db.Store // sessions.db: веб-сессии (боту принадлежит на запись)
	Bot   *bot.Bot

	mu          sync.Mutex
	pendingEdit map[int64]int64 // userID → entryID: ждём исправленный текст
	lastLink    map[int64]time.Time
	analysisMsg map[int]msgRef // bot messageID → запись (для правки reply)
}

type msgRef struct {
	entryID int64
	at      time.Time
}

// NewApp конструирует App (карты требуют инициализации).
func NewApp(cfg config.Config, store, sess *db.Store) *App {
	return &App{Cfg: cfg, Store: store, Sess: sess,
		pendingEdit: map[int64]int64{}, lastLink: map[int64]time.Time{},
		analysisMsg: map[int]msgRef{}}
}

// rememberAnalysis связывает сообщение-разбор с записью (reply-to-edit).
// Чистим старше 7 дней — карта только для свежих разборов.
func (a *App) rememberAnalysis(msgID int, entryID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -7)
	for id, ref := range a.analysisMsg {
		if ref.at.Before(cutoff) {
			delete(a.analysisMsg, id)
		}
	}
	a.analysisMsg[msgID] = msgRef{entryID: entryID, at: time.Now()}
}

func (a *App) lookupAnalysisMsg(msgID int) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref, ok := a.analysisMsg[msgID]
	if !ok {
		return 0, false
	}
	return ref.entryID, true
}

func (a *App) takePendingEdit(userID int64) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.pendingEdit[userID]
	if ok {
		delete(a.pendingEdit, userID)
	}
	return id, ok
}

func (a *App) setPendingEdit(userID, entryID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingEdit[userID] = entryID
}

func (a *App) clearPendingEdit(userID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pendingEdit, userID)
}

// userNow — «сейчас» по персональной зоне пользователя (дефолт из конфига).
func (a *App) userNow(userID int64) time.Time {
	return time.Now().In(services.UserLoc(a.Store, userID, a.Cfg.TZ))
}

const helpText = `🎙 <b>ИИ-Диктофон Дневника</b> (voice-first, текст тоже ок)

Каждый день излагай мысли <b>голосом или текстом</b> — как удобно. Голос — основной способ, текст — равноправный. Я сам веду журнал: заметки, сущности, 📌 планы, ✅ задачи, ⏰ напоминания.

/today — сегодня
/day [ГГГГ-ММ-ДД] — любой день
/notes — последние заметки
/week — 7 дней
/tasks — открытые задачи
/done &lt;id&gt; — закрыть задачу
/plans — планы по дням
/plandone &lt;id&gt; — закрыть план
/ideas — банк идей
/energy [1–10] — записать энергию дня
/person &lt;имя&gt; — всё про человека
/project &lt;тег&gt; — всё про проект
/delegations — карточки для внешнего агента
/search &lt;запрос&gt; — поиск
/export [ГГГГ-ММ] — Markdown дневника за месяц файлом
/delete_day [ГГГГ-ММ-ДД] — удалить день (по умолчанию сегодня)
/link — ссылка на веб-версию дневника
/revoke — отозвать все веб-сессии
/tz [Зона] — часовой пояс (по умолчанию Europe/Moscow)
/help — это сообщение

Под каждым разбором кнопки: ✅ в задачу, ✏️ исправить текст, 🗑 удалить запись. Исправить можно и проще: ответь на разбор (reply) исправленным текстом или голосом.

<i>Чтобы что-то поручить агенту-исполнителю, напиши или скажи: «поручи агенту …» — я сохраню карточку, исполнение подключится позже плагином.</i>`

var entityEmoji = map[string]string{
	"person": "👥", "project": "#️⃣", "place": "📍",
	"date": "📅", "money": "💰", "promise": "🤝",
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (a *App) send(ctx context.Context, chatID int64, text string) {
	_, _ = a.Bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	})
}

// sendLong режет длинные тексты на чанки ≤4000 символов по границам строк
// (лимит Telegram — 4096). parseMode "" = plain text.
func (a *App) sendLong(ctx context.Context, chatID int64, text string, parseMode models.ParseMode) {
	const limit = 4000
	var chunks []string
	for len([]rune(text)) > limit {
		runes := []rune(text)
		cut := limit
		for i := limit; i > limit-500 && i > 0; i-- {
			if runes[i] == '\n' {
				cut = i
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		text = strings.TrimLeft(string(runes[cut:]), "\n")
	}
	chunks = append(chunks, text)
	for _, c := range chunks {
		_, _ = a.Bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: c, ParseMode: parseMode})
	}
}

func renderResult(prefix string, res *services.IngestResult) string {
	var b strings.Builder
	b.WriteString(prefix)
	if res.Day != "" && res.Day != services.TodayStr() {
		fmt.Fprintf(&b, "\n📅 Записал за %s", res.Day)
	}
	if len(res.Blocks) > 0 {
		b.WriteString("\n\n" + services.RenderBlocks(res.Blocks))
	}
	if len(res.Entities) > 0 {
		n := len(res.Entities)
		if n > 8 {
			n = 8
		}
		parts := make([]string, 0, n)
		for _, e := range res.Entities[:n] {
			em := entityEmoji[e.Type]
			if em == "" {
				em = "•"
			}
			parts = append(parts, em+e.Value)
		}
		b.WriteString("\n\n🔎 Сущности: " + strings.Join(parts, " "))
	}
	if len(res.TaskIDs) > 0 {
		fmt.Fprintf(&b, "\n\n✅ В задачи: %d (см. /tasks)", len(res.TaskIDs))
	}
	if len(res.PlanIDs) > 0 {
		fmt.Fprintf(&b, "\n📌 В планы: %d (см. /plans)", len(res.PlanIDs))
	}
	if res.Reminder != nil {
		fmt.Fprintf(&b, "\n⏰ Напомню: %s", res.Reminder.FireAt.Format("02.01 в 15:04"))
	}
	if res.Delegation != nil {
		d := res.Delegation
		title := d.Title
		if len([]rune(title)) > 100 {
			title = string([]rune(title)[:100])
		}
		fmt.Fprintf(&b, "\n\n🤖 Похоже, это можно <b>поручить агенту</b> (%s): «%s»\nКарточка #%d сохранена (статус proposed, исполнения пока нет — агенты подключатся позже). Список: /delegations",
			d.Agent, title, d.ID)
	}
	return b.String()
}

// ingestErrText: ошибка разбора LLM — не то же самое, что потеря данных.
// Сырьё уже лежит в entries, поэтому честно говорим об этом пользователю.
func ingestErrText(err error) string {
	if errors.Is(err, services.ErrAnalyze) {
		return "💾 Заметка сохранена, но разобрать её через LLM не получилось: " + err.Error()
	}
	return "❌ Ошибка сохранения: " + err.Error()
}

func (a *App) sendButtons(ctx context.Context, chatID int64, text string, entryID int64) {
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ В задачу", CallbackData: fmt.Sprintf("task:%d", entryID)},
		{Text: "✏️ Исправить", CallbackData: fmt.Sprintf("edit:%d", entryID)},
		{Text: "🗑 Удалить", CallbackData: fmt.Sprintf("del:%d", entryID)},
	}}}
	msg, err := a.Bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: kb,
	})
	if err == nil && msg != nil {
		a.rememberAnalysis(msg.ID, entryID)
	}
}

func (a *App) Handle(ctx context.Context, b *bot.Bot, u *models.Update) {
	if u.CallbackQuery != nil {
		a.handleCallback(ctx, u.CallbackQuery)
		return
	}
	msg := u.Message
	if msg == nil {
		return
	}
	chatID := msg.Chat.ID
	userID := msg.From.ID
	text := strings.TrimSpace(msg.Text)

	// голос / аудио / кружок
	fileID := ""
	kind := ""
	if msg.Voice != nil {
		fileID, kind = msg.Voice.FileID, "voice"
	} else if msg.Audio != nil {
		fileID, kind = msg.Audio.FileID, "voice"
	} else if msg.VideoNote != nil {
		fileID, kind = msg.VideoNote.FileID, "voice"
	}
	// Reply-to-edit: ответ на разбор (текстом или голосом) = исправление,
	// копировать расшифровку не нужно. Команды идут обычным путём.
	if replyTo := msg.ReplyToMessage; replyTo != nil && !strings.HasPrefix(text, "/") {
		if entryID, ok := a.lookupAnalysisMsg(replyTo.ID); ok {
			a.clearPendingEdit(userID)
			corrected := text
			if fileID != "" {
				tr, err := a.downloadAndTranscribe(ctx, fileID)
				if err != nil || tr == "" {
					a.send(ctx, chatID, "🎙 Не смог расшифровать исправление. Пришли текстом.")
					return
				}
				corrected = tr
			}
			if strings.TrimSpace(corrected) == "" {
				return
			}
			res, err := services.ReanalyzeEntry(ctx, a.Cfg, a.Store, userID, entryID, corrected)
			if err != nil {
				if errors.Is(err, db.ErrNotFound) {
					a.send(ctx, chatID, "Запись уже удалена — исправлять нечего.")
				} else {
					a.send(ctx, chatID, "💾 Исправленный текст сохранён, но разобрать через LLM не получилось: "+err.Error())
				}
				return
			}
			a.sendButtons(ctx, chatID, renderResult("✏️ Переразобрал:", res), res.EntryID)
			return
		}
	}
	if fileID != "" {
		a.clearPendingEdit(userID) // голосовое — всегда новая запись
		transcript, err := a.downloadAndTranscribe(ctx, fileID)
		if err != nil || transcript == "" {
			a.send(ctx, chatID, "🎙 Получил голосовое, но расшифровать не смог (ошибка STT).\nПришли то же текстом — разберу через LLM.")
			return
		}
		res, err := services.IngestText(ctx, a.Cfg, a.Store, userID, kind, "", transcript)
		if err != nil {
			a.send(ctx, chatID, ingestErrText(err))
			return
		}
		preview := transcript
		if len([]rune(preview)) > 500 {
			preview = string([]rune(preview)[:500])
		}
		a.sendButtons(ctx, chatID, renderResult("🎧 <b>Расшифровка:</b> "+preview, res), res.EntryID)
		return
	}

	if strings.HasPrefix(text, "/") {
		a.clearPendingEdit(userID) // команда отменяет ожидание исправления
		a.handleCommand(ctx, chatID, userID, text)
		return
	}
	if text == "" {
		return
	}
	if entryID, ok := a.takePendingEdit(userID); ok {
		res, err := services.ReanalyzeEntry(ctx, a.Cfg, a.Store, userID, entryID, text)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				a.send(ctx, chatID, "Запись уже удалена — исправлять нечего.")
			} else {
				a.send(ctx, chatID, "💾 Исправленный текст сохранён, но разобрать через LLM не получилось: "+err.Error())
			}
			return
		}
		a.sendButtons(ctx, chatID, renderResult("✏️ Переразобрал:", res), res.EntryID)
		return
	}
	res, err := services.IngestText(ctx, a.Cfg, a.Store, userID, "text", text, text)
	if err != nil {
		a.send(ctx, chatID, ingestErrText(err))
		return
	}
	a.sendButtons(ctx, chatID, renderResult("📝 Разложил:", res), res.EntryID)
}

// handleCallback — кнопки под разбором, энергия и подтверждения удаления.
// Форматы: "действие:entryID" либо "energy:ГГГГ-ММ-ДД:1-10".
// Спиннер гасим всегда; для энергии — тостом с результатом.
// tzZones — популярные зоны для кнопок; любую IANA-зону можно вписать текстом: /tz Asia/Almaty.
var tzZones = [][2]string{
	{"Калининград", "Europe/Kaliningrad"}, {"Москва", "Europe/Moscow"},
	{"Самара", "Europe/Samara"}, {"Екатеринбург", "Asia/Yekaterinburg"},
	{"Омск", "Asia/Omsk"}, {"Новосибирск", "Asia/Novosibirsk"},
	{"Владивосток", "Asia/Vladivostok"},
}

func (a *App) sendTZQuestion(ctx context.Context, chatID int64) {
	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for i, z := range tzZones {
		row = append(row, models.InlineKeyboardButton{Text: z[0], CallbackData: "tz:" + z[1]})
		if i%3 == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	_, _ = a.Bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "🌍 Какой у тебя часовой пояс? От него зависят утро/вечер и время напоминаний.\nМожно и текстом: /tz Asia/Almaty",
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
}

func (a *App) handleCallback(ctx context.Context, cb *models.CallbackQuery) {
	userID := cb.From.ID
	if act, rest, _ := strings.Cut(cb.Data, ":"); act == "tz" {
		zone := rest
		if _, err := time.LoadLocation(zone); err != nil {
			_, _ = a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: cb.ID, Text: "Не знаю такую зону",
			})
			return
		}
		if err := a.Store.SetUserTZ(userID, zone); err != nil {
			_, _ = a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: cb.ID, Text: "Не сохранилось",
			})
			return
		}
		_, _ = a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: cb.ID, Text: "Зона: " + zone + " ✓",
		})
		if cb.Message.Message != nil {
			_, _ = a.Bot.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: cb.Message.Message.Chat.ID,
				Text:   "🌍 Часовой пояс: " + zone + ". Утро/вечер и напоминания теперь по нему.",
			})
		}
		return
	}
	if act, rest, _ := strings.Cut(cb.Data, ":"); act == "energy" {
		day, vstr, _ := strings.Cut(rest, ":")
		v, err := strconv.Atoi(vstr)
		if err != nil || v < 1 || v > 10 || !dayRe.MatchString(day) {
			_, _ = a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: cb.ID, Text: "Так не бывает 🙂 Жми 1–10.",
			})
			return
		}
		if err := a.Store.SetEnergy(userID, day, v); err != nil {
			_, _ = a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: cb.ID, Text: "Не сохранилось: " + err.Error(),
			})
			return
		}
		_, _ = a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: cb.ID, Text: fmt.Sprintf("Энергия %s: %d/10 ✓", day, v),
		})
		return
	}
	_, _ = a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cb.ID})
	if cb.Message.Message == nil {
		return
	}
	chatID := cb.Message.Message.Chat.ID
	msgID := cb.Message.Message.ID
	act, idStr, _ := strings.Cut(cb.Data, ":")
	entryID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || entryID <= 0 {
		return
	}

	switch act {
	case "task":
		if _, err := a.Store.EntryDay(userID, entryID); err != nil {
			a.send(ctx, chatID, "Запись уже удалена.")
			return
		}
		if open, _ := a.Store.OpenTasksByEntry(entryID); len(open) > 0 {
			ids := make([]string, 0, len(open))
			for _, t := range open {
				ids = append(ids, fmt.Sprintf("#%d", t.ID))
			}
			a.send(ctx, chatID, "Уже в задачах: "+strings.Join(ids, ", "))
			return
		}
		text, ok, err := a.Store.EntryText(userID, entryID)
		if err != nil || !ok || strings.TrimSpace(text) == "" {
			a.send(ctx, chatID, "Запись уже удалена.")
			return
		}
		tid, err := a.Store.AddTask(userID, entryID, text, services.GuessDue(text), "")
		if err != nil {
			a.send(ctx, chatID, "❌ Не получилось создать задачу: "+err.Error())
			return
		}
		a.send(ctx, chatID, fmt.Sprintf("✅ Взял в задачи #%d (см. /tasks)", tid))

	case "edit":
		day, err := a.Store.EntryDay(userID, entryID)
		if err != nil {
			a.send(ctx, chatID, "Запись уже удалена — исправлять нечего.")
			return
		}
		a.setPendingEdit(userID, entryID)
		a.send(ctx, chatID, fmt.Sprintf("✏️ Пришли исправленный текст одним сообщением (запись за %s).", day))

	case "del":
		if _, err := a.Store.EntryDay(userID, entryID); err != nil {
			a.send(ctx, chatID, "Запись уже удалена.")
			return
		}
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "Да, удалить", CallbackData: fmt.Sprintf("del_yes:%d", entryID)},
			{Text: "Отмена", CallbackData: fmt.Sprintf("del_no:%d", entryID)},
		}}}
		_, _ = a.Bot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        fmt.Sprintf("Удалить запись #%d? Разбор, задачи и несработавшие напоминания тоже удалятся.", entryID),
			ReplyMarkup: kb,
		})

	case "del_yes":
		ok, err := a.Store.DeleteEntry(userID, entryID)
		if err != nil {
			a.send(ctx, chatID, "❌ Не получилось удалить: "+err.Error())
			return
		}
		text := "🗑 Запись удалена."
		if !ok {
			text = "Запись уже удалена."
		}
		_, _ = a.Bot.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: text,
		})

	case "del_no":
		_, _ = a.Bot.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
	}
}

func (a *App) downloadAndTranscribe(ctx context.Context, fileID string) (string, error) {
	f, err := a.Bot.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", err
	}
	link := a.Bot.FileDownloadLink(f)
	req, _ := http.NewRequestWithContext(ctx, "GET", link, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp("", "voice-*.ogg")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	_, err = io.Copy(tmp, resp.Body)
	tmp.Close()
	if err != nil {
		os.Remove(path)
		return "", err
	}
	defer os.Remove(path)
	return services.TranscribeFile(ctx, path, a.Cfg.OpenAIKey, a.Cfg.OpenAIBaseURL, a.Cfg.STTModel), nil
}

func (a *App) handleCommand(ctx context.Context, chatID, userID int64, text string) {
	parts := strings.Fields(text)
	cmd := strings.Split(parts[0], "@")[0]
	arg := ""
	if i := strings.Index(text, " "); i >= 0 {
		arg = strings.TrimSpace(text[i+1:])
	}
	switch cmd {
	case "/start", "/help":
		a.send(ctx, chatID, helpText)
		if cmd == "/start" {
			if u, err := a.Store.GetUser(userID, a.Cfg.TZ); err == nil && !u.TZSet {
				a.sendTZQuestion(ctx, chatID)
			}
		}
	case "/tz":
		if arg == "" {
			a.sendTZQuestion(ctx, chatID)
			return
		}
		if _, err := time.LoadLocation(arg); err != nil {
			a.send(ctx, chatID, "Не знаю такую зону. Пример: /tz Asia/Almaty (или выбери кнопкой: /tz)")
			return
		}
		if err := a.Store.SetUserTZ(userID, arg); err != nil {
			a.send(ctx, chatID, "❌ Не сохранилось: "+err.Error())
			return
		}
		a.send(ctx, chatID, "🌍 Часовой пояс: "+arg+". Утро/вечер и напоминания теперь по нему.")
	case "/today":
		day := a.userNow(userID).Format("2006-01-02")
		blocks, _ := a.Store.DayBlocks(userID, day)
		a.send(ctx, chatID, services.RenderDay(day, blocks))
	case "/day":
		day := arg
		if day == "" {
			day = a.userNow(userID).Format("2006-01-02")
		}
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(day) {
			a.send(ctx, chatID, "Использование: /day [ГГГГ-ММ-ДД], например /day 2026-10-03")
			return
		}
		blocks, _ := a.Store.DayBlocks(userID, day)
		a.send(ctx, chatID, services.RenderDay(day, blocks))
	case "/notes":
		notes, _ := a.Store.RecentNotes(userID, 10)
		if len(notes) == 0 {
			a.send(ctx, chatID, "📝 Заметок пока нет. Наговори первое голосовое или напиши текст 🎙")
			return
		}
		var b strings.Builder
		b.WriteString("📝 <b>Последние заметки:</b>\n")
		for _, n := range notes {
			preview := n.Transcript
			if preview == "" {
				preview = "(без расшифровки)"
			}
			if len([]rune(preview)) > 120 {
				preview = string([]rune(preview)[:120])
			}
			fmt.Fprintf(&b, "#%d <i>%s</i> [%s] %s\n", n.ID, n.Day, n.Kind, preview)
		}
		a.send(ctx, chatID, b.String())
	case "/week":
		var days []string
		unow := a.userNow(userID)
		for i := 6; i >= 0; i-- {
			days = append(days, unow.AddDate(0, 0, -i).Format("2006-01-02"))
		}
		rows, _ := a.Store.WeekBlocks(userID, days)
		if len(rows) == 0 {
			a.send(ctx, chatID, "📅 За неделю пусто. Начни с одного голосового или текста сегодня.")
			return
		}
		counts := map[string]int{}
		for _, r := range rows {
			counts[r.Aspect]++
		}
		var b strings.Builder
		fmt.Fprintf(&b, "📅 <b>Неделя</b>: %d блоков\n", len(rows))
		for asp, n := range counts {
			fmt.Fprintf(&b, "• %s: %d\n", services.AspectName(asp), n)
		}
		if energy, _ := a.Store.WeekEnergy(userID, days); len(energy) > 0 {
			bars, nums := services.EnergySparkline(days, energy)
			fmt.Fprintf(&b, "\n❤️ Энергия: %s\n<i>%s</i>", bars, nums)
		}
		a.send(ctx, chatID, b.String())
		// Нарративное ревью одним LLM-вызовом; при ошибке — молча пропускаем,
		// статистика выше уже отдана. Ревью — всегда plain text: LLM может
		// вернуть сырой "<", а это роняет parse_mode=HTML.
		if review, err := services.BuildWeeklyReview(ctx, a.Cfg, a.Store, userID); err == nil {
			a.send(ctx, chatID, "📝 <b>Итоги недели:</b>")
			a.sendLong(ctx, chatID, review, "")
		}
	case "/energy":
		day := a.userNow(userID).Format("2006-01-02")
		if arg == "" {
			_, _ = a.Bot.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID, Text: "Какая энергия сегодня? Жми 👇",
				ReplyMarkup: services.EnergyKeyboard(day),
			})
			return
		}
		v, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil || v < 1 || v > 10 {
			a.send(ctx, chatID, "Использование: /energy [1–10]")
			return
		}
		if err := a.Store.SetEnergy(userID, day, v); err != nil {
			a.send(ctx, chatID, "❌ Не сохранилось: "+err.Error())
			return
		}
		a.send(ctx, chatID, fmt.Sprintf("❤️ Записал энергию: %d/10", v))
	case "/tasks":
		tasks, _ := a.Store.OpenTasks(userID)
		a.send(ctx, chatID, services.RenderTasks(tasks))
	case "/plans":
		plans, _ := a.Store.OpenPlans(userID, 30)
		if len(plans) == 0 {
			a.send(ctx, chatID, "📌 Открытых планов нет. Расскажи вечером, что хочешь завтра, — утром напомню.")
			return
		}
		var b strings.Builder
		b.WriteString("📌 <b>Планы:</b>\n")
		lastDay := ""
		for _, p := range plans {
			if p.TargetDay != lastDay {
				fmt.Fprintf(&b, "\n<i>%s</i>\n", p.TargetDay)
				lastDay = p.TargetDay
			}
			fmt.Fprintf(&b, "#%d — %s\n", p.ID, p.Text)
		}
		b.WriteString("\nЗакрыть вручную: /plandone &lt;id&gt;")
		a.send(ctx, chatID, b.String())
	case "/plandone":
		if len(parts) < 2 {
			a.send(ctx, chatID, "Использование: /plandone &lt;id&gt; (см. /plans)")
			return
		}
		pid, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			a.send(ctx, chatID, "Использование: /plandone &lt;id&gt; (см. /plans)")
			return
		}
		ok, _ := a.Store.ClosePlan(userID, pid)
		if ok {
			a.send(ctx, chatID, "📌 План закрыт!")
		} else {
			a.send(ctx, chatID, "❌ Не нашёл такой план.")
		}
	case "/done":
		if len(parts) < 2 {
			a.send(ctx, chatID, "Использование: /done &lt;id&gt; (см. /tasks)")
			return
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			a.send(ctx, chatID, "Использование: /done &lt;id&gt; (см. /tasks)")
			return
		}
		ok, _ := a.Store.CloseTask(userID, id)
		if ok {
			a.send(ctx, chatID, "✅ Закрыта!")
		} else {
			a.send(ctx, chatID, "❌ Не нашёл такую задачу.")
		}
	case "/delete_day":
		day := arg
		if day == "" {
			day = a.userNow(userID).Format("2006-01-02")
		}
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(day) {
			a.send(ctx, chatID, "Использование: /delete_day [ГГГГ-ММ-ДД], например /delete_day 2026-10-03")
			return
		}
		n, err := a.Store.DeleteDay(userID, day)
		if err != nil {
			a.send(ctx, chatID, "❌ Не получилось удалить: "+err.Error())
			return
		}
		if n == 0 {
			a.send(ctx, chatID, "📭 В этот день ("+day+") записей нет.")
			return
		}
		a.send(ctx, chatID, fmt.Sprintf("🗑 День %s удалён: записей %d (разборы, открытые задачи и несработавшие напоминания — тоже).", day, n))
	case "/ideas":
		rows, _ := a.Store.BlocksByAspect(userID, "idea", 20)
		if len(rows) == 0 {
			a.send(ctx, chatID, "💡 Идей пока нет. Поделись первой!")
			return
		}
		var b strings.Builder
		b.WriteString("💡 <b>Банк идей:</b>\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "<i>%s</i> — %s\n", r.Day, r.Content)
		}
		a.send(ctx, chatID, b.String())
	case "/person", "/project":
		if arg == "" {
			a.send(ctx, chatID, "Использование: "+cmd+" &lt;имя&gt; — например, "+cmd+" Маша")
			return
		}
		etype, icon := "person", "👥"
		if cmd == "/project" {
			etype, icon = "project", "#️⃣"
		}
		hits, values, err := services.EntityHistory(a.Store, userID, etype, arg, 10)
		if err != nil {
			a.send(ctx, chatID, "❌ Ошибка поиска: "+err.Error())
			return
		}
		if len(hits) == 0 {
			a.send(ctx, chatID, icon+" Про «"+arg+"» пока ничего нет. Упомяни в записи — и я начну собирать историю.")
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s <b>%s</b> — записей: %d", icon, arg, len(hits))
		if len(values) > 0 {
			fmt.Fprintf(&b, " <i>(нашёл как: %s)</i>", strings.Join(values, ", "))
		}
		b.WriteString("\n")
		for _, h := range hits {
			fmt.Fprintf(&b, "\n📅 <i>%s</i>\n", h.Day)
			for _, bl := range h.Blocks {
				c := bl.Content
				if len([]rune(c)) > 150 {
					c = string([]rune(c)[:150])
				}
				fmt.Fprintf(&b, "%s [%s] %s\n", services.EmojiFor(bl.Aspect), services.AspectName(bl.Aspect), c)
			}
		}
		a.sendLong(ctx, chatID, b.String(), models.ParseModeHTML)
	case "/delegations":
		rows, _ := a.Store.ListDelegations(userID, 20)
		if len(rows) == 0 {
			a.send(ctx, chatID, "🤖 Карточек делегирования нет.\nСкажи или напиши «поручи агенту …» — я сохраню карточку здесь. Реальное исполнение подключится позже отдельным плагином.")
			return
		}
		var b strings.Builder
		b.WriteString("🤖 <b>Делегирование (только карточки, исполнения нет):</b>\n")
		for _, r := range rows {
			title := r.Title
			if len([]rune(title)) > 120 {
				title = string([]rune(title)[:120])
			}
			fmt.Fprintf(&b, "#%d [%s/%s] %s\n", r.ID, r.Status, r.Agent, title)
		}
		a.send(ctx, chatID, b.String())
	case "/export":
		month := arg
		if month == "" {
			month = a.userNow(userID).Format("2006-01")
		}
		if !regexp.MustCompile(`^\d{4}-\d{2}$`).MatchString(month) {
			a.send(ctx, chatID, "Использование: /export [ГГГГ-ММ], например /export 2026-10")
			return
		}
		notes, err := a.Store.ExportMonth(userID, month)
		if err != nil {
			a.send(ctx, chatID, "❌ Ошибка экспорта: "+err.Error())
			return
		}
		if len(notes) == 0 {
			a.send(ctx, chatID, "📭 За "+month+" записей нет.")
			return
		}
		doc := services.RenderExportMarkdown(month, notes)
		_, _ = a.Bot.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID:   chatID,
			Document: &models.InputFileUpload{Filename: "diary-" + month + ".md", Data: bytes.NewReader([]byte(doc))},
			Caption:  fmt.Sprintf("📦 Дневник за %s: заметок %d", month, len(notes)),
		})
	case "/link":
		a.mu.Lock()
		last, seen := a.lastLink[userID]
		if seen && time.Since(last) < time.Minute {
			a.mu.Unlock()
			a.send(ctx, chatID, "⏳ Ссылку можно просить не чаще раза в минуту. Подожди немного.")
			return
		}
		a.lastLink[userID] = time.Now()
		a.mu.Unlock()
		token, err := a.Sess.CreateWebToken(userID, 15*time.Minute)
		if err != nil {
			a.send(ctx, chatID, "❌ Не получилось выпустить ссылку: "+err.Error())
			return
		}
		// Превью ссылки ВЫКЛЮЧЕНО: клиент для превью ходит по URL и тем
		// самым палит одноразовость (ловили 401 сразу после выдачи).
		noPreview := true
		_, _ = a.Bot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "🔗 Твоя ссылка на веб-дневник (одноразовая, 15 минут):\n" + a.Cfg.WebBaseURL + "/r/" + token,
			LinkPreviewOptions: &models.LinkPreviewOptions{
				IsDisabled: &noPreview,
			},
		})
	case "/revoke":
		if err := a.Sess.RevokeWebSessions(userID); err != nil {
			a.send(ctx, chatID, "❌ Не получилось отозвать: "+err.Error())
			return
		}
		a.send(ctx, chatID, "🔒 Все веб-сессии отозваны. Новая ссылка — по /link.")
	case "/search":
		if arg == "" {
			a.send(ctx, chatID, "Использование: /search &lt;запрос&gt;")
			return
		}
		rows, _ := a.Store.SearchBlocks(userID, arg, 20)
		if len(rows) == 0 {
			a.send(ctx, chatID, "🔍 По «"+arg+"» ничего не нашёл.")
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "🔍 <b>%s</b>:\n", arg)
		for _, r := range rows {
			c := r.Content
			if len([]rune(c)) > 150 {
				c = string([]rune(c)[:150])
			}
			fmt.Fprintf(&b, "<i>%s</i> [%s] %s\n", r.Day, services.AspectName(r.Aspect), c)
		}
		a.send(ctx, chatID, b.String())
	default:
		a.send(ctx, chatID, helpText)
	}
}
