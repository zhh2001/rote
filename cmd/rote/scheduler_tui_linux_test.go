package main

import (
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
