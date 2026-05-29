package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/store"
)

// TestMain pins lipgloss to a no-color profile so View() output is stable.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rote.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func insertRun(t *testing.T, st *store.Store, job string, started time.Time, success bool, exit int, dur time.Duration, stdout string) int64 {
	t.Helper()
	id, err := st.Insert(context.Background(), store.Run{
		JobName:    job,
		StartedAt:  started,
		FinishedAt: started.Add(dur),
		Duration:   dur,
		ExitCode:   exit,
		Success:    success,
		Stdout:     []byte(stdout),
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return id
}

func twoJobs() []config.Job {
	return []config.Job{
		{Name: "alpha", Schedule: "@hourly", Command: "true"},
		{Name: "beta", Schedule: "@daily", Command: "true"},
	}
}

func update(m model, msg tea.Msg) model {
	next, _ := m.Update(msg)
	return next.(model)
}

func keyType(tp tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: tp} }

func findRow(rows []table.Row, name string) (table.Row, bool) {
	for _, r := range rows {
		if len(r) > 0 && r[0] == name {
			return r, true
		}
	}
	return nil, false
}

// 1. The initial list has a row per job, with LAST reflecting preset history.
func TestInitListRows(t *testing.T) {
	st := openStore(t)
	now := time.Now()
	insertRun(t, st, "alpha", now.Add(-2*time.Minute), true, 0, 1300*time.Millisecond, "hi")

	m := newModel(context.Background(), twoJobs(), st)
	rows := m.listTbl.Rows()
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	alpha, ok := findRow(rows, "alpha")
	if !ok {
		t.Fatal("missing alpha row")
	}
	if !strings.Contains(alpha[3], "✓") {
		t.Errorf("alpha LAST = %q, want a ✓", alpha[3])
	}
	beta, ok := findRow(rows, "beta")
	if !ok {
		t.Fatal("missing beta row")
	}
	if beta[3] != "-" {
		t.Errorf("beta LAST = %q, want -", beta[3])
	}
}

// 2. Down/up move the list selection.
func TestListNavigation(t *testing.T) {
	st := openStore(t)
	m := newModel(context.Background(), twoJobs(), st)

	if m.listTbl.Cursor() != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.listTbl.Cursor())
	}
	m = update(m, keyType(tea.KeyDown))
	if m.listTbl.Cursor() != 1 {
		t.Errorf("after down cursor = %d, want 1", m.listTbl.Cursor())
	}
	m = update(m, keyType(tea.KeyUp))
	if m.listTbl.Cursor() != 0 {
		t.Errorf("after up cursor = %d, want 0", m.listTbl.Cursor())
	}
}

// 3. Enter opens detail for the selected job; Esc returns to the list.
func TestEnterDetailAndBack(t *testing.T) {
	st := openStore(t)
	now := time.Now()
	insertRun(t, st, "beta", now.Add(-time.Minute), false, 1, 200*time.Millisecond, "boom")
	insertRun(t, st, "beta", now.Add(-2*time.Minute), true, 0, 100*time.Millisecond, "ok")

	m := newModel(context.Background(), twoJobs(), st)
	m = update(m, keyType(tea.KeyDown)) // select beta
	m = update(m, keyType(tea.KeyEnter))

	if m.mode != detailView {
		t.Fatalf("mode = %v, want detailView", m.mode)
	}
	if m.jobIdx != 1 {
		t.Errorf("jobIdx = %d, want 1 (beta)", m.jobIdx)
	}
	if len(m.history) != 2 {
		t.Errorf("history len = %d, want 2", len(m.history))
	}

	m = update(m, keyType(tea.KeyEsc))
	if m.mode != listView {
		t.Errorf("mode = %v, want listView after Esc", m.mode)
	}
}

// 4. A tick refreshes the list and (in detail) the history from the store.
func TestTickRefresh(t *testing.T) {
	st := openStore(t)
	now := time.Now()
	m := newModel(context.Background(), twoJobs(), st)

	// Initially beta has no history.
	if r, _ := findRow(m.listTbl.Rows(), "beta"); r[3] != "-" {
		t.Fatalf("beta LAST = %q, want - initially", r[3])
	}
	// A new run lands, then a tick arrives.
	insertRun(t, st, "beta", now, true, 0, 100*time.Millisecond, "ok")
	m = update(m, tickMsg(time.Now()))
	if r, _ := findRow(m.listTbl.Rows(), "beta"); !strings.Contains(r[3], "✓") {
		t.Errorf("beta LAST = %q, want a ✓ after tick", r[3])
	}

	// In detail, a tick should pick up newly inserted history.
	insertRun(t, st, "alpha", now.Add(-3*time.Minute), true, 0, 100*time.Millisecond, "a1")
	m = newModel(context.Background(), twoJobs(), st)
	m = update(m, keyType(tea.KeyEnter)) // open alpha
	before := len(m.history)
	insertRun(t, st, "alpha", now, true, 0, 100*time.Millisecond, "a2")
	m = update(m, tickMsg(time.Now()))
	if len(m.history) != before+1 {
		t.Errorf("history len = %d, want %d after tick", len(m.history), before+1)
	}
}

// 5. Selecting a history row loads that run's output; changing rows changes it.
func TestOutputFollowsSelection(t *testing.T) {
	st := openStore(t)
	now := time.Now()
	// Newest first after query: a2 (newest), a1.
	insertRun(t, st, "alpha", now.Add(-2*time.Minute), true, 0, 100*time.Millisecond, "out-a1")
	insertRun(t, st, "alpha", now.Add(-1*time.Minute), false, 1, 100*time.Millisecond, "out-a2")

	m := newModel(context.Background(), twoJobs(), st)
	m = update(m, keyType(tea.KeyEnter)) // open alpha; selects newest (out-a2)

	if !m.outputOK {
		t.Fatalf("expected output loaded, got outputOK=false")
	}
	if m.outputID != m.history[0].ID {
		t.Errorf("outputID = %d, want %d (newest)", m.outputID, m.history[0].ID)
	}
	if got := string(m.output.Stdout); got != "out-a2" {
		t.Errorf("loaded stdout = %q, want out-a2", got)
	}

	// Move to the older run; output must follow.
	m = update(m, keyType(tea.KeyDown))
	if m.histTbl.Cursor() != 1 {
		t.Fatalf("history cursor = %d, want 1", m.histTbl.Cursor())
	}
	if m.outputID != m.history[1].ID {
		t.Errorf("outputID = %d, want %d (older)", m.outputID, m.history[1].ID)
	}
	if got := string(m.output.Stdout); got != "out-a1" {
		t.Errorf("loaded stdout = %q, want out-a1", got)
	}
}

// 6. WindowSizeMsg sets sizes and never panics, including at tiny sizes.
func TestWindowSize(t *testing.T) {
	st := openStore(t)
	insertRun(t, st, "alpha", time.Now().Add(-time.Minute), true, 0, 100*time.Millisecond, "x")
	m := newModel(context.Background(), twoJobs(), st)

	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.width != 100 || m.height != 40 {
		t.Errorf("size = %dx%d, want 100x40", m.width, m.height)
	}
	_ = m.View() // must not panic

	// Detail view at a normal size.
	m = update(m, keyType(tea.KeyEnter))
	_ = m.View()

	// Degenerate tiny sizes must not panic in either view.
	m = update(m, tea.WindowSizeMsg{Width: 1, Height: 1})
	_ = m.View()
	m = update(m, keyType(tea.KeyEsc))
	m = update(m, tea.WindowSizeMsg{Width: 0, Height: 0})
	_ = m.View()
}
