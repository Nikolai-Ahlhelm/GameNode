package logging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
