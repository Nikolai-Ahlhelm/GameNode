# ADR 0005: Server-root filesystem sandbox

## Problem

The Files API receives untrusted client paths but may only expose each server's configured working directory. Path traversal, symlinks, Windows junctions, and special files must not provide host filesystem access.

## Decision

`internal/filesystem` is the only component that resolves client paths. It accepts relative paths only, normalizes either separator, rejects traversal segments, absolute/drive/UNC paths, and applies a path-boundary check with `filepath.Rel` after canonicalizing the configured root and requested target.

On Linux and other non-Windows platforms, symlinks are resolved with `filepath.EvalSymlinks`; a link is usable only when its fully resolved target remains inside the canonical root. On Windows, all reparse points beneath the configured root are denied for 4A. This conservative policy covers symbolic links and junctions without relying on incomplete target-resolution semantics.

Only regular UTF-8 text files and directories are exposed through the JSON read API. Device files, sockets, FIFOs, binary files, oversized files, and unsafe entries are rejected or omitted from listings. Text reads and mutations are capped at 4 MiB and directory responses at 10,000 entries. Creates resolve an existing safe parent and use exclusive creation. Edits write and sync a temporary file in the same safe directory, then atomically replace the existing regular file. Source and destination paths for moves use the same root check; deletion cannot target the root and recursive deletion is explicit.

Uploads use a separately configured size limit and stream a single validated multipart file to a temporary file in a validated target directory. A no-overwrite commit uses an atomic create-at-destination primitive; explicit overwrite atomically replaces an existing regular file. Downloads open and stream a validated regular file without reading it into application memory.

## Consequences

The API can list, read, create, edit, move, and delete bounded text content without duplicating path policy in handlers. Windows servers whose working trees contain junctions or other reparse points cannot access paths through those objects, even if they point back inside the root. On Linux, deleting or moving a final symlink acts on the link itself; it never follows an external target.

Path-based APIs retain a narrow OS-level TOCTOU window if an attacker with host filesystem write access replaces a checked directory after canonicalization and before it is opened. The service resolves the target before access and never returns host paths; eliminating this window fully would require platform-specific handle-relative traversal and is deferred.
