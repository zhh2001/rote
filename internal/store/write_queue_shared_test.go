//go:build darwin || linux

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeQueueUsers(gate chan struct{}) int {
	writeQueuesMu.Lock()
	defer writeQueuesMu.Unlock()
	for _, queue := range writeQueues {
		if queue.gate == gate {
			return queue.users
		}
	}
	return 0
}

func openSharedStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWriteQueueSharedPathAliasesAndLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	first := openSharedStore(t, "real/rote.db")
	if err := os.Symlink("real/rote.db", "alias.db"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", "alias-dir"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "real", "rote.db"),
		"real/rote.db", "./real/../real/rote.db", "alias.db", "alias-dir/rote.db", "file:alias.db",
	} {
		s := openSharedStore(t, path)
		if s.writes != first.writes || writeQueueUsers(first.writes) != 2 {
			t.Fatalf("%q did not join the same database queue", path)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		// Closing a handle twice must not release another handle's reference.
		if err := s.Close(); err != nil || writeQueueUsers(first.writes) != 1 {
			t.Fatalf("%q broke queue reference counting: %v", path, err)
		}
	}
	second := openSharedStore(t, "alias.db")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Insert(context.Background(), makeRun("closed", base)); !errors.Is(err, errStoreClosed) {
		t.Fatalf("closed handle reused a live shared queue: %v", err)
	}
	third := openSharedStore(t, "file:alias.db")
	if third.writes != second.writes || writeQueueUsers(third.writes) != 2 {
		t.Fatal("closing one Store split the shared queue")
	}
	if _, err := second.Insert(context.Background(), makeRun("still-open", base)); err != nil {
		t.Fatal(err)
	}
	second.Close()
	third.Close()
	if writeQueueUsers(first.writes) != 0 {
		t.Fatal("queue registry retained closed Stores")
	}
	reopened := openSharedStore(t, "real/rote.db")
	if reopened.writes == first.writes {
		t.Fatal("last Close did not remove the old queue")
	}
	if _, ok, err := reopened.LastRunMeta(context.Background(), "still-open"); err != nil || !ok {
		t.Fatalf("reopening lost committed history: ok=%v err=%v", ok, err)
	}
}

func TestWriteQueueSharedCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	first, second := openSharedStore(t, path), openSharedStore(t, path)
	if err := first.lockWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer first.unlockWrite()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.InsertWithRetention(ctx, makeRun("queued", base), 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second handle bypassed the first handle's queue: %v", err)
	}
	readCtx, stopRead := context.WithTimeout(context.Background(), time.Second)
	defer stopRead()
	if _, ok, err := second.LastRunMeta(readCtx, "queued"); err != nil || ok {
		t.Fatalf("reader joined the queue or canceled write executed: ok=%v err=%v", ok, err)
	}
}

func TestWriteQueueOpenSerializesMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	first := openSharedStore(t, path)
	if err := first.lockWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Release even if the test fails before Open has reached the queue.
	held := true
	defer func() {
		if held {
			first.unlockWrite()
		}
	}()
	opened := make(chan *Store, 1)
	openErr := make(chan error, 1)
	go func() {
		s, err := Open(path)
		opened <- s
		openErr <- err
	}()
	// Always close the new Store, including on a failing assertion.
	t.Cleanup(func() {
		if s := <-opened; s != nil {
			s.Close()
		}
	})
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for writeQueueUsers(first.writes) != 2 {
		select {
		case err := <-openErr:
			t.Fatalf("Open completed without joining the queue: %v", err)
		case <-deadline.C:
			t.Fatal("Open did not join the shared queue")
		case <-tick.C:
		}
	}
	select {
	case err := <-openErr:
		t.Fatalf("schema writes bypassed the queue: %v", err)
	default:
	}
	first.unlockWrite()
	held = false
	select {
	case err := <-openErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open did not complete after the queue was released")
	}
}

func TestWriteQueueMigrationFailureReleasesReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	s := openSharedStore(t, path)
	if _, err := s.db.Exec(`DROP TABLE runs; CREATE TABLE runs(wrong INTEGER)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	key, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		failed, err := Open(path)
		if failed != nil {
			failed.Close()
		}
		if err == nil {
			t.Fatal("migration unexpectedly succeeded")
		}
		writeQueuesMu.Lock()
		_, leaked := writeQueues[key]
		writeQueuesMu.Unlock()
		if leaked {
			t.Fatal("failed Open leaked a writer queue reference")
		}
	}
}

func TestWriteQueuePrivateMemoryDatabases(t *testing.T) {
	first, second := openSharedStore(t, ":memory:"), openSharedStore(t, ":memory:")
	if first.writes == second.writes {
		t.Fatal("private memory databases must have independent queues")
	}
	if _, err := first.Insert(context.Background(), makeRun("private", base)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := second.LastRunMeta(context.Background(), "private"); err != nil || ok {
		t.Fatalf("memory databases were not isolated: ok=%v err=%v", ok, err)
	}
}

func TestWriteQueueConcurrentOpenClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	anchor := openSharedStore(t, path)
	const workers, rounds = 16, 3
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for round := 0; round < rounds; round++ {
				s, err := Open(path)
				if err != nil {
					errs <- err
					return
				}
				_, err = s.Insert(ctx, makeRun("churn", base.Add(time.Duration(i*rounds+round)*time.Second)))
				closeErr := s.Close()
				if err = errors.Join(err, closeErr); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := writeQueueUsers(anchor.writes); got != 1 {
		t.Errorf("concurrent Open/Close leaked or removed references: users=%d", got)
	}
	rows, err := anchor.RecentRunsMeta(ctx, "churn", 0)
	if err != nil || len(rows) != workers*rounds {
		t.Fatalf("lost concurrent writes: count=%d err=%v", len(rows), err)
	}
	anchor.Close()
	if writeQueueUsers(anchor.writes) != 0 {
		t.Fatal("last Close leaked the shared queue")
	}
}
