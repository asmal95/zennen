package web

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"diarybot/internal/config"
	"diarybot/internal/db"
	"diarybot/internal/services"
)

var nums10 = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

// aspectColors — цвета аспектов для таймлайна и дерева (тёмная тема).
var aspectColors = map[string]string{
	"fact": "6ab2ff", "thought": "c792ea", "plan": "ffd54f", "task": "69f0ae",
	"idea": "ffab40", "emotion": "ff8a80", "health": "80cbc4", "work": "90a4ae",
	"people": "f48fb1", "money": "aed581", "gratitude": "fff59d",
	"decision": "bcaaa4", "other": "78909c",
}

//go:embed templates/*.html
var tmplFS embed.FS

type ctxKey struct{}

type Server struct {
	store *db.Store // diary.db, read-only
	sess  *db.Store // sessions.db, read-write (сессии + кэш ревью)
	cfg   config.Config
	tmpl  *template.Template
}

func New(store, sess *db.Store, cfg config.Config) *Server {
	t := template.Must(template.New("").Funcs(template.FuncMap{
		"emoji":  services.EmojiFor,
		"aspect": services.AspectName,
		"hm": func(s string) string {
			if len(s) >= 16 {
				return s[11:16]
			}
			return s
		},
		// local переводит время в зону пользователя: {{local $.TZ .FireAt}}
		// (прямой вызов, не pipeline: в pipeline piped-значение идёт последним
		// аргументом и ломает порядок — ловили 500 на /reminders).
		"acolor": func(aspect string) string {
			if c, ok := aspectColors[aspect]; ok {
				return c
			}
			return "#78909c"
		},
		"local": func(tz string, t time.Time) string {
			if loc, err := time.LoadLocation(tz); err == nil {
				t = t.In(loc)
			}
			return t.Format("02.01 15:04")
		},
	}).ParseFS(tmplFS, "templates/*.html"))
	return &Server{store: store, sess: sess, cfg: cfg, tmpl: t}
}

func (s *Server) userID(r *http.Request) (int64, bool) {
	v := r.Context().Value(ctxKey{})
	id, ok := v.(int64)
	return id, ok
}

// userNow — «сейчас» по персональной зоне пользователя.
func (s *Server) userNow(userID int64) time.Time {
	return time.Now().In(services.UserLoc(s.store, userID, s.cfg.TZ))
}

// userTZName — имя зоны для шаблонов (форматирование времени).
func (s *Server) userTZName(userID int64) string {
	if u, err := s.store.GetUser(userID, s.cfg.TZ); err == nil && u.TZ != "" {
		return u.TZ
	}
	return s.cfg.TZ
}

// requireAuth пускает только с валидным сессионным cookie.
// Без него — /login с подсказкой взять ссылку в боте.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("diary_session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		id, ok, err := s.sess.LookupWebSession(c.Value)
		if err != nil || !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	}
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/r/", s.handleRedeem)
	mux.HandleFunc("/r/consume", s.handleConsume)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/{$}", s.requireAuth(s.handleIndex))
	mux.HandleFunc("/day", s.requireAuth(s.handleDay))
	mux.HandleFunc("/week", s.requireAuth(s.handleWeek))
	mux.HandleFunc("/tasks", s.requireAuth(s.handleTasks))
	mux.HandleFunc("/ideas", s.requireAuth(s.handleIdeas))
	mux.HandleFunc("/person", s.requireAuth(s.handlePerson))
	mux.HandleFunc("/project", s.requireAuth(s.handleProject))
	mux.HandleFunc("/search", s.requireAuth(s.handleSearch))
	mux.HandleFunc("/export", s.requireAuth(s.handleExport))
	mux.HandleFunc("/timeline", s.requireAuth(s.handleTimeline))
	mux.HandleFunc("/tree", s.requireAuth(s.handleTree))
	mux.HandleFunc("/reminders", s.requireAuth(s.handleReminders))
	mux.HandleFunc("POST /reminders/new", s.requireAuth(s.checkOrigin(s.handleReminderNew)))
	mux.HandleFunc("POST /reminders/cancel", s.requireAuth(s.checkOrigin(s.handleReminderCancel)))
	mux.HandleFunc("POST /tasks/close", s.requireAuth(s.checkOrigin(s.handleTaskClose)))
	mux.HandleFunc("POST /tasks/new", s.requireAuth(s.checkOrigin(s.handleTaskNew)))
	mux.HandleFunc("POST /energy", s.requireAuth(s.checkOrigin(s.handleEnergyVote)))
	return logRequests(mux)
}

// statusWriter перехватывает код ответа для логов.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// logRequests пишет method/path/status без секретов: токен в /r/<hex>
// и значения query (там может быть текст поиска) в лог не попадают.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		path := r.URL.Path
		if strings.HasPrefix(path, "/r/") {
			path = "/r/…"
		}
		q := ""
		if r.URL.RawQuery != "" {
			q = "?…"
		}
		log.Printf("%s %s%s %d %s", r.Method, path, q, sw.status, time.Since(start).Round(time.Millisecond))
	})
}
