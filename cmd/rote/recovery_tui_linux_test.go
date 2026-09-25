package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/store"
)

// Exercise the real dispatcher, input loop and refresh timer in a terminal.
// Unit tests separately assert that recovered frames contain no stale errors.
func TestDashboardReadRecovery(t *testing.T) {
	for _, output := range []bool{false, true} {
		for _, manual := range []bool{false, true} {
			t.Run(fmt.Sprintf("output=%t/manual=%t", output, manual), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "rote.db")
				st, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { st.Close() })
				if _, err := st.Insert(context.Background(), store.Run{
					JobName: "idle", StartedAt: time.Now(), Success: true,
					Stdout: []byte("terminal-recovered-output"),
				}); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				p, terminal := startDashboard(t, "tui", "-c", idleSchedulerConfig(t), "--db", path)
				// Atomically swap in an error-producing view, so the process
				// cannot observe a half-completed schema change between ticks.
				stdout, runErr := "stdout", "err"
				if output {
					stdout = "abs(-9223372036854775808)"
				} else {
					runErr = "abs(-9223372036854775808)"
				}
				_, err = db.Exec(fmt.Sprintf(`BEGIN;
ALTER TABLE runs RENAME TO saved_runs;
CREATE VIEW runs AS SELECT id, job_name, started_at, finished_at, duration,
exit_code, timed_out, success, %s AS stdout, stderr, stdout_truncated,
stderr_truncated, %s AS err FROM saved_runs;
COMMIT;`, stdout, runErr))
				if err != nil {
					t.Fatal(err)
				}
				if output {
					if _, err := terminal.Write([]byte("\r")); err != nil {
						t.Fatal(err)
					}
					p.waitOutput(t, "error loading output:")
				} else {
					p.waitOutput(t, "error loading data:")
					if _, err := terminal.Write([]byte("\r")); err != nil {
						t.Fatal(err)
					}
					p.waitOutput(t, "job: idle")
				}
				if _, err := db.Exec(`BEGIN; DROP VIEW runs; ALTER TABLE saved_runs RENAME TO runs; COMMIT;`); err != nil {
					t.Fatal(err)
				}
				if manual {
					if _, err := terminal.Write([]byte("r")); err != nil {
						t.Fatal(err)
					}
				}
				p.waitOutput(t, "terminal-recovered-output")
				if _, err := terminal.Write([]byte("q")); err != nil {
					t.Fatal(err)
				}
				p.wait(t, 0)
				runs, err := st.RecentRuns(context.Background(), "idle", 0)
				if err != nil || len(runs) != 1 || string(runs[0].Stdout) != "terminal-recovered-output" {
					t.Fatalf("read-only recovery changed stored data: runs=%+v err=%v", runs, err)
				}
			})
		}
	}
}
