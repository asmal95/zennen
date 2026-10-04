package services

import (
	"regexp"
	"strings"

	"diarybot/internal/db"
)

var (
	hashtagRe    = regexp.MustCompile(`#([\w\-а-яёА-ЯЁ]+)`)
	mentionRe    = regexp.MustCompile(`@([\w\-а-яёА-ЯЁ]+)`)
	moneyEntRe   = regexp.MustCompile(`(?i)(\d[\d\s]*\s?(руб|рублей|₽|\$|€|доллар|евро))`)
	dateEntRe    = regexp.MustCompile(`(?i)(сегодня|завтра|послезавтра|в понедельник|во вторник|в среду|в четверг|в пятницу|в субботу|в воскресенье|утром|днём|вечером|ночью|к \d{1,2}(:\d{2})?|до \d{1,2}|через \d+ (час|день|дня|дней|недел)|на выходных|на следующей неделе)`)
	peopleHintRe = regexp.MustCompile(`(мама|папа|жена|муж|сын|дочь|брат|сестра|друг|подруга|коллега|начальник|созвон с|встреча с|позвонить)\s+([А-ЯЁ][а-яё]+)`)
	// RE2 (Go) не умеет lookahead — обещание берём до конца предложения.
	promiseRe = regexp.MustCompile(`(?i)(обещал[а-я]*|договорил[а-яйся]*|пообещал[а-я]*)[^.!]*`)
)

func ExtractEntities(text string) []db.Entity {
	var out []db.Entity
	seen := map[string]bool{}
	add := func(etype, value string) {
		v := strings.Trim(strings.Trim(value, ",. "), " ")
		if len([]rune(v)) < 2 || len([]rune(v)) > 120 {
			return
		}
		key := etype + "\x00" + strings.ToLower(v)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, db.Entity{Type: etype, Value: v})
	}
	for _, m := range hashtagRe.FindAllStringSubmatch(text, -1) {
		add("project", "#"+m[1])
	}
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		add("person", "@"+m[1])
	}
	for _, m := range moneyEntRe.FindAllStringSubmatch(text, -1) {
		add("money", m[1])
	}
	for _, m := range dateEntRe.FindAllStringSubmatch(text, -1) {
		add("date", m[1])
	}
	for _, m := range peopleHintRe.FindAllStringSubmatch(text, -1) {
		add("person", m[2])
	}
	for _, m := range promiseRe.FindAllString(text, -1) {
		add("promise", m)
	}
	return out
}
