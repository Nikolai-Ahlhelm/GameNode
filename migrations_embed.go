package gamenode

import "embed"

// MigrationFiles contains the committed database migrations used by the binary.
//
//go:embed migrations/*.sql
var MigrationFiles embed.FS
