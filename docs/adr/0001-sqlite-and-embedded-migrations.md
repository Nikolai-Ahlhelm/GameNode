# ADR 0001: SQLite and embedded SQL migrations

## Problem

Milestone 1 needs a self-contained, cross-platform data store with deterministic schema evolution.

## Options

Use an external database, use SQLite with a heavyweight ORM, or use SQLite with committed SQL migrations.

## Trade-offs

An external database adds operational dependencies. An ORM can obscure schema changes. SQLite has a single-writer model but is appropriate for one autonomous node.

## Decision

GameNode uses SQLite through the pure-Go `modernc.org/sqlite` driver. Ordered SQL migration files are embedded into the binary and recorded in `schema_migrations`.

## Consequences

The release binary needs no database server or C compiler. Schema changes must be added as immutable, ordered SQL files. SQLite concurrency limits must be considered as future workloads grow.
