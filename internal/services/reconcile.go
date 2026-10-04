package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"diarybot/internal/config"
	"diarybot/internal/db"

	openai "github.com/sashabaranov/go-openai"
)

const reconcileSystem = `Ты — сверщик личного дневника. Тебе дают планы дня, задачи дня (со статусами) и записи этого дня. Определи, что из запланированного сделано, а что нет.

Верни ТОЛЬКО JSON: {"done": [<id планов>], "missed": [<id планов>], "report": "..."}.
Правила: done — план явно выполнен судя по записям (или задача с тем же смыслом закрыта); missed — про план в записях ни слова либо прямо сказано, что не сделал; сомневаешься — missed.
Поле report — короткий итог дня на русском, plain text, до 800 символов: что сделано / что пропущено / одна строка вывода. Без markdown и HTML, ничего не выдумывай.`

// ReconcileDay сверяет планы дня с фактом дня: обновляет статусы open-планов
// (done/missed) и возвращает текст отчёта. Задач не трогает — у них свой done.
// Нечего сверять — ErrNoData.
func ReconcileDay(ctx context.Context, cfg config.Config, store *db.Store, userID int64, day string) (string, error) {
	plans, err := store.PlansForDay(userID, day, true)
	if err != nil {
		return "", err
	}
	tasks, err := store.TasksForDay(userID, day)
	if err != nil {
		return "", err
	}
	if len(plans) == 0 && len(tasks) == 0 {
		return "", ErrNoData
	}
	blocks, err := store.DayBlocks(userID, day)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("Планы дня (id в скобках):\n")
	for _, p := range plans {
		fmt.Fprintf(&b, "[P%d] %s\n", p.ID, p.Text)
	}
	b.WriteString("Задачи дня:\n")
	for _, t := range tasks {
		mark := "открыта"
		if t.Done {
			mark = "закрыта"
		}
		fmt.Fprintf(&b, "[T%d] %s (%s)\n", t.ID, t.Text, mark)
	}
	b.WriteString("Записи дня:\n")
	for _, bl := range blocks {
		fmt.Fprintf(&b, "[%s] %s\n", bl.Aspect, bl.Content)
	}
	content := b.String()
	if len(content) > 6000 {
		content = content[len(content)-6000:]
	}

	client := NewLLMClient(cfg.OpenAIKey, cfg.OpenAIBaseURL)
	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       cfg.OpenAIModel,
		Temperature: 0.3,
		Messages: []openai.ChatCompletionMessage{
			{Role: "system", Content: reconcileSystem},
			{Role: "user", Content: content},
		},
	})
	if err != nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("reconcile llm: %w", err)
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	raw = codeFenceRe.ReplaceAllString(raw, "")
	var data struct {
		Done   []int64 `json:"done"`
		Missed []int64 `json:"missed"`
		Report string  `json:"report"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", fmt.Errorf("reconcile bad json: %w", err)
	}
	// Применяем только id из наших open-планов (LLM не доверяем вслепую).
	open := map[int64]bool{}
	for _, p := range plans {
		open[p.ID] = true
	}
	for _, id := range data.Done {
		if open[id] {
			_ = store.SetPlanStatus(userID, id, "done")
		}
	}
	for _, id := range data.Missed {
		if open[id] {
			_ = store.SetPlanStatus(userID, id, "missed")
		}
	}
	return strings.TrimSpace(data.Report), nil
}
