package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound — записи нет или она чужая (владение всегда проверяем).
var ErrNotFound = errors.New("entry not found")

const schema = `
CREATE TABLE IF NOT EXISTS entries(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  kind TEXT NOT NULL,
  raw_text TEXT DEFAULT '',
  transcript TEXT DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entries_user_day ON entries(user_id, day);
CREATE TABLE IF NOT EXISTS blocks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id INTEGER NOT NULL REFERENCES entries(id),
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  aspect TEXT NOT NULL,
  content TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_blocks_user_aspect ON blocks(user_id, aspect);
CREATE TABLE IF NOT EXISTS tasks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  entry_id INTEGER REFERENCES entries(id),
  text TEXT NOT NULL,
  due TEXT DEFAULT '',
  done INTEGER DEFAULT 0,
  created_at TEXT NOT NULL,
  completed_at TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_tasks_user_done ON tasks(user_id, done);
CREATE TABLE IF NOT EXISTS entities(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id INTEGER NOT NULL REFERENCES entries(id),
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  type TEXT NOT NULL,
  value TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entities_user_type ON entities(user_id, type);
CREATE TABLE IF NOT EXISTS reminders(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER REFERENCES tasks(id),
  user_id INTEGER NOT NULL,
  fire_at TEXT NOT NULL,
  kind TEXT DEFAULT 'deadline',
  sent INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS delegations(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  entry_id INTEGER REFERENCES entries(id),
  title TEXT NOT NULL,
  context TEXT DEFAULT '',
  due TEXT DEFAULT '',
  agent TEXT DEFAULT 'noop',
  status TEXT DEFAULT 'proposed',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_deleg_user_status ON delegations(user_id, status);
CREATE TABLE IF NOT EXISTS energy(
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  value INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (user_id, day)
);
CREATE TABLE IF NOT EXISTS web_sessions(
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS week_cache(
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  text TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (user_id, day)
);
CREATE TABLE IF NOT EXISTS users(
  user_id INTEGER PRIMARY KEY,
  tz TEXT NOT NULL DEFAULT '',
  tz_set INTEGER NOT NULL DEFAULT 0,
  last_morning_day TEXT NOT NULL DEFAULT '',
  last_evening_day TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
`

type Store struct {
	Path string
	db   *sql.DB
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := database.Exec(schema); err != nil {
		return nil, err
	}
	// Лёгкие миграции существующих БД: ошибка duplicate column — норма.
	for _, m := range []string{
		`ALTER TABLE tasks ADD COLUMN completed_at TEXT DEFAULT ''`,
	} {
		_, _ = database.Exec(m)
	}
	return &Store{Path: path, db: database}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Block struct {
	Aspect  string
	Content string
	Day     string
}

type Task struct {
	ID   int64
	Text string
	Due  string
	Done bool
}

type Delegation struct {
	ID     int64
	Title  string
	Agent  string
	Status string
	Due    string
}

type Note struct {
	ID         int64
	Day        string
	Kind       string
	Transcript string
	CreatedAt  string
}

type Entity struct {
	ID      int64
	EntryID int64
	Type    string
	Value   string
	Day     string
}

// EntitiesByType отдаёт все сущности типа (person/project/…) пользователя.
// Морфологический матчинг («Иван» = «Иваном») — в Go, объём личный.
func (s *Store) EntitiesByType(userID int64, etype string) ([]Entity, error) {
	rows, err := s.db.Query(
		`SELECT id, entry_id, type, value, day FROM entities WHERE user_id=? AND type=? ORDER BY id`,
		userID, etype)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.EntryID, &e.Type, &e.Value, &e.Day); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntryBlocks возвращает все блоки одной записи.
func (s *Store) EntryBlocks(entryID int64) ([]Block, error) {
	rows, err := s.db.Query(
		`SELECT day, aspect, content FROM blocks WHERE entry_id=? ORDER BY id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.Day, &b.Aspect, &b.Content); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) AddEntry(userID int64, day, kind, raw, transcript string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO entries(user_id, day, kind, raw_text, transcript, created_at) VALUES(?,?,?,?,?,?)`,
		userID, day, kind, raw, transcript, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) AddBlocks(entryID, userID int64, day string, blocks []Block) error {
	for _, b := range blocks {
		if _, err := s.db.Exec(
			`INSERT INTO blocks(entry_id, user_id, day, aspect, content) VALUES(?,?,?,?,?)`,
			entryID, userID, day, b.Aspect, b.Content); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AddEntities(entryID, userID int64, day string, entities []Entity) error {
	for _, e := range entities {
		if _, err := s.db.Exec(
			`INSERT INTO entities(entry_id, user_id, day, type, value) VALUES(?,?,?,?,?)`,
			entryID, userID, day, e.Type, e.Value); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AddTask(userID, entryID int64, text, due string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO tasks(user_id, entry_id, text, due, done, created_at) VALUES(?,?,?,?,0,?)`,
		userID, entryID, text, due, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DayBlocks(userID int64, day string) ([]Block, error) {
	rows, err := s.db.Query(`SELECT aspect, content FROM blocks WHERE user_id=? AND day=? ORDER BY id`, userID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		b.Day = day
		if err := rows.Scan(&b.Aspect, &b.Content); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) WeekBlocks(userID int64, days []string) ([]Block, error) {
	if len(days) == 0 {
		return nil, nil
	}
	q := `SELECT day, aspect, content FROM blocks WHERE user_id=? AND day IN (`
	args := []any{userID}
	for i, d := range days {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, d)
	}
	q += `) ORDER BY day, id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.Day, &b.Aspect, &b.Content); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) OpenTasks(userID int64) ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, text, due, done FROM tasks WHERE user_id=? AND done=0 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Due, &t.Done); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DoneTasksSince(userID int64, since time.Time) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, text, due, done FROM tasks WHERE user_id=? AND done=1 AND completed_at >= ? ORDER BY id`,
		userID, since.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Due, &t.Done); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CloseTask(userID, taskID int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE tasks SET done=1, completed_at=? WHERE id=? AND user_id=?`,
		time.Now().Format(time.RFC3339), taskID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) BlocksByAspect(userID int64, aspect string, limit int) ([]Block, error) {
	rows, err := s.db.Query(`SELECT day, content FROM blocks WHERE user_id=? AND aspect=? ORDER BY id DESC LIMIT ?`, userID, aspect, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		b.Aspect = aspect
		if err := rows.Scan(&b.Day, &b.Content); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) SearchBlocks(userID int64, query string, limit int) ([]Block, error) {
	rows, err := s.db.Query(`SELECT day, aspect, content FROM blocks WHERE user_id=? AND content LIKE ? ORDER BY id DESC LIMIT ?`,
		userID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.Day, &b.Aspect, &b.Content); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DistinctUsers() ([]int64, error) {
	rows, err := s.db.Query(`SELECT DISTINCT user_id FROM entries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var u int64
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) AddDelegation(userID, entryID int64, title, context, due, agent string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO delegations(user_id, entry_id, title, context, due, agent, status, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		userID, entryID, title, context, due, agent, "proposed", time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListDelegations(userID int64, limit int) ([]Delegation, error) {
	rows, err := s.db.Query(`SELECT id, title, agent, status, due FROM delegations WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delegation
	for rows.Next() {
		var d Delegation
		if err := rows.Scan(&d.ID, &d.Title, &d.Agent, &d.Status, &d.Due); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RecentNotes(userID int64, limit int) ([]Note, error) {
	rows, err := s.db.Query(`SELECT id, day, kind, transcript, created_at FROM entries WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Day, &n.Kind, &n.Transcript, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type Reminder struct {
	ID     int64
	TaskID int64
	UserID int64
	Text   string
	FireAt time.Time
	Kind   string
}

func (s *Store) AddReminder(taskID, userID int64, fireAt time.Time, kind string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO reminders(task_id, user_id, fire_at, kind, sent) VALUES(?,?,?,?,0)`,
		taskID, userID, fireAt.Format(time.RFC3339), kind)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DueReminders возвращает несработавшие напоминания с наступившим временем.
// Фильтр по времени — в Go, чтобы не зависеть от формата/зоны в строке.
func (s *Store) DueReminders(now time.Time) ([]Reminder, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.task_id, r.user_id, t.text, r.fire_at, r.kind
		 FROM reminders r JOIN tasks t ON t.id = r.task_id
		 WHERE r.sent = 0 ORDER BY r.fire_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		var fireAt string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.UserID, &r.Text, &fireAt, &r.Kind); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, fireAt)
		if err != nil {
			continue
		}
		if !t.After(now) {
			r.FireAt = t
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func (s *Store) MarkReminderSent(id int64) error {
	_, err := s.db.Exec(`UPDATE reminders SET sent=1 WHERE id=?`, id)
	return err
}

// UpcomingReminders — все несработавшие напоминания пользователя по времени.
func (s *Store) UpcomingReminders(userID int64) ([]Reminder, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.task_id, r.user_id, t.text, r.fire_at, r.kind
		 FROM reminders r JOIN tasks t ON t.id = r.task_id
		 WHERE r.sent = 0 AND r.user_id = ? ORDER BY r.fire_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		var fireAt string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.UserID, &r.Text, &fireAt, &r.Kind); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, fireAt)
		if err != nil {
			continue
		}
		r.FireAt = t
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteReminder отменяет напоминание (только своё).
func (s *Store) DeleteReminder(userID, id int64) error {
	_, err := s.db.Exec(`DELETE FROM reminders WHERE id=? AND user_id=?`, id, userID)
	return err
}

// EntryDay возвращает день записи с проверкой владения.
// Записи нет или она чужая — ErrNotFound.
func (s *Store) EntryDay(userID, entryID int64) (string, error) {
	var day string
	err := s.db.QueryRow(`SELECT day FROM entries WHERE id=? AND user_id=?`, entryID, userID).Scan(&day)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return day, nil
}

// EntryText возвращает текст записи (transcript, иначе raw) с проверкой владения.
func (s *Store) EntryText(userID, entryID int64) (string, bool, error) {
	var transcript, raw string
	err := s.db.QueryRow(`SELECT transcript, raw_text FROM entries WHERE id=? AND user_id=?`,
		entryID, userID).Scan(&transcript, &raw)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if transcript == "" {
		transcript = raw
	}
	return transcript, true, nil
}

// TasksByEntry — все задачи одной записи со статусами (для таймлайна).
func (s *Store) TasksByEntry(entryID int64) ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, text, due, done FROM tasks WHERE entry_id=? ORDER BY id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Due, &t.Done); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EntityValues — distinct-значения сущностей типа (корни дерева связей).
func (s *Store) EntityValues(userID int64, etype string, limit int) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT value FROM entities WHERE user_id=? AND type=? ORDER BY value LIMIT ?`,
		userID, etype, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// OpenTasksByEntry — открытые задачи одной записи (для кнопки «В задачу»: дубли не плодим).
func (s *Store) OpenTasksByEntry(entryID int64) ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, text, due, done FROM tasks WHERE entry_id=? AND done=0 ORDER BY id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Due, &t.Done); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClearDerived удаляет производные записи: блоки, сущности, открытые задачи
// с их несработавшими напоминаниями, proposed-делегации.
// Выполненные задачи и ушедшие в работу делегации НЕ трогаем — это история.
func (s *Store) ClearDerived(entryID, userID int64) error {
	queries := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM reminders WHERE sent=0 AND task_id IN (SELECT id FROM tasks WHERE entry_id=? AND done=0)`, []any{entryID}},
		{`DELETE FROM tasks WHERE entry_id=? AND user_id=? AND done=0`, []any{entryID, userID}},
		{`DELETE FROM blocks WHERE entry_id=?`, []any{entryID}},
		{`DELETE FROM entities WHERE entry_id=?`, []any{entryID}},
		{`DELETE FROM delegations WHERE entry_id=? AND status='proposed'`, []any{entryID}},
	}
	for _, qq := range queries {
		if _, err := s.db.Exec(qq.q, qq.args...); err != nil {
			return err
		}
	}
	return nil
}

// DeleteEntry удаляет запись и производные. Нет записи / чужая — ErrNotFound.
func (s *Store) DeleteEntry(userID, entryID int64) (bool, error) {
	if _, err := s.EntryDay(userID, entryID); err != nil {
		return false, err
	}
	if err := s.ClearDerived(entryID, userID); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`DELETE FROM entries WHERE id=? AND user_id=?`, entryID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteDay удаляет все записи дня пользователя. Возвращает их количество.
func (s *Store) DeleteDay(userID int64, day string) (int, error) {
	rows, err := s.db.Query(`SELECT id FROM entries WHERE user_id=? AND day=?`, userID, day)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := s.DeleteEntry(userID, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// UpdateTranscript заменяет сырьё записи при исправлении (kind происхождения сохраняем).
func (s *Store) UpdateTranscript(userID, entryID int64, text string) (bool, error) {
	res, err := s.db.Exec(`UPDATE entries SET raw_text=?, transcript=? WHERE id=? AND user_id=?`,
		text, text, entryID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

type ExportNote struct {
	ID         int64
	Day        string
	Kind       string
	Transcript string
	CreatedAt  string
	Blocks     []Block
}

// ExportMonth отдаёт заметки месяца с разборами одним JOIN-запросом.
// month строго вида "2006-01" — проверяется хендлером.
func (s *Store) ExportMonth(userID int64, month string) ([]ExportNote, error) {
	rows, err := s.db.Query(
		`SELECT e.id, e.day, e.kind, e.transcript, e.created_at, b.aspect, b.content
		 FROM entries e LEFT JOIN blocks b ON b.entry_id = e.id
		 WHERE e.user_id = ? AND e.day LIKE ? ORDER BY e.id, b.id`,
		userID, month+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportNote
	var lastID int64 = -1
	for rows.Next() {
		var id int64
		var day, kind, transcript, createdAt string
		var aspect, content sql.NullString
		if err := rows.Scan(&id, &day, &kind, &transcript, &createdAt, &aspect, &content); err != nil {
			return nil, err
		}
		if id != lastID {
			out = append(out, ExportNote{ID: id, Day: day, Kind: kind, Transcript: transcript, CreatedAt: createdAt})
			lastID = id
		}
		if aspect.Valid {
			last := &out[len(out)-1]
			last.Blocks = append(last.Blocks, Block{Aspect: aspect.String, Content: content.String, Day: day})
		}
	}
	return out, rows.Err()
}

// SetEnergy записывает энергию дня 1-10 (повторный тап обновляет).
func (s *Store) SetEnergy(userID int64, day string, value int) error {
	if value < 1 || value > 10 {
		return fmt.Errorf("energy must be 1..10")
	}
	_, err := s.db.Exec(
		`INSERT INTO energy(user_id, day, value, created_at) VALUES(?,?,?,?)
		 ON CONFLICT(user_id, day) DO UPDATE SET value=excluded.value, created_at=excluded.created_at`,
		userID, day, value, time.Now().Format(time.RFC3339))
	return err
}

// WeekEnergy возвращает энергию по дням (нет замера — дня нет в карте).
func (s *Store) WeekEnergy(userID int64, days []string) (map[string]int, error) {
	if len(days) == 0 {
		return map[string]int{}, nil
	}
	q := `SELECT day, value FROM energy WHERE user_id=? AND day IN (`
	args := []any{userID}
	for i, d := range days {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, d)
	}
	q += `)`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var day string
		var v int
		if err := rows.Scan(&day, &v); err != nil {
			return nil, err
		}
		out[day] = v
	}
	return out, rows.Err()
}

func webTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateWebToken выпускает токен сессии (сырой — только в ответ пользователю,
// в БД лежит только SHA-256). ttl: 15 минут для magic link, 30 дней для сессии.
func (s *Store) CreateWebToken(userID int64, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := time.Now()
	_, err := s.db.Exec(
		`INSERT INTO web_sessions(token_hash, user_id, created_at, expires_at) VALUES(?,?,?,?)`,
		webTokenHash(token), userID, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return token, nil
}

// CheckWebToken проверяет токен БЕЗ удаления (для страницы подтверждения:
// GET-запросы ходят и краулеры превью — гасить токен по ним нельзя).
// ok=false — токена нет или истёк.
func (s *Store) CheckWebToken(raw string) (int64, bool, error) {
	var userID int64
	var expires string
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM web_sessions WHERE token_hash=?`,
		webTokenHash(raw)).Scan(&userID, &expires)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil || !t.After(time.Now()) {
		return 0, false, nil
	}
	return userID, true, nil
}

// DropWebToken удаляет токен (плюс заодно подчищает просроченные).
func (s *Store) DropWebToken(raw string) error {
	h := webTokenHash(raw)
	if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE token_hash=?`, h); err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE expires_at < ?`, time.Now().Format(time.RFC3339))
	return nil
}

// LookupWebSession проверяет сессионный cookie-токен (без удаления).
func (s *Store) LookupWebSession(raw string) (int64, bool, error) {
	var userID int64
	var expires string
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM web_sessions WHERE token_hash=?`,
		webTokenHash(raw)).Scan(&userID, &expires)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil || !t.After(time.Now()) {
		return 0, false, nil
	}
	return userID, true, nil
}

// RevokeWebSessions отзывает все токены и сессии пользователя.
func (s *Store) RevokeWebSessions(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM web_sessions WHERE user_id=?`, userID)
	return err
}

// WeekCacheGet отдаёт кэшированный текст недельного ревью за день.
func (s *Store) WeekCacheGet(userID int64, day string) (string, bool, error) {
	var text string
	err := s.db.QueryRow(`SELECT text FROM week_cache WHERE user_id=? AND day=?`, userID, day).Scan(&text)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return text, true, nil
}

// WeekCacheSet сохраняет текст ревью (повторный показ в тот же день бесплатен).
func (s *Store) WeekCacheSet(userID int64, day, text string) error {
	_, err := s.db.Exec(
		`INSERT INTO week_cache(user_id, day, text, created_at) VALUES(?,?,?,?)
		 ON CONFLICT(user_id, day) DO UPDATE SET text=excluded.text, created_at=excluded.created_at`,
		userID, day, text, time.Now().Format(time.RFC3339))
	return err
}

// DayList возвращает дни пользователя с записями, свежие первыми.
func (s *Store) DayList(userID int64, limit int) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT day FROM entries WHERE user_id=? ORDER BY day DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DayEntries — заметки одного дня с разборами (для веба и экспорта дня).
func (s *Store) DayEntries(userID int64, day string) ([]ExportNote, error) {
	rows, err := s.db.Query(
		`SELECT e.id, e.day, e.kind, e.transcript, e.created_at, b.aspect, b.content
		 FROM entries e LEFT JOIN blocks b ON b.entry_id = e.id
		 WHERE e.user_id = ? AND e.day = ? ORDER BY e.id, b.id`,
		userID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportNote
	var lastID int64 = -1
	for rows.Next() {
		var id int64
		var d, kind, transcript, createdAt string
		var aspect, content sql.NullString
		if err := rows.Scan(&id, &d, &kind, &transcript, &createdAt, &aspect, &content); err != nil {
			return nil, err
		}
		if id != lastID {
			out = append(out, ExportNote{ID: id, Day: d, Kind: kind, Transcript: transcript, CreatedAt: createdAt})
			lastID = id
		}
		if aspect.Valid {
			last := &out[len(out)-1]
			last.Blocks = append(last.Blocks, Block{Aspect: aspect.String, Content: content.String, Day: d})
		}
	}
	return out, rows.Err()
}

type User struct {
	ID             int64
	TZ             string
	TZSet          bool
	LastMorningDay string
	LastEveningDay string
}

// GetUser возвращает пользователя, создавая строку с дефолтной зоной при первом обращении.
func (s *Store) GetUser(userID int64, defaultTZ string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT user_id, tz, tz_set, last_morning_day, last_evening_day FROM users WHERE user_id=?`,
		userID).Scan(&u.ID, &u.TZ, &u.TZSet, &u.LastMorningDay, &u.LastEveningDay)
	if err == sql.ErrNoRows {
		tz := defaultTZ
		if tz == "" {
			tz = "Europe/Moscow"
		}
		if _, err := s.db.Exec(`INSERT INTO users(user_id, tz, created_at) VALUES(?,?,?)`,
			userID, tz, time.Now().Format(time.RFC3339)); err != nil {
			return User{}, err
		}
		return User{ID: userID, TZ: tz}, nil
	}
	if err != nil {
		return User{}, err
	}
	if u.TZ == "" {
		u.TZ = defaultTZ
	}
	return u, nil
}

// SetUserTZ ставит зону (проверка валидности — на вызывающем через time.LoadLocation).
func (s *Store) SetUserTZ(userID int64, tz string) error {
	if _, err := s.GetUser(userID, tz); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE users SET tz=?, tz_set=1 WHERE user_id=?`, tz, userID)
	return err
}

// MarkDigest фиксирует отправку дайджеста за день (защита от повторов).
func (s *Store) MarkDigest(userID int64, kind, day string) error {
	col := "last_morning_day"
	if kind == "evening" {
		col = "last_evening_day"
	}
	_, err := s.db.Exec(fmt.Sprintf(`UPDATE users SET %s=? WHERE user_id=?`, col), day, userID)
	return err
}

// VacuumInto делает консистентный снапшот живой БД (для бэкапов).
// Путь строится из даты, инъекции исключены — параметр не нужен.
func (s *Store) VacuumInto(path string) error {
	_, err := s.db.Exec(fmt.Sprintf(`VACUUM INTO '%s'`, strings.ReplaceAll(path, `'`, ``)))
	return err
}
