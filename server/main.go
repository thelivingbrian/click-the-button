package main

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/wcharczuk/go-chart"
)

const (
	dbFilePath       = "data/clicks.db"
	schemaFilePath   = "sql/schema.sql"
	backupDirectory  = "data/backups"
	snapshotInterval = 1 * 60 * time.Second
)

var (
	greeting = "...if you dare!"
	tmpl     = template.Must(template.ParseGlob("templates/*.tmpl.html")) // embed?
)

type App struct {
	db     DB
	views  atomic.Int64
	clicks atomic.Int64
}

func main() {
	// grab config from .env
	db := initDB()
	app := createApp(db)
	app.takePeriodicSnapshots()

	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/{$}", app.homeHandler)
	http.HandleFunc("/click", app.clickHandler)
	http.HandleFunc("/stream", app.streamHandler)
	http.HandleFunc("/metrics", app.metricsHandler)
	http.HandleFunc("/test", app.testHandler)
	http.HandleFunc("/metrics.svg", func(w http.ResponseWriter, r *http.Request) {
		points, err := fetchPoints(db)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		renderSVG(w, points)
	})

	log.Println("listening on :14010")
	log.Fatal(http.ListenAndServe(":14010", nil))
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
			ValueFormatter: chart.TimeValueFormatter,
		},
		YAxis: chart.YAxis{Name: "Count"},
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
				Style:   chart.Style{StrokeColor: chart.ColorAlternateGray},
			},
		},
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	_ = graph.Render(chart.SVG, w) // errors here are rare; ignore or log
}

func createApp(db DB) *App {
	app := App{
		db:     db,
		views:  atomic.Int64{},
		clicks: atomic.Int64{},
	}
	clickCount, viewCount := fetchMostRecentSnapshot(db)
	app.clicks.Store(clickCount)
	app.views.Store(viewCount)
	if clickCount != 0 || viewCount != 0 {
		backupWithVacuumInto(context.Background(), db, backupDirectory)
	}
	return &app
}
