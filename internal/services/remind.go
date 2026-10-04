package services

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RemindIntentRe — просьба напомнить: «напомни», «напомнить», «напоминание».
var RemindIntentRe = regexp.MustCompile(`(?i)(напомни|напомнить|напоминай|напоминание)`)

// Внимание: \b в Go (RE2) — ASCII-only и НЕ работает вокруг кириллицы,
// поэтому границы слов заданы явными классами. L = «не буква».
const nonLetter = `[^а-яёА-ЯЁa-zA-Z0-9]`

var (
	reInHours   = regexp.MustCompile(`(?i)через\s+(\d+)\s*(час(?:а|ов)?|ч)(?:` + nonLetter + `|$)`)
	reInMinutes = regexp.MustCompile(`(?i)через\s+(\d+)\s*(минут(?:у|ы)?|мин|м)(?:` + nonLetter + `|$)`)
	reHalfHour  = regexp.MustCompile(`(?i)через\s+полчаса|через\s+пол\s+часа`)
	reClock     = regexp.MustCompile(`(?i)(?:^|` + nonLetter + `)в\s+(\d{1,2})(?::(\d{2}))?\b`)
	reTomorrow  = regexp.MustCompile(`(?i)(?:^|` + nonLetter + `)завтра(?:` + nonLetter + `|$)`)
	// Голое «сегодня» без времени не парсим — непонятно, во сколько напоминать.
	// reTomorrow с границами заодно не цепляет «позавтракать».
)

func daypartHour(text string) (int, bool) {
	switch {
	case strings.Contains(strings.ToLower(text), "утром"):
		return 9, true
	case strings.Contains(strings.ToLower(text), "днём"),
		strings.Contains(strings.ToLower(text), "днем"):
		return 14, true
	case strings.Contains(strings.ToLower(text), "вечером"):
		return 20, true
	case strings.Contains(strings.ToLower(text), "ночью"):
		return 22, true
	}
	return 0, false
}

// ParseRemindAt вытаскивает из русского текста момент срабатывания.
// Понимает: «через N час/мин», «через полчаса», «в 15:30» / «в 15»,
// «завтра [в H:MM]», «сегодня …», «утром/днём/вечером/ночью».
// Возвращает ok=false, если времени в тексте нет.
func ParseRemindAt(text string, now time.Time, loc *time.Location) (time.Time, bool) {
	now = now.In(loc)
	lower := strings.ToLower(text)

	if reHalfHour.MatchString(text) {
		return now.Add(30 * time.Minute), true
	}
	if m := reInHours.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 0 && n <= 72 {
			return now.Add(time.Duration(n) * time.Hour), true
		}
	}
	if m := reInMinutes.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 0 && n <= 24*60 {
			return now.Add(time.Duration(n) * time.Minute), true
		}
	}
	at := func(day time.Time, h, min int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, min, 0, 0, loc)
	}
	if m := reClock.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min := 0
		if m[2] != "" {
			min, _ = strconv.Atoi(m[2])
		}
		if h > 23 || min > 59 {
			return time.Time{}, false
		}
		if reTomorrow.MatchString(text) {
			return at(now.AddDate(0, 0, 1), h, min), true
		}
		t := at(now, h, min)
		if !t.After(now) {
			t = at(now.AddDate(0, 0, 1), h, min) // время прошло — значит, завтра
		}
		return t, true
	}
	tomorrow := reTomorrow.MatchString(text)
	if h, ok := daypartHour(lower); ok {
		day := now
		if tomorrow {
			day = now.AddDate(0, 0, 1)
		}
		t := at(day, h, 0)
		if !tomorrow && !t.After(now) {
			t = at(now.AddDate(0, 0, 1), h, 0)
		}
		return t, true
	}
	if tomorrow {
		return at(now.AddDate(0, 0, 1), 9, 0), true // «завтра» без времени — 9:00
	}
	return time.Time{}, false
}
