package main

import (
	"path/filepath"
	"testing"
)

func TestConfigPath(t *testing.T) {
	base := filepath.Join("/home", "u", ".config")
	if got, want := configPath("", base), filepath.Join(base, "rote", "jobs.toml"); got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	if got, want := configPath("/custom/jobs.toml", base), "/custom/jobs.toml"; got != want {
		t.Errorf("override = %q, want %q", got, want)
	}
}

func TestDBPath(t *testing.T) {
	home := filepath.Join("/home", "u")

	if got, want := dbPath("", "", home), filepath.Join(home, ".local", "state", "rote", "rote.db"); got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	xdg := filepath.Join("/xdg", "state")
	if got, want := dbPath("", xdg, home), filepath.Join(xdg, "rote", "rote.db"); got != want {
		t.Errorf("XDG override = %q, want %q", got, want)
	}
	if got, want := dbPath("/custom/x.db", xdg, home), "/custom/x.db"; got != want {
		t.Errorf("override = %q, want %q", got, want)
	}
}

// resolveDBPath honors $XDG_STATE_HOME end to end.
func TestResolveDBPathEnv(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	got, err := resolveDBPath("")
	if err != nil {
		t.Fatalf("resolveDBPath: %v", err)
	}
	if want := filepath.Join(state, "rote", "rote.db"); got != want {
		t.Errorf("resolveDBPath = %q, want %q", got, want)
	}

	// An explicit override wins over the environment.
	if got, _ := resolveDBPath("/explicit.db"); got != "/explicit.db" {
		t.Errorf("override = %q, want /explicit.db", got)
	}
}

// resolveConfigPath uses the OS config dir (driven by XDG_CONFIG_HOME on Linux).
func TestResolveConfigPathEnv(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	if want := filepath.Join(cfgDir, "rote", "jobs.toml"); got != want {
		t.Errorf("resolveConfigPath = %q, want %q", got, want)
	}
	if got, _ := resolveConfigPath("/explicit.toml"); got != "/explicit.toml" {
		t.Errorf("override = %q, want /explicit.toml", got)
	}
}
