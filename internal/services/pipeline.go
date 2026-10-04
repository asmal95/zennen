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

// FutureStrs — завтра и послезавтра для target_date в промпте.
func FutureStrs() (tomorrow, dayAfter string) {
	now := time.Now()
	return now.AddDate(0, 0, 1).Format("2006-01-02"), now.AddDate(0, 0, 2).Format("2006-01-02")
}

// ResolveTargetDate проверяет target_date от LLM: валидный ISO в окне
// [вчера, сегодня+365]. Прошлое старше вчера и мусор → "".
func ResolveTargetDate(dateStr string, now time.Time) string {
	d, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return ""
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if d.Before(today.AddDate(0, 0, -1)) || d.After(today.AddDate(1, 0, 0)) {
		return ""
	}
	return d.Format("2006-01-02")
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
	EntryID  int64
	Day      string // дата, к которой отнесена запись (обычно сегодня)
	Blocks   []db.Block
	Entities []db.Entity
	TaskIDs  []int64
	PlanIDs  []int64
	Reminder *ReminderInfo
	// RemindMissed: просили напомнить, но время не распозналось.
	// Триггер НЕ поставлен — пользователь должен это увидеть.
	RemindMissed bool
	Delegation   *DelegationInfo
}

type ReminderInfo struct {
	ID     int64
	FireAt time.Time
	// Vague: время угадано из неточных слов («утром», голое «завтра»).
	Vague bool
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
	unow := time.Now().In(UserLoc(store, userID, cfg.TZ)) // «сегодня» — по зоне пользователя
	day := unow.Format("2006-01-02")
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
	if d := ResolveEntryDate(analysis.EntryDate, unow); d != "" {
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
	targets := analysis.Targets
	for i, b := range blocks {
		var target string
		if i < len(targets) {
			target = targets[i]
		}
		switch {
		case b.Aspect == "task":
			tid, err := store.AddTask(userID, entryID, b.Content, GuessDue(b.Content), target)
			if err != nil {
				return nil, err
			}
			res.TaskIDs = append(res.TaskIDs, tid)
		case b.Aspect == "fact" && target != "" && target > day:
			// Событие будущего («в пятницу встреча») — тоже план на тот день,
			// иначе «я же говорил про пятницу» потеряется.
			pid, err := store.AddPlan(userID, entryID, b.Content, target)
			if err != nil {
				return nil, err
			}
			res.PlanIDs = append(res.PlanIDs, pid)
		case b.Aspect == "plan" && target != "":
			// Намерение на конкретный день — в планы (с датой-целью).
			pid, err := store.AddPlan(userID, entryID, b.Content, target)
			if err != nil {
				return nil, err
			}
			res.PlanIDs = append(res.PlanIDs, pid)
		case b.Aspect == "plan" && GuessDue(b.Content) != "":
			// План со сроком словами, но без точной даты — как раньше, в задачи.
			tid, err := store.AddTask(userID, entryID, b.Content, GuessDue(b.Content), "")
			if err != nil {
				return nil, err
			}
			res.TaskIDs = append(res.TaskIDs, tid)
		}
	}
	// Просьба напомнить: парсим время и ставим триггер в reminders.
	// Без времени («напомни потом») триггер не ставим — задача уже в /tasks.
	if RemindIntentRe.MatchString(text) {
		loc := UserLoc(store, userID, cfg.TZ)
		if fireAt, ok := ParseRemindAt(text, time.Now(), loc); ok {
			taskID := int64(0)
			if len(res.TaskIDs) > 0 {
				taskID = res.TaskIDs[0]
			} else {
				tid, err := store.AddTask(userID, entryID, text, "", "")
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
			res.Reminder = &ReminderInfo{ID: rid, FireAt: fireAt, Vague: VagueTime(text)}
		} else {
			// Просили напомнить, а время не распознали: молчать нельзя.
			res.RemindMissed = true
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
