package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runDispatch invokes dispatch with captured output.
func runDispatch(args []string) (code int, stdout, stderr string) {
	var out, errBuf bytes.Buffer
	code = dispatch(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// Flag-leading arguments route to the default all-in-one command and its flags
// are parsed (rather than being mistaken for an unknown command).
func TestDispatchFlagsRouteToDefault(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "nope.toml")
	db := filepath.Join(t.TempDir(), "rote.db")

	for _, args := range [][]string{
		{"-c", bad, "--db", db},
		{"--db", db, "-c", bad},
		{"--config", bad, "--db", db},
	} {
		code, _, stderr := runDispatch(args)
		if strings.Contains(stderr, "unknown command") {
			t.Errorf("args %v: routed to unknown command:\n%s", args, stderr)
		}
		if code == 0 {
			t.Errorf("args %v: code = 0, want non-zero (bad config)", args)
		}
		// The -c value was parsed and used, proving routing to the default cmd.
		if !strings.Contains(stderr, bad) {
			t.Errorf("args %v: stderr does not mention the -c path %q:\n%s", args, bad, stderr)
		}
	}
}

// No arguments still routes to the default command.
func TestDispatchNoArgsDefault(t *testing.T) {
	// Point the default config/db at empty temp dirs so it fails fast (no TTY).
	cfgDir := isolateConfigDir(t, true)
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	code, _, stderr := runDispatch(nil)
	if strings.Contains(stderr, "unknown command") {
		t.Errorf("no args routed to unknown command:\n%s", stderr)
	}
	if code != 1 {
		t.Errorf("code = %d, want 1 (missing default config)", code)
	}
	missing := filepath.Join(cfgDir, "rote", "jobs.toml")
	if !strings.Contains(stderr, missing) || !strings.Contains(stderr, "rote init") {
		t.Errorf("stderr should name the isolated missing config and init hint:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "rote")); !os.IsNotExist(err) {
		t.Errorf("missing config should not create database state: %v", err)
	}
}

// A non-flag word that is not a known subcommand is still an error.
func TestDispatchUnknownCommand(t *testing.T) {
	code, _, stderr := runDispatch([]string{"bogus"})
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr, `unknown command "bogus"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", stderr)
	}
}

// Subcommand-first usage with trailing flags keeps working.
func TestDispatchSubcommandWithFlags(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "nope.toml")
	db := filepath.Join(t.TempDir(), "rote.db")

	// Flags precede the positional job argument, as Go's flag package requires.
	cases := [][]string{
		{"tui", "-c", bad, "--db", db},
		{"list", "-c", bad, "--db", db},
		{"run", "-c", bad, "--db", db, "somejob"},
	}
	for _, args := range cases {
		code, _, stderr := runDispatch(args)
		if strings.Contains(stderr, "unknown command") {
			t.Errorf("args %v: unexpected unknown command:\n%s", args, stderr)
		}
		if code == 0 {
			t.Errorf("args %v: code = 0, want non-zero (bad config)", args)
		}
		if !strings.Contains(stderr, bad) {
			t.Errorf("args %v: stderr should mention the -c path %q:\n%s", args, bad, stderr)
		}
	}
}

// Help and version are unaffected, including the leading-dash help forms.
func TestDispatchHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		code, stdout, _ := runDispatch(args)
		if code != 0 {
			t.Errorf("%v: code = %d, want 0", args, code)
		}
		if !strings.Contains(stdout, "Usage") {
			t.Errorf("%v: stdout missing usage:\n%s", args, stdout)
		}
	}

	code, stdout, _ := runDispatch([]string{"version"})
	if code != 0 {
		t.Errorf("version: code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "rote ") {
		t.Errorf("version stdout = %q", stdout)
	}
}
