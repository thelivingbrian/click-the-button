package main

import (
	"context"
	"log"
	"net/http" //_ "net/http/pprof"
	"sync/atomic"
	"text/template"
)

const (
	dbFilePath      = "data/clicks.db"
	schemaFilePath  = "sql/schema.sql"
	backupDirectory = "data/backups"
)

var (
	greeting = "...if you dare!"
	tmpl     = template.Must(template.ParseGlob("templates/*.tmpl.html")) // embed?
)

type App struct {
	db            DB
	configuration *Configuration
	broadcaster   *Broadcaster
	views         atomic.Int64
	clicksA       atomic.Int64
	clicksB       atomic.Int64
}

func main() {
	config := getConfiguration()
	db := initDB()

	app := createApp(db, config)
	app.takePeriodicSnapshots()
	app.sendPeriodicBroadcasts()

	launchPprof(config) // Need seperate mux to ensure pprof is truly disabled

	// Site
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/{$}", app.homeHandler)

	// Clicks
	http.HandleFunc("/click/", app.clickHandler)

	// Updates
	http.HandleFunc("/stream", app.streamHandler)
	http.HandleFunc("/metrics/feed", app.metricsFeed)
	http.HandleFunc("/metrics/history", app.metricsHandler)

	// Modals
	http.HandleFunc("/about", app.aboutHandler)
	http.HandleFunc("/chart", app.chartHandler)
	http.HandleFunc("/modal/toggle", app.modalToggle)

	// Unused server side graph
	http.HandleFunc("/metrics.svg", db.metricsAsSvg)

	log.Println("listening on :" + config.port)
	log.Fatal(http.ListenAndServe(":"+config.port, nil))
}

func createApp(db DB, config *Configuration) *App {
	app := App{
		db:            db,
		configuration: config,
		broadcaster:   NewBroadcaster(),
		views:         atomic.Int64{},
		clicksA:       atomic.Int64{},
		clicksB:       atomic.Int64{},
	}
	clickCountA, clickCountB, viewCount := fetchMostRecentSnapshot(db)
	app.clicksA.Store(clickCountA)
	app.clicksB.Store(clickCountB)
	app.views.Store(viewCount)
	if viewCount != 0 {
		backupWithVacuumInto(context.Background(), db, backupDirectory)
	}
	return &app
}

func launchPprof(config *Configuration) {
	if !config.pprofEnabled {
		return
	}
	log.Println("pprof enabled, listening on port", config.pprofPort)
	go func() {
		if err := http.ListenAndServe(":"+config.pprofPort, nil); err != nil {
			log.Fatalf("pprof server failed: %v", err)
		}
	}()
}
