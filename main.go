package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"text/template"
	"time"

	datastar "github.com/starfederation/datastar/sdk/go"
)

// ---------- Config -----------

const (
	dbFilePath       = "data/clicks.db"
	schemaFilePath   = "sql/schema.sql"
	snapshotInterval = 1 * 60 * time.Second
)

// ---------- App ----------

type Signal map[string]any

type HomePageSignals struct {
	Message    string `json:"message"`
	Counter    int64  `json:"counter"`
	ShowDialog bool   `json:"showDialog"`
}

var (
	greeting = "...if you dare!"
	tmpl     = template.Must(template.ParseGlob("templates/*.tmpl.html"))
	views    atomic.Int64
	clicks   atomic.Int64
)

func main() {
	db := initDB()            // grab config from .env
	takePeriodicSnapshots(db) // Inject interval from config
	clickCount, viewCount := fetchMostRecentSnapshot(db)
	clicks.Store(clickCount)
	views.Store(viewCount)

	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/{$}", home)
	http.HandleFunc("/click", click)
	http.HandleFunc("/stream", stream)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ---------- Routes -------------

func home(w http.ResponseWriter, r *http.Request) {
	views.Add(1)
	signal := HomePageSignals{
		Message:    greeting,
		Counter:    clicks.Load(),
		ShowDialog: false,
	}

	bytes, err := json.Marshal(&signal)
	if err != nil {
		return
	}
	_ = tmpl.ExecuteTemplate(w, "home", string(bytes))
}

func click(w http.ResponseWriter, r *http.Request) {
	count := clicks.Add(1)
	signal := Signal{"counter": count}

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&signal); err != nil {
		log.Println("sse:", err)
	}
}

func stream(w http.ResponseWriter, r *http.Request) {
	signal := Signal{}
	previous := int64(0)
	ticker := time.NewTicker(100 * time.Millisecond)
	sse := datastar.NewSSE(w, r)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			count := clicks.Load()
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
