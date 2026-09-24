package main

import "testing"

func TestRetentionReadOnlyDashboardDoesNotPrune(t *testing.T) {
	cfg, path, st := retentionFixture(t, 1)
	viewer, terminal := startDashboard(t, "tui", "-c", cfg, "--db", path)
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	viewer.wait(t, 0)
	assertRetainedCounts(t, st, 5, 3)
}
