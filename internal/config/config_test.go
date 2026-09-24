package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// write creates a temp .toml file with the given content and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestLoadHistoryLimit(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		value   string
		want    int
		invalid bool
	}{
		{value: ""}, {value: "0"}, {value: "1", want: 1},
		{value: "1000", want: 1000}, {value: fmt.Sprint(maxInt), want: maxInt},
		{value: "-1", invalid: true}, {value: "-100", invalid: true},
		{value: "1.5", invalid: true}, {value: "1.0", invalid: true},
		{value: "'2'", invalid: true}, {value: "true", invalid: true},
		{value: "[]", invalid: true}, {value: "9223372036854775808", invalid: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			content := "[[job]]\nname='bounded'\nschedule='@hourly'\ncommand='true'\n"
			if tc.value != "" {
				content += "history_limit=" + tc.value + "\n"
			}
			jobs, err := Load(write(t, content))
			if tc.invalid {
				if err == nil || jobs != nil || !strings.Contains(err.Error(), "history_limit") {
					t.Fatalf("invalid history limit accepted or poorly diagnosed: jobs=%+v err=%v", jobs, err)
				}
				return
			}
			if err != nil || len(jobs) != 1 || jobs[0].HistoryLimit != tc.want {
				t.Fatalf("jobs=%+v err=%v, want history limit %d", jobs, err, tc.want)
			}
		})
	}
}

func TestLoadHistoryLimitCollectsOtherErrors(t *testing.T) {
	jobs, err := Load(write(t, "[[job]]\nname='bad'\nschedule='@hourly'\ncommand=''\ntimeout='-1s'\nhistory_limit=-1\n"))
	if err == nil || jobs != nil {
		t.Fatalf("invalid config accepted: jobs=%+v err=%v", jobs, err)
	}
	for _, text := range []string{"bad", "command must not be empty", "timeout", "history_limit", "non-negative"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("combined error %q missing %q", err, text)
		}
	}
}

func TestLoadTimeoutBounds(t *testing.T) {
	cases := []struct {
		timeout string
		want    time.Duration
		invalid bool
	}{
		{timeout: ""},
		{timeout: "0"},
		{timeout: "0s"},
		{timeout: "1ns", want: time.Nanosecond},
		{timeout: "100ms", want: 100 * time.Millisecond},
		{timeout: "1h30m", want: 90 * time.Minute},
		{timeout: "9223372036854775807ns", want: time.Duration(1<<63 - 1)},
		{timeout: "-1ns", invalid: true},
		{timeout: "-100ms", invalid: true},
		{timeout: "-1h30m", invalid: true},
		{timeout: "-9223372036854775808ns", invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.timeout, func(t *testing.T) {
			content := "[[job]]\nname='bounded'\nschedule='every 1m'\ncommand='true'\n"
			if tc.timeout != "" {
				content += fmt.Sprintf("timeout=%q\n", tc.timeout)
			}
			jobs, err := Load(write(t, content))
			if tc.invalid {
				if err == nil || jobs != nil {
					t.Fatalf("negative timeout accepted: jobs=%+v err=%v", jobs, err)
				}
				for _, text := range []string{"bounded", "timeout", tc.timeout, "non-negative"} {
					if !strings.Contains(err.Error(), text) {
						t.Errorf("error %q does not contain %q", err, text)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 || jobs[0].Timeout != tc.want {
				t.Fatalf("jobs=%+v, want timeout %v", jobs, tc.want)
			}
		})
	}
}

func TestLoadNegativeTimeoutCollectsOtherErrors(t *testing.T) {
	_, err := Load(write(t, `
[[job]]
name = "negative"
schedule = "@hourly"
command = "true"
timeout = "-1s"

[[job]]
name = "empty-command"
schedule = "@hourly"
command = ""
`))
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	for _, text := range []string{"negative", "timeout", "non-negative", "empty-command", "command must not be empty"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("combined error %q does not contain %q", err, text)
		}
	}
}

// 1. A valid file with mixed optional fields parses correctly.
func TestLoadValid(t *testing.T) {
	path := write(t, `
[[job]]
name = "backup"
schedule = "daily at 03:00"
command = "/usr/local/bin/backup.sh"
timeout = "10m"
on_failure = "notify"

[[job]]
name = "heartbeat"
schedule = "every 90s"
command = "curl -s https://example.com/ping"

[[job]]
name = "report"
schedule = "0 9 * * 1"
command = "make report"
timeout = "1h30m"
`)

	jobs, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("got %d jobs, want 3", len(jobs))
	}

	want := []Job{
		{Name: "backup", Schedule: "daily at 03:00", Command: "/usr/local/bin/backup.sh", Timeout: 10 * time.Minute, OnFailure: "notify"},
		{Name: "heartbeat", Schedule: "every 90s", Command: "curl -s https://example.com/ping", Timeout: 0, OnFailure: ""},
		{Name: "report", Schedule: "0 9 * * 1", Command: "make report", Timeout: 90 * time.Minute, OnFailure: ""},
	}
	for i, w := range want {
		if jobs[i] != w {
			t.Errorf("job %d = %+v, want %+v", i, jobs[i], w)
		}
	}
}

// 2. Each validation failure, asserting the message names the job/field.
func TestLoadValidationFailures(t *testing.T) {
	cases := []struct {
		name    string
		content string
		substrs []string // all must appear in the error
	}{
		{
			name: "duplicate name",
			content: `
[[job]]
name = "dup"
schedule = "@hourly"
command = "echo a"

[[job]]
name = "dup"
schedule = "@hourly"
command = "echo b"
`,
			substrs: []string{"dup", "duplicate"},
		},
		{
			name: "empty command",
			content: `
[[job]]
name = "nocmd"
schedule = "@hourly"
command = ""
`,
			substrs: []string{"nocmd", "command"},
		},
		{
			name: "empty name",
			content: `
[[job]]
name = ""
schedule = "@hourly"
command = "echo hi"
`,
			substrs: []string{"job #1", "name"},
		},
		{
			name: "invalid schedule",
			content: `
[[job]]
name = "badsched"
schedule = "not a schedule"
command = "echo hi"
`,
			substrs: []string{"badsched", "schedule"},
		},
		{
			name: "invalid timeout",
			content: `
[[job]]
name = "badtimeout"
schedule = "@hourly"
command = "echo hi"
timeout = "10 furlongs"
`,
			substrs: []string{"badtimeout", "timeout"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(write(t, c.content))
			if err == nil {
				t.Fatalf("Load succeeded, want error")
			}
			msg := err.Error()
			for _, sub := range c.substrs {
				if !strings.Contains(msg, sub) {
					t.Errorf("error %q does not contain %q", msg, sub)
				}
			}
		})
	}
}

// 3. Several problems are all reported at once.
func TestLoadCollectsAllErrors(t *testing.T) {
	path := write(t, `
[[job]]
name = ""
schedule = "@hourly"
command = "echo ok"

[[job]]
name = "missingcmd"
schedule = "garbage schedule"
command = ""

[[job]]
name = "badt"
schedule = "@daily"
command = "echo hi"
timeout = "soon"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load succeeded, want error")
	}
	msg := err.Error()

	// Expect: empty name (job #1), empty command + bad schedule (missingcmd),
	// and bad timeout (badt) -- four distinct problems.
	for _, sub := range []string{"name", "command", "schedule", "timeout", "missingcmd", "badt"} {
		if !strings.Contains(msg, sub) {
			t.Errorf("combined error missing %q:\n%s", sub, msg)
		}
	}
	if lines := strings.Count(msg, "\n") + 1; lines < 4 {
		t.Errorf("expected at least 4 error lines, got %d:\n%s", lines, msg)
	}
}

// 4. A file with no jobs is an error (chosen semantics).
func TestLoadNoJobs(t *testing.T) {
	for _, content := range []string{"", "# just a comment\n"} {
		_, err := Load(write(t, content))
		if err == nil {
			t.Errorf("Load(%q) succeeded, want error for empty/jobless file", content)
		}
	}
}

// 5. Missing file and TOML syntax errors both return errors.
func TestLoadFileAndSyntaxErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml")); err == nil {
		t.Errorf("Load(missing) succeeded, want error")
	}

	bad := write(t, `
[[job]]
name = "broken
schedule = "@hourly"
`)
	if _, err := Load(bad); err == nil {
		t.Errorf("Load(syntax error) succeeded, want error")
	}
}

// 6. An unknown key is rejected and named, catching typos.
func TestLoadUnknownKey(t *testing.T) {
	path := write(t, `
[[job]]
name = "typo"
schedule = "@hourly"
command = "true"
timout = "10m"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded, want error for unknown key")
	}
	if !strings.Contains(err.Error(), "timout") {
		t.Errorf("error %q should name the unknown key %q", err.Error(), "timout")
	}

	// A correct file with all known keys must still load.
	ok := write(t, `
[[job]]
name = "fine"
schedule = "@hourly"
command = "true"
timeout = "10m"
on_failure = "echo failed"
`)
	if _, err := Load(ok); err != nil {
		t.Errorf("Load(valid) returned error: %v", err)
	}
}
