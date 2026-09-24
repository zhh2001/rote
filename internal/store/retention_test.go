package store

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInsertWithRetentionLimits(t *testing.T) {
	ctx := context.Background()
	for _, keep := range []int{0, 1, 3, 20} {
		t.Run(fmt.Sprint(keep), func(t *testing.T) {
			s := openTemp(t)
			const job = "bounded' OR 1=1 --"
			for i := 0; i < 5; i++ {
				for _, name := range []string{job, "untouched"} {
					if _, err := s.Insert(ctx, makeRun(name, base.Add(time.Duration(i)*time.Minute))); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := makeRun(job, base.Add(time.Hour))
			want.Success, want.TimedOut, want.ExitCode = false, true, -1
			want.Err, want.StdoutTruncated, want.StderrTruncated = "diagnostic", true, true
			want.Stdout, want.Stderr = []byte{0, 255, 1}, []byte{254, 0, 3}
			id, err := s.InsertWithRetention(ctx, want, keep)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := s.RecentRuns(ctx, job, 0)
			count := 6
			if keep > 0 && keep < count {
				count = keep
			}
			if err != nil || len(runs) != count {
				t.Fatalf("retained %d runs, want %d: %v", len(runs), count, err)
			}
			if runs[0].ID != id || !equalRun(runs[0], want) {
				t.Fatalf("retention changed the newest result: %+v", runs[0])
			}
			for i := 1; i < count; i++ {
				if !runs[i].StartedAt.Equal(base.Add(time.Duration(5-i) * time.Minute)) {
					t.Errorf("wrong survivor at %d: %+v", i, runs[i])
				}
			}
			other, err := s.RecentRuns(ctx, "untouched", 0)
			if err != nil || len(other) != 5 {
				t.Fatalf("other job changed: len=%d err=%v", len(other), err)
			}
		})
	}
}

func TestRetentionOrderingAndOutputDeletion(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	var ids []int64
	for _, minute := range []int{4, 1, 2, 4, 3} {
		id, err := s.Insert(ctx, makeRun("job", base.Add(time.Duration(minute)*time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// An older run can finish later; retention follows start time, not insertion
	// order. Ties use the larger ID, exactly as the history queries do.
	lateID, err := s.InsertWithRetention(ctx, makeRun("job", base.Add(time.Minute)), 3)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.RecentRuns(ctx, "job", 0)
	if err != nil || len(runs) != 3 {
		t.Fatalf("unexpected retained history: len=%d err=%v", len(runs), err)
	}
	for i, want := range []int64{ids[3], ids[0], ids[4]} {
		if runs[i].ID != want {
			t.Errorf("survivor %d ID=%d, want %d", i, runs[i].ID, want)
		}
	}
	for _, id := range []int64{ids[1], ids[2], lateID} {
		if _, ok, err := s.RunOutput(ctx, id); err != nil || ok {
			t.Errorf("pruned output %d still available: ok=%v err=%v", id, ok, err)
		}
	}
	meta, err := s.LatestMetaPerJob(ctx)
	if err != nil || meta["job"].ID != ids[3] {
		t.Fatalf("latest metadata changed: %+v err=%v", meta, err)
	}
}

func TestRetentionLimitChangesPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, keep := range []int{0, 0, 0, 2, 1, 0, 0, 5} {
		if _, err := s.InsertWithRetention(context.Background(), makeRun("job", base.Add(time.Duration(i)*time.Minute)), keep); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	runs, err := reader.RecentRuns(context.Background(), "job", 0)
	// Reducing the limit removed starts 0..3. Disabling or raising the limit
	// permits growth again, but never restores those deleted rows.
	if err != nil || len(runs) != 4 {
		t.Fatalf("unexpected history after reopen: len=%d err=%v", len(runs), err)
	}
	for i, run := range runs {
		if !run.StartedAt.Equal(base.Add(time.Duration(7-i) * time.Minute)) {
			t.Errorf("unexpected survivor after limit change: %+v", run)
		}
	}
}

func TestRetentionFailurePreservesExistingHistory(t *testing.T) {
	for _, kind := range []string{"negative", "canceled", "closed", "insert", "prune", "commit"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rote.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			for i := 0; i < 3; i++ {
				if _, err := s.Insert(ctx, makeRun("job", base.Add(time.Duration(i)*time.Minute))); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.RecentRuns(ctx, "job", 0)
			if err != nil {
				t.Fatal(err)
			}
			keep := 1
			var setup []string
			switch kind {
			case "negative":
				keep = -1
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			case "insert":
				setup = []string{"CREATE TRIGGER fail_insert BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT, 'blocked insert'); END"}
			case "prune":
				setup = []string{"CREATE TRIGGER fail_delete BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT, 'blocked prune'); END"}
			case "commit":
				setup = []string{
					"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
					"CREATE TABLE child (id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)",
					"CREATE TRIGGER fail_commit AFTER INSERT ON runs BEGIN INSERT INTO child VALUES (-1); END",
				}
			}
			for _, statement := range setup {
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			id, err := s.InsertWithRetention(ctx, makeRun("job", base.Add(time.Hour)), keep)
			if err == nil || id != 0 {
				t.Fatalf("failed transaction returned id=%d err=%v", id, err)
			}
			if kind == "commit" && !strings.Contains(err.Error(), "commit retention transaction") {
				t.Fatalf("did not reach commit failure: %v", err)
			}
			// Re-open independently to verify durable rollback, not just an
			// uncommitted snapshot from the writing connection.
			reader, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			after, err := reader.RecentRuns(context.Background(), "job", 0)
			if err != nil || len(after) != len(before) {
				t.Fatalf("history changed on error: len=%d err=%v", len(after), err)
			}
			for i := range before {
				if before[i].ID != after[i].ID || !equalRun(before[i], after[i]) {
					t.Errorf("row %d changed on failure", i)
				}
			}
			if kind == "prune" {
				// Disabled retention must not issue DELETE at all.
				if _, err := reader.InsertWithRetention(context.Background(), makeRun("job", base.Add(time.Hour)), 0); err != nil {
					t.Fatalf("disabled retention attempted cleanup: %v", err)
				}
			}
		})
	}
}

func TestConcurrentRetentionAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	var stores []*Store
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores = append(stores, s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const n, keep = 20, 4
	done, readerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			var count int
			if err := stores[0].db.QueryRowContext(ctx, "SELECT count(*) FROM runs WHERE job_name='concurrent'").Scan(&count); err != nil {
				t.Errorf("concurrent read: %v", err)
				return
			}
			if count > keep {
				t.Errorf("reader observed partial retention transaction: %d rows", count)
				return
			}
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := stores[i%len(stores)].InsertWithRetention(ctx, makeRun("concurrent", base.Add(time.Duration(i)*time.Minute)), keep)
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(done)
	<-readerDone
	runs, err := stores[0].RecentRuns(ctx, "concurrent", 0)
	if err != nil || len(runs) != keep {
		t.Fatalf("retained %d rows, want %d: %v", len(runs), keep, err)
	}
	for i, run := range runs {
		if !run.StartedAt.Equal(base.Add(time.Duration(n-1-i) * time.Minute)) {
			t.Errorf("wrong concurrent survivor: %+v", run)
		}
	}
}

func TestRetentionReusesFreedPages(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	run := makeRun("output", base)
	run.Stdout, run.Stderr = bytes.Repeat([]byte{'x'}, 32<<10), bytes.Repeat([]byte{'y'}, 32<<10)
	for i := 0; i < 20; i++ {
		if _, err := s.Insert(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	var before, freed, after int
	if _, err := s.InsertWithRetention(ctx, run, 3); err != nil {
		t.Fatal(err)
	}
	// The first transaction inserts before deleting and may need one extra
	// record's pages. Subsequent writes should reuse that retained allocation.
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA freelist_count").Scan(&freed); err != nil || freed == 0 {
		t.Fatalf("cleanup did not free database pages: pages=%d err=%v", freed, err)
	}
	for i := 0; i < 20; i++ {
		if _, err := s.InsertWithRetention(ctx, run, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&after); err != nil || after > before {
		t.Fatalf("later writes did not reuse free pages: before=%d after=%d err=%v", before, after, err)
	}
}
