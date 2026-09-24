package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/config"
)

func waitMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("command did not create %s", marker)
		case <-tick.C:
		}
	}
}

func runUntilCleanup(t *testing.T, e *Engine) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := e.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		e.ForceStop()
		awaitEngine(t, done)
	})
	return cancel, done
}

func awaitEngine(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not finish after force stop")
	}
}

func TestForceStopCancelsAllJobsAndSkipsHooks(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	var entries []*jobEntry
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("job-%d", i)
		entries = append(entries, &jobEntry{
			cfg: config.Job{
				Name:      name,
				Command:   fmt.Sprintf("echo ready > %q; sleep 30", filepath.Join(dir, name)),
				OnFailure: fmt.Sprintf("touch %q", filepath.Join(dir, "must-not-hook")),
			},
			sched: everySchedule{20 * time.Millisecond},
		})
	}
	e := newEngine(entries, st, nil)
	cancel, done := runUntilCleanup(t, e)
	for _, j := range entries {
		waitMarker(t, filepath.Join(dir, j.cfg.Name))
	}
	cancel()
	select {
	case <-done:
		t.Fatal("graceful stop canceled running jobs")
	case <-time.After(50 * time.Millisecond):
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.ForceStop() }()
	}
	wg.Wait()
	awaitEngine(t, done)
	for _, j := range entries {
		runs := recent(t, st, j.cfg.Name)
		if len(runs) != 1 || runs[0].Success || runs[0].TimedOut || runs[0].Err != context.Canceled.Error() {
			t.Errorf("unexpected canceled history for %s: %+v", j.cfg.Name, runs)
		}
		if j.running.Load() {
			t.Errorf("job %s still marked running", j.cfg.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "must-not-hook")); !os.IsNotExist(err) {
		t.Errorf("forced stop started a failure hook: %v", err)
	}
}

func TestForceStopCancelsRunningHook(t *testing.T) {
	st := openStore(t)
	marker := filepath.Join(t.TempDir(), "hook-ready")
	entry := &jobEntry{
		cfg:   config.Job{Name: "failure", Command: "exit 7", OnFailure: fmt.Sprintf("echo ready > %q; sleep 30", marker)},
		sched: everySchedule{20 * time.Millisecond},
	}
	e := newEngine([]*jobEntry{entry}, st, nil)
	_, done := runUntilCleanup(t, e)
	waitMarker(t, marker)
	e.ForceStop() // also stops scheduling, even without a preceding graceful stop
	awaitEngine(t, done)
	runs := recent(t, st, "failure")
	if len(runs) != 1 || runs[0].Success || runs[0].ExitCode != 7 || runs[0].Err != "" {
		t.Fatalf("hook cancellation changed original job result: %+v", runs)
	}
}

func TestStopBeforeRunDoesNotExecute(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%t", forced), func(t *testing.T) {
			st := openStore(t)
			marker := filepath.Join(t.TempDir(), "must-not-run")
			e := newEngine([]*jobEntry{{
				cfg:   config.Job{Name: "stopped", Command: fmt.Sprintf("touch %q", marker)},
				sched: everySchedule{-time.Second}, // timer would be ready immediately
			}}, st, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if forced {
				e.ForceStop()
			} else {
				cancel()
			}
			if err := e.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if runs := recent(t, st, "stopped"); len(runs) != 0 {
				t.Fatalf("shutdown dispatched a run: %+v", runs)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("shutdown executed command: %v", err)
			}
		})
	}
}

type cancelingSchedule struct{ stop context.CancelFunc }

func (s cancelingSchedule) Next(after time.Time) time.Time {
	s.stop()
	return after.Add(-time.Second)
}

func TestShutdownWithReadyTimerDoesNotDispatch(t *testing.T) {
	for i := 0; i < 20; i++ {
		st := openStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		entry := &jobEntry{cfg: config.Job{Name: "stopped", Command: "true"}}
		e := newEngine([]*jobEntry{entry}, st, nil)
		stop := cancel
		if i%2 == 0 {
			stop = e.ForceStop
		}
		// Cancellation and a ready timer become observable together.
		entry.sched = cancelingSchedule{stop: stop}
		if err := e.Run(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		if runs := recent(t, st, "stopped"); len(runs) != 0 {
			t.Fatalf("shutdown dispatched work on iteration %d: %+v", i, runs)
		}
	}
}
