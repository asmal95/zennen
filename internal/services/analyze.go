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
	`Верни ТОЛЬКО JSON-объект: {"entry_date": ..., "blocks": [{"aspect": "...", "content": "..."}]}. ` +
	`Поле entry_date: к какому числу относятся описываемые СОБЫТИЯ — "YYYY-MM-DD" или null, если события сегодняшние или дата не указана. ` +
	`Правила даты: «вчера» = %s; «позавчера» = %s; день недели — строго из таблицы: %s; ` +
	`явная дата («3 октября») = такое число текущего года в формате YYYY-MM-DD (если получилось будущее — прошлый год); ` +
	`упоминание БУДУЩЕЙ даты как срока задачи («завтра сдать», «напомни в пятницу») НЕ меняет entry_date — ставь null; ` +
	`entry_date никогда не в будущем. ` +
	`Сегодня %s.`

var codeFenceRe = regexp.MustCompile("(?m)^```json|^```|```$")

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

// Analysis — разбор текста: блоки аспектов + дата, к которой относятся события.
type Analysis struct {
	Blocks    []db.Block
	EntryDate string // "" = сегодня
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
	system := fmt.Sprintf(systemPromptBase, yesterday, dayBefore, weekdayTable(), today)
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
			Aspect  string `json:"aspect"`
			Content string `json:"content"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("%w: bad json: %w", ErrAnalyze, err)
	}
	out := &Analysis{}
	if data.EntryDate != nil {
		out.EntryDate = strings.TrimSpace(*data.EntryDate)
	}
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
	}
	if len(out.Blocks) == 0 {
		return nil, fmt.Errorf("%w: no blocks", ErrAnalyze)
	}
	return out, nil
}
