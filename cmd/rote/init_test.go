package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestCmdInitKeepsSymlinks(t *testing.T) {
	for _, exists := range []bool{false, true} {
		for _, relative := range []bool{false, true} {
			name := "dangling"
			if exists {
				name = "existing"
			}
			if relative {
				name += "/relative"
			} else {
				name += "/absolute"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "jobs.toml")
				target := filepath.Join(dir, "target.toml")
				original := "# existing jobs\n"
				if exists {
					if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				link := target
				if relative {
					link = filepath.Base(target)
				}
				if err := os.Symlink(link, path); err != nil {
					t.Fatal(err)
				}

				var out, errBuf bytes.Buffer
				if code := dispatch([]string{"init", "-c", path}, &out, &errBuf); code != 0 {
					t.Fatalf("init code = %d (stderr: %s)", code, errBuf.String())
				}
				if !strings.Contains(out.String(), "already exists") {
					t.Errorf("init output = %q, want existing config message", out.String())
				}
				if got, err := os.Readlink(path); err != nil || got != link {
					t.Errorf("config link = %q, %v; want %q", got, err, link)
				}
				if exists {
					data, err := os.ReadFile(target)
					if err != nil || string(data) != original {
						t.Errorf("target contents = %q, %v; want %q", data, err, original)
					}
				} else if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Errorf("dangling link target was created: %v", err)
				}
			})
		}
	}
}

func TestCmdInitConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "jobs.toml")
	const workers = 32
	type result struct {
		code   int
		stdout string
		stderr string
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			ready.Done()
			<-start
			var out, errBuf bytes.Buffer
			code := cmdInit(&out, &errBuf, path)
			results <- result{code, out.String(), errBuf.String()}
		}()
	}
	ready.Wait()
	close(start)
	created := 0
	for i := 0; i < workers; i++ {
		r := <-results
		if r.code != 0 {
			t.Errorf("init code = %d (stderr: %s)", r.code, r.stderr)
		}
		if strings.Contains(r.stdout, "wrote starter config") {
			created++
		} else if !strings.Contains(r.stdout, "already exists") {
			t.Errorf("unexpected init output: %q", r.stdout)
		}
	}
	if created != 1 {
		t.Errorf("%d initializers wrote the config, want exactly one", created)
	}
	jobs, err := config.Load(path)
	if err != nil {
		t.Fatalf("created config does not load: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Name != "example" {
		t.Errorf("starter jobs = %+v, want one job named example", jobs)
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
