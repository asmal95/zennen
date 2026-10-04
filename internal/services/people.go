package services

import (
	"strings"

	"diarybot/internal/db"
)

// Русские падежные окончания для грубого стемминга имён/тегов.
// Двубуквенные проверяются первыми («Иваном» → «иван», «Машей» → «маш»).
// Не настоящий морфологический разбор: для личного дневника достаточно,
// сложные случаи (супплетивизм и т.п.) остаются на будущее.
var nameEndings = []string{"ом", "ем", "ой", "ей", "а", "я", "о", "е", "ё", "и", "ы", "у", "ю", "ь"}

func cleanName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimLeft(s, "@#")
}

func stemName(s string) string {
	s = cleanName(s)
	for _, e := range nameEndings {
		if strings.HasSuffix(s, e) && len([]rune(s)) > len([]rune(e))+2 {
			return strings.TrimSuffix(s, e)
		}
	}
	return s
}

// MatchName: «Иван» = «Иваном» ( contains), «Маша» = «Маши» (общий стем),
// «петр» = «@petr», «стартап» = «#стартап».
func MatchName(query, value string) bool {
	q, v := cleanName(query), cleanName(value)
	if q == "" || v == "" {
		return false
	}
	// Префикс, а не подстрока: «Иваном» начинается с «иван» ✓,
	// а «диван» — нет (contains давал ложное срабатывание).
	if q == v || strings.HasPrefix(v, q) || strings.HasPrefix(q, v) {
		return true
	}
	return stemName(q) == stemName(v)
}

// HistoryHit — одна запись с упоминанием сущности.
type HistoryHit struct {
	Day    string
	Kind   string
	Blocks []db.Block
}

// EntityHistory собирает записи пользователя с упоминанием сущности:
// person («Иван») или project («стартап», с/без #).
// Возвращает хиты (свежие первыми, не более limit записей) и формы,
// в которых сущность встретилась («нашёл как: Иваном, @ivan»).
func EntityHistory(store *db.Store, userID int64, etype, query string, limit int) ([]HistoryHit, []string, error) {
	ents, err := store.EntitiesByType(userID, etype)
	if err != nil {
		return nil, nil, err
	}
	seenEntry := map[int64]bool{}
	seenValue := map[string]bool{}
	var values []string
	var entryIDs []int64
	for _, e := range ents {
		if !MatchName(query, e.Value) {
			continue
		}
		if !seenValue[e.Value] {
			seenValue[e.Value] = true
			values = append(values, e.Value)
		}
		if !seenEntry[e.EntryID] {
			seenEntry[e.EntryID] = true
			entryIDs = append(entryIDs, e.EntryID)
		}
	}
	// Свежие первыми, срез лимита.
	var hits []HistoryHit
	for i := len(entryIDs) - 1; i >= 0 && len(hits) < limit; i-- {
		blocks, err := store.EntryBlocks(entryIDs[i])
		if err != nil || len(blocks) == 0 {
			continue
		}
		hits = append(hits, HistoryHit{Day: blocks[0].Day, Blocks: blocks})
	}
	return hits, values, nil
}
