package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhh2001/rote/internal/config"
)

func TestEngineHistoryRetention(t *testing.T) {
	for _, limit := range []int{0, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			st := openStore(t)
			marker := filepath.Join(t.TempDir(), "hook")
			entry := &jobEntry{cfg: config.Job{Name: "bounded", HistoryLimit: limit, OnFailure: fmt.Sprintf("touch %q", marker)}}
			e := newEngine([]*jobEntry{entry}, st, nil)
			defer e.ForceStop()
			for i := 0; i < 5; i++ {
				entry.cfg.Command = fmt.Sprintf("echo run-%d; exit %d", i, i%2)
				e.execute(entry)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("retention prevented failure hook: %v", err)
			}
			// Forced cancellation must still record/prune with a detached
			// persistence context, without starting another failure hook.
			e.ForceStop()
			e.execute(entry)
			runs := recent(t, st, "bounded")
			want := 6
			if limit > 0 {
				want = limit
			}
			if len(runs) != want {
				t.Fatalf("retained %d runs, want %d", len(runs), want)
			}
			last := runs[len(runs)-1]
			if last.Success || last.Err != context.Canceled.Error() || last.TimedOut {
				t.Fatalf("canceled result not preserved: %+v", last)
			}
			if !strings.Contains(string(runs[len(runs)-2].Stdout), "run-4") {
				t.Fatalf("wrong retained run: %+v", runs[len(runs)-2])
			}
		})
	}
}
