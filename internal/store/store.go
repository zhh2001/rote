package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// schemaVersion is recorded in PRAGMA user_version to track the on-disk layout.
const schemaVersion = 1

// Run is one recorded execution of a job. Times are stored as Unix nanoseconds
// (UTC) and reconstructed on read; the display layer is responsible for any
// conversion to local time.
type Run struct {
	ID              int64
	JobName         string
	StartedAt       time.Time
	FinishedAt      time.Time
	Duration        time.Duration
	ExitCode        int
	TimedOut        bool
	Success         bool
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Err             string // runner-level error message; empty if none
}

// Store persists run history in a SQLite database. Its methods are safe for
// concurrent use by multiple goroutines.
type Store struct {
	db            *sql.DB
	writes        chan struct{}
	releaseWrites func()
	closed        chan struct{}
	schedulerLock *os.File
	closeOnce     sync.Once
	closeErr      error
}

// runColumns lists the run table columns in the order scanRun expects.
const runColumns = `id, job_name, started_at, finished_at, duration, exit_code, ` +
	`timed_out, success, stdout, stderr, stdout_truncated, stderr_truncated, err`

const createTableSQL = `
CREATE TABLE IF NOT EXISTS runs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    job_name         TEXT    NOT NULL,
    started_at       INTEGER NOT NULL,
    finished_at      INTEGER NOT NULL,
    duration         INTEGER NOT NULL,
    exit_code        INTEGER NOT NULL,
    timed_out        INTEGER NOT NULL,
    success          INTEGER NOT NULL,
    stdout           BLOB,
    stderr           BLOB,
    stdout_truncated INTEGER NOT NULL,
    stderr_truncated INTEGER NOT NULL,
    err              TEXT    NOT NULL DEFAULT ''
)`

const createIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_runs_job_started ON runs (job_name, started_at)`

// Open opens (creating if necessary) the SQLite database at path, creating any
// missing parent directories, and applies the schema idempotently. WAL mode and
// a busy timeout are enabled on every connection. Writes are
// serialized across Stores for the same file in this process. Other processes
// can still exhaust SQLite's busy timeout.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create directory: %w", err)
		}
	}

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open database: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open database: %w", err)
	}

	writes, release, err := acquireWriteQueue(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, writes: writes, releaseWrites: release, closed: make(chan struct{})}
	if err := s.migrate(context.Background()); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// OpenScheduler opens a store and exclusively locks scheduling against its
// database until Close. Other callers of Open may still read and write runs.
// The lock is also released by the OS if the scheduler process exits abruptly.
func OpenScheduler(path string) (*Store, error) {
	// Acquire before initializing WAL or the schema, so simultaneous starts on
	// a new database cannot race its initialization before reaching the lock.
	lock, err := lockScheduler(path)
	if err != nil {
		return nil, err
	}
	s, err := Open(path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	s.schedulerLock = lock
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if err := s.lockWrite(ctx); err != nil {
		return fmt.Errorf("store: apply schema: %w", err)
	}
	defer s.unlockWrite()
	for _, stmt := range []string{createTableSQL, createIndexSQL} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: apply schema: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("store: set schema version: %w", err)
	}
	return nil
}

// Close rejects queued writes, waits for the admitted write to finish, then
// releases the database handle and any scheduler lock. It is idempotent.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.writes <- struct{}{}
		defer s.unlockWrite()
		s.closeErr = s.db.Close()
		if s.schedulerLock != nil {
			s.closeErr = errors.Join(s.closeErr, s.schedulerLock.Close())
		}
		s.releaseWrites()
	})
	return s.closeErr
}

// Insert stores a run and returns its new row id.
func (s *Store) Insert(ctx context.Context, r Run) (int64, error) {
	if err := s.lockWrite(ctx); err != nil {
		return 0, fmt.Errorf("store: insert run: %w", err)
	}
	defer s.unlockWrite()
	return insertRun(ctx, s.db, r)
}

// executor is implemented by both a database handle and a transaction.
type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertRun(ctx context.Context, db executor, r Run) (int64, error) {
	res, err := db.ExecContext(ctx, `
INSERT INTO runs (job_name, started_at, finished_at, duration, exit_code,
                  timed_out, success, stdout, stderr, stdout_truncated,
                  stderr_truncated, err)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.JobName,
		r.StartedAt.UTC().UnixNano(),
		r.FinishedAt.UTC().UnixNano(),
		int64(r.Duration),
		r.ExitCode,
		boolToInt(r.TimedOut),
		boolToInt(r.Success),
		r.Stdout,
		r.Stderr,
		boolToInt(r.StdoutTruncated),
		boolToInt(r.StderrTruncated),
		r.Err,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert run: %w", err)
	}
	return id, nil
}

// RecentRuns returns up to limit runs for jobName, newest first. A non-positive
// limit returns all runs for the job.
func (s *Store) RecentRuns(ctx context.Context, jobName string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = -1 // SQLite treats a negative LIMIT as unbounded.
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+runColumns+`
FROM runs
WHERE job_name = ?
ORDER BY started_at DESC, id DESC
LIMIT ?`, jobName, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent runs: %w", err)
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: recent runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: recent runs: %w", err)
	}
	return out, nil
}

// LastRun returns the most recent run for jobName. ok is false when the job has
// no recorded runs.
func (s *Store) LastRun(ctx context.Context, jobName string) (Run, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT `+runColumns+`
FROM runs
WHERE job_name = ?
ORDER BY started_at DESC, id DESC
LIMIT 1`, jobName)

	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("store: last run: %w", err)
	}
	return r, true, nil
}

// latestRunIDs enumerates job names using the covering job/start index, then
// seeks to each job's newest ID using that same index (including the rowid tie
// breaker). Only those rows' payloads are read by the outer query. A window
// query over SELECT * instead sorts/copies every historical row and its blobs.
// Discovering job names still scans the index, but not the historical payloads.
const latestRunIDs = `
SELECT (
    SELECT latest.id FROM runs AS latest
    WHERE latest.job_name = jobs.job_name
    ORDER BY latest.started_at DESC, latest.id DESC
    LIMIT 1
)
FROM (SELECT DISTINCT job_name FROM runs) AS jobs`

// LatestPerJob returns the most recent run for every job, keyed by job name.
func (s *Store) LatestPerJob(ctx context.Context) (map[string]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+runColumns+`
FROM runs
WHERE id IN (`+latestRunIDs+`)`)
	if err != nil {
		return nil, fmt.Errorf("store: latest per job: %w", err)
	}
	defer rows.Close()

	out := make(map[string]Run)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: latest per job: %w", err)
		}
		out[r.JobName] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: latest per job: %w", err)
	}
	return out, nil
}

// Prune keeps only the newest keep runs for jobName, deleting older ones. Other
// jobs are unaffected. A non-positive keep deletes all runs for the job.
func (s *Store) Prune(ctx context.Context, jobName string, keep int) error {
	if err := s.lockWrite(ctx); err != nil {
		return fmt.Errorf("store: prune: %w", err)
	}
	defer s.unlockWrite()
	return pruneRuns(ctx, s.db, jobName, keep)
}

func pruneRuns(ctx context.Context, db executor, jobName string, keep int) error {
	if keep < 0 {
		keep = 0
	}
	_, err := db.ExecContext(ctx, `
DELETE FROM runs
WHERE job_name = ?
  AND id NOT IN (
      SELECT id FROM runs
      WHERE job_name = ?
      ORDER BY started_at DESC, id DESC
      LIMIT ?
  )`, jobName, jobName, keep)
	if err != nil {
		return fmt.Errorf("store: prune: %w", err)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanRun(sc scanner) (Run, error) {
	var (
		r                           Run
		started, finished, duration int64
		timedOut, success           int
		stdoutTrunc, stderrTrunc    int
		stdout, stderr              []byte
	)
	if err := sc.Scan(
		&r.ID, &r.JobName, &started, &finished, &duration, &r.ExitCode,
		&timedOut, &success, &stdout, &stderr, &stdoutTrunc, &stderrTrunc, &r.Err,
	); err != nil {
		return Run{}, err
	}
	r.StartedAt = time.Unix(0, started).UTC()
	r.FinishedAt = time.Unix(0, finished).UTC()
	r.Duration = time.Duration(duration)
	r.TimedOut = timedOut != 0
	r.Success = success != 0
	r.Stdout = stdout
	r.Stderr = stderr
	r.StdoutTruncated = stdoutTrunc != 0
	r.StderrTruncated = stderrTrunc != 0
	return r, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
