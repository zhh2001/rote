//go:build darwin || linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhh2001/rote/internal/store"
)

func recordedRun(t *testing.T, db, job string) store.Run {
	t.Helper()
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs, err := st.RecentRuns(context.Background(), job, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected one recorded run, got %d: %v", len(runs), err)
	}
	return runs[0]
}

func TestRunProcessExitCodes(t *testing.T) {
	cases := []struct {
		name, command, timeout string
		cliCode, shellCode     int
		timedOut, runnerError  bool
		emptyPath              bool
	}{
		{name: "success", command: "echo done"},
		{name: "nonzero", command: "exit 3", cliCode: 3, shellCode: 3},
		{name: "maximum exit code", command: "exit 255", cliCode: 255, shellCode: 255},
		{name: "zero timeout", command: "sleep 0.02; echo done", timeout: "0s"},
		{name: "timeout", command: "sleep 10", timeout: "100ms", cliCode: 124, shellCode: -1, timedOut: true},
		{name: "timeout after shell exit", command: "sleep 10 & echo spawned", timeout: "100ms", cliCode: 124, timedOut: true},
		{name: "signal", command: "kill -TERM $$", cliCode: 126, shellCode: -1},
		{name: "start failure", command: "true", cliCode: 126, shellCode: -1, runnerError: true, emptyPath: true},
		{name: "command not found", command: "rote_test_missing_command", cliCode: 127, shellCode: 127},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeConfig(t, fmt.Sprintf("[[job]]\nname='sample'\nschedule='@hourly'\ncommand=%q\ntimeout=%q\n", tc.command, tc.timeout))
			db := filepath.Join(t.TempDir(), "rote.db")
			p := newCLIProcess(t, "run", "-c", cfg, "--db", db, "sample")
			if tc.emptyPath {
				p.cmd.Env = append(p.cmd.Env, "PATH=")
			}
			p.start(t)
			p.wait(t, tc.cliCode)
			run := recordedRun(t, db, "sample")
			if run.ExitCode != tc.shellCode || run.TimedOut != tc.timedOut || (run.Err != "") != tc.runnerError || run.Success != (tc.cliCode == 0) {
				t.Errorf("unexpected persisted result: %+v", run)
			}
			status := "failed"
			if tc.cliCode == 0 {
				status = "ok"
			}
			for _, text := range []string{
				"job sample: " + status,
				fmt.Sprintf("exit code: %d", tc.shellCode),
				fmt.Sprintf("timed out: %t", tc.timedOut),
			} {
				if !strings.Contains(p.output.String(), text) {
					t.Errorf("summary missing %q: %s", text, p.output.String())
				}
			}
		})
	}
}

func TestRunProcessWaitDelayFailure(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	// Keep the pipe open well beyond WaitDelay, independent of machine load.
	// Clean up this test's background process even when an assertion fails.
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			t.Errorf("invalid child PID %q: %v", data, err)
			return
		}
		child, err := os.FindProcess(pid)
		if err != nil {
			t.Error(err)
			return
		}
		defer child.Release()
		if err := child.Kill(); err != nil && !os.IsNotExist(err) && err != os.ErrProcessDone {
			t.Errorf("clean up background child: %v", err)
		}
	})
	command := fmt.Sprintf("sleep 30 & echo $! > %q; echo parent-done", pidFile)
	cfg := writeConfig(t, fmt.Sprintf("[[job]]\nname='background'\nschedule='@hourly'\ncommand=%q\n", command))
	db := filepath.Join(dir, "rote.db")
	p := newCLIProcess(t, "run", "-c", cfg, "--db", db, "background")
	p.start(t)
	p.wait(t, 126)

	run := recordedRun(t, db, "background")
	if run.Success || run.TimedOut || run.ExitCode != 0 || run.Err != exec.ErrWaitDelay.Error() || string(run.Stdout) != "parent-done\n" {
		t.Fatalf("unexpected WaitDelay result: %+v", run)
	}
	for _, text := range []string{"job background: failed", "exit code: 0", exec.ErrWaitDelay.Error()} {
		if !strings.Contains(p.output.String(), text) {
			t.Errorf("summary missing %q: %s", text, p.output.String())
		}
	}
}

func TestNegativeTimeoutRejectedBeforeExecution(t *testing.T) {
	for _, command := range []string{"run", "start", "", "list", "tui"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "must-not-run")
			cfg := writeConfig(t, fmt.Sprintf("[[job]]\nname='negative'\nschedule='every 1s'\ncommand=%q\ntimeout='-1s'\n", fmt.Sprintf("touch %q", marker)))
			db := filepath.Join(dir, "rote.db")
			var args []string
			if command != "" {
				args = append(args, command)
			}
			args = append(args, "-c", cfg, "--db", db)
			if command == "run" {
				args = append(args, "negative")
			}
			p := newCLIProcess(t, args...)
			p.start(t)
			p.wait(t, 1)
			for _, text := range []string{"negative", "timeout", "-1s", "non-negative"} {
				if !strings.Contains(p.output.String(), text) {
					t.Errorf("error missing %q: %s", text, p.output.String())
				}
			}
			for _, path := range []string{marker, db, db + ".lock"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("invalid config created %s (stat error: %v)", path, err)
				}
			}
		})
	}
}
