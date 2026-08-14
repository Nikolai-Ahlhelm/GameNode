package database

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func Open(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Migrate(db *sql.DB, files embed.FS) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration tracking table: %w", err)
	}

	entries, err := migrationEntries(files)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, entry := range entries {
		version := entry.Name()
		var exists int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		b, err := fs.ReadFile(files, path.Join("migrations", version))
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(b)); err == nil {
			_, err = tx.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", version, time.Now().UTC().Format(time.RFC3339Nano))
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// BackupIfMigrationPending creates a consistent SQLite copy before applying
// migrations to an existing GameNode database. Fresh databases are not backed
// up: there is no schema_migrations table to protect yet. A caller must treat a
// backup error as fatal and must not migrate after it.
func BackupIfMigrationPending(db *sql.DB, dbPath string, files embed.FS) (string, bool, error) {
	var found int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&found)
	if err != nil {
		return "", false, err
	}
	if found == 0 {
		return "", false, nil
	}
	entries, err := migrationEntries(files)
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		var applied int
		if err = db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=?", entry.Name()).Scan(&applied); err != nil {
			return "", false, err
		}
		if applied == 0 {
			if dbPath == ":memory:" {
				return "", true, nil
			}
			ext := filepath.Ext(dbPath)
			base := strings.TrimSuffix(filepath.Base(dbPath), ext)
			target := filepath.Join(filepath.Dir(dbPath), fmt.Sprintf("%s.pre-migration-%s%s", base, time.Now().UTC().Format("20060102T150405.000000000Z"), ext))
			if _, statErr := os.Stat(target); statErr == nil {
				return "", true, fmt.Errorf("migration backup target already exists: %s", target)
			} else if !os.IsNotExist(statErr) {
				return "", true, statErr
			}
			if _, err = db.Exec("VACUUM INTO ?", target); err != nil {
				return "", true, fmt.Errorf("create migration backup: %w", err)
			}
			return target, true, nil
		}
	}
	return "", false, nil
}

func migrationEntries(files embed.FS) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	returnEntries := entries[:0]
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			returnEntries = append(returnEntries, entry)
		}
	}
	return returnEntries, nil
}
