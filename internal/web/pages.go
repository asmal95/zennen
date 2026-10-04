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
		day = s.userNow(id).Format("2006-01-02")
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
		"Nums": nums10,
	})
}

func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	now := s.userNow(id)
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

	review, regenErr := s.cachedReview(r, id, now.Format("2006-01-02"))
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

// TLNote — запись таймлайна: разбор + задачи со статусами + доминантный аспект.
type TLNote struct {
	db.ExportNote
	Tasks []db.Task
	Color string
}

// TLDay — день таймлайна.
type TLDay struct {
	Day       string
	Energy    int
	HasEnergy bool
	Notes     []TLNote
}

// TLMonth — группа дней одного месяца.
type TLMonth struct {
	Month string
	Days  []TLDay
}

func (s *Server) tlDay(userID int64, day string) TLDay {
	d := TLDay{Day: day}
	notes, _ := s.store.DayEntries(userID, day)
	if m, _ := s.store.WeekEnergy(userID, []string{day}); m != nil {
		if e, ok := m[day]; ok {
			d.Energy, d.HasEnergy = e, true
		}
	}
	for _, n := range notes {
		tn := TLNote{ExportNote: n}
		if len(n.Blocks) > 0 {
			if c, ok := aspectColors[n.Blocks[0].Aspect]; ok {
				tn.Color = c
			} else {
				tn.Color = "78909c"
			}
		} else {
			tn.Color = "78909c"
		}
		tn.Tasks, _ = s.store.TasksByEntry(n.ID)
		d.Notes = append(d.Notes, tn)
	}
	return d
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	days, _ := s.store.DayList(id, 30)
	var months []TLMonth
	for _, d := range days {
		m := d[:7]
		if len(months) == 0 || months[len(months)-1].Month != m {
			months = append(months, TLMonth{Month: m})
		}
		last := &months[len(months)-1]
		last.Days = append(last.Days, s.tlDay(id, d))
	}
	s.render(w, "timeline", map[string]any{"Months": months})
}

// TreeItem — ветка дерева: значение сущности + записи с ней.
type TreeItem struct {
	Value string
	Hits  []services.HistoryHit
}

// TreeGroup — корень дерева: люди или проекты.
type TreeGroup struct {
	Icon  string
	Label string
	Items []TreeItem
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	id, _ := s.userID(r)
	var groups []TreeGroup
	for _, g := range [][3]string{{"person", "👥", "Люди"}, {"project", "#️⃣", "Проекты"}} {
		tg := TreeGroup{Icon: g[1], Label: g[2]}
		values, _ := s.store.EntityValues(id, g[0], 30)
		for _, v := range values {
			hits, _, _ := services.EntityHistory(s.store, id, g[0], v, 10)
			if len(hits) == 0 {
				continue
			}
			tg.Items = append(tg.Items, TreeItem{Value: v, Hits: hits})
		}
		groups = append(groups, tg)
	}
	s.render(w, "tree", map[string]any{"Groups": groups})
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
		month = s.userNow(id).Format("2006-01")
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

// handleRedeem GET: проверяет magic link БЕЗ гашения и показывает кнопку входа.
// Гашение только по POST: GET-запросы шлют и краулеры превью (Telegram),
// а они за POST не ходят — иначе ссылку съедает превью до пользователя.
func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/r/"))
	if raw == "" || strings.Contains(raw, "/") {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if _, ok, _ := s.sess.CheckWebToken(raw); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "expired", nil)
		return
	}
	s.render(w, "confirm", map[string]any{"Token": raw})
}

// handleConsume POST: гасит magic link, ставит сессионный cookie.
func (s *Server) handleConsume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.FormValue("t"))
	userID, ok, err := s.sess.CheckWebToken(raw)
	if err != nil || !ok {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "expired", nil)
		return
	}
	if err := s.sess.DropWebToken(raw); err != nil {
		http.Error(w, "token error", http.StatusInternalServerError)
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
