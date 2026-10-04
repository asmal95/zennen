package services

import (
	"context"
	"regexp"
	"strings"
)

// DelegationCard — карточка для внешнего агента-исполнителя.
// Ядро только готовит карточку, исполнение — через AgentPlugin.
type DelegationCard struct {
	Title          string
	Context        string
	Due            string
	Entities       map[string][]string
	SourceEntryIDs []int64
}

var delegateRe = regexp.MustCompile(`(?i)(поручи|передай|отдай|делегируй).{0,20}(агент|календар|кодер|боту|ассистент)|(пусть|попроси).{0,30}(сделает|забронирует|найдёт|найдет|напишет|создаст)`)

func DetectDelegationIntent(text string) *DelegationCard {
	if !delegateRe.MatchString(text) {
		return nil
	}
	t := strings.TrimSpace(text)
	if len([]rune(t)) > 200 {
		t = string([]rune(t)[:200])
	}
	return &DelegationCard{Title: t, Context: strings.TrimSpace(text)}
}

// AgentPlugin — интерфейс внешнего исполнителя.
// Реализуй в отдельном пакете и зарегистрируй в Registry.
type AgentPlugin interface {
	Name() string
	CanHandle(ctx context.Context, card DelegationCard) bool
	Handle(ctx context.Context, card DelegationCard) string
}

type NoOpPlugin struct{}

func (NoOpPlugin) Name() string                                       { return "noop" }
func (NoOpPlugin) CanHandle(_ context.Context, _ DelegationCard) bool { return true }
func (NoOpPlugin) Handle(_ context.Context, card DelegationCard) string {
	title := card.Title
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80])
	}
	return "[noop] Карточка «" + title + "» принята в очередь. Реальный агент не подключён."
}

type CalendarPlugin struct{}

func (CalendarPlugin) Name() string { return "calendar" }
func (CalendarPlugin) CanHandle(_ context.Context, card DelegationCard) bool {
	t := strings.ToLower(card.Title + " " + card.Context)
	for _, w := range []string{"встреч", "созвон", "календар", "напомни", "запланируй"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}
func (CalendarPlugin) Handle(_ context.Context, card DelegationCard) string {
	title := card.Title
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80])
	}
	due := card.Due
	if due == "" {
		due = "—"
	}
	return "[calendar] (stub) Создал бы встречу: «" + title + "» срок: " + due
}

var Registry = []AgentPlugin{CalendarPlugin{}, NoOpPlugin{}}

func RouteToPlugin(ctx context.Context, card DelegationCard) (string, string) {
	for _, p := range Registry {
		if p.CanHandle(ctx, card) {
			return p.Name(), p.Handle(ctx, card)
		}
	}
	return "noop", "Нет подходящего плагина."
}
