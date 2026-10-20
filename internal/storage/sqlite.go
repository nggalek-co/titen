package storage

/*
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"titen/internal/transcript"
	"unsafe"
)

type Meeting struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	CreatedAt        time.Time  `json:"createdAt"`
	EndedAt          *time.Time `json:"endedAt,omitempty"`
	DocumentID       string     `json:"documentId"`
	NeedsDocument    bool       `json:"needsDocument"`
	DocumentAttempts int        `json:"documentAttempts"`
	DocumentRetryAt  time.Time  `json:"documentRetryAt"`
}
type Operation struct {
	ID, MeetingID, DocumentID, Text string
	Attempts                        int
	NextAt                          time.Time
}
type Store struct {
	mu sync.Mutex
	db *C.sqlite3
}

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		f.Close()
		if err = os.Chmod(path, 0600); err != nil {
			return nil, err
		}
	}
	s := &Store{}
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	if C.sqlite3_open(p, &s.db) != C.SQLITE_OK {
		return nil, fmt.Errorf("open sqlite")
	}
	C.sqlite3_busy_timeout(s.db, 5000)
	err := s.exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS meetings(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS segments(id TEXT PRIMARY KEY,meeting_id TEXT NOT NULL,data TEXT NOT NULL,start_ms INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS outbox(id TEXT PRIMARY KEY,meeting_id TEXT NOT NULL,text TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0); CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		C.sqlite3_close(s.db)
		s.db = nil
	}
	return nil
}
func q(v string) string {
	if strings.ContainsRune(v, 0) {
		return "CAST(X'" + fmt.Sprintf("%x", []byte(v)) + "' AS TEXT)"
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
func (s *Store) exec(sql string) error {
	p := C.CString(sql)
	defer C.free(unsafe.Pointer(p))
	if C.sqlite3_exec(s.db, p, nil, nil, nil) != C.SQLITE_OK {
		return fmt.Errorf("sqlite: %s", C.GoString(C.sqlite3_errmsg(s.db)))
	}
	return nil
}
func (s *Store) rows(sql string) ([][]string, error) {
	p := C.CString(sql)
	defer C.free(unsafe.Pointer(p))
	var stmt *C.sqlite3_stmt
	if C.sqlite3_prepare_v2(s.db, p, -1, &stmt, nil) != C.SQLITE_OK {
		return nil, fmt.Errorf("sqlite prepare: %s", C.GoString(C.sqlite3_errmsg(s.db)))
	}
	defer C.sqlite3_finalize(stmt)
	var rows [][]string
	for {
		r := C.sqlite3_step(stmt)
		if r == C.SQLITE_DONE {
			return rows, nil
		}
		if r != C.SQLITE_ROW {
			return nil, fmt.Errorf("sqlite step")
		}
		row := make([]string, int(C.sqlite3_column_count(stmt)))
		for i := range row {
			row[i] = string(C.GoBytes(unsafe.Pointer(C.sqlite3_column_text(stmt, C.int(i))), C.sqlite3_column_bytes(stmt, C.int(i))))
		}
		rows = append(rows, row)
	}
}
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}
func (s *Store) CreateMeeting(ctx context.Context, title string) (Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := Meeting{ID: NewID(), Title: title, CreatedAt: time.Now()}
	b, _ := json.Marshal(m)
	return m, s.exec("INSERT INTO meetings VALUES(" + q(m.ID) + "," + q(string(b)) + ")")
}
func (s *Store) meeting(id string) (Meeting, error) {
	r, e := s.rows("SELECT data FROM meetings WHERE id=" + q(id))
	if e != nil {
		return Meeting{}, e
	}
	if len(r) == 0 {
		return Meeting{}, fmt.Errorf("meeting not found")
	}
	var m Meeting
	e = json.Unmarshal([]byte(r[0][0]), &m)
	return m, e
}
func (s *Store) update(m Meeting) error {
	b, _ := json.Marshal(m)
	return s.exec("UPDATE meetings SET data=" + q(string(b)) + " WHERE id=" + q(m.ID))
}
func (s *Store) FinishMeeting(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.meeting(id)
	if e != nil {
		return e
	}
	now := time.Now()
	m.EndedAt = &now
	return s.update(m)
}
func (s *Store) SetDocument(ctx context.Context, id, doc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.meeting(id)
	if e != nil {
		return e
	}
	if m.DocumentID != "" && m.DocumentID != doc {
		return fmt.Errorf("this meeting already has a Google document; changing it could lose sync checkpoints")
	}
	if doc == "" {
		return fmt.Errorf("document ID is required")
	}
	for _, r := range doc {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("enter the document ID, not its full URL")
		}
	}
	m.DocumentID = doc
	m.NeedsDocument = false
	return s.update(m)
}
func (s *Store) Meetings(ctx context.Context, search string) ([]Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.rows("SELECT DISTINCT m.data FROM meetings m LEFT JOIN segments s ON s.meeting_id=m.id WHERE m.data LIKE " + q("%"+search+"%") + " OR s.data LIKE " + q("%"+search+"%") + " ORDER BY m.rowid DESC")
	var out []Meeting
	for _, row := range r {
		var m Meeting
		if err := json.Unmarshal([]byte(row[0]), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, e
}
func (s *Store) Segments(ctx context.Context, id string) ([]transcript.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.rows("SELECT data FROM segments WHERE meeting_id=" + q(id) + " ORDER BY start_ms,rowid")
	var out []transcript.Segment
	for _, row := range r {
		var seg transcript.Segment
		if err := json.Unmarshal([]byte(row[0]), &seg); err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, e
}
func (s *Store) AddSegments(ctx context.Context, segs []transcript.Segment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer s.exec("ROLLBACK")
	for _, v := range segs {
		if v.MeetingID == "" {
			return fmt.Errorf("missing meeting ID")
		}
		if _, err := s.meeting(v.MeetingID); err != nil {
			return err
		}
		if v.StartMS < 0 || v.EndMS < v.StartMS {
			return fmt.Errorf("invalid transcript timestamps")
		}
		v.ID = v.Key()
		b, _ := json.Marshal(v)
		sql := "INSERT OR IGNORE INTO segments VALUES(" + q(v.ID) + "," + q(v.MeetingID) + "," + q(string(b)) + fmt.Sprintf(",%d)", v.StartMS)
		if err := s.exec(sql); err != nil {
			return err
		}
		if C.sqlite3_changes(s.db) > 0 {
			line := v.Text
			if err := s.exec("INSERT OR IGNORE INTO outbox(id,meeting_id,text) VALUES(" + q(v.ID) + "," + q(v.MeetingID) + "," + q(line) + ")"); err != nil {
				return err
			}
		}
	}
	return s.exec("COMMIT")
}
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exec("INSERT INTO settings VALUES(" + q(key) + "," + q(value) + ") ON CONFLICT(key) DO UPDATE SET value=excluded.value")
}
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.rows("SELECT value FROM settings WHERE key=" + q(key))
	if e != nil || len(r) == 0 {
		return "", e
	}
	return r[0][0], nil
}
func (s *Store) Pending(ctx context.Context) ([]Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	r, e := s.rows(fmt.Sprintf(`SELECT o.id,o.meeting_id,o.text,o.attempts FROM outbox o JOIN meetings m ON m.id=o.meeting_id WHERE json_extract(m.data,'$.documentId')<>'' AND o.next_at<=%d AND NOT EXISTS (SELECT 1 FROM outbox earlier WHERE earlier.meeting_id=o.meeting_id AND earlier.rowid<o.rowid AND earlier.next_at>%d) ORDER BY o.rowid LIMIT 100`, now, now))
	var out []Operation
	for _, row := range r {
		m, err := s.meeting(row[1])
		if err != nil {
			return nil, err
		}
		if m.DocumentID == "" {
			continue
		}
		o := Operation{ID: row[0], MeetingID: row[1], DocumentID: m.DocumentID, Text: row[2]}
		fmt.Sscan(row[3], &o.Attempts)
		out = append(out, o)
	}
	return out, e
}
func (s *Store) Ack(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exec("DELETE FROM outbox WHERE id=" + q(id))
}
func (s *Store) Retry(ctx context.Context, id string, attempts int, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exec(fmt.Sprintf("UPDATE outbox SET attempts=%d,next_at=%d WHERE id=", attempts, next.Unix()) + q(id))
}

func (s *Store) RequestDocument(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.meeting(id)
	if err != nil {
		return err
	}
	if m.DocumentID != "" {
		return nil
	}
	m.NeedsDocument = true
	return s.update(m)
}
func (s *Store) RetryDocument(ctx context.Context, id string, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.meeting(id)
	if err != nil {
		return err
	}
	m.DocumentAttempts++
	m.DocumentRetryAt = next
	return s.update(m)
}
