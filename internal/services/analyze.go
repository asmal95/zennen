package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"diarybot/internal/db"

	openai "github.com/sashabaranov/go-openai"
)

// ErrAnalyze возвращается, когда разбор через LLM не удался.
// Сырьё (entries) к этому моменту уже сохранено — теряется только разбор.
var ErrAnalyze = errors.New("llm analyze failed")

var validAspects = map[string]bool{
	"fact": true, "thought": true, "plan": true, "task": true,
	"idea": true, "emotion": true, "health": true, "work": true,
	"people": true, "money": true, "gratitude": true, "decision": true,
	"other": true,
}

const systemPromptBase = `Ты — структуризатор личного дневника. Разбей текст пользователя на блоки. ` +
	`aspect строго из списка: fact, thought, plan, task, idea, emotion, health, work, people, money, gratitude, decision, other. ` +
	`Правила: task — конкретное действие с датой/сроком; plan — намерение без даты; ` +
	`idea — озарение; emotion — чувства/состояние; fact — события; thought — размышления; ` +
	`health/work/people/money/gratitude/decision — по смыслу; other — остальное. ` +
	`Не выдумывай, сохраняй смысл, каждый блок 1-2 предложения. ` +
	`Сохраняй в тексте блока слова-даты из оригинала («завтра», «послезавтра», «в пятницу», «10 октября») как есть, не перефразируй их и не выбрасывай. ` +
	`Верни ТОЛЬКО JSON-объект: {"entry_date": ..., "blocks": [{"aspect": "...", "content": "...", "target_date": ...}]}. ` +
	`Поле entry_date: к какому числу относятся описываемые СОБЫТИЯ — "YYYY-MM-DD" или null, если события сегодняшние или дата не указана. ` +
	`Поле target_date (только для aspect plan/task, остальным null): к какому дню относится намерение или действие — "YYYY-MM-DD" или null, если день не указан («когда-нибудь», «скоро», «надо бы» = null). ` +
	`Правила target_date: «завтра» = %s; «послезавтра» = %s; день недели — строго из таблицы будущего: %s; ` +
	`явная дата («10 октября») = такое число (если в этом году уже прошло — следующий год). ` +
	`Примеры: «завтра хочу в бассейн» → plan + target_date; «надо завтра сдать отчёт» → task + target_date. ` +
	`Правила даты: «вчера» = %s; «позавчера» = %s; день недели — строго из таблицы: %s; ` +
	`явная дата («3 октября») = такое число текущего года в формате YYYY-MM-DD (если получилось будущее — прошлый год); ` +
	`упоминание БУДУЩЕЙ даты как срока задачи («завтра сдать», «напомни в пятницу») НЕ меняет entry_date — ставь null; ` +
	`entry_date никогда не в будущем. ` +
	`Сегодня %s.`

var codeFenceRe = regexp.MustCompile("(?m)^```json|^```|```$")

// Границы слов для кириллицы (RE2: \b ASCII-only, не работает).
const ruBound = `[^а-яёa-z]`

var (
	reDayAfter  = regexp.MustCompile(`(?i)(?:^|` + ruBound + `)послезавтра(?:` + ruBound + `|$)`)
	reTomorrowW = regexp.MustCompile(`(?i)(?:^|` + ruBound + `)завтра(?:` + ruBound + `|$)`)
	reWeekdayW  = regexp.MustCompile(`(?i)(?:^|` + ruBound + `)(понедельник|вторник|сред[ау]|четверг|пятниц[ау]|суббот[ау]|воскресень[ея])(?:` + ruBound + `|$)`)
	reDaypartW  = regexp.MustCompile(`(?i)(?:^|` + ruBound + `)(утром|днём|днем|вечером|ночью)(?:` + ruBound + `|$)`)
	reDateNum   = regexp.MustCompile(`(\d{1,2})\.(\d{1,2})(?:\.(\d{2,4}))?`)
	reDateWord  = regexp.MustCompile(`(?i)(\d{1,2})\s+(января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря)`)
)

var ruMonths = map[string]int{
	"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6,
	"июля": 7, "августа": 8, "сентября": 9, "октября": 10, "ноября": 11, "декабря": 12,
}

// targetOverride детерминированно резолвит явные относительные маркеры
// в тексте ОДНОГО блока: послезавтра/завтра/дни недели.
// LLM систематически ошибается даже на «завтра» (3/3 мимо в тесте),
// поэтому явные маркеры — всегда Go, LLM — только для прочих формулировок.
// Возвращает "" если маркеров нет.
func targetOverride(content string, now time.Time) string {
	if reDayAfter.MatchString(content) {
		return now.AddDate(0, 0, 2).Format("2006-01-02")
	}
	if reTomorrowW.MatchString(content) {
		return now.AddDate(0, 0, 1).Format("2006-01-02")
	}
	if m := reWeekdayW.FindStringSubmatch(strings.ToLower(content)); m != nil {
		got := stemName(m[1]) // stemName из people.go: «среду» и «среда» → «сред»
		for wd, name := range ruWeekdays {
			if stemName(name) == got {
				delta := (int(wd) - int(now.Weekday()) + 7) % 7
				return now.AddDate(0, 0, delta).Format("2006-01-02")
			}
		}
	}
	return ""
}

// messageAnchor ищет в ВСЁМ тексте первый явный якорь дня
// (послезавтра → завтра → день недели → точная дата).
// Голые «утром/вечером» якорем НЕ считаются — они наследуют его.
func messageAnchor(text string, now time.Time) string {
	if reDayAfter.MatchString(text) {
		return now.AddDate(0, 0, 2).Format("2006-01-02")
	}
	if reTomorrowW.MatchString(text) {
		return now.AddDate(0, 0, 1).Format("2006-01-02")
	}
	lower := strings.ToLower(text)
	if m := reWeekdayW.FindStringSubmatch(lower); m != nil {
		got := stemName(m[1])
		for wd, name := range ruWeekdays {
			if stemName(name) == got {
				delta := (int(wd) - int(now.Weekday()) + 7) % 7
				return now.AddDate(0, 0, delta).Format("2006-01-02")
			}
		}
	}
	if m := reDateWord.FindStringSubmatch(lower); m != nil {
		day := atoi(m[1])
		mon := ruMonths[m[2]]
		if day >= 1 && day <= 31 && mon >= 1 {
			y := now.Year()
			d := time.Date(y, time.Month(mon), day, 0, 0, 0, 0, now.Location())
			if d.Before(startOfDay(now)) {
				d = time.Date(y+1, time.Month(mon), day, 0, 0, 0, 0, now.Location())
			}
			return d.Format("2006-01-02")
		}
	}
	if m := reDateNum.FindStringSubmatch(text); m != nil {
		day, mon := atoi(m[1]), atoi(m[2])
		if day >= 1 && day <= 31 && mon >= 1 && mon <= 12 {
			y := now.Year()
			d := time.Date(y, time.Month(mon), day, 0, 0, 0, 0, now.Location())
			if d.Before(startOfDay(now)) {
				d = time.Date(y+1, time.Month(mon), day, 0, 0, 0, 0, now.Location())
			}
			return d.Format("2006-01-02")
		}
	}
	return ""
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// hasDateAnchor — в тексте есть явный якорь дня (не голое время суток).
func hasDateAnchor(content string) bool {
	lower := strings.ToLower(content)
	return reDayAfter.MatchString(content) ||
		reTomorrowW.MatchString(content) ||
		reWeekdayW.MatchString(lower) ||
		reDateWord.MatchString(lower) ||
		reDateNum.MatchString(content)
}

var ruWeekdays = map[time.Weekday]string{
	time.Monday: "понедельник", time.Tuesday: "вторник", time.Wednesday: "среда",
	time.Thursday: "четверг", time.Friday: "пятница", time.Saturday: "суббота",
	time.Sunday: "воскресенье",
}

// weekdayTable — «понедельник=2026-09-28, …» за последние 7 дней:
// LLM берёт дату дня недели поиском по таблице, а не вычислением.
func weekdayTable() string {
	now := time.Now()
	parts := make([]string, 0, 7)
	for i := 6; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		parts = append(parts, ruWeekdays[d.Weekday()]+"="+d.Format("2006-01-02"))
	}
	return strings.Join(parts, ", ")
}

// weekdayTableFuture — то же на 14 дней вперёд (для target_date).
func weekdayTableFuture() string {
	now := time.Now()
	parts := make([]string, 0, 14)
	seen := map[string]bool{}
	for i := 0; i < 14; i++ {
		d := now.AddDate(0, 0, i)
		name := ruWeekdays[d.Weekday()]
		if seen[name] {
			continue // день недели уже есть (ближайший future)
		}
		seen[name] = true
		parts = append(parts, name+"="+d.Format("2006-01-02"))
	}
	return strings.Join(parts, ", ")
}

// Analysis — разбор текста: блоки аспектов + дата событий + даты-цели.
// Targets[i] — target_date блока Blocks[i] ("" = день не указан).
type Analysis struct {
	Blocks    []db.Block
	EntryDate string
	Targets   []string
}

// AnalyzeFull разбирает текст на блоки аспектов и определяет entry_date
// (дату событий для записи задним числом) — только через LLM API.
// Без ключа или при любой ошибке LLM возвращает ошибку (wrapping ErrAnalyze) —
// тихого fallback на эвристики нет по решению концепции.
func AnalyzeFull(ctx context.Context, text, apiKey, baseURL, model string) (*Analysis, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%w: empty text", ErrAnalyze)
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	if apiKey == "" {
		return nil, fmt.Errorf("%w: OPENAI_API_KEY is not set", ErrAnalyze)
	}
	today := TodayStr()
	yesterday, dayBefore := PastStrs()
	tomorrow, dayAfter := FutureStrs()
	system := fmt.Sprintf(systemPromptBase, yesterday, dayBefore, weekdayTable(), tomorrow, dayAfter, weekdayTableFuture(), today)
	client := NewLLMClient(apiKey, baseURL)
	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       model,
		Temperature: 0.2,
		Messages: []openai.ChatCompletionMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: text},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: chat completion: %w", ErrAnalyze, err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("%w: empty choices", ErrAnalyze)
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	raw = codeFenceRe.ReplaceAllString(raw, "")
	var data struct {
		EntryDate *string `json:"entry_date"`
		Blocks    []struct {
			Aspect     string  `json:"aspect"`
			Content    string  `json:"content"`
			TargetDate *string `json:"target_date"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("%w: bad json: %w", ErrAnalyze, err)
	}
	out := &Analysis{}
	if data.EntryDate != nil {
		out.EntryDate = strings.TrimSpace(*data.EntryDate)
	}
	now := time.Now()
	anchor := messageAnchor(text, now) // якорь всего сообщения для наследования
	for _, b := range data.Blocks {
		c := strings.TrimSpace(b.Content)
		if c == "" {
			continue
		}
		a := b.Aspect
		if !validAspects[a] {
			a = "other"
		}
		out.Blocks = append(out.Blocks, db.Block{Aspect: a, Content: c})
		t := ""
		if b.TargetDate != nil && (a == "plan" || a == "task") {
			t = strings.TrimSpace(*b.TargetDate)
		}
		t = ResolveTargetDate(t, now)
		if a == "plan" || a == "task" || a == "fact" {
			if ov := targetOverride(c, now); ov != "" {
				t = ov // явный маркер в блоке — детерминированно поверх LLM
			} else if anchor != "" && !hasDateAnchor(c) && reDaypartW.MatchString(text) {
				// В блоке якоря нет (LLM мог перефразировать «вечером» прочь
				// и выдумать дату — ловили 10-03 вместо 10-05), но сообщение
				// говорит о части якорного дня — берём якорь всегда.
				// Только plan/task: факты прошлого так не притягиваем.
				if a == "plan" || a == "task" {
					t = anchor
				}
			}
		}
		out.Targets = append(out.Targets, t)
	}
	if len(out.Blocks) == 0 {
		return nil, fmt.Errorf("%w: no blocks", ErrAnalyze)
	}
	return out, nil
}
