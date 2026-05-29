package engine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/store"
)

// everySchedule fires at a fixed interval relative to the current time. It gives
// tests deterministic sub-second cadence that the real scheduler (which rounds
// @every to whole seconds) cannot.
type everySchedule struct{ d time.Duration }

func (s everySchedule) Next(after time.Time) time.Time { return after.Add(s.d) }

// neverSchedule never fires.
type neverSchedule struct{}

func (neverSchedule) Next(time.Time) time.Time { return time.Time{} }

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rote.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// runFor runs the engine, lets it work for d, then cancels and waits (bounded)
// for Run to return.
func runFor(t *testing.T, e *Engine, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.Run(ctx) }()

	time.Sleep(d)
	cancel()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within bound after cancel")
	}
}

func recent(t *testing.T, st *store.Store, job string) []store.Run {
	t.Helper()
	runs, err := st.RecentRuns(context.Background(), job, 1000)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	// Sort ascending by start time for invariant checks.
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
	return runs
}

// New parses each schedule and reports a bad one.
func TestNewParsesSchedules(t *testing.T) {
	st := openStore(t)
	if _, err := New([]config.Job{{Name: "ok", Schedule: "@hourly", Command: "true"}}, st, nil); err != nil {
		t.Errorf("New with valid schedule: %v", err)
	}
	if _, err := New([]config.Job{{Name: "bad", Schedule: "not a schedule", Command: "true"}}, st, nil); err == nil {
		t.Errorf("New with invalid schedule: want error")
	}
}

// 1. Basic run: a fast command on a short interval produces several successful runs.
func TestBasicRun(t *testing.T) {
	st := openStore(t)
	entry := &jobEntry{
		cfg:   config.Job{Name: "tick", Command: "true", Timeout: time.Second},
		sched: everySchedule{80 * time.Millisecond},
	}
	e := newEngine([]*jobEntry{entry}, st, nil)

	runFor(t, e, 500*time.Millisecond)

	runs := recent(t, st, "tick")
	if len(runs) < 3 {
		t.Fatalf("got %d runs, want >= 3", len(runs))
	}
	for i, r := range runs {
		if !r.Success {
			t.Errorf("run %d not successful: %+v", i, r)
		}
		if r.ExitCode != 0 {
			t.Errorf("run %d exit code = %d, want 0", i, r.ExitCode)
		}
	}
}

// 2. Overlap skip: interval shorter than the command means runs must not overlap.
func TestOverlapSkip(t *testing.T) {
	st := openStore(t)
	entry := &jobEntry{
		cfg:   config.Job{Name: "slow", Command: "sleep 0.3", Timeout: 5 * time.Second},
		sched: everySchedule{100 * time.Millisecond},
	}
	e := newEngine([]*jobEntry{entry}, st, nil)

	runFor(t, e, 900*time.Millisecond)

	runs := recent(t, st, "slow")
	if len(runs) < 2 {
		t.Fatalf("got %d runs, want >= 2 to check overlap", len(runs))
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].StartedAt.Before(runs[i-1].FinishedAt) {
			t.Errorf("runs overlap: run %d started %s before run %d finished %s",
				i, runs[i].StartedAt, i-1, runs[i-1].FinishedAt)
		}
	}
}

// 3. Graceful shutdown: cancelling mid-run returns nil in bounded time and the
// in-flight run completes and is recorded (not killed).
func TestGracefulShutdown(t *testing.T) {
	st := openStore(t)
	entry := &jobEntry{
		cfg:   config.Job{Name: "inflight", Command: "sleep 0.4", Timeout: 5 * time.Second},
		sched: everySchedule{50 * time.Millisecond},
	}
	e := newEngine([]*jobEntry{entry}, st, nil)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.Run(ctx) }()

	time.Sleep(120 * time.Millisecond) // let a run get in flight
	cancel()

	start := time.Now()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within bound after cancel")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s, want bounded", elapsed)
	}

	runs := recent(t, st, "inflight")
	if len(runs) < 1 {
		t.Fatalf("got %d runs, want >= 1 (the in-flight run should be recorded)", len(runs))
	}
	for i, r := range runs {
		if !r.Success || r.TimedOut {
			t.Errorf("run %d was not completed cleanly: success=%v timedOut=%v", i, r.Success, r.TimedOut)
		}
	}
}

// 4. on_failure: a failing job's hook runs and creates its marker file.
func TestOnFailureHook(t *testing.T) {
	st := openStore(t)
	marker := filepath.Join(t.TempDir(), "failed.marker")
	entry := &jobEntry{
		cfg: config.Job{
			Name:      "flaky",
			Command:   "exit 1",
			Timeout:   time.Second,
			OnFailure: "touch " + marker,
		},
		sched: everySchedule{50 * time.Millisecond},
	}
	e := newEngine([]*jobEntry{entry}, st, nil)

	runFor(t, e, 250*time.Millisecond)

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on_failure marker not created: %v", err)
	}
	runs := recent(t, st, "flaky")
	if len(runs) < 1 {
		t.Fatalf("got %d runs, want >= 1", len(runs))
	}
	for i, r := range runs {
		if r.Success {
			t.Errorf("run %d unexpectedly succeeded", i)
		}
	}
}

// 5. A never-firing schedule lets its goroutine exit cleanly (no leak): Run
// returns without needing cancellation.
func TestNeverFiresExits(t *testing.T) {
	st := openStore(t)
	entry := &jobEntry{
		cfg:   config.Job{Name: "never", Command: "true"},
		sched: neverSchedule{},
	}
	e := newEngine([]*jobEntry{entry}, st, nil)

	errc := make(chan error, 1)
	go func() { errc <- e.Run(context.Background()) }()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return for a never-firing job (goroutine leak)")
	}

	if runs := recent(t, st, "never"); len(runs) != 0 {
		t.Errorf("got %d runs for never-firing job, want 0", len(runs))
	}
}
