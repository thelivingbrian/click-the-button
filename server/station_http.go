package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var stationTemplates = template.Must(template.New("station").Funcs(template.FuncMap{
	"number": func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"date": func(v int64) string {
		if v == 0 {
			return "—"
		}
		return time.UnixMilli(v).UTC().Format("Jan 2, 15:04 UTC")
	},
	"inc": func(v int) int { return v + 1 },
	"percent": func(n, total int64) float64 {
		if total == 0 {
			return 0
		}
		return 100 * float64(n) / float64(total)
	},
	"heat": func(n int64, counts []int64) float64 {
		var max int64 = 1
		for _, c := range counts {
			if c > max {
				max = c
			}
		}
		return 0.08 + 0.85*float64(n)/float64(max)
	},
}).ParseFiles("templates/station.html", "templates/legacy.html", "templates/admin.html"))

type Page struct {
	Title, Mode, Message string
	Poll                 Poll
	Polls                []Poll
	Archives             []Poll
	Activity             []Activity
	Session              Session
	Next                 Poll
	Formats              []Poll
	Discussion           []Discussion
	Voted, NextVoted     bool
	AuthEnabled          bool
	Admin                AdminPage
}

func (s *Station) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if err := s.db.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "revision": buildRevision})
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /poll/{id}", s.detail)
	mux.HandleFunc("GET /live", s.live)
	mux.HandleFunc("POST /poll/{id}/click/{choice}", s.vote)
	mux.HandleFunc("POST /profile", s.profile)
	mux.HandleFunc("GET /create", s.studio)
	mux.HandleFunc("POST /events", s.submitEvent)
	mux.HandleFunc("GET /account", s.account)
	mux.HandleFunc("POST /auth/google", s.googleStart)
	mux.HandleFunc("GET /auth/google/callback", s.googleCallback)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /admin", s.admin)
	mux.HandleFunc("POST /admin/action", s.adminAction)
	mux.HandleFunc("POST /poll/{id}/discussion", s.postDiscussion)
	mux.HandleFunc("GET /archive", s.archiveIndex)
	mux.HandleFunc("GET /archive/{id}", s.detail)
	mux.HandleFunc("GET /archive/{id}/export", s.export)
	mux.HandleFunc("GET /studio", s.studio)
	mux.HandleFunc("POST /studio/{id}/archive", s.restricted)
	mux.HandleFunc("POST /studio/{id}/rematch", s.restricted)
	mux.HandleFunc("GET /legacy/{$}", s.legacy)
	mux.HandleFunc("GET /legacy/history.json", s.legacyHistory)
	mux.HandleFunc("GET /legacy/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(s.legacyDir, "manifest.json"))
	})
	for _, path := range []string{"/click/", "/stream", "/metrics/feed", "/metrics/history", "/about", "/chart", "/modal/toggle", "/metrics.svg"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Dogs vs. Cats is retired. Visit /legacy/ for the frozen results.", http.StatusGone)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; form-action 'self' https://accounts.google.com")
		if s.auth != nil && r.Host != s.auth.origin.Host {
			if r.Method != "GET" && r.Method != "HEAD" {
				http.Error(w, "Use the configured site address", 403)
				return
			}
			canonical := *s.auth.origin
			canonical.Path = r.URL.Path
			canonical.RawQuery = r.URL.RawQuery
			http.Redirect(w, r, canonical.String(), http.StatusPermanentRedirect)
			return
		}
		if r.Method == "POST" {
			// All browser mutation requests must originate at this host. SameSite is
			// defense in depth, not the sole cross-site request protection.
			origin, err := url.Parse(r.Header.Get("Origin"))
			scheme := "http"
			if s.secureCookies(r) {
				scheme = "https"
			}
			if err != nil || origin.Host != r.Host || origin.Scheme != scheme || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "Same-origin requests only", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Station) getSession(w http.ResponseWriter, r *http.Request, create bool) (Session, error) {
	if c, err := r.Cookie("station_guest"); err == nil {
		if v, err := s.session(c.Value); err == nil {
			return v, nil
		} else if err != sql.ErrNoRows {
			return Session{}, err
		}
	}
	if !create {
		return Session{}, errors.New("Open the station to begin a browser session.")
	}
	v, err := s.newSession()
	if err == nil {
		s.setSessionCookie(w, r, v)
	}
	return v, err
}
func renderPage(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := stationTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		log.Println(err)
		http.Error(w, "Could not render page", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}
func (s *Station) board(session Session) (Page, error) {
	d := Page{Title: "Current event", Mode: "board", Session: session}
	if err := s.advance(time.Now().UnixMilli()); err != nil {
		return d, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var mainID, nextID string
	err := s.db.QueryRow("SELECT main,next FROM event_schedule WHERE id=1").Scan(&mainID, &nextID)
	if err != nil {
		return d, err
	}
	if d.Poll, err = s.poll(mainID); err != nil {
		return d, err
	}
	d.Poll.decay(time.Now().UnixMilli())
	if d.Next, err = s.poll(nextID); err != nil {
		return d, err
	}
	polls, err := s.list("live")
	if err != nil {
		return d, err
	}
	for _, p := range polls {
		hidden, e := hiddenEvent(s.db, p.ID)
		if e != nil {
			return d, e
		}
		if p.Scope == "community" && !hidden {
			d.Polls = append(d.Polls, p)
		}
	}
	if d.Voted, err = s.hasVoted(d.Poll, session); err != nil {
		return d, err
	}
	if d.NextVoted, err = s.hasVoted(d.Next, session); err != nil {
		return d, err
	}
	if err = s.presentCandidates(&d.Next); err != nil {
		return d, err
	}
	d.Discussion, err = s.comments(mainID)
	return d, err
}
func (s *Station) home(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, true)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d, err := s.board(v)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	renderPage(w, "page", d)
}
func (s *Station) detail(w http.ResponseWriter, r *http.Request) {
	if !s.visibleEvent(w, r, r.PathValue("id")) {
		return
	}
	if err := s.advance(time.Now().UnixMilli()); err != nil {
		http.Error(w, "Could not load event", 500)
		return
	}
	p, err := s.poll(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/archive/") && p.Status != "archived" {
		http.NotFound(w, r)
		return
	}
	v, err := s.getSession(w, r, true)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	p.decay(time.Now().UnixMilli())
	comments, err := s.comments(p.ID)
	if err != nil {
		http.Error(w, "Could not load discussion", 500)
		return
	}
	voted, err := s.hasVoted(p, v)
	if err != nil {
		http.Error(w, "Could not load ballot", 500)
		return
	}
	if err = s.presentCandidates(&p); err != nil {
		http.Error(w, "Could not load candidates", 500)
		return
	}
	renderPage(w, "page", Page{Title: p.Title, Mode: "detail", Poll: p, Session: v, Discussion: comments, Voted: voted})
}
func (s *Station) vote(w http.ResponseWriter, r *http.Request) {
	session, err := s.getSession(w, r, false)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	choice, err := strconv.Atoi(r.PathValue("choice"))
	if err != nil {
		http.Error(w, "Invalid choice", 400)
		return
	}
	requestID := r.URL.Query().Get("request")
	if requestID == "" {
		requestID = randomID()
	}
	if len(requestID) < 8 || len(requestID) > 100 {
		http.Error(w, "Invalid interaction identifier", 400)
		return
	}
	err = s.click(r.PathValue("id"), session, choice, requestID, time.Now().UnixMilli())
	s.actionResponse(w, r, err, "Vote recorded.")
}

func (s *Station) actionResponse(w http.ResponseWriter, r *http.Request, err error, message string) {
	code := http.StatusOK
	if err != nil {
		message, code = err.Error(), http.StatusBadRequest
		if errors.Is(err, errSignIn) || errors.Is(err, errSuspended) {
			code = http.StatusForbidden
		}
		if errors.Is(err, errClosed) || errors.Is(err, errVoted) {
			code = http.StatusConflict
		}
		if errors.Is(err, errCooldown) {
			code = http.StatusTooManyRequests
		}
	}
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
		return
	}
	if err != nil {
		http.Error(w, message, code)
		return
	}
	target := "/poll/" + r.PathValue("id")
	if r.FormValue("return") == "home" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Station) live(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, false)
	if err != nil {
		http.Error(w, "Open an event to begin", 403)
		return
	}
	var d Page
	name := "board-live"
	if id := r.URL.Query().Get("poll"); id != "" {
		if err = s.advance(time.Now().UnixMilli()); err != nil {
			http.Error(w, "Could not refresh", 500)
			return
		}
		if !s.visibleEvent(w, r, id) {
			return
		}
		d.Poll, err = s.poll(id)
		if err == nil {
			err = s.presentCandidates(&d.Poll)
		}
		if err == nil {
			d.Discussion, err = s.comments(id)
		}
		d.Poll.decay(time.Now().UnixMilli())
		d.Session, d.Mode = v, "detail"
		if err == nil {
			d.Voted, err = s.hasVoted(d.Poll, v)
		}
		name = "event-live"
	} else {
		d, err = s.board(v)
	}
	if err != nil {
		http.Error(w, "Could not refresh event", 500)
		return
	}
	renderPage(w, name, d)
}

func (s *Station) presentCandidates(p *Poll) error {
	if p.Scope != "selection" || p.Status != "live" {
		return nil
	}
	for i := range p.Candidates {
		unavailable, err := unavailableCandidate(s.db, p.ID, p.Candidates[i].ID)
		if err != nil {
			return err
		}
		p.Candidates[i].Unavailable = unavailable
		if unavailable {
			p.Options[i] = "Removed suggestion"
		}
	}
	return nil
}
func (s *Station) profile(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, false)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	handle := strings.TrimSpace(r.FormValue("handle"))
	if !validHandle(handle) {
		http.Error(w, "Use up to 24 letters, numbers, spaces, underscores or hyphens.", 400)
		return
	}
	if _, err = s.db.Exec("UPDATE sessions SET handle=? WHERE id=?", handle, v.ID); err != nil {
		http.Error(w, "Could not save handle", 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (s *Station) archiveIndex(w http.ResponseWriter, r *http.Request) {
	polls, err := s.list("archived")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var visible []Poll
	for _, p := range polls {
		hidden, e := hiddenEvent(s.db, p.ID)
		if e != nil {
			http.Error(w, "Could not load archive", 500)
			return
		}
		if p.Scope != "" && !hidden {
			visible = append(visible, p)
		}
	}
	v, _ := s.getSession(w, r, false)
	renderPage(w, "page", Page{Title: "Archive", Mode: "archive", Archives: visible, Session: v})
}
func (s *Station) studio(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, true)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d, err := s.board(v)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d.Mode = "create"
	d.Title = "Create an event"
	d.Formats = formats()
	renderPage(w, "page", d)
}
func (s *Station) export(w http.ResponseWriter, r *http.Request) {
	if !s.visibleEvent(w, r, r.PathValue("id")) {
		return
	}
	var payload []byte
	var hash string
	if err := s.db.QueryRow("SELECT payload,sha256 FROM archives WHERE poll=?", r.PathValue("id")).Scan(&payload, &hash); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="poll-archive.json"`)
	w.Header().Set("ETag", `"`+hash+`"`)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(payload)
}

type LegacyManifest struct {
	Title, Provenance, Cutoff string
	CutoffUnix                int64
	Snapshots                 int
	Final                     struct{ ClicksA, ClicksB, Views int64 }
	ArchivedAt                string
}

func (s *Station) legacy(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(filepath.Join(s.legacyDir, "manifest.json"))
	if err != nil {
		http.Error(w, "Run scripts/archive-legacy.py after stopping the legacy server.", 503)
		return
	}
	var m LegacyManifest
	if err = json.Unmarshal(b, &m); err != nil {
		http.Error(w, "Invalid legacy manifest", 500)
		return
	}
	renderPage(w, "legacy", m)
}
func (s *Station) legacyHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(s.legacyDir, "history.json"))
}

func (s *Station) restricted(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Manual event controls are unavailable. Main events close automatically each week.", http.StatusForbidden)
}
func (s *Station) account(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, true)
	if err != nil {
		http.Error(w, "Could not load account", 500)
		return
	}
	message := ""
	if r.URL.Query().Get("login") == "cancelled" {
		message = "Google sign-in was cancelled. You can try again."
	}
	renderPage(w, "page", Page{Title: "Account", Mode: "account", Session: v, AuthEnabled: s.auth != nil, Message: message})
}
func (s *Station) submitEvent(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, false)
	if err != nil || v.AccountID == "" {
		http.Error(w, errSignIn.Error(), 403)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	id, err := s.createEvent(v, r.FormValue("kind"), r.FormValue("title"), r.FormValue("options"), time.Now().UnixMilli())
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/poll/"+id, http.StatusSeeOther)
}
func (s *Station) postDiscussion(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, false)
	if err != nil {
		http.Error(w, "Open an event to join the discussion", 403)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "Invalid comment", 400)
		return
	}
	err = s.discuss(r.PathValue("id"), v, r.FormValue("body"), time.Now().UnixMilli())
	s.actionResponse(w, r, err, "Comment posted.")
}

func (m LegacyManifest) Total() int64        { return m.Final.ClicksA + m.Final.ClicksB }
func (m LegacyManifest) CutoffMillis() int64 { return m.CutoffUnix * 1000 }

func (s *Station) hasVoted(p Poll, v Session) (bool, error) {
	if p.Kind != "one" {
		return false, nil
	}
	var count int
	var err error
	if p.Scope == "selection" {
		err = s.db.QueryRow("SELECT count(*) FROM next_ballots WHERE poll=? AND account=?", p.ID, v.AccountID).Scan(&count)
	} else {
		err = s.db.QueryRow("SELECT count(*) FROM ballots WHERE poll=? AND session=?", p.ID, v.ID).Scan(&count)
	}
	return count > 0, err
}
