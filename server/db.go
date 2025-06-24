package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

// ---------- Startup -------------

func initDB() DB {
	repoRoot, _ := os.Getwd()
	dbPath := filepath.Join(repoRoot, filepath.FromSlash(dbFilePath))

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("mkdir data dir: %v", err)
	}

	var err error
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_journal_mode=WAL", dbPath))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	schemaPath := filepath.Join(repoRoot, filepath.FromSlash(schemaFilePath))
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		log.Fatalf("read schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		log.Fatalf("apply schema: %v", err)
	}
	return DB{DB: db}
}

func fetchMostRecentSnapshot(db DB) (int64, int64, int64) {
	var clicksA, clicksB, views int64
	err := db.QueryRow(`
		SELECT clicksA, clicksB, views
		FROM   counter_snapshots
		ORDER  BY ts DESC
		LIMIT  1`,
	).Scan(&clicksA, &clicksB, &views)
	if err != nil && err != sql.ErrNoRows {
		fmt.Println("Fatal error fetching most recent snapshot:", err)
		panic(err)
	}

	return clicksA, clicksB, views
}

func backupWithVacuumInto(ctx context.Context, db DB, dir string) error {
	ts := time.Now().UTC().Format("2006-01-02")
	filename := filepath.Join(dir, fmt.Sprintf("backup-%s.db", ts))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}

	// If a backup for this date already exists, remove it so VACUUM INTO can recreate it.
	// Possibly not great - May want to filter by quantity or age instead.
	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		return err
	}

	// SQL single quotes are escaped via doubling
	quoted := strings.ReplaceAll(filename, `'`, `''`)

	_, err := db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'")
	return err
}

// ---------- Snapshots -------------

func (app *App) takePeriodicSnapshots() {
	// Add guard clause based on config
	go func() {
		ticker := time.NewTicker(snapshotInterval) // Source from config
		defer ticker.Stop()

		var previousClickACount, previousClickBCount, previousViewCount int64
		for range ticker.C {
			currentClicksA := app.clicksA.Load()
			currentClicksB := app.clicksB.Load()
			currentViews := app.views.Load()
			if currentClicksA == previousClickACount &&
				currentClicksB == previousClickBCount &&
				currentViews == previousViewCount {
				continue
			}
			fmt.Println("inserting: ", currentClicksA, currentClicksB, currentViews)
			if err := insertSnapshot(app.db, currentClicksA, currentClicksB, currentViews); err != nil {
				log.Println("Error taking snapshot:", err)
				continue
			}
			previousClickACount, previousClickBCount, previousViewCount = currentClicksA, currentClicksB, currentViews
		}
	}()
}

func insertSnapshot(db DB, clicksA, clicksB, views int64) error {
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO counter_snapshots(ts, clicksA, clicksB, views) 
		VALUES (?,?,?,?)`,
		time.Now().UTC().Unix(), clicksA, clicksB, views)
	return err
}

// ---------- Point Broadcasts -------------
type Broadcaster struct {
	mu        sync.Mutex
	listeners map[chan Point]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{listeners: make(map[chan Point]struct{})}
}

func (b *Broadcaster) Subscribe() chan Point {
	ch := make(chan Point, 100) // Keep buffer ?
	b.mu.Lock()
	b.listeners[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broadcaster) Unsubscribe(ch chan Point) {
	b.mu.Lock()
	delete(b.listeners, ch)
	b.mu.Unlock()
	close(ch)
}

func (b *Broadcaster) Publish(p Point) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.listeners {
		select { // Default to prevent lock ?
		case ch <- p:
			// noop
		default:
			// noop
		}
	}
}

func (app *App) sendPeriodicBroadcasts() {
	// Add guard clause based on config
	go func() {
		ticker := time.NewTicker(5 * time.Second) // Source from config
		defer ticker.Stop()

		var previousClickACount, previousClickBCount, previousViewCount int64
		for range ticker.C {
			currentClicksA := app.clicksA.Load()
			currentClicksB := app.clicksB.Load()
			currentViews := app.views.Load()
			if currentClicksA == previousClickACount &&
				currentClicksB == previousClickBCount &&
				currentViews == previousViewCount {
				continue
			}
			app.broadcaster.Publish(Point{
				Ts:      time.Now().UTC().Unix(),
				ClicksA: currentClicksA,
				ClicksB: currentClicksB,
			})
			previousClickACount, previousClickBCount, previousViewCount = currentClicksA, currentClicksB, currentViews
		}
	}()
}
