//go:build darwin || linux

package main

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zhh2001/rote/internal/store"
)

func TestConcurrentManualWritersWithScheduler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedHistory(t, st, "other", 3)
	cfg := writeConfig(t, `
[[job]]
name = "bounded"
schedule = "@hourly"
command = "printf concurrent-output"
history_limit = 4
`)
	owner := startHeadless(t, cfg, path)
	var processes []*cliProcess
	for i := 0; i < 8; i++ {
		p := newCLIProcess(t, "run", "-c", cfg, "--db", path, "bounded")
		p.start(t)
		processes = append(processes, p)
	}
	// A reader remains usable while separate CLI processes are persisting.
	reader := newCLIProcess(t, "list", "-c", cfg, "--db", path)
	reader.start(t)
	reader.wait(t, 0)
	for _, p := range processes {
		p.wait(t, 0)
		if !strings.Contains(p.output.String(), "concurrent-output") {
			t.Errorf("manual run lost output: %s", p.output.String())
		}
	}
	if err := owner.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	owner.wait(t, 0)
	assertRetainedCounts(t, st, 4, 3)
	rows, err := st.RecentRuns(context.Background(), "bounded", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if !row.Success || string(row.Stdout) != "concurrent-output" {
			t.Errorf("bad persisted result: %+v", row)
		}
	}
}
