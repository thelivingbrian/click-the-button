package main

import (
	"context"
	"log"
	"net/http" //_ "net/http/pprof"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"
)

const (
	dbFilePath      = "data/clicks.db"
	schemaFilePath  = "sql/schema.sql"
	backupDirectory = "data/backups"
)

var (
	buildRevision = "development"
	greeting      = "Choose your favorite!"
	tmpl          = template.Must(template.ParseFiles("templates/home.tmpl.html"))
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
	if err := os.MkdirAll("data", 0o755); err != nil {
		log.Fatal(err)
	}
	station, err := openStation("data/station.db", "data/legacy")
	if err != nil {
		log.Fatal(err)
	}
	defer station.db.Close()
	if err := station.configureGoogle(); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := station.advance(now.UnixMilli()); err != nil {
					log.Println("event rotation:", err)
				}
			}
		}
	}()
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	log.Println("station listening on http://" + host + ":" + config.port)
	server := &http.Server{Addr: host + ":" + config.port, Handler: station.routes(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Println("shutdown:", err)
			_ = server.Close()
		}
		close(done)
	}()
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-done
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
