package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// A real pseudo-terminal exercises both TUI entry points without replacing the
// dashboard or depending on an interactive terminal in the test environment.
func startDashboard(t *testing.T, args ...string) (*cliProcess, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudo-terminal unavailable: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 100}); err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	p := newCLIProcess(t, args...)
	// This pseudo-terminal has no emulator to answer OSC color queries. Avoid
	// inheriting a host TERM that makes startup wait for those replies.
	p.cmd.Env = append(p.cmd.Env, "TERM=dumb")
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = slave, slave, slave
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	outputDone := make(chan struct{})
	p.outputDone = outputDone
	go func() {
		io.Copy(&p.output, master)
		close(outputDone)
	}()
	p.start(t)
	slave.Close()
	p.waitOutput(t, "1 jobs")
	return p, master
}

func TestIntegratedSchedulerOwnsLock(t *testing.T) {
	cfg := idleSchedulerConfig(t)
	db := filepath.Join(t.TempDir(), "rote.db")
	owner, terminal := startDashboard(t, "-c", cfg, "--db", db)
	assertSchedulerRejected(t, "start", "-c", cfg, "--db", db)
	assertSchedulerRejected(t, "-c", cfg, "--db", db)
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	owner.wait(t, 0)
	next := startHeadless(t, cfg, db)
	next.cmd.Process.Signal(syscall.SIGTERM)
	next.wait(t, 0)
}

func TestReadOnlyDashboardWithScheduler(t *testing.T) {
	cfg := idleSchedulerConfig(t)
	db := filepath.Join(t.TempDir(), "rote.db")
	owner := startHeadless(t, cfg, db)
	viewer, terminal := startDashboard(t, "tui", "-c", cfg, "--db", db)
	assertSchedulerRejected(t, "start", "-c", cfg, "--db", db)
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	viewer.wait(t, 0)
	assertSchedulerRejected(t, "start", "-c", cfg, "--db", db)
	owner.cmd.Process.Signal(syscall.SIGTERM)
	owner.wait(t, 0)
}

func TestIntegratedStartupFailureReleasesLock(t *testing.T) {
	cfg := idleSchedulerConfig(t)
	db := filepath.Join(t.TempDir(), "rote.db")
	p := newCLIProcess(t, "-c", cfg, "--db", db)
	// Detach from the controlling terminal so Bubble Tea initialization fails
	// even when the test suite itself runs from an interactive shell.
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	p.start(t)
	p.wait(t, 1)
	next := startHeadless(t, cfg, db)
	next.cmd.Process.Signal(syscall.SIGTERM)
	next.wait(t, 0)
}

func TestIntegratedTwoStageShutdown(t *testing.T) {
	for _, first := range []string{"q", "ctrl+c", "SIGTERM"} {
		t.Run(first, func(t *testing.T) {
			b := newBlockedCommand(t, false)
			cfg := shutdownConfig(t, b.command, "")
			db := filepath.Join(t.TempDir(), "rote.db")
			p, terminal := startDashboard(t, "-c", cfg, "--db", db)
			b.waitReady(t)
			var err error
			switch first {
			case "q":
				_, err = terminal.Write([]byte("q"))
			case "ctrl+c":
				_, err = terminal.Write([]byte{3})
			default:
				err = p.cmd.Process.Signal(syscall.SIGTERM)
			}
			if err != nil {
				t.Fatal(err)
			}
			p.waitOutput(t, "waiting for running jobs")
			assertLockHeld(t, db)
			code := 130
			if first == "SIGTERM" {
				code = 143
				err = p.cmd.Process.Signal(syscall.SIGTERM)
			} else {
				// The TUI must have restored terminal signal handling, so this
				// actual Ctrl+C now reaches the shutdown handler as SIGINT.
				_, err = terminal.Write([]byte{3})
			}
			if err != nil {
				t.Fatal(err)
			}
			p.wait(t, code)
			run := recordedRun(t, db, "blocking")
			if run.Success || run.TimedOut || run.Err != context.Canceled.Error() {
				t.Fatalf("unexpected integrated shutdown result: %+v", run)
			}
			b.assertTerminated(t)
			assertSchedulerRestarts(t, db)
		})
	}
}

func TestIntegratedSingleSignalRemainsGraceful(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			b := newBlockedCommand(t, false)
			cfg := shutdownConfig(t, b.command, "")
			db := filepath.Join(t.TempDir(), "rote.db")
			p, _ := startDashboard(t, "-c", cfg, "--db", db)
			b.waitReady(t)
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			p.waitOutput(t, "waiting for running jobs")
			assertLockHeld(t, db)
			b.unblock(t)
			p.wait(t, 0)
			if run := recordedRun(t, db, "blocking"); !run.Success || run.Err != "" {
				t.Fatalf("one signal canceled a job: %+v", run)
			}
			assertSchedulerRestarts(t, db)
		})
	}
}

func TestReadOnlyDashboardSignalsLeaveSchedulerRunning(t *testing.T) {
	cfg := idleSchedulerConfig(t)
	db := filepath.Join(t.TempDir(), "rote.db")
	owner := startHeadless(t, cfg, db)
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			viewer, _ := startDashboard(t, "tui", "-c", cfg, "--db", db)
			if err := viewer.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			viewer.wait(t, 0)
			assertLockHeld(t, db)
		})
	}
	if err := owner.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	owner.wait(t, 0)
}
