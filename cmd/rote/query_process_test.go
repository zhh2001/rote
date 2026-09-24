//go:build darwin || linux

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/store"
)

func TestReadCommandsProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rote.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := writeConfig(t, "[[job]]\nname='job'\nschedule='@hourly'\ncommand='true'\n")
	now := time.Now()
	for i, minute := range []int{-1, 0, 0, -2} {
		stdout := "unwanted-older-output"
		if i == 2 {
			stdout = "selected-latest-output"
		}
		if _, err := st.Insert(context.Background(), store.Run{
			JobName: "job", StartedAt: now.Add(time.Duration(minute) * time.Minute),
			Success: i == 2, ExitCode: i, Stdout: []byte(stdout),
			Stderr: []byte("selected-stderr"), StdoutTruncated: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		args []string
		want []string
		omit []string
	}{
		{"list", []string{"list", "-c", cfg, "--db", path}, []string{"NAME", "job", "✓"}, []string{"output", "✗"}},
		{"logs", []string{"logs", "--db", path, "-n", "1", "job"}, []string{"TIME", "✓"}, []string{"output", "✗", "stderr"}},
		{"logs-output", []string{"logs", "--db", path, "-o", "-n", "20", "job"},
			[]string{"TIME", "✓", "✗", "selected-latest-output", "selected-stderr", "truncated"}, []string{"unwanted-older-output"}},
		{"empty", []string{"logs", "--db", path, "-o", "missing"}, []string{"no runs recorded"}, []string{"stdout", "stderr"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newCLIProcess(t, tc.args...)
			p.start(t)
			p.wait(t, 0)
			out := p.output.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q: %s", want, out)
				}
			}
			for _, omit := range tc.omit {
				if strings.Contains(out, omit) {
					t.Errorf("unexpected %q: %s", omit, out)
				}
			}
		})
	}
	if runs, err := st.RecentRunsMeta(context.Background(), "job", 0); err != nil || len(runs) != 4 {
		t.Fatalf("read commands changed history: count=%d err=%v", len(runs), err)
	}
}
