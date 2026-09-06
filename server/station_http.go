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

	datastar "github.com/starfederation/datastar/sdk/go"
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
}).ParseFiles("templates/station.html", "templates/legacy.html"))

type Page struct {
	Title, Mode, Message string
	Poll                 Poll
	Polls                []Poll
	Archives             []Poll
	Activity             []Activity
	Session              Session
}

func (s *Station) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /poll/{id}", s.detail)
	mux.HandleFunc("GET /live", s.live)
	mux.HandleFunc("POST /poll/{id}/click/{choice}", s.vote)
	mux.HandleFunc("POST /profile", s.profile)
	mux.HandleFunc("GET /archive", s.archiveIndex)
	mux.HandleFunc("GET /archive/{id}", s.detail)
	mux.HandleFunc("GET /archive/{id}/export", s.export)
	mux.HandleFunc("GET /studio", s.studio)
	mux.HandleFunc("POST /studio/{id}/archive", s.closePoll)
	mux.HandleFunc("POST /studio/{id}/rematch", s.startRematch)
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
		if r.Method == "POST" {
			// All browser mutation requests must originate at this host. SameSite is
			// defense in depth, not the sole cross-site request protection.
			origin, err := url.Parse(r.Header.Get("Origin"))
			scheme := "http"
			if r.TLS != nil {
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
		http.SetCookie(w, &http.Cookie{Name: "station_guest", Value: v.ID, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 365 * 24 * 3600})
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
	d := Page{Title: "The station", Mode: "board", Session: session}
	var err error
	d.Polls, err = s.list("live")
	if err != nil {
		return d, err
	}
	d.Archives, err = s.list("archived")
	if err != nil {
		return d, err
	}
	rows, err := s.db.Query("SELECT title,handle,option,ts FROM activity WHERE ts>? ORDER BY id DESC LIMIT 5", time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Activity
		if err = rows.Scan(&a.Title, &a.Handle, &a.Option, &a.At); err != nil {
			return d, err
		}
		d.Activity = append(d.Activity, a)
	}
	return d, rows.Err()
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
	renderPage(w, "page", Page{Title: p.Title, Mode: "detail", Poll: p, Session: v})
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
	if len(requestID) < 8 || len(requestID) > 100 {
		http.Error(w, "Missing interaction identifier", 400)
		return
	}
	err = s.click(r.PathValue("id"), session, choice, requestID, time.Now().UnixMilli())
	message := "Click received. You’re part of it."
	if err != nil {
		switch {
		case errors.Is(err, errClosed), errors.Is(err, errVoted), errors.Is(err, errCooldown):
			message = err.Error()
		default:
			log.Println(err)
			http.Error(w, "Could not accept click", 400)
			return
		}
	}
	// Datastar merges an accessible status message; the single page stream owns results.
	sse := datastar.NewSSE(w, r)
	_ = sse.MarshalAndMergeSignals(map[string]any{"notice": message})
}
func (s *Station) live(w http.ResponseWriter, r *http.Request) {
	// Read-only streaming: one bounded stream per page, no per-card connections.
	sse := datastar.NewSSE(w, r)
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	previous := ""
	for {
		var data Page
		var err error
		name := "board-live"
		if id := r.URL.Query().Get("poll"); id != "" {
			data.Poll, err = s.poll(id)
			data.Poll.decay(time.Now().UnixMilli())
			name = "poll-live"
		} else {
			data, err = s.board(Session{})
		}
		if err != nil {
			return
		}
		var buf bytes.Buffer
		if err = stationTemplates.ExecuteTemplate(&buf, name, data); err != nil {
			log.Println(err)
			return
		}
		if buf.String() != previous {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err = sse.MergeFragments(buf.String()); err != nil {
				return
			}
			previous = buf.String()
		}
		if data.Poll.Status == "archived" {
			_ = sse.MarshalAndMergeSignals(map[string]any{"closed": true, "notice": "This round has closed. Its final result is preserved."})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
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
	renderPage(w, "page", Page{Title: "The archive", Mode: "archive", Archives: polls})
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
	d.Mode = "studio"
	d.Title = "Local studio"
	renderPage(w, "page", d)
}
func (s *Station) closePoll(w http.ResponseWriter, r *http.Request) {
	if _, err := s.getSession(w, r, false); err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if err := s.archive(r.PathValue("id"), time.Now().UnixMilli()); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/archive/"+r.PathValue("id"), http.StatusSeeOther)
}
func (s *Station) startRematch(w http.ResponseWriter, r *http.Request) {
	if _, err := s.getSession(w, r, false); err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	id, err := s.rematch(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/poll/"+id, http.StatusSeeOther)
}
func (s *Station) export(w http.ResponseWriter, r *http.Request) {
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
