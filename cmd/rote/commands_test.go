package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/store"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rote.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// 2. run executes a job, records it, and prints a useful summary.
func TestCmdRun(t *testing.T) {
	cfg := writeConfig(t, `
[[job]]
name = "greet"
schedule = "@hourly"
command = "echo hello-from-rote"
`)
	jobs, err := config.Load(cfg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	st := openStore(t)

	var buf bytes.Buffer
	code, err := cmdRun(context.Background(), &buf, jobs, st, "greet")
	if err != nil {
		t.Fatalf("cmdRun: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 for a successful job", code)
	}

	out := buf.String()
	for _, want := range []string{"greet", "exit code: 0", "duration:", "hello-from-rote"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}

	runs, err := st.RecentRuns(context.Background(), "greet", 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if !r.Success || r.ExitCode != 0 {
		t.Errorf("recorded run = success %v exit %d, want success exit 0", r.Success, r.ExitCode)
	}
	if !strings.Contains(string(r.Stdout), "hello-from-rote") {
		t.Errorf("recorded stdout = %q", r.Stdout)
	}
}

// 2b. An unknown job name errors and lists the available jobs.
func TestCmdRunUnknownJob(t *testing.T) {
	cfg := writeConfig(t, `
[[job]]
name = "greet"
schedule = "@hourly"
command = "true"
`)
	jobs, _ := config.Load(cfg)
	st := openStore(t)

	var buf bytes.Buffer
	code, err := cmdRun(context.Background(), &buf, jobs, st, "missing")
	if err == nil {
		t.Fatal("cmdRun succeeded, want error")
	}
	if code != 127 {
		t.Errorf("exit code = %d, want 127 for an unknown job", code)
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing") || !strings.Contains(msg, "not found") || !strings.Contains(msg, "greet") {
		t.Errorf("error %q should name the job, say not found, and list available jobs", msg)
	}
}

// 2c. The run exit code propagates: success, non-zero exit, and timeout.
func TestCmdRunExitCodes(t *testing.T) {
	cfg := writeConfig(t, `
[[job]]
name = "ok"
schedule = "@hourly"
command = "true"

[[job]]
name = "fail"
schedule = "@hourly"
command = "exit 3"

[[job]]
name = "slow"
schedule = "@hourly"
command = "sleep 5"
timeout = "100ms"
`)
	jobs, err := config.Load(cfg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	st := openStore(t)

	cases := []struct {
		job  string
		want int
	}{
		{"ok", 0},
		{"fail", 3},
		{"slow", 124},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		code, err := cmdRun(context.Background(), &buf, jobs, st, c.job)
		if err != nil {
			t.Fatalf("cmdRun(%s): %v", c.job, err)
		}
		if code != c.want {
			t.Errorf("cmdRun(%s) exit code = %d, want %d", c.job, code, c.want)
		}
	}
}

// 3. list renders a table with status, relative next/last times.
func TestCmdList(t *testing.T) {
	cfg := writeConfig(t, `
[[job]]
name = "withhistory"
schedule = "@hourly"
command = "true"

[[job]]
name = "fresh"
schedule = "@daily"
command = "true"
`)
	jobs, _ := config.Load(cfg)
	st := openStore(t)

	now := time.Now()
	if _, err := st.Insert(context.Background(), store.Run{
		JobName:   "withhistory",
		StartedAt: now.Add(-5 * time.Minute),
		Duration:  1200 * time.Millisecond,
		Success:   true,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var buf bytes.Buffer
	if err := cmdList(context.Background(), &buf, jobs, st, now); err != nil {
		t.Fatalf("cmdList: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"NAME", "SCHEDULE", "NEXT", "LAST", "withhistory", "fresh", "✓", "ago", "in "} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	// The history-less job shows a placeholder.
	if !strings.Contains(out, "-") {
		t.Errorf("expected a %q placeholder for the job without history:\n%s", "-", out)
	}
}

// 4. logs honors the count and the -o output flag.
func TestCmdLogs(t *testing.T) {
	st := openStore(t)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 4; i++ {
		if _, err := st.Insert(context.Background(), store.Run{
			JobName:   "job",
			StartedAt: base.Add(time.Duration(i) * time.Minute),
			Duration:  time.Duration(i+1) * 100 * time.Millisecond,
			ExitCode:  i,
			Success:   i == 0,
			Stdout:    []byte("output-line-" + string(rune('A'+i))),
		}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	// Limit to 2 rows (newest first), no output.
	var buf bytes.Buffer
	if err := cmdLogs(context.Background(), &buf, st, "job", 2, false); err != nil {
		t.Fatalf("cmdLogs: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "TIME") || !strings.Contains(out, "STATUS") {
		t.Errorf("logs missing header:\n%s", out)
	}
	// Two data rows + one header row.
	if rows := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1; rows != 3 {
		t.Errorf("got %d total lines, want 3 (header + 2 rows):\n%s", rows, out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("logs should show the failure symbol:\n%s", out)
	}

	// With -o and the full history, both symbols appear and the latest run's
	// stdout (output-line-D) is included.
	var buf2 bytes.Buffer
	if err := cmdLogs(context.Background(), &buf2, st, "job", 20, true); err != nil {
		t.Fatalf("cmdLogs -o: %v", err)
	}
	full := buf2.String()
	if !strings.Contains(full, "✓") || !strings.Contains(full, "✗") {
		t.Errorf("full logs should show both success and failure symbols:\n%s", full)
	}
	if !strings.Contains(full, "output-line-D") {
		t.Errorf("logs -o missing latest stdout:\n%s", full)
	}

	// A job with no history is reported, not errored.
	var buf3 bytes.Buffer
	if err := cmdLogs(context.Background(), &buf3, st, "nobody", 5, false); err != nil {
		t.Fatalf("cmdLogs(empty): %v", err)
	}
	if !strings.Contains(buf3.String(), "no runs recorded") {
		t.Errorf("expected empty-history note, got:\n%s", buf3.String())
	}
}

// 5. Missing config and malformed TOML produce user errors and non-zero codes.
func TestRunRunConfigErrors(t *testing.T) {
	db := filepath.Join(t.TempDir(), "rote.db")

	var stderr bytes.Buffer
	code := runRun([]string{"-c", filepath.Join(t.TempDir(), "nope.toml"), "--db", db, "anyjob"}, io.Discard, &stderr)
	if code == 0 {
		t.Errorf("missing config: exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "rote:") {
		t.Errorf("missing config: stderr lacks a rote error:\n%s", stderr.String())
	}

	bad := writeConfig(t, "this is = definitely [[ not toml")
	stderr.Reset()
	code = runRun([]string{"-c", bad, "--db", db, "anyjob"}, io.Discard, &stderr)
	if code == 0 {
		t.Errorf("bad TOML: exit code = 0, want non-zero")
	}
	if stderr.Len() == 0 {
		t.Errorf("bad TOML: expected an error on stderr")
	}
}

// 6. start returns cleanly in bounded time when its context is canceled.
func TestCmdStartShutdown(t *testing.T) {
	cfg := writeConfig(t, `
[[job]]
name = "idle"
schedule = "@hourly"
command = "true"
`)
	jobs, _ := config.Load(cfg)
	st := openStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- cmdStart(ctx, logger, jobs, st) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("cmdStart returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cmdStart did not return within bound after cancel")
	}
}
