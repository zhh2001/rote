package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrSchedulerRunning means another scheduler already owns this database.
var ErrSchedulerRunning = errors.New("a scheduler is already running")

func lockScheduler(database string) (*os.File, error) {
	if dir := filepath.Dir(database); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create directory: %w", err)
		}
	}
	// This short-lived connection only resolves the database filename. Do not
	// initialize WAL or tables until the scheduler lock has been acquired.
	probe, err := sql.Open("sqlite", database+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("store: locate scheduler database: %w", err)
	}
	defer probe.Close()
	// Ask SQLite for the actual filename so relative paths and file: URIs use
	// the same lock. Resolve symlinks as well before choosing the sidecar path.
	var seq int
	var name, path string
	if err := probe.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		return nil, fmt.Errorf("store: locate scheduler database: %w", err)
	}
	if path == "" {
		return nil, fmt.Errorf("store: scheduler requires a file-backed database")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve scheduler database: %w", err)
	}

	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: open scheduler lock: %w", err)
	}
	if err := trySchedulerLock(file); err != nil {
		file.Close()
		if errors.Is(err, ErrSchedulerRunning) {
			return nil, fmt.Errorf("store: %w for database %q; use 'rote tui' to view it", err, path)
		}
		return nil, fmt.Errorf("store: lock scheduler database %q: %w", path, err)
	}
	// Keep the file in place on release. Unlinking it could let a new process
	// lock a different inode while another process still holds the old one.
	// os.OpenFile sets close-on-exec, so job subprocesses cannot retain the lock.
	return file, nil
}
