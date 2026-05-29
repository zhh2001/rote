package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/zhh2001/rote/internal/scheduler"
	"github.com/zhh2001/rote/internal/store"
)

// statusSymbol renders run success as a compact glyph.
func statusSymbol(success bool) string {
	if success {
		return "✓"
	}
	return "✗"
}

// formatCountdown renders a forward-looking duration with two units, e.g.
// "4m12s", "1h2m", "5s", "2d3h".
func formatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	default:
		return fmt.Sprintf("%dd%dh", int(d/(24*time.Hour)), int((d%(24*time.Hour))/time.Hour))
	}
}

// formatNext renders the countdown to a schedule's next activation, or "never".
func formatNext(sched scheduler.Schedule, now time.Time) string {
	if sched == nil {
		return "never"
	}
	next := sched.Next(now)
	if next.IsZero() {
		return "never"
	}
	return "in " + formatCountdown(next.Sub(now))
}

// relAge renders an elapsed duration with a single coarse unit, e.g. "2m".
func relAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// durShort renders a run duration, e.g. "300ms", "1.3s", "2m5s".
func durShort(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	}
}

// formatLast renders the LAST column: a glyph, relative time, and duration, or
// "-" when there is no history.
func formatLast(meta store.RunMeta, ok bool, now time.Time) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%s %s ago (%s)", statusSymbol(meta.Success), relAge(now.Sub(meta.StartedAt)), durShort(meta.Duration))
}

// renderOutput formats a run's captured streams for the detail viewport.
func renderOutput(o store.Output) string {
	var b strings.Builder
	writeSection(&b, "stdout", o.Stdout, o.StdoutTruncated)
	b.WriteString("\n")
	writeSection(&b, "stderr", o.Stderr, o.StderrTruncated)
	return b.String()
}

func writeSection(b *strings.Builder, label string, data []byte, truncated bool) {
	b.WriteString(label)
	if truncated {
		b.WriteString(" (truncated)")
	}
	b.WriteString("\n")
	if len(data) == 0 {
		b.WriteString("(no output)\n")
		return
	}
	b.Write(data)
	if data[len(data)-1] != '\n' {
		b.WriteString("\n")
	}
}
