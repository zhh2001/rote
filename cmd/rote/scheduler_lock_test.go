//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/store"
)

// Run the real command dispatcher in an independent process, including its
// signal handlers and deferred store/lock cleanup.
func TestSchedulerProcess(t *testing.T) {
	if os.Getenv("ROTE_TEST_SCHEDULER_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(dispatch(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

type processOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *processOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *processOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

type cliProcess struct {
	cmd        *exec.Cmd
	output     processOutput
	done       chan struct{}
	outputDone <-chan struct{}
	err        error // read only after done closes
}

func newCLIProcess(t *testing.T, args ...string) *cliProcess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestSchedulerProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "ROTE_TEST_SCHEDULER_PROCESS=1")
	p := &cliProcess{cmd: cmd, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = &p.output, &p.output
	return p
}

func (p *cliProcess) start(t *testing.T) {
	t.Helper()
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		p.err = p.cmd.Wait()
		if p.outputDone != nil {
			<-p.outputDone
		}
		close(p.done)
	}()
	t.Cleanup(func() {
		p.cmd.Process.Kill()
		<-p.done
	})
}

func (p *cliProcess) waitOutput(t *testing.T, text string) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if strings.Contains(p.output.String(), text) {
			return
		}
		select {
		case <-tick.C:
		case <-p.done:
			t.Fatalf("process exited before %q: %v\n%s", text, p.err, p.output.String())
		case <-deadline.C:
			t.Fatalf("process did not print %q\n%s", text, p.output.String())
		}
	}
}

func (p *cliProcess) wait(t *testing.T, code int) {
	t.Helper()
	select {
	case <-p.done:
		// Go only runs racefini on os.Exit(0). A forced shutdown's expected
		// nonzero exit can otherwise conceal a race reported by the child.
		if strings.Contains(p.output.String(), "WARNING: DATA RACE") {
			t.Fatalf("child process reported a data race:\n%s", p.output.String())
		}
		if got := p.cmd.ProcessState.ExitCode(); got != code {
			t.Fatalf("exit code = %d, want %d: %v\n%s", got, code, p.err, p.output.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("process did not exit\n%s", p.output.String())
	}
}

func startHeadless(t *testing.T, cfg, db string) *cliProcess {
	t.Helper()
	p := newCLIProcess(t, "start", "-c", cfg, "--db", db)
	p.start(t)
	p.waitOutput(t, "starting scheduler")
	return p
}

func idleSchedulerConfig(t *testing.T) string {
	t.Helper()
	return writeConfig(t, "[[job]]\nname='idle'\nschedule='every 24h'\ncommand='echo manual-output'\n")
}

func assertSchedulerRejected(t *testing.T, args ...string) {
	t.Helper()
	p := newCLIProcess(t, args...)
	p.start(t)
	p.wait(t, 1)
	for _, text := range []string{"scheduler is already running", "rote tui"} {
		if !strings.Contains(p.output.String(), text) {
			t.Errorf("missing %q in error: %s", text, p.output.String())
		}
	}
}

func TestSchedulerRejectsSecondProcess(t *testing.T) {
	cfg := idleSchedulerConfig(t)
	db := filepath.Join(t.TempDir(), "rote.db")
	owner := startHeadless(t, cfg, db)
	assertSchedulerRejected(t, "start", "-c", cfg, "--db", db)
	assertSchedulerRejected(t, "-c", cfg, "--db", db) // integrated mode

	for _, args := range [][]string{
		{"list", "-c", cfg, "--db", db},
		{"run", "-c", cfg, "--db", db, "idle"},
		{"logs", "--db", db, "-o", "idle"},
	} {
		code, out, stderr := runDispatch(args)
		if code != 0 {
			t.Fatalf("%v: exit=%d, stderr=%s", args, code, stderr)
		}
		if args[0] == "logs" && !strings.Contains(out, "manual-output") {
			t.Errorf("manual run not recorded: %s", out)
		}
	}
	// Ordinary database users must not have released the scheduler's lock.
	assertSchedulerRejected(t, "start", "-c", cfg, "--db", db)
	if err := owner.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	owner.wait(t, 0)
}

func TestSchedulerProcessRestart(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			cfg := idleSchedulerConfig(t)
			db := filepath.Join(t.TempDir(), "rote.db")
			owner := startHeadless(t, cfg, db)
			if err := owner.cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			code := 0
			if signal == syscall.SIGKILL {
				code = -1
			}
			owner.wait(t, code)
			next := startHeadless(t, cfg, db)
			next.cmd.Process.Signal(syscall.SIGTERM)
			next.wait(t, 0)
		})
	}
}

func TestSchedulerProcessesDifferentDatabases(t *testing.T) {
	cfg := idleSchedulerConfig(t)
	dir := t.TempDir()
	first := startHeadless(t, cfg, filepath.Join(dir, "first.db"))
	second := startHeadless(t, cfg, filepath.Join(dir, "second.db"))
	for _, p := range []*cliProcess{first, second} {
		p.cmd.Process.Signal(syscall.SIGTERM)
		p.wait(t, 0)
	}
}

func TestSchedulerConcurrentProcesses(t *testing.T) {
	cfg := idleSchedulerConfig(t)
	db := filepath.Join(t.TempDir(), "new.db")
	const count = 6
	processes := make([]*cliProcess, count)
	for i := range processes {
		processes[i] = newCLIProcess(t, "start", "-c", cfg, "--db", db)
		processes[i].start(t)
	}
	winners := 0
	for _, p := range processes {
		// The winner holds its lock until every contender has either started or
		// been rejected. In particular, all processes race on a brand-new DB.
		tick := time.NewTicker(10 * time.Millisecond)
		deadline := time.NewTimer(10 * time.Second)
	check:
		for {
			if strings.Contains(p.output.String(), "starting scheduler") {
				winners++
				break
			}
			select {
			case <-p.done:
				p.wait(t, 1)
				if !strings.Contains(p.output.String(), "scheduler is already running") {
					t.Errorf("unexpected startup error: %s", p.output.String())
				}
				break check
			case <-tick.C:
			case <-deadline.C:
				t.Fatal("concurrent startup did not finish")
			}
		}
		tick.Stop()
		deadline.Stop()
	}
	if winners != 1 {
		t.Fatalf("started %d schedulers, want exactly 1", winners)
	}
	for _, p := range processes {
		if strings.Contains(p.output.String(), "starting scheduler") {
			p.cmd.Process.Signal(syscall.SIGTERM)
			p.wait(t, 0)
		}
	}
}

func TestSchedulerLockHeldDuringShutdown(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	release := filepath.Join(dir, "finish")
	command := fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.02; done; echo finished", marker, release)
	cfg := writeConfig(t, fmt.Sprintf("[[job]]\nname='inflight'\nschedule='every 1s'\ncommand='%s'\ntimeout='10s'\n", command))
	db := filepath.Join(dir, "rote.db")
	owner := startHeadless(t, cfg, db)
	t.Cleanup(func() { os.WriteFile(release, nil, 0o600) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduled command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := owner.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	assertSchedulerRejected(t, "start", "-c", cfg, "--db", db)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	owner.wait(t, 0)
	st, err := store.OpenScheduler(db)
	if err != nil {
		t.Fatalf("lock not released after drain: %v", err)
	}
	defer st.Close()
	run, ok, err := st.LastRun(context.Background(), "inflight")
	if err != nil || !ok || !run.Success || string(run.Stdout) != "finished\n" {
		t.Fatalf("in-flight run not persisted: ok=%v err=%v run=%+v", ok, err, run)
	}
}
