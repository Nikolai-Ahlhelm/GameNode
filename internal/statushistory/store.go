// Package statushistory stores the low-frequency, durable status checks used by
// tenant status pages. It intentionally does not replace the high-frequency
// in-memory process metrics sampler in internal/monitoring.
package statushistory

import (
	"context"
	"database/sql"
	"time"
)

const (
	// Interval is deliberately much larger than the live metrics interval. A
	// five-minute sample is enough for a status page while keeping writes small.
	Interval = 5 * time.Minute
	// Retention is the maximum age of durable status checks.
	Retention = 30 * 24 * time.Hour
)

type Check struct {
	ServerID  string
	CheckedAt time.Time
	Status    string
	State     string
}

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) RecordBatch(ctx context.Context, checks []Check) error {
	if len(checks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO server_status_history(server_id, checked_at, status, state) VALUES(?,?,?,?) ON CONFLICT(server_id, checked_at) DO UPDATE SET status=excluded.status, state=excluded.state`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, check := range checks {
		if check.ServerID == "" || check.CheckedAt.IsZero() {
			continue
		}
		if _, err = stmt.ExecContext(ctx, check.ServerID, check.CheckedAt.UTC().Format(time.RFC3339), check.Status, check.State); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PruneBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM server_status_history WHERE checked_at < ?`, cutoff.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) List(ctx context.Context, serverID string, since time.Time) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, checked_at, status, state FROM server_status_history WHERE server_id = ? AND checked_at >= ? ORDER BY checked_at ASC`, serverID, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checks := make([]Check, 0)
	for rows.Next() {
		var check Check
		var checkedAt string
		if err := rows.Scan(&check.ServerID, &checkedAt, &check.Status, &check.State); err != nil {
			return nil, err
		}
		check.CheckedAt, err = time.Parse(time.RFC3339, checkedAt)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return checks, nil
}

func StatusFromHealth(health string) string {
	switch health {
	case "healthy":
		return "up"
	case "degraded", "detached":
		return "degraded"
	default:
		return "down"
	}
}
