//go:build darwin || linux

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/store"
)

func retentionFixture(t *testing.T, limit int) (string, string, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rote.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seedHistory(t, st, "bounded", 5)
	seedHistory(t, st, "other", 3)
	cfg := writeConfig(t, fmt.Sprintf("[[job]]\nname='bounded'\nschedule='every 1s'\ncommand='echo new-output'\nhistory_limit=%d\n", limit))
	return cfg, path, st
}

func assertRetainedCounts(t *testing.T, st *store.Store, bounded, other int) {
	t.Helper()
	for name, want := range map[string]int{"bounded": bounded, "other": other} {
		runs, err := st.RecentRunsMeta(context.Background(), name, 0)
		if err != nil || len(runs) != want {
			t.Fatalf("%s history len=%d, want %d: %v", name, len(runs), want, err)
		}
	}
}

func TestRetentionSchedulerAndManualRuns(t *testing.T) {
	cfg, path, st := retentionFixture(t, 2)
	owner := startHeadless(t, cfg, path)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		runs, err := st.RecentRunsMeta(context.Background(), "bounded", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 2 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("scheduler never applied retention")
		case <-tick.C:
		}
	}
	// A manual writer remains usable while the scheduler owns the same DB.
	manual := newCLIProcess(t, "run", "-c", cfg, "--db", path, "bounded")
	manual.start(t)
	manual.wait(t, 0)
	if err := owner.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	owner.wait(t, 0)
	assertRetainedCounts(t, st, 2, 3)
	// Also verify manual cleanup without a scheduler that could mask its absence.
	manual = newCLIProcess(t, "run", "-c", cfg, "--db", path, "bounded")
	manual.start(t)
	manual.wait(t, 0)
	assertRetainedCounts(t, st, 2, 3)
	if !strings.Contains(manual.output.String(), "new-output") {
		t.Fatalf("manual summary lost captured output: %s", manual.output.String())
	}
}

func TestRetentionReadOnlyCommandsDoNotPrune(t *testing.T) {
	cfg, path, st := retentionFixture(t, 1)
	for _, args := range [][]string{
		{"list", "-c", cfg, "--db", path},
		{"logs", "--db", path, "-n", "20", "-o", "bounded"},
	} {
		p := newCLIProcess(t, args...)
		p.start(t)
		p.wait(t, 0)
		assertRetainedCounts(t, st, 5, 3)
	}
}

func TestInvalidHistoryLimitRejectedBeforeExecution(t *testing.T) {
	for _, command := range []string{"run", "start", "", "list", "tui"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "must-not-run")
			cfg := writeConfig(t, fmt.Sprintf("[[job]]\nname='invalid'\nschedule='every 1s'\ncommand=%q\nhistory_limit=-1\n", fmt.Sprintf("touch %q", marker)))
			path := filepath.Join(dir, "rote.db")
			var args []string
			if command != "" {
				args = append(args, command)
			}
			args = append(args, "-c", cfg, "--db", path)
			if command == "run" {
				args = append(args, "invalid")
			}
			p := newCLIProcess(t, args...)
			p.start(t)
			p.wait(t, 1)
			for _, text := range []string{"invalid", "history_limit", "non-negative"} {
				if !strings.Contains(p.output.String(), text) {
					t.Errorf("missing %q in error: %s", text, p.output.String())
				}
			}
			for _, name := range []string{marker, path, path + ".lock"} {
				if _, err := os.Stat(name); !os.IsNotExist(err) {
					t.Errorf("invalid configuration created %s: %v", name, err)
				}
			}
		})
	}
}
