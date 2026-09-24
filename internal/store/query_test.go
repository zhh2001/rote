package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A view that raises an error if an old row's payload is evaluated makes this
// regression deterministic, without wall-clock thresholds or huge fixtures.
func TestLatestQueriesSkipOldPayloads(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	latestStart := base.Add(time.Hour)
	for _, job := range []string{"a", "b"} {
		for _, start := range []time.Time{base, latestStart, base.Add(time.Minute)} {
			if _, err := s.Insert(ctx, makeRun(job, start)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.db.Exec(`ALTER TABLE runs RENAME TO stored_runs`); err != nil {
		t.Fatal(err)
	}
	columns := strings.Replace(runColumns, ", err", fmt.Sprintf(
		", CASE WHEN started_at = %d THEN err ELSE abs(-9223372036854775808) END AS err",
		latestStart.UnixNano()), 1)
	if _, err := s.db.Exec(`CREATE VIEW runs AS SELECT ` + columns + ` FROM stored_runs`); err != nil {
		t.Fatal(err)
	}
	// Negative control: ensure the guarded payload really fails if read.
	if _, err := s.RecentRuns(ctx, "a", 0); err == nil {
		t.Fatal("guard did not reject reading an old payload")
	}
	full, err := s.LatestPerJob(ctx)
	if err != nil || len(full) != 2 {
		t.Errorf("latest full evaluated old payload: count=%d err=%v", len(full), err)
	}
	meta, err := s.LatestMetaPerJob(ctx)
	if err != nil || len(meta) != 2 {
		t.Errorf("latest metadata evaluated old payload: count=%d err=%v", len(meta), err)
	}
}

func TestLatestQueriesOrderingAndEmpty(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if got, err := s.LatestPerJob(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty full: %+v, %v", got, err)
	}
	if got, err := s.LatestMetaPerJob(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty metadata: %+v, %v", got, err)
	}
	want := make(map[string]Run)
	for _, job := range []string{"", "a", "A", "任务' OR 1=1 --"} {
		for i, minute := range []int{2, -1, 2, 1} {
			r := makeRun(job, base.Add(time.Duration(minute)*time.Minute))
			r.Success, r.TimedOut, r.ExitCode = false, true, -1
			r.Err = fmt.Sprintf("run-%d", i)
			r.StdoutTruncated, r.StderrTruncated = true, true
			id, err := s.Insert(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			if i == 2 { // Tie on newest start: greater ID wins, not last insert.
				r.ID = id
				want[job] = r
			}
		}
	}
	full, err := s.LatestPerJob(ctx)
	if err != nil || len(full) != len(want) {
		t.Fatalf("full latest: count=%d err=%v", len(full), err)
	}
	meta, err := s.LatestMetaPerJob(ctx)
	if err != nil || len(meta) != len(want) {
		t.Fatalf("metadata latest: count=%d err=%v", len(meta), err)
	}
	for job, r := range want {
		if got := full[job]; got.ID != r.ID || !equalRun(got, r) {
			t.Errorf("wrong latest run for %q: %+v, want %+v", job, got, r)
		}
		sameMeta(t, job, meta[job], r)
		last, ok, err := s.LastRunMeta(ctx, job)
		if err != nil || !ok {
			t.Fatalf("last metadata for %q: ok=%v err=%v", job, ok, err)
		}
		sameMeta(t, job, last, r)
	}
}

func TestLastRunMetaMissing(t *testing.T) {
	s := openTemp(t)
	if got, ok, err := s.LastRunMeta(context.Background(), "missing"); err != nil || ok || got != (RunMeta{}) {
		t.Fatalf("missing: %+v, ok=%v err=%v", got, ok, err)
	}
}

func TestLatestQueryPlan(t *testing.T) {
	s := openTemp(t)
	for _, columns := range []string{runColumns, metaColumns} {
		rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT ` + columns + ` FROM runs WHERE id IN (` + latestRunIDs + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(plan, "\n")
		t.Log(got)
		// The pinned SQLite version should enumerate only the covering index,
		// seek to each latest ID without sorting, and fetch payloads by rowid.
		for _, want := range []string{
			"SCAN runs USING COVERING INDEX idx_runs_job_started",
			"SEARCH latest USING COVERING INDEX idx_runs_job_started (job_name=?)",
			"SEARCH runs USING INTEGER PRIMARY KEY (rowid=?)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in query plan:\n%s", want, got)
			}
		}
		if strings.Contains(got, "TEMP B-TREE") {
			t.Errorf("latest query unexpectedly sorts history:\n%s", got)
		}
	}
}

func TestMetadataQueriesNeverReadOutput(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.Insert(ctx, makeRun("job", base)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE runs RENAME TO stored_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE VIEW runs AS SELECT ` + metaColumns + `,
abs(-9223372036854775808) AS stdout, abs(-9223372036854775808) AS stderr FROM stored_runs`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LastRun(ctx, "job"); err == nil {
		t.Fatal("guard did not reject output reads")
	}
	if _, ok, err := s.LastRunMeta(ctx, "job"); err != nil || !ok {
		t.Errorf("last metadata: ok=%v err=%v", ok, err)
	}
	if got, err := s.RecentRunsMeta(ctx, "job", 0); err != nil || len(got) != 1 {
		t.Errorf("recent metadata: count=%d err=%v", len(got), err)
	}
	if got, err := s.LatestMetaPerJob(ctx); err != nil || len(got) != 1 {
		t.Errorf("latest metadata: count=%d err=%v", len(got), err)
	}
}

func TestLatestQueriesErrors(t *testing.T) {
	queries := map[string]func(context.Context, *Store) error{
		"full":     func(ctx context.Context, s *Store) error { _, err := s.LatestPerJob(ctx); return err },
		"metadata": func(ctx context.Context, s *Store) error { _, err := s.LatestMetaPerJob(ctx); return err },
		"last-metadata": func(ctx context.Context, s *Store) error {
			_, _, err := s.LastRunMeta(ctx, "job")
			return err
		},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := query(ctx, s); !errors.Is(err, context.Canceled) {
				t.Errorf("canceled context: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := query(context.Background(), s); err == nil {
				t.Fatal("closed database error was swallowed")
			}
		})
	}
}

func TestLatestQueriesSnapshotDuringRetention(t *testing.T) {
	s := openTemp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	writePair := func(start time.Time) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, job := range []string{"a", "b"} {
			if _, err := insertRun(ctx, tx, makeRun(job, start)); err != nil {
				return err
			}
			if err := pruneRuns(ctx, tx, job, 1); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if err := writePair(base); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 1; i <= 30; i++ {
			if err := writePair(base.Add(time.Duration(i) * time.Second)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 60; i++ {
		full, err := s.LatestPerJob(ctx)
		if err != nil || len(full) != 2 || !full["a"].StartedAt.Equal(full["b"].StartedAt) {
			t.Fatalf("full query lost its snapshot: %+v err=%v", full, err)
		}
		meta, err := s.LatestMetaPerJob(ctx)
		if err != nil || len(meta) != 2 || !meta["a"].StartedAt.Equal(meta["b"].StartedAt) {
			t.Fatalf("metadata query lost its snapshot: %+v err=%v", meta, err)
		}
	}
}
