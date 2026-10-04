package services

import (
	"fmt"
	"strings"

	"diarybot/internal/db"
)

var names = map[string]string{
	"fact": "Факты", "thought": "Мысли", "plan": "Планы", "task": "Задачи",
	"idea": "Идеи", "emotion": "Состояние", "health": "Здоровье", "work": "Работа",
	"people": "Люди", "money": "Финансы", "gratitude": "Благодарности",
	"decision": "Решения", "other": "Прочее",
}

func em(a string) string { return EmojiFor(a) } // карта — в export.go

// AspectName — русское имя аспекта (для веба).
func AspectName(a string) string { return nm(a) }

func nm(a string) string {
	if n, ok := names[a]; ok {
		return n
	}
	return a
}

func RenderBlocks(blocks []db.Block) string {
	var b strings.Builder
	for i, bl := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s <b>%s</b>: %s", em(bl.Aspect), nm(bl.Aspect), bl.Content)
	}
	return b.String()
}

func RenderDay(day string, blocks []db.Block) string {
	if len(blocks) == 0 {
		return fmt.Sprintf("📅 <b>%s</b>\nПока пусто. Пришли голосовое или текст — начнём историю дня.", day)
	}
	counts := map[string]int{}
	order := []string{}
	for _, b := range blocks {
		if _, ok := counts[b.Aspect]; !ok {
			order = append(order, b.Aspect)
		}
		counts[b.Aspect]++
	}
	// топ-4 по порядку появления (достаточно для MVP)
	top := []string{}
	for _, a := range order {
		if len(top) >= 4 {
			break
		}
		top = append(top, fmt.Sprintf("%s %d", em(a), counts[a]))
	}
	tail := blocks
	if len(tail) > 30 {
		tail = tail[len(tail)-30:]
	}
	return fmt.Sprintf("📅 <b>%s</b> — записей: %d (%s)\n\n%s",
		day, len(blocks), strings.Join(top, ", "), RenderBlocks(tail))
}

// RenderPlans — планы на день. Пусто — приглашение записать вечером.
func RenderPlans(plans []db.Plan) string {
	if len(plans) == 0 {
		return "📌 На сегодня планов не записано. Вечером расскажи, что хочешь завтра, — утром напомню."
	}
	var b strings.Builder
	b.WriteString("📌 <b>Планы на сегодня:</b>\n")
	for _, p := range plans {
		fmt.Fprintf(&b, "#%d — %s\n", p.ID, p.Text)
	}
	b.WriteString("\nЗакрыть вручную: /plandone &lt;id&gt;")
	return b.String()
}

func RenderTasks(tasks []db.Task) string {
	if len(tasks) == 0 {
		return "✅ Открытых задач нет. Так держать!"
	}
	var b strings.Builder
	b.WriteString("✅ <b>Открытые задачи:</b>\n")
	for _, t := range tasks {
		fmt.Fprintf(&b, "#%d — %s", t.ID, t.Text)
		if t.Due != "" {
			fmt.Fprintf(&b, " <i>(%s)</i>", t.Due)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nЗакрыть: /done &lt;id&gt;")
	return b.String()
}
