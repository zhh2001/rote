package main

import (
	"fmt"
	"time"
)

// statusSymbol renders a run's success as a compact glyph.
func statusSymbol(success bool) string {
	if success {
		return "✓"
	}
	return "✗"
}

// shortDur formats a duration compactly using a single unit, e.g. "300ms",
// "5s", "12m", "3h", "2d". The sign is dropped.
func shortDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}
