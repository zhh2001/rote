package main

import "path/filepath"

// configPath resolves the jobs.toml path. A non-empty override wins; otherwise
// it is userConfigDir/rote/jobs.toml.
func configPath(override, userConfigDir string) string {
	if override != "" {
		return override
	}
	return filepath.Join(userConfigDir, "rote", "jobs.toml")
}

// dbPath resolves the database path. A non-empty override wins; otherwise it is
// <state>/rote/rote.db, where <state> is $XDG_STATE_HOME or ~/.local/state.
func dbPath(override, xdgStateHome, home string) string {
	if override != "" {
		return override
	}
	state := xdgStateHome
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "rote", "rote.db")
}
