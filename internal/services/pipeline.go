package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"diarybot/internal/config"
	"diarybot/internal/db"
)

var dueRe = regexp.MustCompile(`(?i)(завтра|сегодня|в понедельник|во вторник|в среду|в четверг|в пятницу|утром|вечером|к \d+|до \d+)`)

func TodayStr() string { return time.Now().Format("2006-01-02") }

// PastStrs возвращает даты вчера и позавчера для промпта (без математики на стороне LLM).
func PastStrs() (yesterday, dayBefore string) {
	now := time.Now()
	return now.AddDate(0, 0, -1).Format("2006-01-02"), now.AddDate(0, 0, -2).Format("2006-01-02")
}

// ResolveEntryDate проверяет entry_date от LLM: только прошлое (не старше года)
// и не будущее. Пусто/мусор/будущее → "" (= сегодня).
func ResolveEntryDate(dateStr string, now time.Time) string {
	d, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return ""
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if d.After(today) || d.Before(today.AddDate(-1, 0, 0)) {
		return ""
	}
	if d.Equal(today) {
		return ""
	}
	return d.Format("2006-01-02")
}

func GuessDue(content string) string {
	m := dueRe.FindString(content)
	return m
}

type IngestResult struct {
	EntryID    int64
	Day        string // дата, к которой отнесена запись (обычно сегодня)
	Blocks     []db.Block
	Entities   []db.Entity
	TaskIDs    []int64
	Reminder   *ReminderInfo
	Delegation *DelegationInfo
}

type ReminderInfo struct {
	ID     int64
	FireAt time.Time
}

type DelegationInfo struct {
	ID     int64
	Agent  string
	Report string
	Title  string
}

func IngestText(ctx context.Context, cfg config.Config, store *db.Store, userID int64, kind, raw, transcript string) (*IngestResult, error) {
	text := transcript
	if text == "" {
		text = raw
	}
	// Сначала анализ: от него зависит дата записи (entry_date).
	analysis, err := AnalyzeFull(ctx, text, cfg.OpenAIKey, cfg.OpenAIBaseURL, cfg.OpenAIModel)
	day := TodayStr()
	if err != nil {
		// Сырьё сохраняем сегодняшним числом — возвращаем ошибку с контекстом,
		// хендлер скажет пользователю, что заметка сохранена.
		entryID, dberr := store.AddEntry(userID, day, kind, raw, transcript)
		if dberr != nil {
			return nil, dberr
		}
		if errors.Is(err, ErrAnalyze) {
			return nil, fmt.Errorf("%w (заметка #%d сохранена, сырьё не потеряно)", err, entryID)
		}
		return nil, err
	}
	if d := ResolveEntryDate(analysis.EntryDate, time.Now()); d != "" {
		day = d // события прошлого — запись задним числом
	}
	entryID, err := store.AddEntry(userID, day, kind, raw, transcript)
	if err != nil {
		return nil, err
	}
	return analyzeAndStore(ctx, cfg, store, userID, entryID, day, text, analysis)
}

// ReanalyzeEntry — переразбор после исправления текста: сырьё заменяется,
// производные чистятся, анализ идёт заново. День записи сохраняется.
func ReanalyzeEntry(ctx context.Context, cfg config.Config, store *db.Store, userID, entryID int64, text string) (*IngestResult, error) {
	day, err := store.EntryDay(userID, entryID)
	if err != nil {
		return nil, err
	}
	if _, err := store.UpdateTranscript(userID, entryID, text); err != nil {
		return nil, err
	}
	if err := store.ClearDerived(entryID, userID); err != nil {
		return nil, err
	}
	analysis, err := AnalyzeFull(ctx, text, cfg.OpenAIKey, cfg.OpenAIBaseURL, cfg.OpenAIModel)
	if err != nil {
		return nil, err
	}
	return analyzeAndStore(ctx, cfg, store, userID, entryID, day, text, analysis)
}

// analyzeAndStore — общая часть: блоки → сущности → задачи → напоминание → делегирование.
func analyzeAndStore(ctx context.Context, cfg config.Config, store *db.Store, userID, entryID int64, day, text string, analysis *Analysis) (*IngestResult, error) {
	blocks := analysis.Blocks
	if err := store.AddBlocks(entryID, userID, day, blocks); err != nil {
		return nil, err
	}
	entities := ExtractEntities(text)
	if err := store.AddEntities(entryID, userID, day, entities); err != nil {
		return nil, err
	}
	res := &IngestResult{EntryID: entryID, Day: day, Blocks: blocks, Entities: entities}
	for _, b := range blocks {
		if b.Aspect == "task" || (b.Aspect == "plan" && GuessDue(b.Content) != "") {
			tid, err := store.AddTask(userID, entryID, b.Content, GuessDue(b.Content))
			if err != nil {
				return nil, err
			}
			res.TaskIDs = append(res.TaskIDs, tid)
		}
	}
	// Просьба напомнить: парсим время и ставим триггер в reminders.
	// Без времени («напомни потом») триггер не ставим — задача уже в /tasks.
	if RemindIntentRe.MatchString(text) {
		loc, _ := time.LoadLocation(cfg.TZ)
		if loc == nil {
			loc = time.Local
		}
		if fireAt, ok := ParseRemindAt(text, time.Now(), loc); ok {
			taskID := int64(0)
			if len(res.TaskIDs) > 0 {
				taskID = res.TaskIDs[0]
			} else {
				tid, err := store.AddTask(userID, entryID, text, "")
				if err != nil {
					return nil, err
				}
				res.TaskIDs = append(res.TaskIDs, tid)
				taskID = tid
			}
			rid, err := store.AddReminder(taskID, userID, fireAt, "deadline")
			if err != nil {
				return nil, err
			}
			res.Reminder = &ReminderInfo{ID: rid, FireAt: fireAt}
		}
	}
	if card := DetectDelegationIntent(text); card != nil {
		card.Context = text
		if len([]rune(card.Context)) > 1000 {
			card.Context = string([]rune(card.Context)[:1000])
		}
		card.Due = GuessDue(text)
		card.SourceEntryIDs = []int64{entryID}
		agent, report := RouteToPlugin(ctx, *card)
		did, err := store.AddDelegation(userID, entryID, card.Title, card.Context, card.Due, agent)
		if err != nil {
			return nil, err
		}
		res.Delegation = &DelegationInfo{ID: did, Agent: agent, Report: report, Title: card.Title}
	}
	return res, nil
}
