package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	db *sql.DB
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// PRAGMAs are connection-local. Keep each Store on the initialized
	// connection; callers can still open multiple Store handles safely.
	db.SetMaxOpenConns(1)
	// Task swarms legitimately have multiple workers updating one local store.
	// SQLite otherwise fails immediately when another writer briefly owns the
	// lock. A bounded busy timeout turns that expected contention into waiting.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if err := execPragmaWithBusyRetry(db, "PRAGMA journal_mode=WAL", 5*time.Second); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func execPragmaWithBusyRetry(db *sql.DB, statement string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := db.Exec(statement)
		if err == nil {
			return nil
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		content, err := migrationsFS.ReadFile("migrations/" + f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}

		conn, err := s.db.Conn(context.Background())
		if err != nil {
			return fmt.Errorf("reserve connection for %s: %w", f, err)
		}
		if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			_ = conn.Close()
			return fmt.Errorf("begin tx for %s: %w", f, err)
		}
		rollback := func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
		}
		var applied int
		if err := conn.QueryRowContext(
			context.Background(), "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", f,
		).Scan(&applied); err != nil {
			rollback()
			return fmt.Errorf("check migration %s: %w", f, err)
		}
		if applied > 0 {
			rollback()
			continue
		}
		if _, err := conn.ExecContext(context.Background(), string(content)); err != nil {
			rollback()
			return fmt.Errorf("exec migration %s: %w", f, err)
		}
		if _, err := conn.ExecContext(
			context.Background(), "INSERT INTO schema_migrations (version) VALUES (?)", f,
		); err != nil {
			rollback()
			return fmt.Errorf("record migration %s: %w", f, err)
		}
		if _, err := conn.ExecContext(context.Background(), "COMMIT"); err != nil {
			rollback()
			return fmt.Errorf("commit migration %s: %w", f, err)
		}
		if err := conn.Close(); err != nil {
			return fmt.Errorf("release migration connection %s: %w", f, err)
		}
	}
	return nil
}
