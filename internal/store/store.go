package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
	db *sql.DB
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
// a busy timeout are enabled on every connection so that concurrent writers wait
// rather than fail with "database is locked".
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

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
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

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Insert stores a run and returns its new row id.
func (s *Store) Insert(ctx context.Context, r Run) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
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

// LatestPerJob returns the most recent run for every job, keyed by job name.
func (s *Store) LatestPerJob(ctx context.Context) (map[string]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+runColumns+`
FROM (
    SELECT *, ROW_NUMBER() OVER (
        PARTITION BY job_name ORDER BY started_at DESC, id DESC
    ) AS rn
    FROM runs
)
WHERE rn = 1`)
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
	if keep < 0 {
		keep = 0
	}
	_, err := s.db.ExecContext(ctx, `
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
