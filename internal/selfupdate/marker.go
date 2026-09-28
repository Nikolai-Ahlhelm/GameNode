package selfupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The pending-update marker is the only state that must survive the restart
// itself. It lives in <data>/updates/pending.json (never in SQLite: the very
// thing an update may break is the database schema) and records which binary
// to fall back to if the new one never proves healthy.

const (
	markerApplied    = "applied"     // binary swapped; awaiting proof the new binary is healthy
	markerRolledBack = "rolled_back" // backup restored; awaiting one audit report from the restored binary

	markerFileName = "pending.json"
	maxMarkerBytes = 16 << 10
)

type marker struct {
	State         string    `json:"state"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	AppliedAt     time.Time `json:"applied_at"`
	ActorID       string    `json:"actor_id,omitempty"`
	ActorUsername string    `json:"actor_username,omitempty"`
	BootAttempts  int       `json:"boot_attempts"`
	// Reason is a controlled code recorded on rollback, never raw output.
	Reason string `json:"reason,omitempty"`
}

func updatesDir(dataDirectory string) string { return filepath.Join(dataDirectory, "updates") }

func markerPath(dataDirectory string) string {
	return filepath.Join(updatesDir(dataDirectory), markerFileName)
}

func readMarker(dataDirectory string) (marker, bool, error) {
	file, err := os.Open(markerPath(dataDirectory))
	if errors.Is(err, os.ErrNotExist) {
		return marker{}, false, nil
	}
	if err != nil {
		return marker{}, false, err
	}
	defer file.Close()
	decoder := json.NewDecoder(&limitedFile{file: file, remaining: maxMarkerBytes})
	var value marker
	if err := decoder.Decode(&value); err != nil {
		return marker{}, false, err
	}
	return value, true, nil
}

func writeMarker(dataDirectory string, value marker) error {
	dir := updatesDir(dataDirectory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "pending-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err = temp.Write(encoded); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err = os.Rename(tempName, markerPath(dataDirectory)); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return nil
}

func removeMarker(dataDirectory string) error {
	err := os.Remove(markerPath(dataDirectory))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type limitedFile struct {
	file      *os.File
	remaining int64
}

func (l *limitedFile) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, errors.New("marker file too large")
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.file.Read(p)
	l.remaining -= int64(n)
	return n, err
}
