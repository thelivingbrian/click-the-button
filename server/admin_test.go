package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminModerationPermissionsAndWithdrawnWinner(t *testing.T) {
	s := testStation(t)
	s.auth = &googleAuth{bootstrapEmail: "owner@gmail.com"}
	now := time.Now().UnixMilli()
	admin, err := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "owner", Email: "owner@gmail.com", Verified: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "member", Email: "member@gmail.com", Verified: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Authorization ignores caller-supplied role labels.
	member.Role = "admin"
	if err = s.moderate(member, "pause-submissions", "", now); err == nil {
		t.Fatal("forged role accepted")
	}
	member.Role = "member"
	s.auth = nil // No canonical origin needed for these internal test fixtures.
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, requestWithSession("GET", "/admin", "", member))
	if rr.Code != 403 {
		t.Fatal("member read admin data")
	}
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, requestWithSession("POST", "/admin/action", "action=pause-submissions", member))
	if rr.Code != 403 {
		t.Fatal("member performed admin action")
	}
	id, err := s.createEvent(member, "one", "Which local improvement should we prioritize?", "Libraries\nParks", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.moderate(admin, "nominate", id, now); err != nil {
		t.Fatal(err)
	}
	board, _ := s.board(admin)
	if err = s.advance(board.Poll.Ends); err != nil {
		t.Fatal(err)
	}
	board, err = s.board(admin)
	if err != nil {
		t.Fatal(err)
	}
	if board.Next.Candidates[0].ID != id {
		t.Fatal("priority not included", board.Next.Candidates)
	}
	guest := testGuest(t, s)
	if err = s.discuss(id, guest, "remove this private text", now+1000); err != nil {
		t.Fatal(err)
	}
	if err = s.click(board.Next.ID, member, 0, "vote-before-withdrawal", now+1000); err != nil {
		t.Fatal(err)
	}
	if err = s.moderate(admin, "hide", id, now+2000); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/poll/" + id, "/archive/" + id + "/export", "/live?poll=" + id} {
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, requestWithSession("GET", path, "", guest))
		if rr.Code != 404 {
			t.Fatal("hidden event visible", path, rr.Code)
		}
	}
	comments, _ := s.comments(id)
	if len(comments) != 0 {
		t.Fatal("hidden discussion retained")
	}
	if err = s.click(id, guest, 0, "hidden-vote", now+3000); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}
	other := testAccount(t, s, "another-member")
	if err = s.click(board.Next.ID, other, 0, "withdrawn-candidate", now+3000); err == nil {
		t.Fatal("withdrawn candidate accepted votes")
	}
	if err = s.advance(board.Poll.Ends); err != nil {
		t.Fatal(err)
	}
	var main string
	s.db.QueryRow("SELECT main FROM event_schedule").Scan(&main)
	current, _ := s.poll(main)
	if current.Title == "Which local improvement should we prioritize?" {
		t.Fatal("withdrawn winner promoted")
	}
	var audit string
	s.db.QueryRow("SELECT group_concat(action || target) FROM admin_audit").Scan(&audit)
	if strings.Contains(audit, "private text") {
		t.Fatal("deleted comment text in audit")
	}
}

func TestAdminPauseCommentRemovalAndRestore(t *testing.T) {
	s := testStation(t)
	s.auth = &googleAuth{bootstrapEmail: "owner@gmail.com"}
	now := time.Now().UnixMilli()
	admin, _ := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "owner", Email: "owner@gmail.com", Verified: true}, now)
	member := testAccount(t, s, "member")
	if err := s.moderate(admin, "pause-submissions", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createEvent(member, "one", "Should submissions remain paused?", "Yes\nNo", now); err == nil {
		t.Fatal("submission pause ignored")
	}
	if err := s.moderate(admin, "resume-submissions", "", now); err != nil {
		t.Fatal(err)
	}
	id, err := s.createEvent(member, "one", "Should submissions remain paused?", "Yes\nNo", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.moderate(admin, "hide", id, now); err != nil {
		t.Fatal(err)
	}
	if err = s.moderate(admin, "restore", id, now); err != nil {
		t.Fatal(err)
	}
	if err = s.click(id, testGuest(t, s), 0, "restored-event", now); err != nil {
		t.Fatal(err)
	}
	board, _ := s.board(admin)
	guest := testGuest(t, s)
	if err = s.discuss(board.Poll.ID, guest, "moderate this", now); err != nil {
		t.Fatal(err)
	}
	var comment string
	s.db.QueryRow("SELECT id FROM discussion LIMIT 1").Scan(&comment)
	if err = s.moderate(admin, "delete-comment", comment, now); err != nil {
		t.Fatal(err)
	}
	comments, _ := s.comments(board.Poll.ID)
	if len(comments) != 0 {
		t.Fatal("comment not deleted")
	}
	if err = s.moderate(admin, "hide", board.Poll.ID, now); err == nil {
		t.Fatal("main event hidden")
	}
}
