package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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

	// If a backup for today exists, remove it and recreate it.
	// Todo: filter by quantity or age instead.
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
	if !app.configuration.snapshotEnabled() {
		return
	}
	go func() {
		ticker := time.NewTicker(app.configuration.snapshotInterval) // Source from config
		defer ticker.Stop()

		var previousClickACount, previousClickBCount int64
		for range ticker.C {
			currentClicksA := app.clicksA.Load()
			currentClicksB := app.clicksB.Load()
			currentViews := app.views.Load()
			if currentClicksA == previousClickACount &&
				currentClicksB == previousClickBCount {
				//currentViews == previousViewCount {
				continue
			}
			fmt.Println("inserting: ", currentClicksA, currentClicksB, currentViews)
			if err := insertSnapshot(app.db, currentClicksA, currentClicksB, currentViews); err != nil {
				log.Println("Error taking snapshot:", err)
				continue
			}
			previousClickACount, previousClickBCount = currentClicksA, currentClicksB
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
