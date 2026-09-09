package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type AdminAccount struct {
	ID, Email, Role string
	Suspended       bool
}
type AdminEvent struct {
	Poll              Poll
	Hidden, Nominated bool
}
type AuditEntry struct {
	Actor, Action, Target string
	At                    int64
}
type AdminPage struct {
	Events   []AdminEvent
	Accounts []AdminAccount
	Comments []Discussion
	Audit    []AuditEntry
	Paused   bool
}

func (s *Station) admin(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, false)
	if err != nil || v.Role != "admin" || v.Suspended {
		http.Error(w, "Administrator access required", 403)
		return
	}
	d := Page{Title: "Administration", Mode: "admin", Session: v}
	rows, err := s.db.Query(`SELECT p.state,COALESCE(m.hidden,0),COALESCE(m.nominated,0) FROM polls p LEFT JOIN moderation_events m ON m.poll=p.id WHERE p.status='live' ORDER BY p.rowid DESC LIMIT 100`)
	if err != nil {
		http.Error(w, "Could not load administration", 500)
		return
	}
	for rows.Next() {
		var e AdminEvent
		var raw []byte
		if err = rows.Scan(&raw, &e.Hidden, &e.Nominated); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &e.Poll); err != nil {
			break
		}
		if e.Poll.Scope != "" {
			d.Admin.Events = append(d.Admin.Events, e)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		http.Error(w, "Could not load events", 500)
		return
	}
	rows, err = s.db.Query("SELECT id,email,role,suspended FROM accounts ORDER BY created DESC LIMIT 100")
	if err != nil {
		http.Error(w, "Could not load accounts", 500)
		return
	}
	for rows.Next() {
		var a AdminAccount
		if err = rows.Scan(&a.ID, &a.Email, &a.Role, &a.Suspended); err != nil {
			break
		}
		d.Admin.Accounts = append(d.Admin.Accounts, a)
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		http.Error(w, "Could not load accounts", 500)
		return
	}
	rows, err = s.db.Query("SELECT id,handle,body,ts FROM discussion ORDER BY id DESC LIMIT 100")
	if err != nil {
		http.Error(w, "Could not load comments", 500)
		return
	}
	for rows.Next() {
		var c Discussion
		if err = rows.Scan(&c.ID, &c.Handle, &c.Body, &c.At); err != nil {
			break
		}
		d.Admin.Comments = append(d.Admin.Comments, c)
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		http.Error(w, "Could not load comments", 500)
		return
	}
	rows, err = s.db.Query("SELECT a.email,l.action,l.target,l.ts FROM admin_audit l JOIN accounts a ON a.id=l.actor ORDER BY l.id DESC LIMIT 30")
	if err != nil {
		http.Error(w, "Could not load audit log", 500)
		return
	}
	for rows.Next() {
		var a AuditEntry
		if err = rows.Scan(&a.Actor, &a.Action, &a.Target, &a.At); err != nil {
			break
		}
		d.Admin.Audit = append(d.Admin.Audit, a)
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		http.Error(w, "Could not load audit log", 500)
		return
	}
	if err = s.db.QueryRow("SELECT submissions_paused FROM site_settings WHERE id=1").Scan(&d.Admin.Paused); err != nil {
		http.Error(w, "Could not load settings", 500)
		return
	}
	renderPage(w, "page", d)
}

func (s *Station) adminAction(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, false)
	if err != nil {
		http.Error(w, "Administrator access required", 403)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	if err = s.moderate(v, r.FormValue("action"), r.FormValue("target"), time.Now().UnixMilli()); err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	target := "/admin"
	if r.FormValue("return") == "home" {
		target = "/"
	} else if back, ok := strings.CutPrefix(r.FormValue("return"), "poll:"); ok {
		target = "/poll/" + url.PathEscape(back)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Station) moderate(session Session, action, target string, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := sessionFrom(tx, session.ID, now)
	if err != nil || actor.Role != "admin" || actor.Suspended {
		return errors.New("Administrator access required")
	}
	switch action {
	case "hide", "restore", "nominate", "unnominate", "delete-event":
		var raw []byte
		var p Poll
		if err = tx.QueryRow("SELECT state FROM polls WHERE id=?", target).Scan(&raw); err != nil {
			return errors.New("Event not found")
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if p.Scope != "community" {
			return errors.New("Only community events can be moderated here; the main event rotates automatically")
		}
		if action == "nominate" && p.Status != "live" {
			return errors.New("Only open events can enter the upcoming ballot")
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO moderation_events(poll) VALUES(?)", target); err != nil {
			return err
		}
		switch action {
		case "hide", "delete-event":
			_, err = tx.Exec("UPDATE moderation_events SET hidden=1,nominated=0 WHERE poll=?", target)
		case "restore":
			_, err = tx.Exec("UPDATE moderation_events SET hidden=0 WHERE poll=?", target)
		case "nominate":
			_, err = tx.Exec("UPDATE moderation_events SET nominated=1 WHERE poll=? AND hidden=0", target)
		case "unnominate":
			_, err = tx.Exec("UPDATE moderation_events SET nominated=0 WHERE poll=?", target)
		}
		if err != nil {
			return err
		}
		if action == "delete-event" {
			if err = archiveTx(tx, target, now); err != nil {
				return err
			}
		}
		if action == "hide" || action == "delete-event" {
			if _, err = tx.Exec("DELETE FROM discussion WHERE poll=?", target); err != nil {
				return err
			}
		}
	case "delete-suggestion":
		ballot, candidate, ok := strings.Cut(target, ":")
		if !ok {
			return errors.New("Invalid suggestion")
		}
		var raw []byte
		if err = tx.QueryRow("SELECT p.state FROM polls p JOIN event_schedule e ON e.next=p.id WHERE p.id=? AND p.status='live'", ballot).Scan(&raw); err != nil {
			return errors.New("This ballot is no longer open")
		}
		var p Poll
		if err = json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if now >= p.Ends {
			return errClosed
		}
		found := false
		for _, c := range p.Candidates {
			found = found || c.ID == candidate
		}
		if !found {
			return errors.New("Suggestion not found")
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO withdrawn_candidates(ballot,candidate) VALUES(?,?)", ballot, candidate); err != nil {
			return err
		}
	case "delete-comment":
		id, e := strconv.ParseInt(target, 10, 64)
		if e != nil {
			return errors.New("Invalid comment")
		}
		if _, err = tx.Exec("DELETE FROM discussion WHERE id=?", id); err != nil {
			return err
		}
	case "suspend", "restore-account":
		var role string
		if err = tx.QueryRow("SELECT role FROM accounts WHERE id=?", target).Scan(&role); err != nil {
			return errors.New("Account not found")
		}
		if target == actor.AccountID || role == "admin" {
			return errors.New("Administrator accounts cannot be suspended here")
		}
		if _, err = tx.Exec("UPDATE accounts SET suspended=? WHERE id=?", action == "suspend", target); err != nil {
			return err
		}
	case "pause-submissions", "resume-submissions":
		target = "community-submissions"
		if _, err = tx.Exec("UPDATE site_settings SET submissions_paused=? WHERE id=1", action == "pause-submissions"); err != nil {
			return err
		}
	default:
		return errors.New("Unknown administrator action")
	}
	if _, err = tx.Exec("INSERT INTO admin_audit(actor,action,target,ts) VALUES(?,?,?,?)", actor.AccountID, action, target, now); err != nil {
		return err
	}
	return tx.Commit()
}

func hiddenEvent(db queryRower, id string) (bool, error) {
	var hidden bool
	err := db.QueryRow("SELECT hidden FROM moderation_events WHERE poll=?", id).Scan(&hidden)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return hidden, err
}

func unavailableCandidate(db queryRower, ballot, candidate string) (bool, error) {
	hidden, err := hiddenEvent(db, candidate)
	if err != nil || hidden {
		return hidden, err
	}
	var withdrawn bool
	err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM withdrawn_candidates WHERE ballot=? AND candidate=?)", ballot, candidate).Scan(&withdrawn)
	return withdrawn, err
}
func (s *Station) visibleEvent(w http.ResponseWriter, r *http.Request, id string) bool {
	hidden, err := hiddenEvent(s.db, id)
	if err != nil {
		http.Error(w, "Could not load event", 500)
		return false
	}
	if hidden {
		http.NotFound(w, r)
		return false
	}
	return true
}
