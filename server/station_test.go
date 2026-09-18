package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStation(t *testing.T) *Station {
	t.Helper()
	s, err := openStation(filepath.Join(t.TempDir(), "station.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	for _, kind := range []string{"one", "tug", "pulse", "contest", "scale", "stars", "heat"} {
		p := preset(kind)
		p.ID = kind
		b, _ := json.Marshal(p)
		if _, err := s.db.Exec("INSERT INTO polls(id,status,state) VALUES(?,'live',?)", kind, b); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func testGuest(t *testing.T, s *Station) Session {
	t.Helper()
	v, err := s.newSession()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDurableClicksIdentityAndRetry(t *testing.T) {
	s := testStation(t)
	v := testGuest(t, s)
	now := time.Now().UnixMilli()
	if err := s.click("one", v, 1, "first-request", now); err != nil {
		t.Fatal(err)
	}
	if err := s.click("one", v, 1, "first-request", now); err != nil {
		t.Fatal("idempotent retry", err)
	}
	if err := s.click("one", v, 0, "another-request", now); !errors.Is(err, errVoted) {
		t.Fatal("second ballot", err)
	}
	if err := s.click("one", testGuest(t, s), 0, "other-browser", now); err != nil {
		t.Fatal(err)
	}
	p, _ := s.poll("one")
	if p.Total() != 2 || p.Counts[1] != 1 {
		t.Fatal(p)
	}
	if err := s.click("tug", v, 99, "bad-choice", now); err == nil {
		t.Fatal("invalid choice accepted")
	}
	for i := 0; i < 7; i++ {
		if err := s.click("tug", v, 0, fmt.Sprint(i), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.click("tug", v, 0, "too-fast", now); !errors.Is(err, errCooldown) {
		t.Fatal("rate limit", err)
	}
	if err := s.click("tug", v, 0, "after-rest", now+1000); err != nil {
		t.Fatal(err)
	}
}

func TestPulseDecayPeakAndFrozenExport(t *testing.T) {
	s := testStation(t)
	v := testGuest(t, s)
	now := time.Now().UnixMilli()
	if err := s.click("pulse", v, 0, "spark-one", now); err != nil {
		t.Fatal(err)
	}
	if err := s.click("pulse", v, 0, "spark-two", now+30000); err != nil {
		t.Fatal(err)
	}
	p, _ := s.poll("pulse")
	if math.Abs(p.EnergyTotal()-1.5) > 0.00001 || p.Peak != 1.5 || p.PeakAt != now+30000 {
		t.Fatal(p)
	}
	if err := s.archive("pulse", now+60000); err != nil {
		t.Fatal(err)
	}
	p, _ = s.poll("pulse")
	if p.EnergyTotal() != 0.75 || p.Peak != 1.5 || p.Total() != 2 {
		t.Fatal(p)
	}
	p.decay(now + 90000)
	if p.EnergyTotal() != 0.75 {
		t.Fatal("archived energy decayed")
	}
	var before, after []byte
	var digest string
	if err := s.db.QueryRow("SELECT payload,sha256 FROM archives WHERE poll='pulse'").Scan(&before, &digest); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(before)
	if hex.EncodeToString(hash[:]) != digest {
		t.Fatal("checksum")
	}
	var a Archive
	if err := json.Unmarshal(before, &a); err != nil {
		t.Fatal(err)
	}
	if len(a.History) != 2 || a.Poll.Status != "archived" {
		t.Fatal(a)
	}
	if err := s.archive("pulse", now+120000); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT payload FROM archives WHERE poll='pulse'").Scan(&after)
	if !bytes.Equal(before, after) {
		t.Fatal("repeat archive changed export")
	}
	if err := s.click("pulse", v, 0, "late-click", now+120000); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE archives SET payload='oops' WHERE poll='pulse'"); err == nil {
		t.Fatal("archive mutated")
	}
	id, err := s.rematch("pulse")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.rematch("pulse")
	if err != nil || id != again {
		t.Fatal("rematch retry", err)
	}
	next, _ := s.poll(id)
	if next.Parent != "pulse" || next.Total() != 0 || next.Peak != 0 || next.Status != "live" {
		t.Fatal(next)
	}
}

func TestClosureRacesPreserveExactlyAcceptedClicks(t *testing.T) {
	s := testStation(t)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	guests := make([]Session, 30)
	for i := range guests {
		guests[i] = testGuest(t, s)
	}
	start := make(chan struct{})
	for i, v := range guests {
		wg.Add(1)
		go func(i int, v Session) {
			defer wg.Done()
			<-start
			err := s.click("contest", v, i%2, fmt.Sprint(i), time.Now().UnixMilli())
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, errClosed) {
				t.Error(err)
			}
		}(i, v)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if err := s.archive("contest", time.Now().UnixMilli()); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	p, _ := s.poll("contest")
	if p.Status != "archived" || p.Total() != accepted.Load() {
		t.Fatalf("%+v accepted %d", p, accepted.Load())
	}
	var raw []byte
	s.db.QueryRow("SELECT payload FROM archives WHERE poll='contest'").Scan(&raw)
	var a Archive
	json.Unmarshal(raw, &a)
	if a.Poll.Total() != accepted.Load() || int64(len(a.History)) != accepted.Load() {
		t.Fatal("inconsistent archive")
	}
}

func TestRestartKeepsResultsAndArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "station.db")
	s, err := openStation(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p0 := preset("stars")
	p0.ID = "stars"
	b0, _ := json.Marshal(p0)
	if _, err = s.db.Exec("INSERT INTO polls(id,status,state) VALUES('stars','live',?)", b0); err != nil {
		t.Fatal(err)
	}
	v := testGuest(t, s)
	if err = s.click("stars", v, 4, "five-stars", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err = s.archive("stars", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s, err = openStation(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	p, err := s.poll("stars")
	if err != nil || p.Status != "archived" || p.Average() != "5.0" {
		t.Fatal(p, err)
	}
	if _, err = s.session(v.ID); err != nil {
		t.Fatal("session lost", err)
	}
}

func TestPagesRenderAndRetiredEndpointsRejectWrites(t *testing.T) {
	s := testStation(t)
	handler := s.routes()
	for _, path := range []string{"/", "/archive", "/studio", "/poll/tug", "/poll/pulse", "/poll/stars", "/poll/heat", "/poll/scale", "/poll/one", "/poll/contest"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), "</html>") {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "ZgotmplZ") {
			t.Fatalf("unsafe template URL: %s", path)
		}
	}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://example.com/click/A", nil)
	r.Header.Set("Origin", "http://example.com")
	handler.ServeHTTP(rr, r)
	if rr.Code != 410 {
		t.Fatal(rr.Code)
	}
	rr = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://example.com/studio/tug/archive", nil)
	r.Header.Set("Origin", "https://evil.example")
	handler.ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Fatal("cross origin accepted")
	}
}

func TestArchivePageHasNoLiveSubscription(t *testing.T) {
	s := testStation(t)
	if err := s.archive("heat", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest("GET", "/archive/heat", nil))
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "data-on-load") || strings.Contains(rr.Body.String(), "data-on-click") {
		t.Fatal(rr.Body.String())
	}
}
