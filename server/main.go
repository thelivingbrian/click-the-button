package main

import (
	"context"
	"log"
	"net/http"
	_ "net/http/pprof"
	"sync/atomic"
	"text/template"
	"time"
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
	db          DB
	broadcaster *Broadcaster
	views       atomic.Int64
	clicks      atomic.Int64
}

func main() {
	// grab config from .env
	db := initDB()
	app := createApp(db)
	app.takePeriodicSnapshots()
	app.sendPeriodicBroadcasts()

	go func() {
		log.Println("pprof listening on :6060")
		if err := http.ListenAndServe(":6060", nil); err != nil {
			log.Fatalf("pprof server failed: %v", err)
		}
	}()

	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/{$}", app.homeHandler)
	http.HandleFunc("/click", app.clickHandler)
	http.HandleFunc("/stream", app.streamHandler)
	http.HandleFunc("/metrics/toggle", app.metricsToggle)
	http.HandleFunc("/metrics/feed", app.metricsFeed)
	http.HandleFunc("/metrics", app.metricsHandler)
	http.HandleFunc("/test", app.testHandler)
	http.HandleFunc("/reload", app.reloadHandler)
	http.HandleFunc("/metrics.svg", db.metricsAsSvg)

	log.Println("listening on :14010")
	log.Fatal(http.ListenAndServe(":14010", nil))
}

func createApp(db DB) *App {
	app := App{
		db:          db,
		broadcaster: NewBroadcaster(),
		views:       atomic.Int64{},
		clicks:      atomic.Int64{},
	}
	clickCount, viewCount := fetchMostRecentSnapshot(db)
	app.clicks.Store(clickCount)
	app.views.Store(viewCount)
	if clickCount != 0 || viewCount != 0 {
		backupWithVacuumInto(context.Background(), db, backupDirectory)
	}
	return &app
}
