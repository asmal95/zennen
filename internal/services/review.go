package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"diarybot/internal/config"
	"diarybot/internal/db"

	openai "github.com/sashabaranov/go-openai"
)

// ErrNoData — за неделю нет ни записей, ни задач: ревью строить не из чего.
var ErrNoData = errors.New("no weekly data")

const reviewSystem = `Ты — внимательный редактор личного дневника. По записям недели напиши тёплую и честную недельную рефлексию на русском языке.

Строгая структура ответа:
📌 Планировал
✅ Сделал
⏳ Не доделал (осталось открытым)
💡 Идеи недели
👥 Люди
❤️ Состояние
➡️ Фокус на следующую неделю (2-3 конкретных пункта)

Правила: опирайся только на приведённые записи, ничего не выдумывай; если по разделу данных нет — пиши «—» и иди дальше; задачу считай выполненной, только если она в списке закрытых, иначе она в «Не доделал»; не приписывай автору чувства и мысли, которых нет в записях; будь конкретным, ссылайся на факты; без воды и общих фраз; весь ответ — до 2500 символов. Форматирование — plain text, без markdown и HTML.`

// BuildWeeklyReview собирает данные 7 дней и строит нарративный дайджест одним LLM-вызовом.
func BuildWeeklyReview(ctx context.Context, cfg config.Config, store *db.Store, userID int64) (string, error) {
	now := time.Now()
	var days []string
	for i := 6; i >= 0; i-- {
		days = append(days, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	blocks, err := store.WeekBlocks(userID, days)
	if err != nil {
		return "", err
	}
	open, err := store.OpenTasks(userID)
	if err != nil {
		return "", err
	}
	done, err := store.DoneTasksSince(userID, now.AddDate(0, 0, -7))
	if err != nil {
		return "", err
	}
	energy, err := store.WeekEnergy(userID, days)
	if err != nil {
		return "", err
	}
	if len(blocks) == 0 && len(open) == 0 && len(done) == 0 && len(energy) == 0 {
		return "", ErrNoData
	}

	var b strings.Builder
	for _, day := range days {
		var lines []string
		for _, bl := range blocks {
			if bl.Day != day {
				continue
			}
			lines = append(lines, "["+bl.Aspect+"] "+bl.Content)
		}
		if len(lines) == 0 {
			continue
		}
		b.WriteString(day + ":\n" + strings.Join(lines, "\n") + "\n")
	}
	if len(open) > 0 {
		b.WriteString("Открытые задачи:\n")
		for _, t := range open {
			b.WriteString("- " + t.Text + "\n")
		}
	}
	if len(done) > 0 {
		b.WriteString("Закрытые за неделю:\n")
		for _, t := range done {
			b.WriteString("- " + t.Text + "\n")
		}
	}
	if len(energy) > 0 {
		b.WriteString("Энергия по дням (1-10):\n")
		for _, day := range days {
			if v, ok := energy[day]; ok {
				fmt.Fprintf(&b, "%s: %d\n", day, v)
			}
		}
	}
	content := b.String()
	if len(content) > 6000 {
		content = content[len(content)-6000:] // режем старые, оставляем свежее
	}

	client := NewLLMClient(cfg.OpenAIKey, cfg.OpenAIBaseURL)
	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       cfg.OpenAIModel,
		Temperature: 0.7,
		Messages: []openai.ChatCompletionMessage{
			{Role: "system", Content: reviewSystem},
			{Role: "user", Content: content},
		},
	})
	if err != nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("review llm: %w", err)
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}
