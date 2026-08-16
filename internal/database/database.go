package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
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

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	// A table rebuild (see 020_tenants.sql) may need to DROP a table that
	// other, unrelated tables still reference by foreign key with ON DELETE
	// CASCADE. SQLite performs an implicit cascading DELETE across those
	// foreign keys when the referenced table is dropped while enforcement is
	// on, which would destroy live child rows the migration never intended
	// to touch. Disabling enforcement for this migration's dedicated
	// connection - and only this connection - avoids that; PRAGMA
	// foreign_keys can only be changed outside a transaction, so this must
	// happen before any migration's BEGIN below. PRAGMA foreign_key_check
	// below verifies referential integrity is intact before enforcement is
	// restored.
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys for migration: %w", err)
	}

	applyErr := applyPendingMigrations(ctx, conn, files, entries)
	if applyErr == nil {
		applyErr = verifyForeignKeys(ctx, conn)
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil && applyErr == nil {
		applyErr = fmt.Errorf("re-enable foreign keys after migration: %w", err)
	}
	return applyErr
}

func applyPendingMigrations(ctx context.Context, conn *sql.Conn, files embed.FS, entries []fs.DirEntry) error {
	for _, entry := range entries {
		version := entry.Name()
		var exists int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		b, err := fs.ReadFile(files, path.Join("migrations", version))
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(b)); err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", version, time.Now().UTC().Format(time.RFC3339Nano))
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

// verifyForeignKeys runs after every pending migration has been applied with
// foreign key enforcement disabled. It fails the migration rather than
// silently re-enabling enforcement over a database left with dangling
// foreign key references.
func verifyForeignKeys(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("verify foreign keys after migration: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("migration left dangling foreign key references")
	}
	return rows.Err()
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
