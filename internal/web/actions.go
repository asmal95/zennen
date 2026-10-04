package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"diarybot/internal/services"
)

// checkOrigin — мини-CSRF для POST: принимаем только с нашего домена.
// Первый рубеж — SameSite=Lax cookie (чужой сайт POST с кукой не сделает),
// это второй: режет даже lax-совместимые кейсы и прямые подделки.
func (s *Server) checkOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && o != s.cfg.WebBaseURL {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleReminders(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	items, _ := s.store.UpcomingReminders(id)
	s.render(w, "reminders", map[string]any{"Items": items, "TZ": s.userTZName(id)})
}

// handleReminderNew: поля «что» + «когда» («завтра в 9», «через 2 часа»).
// Время парсим тем же ParseRemindAt, что и бот.
func (s *Server) handleReminderNew(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	what := strings.TrimSpace(r.FormValue("what"))
	when := strings.TrimSpace(r.FormValue("when"))
	fail := func(msg string) {
		items, _ := s.store.UpcomingReminders(id)
		s.render(w, "reminders", map[string]any{"Items": items, "Err": msg, "TZ": s.userTZName(id)})
	}
	if what == "" {
		fail("Напиши, о чём напомнить.")
		return
	}
	fireAt, ok := services.ParseRemindAt(when, time.Now(), services.UserLoc(s.store, id, s.cfg.TZ))
	if !ok {
		fail("Не понял «когда». Примеры: «через 2 часа», «в 15:30», «завтра в 9», «в пятницу вечером».")
		return
	}
	tid, err := s.store.AddTask(id, 0, what, "", "")
	if err != nil {
		fail("Не сохранилось: " + err.Error())
		return
	}
	if _, err := s.store.AddReminder(tid, id, fireAt, "deadline"); err != nil {
		fail("Не сохранилось: " + err.Error())
		return
	}
	http.Redirect(w, r, "/reminders", http.StatusSeeOther)
}

func (s *Server) handleReminderCancel(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if rid, err := strconv.ParseInt(r.FormValue("id"), 10, 64); err == nil {
		_ = s.store.DeleteReminder(id, rid)
	}
	http.Redirect(w, r, "/reminders", http.StatusSeeOther)
}

func (s *Server) handleTaskClose(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if tid, err := strconv.ParseInt(r.FormValue("id"), 10, 64); err == nil {
		_, _ = s.store.CloseTask(id, tid)
	}
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}

func (s *Server) handleTaskNew(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if text := strings.TrimSpace(r.FormValue("text")); text != "" {
		_, _ = s.store.AddTask(id, 0, text, services.GuessDue(text), "")
	}
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}

func (s *Server) handleEnergyVote(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	day := r.FormValue("day")
	v, err := strconv.Atoi(r.FormValue("v"))
	if err != nil || v < 1 || v > 10 || len(day) != 10 {
		http.Error(w, "bad vote", http.StatusBadRequest)
		return
	}
	_ = s.store.SetEnergy(id, day, v)
	http.Redirect(w, r, "/day?d="+day, http.StatusSeeOther)
}
