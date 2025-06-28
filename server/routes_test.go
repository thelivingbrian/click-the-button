package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"sync/atomic"
	"testing"

	"golang.org/x/net/html"
)

// ---------------------- helpers ----------------------

func newTestApp() *App {
	return &App{
		// db / configuration / broadcaster unused
		views:   atomic.Int64{},
		clicksA: atomic.Int64{},
		clicksB: atomic.Int64{},
	}
}

// ---------------------- ClickA / ClickB ----------------------

func TestClickFunctionsIncrementAndReturnSignal(t *testing.T) {
	app := newTestApp()

	// First ClickA
	sigA1 := app.ClickA()
	if want := int64(1); sigA1["counterA"].(int64) != want || app.clicksA.Load() != want {
		t.Fatalf("ClickA first call: want count %d, got %+v / stored %d",
			want, sigA1["counterA"], app.clicksA.Load())
	}

	// Second ClickA
	sigA2 := app.ClickA()
	if want := int64(2); sigA2["counterA"].(int64) != want || app.clicksA.Load() != want {
		t.Fatalf("ClickA second call: want count %d, got %+v / stored %d",
			want, sigA2["counterA"], app.clicksA.Load())
	}

	// ClickB once
	sigB := app.ClickB()
	if want := int64(1); sigB["counterB"].(int64) != want || app.clicksB.Load() != want {
		t.Fatalf("ClickB first call: want count %d, got %+v / stored %d",
			want, sigB["counterB"], app.clicksB.Load())
	}
}

// ---------------------- homeHandler ----------------------

func TestHomeHandlerRendersJSON(t *testing.T) {
	// Arrange
	app := newTestApp()
	greeting = "hello, world" // Todo: remove global

	app.views.Store(42)
	app.clicksA.Store(2)
	app.clicksB.Store(0)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	// Act
	app.homeHandler(rr, req)

	// Assert
	if rr.Code != http.StatusOK {
		t.Fatalf("homeHandler: want HTTP 200, got %d", rr.Code)
	}

	// verify HTML
	doc, err := html.Parse(rr.Body)
	if err != nil {
		t.Fatalf("response is not valid HTML: %v", err)
	}

	body := find(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "body"
	})
	if body == nil {
		t.Fatal("no <body> element found")
	}

	var signalsAttr string
	for _, a := range body.Attr {
		if a.Key == "data-signals" {
			signalsAttr = a.Val
			break
		}
	}
	if signalsAttr == "" {
		t.Fatal("body lacks data-signals attribute")
	}

	var sig HomePageSignals
	if err := json.Unmarshal([]byte(signalsAttr), &sig); err != nil {
		t.Fatalf("data-signals is not valid JSON: %v\nvalue: %q", err, signalsAttr)
	}

	// check signals are sent correctly
	if sig.Message != greeting || sig.CounterA != 2 || sig.CounterB != 0 || sig.ShowModal != false {
		t.Errorf("unexpected signals: %+v", sig)
	}

	// views should have incremented
	if want := int64(43); app.views.Load() != want {
		t.Errorf("homeHandler did not bump views counter: want %d, got %d", want, app.views.Load())
	}
}

func find(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := find(c, pred); r != nil {
			return r
		}
	}
	return nil
}

// ---------------------- clickHandler ----------------------

func TestClickHandlerRoutingAndValidation(t *testing.T) {
	app := newTestApp()

	tests := []struct {
		method   string
		url      string
		wantCode int
		wantA    int64
		wantB    int64
	}{
		// wrong method
		{http.MethodGet, "/click/A", http.StatusMethodNotAllowed, 0, 0},
		// unknown path
		{http.MethodPost, "/click/C", http.StatusNotFound, 0, 0},
		// happy paths
		{http.MethodPost, "/click/A", http.StatusOK, 1, 0},
		{http.MethodPost, "/click/B", http.StatusOK, 1, 1},
	}

	for i, tc := range tests {
		req := httptest.NewRequest(tc.method, tc.url, nil)
		rr := httptest.NewRecorder()

		app.clickHandler(rr, req)

		if rr.Code != tc.wantCode {
			t.Fatalf("case %d (%s %s): want HTTP %d, got %d",
				i, tc.method, path.Base(tc.url), tc.wantCode, rr.Code)
		}
		if app.clicksA.Load() != tc.wantA || app.clicksB.Load() != tc.wantB {
			t.Fatalf("case %d (%s): counters wrong – want A=%d,B=%d got A=%d,B=%d",
				i, path.Base(tc.url), tc.wantA, tc.wantB, app.clicksA.Load(), app.clicksB.Load())
		}
	}
}
