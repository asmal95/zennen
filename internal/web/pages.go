package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"diarybot/internal/db"
	"diarybot/internal/services"
)

type DayView struct {
	Day       string
	Energy    int
	HasEnergy bool
	Notes     []db.ExportNote
}

func (s *Server) dayView(userID int64, day string) DayView {
	notes, _ := s.store.DayEntries(userID, day)
	v := DayView{Day: day, Notes: notes}
	if days := []string{day}; len(days) > 0 {
		if m, _ := s.store.WeekEnergy(userID, days); m != nil {
			if e, ok := m[day]; ok {
				v.Energy, v.HasEnergy = e, true
			}
		}
	}
	return v
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	days, _ := s.store.DayList(id, 14)
	view := make([]DayView, 0, len(days))
	for _, d := range days {
		view = append(view, s.dayView(id, d))
	}
	s.render(w, "index", map[string]any{"Days": view})
}

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	day := r.URL.Query().Get("d")
	if day == "" {
		day = today()
	}
	if len(day) != 10 {
		http.Error(w, "bad day, want YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		http.Error(w, "bad day", http.StatusBadRequest)
		return
	}
	s.render(w, "day", map[string]any{
		"View": s.dayView(id, day),
		"Prev": t.AddDate(0, 0, -1).Format("2006-01-02"),
		"Next": t.AddDate(0, 0, 1).Format("2006-01-02"),
	})
}

func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	now := time.Now()
	var days []string
	for i := 6; i >= 0; i-- {
		days = append(days, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	rows, _ := s.store.WeekBlocks(id, days)
	counts := map[string]int{}
	for _, b := range rows {
		counts[b.Aspect]++
	}
	energy, _ := s.store.WeekEnergy(id, days)
	bars, nums := services.EnergySparkline(days, energy)

	review, regenErr := s.cachedReview(r, id, today())
	s.render(w, "week", map[string]any{
		"Blocks": len(rows), "Counts": counts,
		"Bars": bars, "Nums": nums, "HasEnergy": len(energy) > 0,
		"Review": review, "ReviewErr": regenErr != "",
	})
}

// cachedReview отдаёт ревью из кэша дня, при промахе генерирует и сохраняет.
// Ручной regen — query ?regen=1 (GET читает кэш, регенерация только при промахе;
// принудительно — удалением строки, позже через кнопку v2).
func (s *Server) cachedReview(r *http.Request, userID int64, day string) (string, string) {
	if r.URL.Query().Get("regen") == "1" {
		if text, err := services.BuildWeeklyReview(r.Context(), s.cfg, s.store, userID); err == nil {
			_ = s.sess.WeekCacheSet(userID, day, text)
			return text, ""
		} else {
			return "", err.Error()
		}
	}
	if text, ok, _ := s.sess.WeekCacheGet(userID, day); ok {
		return text, ""
	}
	if text, err := services.BuildWeeklyReview(r.Context(), s.cfg, s.store, userID); err == nil {
		_ = s.sess.WeekCacheSet(userID, day, text)
		return text, ""
	} else {
		return "", err.Error()
	}
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	open, _ := s.store.OpenTasks(id)
	done, _ := s.store.DoneTasksSince(id, time.Now().AddDate(0, 0, -30))
	s.render(w, "tasks", map[string]any{"Open": open, "Done": done})
}

func (s *Server) handleIdeas(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	rows, _ := s.store.BlocksByAspect(id, "idea", 50)
	s.render(w, "ideas", map[string]any{"Rows": rows})
}

func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	s.handleHistory(w, r, "person", "👥", "Человек")
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	s.handleHistory(w, r, "project", "#️⃣", "Проект")
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, etype, icon, label string) {
	id, _ := s.userID(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := map[string]any{"Q": q, "Icon": icon, "Label": label}
	if q != "" {
		hits, values, _ := services.EntityHistory(s.store, id, etype, q, 50)
		data["Hits"] = hits
		data["Values"] = values
		data["Found"] = true
	}
	s.render(w, "history", data)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := map[string]any{"Q": q}
	if q != "" {
		rows, _ := s.store.SearchBlocks(id, q, 50)
		data["Rows"] = rows
		data["Found"] = true
	}
	s.render(w, "search", data)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	month := r.URL.Query().Get("month")
	if month == "" {
		month = today()[:7]
	}
	notes, err := s.store.ExportMonth(id, month)
	if err != nil || len(notes) == 0 {
		http.Error(w, "нет записей за "+month, http.StatusNotFound)
		return
	}
	md := services.RenderExportMarkdown(month, notes)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="diary-%s.md"`, month))
	w.Write([]byte(md))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", nil)
}

// handleRedeem гасит одноразовый magic link и ставит сессионный cookie.
func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/r/")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	userID, ok, err := s.sess.ConsumeWebToken(raw)
	if err != nil || !ok {
		http.Error(w, "ссылка недействительна или истекла — попроси новую в боте: /link", http.StatusUnauthorized)
		return
	}
	session, err := s.sess.CreateWebToken(userID, 30*24*time.Hour)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "diary_session", Value: session, Path: "/",
		MaxAge: 30 * 24 * 3600, HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}
