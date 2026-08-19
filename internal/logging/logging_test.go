package logging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesReadableModuleLineAndClearsHistoricFiles(t *testing.T) {
	directory := t.TempDir()
	manager, log, err := New(directory, "info")
	if err != nil {
		t.Fatal(err)
	}
	log.With("module", "Server.Create").Info("server created", "server_id", "server-1")
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("log file not created: %v %#v", err, entries)
	}
	data, err := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[INFO] [Server.Create] server created server_id=\"server-1\"") {
		t.Fatalf("unexpected log line: %s", data)
	}
	memory := manager.Entries()
	if len(memory) != 1 || memory[0].Level != "INFO" || !strings.Contains(memory[0].Line, "[Server.Create] server created") {
		t.Fatalf("unexpected in-memory log: %#v", memory)
	}
	if err := os.WriteFile(filepath.Join(directory, "gamenode-2000-01-01.log"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ClearExceptCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "gamenode-2000-01-01.log")); !os.IsNotExist(err) {
		t.Fatalf("historic file was not cleared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, entries[0].Name())); err != nil {
		t.Fatalf("current file was not retained: %v", err)
	}
}

func TestCategoryDisabledSuppressesInfoAndBelow(t *testing.T) {
	directory := t.TempDir()
	manager, log, err := New(directory, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCategories(map[string]bool{CategoryHTTP: false}); err != nil {
		t.Fatal(err)
	}
	WithCategory(log, CategoryHTTP).Info("http request completed", "path", "/api/v1/dashboard")
	WithCategory(log, CategoryRuntime).Info("server started", "server_id", "server-1")
	if len(manager.Entries()) != 1 {
		t.Fatalf("expected only the enabled-category entry to be recorded: %#v", manager.Entries())
	}
	if strings.Contains(manager.Entries()[0].Line, "http request completed") {
		t.Fatalf("disabled category entry was recorded: %#v", manager.Entries())
	}
}

func TestCategoryEnabledEmitsEntries(t *testing.T) {
	directory := t.TempDir()
	manager, log, err := New(directory, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCategories(map[string]bool{CategoryHTTP: true, CategoryRuntime: false}); err != nil {
		t.Fatal(err)
	}
	WithCategory(log, CategoryHTTP).Info("http request completed", "path", "/api/v1/dashboard")
	entries := manager.Entries()
	if len(entries) != 1 || !strings.Contains(entries[0].Line, "http request completed") {
		t.Fatalf("expected enabled category entry to be recorded: %#v", entries)
	}
}

func TestDisabledCategoryNeverHidesWarningsOrErrors(t *testing.T) {
	directory := t.TempDir()
	manager, log, err := New(directory, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCategories(map[string]bool{CategoryHTTP: false}); err != nil {
		t.Fatal(err)
	}
	WithCategory(log, CategoryHTTP).Warn("http request rejected", "status", 400)
	WithCategory(log, CategoryHTTP).Error("http request failed", "status", 500)
	entries := manager.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected warn/error entries to survive a disabled category: %#v", entries)
	}
}

func TestUnknownCategoryIsRejected(t *testing.T) {
	directory := t.TempDir()
	manager, _, err := New(directory, "info")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCategories(map[string]bool{"nonsense": true}); err == nil {
		t.Fatal("expected an unknown category to be rejected")
	}
}

func TestDetailedErrorsGateUnderlyingError(t *testing.T) {
	directory := t.TempDir()
	manager, log, err := New(directory, "info")
	if err != nil {
		t.Fatal(err)
	}
	underlying := errors.New(`UNIQUE constraint failed: users.username (SQLITE_CONSTRAINT_UNIQUE)`)
	log.Error("settings update failed", "error_summary", "operation failed", ErrorDetail(underlying))
	if strings.Contains(manager.Entries()[0].Line, "SQLITE_CONSTRAINT_UNIQUE") {
		t.Fatalf("underlying error leaked while detailed errors were disabled: %#v", manager.Entries())
	}
	manager.SetDetailedErrors(true)
	log.Error("settings update failed", "error_summary", "operation failed", ErrorDetail(underlying))
	entries := manager.Entries()
	if !strings.Contains(entries[len(entries)-1].Line, "SQLITE_CONSTRAINT_UNIQUE") {
		t.Fatalf("underlying error missing while detailed errors were enabled: %#v", entries)
	}
}

func TestErrorDetailAttachesSecretLikeUnderlyingErrorOnlyWhenEnabled(t *testing.T) {
	// This does not claim ErrorDetail redacts secrets - it documents the
	// existing contract that callers must only ever pass errors from
	// trusted internal libraries (e.g. a database driver), never request
	// input, and confirms the gate itself is the only thing standing
	// between that value and the log file.
	directory := t.TempDir()
	manager, log, err := New(directory, "info")
	if err != nil {
		t.Fatal(err)
	}
	log.Error("op failed", ErrorDetail(errors.New("token=super-secret-value")))
	if strings.Contains(manager.Entries()[0].Line, "super-secret-value") {
		t.Fatal("underlying error field leaked while detailed errors were disabled")
	}
}

func TestLoggerRotatesAtSizeAndKeepsBoundedBackups(t *testing.T) {
	directory := t.TempDir()
	_, log, err := New(directory, "info")
	if err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(directory, "gamenode-"+time.Now().Format("2006-01-02")+".log")
	filler := []byte(strings.Repeat("x", maxFileBytes))
	for index := 0; index < maxFileBackups+2; index++ {
		if err = os.WriteFile(current, filler, 0600); err != nil {
			t.Fatal(err)
		}
		log.With("module", "Rotation.Test").Info("continued after rotation", "index", index)
	}
	data, err := os.ReadFile(current)
	if err != nil || !strings.Contains(string(data), "[INFO] [Rotation.Test] continued after rotation") {
		t.Fatalf("current log after rotation: %v %q", err, data)
	}
	for index := 1; index <= maxFileBackups; index++ {
		if _, err = os.Stat(current + "." + strconv.Itoa(index)); err != nil {
			t.Fatalf("missing backup %d: %v", index, err)
		}
	}
	if _, err = os.Stat(current + "." + strconv.Itoa(maxFileBackups+1)); !os.IsNotExist(err) {
		t.Fatalf("backup limit exceeded: %v", err)
	}
}
