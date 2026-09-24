//go:build darwin || linux

package store

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustOpenScheduler(t *testing.T, path string) *Store {
	t.Helper()
	s, err := OpenScheduler(path)
	if err != nil {
		t.Fatalf("OpenScheduler: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func assertSchedulerLocked(t *testing.T, path string) {
	t.Helper()
	s, err := OpenScheduler(path)
	if s != nil {
		s.Close()
	}
	if !errors.Is(err, ErrSchedulerRunning) {
		t.Fatalf("OpenScheduler(%q) = %v, want ErrSchedulerRunning", path, err)
	}
	if !strings.Contains(err.Error(), "rote tui") {
		t.Errorf("lock error lacks dashboard hint: %v", err)
	}
}

func TestSchedulerLockLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "rote.db")
	first := mustOpenScheduler(t, path)
	assertSchedulerLocked(t, path)
	// Failed acquisition must not release the original owner's lock.
	assertSchedulerLocked(t, path)
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("lock permissions = %o, want no group/other access", info.Mode().Perm())
	}

	// Query tools and manual runs may still use the database concurrently.
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	if _, err := reader.Insert(context.Background(), makeRun("manual", base)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := first.LastRun(context.Background(), "manual"); err != nil || !ok {
		t.Fatalf("concurrent read: ok=%v err=%v", ok, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	assertSchedulerLocked(t, path)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := mustOpenScheduler(t, path)
	after, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(info, after) {
		t.Fatalf("lock file must remain the same inode across owners: %v", err)
	}
	// Closing the old owner again must be harmless to its successor.
	if err := first.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	assertSchedulerLocked(t, path)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerCloseWaitsForAdmittedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	s := mustOpenScheduler(t, path)
	if _, err := s.Insert(context.Background(), makeRun("seed", base)); err != nil {
		t.Fatal(err)
	}
	release := holdSQLiteWriter(t, s)
	writeDone := make(chan struct{})
	var writeErr error
	go func() {
		defer close(writeDone)
		_, writeErr = s.InsertWithRetention(context.Background(), makeRun("seed", base.Add(time.Hour)), 1)
	}()
	defer func() { release(); <-writeDone }()
	waitForConnections(t, s, 2)
	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	// Closing the queue must not release scheduler ownership while its
	// previously admitted transaction is still trying to persist a result.
	assertSchedulerLocked(t, path)
	if err := s.Prune(context.Background(), "seed", 0); !errors.Is(err, errStoreClosed) {
		t.Fatalf("write accepted while closing: %v", err)
	}
	release()
	<-writeDone
	if writeErr != nil {
		t.Fatalf("admitted transaction lost during Close: %v", writeErr)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not complete")
	}
	next := mustOpenScheduler(t, path)
	rows, err := next.RecentRuns(context.Background(), "seed", 0)
	if err != nil || len(rows) != 1 || !rows[0].StartedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("admitted retention transaction was not committed: %+v, %v", rows, err)
	}
}

func TestSchedulerLockPathAliases(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "real", "rote.db")
	mustOpenScheduler(t, path)
	if err := os.Symlink("real/rote.db", "alias.db"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", "alias-dir"); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{
		path,
		"real/rote.db",
		"./real/../real/rote.db",
		"alias.db",
		"alias-dir/rote.db",
		"file:alias.db",
	} {
		t.Run(alias, func(t *testing.T) { assertSchedulerLocked(t, alias) })
	}
}

func TestSchedulerLockIndependentDatabases(t *testing.T) {
	dir := t.TempDir()
	mustOpenScheduler(t, filepath.Join(dir, "first.db"))
	mustOpenScheduler(t, filepath.Join(dir, "second.db"))
}

func TestSchedulerLockExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mustOpenScheduler(t, path)
}

func TestSchedulerLockOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenScheduler(path); err == nil {
		s.Close()
		t.Fatal("scheduler started despite an unusable lock path")
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	mustOpenScheduler(t, path)
}

func TestSchedulerLockMigrationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	s := mustOpenScheduler(t, path)
	// Make subsequent migration fail after lock acquisition.
	if _, err := s.db.Exec(`DROP TABLE runs; CREATE TABLE runs (wrong INTEGER)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		reopened, err := OpenScheduler(path)
		if reopened != nil {
			reopened.Close()
		}
		if err == nil || errors.Is(err, ErrSchedulerRunning) || !strings.Contains(err.Error(), "apply schema") {
			t.Fatalf("attempt %d: expected schema error with lock released, got %v", i, err)
		}
	}
}

func TestSchedulerLockRequiresFile(t *testing.T) {
	s, err := OpenScheduler(":memory:")
	if s != nil {
		s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "file-backed") {
		t.Fatalf("in-memory scheduler error = %v", err)
	}
}

func TestSchedulerLockConcurrentAcquisition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	const count = 12
	stores := make([]*Store, count)
	errs := make([]error, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stores[i], errs[i] = OpenScheduler(path)
		}()
	}
	close(start)
	wg.Wait()
	winners := 0
	for i, s := range stores {
		if s != nil {
			winners++
			s.Close()
		} else if !errors.Is(errs[i], ErrSchedulerRunning) {
			t.Errorf("contender %d: %v", i, errs[i])
		}
	}
	if winners != 1 {
		t.Fatalf("%d schedulers acquired the same database, want 1", winners)
	}
	mustOpenScheduler(t, path)
}

func TestSchedulerLockNotInheritedByCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	s := mustOpenScheduler(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "echo ready; cat >/dev/null")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child startup: %q, %v", line, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The child is still alive with stdin open, but must not retain the lock.
	mustOpenScheduler(t, path)
}
