package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RunMeta is a run's metadata without the captured output blobs, for cheap list
// and history queries that should not pull stdout/stderr into memory.
type RunMeta struct {
	ID              int64
	JobName         string
	StartedAt       time.Time
	FinishedAt      time.Time
	Duration        time.Duration
	ExitCode        int
	TimedOut        bool
	Success         bool
	StdoutTruncated bool
	StderrTruncated bool
	Err             string
}

// Output holds the captured streams of a single run.
type Output struct {
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
}

// metaColumns lists the non-blob columns in the order scanRunMeta expects.
const metaColumns = `id, job_name, started_at, finished_at, duration, exit_code, ` +
	`timed_out, success, stdout_truncated, stderr_truncated, err`

// LatestMetaPerJob returns the most recent run's metadata for every job, keyed
// by job name. It matches LatestPerJob's semantics but omits the output blobs.
func (s *Store) LatestMetaPerJob(ctx context.Context) (map[string]RunMeta, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+metaColumns+`
FROM (
    SELECT *, ROW_NUMBER() OVER (
        PARTITION BY job_name ORDER BY started_at DESC, id DESC
    ) AS rn
    FROM runs
)
WHERE rn = 1`)
	if err != nil {
		return nil, fmt.Errorf("store: latest meta per job: %w", err)
	}
	defer rows.Close()

	out := make(map[string]RunMeta)
	for rows.Next() {
		m, err := scanRunMeta(rows)
		if err != nil {
			return nil, fmt.Errorf("store: latest meta per job: %w", err)
		}
		out[m.JobName] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: latest meta per job: %w", err)
	}
	return out, nil
}

// RecentRunsMeta returns up to limit runs' metadata for jobName, newest first.
// It matches RecentRuns' semantics but omits the output blobs. A non-positive
// limit returns all runs for the job.
func (s *Store) RecentRunsMeta(ctx context.Context, jobName string, limit int) ([]RunMeta, error) {
	if limit <= 0 {
		limit = -1 // SQLite treats a negative LIMIT as unbounded.
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+metaColumns+`
FROM runs
WHERE job_name = ?
ORDER BY started_at DESC, id DESC
LIMIT ?`, jobName, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent runs meta: %w", err)
	}
	defer rows.Close()

	var out []RunMeta
	for rows.Next() {
		m, err := scanRunMeta(rows)
		if err != nil {
			return nil, fmt.Errorf("store: recent runs meta: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: recent runs meta: %w", err)
	}
	return out, nil
}

// RunOutput returns the captured output of the run with the given id. ok is
// false when no run has that id.
func (s *Store) RunOutput(ctx context.Context, id int64) (Output, bool, error) {
	var (
		o                        Output
		stdoutTrunc, stderrTrunc int
	)
	err := s.db.QueryRowContext(ctx, `
SELECT stdout, stderr, stdout_truncated, stderr_truncated
FROM runs
WHERE id = ?`, id).Scan(&o.Stdout, &o.Stderr, &stdoutTrunc, &stderrTrunc)
	if err == sql.ErrNoRows {
		return Output{}, false, nil
	}
	if err != nil {
		return Output{}, false, fmt.Errorf("store: run output: %w", err)
	}
	o.StdoutTruncated = stdoutTrunc != 0
	o.StderrTruncated = stderrTrunc != 0
	return o, true, nil
}

func scanRunMeta(sc scanner) (RunMeta, error) {
	var (
		m                           RunMeta
		started, finished, duration int64
		timedOut, success           int
		stdoutTrunc, stderrTrunc    int
	)
	if err := sc.Scan(
		&m.ID, &m.JobName, &started, &finished, &duration, &m.ExitCode,
		&timedOut, &success, &stdoutTrunc, &stderrTrunc, &m.Err,
	); err != nil {
		return RunMeta{}, err
	}
	m.StartedAt = time.Unix(0, started).UTC()
	m.FinishedAt = time.Unix(0, finished).UTC()
	m.Duration = time.Duration(duration)
	m.TimedOut = timedOut != 0
	m.Success = success != 0
	m.StdoutTruncated = stdoutTrunc != 0
	m.StderrTruncated = stderrTrunc != 0
	return m, nil
}
