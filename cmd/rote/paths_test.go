package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateConfigDir redirects both supported OS config roots to temporary
// directories. Setting XDG_CONFIG_HOME alone does not isolate macOS tests.
// Return the platform's expected directory without calling the resolver under
// test (or os.UserConfigDir), so a wrong platform choice cannot validate itself.
func isolateConfigDir(t *testing.T, useXDG bool) string {
	t.Helper()
	testHome := t.TempDir()
	t.Setenv("HOME", testHome)
	xdgConfig := ""
	if useXDG {
		xdgConfig = t.TempDir()
	}
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(testHome, "Library", "Application Support")
	case "linux":
		if useXDG {
			return xdgConfig
		}
		return filepath.Join(testHome, ".config")
	default:
		t.Fatalf("unsupported test platform: %s", runtime.GOOS)
		return ""
	}
}

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

// Linux honors XDG_CONFIG_HOME, falling back to ~/.config. macOS always uses
// ~/Library/Application Support, even when XDG_CONFIG_HOME is set.
func TestResolveConfigPathEnv(t *testing.T) {
	for _, useXDG := range []bool{true, false} {
		name := "xdg_empty"
		if useXDG {
			name = "xdg_set"
		}
		t.Run(name, func(t *testing.T) {
			cfgDir := isolateConfigDir(t, useXDG)
			got, err := resolveConfigPath("")
			if err != nil {
				t.Fatalf("resolveConfigPath: %v", err)
			}
			if want := filepath.Join(cfgDir, "rote", "jobs.toml"); got != want {
				t.Errorf("resolveConfigPath = %q, want %q", got, want)
			}
			if got, err := resolveConfigPath("/explicit.toml"); err != nil || got != "/explicit.toml" {
				t.Errorf("override = %q, err = %v; want /explicit.toml", got, err)
			}
		})
	}
}

func TestResolveConfigPathWithoutHome(t *testing.T) {
	for _, useXDG := range []bool{true, false} {
		name := "xdg_empty"
		if useXDG {
			name = "xdg_set"
		}
		t.Run(name, func(t *testing.T) {
			cfgDir := isolateConfigDir(t, useXDG)
			t.Setenv("HOME", "")
			got, err := resolveConfigPath("")
			if runtime.GOOS == "linux" && useXDG {
				// Linux can locate config using XDG_CONFIG_HOME alone; macOS
				// ignores that variable and still needs HOME.
				if want := filepath.Join(cfgDir, "rote", "jobs.toml"); err != nil || got != want {
					t.Fatalf("resolveConfigPath = %q, err = %v; want %q", got, err, want)
				}
			} else if err == nil || got != "" || !strings.Contains(err.Error(), "locate config directory") {
				t.Fatalf("resolveConfigPath = %q, err = %v; want config directory error", got, err)
			}
			// An explicit -c/--config must work even without a usable default.
			if got, err := resolveConfigPath("/explicit.toml"); err != nil || got != "/explicit.toml" {
				t.Errorf("override = %q, err = %v; want /explicit.toml", got, err)
			}
		})
	}
}

func TestIsolateConfigDirRestoresEnvironment(t *testing.T) {
	beforeHome, hadHome := os.LookupEnv("HOME")
	beforeXDG, hadXDG := os.LookupEnv("XDG_CONFIG_HOME")
	t.Run("isolated", func(t *testing.T) {
		cfgDir := isolateConfigDir(t, true)
		if _, err := os.Stat(filepath.Join(cfgDir, "rote", "jobs.toml")); !os.IsNotExist(err) {
			t.Fatalf("isolated config should not exist: %v", err)
		}
	})
	if got, set := os.LookupEnv("HOME"); got != beforeHome || set != hadHome {
		t.Errorf("HOME was not restored after the test")
	}
	if got, set := os.LookupEnv("XDG_CONFIG_HOME"); got != beforeXDG || set != hadXDG {
		t.Errorf("XDG_CONFIG_HOME was not restored after the test")
	}
}
