package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/store"
)

// fakeSched is a scheduler.Schedule returning a fixed next time.
type fakeSched struct{ next time.Time }

func (f fakeSched) Next(time.Time) time.Time { return f.next }

func TestStatusSymbol(t *testing.T) {
	if statusSymbol(true) != "✓" {
		t.Errorf("success symbol = %q, want ✓", statusSymbol(true))
	}
	if statusSymbol(false) != "✗" {
		t.Errorf("failure symbol = %q, want ✗", statusSymbol(false))
	}
}

func TestFormatCountdown(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{4*time.Minute + 12*time.Second, "4m12s"},
		{90 * time.Minute, "1h30m"},
		{49 * time.Hour, "2d1h"},
		{-3 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := formatCountdown(c.d); got != c.want {
			t.Errorf("formatCountdown(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFormatNext(t *testing.T) {
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	if got := formatNext(nil, now); got != "never" {
		t.Errorf("nil schedule = %q, want never", got)
	}
	if got := formatNext(fakeSched{}, now); got != "never" {
		t.Errorf("zero next = %q, want never", got)
	}
	if got := formatNext(fakeSched{now.Add(5 * time.Minute)}, now); got != "in 5m0s" {
		t.Errorf("future = %q, want \"in 5m0s\"", got)
	}
}

func TestRelAgeAndDurShort(t *testing.T) {
	if got := relAge(2 * time.Minute); got != "2m" {
		t.Errorf("relAge(2m) = %q", got)
	}
	if got := relAge(90 * time.Second); got != "1m" {
		t.Errorf("relAge(90s) = %q", got)
	}
	if got := durShort(1300 * time.Millisecond); got != "1.3s" {
		t.Errorf("durShort(1.3s) = %q", got)
	}
	if got := durShort(300 * time.Millisecond); got != "300ms" {
		t.Errorf("durShort(300ms) = %q", got)
	}
	if got := durShort(0); got != "0s" {
		t.Errorf("durShort(0) = %q", got)
	}
}

func TestFormatLast(t *testing.T) {
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	if got := formatLast(store.RunMeta{}, false, now); got != "-" {
		t.Errorf("no history = %q, want -", got)
	}

	meta := store.RunMeta{Success: true, StartedAt: now.Add(-2 * time.Minute), Duration: 1300 * time.Millisecond}
	if got := formatLast(meta, true, now); got != "✓ 2m ago (1.3s)" {
		t.Errorf("formatLast = %q, want \"✓ 2m ago (1.3s)\"", got)
	}

	fail := store.RunMeta{Success: false, StartedAt: now.Add(-30 * time.Second), Duration: 500 * time.Millisecond}
	if got := formatLast(fail, true, now); got != "✗ 30s ago (500ms)" {
		t.Errorf("formatLast(fail) = %q", got)
	}
}

func TestRenderOutput(t *testing.T) {
	out := renderOutput(store.Output{
		Stdout:          []byte("hello\n"),
		Stderr:          nil,
		StdoutTruncated: true,
	})
	for _, want := range []string{"stdout (truncated)", "hello", "stderr", "(no output)"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderOutput missing %q:\n%s", want, out)
		}
	}
}
