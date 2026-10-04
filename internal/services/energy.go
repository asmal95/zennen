package services

import (
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"
)

var sparkBars = []rune("▁▂▃▄▅▆▇█")

// EnergySparkline строит текстовый график энергии по дням.
// Нет замера — "·". Возвращает две строки: бары и числа.
func EnergySparkline(days []string, energy map[string]int) (bars, nums string) {
	var b, n strings.Builder
	for _, d := range days {
		v, ok := energy[d]
		if !ok {
			b.WriteRune('·')
			n.WriteString("– ")
			continue
		}
		idx := (v - 1) * (len(sparkBars) - 1) / 9
		b.WriteRune(sparkBars[idx])
		fmt.Fprintf(&n, "%d ", v)
	}
	return b.String(), strings.TrimSpace(n.String())
}

// EnergyKeyboard — кнопки 1–10 для замера энергии дня (2 ряда по 5).
func EnergyKeyboard(day string) *models.InlineKeyboardMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	for r := 0; r < 2; r++ {
		row := make([]models.InlineKeyboardButton, 0, 5)
		for i := 1; i <= 5; i++ {
			v := r*5 + i
			row = append(row, models.InlineKeyboardButton{
				Text:         fmt.Sprintf("%d", v),
				CallbackData: fmt.Sprintf("energy:%s:%d", day, v),
			})
		}
		rows = append(rows, row)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
