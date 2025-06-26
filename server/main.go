package main

import (
	"context"
	"fmt"
	"log"
	"net/http" //_ "net/http/pprof"
	"os"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/joho/godotenv"
)

const (
	dbFilePath      = "data/clicks.db"
	schemaFilePath  = "sql/schema.sql"
	backupDirectory = "data/backups"
	//snapshotInterval = 1 * 60 * time.Second
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

	// Need seperate mux to ensure pprof is truly disabled
	launchPprof(config)

	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/{$}", app.homeHandler)
	http.HandleFunc("/click/A", app.clickAHandler) // if method not post
	http.HandleFunc("/click/B", app.clickBHandler)
	http.HandleFunc("/stream", app.streamHandler)
	http.HandleFunc("/metrics/toggle", app.metricsToggle)
	http.HandleFunc("/metrics/feed", app.metricsFeed)
	http.HandleFunc("/metrics", app.metricsHandler)

	// Unused server side graph
	http.HandleFunc("/test", app.testHandler)
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

type Configuration struct {
	port              string
	pprofEnabled      bool
	pprofPort         string
	snapshotInterval  time.Duration
	broadcastInterval time.Duration
}

func getConfiguration() *Configuration {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Error loading .env file")
		return nil
	}

	si := os.Getenv("SNAPSHOT_INTERVAL")
	snapshotInterval, err := time.ParseDuration(si)
	if err != nil {
		fmt.Printf("Invalid SNAPSHOT_INTERVAL=%q, defaulting to 0: %v\n", si, err)
		snapshotInterval = 0
	}

	bi := os.Getenv("BROADCAST_INTERVAL")
	broadcastInterval, err := time.ParseDuration(bi)
	if err != nil {
		fmt.Printf("Invalid BROADCAST_INTERVAL=%q, defaulting to 0: %v\n", bi, err)
		broadcastInterval = 0
	}

	config := Configuration{
		port:              os.Getenv("PORT"),
		pprofEnabled:      strings.ToLower(os.Getenv("PPROF_ENABLED")) == "true",
		pprofPort:         os.Getenv("PPROF_PORT"),
		snapshotInterval:  snapshotInterval,
		broadcastInterval: broadcastInterval,
	}
	return &config
}

func (config *Configuration) snapshotEnabled() bool {
	return config.snapshotInterval != 0
}

func (config *Configuration) broadcastEnabled() bool {
	return config.broadcastInterval != 0
}
