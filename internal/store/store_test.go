package store

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// base is a fixed reference instant carrying sub-second precision.
var base = time.Date(2026, 5, 29, 10, 0, 0, 123456789, time.UTC)

func openTemp(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "dir", "rote.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// makeRun builds a fully populated run for the given job and start time.
func makeRun(job string, started time.Time) Run {
	return Run{
		JobName:         job,
		StartedAt:       started,
		FinishedAt:      started.Add(1500 * time.Millisecond),
		Duration:        1500 * time.Millisecond,
		ExitCode:        0,
		TimedOut:        false,
		Success:         true,
		Stdout:          []byte("out:" + job),
		Stderr:          []byte("err:" + job),
		StdoutTruncated: false,
		StderrTruncated: false,
		Err:             "",
	}
}

func equalRun(a, b Run) bool {
	return a.JobName == b.JobName &&
		a.StartedAt.Equal(b.StartedAt) &&
		a.FinishedAt.Equal(b.FinishedAt) &&
		a.Duration == b.Duration &&
		a.ExitCode == b.ExitCode &&
		a.TimedOut == b.TimedOut &&
		a.Success == b.Success &&
		bytes.Equal(a.Stdout, b.Stdout) &&
		bytes.Equal(a.Stderr, b.Stderr) &&
		a.StdoutTruncated == b.StdoutTruncated &&
		a.StderrTruncated == b.StderrTruncated &&
		a.Err == b.Err
}

// 1. Open creates the DB (and parent dirs) and is idempotent.
func TestOpenCreatesAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "rote.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s.Insert(context.Background(), makeRun("job", base)); err != nil {
		t.Fatalf("Insert after first Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-opening the same path must succeed and preserve the schema.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (idempotent): %v", err)
	}
	defer s2.Close()
	if _, err := s2.Insert(context.Background(), makeRun("job", base.Add(time.Second))); err != nil {
		t.Fatalf("Insert after re-Open: %v", err)
	}
}

// 2. Insert then RecentRuns: round-trip, descending order, limit enforced.
func TestRecentRunsOrderAndLimit(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	const n = 5
	for i := 0; i < n; i++ {
		if _, err := s.Insert(ctx, makeRun("job", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	got, err := s.RecentRuns(ctx, "job", 3)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (limit)", len(got))
	}
	// Newest first: indices 4, 3, 2.
	for i, r := range got {
		want := base.Add(time.Duration(n-1-i) * time.Minute)
		if !r.StartedAt.Equal(want) {
			t.Errorf("row %d StartedAt = %s, want %s", i, r.StartedAt, want)
		}
		if i > 0 && got[i-1].StartedAt.Before(r.StartedAt) {
			t.Errorf("not descending at row %d", i)
		}
	}

	// A non-positive limit returns everything.
	all, err := s.RecentRuns(ctx, "job", 0)
	if err != nil {
		t.Fatalf("RecentRuns(all): %v", err)
	}
	if len(all) != n {
		t.Errorf("len = %d, want %d", len(all), n)
	}
}

// 3. Every field round-trips with full fidelity.
func TestFieldRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	want := Run{
		JobName:         "fidelity",
		StartedAt:       base,
		FinishedAt:      base.Add(2*time.Second + 42*time.Nanosecond),
		Duration:        2*time.Second + 42*time.Nanosecond,
		ExitCode:        137,
		TimedOut:        true,
		Success:         false,
		Stdout:          []byte{0x00, 0x01, 0xff, 0xfe, 'h', 'i', 0x00},
		Stderr:          []byte{0xde, 0xad, 0xbe, 0xef},
		StdoutTruncated: true,
		StderrTruncated: false,
		Err:             "boom: could not start",
	}

	id, err := s.Insert(ctx, want)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	want.ID = id

	runs, err := s.RecentRuns(ctx, "fidelity", 1)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len = %d, want 1", len(runs))
	}
	got := runs[0]
	if !equalRun(got, want) {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
	}
	if got.ID != id {
		t.Errorf("ID = %d, want %d", got.ID, id)
	}
	if got.StartedAt.UnixNano() != base.UnixNano() {
		t.Errorf("StartedAt nanos = %d, want %d", got.StartedAt.UnixNano(), base.UnixNano())
	}
}

// 4. LastRun returns the newest row; ok is false when there are none.
func TestLastRun(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if _, ok, err := s.LastRun(ctx, "ghost"); err != nil || ok {
		t.Fatalf("LastRun(empty) = ok %v, err %v; want ok false, nil", ok, err)
	}

	for i := 0; i < 4; i++ {
		if _, err := s.Insert(ctx, makeRun("job", base.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	last, ok, err := s.LastRun(ctx, "job")
	if err != nil || !ok {
		t.Fatalf("LastRun = ok %v, err %v", ok, err)
	}
	if want := base.Add(3 * time.Hour); !last.StartedAt.Equal(want) {
		t.Errorf("LastRun StartedAt = %s, want %s", last.StartedAt, want)
	}
}

// 5. LatestPerJob returns each job's newest run.
func TestLatestPerJob(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	jobs := map[string]int{"alpha": 3, "beta": 5, "gamma": 1}
	for job, count := range jobs {
		for i := 0; i < count; i++ {
			if _, err := s.Insert(ctx, makeRun(job, base.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatalf("Insert %s/%d: %v", job, i, err)
			}
		}
	}

	latest, err := s.LatestPerJob(ctx)
	if err != nil {
		t.Fatalf("LatestPerJob: %v", err)
	}
	if len(latest) != len(jobs) {
		t.Fatalf("len = %d, want %d", len(latest), len(jobs))
	}
	for job, count := range jobs {
		r, ok := latest[job]
		if !ok {
			t.Errorf("missing job %q", job)
			continue
		}
		want := base.Add(time.Duration(count-1) * time.Minute)
		if !r.StartedAt.Equal(want) {
			t.Errorf("job %q latest StartedAt = %s, want %s", job, r.StartedAt, want)
		}
		if r.JobName != job {
			t.Errorf("job %q has JobName %q", job, r.JobName)
		}
	}
}

// 6. Prune keeps only the newest keep runs per job and leaves others untouched.
func TestPrune(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	const total = 10
	for i := 0; i < total; i++ {
		if _, err := s.Insert(ctx, makeRun("trimmed", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("Insert trimmed/%d: %v", i, err)
		}
	}
	const otherCount = 4
	for i := 0; i < otherCount; i++ {
		if _, err := s.Insert(ctx, makeRun("kept", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("Insert kept/%d: %v", i, err)
		}
	}

	const keep = 3
	if err := s.Prune(ctx, "trimmed", keep); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	remaining, err := s.RecentRuns(ctx, "trimmed", 100)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(remaining) != keep {
		t.Fatalf("trimmed has %d runs, want %d", len(remaining), keep)
	}
	// The survivors must be the newest three (minutes 9, 8, 7).
	for i, r := range remaining {
		want := base.Add(time.Duration(total-1-i) * time.Minute)
		if !r.StartedAt.Equal(want) {
			t.Errorf("survivor %d StartedAt = %s, want %s", i, r.StartedAt, want)
		}
	}

	// The other job is unaffected.
	other, err := s.RecentRuns(ctx, "kept", 100)
	if err != nil {
		t.Fatalf("RecentRuns(kept): %v", err)
	}
	if len(other) != otherCount {
		t.Errorf("kept has %d runs, want %d", len(other), otherCount)
	}
}

// 7. Concurrent inserts do not hit "database is locked" and all land.
func TestConcurrentInsert(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	const n = 50
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := makeRun("concurrent", base.Add(time.Duration(i)*time.Millisecond))
			_, errs[i] = s.Insert(ctx, r)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Insert %d: %v", i, err)
		}
	}

	got, err := s.RecentRuns(ctx, "concurrent", n+10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(got) != n {
		t.Errorf("stored %d runs, want %d", len(got), n)
	}
}

// 8. Data survives Close and re-Open.
func TestPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := makeRun("persisted", base)
	id, err := s.Insert(ctx, want)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	want.ID = id
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close()

	got, ok, err := s2.LastRun(ctx, "persisted")
	if err != nil || !ok {
		t.Fatalf("LastRun after reopen = ok %v, err %v", ok, err)
	}
	if !equalRun(got, want) {
		t.Errorf("after reopen:\n got = %+v\nwant = %+v", got, want)
	}
}
