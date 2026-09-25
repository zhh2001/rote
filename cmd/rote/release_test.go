//go:build darwin || linux

package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/store"
)

func TestReleaseBinaryVersionAndInit(t *testing.T) {
	if os.Getenv("ROTE_TEST_BINARY") == "" {
		t.Skip("set ROTE_TEST_BINARY to verify a packaged executable")
	}
	version := os.Getenv("ROTE_TEST_VERSION")
	if version == "" {
		t.Fatal("ROTE_TEST_VERSION is required with ROTE_TEST_BINARY")
	}
	p := newCLIProcess(t, "version")
	p.start(t)
	p.wait(t, 0)
	if got := strings.TrimSpace(p.output.String()); got != "rote "+version {
		t.Fatalf("version = %q, want rote %s", got, version)
	}
	path := filepath.Join(t.TempDir(), "nested", "jobs.toml")
	for range 2 {
		p = newCLIProcess(t, "init", "-c", path)
		p.start(t)
		p.wait(t, 0)
	}
	if jobs, err := config.Load(path); err != nil || len(jobs) != 1 || jobs[0].Name != "example" {
		t.Fatalf("packaged init generated invalid config: jobs=%+v err=%v", jobs, err)
	}
	if !strings.Contains(p.output.String(), "not overwriting") {
		t.Fatal("second init did not preserve the existing config")
	}
}

func TestReleaseUpgradeFromPrevious(t *testing.T) {
	previous := os.Getenv("ROTE_TEST_PREVIOUS_BINARY")
	if previous == "" {
		t.Skip("set ROTE_TEST_PREVIOUS_BINARY to verify an old database upgrade")
	}
	if !filepath.IsAbs(previous) {
		t.Fatal("ROTE_TEST_PREVIOUS_BINARY must be an absolute path")
	}
	legacy := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, previous, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("previous release %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if version := strings.TrimSpace(legacy("version")); version != "rote 0.2.1" {
		t.Fatalf("upgrade baseline must be v0.2.1, got %q", version)
	}
	cfg := writeConfig(t, "[[job]]\nname='legacy'\nschedule='@hourly'\ncommand='echo preserved-output'\n")
	path := filepath.Join(t.TempDir(), "old.db")
	for range 3 {
		legacy("run", "-c", cfg, "--db", path, "legacy")
	}
	// Inspect without Store.Open so the baseline is captured before any new
	// version's database initialization has run.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, schema int
	if err := db.QueryRow("SELECT count(*) FROM runs").Scan(&count); err != nil || count != 3 {
		t.Fatalf("old database seed: count=%d err=%v", count, err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 1 {
		t.Fatalf("old database schema: version=%d err=%v", schema, err)
	}
	for _, args := range [][]string{
		{"logs", "--db", path, "-o", "legacy"},
		{"list", "-c", cfg, "--db", path},
	} {
		p := newCLIProcess(t, args...)
		p.start(t)
		p.wait(t, 0)
		if !strings.Contains(p.output.String(), "legacy") && args[0] == "list" {
			t.Fatal("upgraded list lost the job")
		}
		if args[0] == "logs" && !strings.Contains(p.output.String(), "preserved-output") {
			t.Fatal("upgraded logs lost old captured output")
		}
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	before, err := st.RecentRuns(context.Background(), "legacy", 0)
	if err != nil || len(before) != 3 {
		t.Fatalf("read-only upgrade changed history: runs=%+v err=%v", before, err)
	}
	p := newCLIProcess(t, "run", "-c", cfg, "--db", path, "legacy")
	p.start(t)
	p.wait(t, 0)
	after, err := st.RecentRuns(context.Background(), "legacy", 0)
	if err != nil || len(after) != 4 || !reflect.DeepEqual(before, after[1:]) {
		t.Fatalf("new write changed existing records: before=%+v after=%+v err=%v", before, after, err)
	}
	if out := legacy("logs", "--db", path, "-o", "legacy"); !strings.Contains(out, "preserved-output") {
		t.Fatal("old release cannot read the unchanged schema after new writes")
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("upgraded database integrity: %q err=%v", integrity, err)
	}
}
