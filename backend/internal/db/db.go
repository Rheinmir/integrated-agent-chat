package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// Open opens (or creates) the SQLite database at path and runs migrations.
func Open(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite3", path+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := ping(database); err != nil {
		return nil, err
	}

	if err := migrate(database); err != nil {
		return nil, err
	}

	return database, nil
}

func ping(db *sql.DB) error {
	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	return nil
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS chat_logs (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			ts         DATETIME DEFAULT CURRENT_TIMESTAMP,
			model      TEXT,
			provider   TEXT,
			user_msg   TEXT,
			reply      TEXT,
			tok_in     INTEGER DEFAULT 0,
			tok_out    INTEGER DEFAULT 0,
			iterations INTEGER DEFAULT 1,
			failure    INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS agent_memory (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_updated ON agent_memory(updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_ts ON chat_logs(ts DESC)`,
	}

	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}
