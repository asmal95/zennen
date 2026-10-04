package services

import (
	"time"

	"diarybot/internal/db"
)

// UserLoc возвращает локальную зону пользователя.
// Фолбэк — дефолт конфига, затем системная зона. Не возвращает nil.
func UserLoc(store *db.Store, userID int64, fallback string) *time.Location {
	tz := fallback
	if u, err := store.GetUser(userID, fallback); err == nil && u.TZ != "" {
		tz = u.TZ
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	if loc, err := time.LoadLocation(fallback); err == nil {
		return loc
	}
	return time.Local
}

// DigestDue — чистая функция диспетчера: слать, если локальный час совпал
// и сегодня этот дайджест ещё не отправляли. Возвращает (пора, день).
func DigestDue(now time.Time, hour int, lastSentDay string) (bool, string) {
	day := now.Format("2006-01-02")
	return now.Hour() == hour && lastSentDay != day, day
}
