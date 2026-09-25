package tui

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zhh2001/rote/internal/store"
)

func recoveryStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rote.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return st, db
}

// Make actual SQLite reads fail in just one part of the dashboard, without
// replacing the store or relying on timing. Only test-owned databases change.
func failReads(t *testing.T, db *sql.DB, output bool) func() {
	t.Helper()
	columns := "id, job_name, started_at, finished_at, duration, exit_code, timed_out, success, stdout, stderr, stdout_truncated, stderr_truncated, err"
	if output {
		columns = strings.Replace(columns, " stdout,", " abs(-9223372036854775808) AS stdout,", 1)
	} else {
		columns = strings.TrimSuffix(columns, "err") + "abs(-9223372036854775808) AS err"
	}
	if _, err := db.Exec(`ALTER TABLE runs RENAME TO saved_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE VIEW runs AS SELECT ` + columns + ` FROM saved_runs`); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		t.Helper()
		if restored {
			return
		}
		if _, err := db.Exec(`DROP VIEW runs`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`ALTER TABLE saved_runs RENAME TO runs`); err != nil {
			t.Fatal(err)
		}
		restored = true
	}
	t.Cleanup(restore)
	return restore
}

func refreshMessage(manual bool) tea.Msg {
	if manual {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	}
	return tickMsg(time.Now())
}

func sizedModel(st *store.Store) model {
	return update(newModel(context.Background(), twoJobs(), st), tea.WindowSizeMsg{Width: 120, Height: 30})
}

func TestMetadataReadRecovery(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, initial := range []bool{false, true} {
			for _, manual := range []bool{false, true} {
				t.Run(fmt.Sprintf("detail=%t/initial=%t/manual=%t", detail, initial, manual), func(t *testing.T) {
					st, db := recoveryStore(t)
					oldID := insertRun(t, st, "alpha", time.Now().Add(-time.Minute), true, 0, time.Second, "old-output")
					var m model
					if !initial {
						m = sizedModel(st)
						if detail {
							m = update(m, keyType(tea.KeyEnter))
						}
					}
					newID := insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "recovered-output")
					restore := failReads(t, db, false)
					if initial {
						m = sizedModel(st)
						if detail {
							m = update(m, keyType(tea.KeyEnter))
						}
					}
					for range 2 {
						m = update(m, refreshMessage(manual))
						if m.loadErr == nil || !strings.Contains(m.View(), "error loading data:") {
							t.Errorf("metadata failure must remain visible: err=%v view=%s", m.loadErr, m.View())
						}
					}
					if detail && initial && strings.Contains(m.View(), "no runs yet") {
						t.Error("failed history query must not be presented as empty history")
					}
					if !initial {
						if detail && (len(m.history) != 1 || m.outputID != oldID || !m.outputOK) {
							t.Error("temporary metadata failure lost previously loaded detail")
						}
						if !detail && m.latest["alpha"].ID != oldID {
							t.Error("temporary metadata failure lost previously loaded list")
						}
					}
					restore()
					m = update(m, refreshMessage(manual))
					if m.loadErr != nil || strings.Contains(m.View(), "error loading") {
						t.Errorf("recovered query retained stale error: err=%v view=%s", m.loadErr, m.View())
					}
					if detail {
						if len(m.history) != 2 || m.outputID != newID || !m.outputOK || string(m.output.Stdout) != "recovered-output" {
							t.Fatalf("detail did not reload: history=%+v output=%+v", m.history, m.output)
						}
					} else if m.latest["alpha"].ID != newID {
						t.Fatalf("list did not reload: %+v", m.latest)
					}
				})
			}
		}
	}
}

func TestOutputReadRecovery(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			st, db := recoveryStore(t)
			id := insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "recovered-output")
			restore := failReads(t, db, true)
			m := update(sizedModel(st), keyType(tea.KeyEnter))
			for range 2 {
				m = update(m, refreshMessage(manual))
				if m.outputOK || !strings.Contains(m.View(), "error loading output:") {
					t.Fatalf("output failure was hidden by successful history load: %s", m.View())
				}
			}
			restore()
			m = update(m, refreshMessage(manual))
			if m.outputID != id || !m.outputOK || string(m.output.Stdout) != "recovered-output" || strings.Contains(m.View(), "error loading") {
				t.Fatalf("same selection was not retried: id=%d ok=%t output=%+v view=%s", m.outputID, m.outputOK, m.output, m.View())
			}
			if m.loadErr != nil {
				t.Errorf("recovery retained error: %v", m.loadErr)
			}
		})
	}
}

func TestOutputFailureClearsPreviousSelection(t *testing.T) {
	st, db := recoveryStore(t)
	insertRun(t, st, "alpha", time.Now().Add(-time.Minute), true, 0, time.Second, "older-output")
	insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "newer-output")
	m := update(sizedModel(st), keyType(tea.KeyEnter))
	restore := failReads(t, db, true)
	m = update(m, keyType(tea.KeyDown))
	if m.outputOK || len(m.output.Stdout) != 0 || !strings.Contains(m.View(), "error loading output:") {
		t.Fatalf("failed selection retained another run's output: %+v", m.output)
	}
	restore()
	m = update(m, keyType(tea.KeyUp))
	if !m.outputOK || string(m.output.Stdout) != "newer-output" || strings.Contains(m.View(), "error loading") || m.loadErr != nil {
		t.Fatalf("new selection did not clear old error: output=%+v err=%v", m.output, m.loadErr)
	}
}

func TestOutputRecoveryDoesNotClearHistoryFailure(t *testing.T) {
	st, db := recoveryStore(t)
	insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "recovered-output")
	restore := failReads(t, db, true)
	m := update(sizedModel(st), keyType(tea.KeyEnter))
	restore()
	restore = failReads(t, db, false)
	m = update(m, refreshMessage(false))
	if !m.outputOK || string(m.output.Stdout) != "recovered-output" {
		t.Fatal("output should recover even while history reads fail")
	}
	if m.loadErr == nil || !strings.Contains(m.View(), "error loading data:") || strings.Contains(m.View(), "error loading output:") {
		t.Fatalf("independent read errors were not preserved: %s", m.View())
	}
	restore()
	m = update(m, refreshMessage(false))
	if m.loadErr != nil || strings.Contains(m.View(), "error loading") {
		t.Fatalf("history error persisted after full recovery: %s", m.View())
	}
}

func TestFailedJobSwitchDoesNotShowPreviousHistory(t *testing.T) {
	st, db := recoveryStore(t)
	insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "alpha-output")
	betaID := insertRun(t, st, "beta", time.Now(), true, 0, time.Second, "beta-output")
	m := update(sizedModel(st), keyType(tea.KeyEnter))
	m = update(m, keyType(tea.KeyEsc))
	m = update(m, keyType(tea.KeyDown))
	restore := failReads(t, db, false)
	m = update(m, keyType(tea.KeyEnter))
	if len(m.history) != 0 || len(m.histTbl.Rows()) != 0 || m.outputOK || m.outputID != 0 || len(m.output.Stdout) != 0 {
		t.Fatalf("beta retained alpha's detail: history=%+v output=%+v", m.history, m.output)
	}
	if !strings.Contains(m.View(), "job: beta") || !strings.Contains(m.View(), "error loading data:") || strings.Contains(m.View(), "no runs yet") {
		t.Fatalf("failed beta query was not shown accurately: %s", m.View())
	}
	restore()
	m = update(m, refreshMessage(false))
	if m.loadErr != nil || m.outputID != betaID || string(m.output.Stdout) != "beta-output" {
		t.Fatalf("beta did not recover: history=%+v output=%+v err=%v", m.history, m.output, m.loadErr)
	}
}

func TestOutputCacheSurvivesRefresh(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			st, db := recoveryStore(t)
			stdout, want := strings.Repeat("cached-output\n", 100), "cached-output"
			if empty {
				stdout, want = "", "(no output)"
			}
			insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, stdout)
			m := update(sizedModel(st), keyType(tea.KeyEnter))
			m = update(m, keyType(tea.KeyTab))
			m = update(m, keyType(tea.KeyDown))
			offset := m.vp.YOffset
			if !empty && offset == 0 {
				t.Fatal("test did not scroll output")
			}
			failReads(t, db, true)
			for _, manual := range []bool{false, true} {
				m = update(m, refreshMessage(manual))
				if !m.outputOK || !strings.Contains(m.View(), want) || strings.Contains(m.View(), "error loading") || m.vp.YOffset != offset {
					t.Fatalf("successful output was reloaded or scroll position reset: offset=%d view=%s", m.vp.YOffset, m.View())
				}
			}
		})
	}
}

func TestOutputRemovedBetweenHistoryAndOutputReads(t *testing.T) {
	st := openStore(t)
	insertRun(t, st, "alpha", time.Now().Add(-time.Minute), true, 0, time.Second, "pruned-output")
	latestID := insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "latest-output")
	m := update(sizedModel(st), keyType(tea.KeyEnter))
	if err := st.Prune(context.Background(), "alpha", 1); err != nil {
		t.Fatal(err)
	}
	m = update(m, keyType(tea.KeyDown))
	if m.outputOK || len(m.output.Stdout) != 0 || !strings.Contains(m.View(), "output unavailable") || strings.Contains(m.View(), "latest-output") {
		t.Fatalf("removed run was shown as empty or another run's output: %s", m.View())
	}
	m = update(m, refreshMessage(false))
	if !m.outputOK || m.outputID != latestID || m.histTbl.Cursor() != 0 || strings.Contains(m.View(), "output unavailable") {
		t.Fatalf("history did not recover after pruning: %s", m.View())
	}
	if err := st.Prune(context.Background(), "alpha", 0); err != nil {
		t.Fatal(err)
	}
	m = update(m, refreshMessage(true))
	if m.outputOK || m.outputID != 0 || len(m.output.Stdout) != 0 || !strings.Contains(m.View(), "no runs yet") {
		t.Fatalf("empty history retained output: %s", m.View())
	}
}

func TestNavigationRefreshesErrorsForActiveView(t *testing.T) {
	for _, fromDetail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fromDetail=%t", fromDetail), func(t *testing.T) {
			st, db := recoveryStore(t)
			insertRun(t, st, "alpha", time.Now().Add(-time.Minute), true, 0, time.Second, "old-output")
			m := sizedModel(st)
			if fromDetail {
				m = update(m, keyType(tea.KeyEnter))
			}
			newID := insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "new-output")
			restore := failReads(t, db, false)
			m = update(m, refreshMessage(false))
			if m.loadErr == nil {
				t.Fatal("test did not inject a metadata failure")
			}
			restore()
			key := tea.KeyEnter
			if fromDetail {
				key = tea.KeyEsc
			}
			m = update(m, keyType(key))
			if m.loadErr != nil || strings.Contains(m.View(), "error loading") {
				t.Fatalf("navigation retained another view's error: %s", m.View())
			}
			if fromDetail && m.latest["alpha"].ID != newID {
				t.Fatal("returning to the list did not refresh its metadata")
			}
			if !fromDetail && m.outputID != newID {
				t.Fatal("entering detail did not load the selected job")
			}
		})
	}
}

func TestMetadataErrorLayout(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(fmt.Sprintf("detail=%t", detail), func(t *testing.T) {
			st, db := recoveryStore(t)
			insertRun(t, st, "alpha", time.Now(), true, 0, time.Second, "output")
			m := sizedModel(st)
			if detail {
				m = update(m, keyType(tea.KeyEnter))
			}
			for _, fullHelp := range []bool{false, true} {
				m.help.ShowAll = fullHelp
				m.layout()
				height := lipgloss.Height(m.View())
				restore := failReads(t, db, false)
				m = update(m, refreshMessage(true))
				if got := lipgloss.Height(m.View()); got != height {
					t.Errorf("error banner changed dashboard height from %d to %d (fullHelp=%t)", height, got, fullHelp)
				}
				for _, width := range []int{0, 1, 20, 120} {
					m = update(m, tea.WindowSizeMsg{Width: width, Height: 30})
					_ = m.View() // tiny widths must not panic while rendering an error
				}
				restore()
				m = update(m, refreshMessage(true))
				if got := lipgloss.Height(m.View()); got != height {
					t.Errorf("recovery did not release banner row: got height %d, want %d", got, height)
				}
			}
		})
	}
}
