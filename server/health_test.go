package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHealthReportsRevisionAndDatabaseFailure(t *testing.T) {
	s := testStation(t)
	handler := s.routes()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result["status"] != "ok" || result["revision"] != buildRevision || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected health: %d %v", w.Code, result)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("health check must not create browser sessions")
	}
	s.db.Close()
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 {
		t.Fatalf("closed database returned %d", w.Code)
	}
}
