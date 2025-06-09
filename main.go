package main

import (
	"context"
	"database/sql"
	_ "embed" // required for the sqlite driver import side‑effect
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"text/template"
	"time"

	datastar "github.com/starfederation/datastar/sdk/go"
	_ "modernc.org/sqlite"
)

// ---------- Config -----------

const (
	dbFileRel   = "data/clicks.db"
	schemaRel   = "sql/schema.sql"
	snapshotInt = 5 * 60 * time.Second
)

// ---------- App ----------

type Signal map[string]any

var (
	tmpl   = template.Must(template.ParseGlob("templates/*.tmpl.html"))
	views  atomic.Int64
	clicks atomic.Int64
	db     *sql.DB // todo: remove global
)

func main() {
	initDB()
	takePeriodicSnapshots() // Inject interval / grab from .env
	clicks.Store(fetchMostRecentClickCount(db))

	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/", home)
	http.HandleFunc("/click", click)
	http.HandleFunc("/stream", stream)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ---------- Routes -------------

func home(w http.ResponseWriter, r *http.Request) {
	views.Add(1)
	_ = tmpl.ExecuteTemplate(w, "home", nil)
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

// ---------- DB -------------

func initDB() {
	repoRoot, _ := os.Getwd()
	dbPath := filepath.Join(repoRoot, filepath.FromSlash(dbFileRel))

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("mkdir data dir: %v", err)
	}

	var err error
	db, err = sql.Open("sqlite", fmt.Sprintf("file:%s?_journal_mode=WAL", dbPath))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	schemaPath := filepath.Join(repoRoot, filepath.FromSlash(schemaRel))
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		log.Fatalf("read schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		log.Fatalf("apply schema: %v", err)
	}
}

func fetchMostRecentClickCount(db *sql.DB) int64 {
	var last int64
	err := db.QueryRow(`
		SELECT total
		FROM   counter_snapshots
		ORDER  BY ts DESC
		LIMIT  1`,
	).Scan(&last)
	if err != nil && err != sql.ErrNoRows {
		log.Fatalf("load last total: %v", err)
	}

	return last // 0 if no rows yet
}

// ---------- Snapshots -------------

func takePeriodicSnapshots() {
	go func() {
		ticker := time.NewTicker(snapshotInt)
		defer ticker.Stop()

		var previousClickCount int64
		for range ticker.C {
			current := clicks.Load()
			if current == previousClickCount {
				continue
			}
			fmt.Println("inserting: ", current)
			if err := insertSnapshot(current); err != nil {
				log.Println("Error taking snapshot:", err)
				continue
			}
			previousClickCount = current
		}
	}()
}

func insertSnapshot(total int64) error {
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO counter_snapshots(ts,total) VALUES (?,?)`,
		time.Now().UTC().Unix(), total)
	return err
}
