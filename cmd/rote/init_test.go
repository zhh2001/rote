package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhh2001/rote/internal/config"
)

// cmdInit writes the starter config (creating missing parent dirs) and the
// result loads cleanly.
func TestCmdInitWritesAndLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "jobs.toml")

	var out, errBuf bytes.Buffer
	if code := cmdInit(&out, &errBuf, path); code != 0 {
		t.Fatalf("cmdInit code = %d, want 0 (stderr: %s)", code, errBuf.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("starter config not written: %v", err)
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("success message should mention the path:\n%s", out.String())
	}

	// init -> Load round-trip: the starter config must parse.
	jobs, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load(starter) failed: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Name != "example" {
		t.Errorf("starter jobs = %+v, want one job named example", jobs)
	}
}

// cmdInit does not overwrite an existing file.
func TestCmdInitNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.toml")
	original := "# my edits\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	var out, errBuf bytes.Buffer
	if code := cmdInit(&out, &errBuf, path); code != 0 {
		t.Fatalf("cmdInit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("expected an already-exists message, got:\n%s", out.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != original {
		t.Errorf("file was overwritten: %q", data)
	}
}

// runInit honors -c and writes a loadable config there.
func TestRunInitRespectsConfigFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.toml")

	var out, errBuf bytes.Buffer
	if code := dispatch([]string{"init", "-c", path}, &out, &errBuf); code != 0 {
		t.Fatalf("dispatch init code = %d (stderr: %s)", code, errBuf.String())
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("config at -c path did not load: %v", err)
	}
}

// A missing config produces a friendly, actionable error pointing at `rote init`.
func TestMissingConfigFriendlyError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.toml")
	db := filepath.Join(t.TempDir(), "rote.db")

	var errBuf bytes.Buffer
	code := runList([]string{"-c", missing, "--db", db}, io.Discard, &errBuf)
	if code == 0 {
		t.Fatalf("runList code = 0, want non-zero for missing config")
	}
	msg := errBuf.String()
	if !strings.Contains(msg, "rote init") {
		t.Errorf("error should suggest 'rote init':\n%s", msg)
	}
	if !strings.Contains(msg, missing) {
		t.Errorf("error should name the missing path %q:\n%s", missing, msg)
	}
	if strings.Contains(msg, "no such file or directory") {
		t.Errorf("raw os error leaked through:\n%s", msg)
	}
}

// A parse error (not a missing file) is reported as-is, not masked by the
// friendly missing-config handling.
func TestParseErrorStillReported(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "jobs.toml")
	if err := os.WriteFile(bad, []byte("this is = not valid toml [["), 0o644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	db := filepath.Join(t.TempDir(), "rote.db")

	var errBuf bytes.Buffer
	code := runList([]string{"-c", bad, "--db", db}, io.Discard, &errBuf)
	if code == 0 {
		t.Fatalf("runList code = 0, want non-zero for bad TOML")
	}
	msg := errBuf.String()
	if strings.Contains(msg, "rote init") {
		t.Errorf("parse error should not be replaced by the init hint:\n%s", msg)
	}
	if !strings.Contains(msg, "parse") {
		t.Errorf("parse error should be reported:\n%s", msg)
	}
}
