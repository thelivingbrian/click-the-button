package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path"
	"time"

	datastar "github.com/starfederation/datastar/sdk/go"
	"github.com/wcharczuk/go-chart"
)

type Signal map[string]any

type HomePageSignals struct {
	Message   string `json:"message"`
	CounterA  int64  `json:"counterA"`
	CounterB  int64  `json:"counterB"`
	ShowModal bool   `json:"showModal"`
}

/////////////////////////////////////////////////////////////
// Home

func (app *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	app.views.Add(1)
	signal := HomePageSignals{
		Message:   greeting,
		CounterA:  app.clicksA.Load(),
		CounterB:  app.clicksB.Load(),
		ShowModal: false,
	}

	bytes, err := json.Marshal(&signal)
	if err != nil {
		return
	}
	_ = tmpl.ExecuteTemplate(w, "home", string(bytes))
}

//////////////////////////////////////////////////////////////
// Modal

func (app *App) aboutHandler(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	err := sse.MergeFragments(`
      <div id="modal-content">
        <h2>About</h2>
        <div>
          Here is where the about page content would go.
        </div>
        <a href="#" data-on-click="@get('metrics/toggle')">Hide</a>
      </div>
	`)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := sse.MarshalAndMergeSignals(&Signal{"showModal": true}); err != nil {
		fmt.Println(err)
		return
	}
}

func (app *App) chartHandler(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	err := sse.MergeFragments(`
      <div id="modal-content">
        <h2>Metrics</h2>
        <div class="range-buttons" style="margin-bottom:1rem; display:flex; gap:0.5rem; flex-wrap:wrap;">
          <button data-range="5m">5m</button>
          <button data-range="1h">1h</button>
          <button data-range="1d">1d</button>
          <button data-range="2d">2d</button>
          <button data-range="1w">1w</button>
          <button data-range="all">All‑Time</button>
        </div>
        <canvas id="mChart"></canvas>
        <a href="#" data-on-click="@get('metrics/toggle')">Hide</a>
      </div>
	`)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := sse.MarshalAndMergeSignals(&Signal{"showModal": true}); err != nil {
		fmt.Println(err)
		return
	}
	if err := sse.ExecuteScript(`setupChart();`); err != nil {
		fmt.Println(err)
		return
	}
}

/////////////////////////////////////////////////////////////
// Click

func (app *App) clickHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch path.Base(r.URL.Path) {
	case "A":
		signal := app.ClickA()
		sse := datastar.NewSSE(w, r)
		if err := sse.MarshalAndMergeSignals(&signal); err != nil {
			log.Println("sse error clickA:", err)
		}

	case "B":
		signal := app.ClickB()
		sse := datastar.NewSSE(w, r)
		if err := sse.MarshalAndMergeSignals(&signal); err != nil {
			log.Println("sse error clickB:", err)
		}

	default:
		http.NotFound(w, r)
	}
}

func (app *App) ClickA() Signal {
	count := app.clicksA.Add(1)
	return Signal{"counterA": count}
}

func (app *App) ClickB() Signal {
	count := app.clicksB.Add(1)
	return Signal{"counterB": count}
}

/////////////////////////////////////////////////////////////
// Stream

func (app *App) streamHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Accel-Buffering", "no")
	sse := datastar.NewSSE(w, r)

	signal := Signal{}
	previousA := int64(0)
	previousB := int64(0)
	ticker := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			countA := app.clicksA.Load()
			if previousA != countA {
				previousA = countA
				signal["counterA"] = countA
				err := sse.MarshalAndMergeSignals(&signal)
				if err != nil {
					fmt.Println(err)
				}
			}
			countB := app.clicksB.Load()
			if previousB != countB {
				previousB = countB
				signal["counterB"] = countB
				err := sse.MarshalAndMergeSignals(&signal)
				if err != nil {
					fmt.Println(err)
				}
			}
		}
	}
}

/////////////////////////////////////////////////////////////
// Metrics

type Point struct {
	Ts      int64 `json:"ts"`
	ClicksA int64 `json:"clicksA"`
	ClicksB int64 `json:"clicksB"`
}

func (app *App) metricsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := app.db.Query(`SELECT ts, clicksA, clicksB FROM counter_snapshots
                        		ORDER BY ts`)
	if err != nil {
		fmt.Println("Error querying metrics:", err)
		return
	}
	defer rows.Close()

	var pts []Point
	for rows.Next() {
		var p Point
		rows.Scan(&p.Ts, &p.ClicksA, &p.ClicksB)
		pts = append(pts, p)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "rows error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pts)
}

func (app *App) metricsFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)

	if err := rc.Flush(); err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Listen for other points
	ch := app.broadcaster.Subscribe()
	defer app.broadcaster.Unsubscribe(ch)

	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case point := <-ch:
			_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second)) // config ?

			if _, err := fmt.Fprintf(w, "id:%d\nevent:point\ndata:", point.Ts); err != nil {
				return
			}
			if err := json.NewEncoder(w).Encode(point); err != nil {
				return
			}
			if _, err := fmt.Fprint(w, "\n\n"); err != nil {
				return
			}

			if err := rc.Flush(); err != nil {
				return
			}

		case <-keepAlive.C:
			fmt.Fprint(w, ": ping\n\n")
			if err := rc.Flush(); err != nil {
				return
			}

		case <-r.Context().Done():
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

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&Signal{"showModal": !signals.ShowModal}); err != nil {
		fmt.Println(err)
		return
	}
}

///////////////////////////////////////////////////////////////
// Server Side Rendered Chart

func (db DB) metricsAsSvg(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=120")
	points, err := fetchPoints(db)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	renderSVG(w, points)
}

type ViewPoint struct {
	Point
	views int64
}

func fetchPoints(db DB) ([]ViewPoint, error) {
	rows, err := db.Query(`SELECT ts, clicksA, clicksB, views FROM counter_snapshots ORDER BY ts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pts []ViewPoint
	for rows.Next() {
		var p ViewPoint
		if err := rows.Scan(&p.Ts, &p.ClicksA, &p.ClicksB, &p.views); err != nil {
			return nil, err
		}
		pts = append(pts, p)
	}
	return pts, nil
}

func renderSVG(w http.ResponseWriter, pts []ViewPoint) {
	x := make([]time.Time, len(pts))
	clicks := make([]float64, len(pts))
	clicksB := make([]float64, len(pts))
	views := make([]float64, len(pts))

	for i, p := range pts {
		x[i] = time.Unix(p.Ts, 0)
		clicks[i] = float64(p.ClicksA)
		clicksB[i] = float64(p.ClicksB)
		views[i] = float64(p.views)
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
				Name:    "Clicks A",
				XValues: x,
				YValues: clicks,
			},
			chart.TimeSeries{
				Name:    "Clicks B",
				XValues: x,
				YValues: clicksB,
				Style: chart.Style{
					Show:        true,
					StrokeColor: chart.ColorRed,
				},
			},
			chart.TimeSeries{
				Name:    "Views",
				XValues: x,
				YValues: views,
				Style: chart.Style{
					Show:        true,
					StrokeColor: chart.ColorGreen,
					StrokeWidth: 2.0,
				},
			},
		},
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	_ = graph.Render(chart.SVG, w)
}
