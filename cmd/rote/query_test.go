package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/store"
)

// Guard output columns with an error-producing expression so these tests catch
// accidental blob reads without relying on allocations or timing thresholds.
func guardedOutputStore(t *testing.T, allowLatest bool) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rote.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	var latestID int64
	for i, minute := range []int{-1, 0, 0, -2} {
		id, err := st.Insert(ctx, store.Run{
			JobName: "job", StartedAt: now.Add(time.Duration(minute) * time.Minute),
			Success: i == 2, ExitCode: i, Duration: time.Second,
			Stdout: []byte(fmt.Sprintf("stdout-%d", i)), Stderr: []byte(fmt.Sprintf("stderr-%d", i)),
			StdoutTruncated: i == 2, StderrTruncated: i == 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && allowLatest {
			latestID = id
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE runs RENAME TO stored_runs`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(fmt.Sprintf(`CREATE VIEW runs AS SELECT
id, job_name, started_at, finished_at, duration, exit_code, timed_out, success,
CASE WHEN id = %d THEN stdout ELSE abs(-9223372036854775808) END AS stdout,
CASE WHEN id = %d THEN stderr ELSE abs(-9223372036854775808) END AS stderr,
stdout_truncated, stderr_truncated, err FROM stored_runs`, latestID, latestID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecentRuns(ctx, "job", 20); err == nil {
		t.Fatal("guard did not reject reading unwanted output")
	}
	return st
}

func TestReadCommandsOnlyLoadRequestedOutput(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		st := guardedOutputStore(t, false)
		jobs := []config.Job{{Name: "job", Schedule: "@hourly"}, {Name: "fresh", Schedule: "@daily"}}
		var out bytes.Buffer
		if err := cmdList(context.Background(), &out, jobs, st, time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"job", "fresh", "✓"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("list missing %q: %s", want, &out)
			}
		}
	})
	for _, showOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("logs/output=%t", showOutput), func(t *testing.T) {
			st := guardedOutputStore(t, showOutput)
			var out bytes.Buffer
			if err := cmdLogs(context.Background(), &out, st, "job", 20, showOutput); err != nil {
				t.Fatal(err)
			}
			if showOutput {
				for _, want := range []string{"stdout-2", "stderr-2", "truncated"} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("logs missing %q: %s", want, &out)
					}
				}
			} else if strings.Contains(out.String(), "stdout") || strings.Count(out.String(), "\n") != 5 {
				t.Errorf("unexpected metadata-only logs: %s", &out)
			}
		})
	}
}

func TestCmdLogsOutputReadError(t *testing.T) {
	st := guardedOutputStore(t, false)
	if err := cmdLogs(context.Background(), io.Discard, st, "job", 20, true); err == nil {
		t.Fatal("output read failure was swallowed")
	}
}

// Change the database immediately after the history query, during table
// rendering. This deterministically exercises the two-read race without sleeps.
type firstWriteHook struct {
	bytes.Buffer
	hook func()
}

func (w *firstWriteHook) Write(p []byte) (int, error) {
	if w.hook != nil {
		hook := w.hook
		w.hook = nil
		hook()
	}
	return w.Buffer.Write(p)
}

func TestCmdLogsOutputKeepsDisplayedRunIdentity(t *testing.T) {
	for _, keep := range []int{0, 1} {
		t.Run(fmt.Sprintf("retention=%d", keep), func(t *testing.T) {
			st := openStore(t)
			ctx := context.Background()
			now := time.Now()
			id, err := st.Insert(ctx, store.Run{
				JobName: "job", StartedAt: now, Success: true,
				Stdout: []byte("displayed-output"),
			})
			if err != nil {
				t.Fatal(err)
			}
			w := &firstWriteHook{hook: func() {
				_, err := st.InsertWithRetention(ctx, store.Run{
					JobName: "job", StartedAt: now.Add(time.Second),
					Stdout: []byte("different-run-output"),
				}, keep)
				if err != nil {
					t.Fatal(err)
				}
			}}
			if err := cmdLogs(ctx, w, st, "job", 20, true); err != nil {
				t.Fatal(err)
			}
			if w.hook != nil || strings.Contains(w.String(), "different-run-output") {
				t.Fatalf("output must belong to the displayed run: %s", w.String())
			}
			want := "displayed-output"
			if keep == 1 {
				want = fmt.Sprintf("last run output unavailable (run %d no longer exists)", id)
				if strings.Contains(w.String(), "(empty)") || strings.Contains(w.String(), "displayed-output") {
					t.Fatalf("pruned output should be unavailable, not empty or stale: %s", w.String())
				}
			}
			if !strings.Contains(w.String(), want) {
				t.Errorf("missing %q: %s", want, w.String())
			}
		})
	}
}

func TestCmdLogsCountDefaults(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		if _, err := st.Insert(ctx, store.Run{JobName: "job", StartedAt: time.Unix(int64(i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	for n, want := range map[int]int{-1: 20, 0: 20, 1: 1, 99: 25} {
		var out bytes.Buffer
		if err := cmdLogs(ctx, &out, st, "job", n, false); err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(out.String(), "\n"); got != want+1 {
			t.Errorf("n=%d: got %d lines, want %d", n, got, want+1)
		}
	}
	var empty bytes.Buffer
	if err := cmdLogs(ctx, &empty, st, "missing", 0, true); err != nil || empty.String() != "no runs recorded for \"missing\"\n" {
		t.Errorf("empty -o: %q, %v", &empty, err)
	}
}

func TestReadCommandsCanceled(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	jobs := []config.Job{{Name: "job", Schedule: "@hourly"}}
	if err := cmdList(ctx, io.Discard, jobs, st, time.Now()); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled list: %v", err)
	}
	if err := cmdLogs(ctx, io.Discard, st, "job", 20, true); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled logs: %v", err)
	}
}

type failingQueryWriter struct{}

func (failingQueryWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCmdLogsTableWriteError(t *testing.T) {
	st := guardedOutputStore(t, false)
	if err := cmdLogs(context.Background(), failingQueryWriter{}, st, "job", 20, true); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("table write error should be returned before output is loaded: %v", err)
	}
}
