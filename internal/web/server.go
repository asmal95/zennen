package web

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"time"

	"diarybot/internal/config"
	"diarybot/internal/db"
	"diarybot/internal/services"
)

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
	}).ParseFS(tmplFS, "templates/*.html"))
	return &Server{store: store, sess: sess, cfg: cfg, tmpl: t}
}

func (s *Server) userID(r *http.Request) (int64, bool) {
	v := r.Context().Value(ctxKey{})
	id, ok := v.(int64)
	return id, ok
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
	return mux
}

func today() string { return time.Now().Format("2006-01-02") }
