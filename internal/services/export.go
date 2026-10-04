package services

import (
	"fmt"
	"strings"

	"diarybot/internal/db"
)

var aspectEmoji = map[string]string{
	"fact": "🕒", "thought": "💭", "plan": "📌", "task": "✅", "idea": "💡",
	"emotion": "❤️", "health": "🏃", "work": "💼", "people": "👥",
	"money": "💰", "gratitude": "🙏", "decision": "⚖️", "other": "📝",
}

// EmojiFor — эмодзи аспекта для экспорта и будущих вьюх.
func EmojiFor(aspect string) string {
	if e, ok := aspectEmoji[aspect]; ok {
		return e
	}
	return "•"
}

// RenderExportMarkdown собирает Markdown дневника за месяц (для Obsidian/Notion).
func RenderExportMarkdown(month string, notes []db.ExportNote) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Дневник — %s\n\n", month)
	lastDay := ""
	for _, n := range notes {
		if n.Day != lastDay {
			fmt.Fprintf(&b, "## %s\n\n", n.Day)
			lastDay = n.Day
		}
		kindIcon := "📝"
		if n.Kind == "voice" {
			kindIcon = "🎧"
		}
		hm := ""
		if len(n.CreatedAt) >= 16 {
			hm = n.CreatedAt[11:16]
		}
		fmt.Fprintf(&b, "### %s %s\n\n", kindIcon, hm)
		if t := strings.TrimSpace(n.Transcript); t != "" {
			for _, line := range strings.Split(t, "\n") {
				fmt.Fprintf(&b, "> %s\n", strings.TrimSpace(line))
			}
			b.WriteString("\n")
		}
		for _, bl := range n.Blocks {
			fmt.Fprintf(&b, "- %s **%s**: %s\n", EmojiFor(bl.Aspect), nm(bl.Aspect), bl.Content)
		}
		b.WriteString("\n")
	}
	return b.String()
}
