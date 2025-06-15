package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	datastar "github.com/starfederation/datastar/sdk/go"
)

type Signal map[string]any

type HomePageSignals struct {
	Message   string  `json:"message"`
	Counter   int64   `json:"counter"`
	ShowModal bool    `json:"showModal"`
	clicks    []int64 `json:"clicks"`
}

func (app *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	app.views.Add(1)
	signal := HomePageSignals{
		Message:   greeting,
		Counter:   app.clicks.Load(),
		ShowModal: true,
		clicks:    []int64{0, 1, 10, 15, 25},
	}

	bytes, err := json.Marshal(&signal)
	if err != nil {
		return
	}
	_ = tmpl.ExecuteTemplate(w, "home", string(bytes))
}

func (app *App) clickHandler(w http.ResponseWriter, r *http.Request) {
	count := app.clicks.Add(1)
	signal := Signal{"counter": count}

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&signal); err != nil {
		log.Println("sse:", err)
	}
}

func (app *App) streamHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Accel-Buffering", "no")
	sse := datastar.NewSSE(w, r)

	signal := Signal{}
	previous := int64(0)
	ticker := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			count := app.clicks.Load()
			if previous != count {
				previous = count
				signal["counter"] = count
				err := sse.MarshalAndMergeSignals(&signal)
				if err != nil {
					fmt.Println(err)
				}
			}
		}
	}
}

type Point struct {
	Ts     int64 `json:"ts"`
	Clicks int   `json:"clicks"`
	Views  int   `json:"views"`
}

func (app *App) metricsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := app.db.Query(`SELECT ts, clicks, views FROM counter_snapshots
                        		ORDER BY ts`)
	if err != nil {
		fmt.Println("Error querying metrics:", err)
		return
	}
	var pts []Point
	for rows.Next() {
		var p Point
		rows.Scan(&p.Ts, &p.Clicks, &p.Views)
		pts = append(pts, p)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pts)
}

func (app *App) testHandler(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	sse.MergeFragments(`
	<div id="modal-content">
		<h2>Content</h2>
		<a href="#" data-on-click="@get('test')">New</a>
	</div>
	`)
	sse.ExecuteScript(`console.log(window.ds.store.signal('clicks').value)`)
}
