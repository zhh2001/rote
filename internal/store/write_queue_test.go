package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Hold a real SQLite writer lock independently of the public write methods.
// This represents another Store/process, and lets a public writer stay active
// while other callers queue behind it.
func holdSQLiteWriter(t *testing.T, s *Store) func() {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE runs SET err = 'uncommitted'`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { tx.Rollback() }) }
	t.Cleanup(release)
	return release
}

func waitForConnections(t *testing.T, s *Store, count int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for s.db.Stats().InUse < count {
		select {
		case <-deadline.C:
			t.Fatalf("writer did not enter SQLite: %+v", s.db.Stats())
		case <-tick.C:
		}
	}
}

func queuedWriteOperations() map[string]func(context.Context, *Store) error {
	return map[string]func(context.Context, *Store) error{
		"insert": func(ctx context.Context, s *Store) error {
			_, err := s.Insert(ctx, makeRun("canceled", base))
			return err
		},
		"retention-disabled": func(ctx context.Context, s *Store) error {
			_, err := s.InsertWithRetention(ctx, makeRun("canceled", base), 0)
			return err
		},
		"retention": func(ctx context.Context, s *Store) error {
			_, err := s.InsertWithRetention(ctx, makeRun("seed", base.Add(time.Hour)), 1)
			return err
		},
		"prune": func(ctx context.Context, s *Store) error { return s.Prune(ctx, "seed", 0) },
	}
}

func TestQueuedWriteCancellation(t *testing.T) {
	for name, operation := range queuedWriteOperations() {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			canceled, cancelNow := context.WithCancel(context.Background())
			cancelNow()
			if err := operation(canceled, s); !errors.Is(err, context.Canceled) {
				t.Fatalf("already canceled write: %v", err)
			}
			if _, err := s.Insert(context.Background(), makeRun("seed", base)); err != nil {
				t.Fatal(err)
			}
			release := holdSQLiteWriter(t, s)
			first := make(chan error, 1)
			go func() {
				_, err := s.Insert(context.Background(), makeRun("first", base))
				first <- err
			}()
			defer func() {
				release()
				if err := <-first; err != nil {
					t.Errorf("first writer: %v", err)
				}
			}()
			waitForConnections(t, s, 2)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := operation(ctx, s)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("queued write returned %v, want context deadline", err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("queued cancellation took %v; should not wait for SQLite's 5s busy timeout", elapsed)
			}
			// Reads must neither join the writer queue nor see uncommitted data.
			readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
			defer readCancel()
			rows, err := s.RecentRuns(readCtx, "seed", 0)
			if err != nil || len(rows) != 1 || rows[0].Err != "" {
				t.Fatalf("read blocked or saw uncommitted data: %+v, %v", rows, err)
			}
			release()
			// A canceled insert or prune must not execute later after release.
			if _, err := s.Insert(context.Background(), makeRun("after", base)); err != nil {
				t.Fatal(err)
			}
			rows, err = s.RecentRuns(context.Background(), "seed", 0)
			if err != nil || len(rows) != 1 || !rows[0].StartedAt.Equal(base) {
				t.Errorf("canceled retention/prune changed history: %+v, %v", rows, err)
			}
			if _, ok, err := s.LastRunMeta(context.Background(), "canceled"); err != nil || ok {
				t.Errorf("canceled run was inserted: ok=%v err=%v", ok, err)
			}
		})
	}
}

// Done is evaluated when lockWrite reaches its blocking select, after the
// initial closed check. This barrier ensures the Close test has real waiters.
type queuedContext struct {
	context.Context
	ready chan struct{}
	once  sync.Once
}

func (c *queuedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.ready) })
	return c.Context.Done()
}

func TestWriteQueueClose(t *testing.T) {
	s := openTemp(t)
	if err := s.lockWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(s.unlockWrite) }
	t.Cleanup(release)
	results := make(chan error, 4)
	var waiters []<-chan struct{}
	for _, operation := range queuedWriteOperations() {
		ctx := &queuedContext{Context: context.Background(), ready: make(chan struct{})}
		waiters = append(waiters, ctx.ready)
		go func() { results <- operation(ctx, s) }()
	}
	for _, ready := range waiters {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("write did not reach the local queue")
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not reject pending writes")
	}
	for i := 0; i < 4; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, errStoreClosed) {
				t.Errorf("queued write after Close: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("queued write did not wake up on Close")
		}
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the admitted write finished: %v", err)
	default:
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after the writer exited")
	}
	for name, operation := range queuedWriteOperations() {
		if err := operation(context.Background(), s); !errors.Is(err, errStoreClosed) {
			t.Errorf("%s accepted after Close: %v", name, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestWriteQueueReleasedAfterFailure(t *testing.T) {
	for _, failure := range []string{"insert", "prune", "commit"} {
		t.Run(failure, func(t *testing.T) {
			s := openTemp(t)
			ctx := context.Background()
			if _, err := s.Insert(ctx, makeRun("seed", base)); err != nil {
				t.Fatal(err)
			}
			var statements []string
			switch failure {
			case "insert":
				statements = []string{`CREATE TRIGGER fail BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT, 'insertion failed'); END`}
			case "prune":
				statements = []string{`CREATE TRIGGER fail BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT, 'pruning failed'); END`}
			case "commit":
				statements = []string{
					`CREATE TABLE parent(id INTEGER PRIMARY KEY)`,
					`CREATE TABLE child(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`,
					`CREATE TRIGGER fail AFTER INSERT ON runs BEGIN INSERT INTO child VALUES(-1); END`,
				}
			}
			for _, statement := range statements {
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.InsertWithRetention(ctx, makeRun("seed", base.Add(time.Minute)), 1); err == nil {
				t.Fatal("retention failure was not injected")
			}
			if failure == "insert" {
				if _, err := s.Insert(ctx, makeRun("seed", base)); err == nil {
					t.Fatal("plain insert did not fail")
				}
			} else if failure == "prune" {
				if err := s.Prune(ctx, "seed", 0); err == nil {
					t.Fatal("plain prune did not fail")
				}
			}
			if _, err := s.db.Exec(`DROP TRIGGER fail`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			id, err := s.InsertWithRetention(ctx, makeRun("seed", base.Add(time.Hour)), 1)
			if err != nil {
				t.Fatalf("write queue or transaction leaked after %s failure: %v", failure, err)
			}
			rows, err := s.RecentRuns(ctx, "seed", 0)
			if err != nil || len(rows) != 1 || rows[0].ID != id {
				t.Fatalf("wrong committed history after recovery: %+v, %v", rows, err)
			}
		})
	}
}

func TestWriteQueueStressAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	const handles, writers, keep = 4, 80, 5
	stores := make([]*Store, handles)
	for i := range stores {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		stores[i] = s
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start, done := make(chan struct{}), make(chan struct{})
	errs := make(chan error, writers+1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		<-start
		for {
			rows, err := stores[0].RecentRunsMeta(ctx, "bounded", 0)
			if err != nil || len(rows) > keep {
				errs <- fmt.Errorf("reader saw partial retention: count=%d err=%v", len(rows), err)
				return
			}
			select {
			case <-done:
				return
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s := stores[i%handles]
			r := makeRun("bounded", base.Add(time.Duration(i)*time.Second))
			r.Stdout = make([]byte, 8<<10)
			if _, err := s.InsertWithRetention(ctx, r, keep); err != nil {
				errs <- err
				return
			}
			r.JobName = "unbounded"
			if _, err := s.Insert(ctx, r); err != nil {
				errs <- err
				return
			}
			if err := s.Prune(ctx, "bounded", keep); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(done)
	<-readerDone
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for job, count := range map[string]int{"bounded": keep, "unbounded": writers} {
		rows, err := stores[0].RecentRuns(ctx, job, 0)
		if err != nil || len(rows) != count {
			t.Fatalf("%s: count=%d want=%d err=%v", job, len(rows), count, err)
		}
		for i, row := range rows {
			if !row.StartedAt.Equal(base.Add(time.Duration(writers-1-i) * time.Second)) {
				t.Errorf("%s has missing/duplicate/wrongly retained run: %+v", job, row)
			}
		}
	}
	var integrity string
	if err := stores[0].db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Errorf("integrity_check=%q err=%v", integrity, err)
	}
}

func TestWriteQueueExternalBusyIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	holder, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { holder.Close() })
	if _, err := s.Insert(context.Background(), makeRun("seed", base)); err != nil {
		t.Fatal(err)
	}
	// Shorten only this fixture's busy timeout to avoid spending five seconds
	// per failure. The production DSN and all stress tests keep the 5s default.
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.Exec(`PRAGMA busy_timeout = 30`); err != nil {
		t.Fatal(err)
	}
	release := holdSQLiteWriter(t, holder)
	for name, operation := range queuedWriteOperations() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := operation(ctx, s)
		cancel()
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
			t.Errorf("%s external writer conflict was not reported as SQLITE_BUSY: %v", name, err)
		}
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id, err := s.InsertWithRetention(ctx, makeRun("seed", base.Add(time.Hour)), 1)
	if err != nil {
		t.Fatalf("write queue or transaction stuck after external lock released: %v", err)
	}
	rows, err := s.RecentRuns(ctx, "seed", 0)
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Errorf("history changed by failed writes: %+v, %v", rows, err)
	}
	if _, ok, err := s.LastRunMeta(ctx, "canceled"); err != nil || ok {
		t.Errorf("failed insert was retried later: ok=%v err=%v", ok, err)
	}
}

func TestWriteQueueIndependentStores(t *testing.T) {
	first, second := openTemp(t), openTemp(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.lockWrite(ctx); err != nil {
		t.Fatal(err)
	}
	defer first.unlockWrite()
	if _, err := second.Insert(ctx, makeRun("independent", base)); err != nil {
		t.Fatalf("unrelated database blocked by another Store's queue: %v", err)
	}
}

// Cause a real cancellation/Close exactly between admission and its recheck,
// without relying on a scheduler race to exercise the post-admission guards.
type admissionContext struct {
	context.Context
	checks    atomic.Int32
	onRecheck func()
}

func (c *admissionContext) Err() error {
	if c.checks.Add(1) == 2 {
		c.onRecheck()
	}
	return c.Context.Err()
}

func TestLockWriteRechecksAdmission(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		s := openTemp(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := s.lockWrite(&admissionContext{Context: ctx, onRecheck: cancel})
		if err == nil {
			s.unlockWrite()
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled context admitted: %v", err)
		}
		if len(s.writes) != 0 {
			t.Fatal("canceled admission leaked the write token")
		}
	})
	t.Run("close", func(t *testing.T) {
		s := openTemp(t)
		done := make(chan error, 1)
		ctx := &admissionContext{Context: context.Background(), onRecheck: func() {
			go func() { done <- s.Close() }()
			<-s.closed
		}}
		err := s.lockWrite(ctx)
		if err == nil {
			s.unlockWrite()
		}
		if !errors.Is(err, errStoreClosed) {
			t.Errorf("closing Store admitted a new writer: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("rejected admission leaked the token, blocking Close")
		}
	})
}
