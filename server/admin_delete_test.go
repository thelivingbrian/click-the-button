package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func deletionAdmin(t *testing.T, s *Station) Session {
	t.Helper()
	v := testAccount(t, s, "delete-admin")
	if _, err := s.db.Exec("UPDATE accounts SET role='admin' WHERE id=?", v.AccountID); err != nil {
		t.Fatal(err)
	}
	v, err := s.session(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func deleteForms(t *testing.T, body string) []url.Values {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var forms []url.Values
	var walk func(*html.Node, url.Values)
	walk = func(n *html.Node, form url.Values) {
		if n.Type == html.ElementNode && n.Data == "form" {
			if form != nil {
				t.Fatal("nested form")
			}
			form = url.Values{}
			defer func() {
				if strings.HasPrefix(form.Get("action"), "delete-") {
					forms = append(forms, form)
				}
			}()
		}
		if n.Data == "input" && form != nil {
			var name, value string
			for _, a := range n.Attr {
				if a.Key == "name" {
					name = a.Val
				}
				if a.Key == "value" {
					value = a.Val
				}
			}
			form.Set(name, value)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, form)
		}
	}
	walk(doc, nil)
	return forms
}

func TestInlineDeleteControlsAndNativeCommentRemoval(t *testing.T) {
	s := testStation(t)
	admin, member, guest := deletionAdmin(t, s), testAccount(t, s, "author"), testGuest(t, s)
	now := time.Now().UnixMilli()
	id, err := s.createEvent(member, "one", "A community event to moderate", "Yes\nNo", now)
	if err != nil {
		t.Fatal(err)
	}
	board, _ := s.board(admin)
	if err = s.discuss(board.Poll.ID, guest, "Private comment text", now); err != nil {
		t.Fatal(err)
	}
	for _, session := range []Session{admin, member, guest} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, requestWithSession("GET", "/", "", session))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		forms := deleteForms(t, w.Body.String())
		if session.Role != "admin" {
			if len(forms) != 0 {
				t.Fatal("non-admin received delete controls")
			}
			continue
		}
		counts := map[string]int{}
		for _, form := range forms {
			counts[form.Get("action")]++
			if form.Get("action") == "delete-event" && form.Get("target") != id {
				t.Fatal("main event can be deleted")
			}
			if form.Get("action") == "delete-comment" {
				response := httptest.NewRecorder()
				s.routes().ServeHTTP(response, requestWithSession("POST", "/admin/action", form.Encode(), admin))
				if response.Code != 303 || response.Header().Get("Location") != "/" {
					t.Fatal("native delete did not return home", response.Code)
				}
			}
		}
		if counts["delete-comment"] != 1 || counts["delete-event"] != 1 || counts["delete-suggestion"] != 3 {
			t.Fatal(counts)
		}
	}
	comments, _ := s.comments(board.Poll.ID)
	if len(comments) != 0 {
		t.Fatal("comment retained")
	}
	for _, path := range []string{"/poll/" + id, "/admin"} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, requestWithSession("GET", path, "", admin))
		if w.Code != 200 || len(deleteForms(t, w.Body.String())) == 0 {
			t.Fatal("missing controls", path, w.Code)
		}
	}
}

func TestDeleteEventArchivesVotesAndRejectsNonAdmins(t *testing.T) {
	s := testStation(t)
	admin, member, guest := deletionAdmin(t, s), testAccount(t, s, "author"), testGuest(t, s)
	now := time.Now().UnixMilli()
	id, _ := s.createEvent(member, "one", "Preserve my event vote history", "Yes\nNo", now)
	board, _ := s.board(admin)
	if err := s.click(id, guest, 1, "first", now); err != nil {
		t.Fatal(err)
	}
	if err := s.discuss(id, guest, "Discard this discussion", now); err != nil {
		t.Fatal(err)
	}
	comments, _ := s.comments(id)
	actions := map[string]string{"delete-event": id, "delete-comment": fmt.Sprint(comments[0].ID), "delete-suggestion": board.Next.ID + ":" + board.Next.Candidates[0].ID}
	for _, session := range []Session{guest, member} {
		for action, target := range actions {
			session.Role = "admin"
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, requestWithSession("POST", "/admin/action", url.Values{"action": {action}, "target": {target}}.Encode(), session))
			if w.Code != 403 {
				t.Fatal("unauthorized deletion", action, w.Code)
			}
		}
	}
	if err := s.moderate(admin, "delete-event", board.Poll.ID, now); err == nil {
		t.Fatal("main event deleted")
	}
	if err := s.moderate(admin, "delete-event", id, now); err != nil {
		t.Fatal(err)
	}
	p, _ := s.poll(id)
	if p.Status != "archived" || p.Total() != 1 {
		t.Fatal(p)
	}
	comments, _ = s.comments(id)
	if len(comments) != 0 {
		t.Fatal("discussion retained")
	}
	if err := s.click(id, guest, 0, "late", now+1000); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}
	for _, path := range []string{"/poll/" + id, "/live?poll=" + id, "/archive/" + id + "/export"} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, requestWithSession("GET", path, "", guest))
		if w.Code != 404 {
			t.Fatal("deleted event still public", path, w.Code)
		}
	}
	var payload string
	if err := s.db.QueryRow("SELECT payload FROM archives WHERE poll=?", id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "Discard this discussion") {
		t.Fatal("comment archived")
	}
	if err := s.moderate(admin, "delete-event", id, now); err != nil {
		t.Fatal("retry failed", err)
	}
}

func TestWithdrawEditorialSuggestionsPreservesVotesAndSchedule(t *testing.T) {
	s := testStation(t)
	admin := deletionAdmin(t, s)
	member := testAccount(t, s, "voter")
	board, _ := s.board(admin)
	now := time.Now().UnixMilli()
	if err := s.click(board.Next.ID, member, 0, "vote", now); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range board.Next.Candidates {
		if err := s.moderate(admin, "delete-suggestion", board.Next.ID+":"+candidate.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := s.board(admin)
	if current.Next.Total() != 1 || current.Poll.ID != board.Poll.ID {
		t.Fatal("deletion changed totals or main event")
	}
	if current.Next.Options[0] != "Removed suggestion" {
		t.Fatal("removed suggestion title still visible")
	}
	if err := s.click(board.Next.ID, testAccount(t, s, "another"), 0, "late", now); err == nil {
		t.Fatal("withdrawn candidate received vote")
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, requestWithSession("GET", "/poll/"+board.Next.ID, "", member))
	if strings.Contains(w.Body.String(), board.Next.Options[0]) {
		t.Fatal("removed title visible on ballot detail")
	}
	if err := s.advance(board.Poll.Ends); err != nil {
		t.Fatal(err)
	}
	current, _ = s.board(admin)
	for _, c := range board.Next.Candidates {
		if current.Poll.Title == c.Title {
			t.Fatal("deleted suggestion promoted")
		}
	}
	var raw []byte
	s.db.QueryRow("SELECT payload FROM archives WHERE poll=?", board.Next.ID).Scan(&raw)
	var archive Archive
	if err := json.Unmarshal(raw, &archive); err != nil {
		t.Fatal(err)
	}
	if archive.Poll.Total() != 1 || !archive.Poll.Candidates[0].Withdrawn {
		t.Fatal("withdrawal provenance lost")
	}
	if err := s.moderate(admin, "delete-suggestion", board.Next.ID+":"+board.Next.Candidates[0].ID, board.Poll.Ends); err == nil {
		t.Fatal("stale ballot modified")
	}
}

func TestDiscussionMigrationPreservesRowsAndNeverReusesDeletedIDs(t *testing.T) {
	s := testStation(t)
	admin := deletionAdmin(t, s)
	guest := testGuest(t, s)
	board, _ := s.board(admin)
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`DROP TABLE discussion; CREATE TABLE discussion(id INTEGER PRIMARY KEY, poll TEXT NOT NULL REFERENCES polls(id), session TEXT NOT NULL REFERENCES sessions(id), handle TEXT NOT NULL, body TEXT NOT NULL, ts INTEGER NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("INSERT INTO discussion VALUES(77,?,?,?,?,?)", board.Poll.ID, guest.ID, "Old name", "Existing text", now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.initializeDiscussionIDs(); err != nil {
			t.Fatal(err)
		}
	}
	comments, _ := s.comments(board.Poll.ID)
	if len(comments) != 1 || comments[0].ID != 77 || comments[0].Body != "Existing text" || comments[0].Handle != "Old name" {
		t.Fatal(comments)
	}
	if err = s.moderate(admin, "delete-comment", "77", now); err != nil {
		t.Fatal(err)
	}
	if err = s.discuss(board.Poll.ID, guest, "New comment", now+20000); err != nil {
		t.Fatal(err)
	}
	if err = s.moderate(admin, "delete-comment", "77", now+20000); err != nil {
		t.Fatal(err)
	}
	comments, _ = s.comments(board.Poll.ID)
	if len(comments) != 1 || comments[0].ID <= 77 || comments[0].Body != "New comment" {
		t.Fatal("stale delete removed a new comment", comments)
	}
}
