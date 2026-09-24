//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/store"
)

// A controlled descendant blocks until released. Its marker proves whether it
// survived cancellation, without mistaking an unreaped zombie for a live job.
type blockedCommand struct {
	command, pidFile, release, finished string
}

func newBlockedCommand(t *testing.T, shellExits bool) blockedCommand {
	t.Helper()
	dir := t.TempDir()
	b := blockedCommand{
		pidFile: filepath.Join(dir, "pid"),
		release: filepath.Join(dir, "release"), finished: filepath.Join(dir, "finished"),
	}
	// The iteration bound is a final safety net if the test process itself dies.
	b.command = fmt.Sprintf("(n=0; while [ ! -f %q ] && [ \"$n\" -lt 1500 ]; do sleep 0.02; n=$((n + 1)); done; touch %q; echo child-finished) & echo $$ > %q", b.release, b.finished, b.pidFile)
	if !shellExits {
		b.command += "; wait"
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(b.pidFile)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			t.Errorf("invalid fixture process group %q: %v", data, err)
			return
		}
		// Only the process group created by this fixture is targeted.
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	return b
}

func (b blockedCommand) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if data, err := os.ReadFile(b.pidFile); err == nil && strings.HasSuffix(string(data), "\n") {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("job did not start")
		case <-tick.C:
		}
	}
}

func (b blockedCommand) unblock(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(b.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (b blockedCommand) assertTerminated(t *testing.T) {
	t.Helper()
	b.unblock(t)
	// A surviving child polls every 20ms; allow ample time to reveal itself.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(b.finished); !os.IsNotExist(err) {
		t.Fatalf("job descendant survived shutdown: %v", err)
	}
}

func shutdownConfig(t *testing.T, command, hook string) string {
	t.Helper()
	return writeConfig(t, fmt.Sprintf("[[job]]\nname='blocking'\nschedule='every 1s'\ncommand=%q\non_failure=%q\n", command, hook))
}

func assertLockHeld(t *testing.T, db string) {
	t.Helper()
	st, err := store.OpenScheduler(db)
	if st != nil {
		st.Close()
	}
	if !errors.Is(err, store.ErrSchedulerRunning) {
		t.Fatalf("scheduler released lock while work was draining: %v", err)
	}
}

func assertSchedulerRestarts(t *testing.T, db string) {
	t.Helper()
	next := startHeadless(t, idleSchedulerConfig(t), db)
	if err := next.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	next.wait(t, 0)
}

func TestHeadlessTwoStageShutdown(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			b := newBlockedCommand(t, false)
			hookMarker := filepath.Join(t.TempDir(), "must-not-hook")
			cfg := shutdownConfig(t, b.command, fmt.Sprintf("touch %q", hookMarker))
			db := filepath.Join(t.TempDir(), "rote.db")
			p := startHeadless(t, cfg, db)
			b.waitReady(t)
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			p.waitOutput(t, "stopping scheduler; waiting")
			assertLockHeld(t, db)
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			code := 143
			if sig == os.Interrupt {
				code = 130
			}
			p.wait(t, code)
			run := recordedRun(t, db, "blocking")
			if run.Success || run.TimedOut || run.ExitCode != -1 || run.Err != context.Canceled.Error() {
				t.Fatalf("unexpected forced result: %+v", run)
			}
			b.assertTerminated(t)
			if _, err := os.Stat(hookMarker); !os.IsNotExist(err) {
				t.Errorf("forced stop started failure hook: %v", err)
			}
			assertSchedulerRestarts(t, db)
		})
	}
}

func TestHeadlessGracefulShutdownFinishesJob(t *testing.T) {
	b := newBlockedCommand(t, false)
	cfg := shutdownConfig(t, b.command, "")
	db := filepath.Join(t.TempDir(), "rote.db")
	p := startHeadless(t, cfg, db)
	b.waitReady(t)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.waitOutput(t, "stopping scheduler; waiting")
	assertLockHeld(t, db)
	b.unblock(t)
	p.wait(t, 0)
	run := recordedRun(t, db, "blocking")
	if !run.Success || run.Err != "" || string(run.Stdout) != "child-finished\n" {
		t.Fatalf("graceful shutdown did not preserve job: %+v", run)
	}
	assertSchedulerRestarts(t, db)
}

func TestHeadlessForceStopsHook(t *testing.T) {
	b := newBlockedCommand(t, false)
	cfg := shutdownConfig(t, "exit 7", b.command)
	db := filepath.Join(t.TempDir(), "rote.db")
	p := startHeadless(t, cfg, db)
	b.waitReady(t)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.waitOutput(t, "stopping scheduler; waiting")
	assertLockHeld(t, db)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.wait(t, 143)
	run := recordedRun(t, db, "blocking")
	if run.Success || run.ExitCode != 7 || run.Err != "" {
		t.Fatalf("hook cancellation overwrote job result: %+v", run)
	}
	b.assertTerminated(t)
	assertSchedulerRestarts(t, db)
}

func TestManualRunCancelAfterShellExit(t *testing.T) {
	b := newBlockedCommand(t, true)
	cfg := shutdownConfig(t, b.command, "")
	db := filepath.Join(t.TempDir(), "rote.db")
	p := newCLIProcess(t, "run", "-c", cfg, "--db", db, "blocking")
	p.start(t)
	b.waitReady(t)
	time.Sleep(100 * time.Millisecond) // allow the shell to exit, leaving the pipe open
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.wait(t, 126)
	run := recordedRun(t, db, "blocking")
	if run.Success || run.TimedOut || run.ExitCode != 0 || run.Err != context.Canceled.Error() {
		t.Fatalf("cancellation misclassified: %+v", run)
	}
	if !strings.Contains(p.output.String(), "job blocking: failed") || !strings.Contains(p.output.String(), "context canceled") {
		t.Fatalf("summary did not explain cancellation: %s", p.output.String())
	}
	b.assertTerminated(t)
}
