package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

func fetchMostRecentClickCount(db DB) int64 {
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

func takePeriodicSnapshots(db DB) {
	go func() {
		ticker := time.NewTicker(snapshotInterval)
		defer ticker.Stop()

		var previousClickCount int64
		for range ticker.C {
			current := clicks.Load()
			if current == previousClickCount {
				continue
			}
			fmt.Println("inserting: ", current)
			if err := insertSnapshot(db, current); err != nil {
				log.Println("Error taking snapshot:", err)
				continue
			}
			previousClickCount = current
		}
	}()
}

func insertSnapshot(db DB, total int64) error {
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO counter_snapshots(ts,total) VALUES (?,?)`,
		time.Now().UTC().Unix(), total)
	return err
}
