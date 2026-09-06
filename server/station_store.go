package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

var errClosed = errors.New("This poll is archived. Start a rematch to keep playing.")
var errVoted = errors.New("This browser has already voted in this poll.")
var errCooldown = errors.New("A little breather. Try again in a second.")

type Poll struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle"`
	Options  []string  `json:"options"`
	Counts   []int64   `json:"counts"`
	Energy   []float64 `json:"energy"`
	Peak     float64   `json:"peak"`
	PeakAt   int64     `json:"peakAt,omitempty"`
	Updated  int64     `json:"updated"`
	Created  int64     `json:"created"`
	Closed   int64     `json:"closed,omitempty"`
	Status   string    `json:"status"`
	Version  int64     `json:"version"`
	Parent   string    `json:"parent,omitempty"`
}

func (p Poll) Total() int64 {
	var n int64
	for _, c := range p.Counts {
		n += c
	}
	return n
}
func (p Poll) Average() string {
	if p.Total() == 0 {
		return "—"
	}
	var n int64
	for i, c := range p.Counts {
		n += int64(i+1) * c
	}
	return fmt.Sprintf("%.1f", float64(n)/float64(p.Total()))
}
func (p Poll) EnergyTotal() float64 {
	var n float64
	for _, e := range p.Energy {
		n += e
	}
	return n
}
func (p Poll) Balance() float64 {
	if p.Total() == 0 {
		return 50
	}
	return 100 * float64(p.Counts[1]) / float64(p.Total())
}
func (p Poll) KindLabel() string {
	return map[string]string{"contest": "Click contest", "pulse": "Pulse", "tug": "Tug of war", "scale": "1–10 scale", "stars": "Star rating", "one": "One vote", "heat": "Heat map"}[p.Kind]
}
func (p Poll) Rule() string {
	return map[string]string{
		"contest": "Pick a side. Repeat clicks welcome.", "pulse": "Every tap adds energy. Energy halves every 30 seconds.",
		"tug":   "Pull left or right. Every click shifts the balance of all pulls.",
		"scale": "Choose 1–10. Repeat ratings welcome; the average includes every click.",
		"stars": "Give 1–5 stars. Repeat ratings welcome; every rating counts.",
		"one":   "One choice per browser. Clearing cookies allows another vote.",
		"heat":  "Choose a square to add heat. Repeat clicks welcome; heat is cumulative."}[p.Kind]
}
func (p *Poll) decay(now int64) {
	if p.Kind != "pulse" || p.Status != "live" {
		return
	}
	elapsed := math.Max(0, float64(now-p.Updated)/1000)
	for i := range p.Energy {
		p.Energy[i] *= math.Exp2(-elapsed / 30)
	}
	p.Updated = now
}

type Session struct{ ID, Handle string }
type Activity struct {
	Title, Handle, Option string
	At                    int64
}
type Station struct {
	db        *sql.DB
	mu        sync.Mutex
	legacyDir string
}

func randomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func openStation(path, legacy string) (*Station, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Station{db: db, legacyDir: legacy}
	_, err = db.Exec(`
 CREATE TABLE IF NOT EXISTS polls(id TEXT PRIMARY KEY, status TEXT NOT NULL, parent TEXT UNIQUE, state BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, handle TEXT NOT NULL DEFAULT '', tokens REAL NOT NULL DEFAULT 8, updated INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS ballots(poll TEXT NOT NULL REFERENCES polls(id), session TEXT NOT NULL REFERENCES sessions(id), PRIMARY KEY(poll,session));
 CREATE TABLE IF NOT EXISTS actions(poll TEXT NOT NULL REFERENCES polls(id), session TEXT NOT NULL, request TEXT NOT NULL, PRIMARY KEY(poll,session,request));
 CREATE TABLE IF NOT EXISTS history(poll TEXT NOT NULL REFERENCES polls(id), version INTEGER NOT NULL, state BLOB NOT NULL, PRIMARY KEY(poll,version));
 CREATE TABLE IF NOT EXISTS archives(poll TEXT PRIMARY KEY REFERENCES polls(id), payload BLOB NOT NULL, sha256 TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS activity(id INTEGER PRIMARY KEY, poll TEXT NOT NULL, title TEXT NOT NULL, handle TEXT NOT NULL, option TEXT NOT NULL, ts INTEGER NOT NULL);
 CREATE TRIGGER IF NOT EXISTS frozen_poll BEFORE UPDATE ON polls WHEN OLD.status='archived' BEGIN SELECT RAISE(ABORT,'archived poll is immutable'); END;
 CREATE TRIGGER IF NOT EXISTS frozen_archive_update BEFORE UPDATE ON archives BEGIN SELECT RAISE(ABORT,'archive is immutable'); END;
 CREATE TRIGGER IF NOT EXISTS frozen_archive_delete BEFORE DELETE ON archives BEGIN SELECT RAISE(ABORT,'archive is immutable'); END;
 `)
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, kind := range []string{"tug", "pulse", "contest", "scale", "stars", "heat", "one"} {
		p := preset(kind)
		p.ID = kind
		body, _ := json.Marshal(p)
		if _, err = db.Exec("INSERT OR IGNORE INTO polls(id,status,state) VALUES(?,'live',?)", p.ID, body); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

func preset(kind string) Poll {
	p := Poll{ID: randomID(), Kind: kind, Status: "live", Created: time.Now().UnixMilli(), Updated: time.Now().UnixMilli()}
	switch kind {
	case "tug":
		p.Title = "Where are we headed?"
		p.Subtitle = "A little push. A collective direction."
		p.Options = []string{"Take it slow", "Go all out"}
	case "pulse":
		p.Title = "Keep the station glowing"
		p.Subtitle = "A small spark becomes a shared moment."
		p.Options = []string{"Add a little energy"}
	case "contest":
		p.Title = "What’s the soundtrack?"
		p.Subtitle = "Two moods. As many clicks as you feel."
		p.Options = []string{"Late-night jazz", "Dance-floor disco"}
	case "scale":
		p.Title = "How’s your day moving?"
		p.Subtitle = "From crawling along to full steam ahead."
		for i := 1; i <= 10; i++ {
			p.Options = append(p.Options, fmt.Sprint(i))
		}
	case "stars":
		p.Title = "Rate this little corner of the internet"
		p.Subtitle = "Leave a little constellation."
		p.Options = []string{"1 star", "2 stars", "3 stars", "4 stars", "5 stars"}
	case "heat":
		p.Title = "Leave your mark"
		p.Subtitle = "One shared canvas. Find your square."
		for i := 0; i < 25; i++ {
			p.Options = append(p.Options, fmt.Sprintf("Row %d, column %d", i/5+1, i%5+1))
		}
	case "one":
		p.Title = "The next station break?"
		p.Subtitle = "A quieter question. One choice."
		p.Options = []string{"Coffee break", "Fresh-air break", "One more song"}
	}
	p.Counts = make([]int64, len(p.Options))
	p.Energy = make([]float64, len(p.Options))
	return p
}

func (s *Station) poll(id string) (Poll, error) {
	var p Poll
	var b []byte
	err := s.db.QueryRow("SELECT state FROM polls WHERE id=?", id).Scan(&b)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}
func (s *Station) list(status string) ([]Poll, error) {
	rows, err := s.db.Query("SELECT state FROM polls WHERE status=? ORDER BY rowid LIMIT 100", status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Poll
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var p Poll
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		p.decay(time.Now().UnixMilli())
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *Station) session(id string) (Session, error) {
	var v Session
	err := s.db.QueryRow("SELECT id,handle FROM sessions WHERE id=?", id).Scan(&v.ID, &v.Handle)
	return v, err
}
func (s *Station) newSession() (Session, error) {
	v := Session{ID: randomID()}
	_, err := s.db.Exec("INSERT INTO sessions(id,updated) VALUES(?,?)", v.ID, time.Now().UnixMilli())
	return v, err
}

// A single local transaction orders acceptance, deduplication, totals and closure.
// The mutex is only an optimization for this one-process prototype; all writes
// below also use a database transaction, with one SQLite connection per process.
func (s *Station) click(id string, session Session, choice int, requestID string, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b []byte
	if err = tx.QueryRow("SELECT state FROM polls WHERE id=?", id).Scan(&b); err != nil {
		return err
	}
	var p Poll
	if err = json.Unmarshal(b, &p); err != nil {
		return err
	}
	if p.Status != "live" {
		return errClosed
	}
	if choice < 0 || choice >= len(p.Options) {
		return errors.New("Choose one of the available options.")
	}
	var exists int
	if err = tx.QueryRow("SELECT count(*) FROM actions WHERE poll=? AND session=? AND request=?", id, session.ID, requestID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	var tokens float64
	var updated int64
	if err = tx.QueryRow("SELECT tokens,updated FROM sessions WHERE id=?", session.ID).Scan(&tokens, &updated); err != nil {
		return err
	}
	tokens = math.Min(8, tokens+math.Max(0, float64(now-updated)/1000)*4)
	if tokens < 1 {
		return errCooldown
	}
	if p.Kind == "one" {
		if err = tx.QueryRow("SELECT count(*) FROM ballots WHERE poll=? AND session=?", id, session.ID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return errVoted
		}
		if _, err = tx.Exec("INSERT INTO ballots(poll,session) VALUES(?,?)", id, session.ID); err != nil {
			return err
		}
	}
	p.decay(now)
	p.Counts[choice]++
	p.Energy[choice]++
	p.Updated = now
	p.Version++
	if p.Kind == "pulse" && p.EnergyTotal() > p.Peak {
		p.Peak = p.EnergyTotal()
		p.PeakAt = now
	}
	b, err = json.Marshal(p)
	if err != nil {
		return err
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE polls SET state=? WHERE id=? AND status='live'", []any{b, id}},
		{"INSERT INTO history(poll,version,state) VALUES(?,?,?)", []any{id, p.Version, b}},
		{"INSERT INTO actions(poll,session,request) VALUES(?,?,?)", []any{id, session.ID, requestID}},
		{"UPDATE sessions SET tokens=?,updated=? WHERE id=?", []any{tokens - 1, now, session.ID}},
		{"INSERT INTO activity(poll,title,handle,option,ts) VALUES(?,?,?,?,?)", []any{id, p.Title, session.Handle, p.Options[choice], now}},
		{"DELETE FROM activity WHERE id NOT IN (SELECT id FROM activity ORDER BY id DESC LIMIT 30)", nil},
	} {
		if _, err = tx.Exec(q.sql, q.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Archive struct {
	Format     int    `json:"format"`
	Poll       Poll   `json:"poll"`
	History    []Poll `json:"history"`
	Provenance string `json:"provenance"`
}

func (s *Station) archive(id string, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b []byte
	if err = tx.QueryRow("SELECT state FROM polls WHERE id=?", id).Scan(&b); err != nil {
		return err
	}
	var p Poll
	if err = json.Unmarshal(b, &p); err != nil {
		return err
	}
	if p.Status == "archived" {
		return nil
	}
	p.decay(now)
	p.Status = "archived"
	p.Closed = now
	p.Version++
	a := Archive{Format: 1, Poll: p, Provenance: "Local prototype activity. Counts represent accepted interactions under the displayed rules."}
	rows, err := tx.Query("SELECT state FROM history WHERE poll=? ORDER BY version", id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h Poll
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &h); err != nil {
			rows.Close()
			return err
		}
		a.History = append(a.History, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	b, err = json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO archives(poll,payload,sha256) VALUES(?,?,?)", id, payload, hex.EncodeToString(sum[:])); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE polls SET status='archived',state=? WHERE id=?", b, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Station) rematch(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.poll(id)
	if err != nil {
		return "", err
	}
	if p.Status != "archived" {
		return "", errors.New("Archive this round before starting a rematch.")
	}
	var existing string
	err = s.db.QueryRow("SELECT id FROM polls WHERE parent=?", id).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	p.ID = randomID()
	p.Parent = id
	p.Status = "live"
	p.Created = time.Now().UnixMilli()
	p.Updated = p.Created
	p.Closed = 0
	p.Version = 0
	p.Peak = 0
	p.PeakAt = 0
	p.Counts = make([]int64, len(p.Options))
	p.Energy = make([]float64, len(p.Options))
	b, _ := json.Marshal(p)
	_, err = s.db.Exec("INSERT INTO polls(id,status,parent,state) VALUES(?,'live',?,?)", p.ID, id, b)
	return p.ID, err
}

func validHandle(v string) bool {
	if len([]rune(v)) > 24 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(" _-", r)) {
			return false
		}
	}
	return true
}
