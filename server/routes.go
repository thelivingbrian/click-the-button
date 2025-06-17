package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	datastar "github.com/starfederation/datastar/sdk/go"
	"github.com/wcharczuk/go-chart"
)

type Signal map[string]any

type HomePageSignals struct {
	Message   string `json:"message"`
	Counter   int64  `json:"counter"`
	ShowModal bool   `json:"showModal"`
}

func (app *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	app.views.Add(1)
	signal := HomePageSignals{
		Message:   greeting,
		Counter:   app.clicks.Load(),
		ShowModal: false,
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
	Clicks int64 `json:"clicks"`
	Views  int64 `json:"views"`
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

func (app *App) metricsFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe to new points
	ch := app.broadcaster.Subscribe()
	defer app.broadcaster.Unsubscribe(ch)

	// (If you want resume support, look at r.Header.Get("Last-Event-ID"))

	// Keep-alive comment every 30 s to stop proxies from closing idle conn
	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case p := <-ch:
			// ts is a perfect event ID – makes resuming trivial
			fmt.Fprintf(w, "id: %d\nevent: point\ndata: ", p.Ts)
			_ = json.NewEncoder(w).Encode(p) // adds trailing \n
			fmt.Fprint(w, "\n")              // blank line = end of msg
			flusher.Flush()

		case <-keepAlive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()

		case <-r.Context().Done(): // client went away
			return
		}
	}
}

func (app *App) metricsToggle(w http.ResponseWriter, r *http.Request) {
	var signals HomePageSignals
	if err := datastar.ReadSignals(r, &signals); err != nil {
		fmt.Println(err)
		return
	}
	//signals.Counter = int(clicks.Add(1))

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&Signal{"showModal": !signals.ShowModal}); err != nil {
		fmt.Println(err)
		// sse.ConsoleError(err, nil)
		return
	}
}

///////////////////////////////////////////////////////////////
// Server Side Rendered Chart

func (app *App) testHandler(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	sse.MergeFragments(`
	<div id="modal-content">
		<h2>Metrics B</h2>
		<img src="metrics.svg" alt="Clicks over time"><br />
		<br />
		<a href="#" data-on-click="@get('test')">Back</a>
	</div>
	`)
	sse.ExecuteScript(`console.log(window.ds.store.signal('clicks').value)`)
}

func (db DB) metricsAsSvg(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=120")
	points, err := fetchPoints(db)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	renderSVG(w, points)
}

func fetchPoints(db DB) ([]Point, error) {
	rows, err := db.Query(`SELECT ts, clicks, views FROM counter_snapshots ORDER BY ts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pts []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.Ts, &p.Clicks, &p.Views); err != nil {
			return nil, err
		}
		pts = append(pts, p)
	}
	return pts, nil
}

func renderSVG(w http.ResponseWriter, pts []Point) {
	x := make([]time.Time, len(pts))
	clicks := make([]float64, len(pts))
	views := make([]float64, len(pts))

	for i, p := range pts {
		x[i] = time.Unix(p.Ts, 0)
		clicks[i] = float64(p.Clicks)
		views[i] = float64(p.Views)
	}

	graph := chart.Chart{
		XAxis: chart.XAxis{
			Name:           "Time",
			NameStyle:      chart.StyleShow(),
			Style:          chart.StyleShow(),
			ValueFormatter: chart.TimeValueFormatter,
		},
		YAxis: chart.YAxis{
			Name:      "Count",
			NameStyle: chart.StyleShow(),
			Style:     chart.StyleShow(),
		},
		Series: []chart.Series{
			chart.TimeSeries{
				Name:    "Clicks",
				XValues: x,
				YValues: clicks,
			},
			chart.TimeSeries{
				Name:    "Views",
				XValues: x,
				YValues: views,
				Style: chart.Style{
					Show:        true,
					StrokeColor: chart.ColorRed,
					StrokeWidth: 2.0,
				},
			},
		},
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	_ = graph.Render(chart.SVG, w)
}
