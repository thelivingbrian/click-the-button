package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAccount(t *testing.T, s *Station, account string) Session {
	t.Helper()
	v := testGuest(t, s)
	if _, err := s.db.Exec("INSERT OR IGNORE INTO accounts(id,provider,subject) VALUES(?,'test',?)", account, account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO account_sessions(session,account,expires) VALUES(?,?,?)", v.ID, account, time.Now().Add(loginLifetime).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	v, err := s.session(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWeeklyWinnerArchivesAtomicallyAndDeletesDiscussion(t *testing.T) {
	s := testStation(t)
	board, err := s.board(Session{})
	if err != nil {
		t.Fatal(err)
	}
	now := board.Poll.Created + 1000
	guest := testGuest(t, s)
	if err = s.discuss(board.Poll.ID, guest, "private ephemeral reasoning", now); err != nil {
		t.Fatal(err)
	}
	if err = s.click(board.Poll.ID, guest, 1, "main-vote", now); err != nil {
		t.Fatal(err)
	}
	a := testAccount(t, s, "account-a")
	if err = s.click(board.Next.ID, a, 1, "next-vote", now); err != nil {
		t.Fatal(err)
	}
	if err = s.click(board.Next.ID, a, 1, "next-vote", now); err != nil {
		t.Fatal("retry must be idempotent", err)
	}
	sameAccount := testAccount(t, s, "account-a")
	if err = s.click(board.Next.ID, sameAccount, 0, "second-browser", now); !errors.Is(err, errVoted) {
		t.Fatal("second session bypassed account vote", err)
	}
	if err = s.advance(board.Poll.Ends); err != nil {
		t.Fatal(err)
	}
	var mainID, nextID string
	var ends int64
	if err = s.db.QueryRow("SELECT main,next,ends FROM event_schedule").Scan(&mainID, &nextID, &ends); err != nil {
		t.Fatal(err)
	}
	winner, err := s.poll(mainID)
	if err != nil {
		t.Fatal(err)
	}
	if winner.Title != board.Next.Options[1] || winner.Total() != 0 || ends != board.Poll.Ends+eventWeek {
		t.Fatal("incorrect promotion", winner, ends)
	}
	for _, id := range []string{board.Poll.ID, board.Next.ID} {
		var raw []byte
		if err = s.db.QueryRow("SELECT payload FROM archives WHERE poll=?", id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("ephemeral reasoning")) {
			t.Fatal("discussion leaked to archive")
		}
		var a Archive
		if err = json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		if a.Poll.Total() != 1 || a.Poll.Status != "archived" || a.Poll.Closed != board.Poll.Ends {
			t.Fatal(a)
		}
	}
	comments, err := s.comments(board.Poll.ID)
	if err != nil || len(comments) != 0 {
		t.Fatal("discussion retained", comments, err)
	}
	if err = s.discuss(board.Poll.ID, guest, "late comment", board.Poll.Ends); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}
	if err = s.advance(board.Poll.Ends); err != nil {
		t.Fatal(err)
	}
	var again string
	s.db.QueryRow("SELECT main FROM event_schedule").Scan(&again)
	if again != mainID {
		t.Fatal("repeated rotation created a second event")
	}
	next, err := s.poll(nextID)
	if err != nil || next.Status != "live" || len(next.Options) < 2 {
		t.Fatal("missing next ballot", next, err)
	}
}

func TestGuestAccessAndForgedAccountRejected(t *testing.T) {
	s := testStation(t)
	board, _ := s.board(Session{})
	guest := testGuest(t, s)
	guest.AccountID = "forged"
	if err := s.click(board.Next.ID, guest, 0, "guest-next", time.Now().UnixMilli()); !errors.Is(err, errSignIn) {
		t.Fatal("guest voted on next", err)
	}
	if _, err := s.createEvent(guest, "one", "A forged community event", "Yes\nNo", time.Now().UnixMilli()); !errors.Is(err, errSignIn) {
		t.Fatal("forged account submitted", err)
	}
	handler := s.routes()
	for _, path := range []string{"/events", "/poll/" + board.Next.ID + "/click/0", "/studio/" + board.Poll.ID + "/archive", "/studio/" + board.Poll.ID + "/rematch"} {
		r := httptest.NewRequest("POST", "http://example.com"+path, strings.NewReader("account=forged&kind=one&title=Forged+title&options=Yes%0ANo"))
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "station_guest", Value: guest.ID})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, r)
		if rr.Code != 403 {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "http://example.com/poll/"+board.Poll.ID+"/click/0", strings.NewReader("return=home"))
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "station_guest", Value: guest.ID})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 303 || rr.Header().Get("Location") != "/" {
		t.Fatal("native guest vote failed", rr.Code, rr.Body.String())
	}
}

func TestCommunityFormatsValidationAndNomination(t *testing.T) {
	s := testStation(t)
	member := testAccount(t, s, "creator")
	now := time.Now().UnixMilli()
	id, err := s.createEvent(member, "one", "Should libraries open on Sundays?", "Yes\nNo", now)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.poll(id)
	if p.Scope != "community" || p.Creator != "creator" || p.Ends != now+eventWeek {
		t.Fatal(p)
	}
	if err = s.click(id, testGuest(t, s), 0, "guest-community", now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, title, options string }{{"unknown", "Invalid event type", "Yes\nNo"}, {"one", "short", "Yes\nNo"}, {"one", "Duplicate options", "Yes\nyes"}, {"tug", "Needs two options", "A\nB\nC"}, {"pulse", "Needs one button", "A\nB"}} {
		if _, err = s.createEvent(member, tc.kind, tc.title, tc.options, now); err == nil {
			t.Fatal("invalid event accepted", tc)
		}
	}
	board, _ := s.board(member)
	// Submit during the week: it appears in the following ballot, not mid-vote.
	if len(board.Next.Candidates) != 3 {
		t.Fatal(board.Next)
	}
	if err = s.advance(board.Poll.Ends); err != nil {
		t.Fatal(err)
	}
	var nextID string
	s.db.QueryRow("SELECT next FROM event_schedule").Scan(&nextID)
	next, _ := s.poll(nextID)
	if next.Candidates[0].ID != id {
		t.Fatal("community submission not nominated", next)
	}
}

func TestRestartCatchupUsesWeeklyBoundaryAndNoDemoSeeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "station.db")
	s, err := openStation(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	board, _ := s.board(Session{})
	polls, _ := s.list("live")
	if len(polls) != 2 {
		t.Fatal("unexpected demo events", polls)
	}
	s.db.Close()
	s, err = openStation(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	now := board.Poll.Ends + 3*eventWeek + 1234
	if err = s.advance(now); err != nil {
		t.Fatal(err)
	}
	var main string
	var ends int64
	s.db.QueryRow("SELECT main,ends FROM event_schedule").Scan(&main, &ends)
	p, _ := s.poll(main)
	if p.Title != board.Next.Options[0] || p.Created != board.Poll.Ends+3*eventWeek || ends <= now {
		t.Fatal("catchup or tie incorrect", p, ends)
	}
	archives, _ := s.list("archived")
	if len(archives) != 2 {
		t.Fatal("invented rounds during downtime", len(archives))
	}
}

func TestDiscussionBoundsEscapingAndExpiry(t *testing.T) {
	s := testStation(t)
	board, _ := s.board(Session{})
	guest := testGuest(t, s)
	now := time.Now().UnixMilli()
	if err := s.discuss(board.Poll.ID, guest, "   ", now); err == nil {
		t.Fatal("blank comment")
	}
	if err := s.discuss(board.Poll.ID, guest, strings.Repeat("a", 501), now); err == nil {
		t.Fatal("long comment")
	}
	if err := s.discuss(board.Poll.ID, guest, "<script>alert('x')</script>", now); err != nil {
		t.Fatal(err)
	}
	if err := s.discuss(board.Poll.ID, guest, "too soon", now+1); err == nil {
		t.Fatal("no cooldown")
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(rr.Body.String(), "<script>alert") || !strings.Contains(rr.Body.String(), "&lt;script&gt;") {
		t.Fatal("comment not safely escaped")
	}
	for i := 1; i <= 55; i++ {
		if err := s.discuss(board.Poll.ID, guest, "bounded comment", now+int64(i)*15000); err != nil {
			t.Fatal(err)
		}
	}
	comments, _ := s.comments(board.Poll.ID)
	if len(comments) != 50 {
		t.Fatal(len(comments))
	}
	if err := s.click(board.Poll.ID, testGuest(t, s), 0, "at-deadline", board.Poll.Ends); !errors.Is(err, errClosed) {
		t.Fatal("late vote accepted", err)
	}
}

func TestCreateAndAccountPagesExposeFormatsWithoutFakeSignIn(t *testing.T) {
	s := testStation(t)
	for _, path := range []string{"/create", "/account"} {
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 200 {
			t.Fatal(path, rr.Code, rr.Body.String())
		}
		if path == "/create" && strings.Count(rr.Body.String(), "class=\"format-card\"") != 7 {
			t.Fatal("missing formats")
		}
	}
	// Guest comments also work as native forms.
	board, _ := s.board(Session{})
	guest := testGuest(t, s)
	form := url.Values{"body": {"Native form comment"}, "return": {"home"}}
	req := httptest.NewRequest("POST", "http://example.com/poll/"+board.Poll.ID+"/discussion", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(&http.Cookie{Name: "station_guest", Value: guest.ID})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != 303 {
		t.Fatal(rr.Code, rr.Body.String())
	}
}
