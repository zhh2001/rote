package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/store"
)

func seedHistory(t *testing.T, st *store.Store, job string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if _, err := st.Insert(context.Background(), store.Run{
			JobName: job, StartedAt: time.Now().Add(-time.Hour + time.Duration(i)*time.Second),
			Success: true, Stdout: []byte("old-output"),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCmdRunHistoryRetention(t *testing.T) {
	for _, limit := range []int{0, 2} {
		for _, outcome := range []string{"success", "failure", "timeout", "canceled"} {
			t.Run(fmt.Sprintf("%d/%s", limit, outcome), func(t *testing.T) {
				st := openStore(t)
				seedHistory(t, st, "bounded", 5)
				seedHistory(t, st, "other", 3)
				job := config.Job{Name: "bounded", Command: "echo new-output", HistoryLimit: limit}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				wantCode := 0
				switch outcome {
				case "failure":
					job.Command, wantCode = "echo new-output; exit 3", 3
				case "timeout":
					job.Command, job.Timeout, wantCode = "sleep 10", 100*time.Millisecond, 124
				case "canceled":
					cancel()
					wantCode = 126
				}
				var out bytes.Buffer
				code, err := cmdRun(ctx, &out, []config.Job{job}, st, job.Name)
				if err != nil || code != wantCode {
					t.Fatalf("exit=%d err=%v, want %d", code, err, wantCode)
				}
				runs, err := st.RecentRuns(context.Background(), "bounded", 0)
				wantCount := 6
				if limit > 0 {
					wantCount = limit
				}
				if err != nil || len(runs) != wantCount {
					t.Fatalf("history len=%d err=%v, want %d", len(runs), err, wantCount)
				}
				if runs[0].Success != (wantCode == 0) || runs[0].TimedOut != (outcome == "timeout") {
					t.Errorf("result changed by retention: %+v", runs[0])
				}
				if outcome == "canceled" && runs[0].Err != context.Canceled.Error() {
					t.Errorf("canceled result not recorded: %+v", runs[0])
				}
				other, err := st.RecentRuns(context.Background(), "other", 0)
				if err != nil || len(other) != 3 {
					t.Errorf("other job changed: len=%d err=%v", len(other), err)
				}
				if !strings.Contains(out.String(), "job bounded:") {
					t.Errorf("missing run summary: %s", out.String())
				}
			})
		}
	}
}

func TestCmdRunReportsRetentionFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedHistory(t, st, "bounded", 3)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER fail_delete BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT, 'retention failure'); END"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code, err := cmdRun(context.Background(), &out, []config.Job{{Name: "bounded", Command: "true", HistoryLimit: 1}}, st, "bounded")
	if code != 1 || err == nil || !strings.Contains(err.Error(), "retention failure") {
		t.Fatalf("cleanup failure was hidden: exit=%d err=%v", code, err)
	}
	runs, err := st.RecentRuns(context.Background(), "bounded", 0)
	if err != nil || len(runs) != 3 || !bytes.Equal(runs[0].Stdout, []byte("old-output")) {
		t.Fatalf("existing history changed on failure: len=%d err=%v", len(runs), err)
	}
}
