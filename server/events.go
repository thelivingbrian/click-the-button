package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const eventWeek = int64(7 * 24 * time.Hour / time.Millisecond)

var errSignIn = errors.New("Sign in is required for this action. Accounts are not available yet.")

type Discussion struct {
	ID           int64
	Handle, Body string
	At           int64
}

// Formats describe abilities, not seeded community activity.
func formats() []Poll {
	var result []Poll
	for _, kind := range []string{"one", "contest", "tug", "pulse", "scale", "stars", "heat"} {
		p := preset(kind)
		p.Title = p.KindLabel()
		p.Subtitle = p.Rule()
		result = append(result, p)
	}
	return result
}

func newEvent(kind, title string, options []string, now int64) Poll {
	p := preset(kind)
	p.ID, p.Title, p.Subtitle = randomID(), title, ""
	p.Created, p.Updated = now, now
	if len(options) > 0 {
		p.Options = options
	}
	p.Counts, p.Energy = make([]int64, len(p.Options)), make([]float64, len(p.Options))
	return p
}

// Editorial fallback candidates keep a ballot available before accounts launch.
// Community submissions take these slots in subsequent ballots.
func editorialEvents(now int64) []Poll {
	return []Poll{
		newEvent("one", "Should publicly funded research be free to read?", []string{"Yes", "No", "It depends"}, now),
		newEvent("one", "Which should we explore next: the ocean or space?", []string{"The ocean", "Space"}, now),
		newEvent("scale", "How optimistic are you about the next ten years?", nil, now),
	}
}

func insertEvent(tx *sql.Tx, p Poll) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO polls(id,status,state) VALUES(?,?,?)", p.ID, p.Status, b)
	return err
}

func (s *Station) initializeEvents(now int64) error {
	_, err := s.db.Exec(`
 CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY, provider TEXT NOT NULL, subject TEXT NOT NULL, UNIQUE(provider,subject));
 CREATE TABLE IF NOT EXISTS account_sessions(session TEXT PRIMARY KEY REFERENCES sessions(id), account TEXT NOT NULL REFERENCES accounts(id));
 CREATE TABLE IF NOT EXISTS next_ballots(poll TEXT NOT NULL REFERENCES polls(id), account TEXT NOT NULL REFERENCES accounts(id), PRIMARY KEY(poll,account));
 CREATE TABLE IF NOT EXISTS event_schedule(id INTEGER PRIMARY KEY CHECK(id=1), main TEXT NOT NULL REFERENCES polls(id), next TEXT NOT NULL REFERENCES polls(id), ends INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS discussion(id INTEGER PRIMARY KEY, poll TEXT NOT NULL REFERENCES polls(id), session TEXT NOT NULL REFERENCES sessions(id), handle TEXT NOT NULL, body TEXT NOT NULL, ts INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS discussion_event_time ON discussion(poll,ts);
 CREATE INDEX IF NOT EXISTS discussion_session_time ON discussion(session,ts);
 `)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow("SELECT count(*) FROM event_schedule").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		p := newEvent("one", "Which Millennium Problem will be solved next?", []string{"P vs NP", "Riemann hypothesis", "Hodge conjecture", "Yang–Mills existence and mass gap", "Birch and Swinnerton-Dyer conjecture"}, now)
		p.ID, p.Scope, p.Ends = "millennium-001", "main", now+eventWeek
		p.Subtitle = "Following the September 8 Navier–Stokes solution announcement."
		if err = insertEvent(tx, p); err != nil {
			return err
		}
		next, err := makeNext(tx, p, now)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO event_schedule(id,main,next,ends) VALUES(1,?,?,?)", p.ID, next.ID, p.Ends); err != nil {
			return err
		}
	}
	if err = s.rotateTx(tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func makeNext(tx *sql.Tx, main Poll, now int64) (Poll, error) {
	next := newEvent("one", "Choose next week’s main event", nil, now)
	next.Scope, next.Ends = "selection", main.Ends
	rows, err := tx.Query("SELECT state FROM polls WHERE status='live' ORDER BY rowid")
	if err != nil {
		return next, err
	}
	for rows.Next() {
		var raw []byte
		var p Poll
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			break
		}
		if p.Scope == "community" && p.Title != main.Title && len(next.Candidates) < 3 {
			next.Candidates = append(next.Candidates, p)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return next, err
	}
	if rowErr != nil {
		return next, rowErr
	}
	for _, p := range editorialEvents(now) {
		if len(next.Candidates) < 3 && p.Title != main.Title {
			next.Candidates = append(next.Candidates, p)
		}
	}
	next.Options = nil
	for _, p := range next.Candidates {
		next.Options = append(next.Options, p.Title)
	}
	next.Counts, next.Energy = make([]int64, len(next.Options)), make([]float64, len(next.Options))
	return next, insertEvent(tx, next)
}

func (s *Station) rotateTx(tx *sql.Tx, now int64) error {
	var mainID, nextID string
	var ends int64
	if err := tx.QueryRow("SELECT main,next,ends FROM event_schedule WHERE id=1").Scan(&mainID, &nextID, &ends); err != nil {
		return err
	}
	if now < ends {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow("SELECT state FROM polls WHERE id=?", nextID).Scan(&raw); err != nil {
		return err
	}
	var next Poll
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	winner := 0
	for i, count := range next.Counts {
		if count > next.Counts[winner] {
			winner = i
		}
	}
	// Ties (including no votes) go to the first listed candidate.
	chosen := next.Candidates[winner]
	for _, id := range []string{mainID, nextID} {
		if err := archiveTx(tx, id, ends); err != nil {
			return err
		}
	}
	// After downtime, open one new round on the original weekly boundary.
	// Do not invent events or activity for weeks when the service was offline.
	starts := ends + ((now-ends)/eventWeek)*eventWeek
	main := newEvent(chosen.Kind, chosen.Title, chosen.Options, starts)
	main.Scope, main.Creator, main.Ends = "main", chosen.Creator, starts+eventWeek
	if chosen.Scope == "community" {
		if err := archiveTx(tx, chosen.ID, ends); err != nil {
			return err
		}
	}
	if err := insertEvent(tx, main); err != nil {
		return err
	}
	ballot, err := makeNext(tx, main, starts)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE event_schedule SET main=?,next=?,ends=? WHERE id=1", main.ID, ballot.ID, main.Ends)
	return err
}

func (s *Station) advance(now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.rotateTx(tx, now); err != nil {
		return err
	}
	// Community events expire after a week, even when never promoted.
	rows, err := tx.Query("SELECT state FROM polls WHERE status='live'")
	if err != nil {
		return err
	}
	var expired []Poll
	for rows.Next() {
		var raw []byte
		var p Poll
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			break
		}
		if p.Scope == "community" && now >= p.Ends {
			expired = append(expired, p)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, p := range expired {
		if err = archiveTx(tx, p.ID, p.Ends); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Station) comments(id string) ([]Discussion, error) {
	rows, err := s.db.Query("SELECT id,handle,body,ts FROM discussion WHERE poll=? ORDER BY id DESC LIMIT 50", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Discussion
	for rows.Next() {
		var d Discussion
		if err = rows.Scan(&d.ID, &d.Handle, &d.Body, &d.At); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *Station) discuss(id string, session Session, body string, now int64) error {
	body = strings.TrimSpace(body)
	if utf8.RuneCountInString(body) < 1 || utf8.RuneCountInString(body) > 500 {
		return errors.New("Write between 1 and 500 characters.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	var p Poll
	if err = tx.QueryRow("SELECT state FROM polls WHERE id=?", id).Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.Status != "live" || (p.Ends > 0 && now >= p.Ends) {
		return errClosed
	}
	if p.Scope != "main" && p.Scope != "community" {
		return errors.New("Discussion is unavailable for this event.")
	}
	var last sql.NullInt64
	if err = tx.QueryRow("SELECT max(ts) FROM discussion WHERE session=?", session.ID).Scan(&last); err != nil {
		return err
	}
	if last.Valid && now-last.Int64 < 15000 {
		return errors.New("Please wait 15 seconds between comments.")
	}
	if _, err = tx.Exec("INSERT INTO discussion(poll,session,handle,body,ts) VALUES(?,?,?,?,?)", id, session.ID, session.Handle, body, now); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM discussion WHERE poll=? AND id NOT IN (SELECT id FROM discussion WHERE poll=? ORDER BY id DESC LIMIT 50)", id, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Station) createEvent(session Session, kind, title, options string, now int64) (string, error) {
	// Trust only a server-bound account; a browser handle never grants permissions.
	actual, err := s.session(session.ID)
	if err != nil || actual.AccountID == "" {
		return "", errSignIn
	}
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) < 8 || utf8.RuneCountInString(title) > 160 {
		return "", errors.New("Use a title between 8 and 160 characters.")
	}
	valid := false
	for _, p := range formats() {
		if p.Kind == kind {
			valid = true
		}
	}
	if !valid {
		return "", errors.New("Choose an available event format.")
	}
	var choices []string
	if kind == "one" || kind == "contest" || kind == "tug" || kind == "pulse" {
		seen := map[string]bool{}
		for _, option := range strings.Split(options, "\n") {
			option = strings.TrimSpace(option)
			if option == "" {
				continue
			}
			key := strings.ToLower(option)
			if utf8.RuneCountInString(option) > 80 || seen[key] {
				return "", errors.New("Options must be unique and at most 80 characters.")
			}
			seen[key] = true
			choices = append(choices, option)
		}
		min, max := 2, 8
		if kind == "tug" {
			max = 2
		}
		if kind == "pulse" {
			min, max = 1, 1
		}
		if len(choices) < min || len(choices) > max {
			return "", fmt.Errorf("This format needs %d–%d options, one per line.", min, max)
		}
	}
	p := newEvent(kind, title, choices, now)
	p.Scope, p.Creator, p.Ends = "community", actual.AccountID, now+eventWeek
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM polls WHERE json_extract(state,'$.creator')=? AND json_extract(state,'$.created')>?", actual.AccountID, now-int64(24*time.Hour/time.Millisecond)).Scan(&count); err != nil {
		return "", err
	}
	if count >= 3 {
		return "", errors.New("You can submit up to three events per day.")
	}
	if err = insertEvent(tx, p); err != nil {
		return "", err
	}
	return p.ID, tx.Commit()
}
