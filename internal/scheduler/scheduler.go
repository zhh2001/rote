package scheduler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
)

// Schedule reports when a job should next run.
type Schedule interface {
	// Next returns the first activation strictly after the given time, or the
	// zero time.Time if the schedule will never fire again.
	Next(after time.Time) time.Time
}

// Parse turns a schedule expression into a Schedule. It accepts a small set of
// human-friendly forms (which are normalized internally) as well as standard
// five-field cron expressions and cron descriptors such as "@every 30s" or
// "@hourly". Schedule semantics use time.Local.
func Parse(expr string) (Schedule, error) {
	spec, err := humanize(expr)
	if err != nil {
		return nil, err
	}
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("scheduler: invalid schedule %q: %w", expr, err)
	}
	return sched, nil
}

// NextN returns the next n activation times strictly after the given instant,
// in strictly ascending order. It stops early (returning fewer than n entries)
// once the schedule reports that it will never fire again.
func NextN(s Schedule, after time.Time, n int) []time.Time {
	if n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, n)
	t := after
	for i := 0; i < n; i++ {
		next := s.Next(t)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}

// weekdays maps lower-case weekday names to their cron day-of-week number,
// where Sunday is 0 (the standard cron convention).
var weekdays = map[string]int{
	"sunday":    0,
	"monday":    1,
	"tuesday":   2,
	"wednesday": 3,
	"thursday":  4,
	"friday":    5,
	"saturday":  6,
}

// durationExpr matches an interval built only from lower-case h/m/s units,
// e.g. "5m", "90s", "2h", "1h30m".
var durationExpr = regexp.MustCompile(`^([0-9]+(h|m|s))+$`)

// humanize normalizes a schedule expression into a form cron.ParseStandard
// accepts. Keywords and weekday names are case-insensitive, and surrounding or
// repeated internal whitespace is tolerated. Expressions that are already valid
// cron specs are passed through unchanged.
func humanize(expr string) (string, error) {
	fields := strings.Fields(expr)
	if len(fields) == 0 {
		return "", fmt.Errorf("scheduler: empty schedule expression")
	}
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(f)
	}

	switch {
	case len(fields) == 1:
		return humanizeDescriptor(fields, lower[0]), nil
	case lower[0] == "every":
		return humanizeEvery(fields, lower, expr)
	case lower[0] == "daily" && len(fields) == 3 && lower[1] == "at":
		return dailySpec(fields[2], expr)
	case lower[0] == "weekly" && len(fields) == 5 && lower[1] == "on" && lower[3] == "at":
		return weekdaySpec(lower[2], fields[4], expr)
	default:
		return strings.Join(fields, " "), nil
	}
}

// humanizeDescriptor maps a single-word keyword to its cron descriptor, and
// otherwise passes the token through unchanged (e.g. a one-field cron spec or a
// raw "@hourly").
func humanizeDescriptor(fields []string, word string) string {
	switch word {
	case "hourly":
		return "@hourly"
	case "daily":
		return "@daily"
	case "weekly":
		return "@weekly"
	case "monthly":
		return "@monthly"
	default:
		return strings.Join(fields, " ")
	}
}

// humanizeEvery handles the "every ..." forms: "every <duration>" and
// "every <weekday> at HH:MM".
func humanizeEvery(fields, lower []string, expr string) (string, error) {
	switch {
	case len(fields) == 2:
		return everyDurationSpec(fields[1], expr)
	case len(fields) == 4 && lower[2] == "at":
		return weekdaySpec(lower[1], fields[3], expr)
	default:
		return "", fmt.Errorf("scheduler: invalid 'every' expression %q: want 'every <duration>' or 'every <weekday> at HH:MM'", expr)
	}
}

// everyDurationSpec converts "every <duration>" into an "@every" descriptor,
// accepting only lower-case h/m/s units.
func everyDurationSpec(dur, expr string) (string, error) {
	if !durationExpr.MatchString(dur) {
		return "", fmt.Errorf("scheduler: invalid interval %q in %q: want a duration like 5m, 90s or 1h30m", dur, expr)
	}
	if _, err := time.ParseDuration(dur); err != nil {
		return "", fmt.Errorf("scheduler: invalid interval %q in %q: %w", dur, expr, err)
	}
	return "@every " + dur, nil
}

// dailySpec converts "daily at HH:MM" into a cron spec.
func dailySpec(clock, expr string) (string, error) {
	min, hour, err := parseClock(clock, expr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d %d * * *", min, hour), nil
}

// weekdaySpec builds a cron spec that fires at the given clock time on a single
// weekday.
func weekdaySpec(name, clock, expr string) (string, error) {
	dow, ok := weekdays[name]
	if !ok {
		return "", fmt.Errorf("scheduler: unknown weekday %q in %q", name, expr)
	}
	min, hour, err := parseClock(clock, expr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d %d * * %d", min, hour, dow), nil
}

// parseClock parses a 24-hour "H:MM" or "HH:MM" time, returning the minute and
// hour fields. The minute component must always be two digits.
func parseClock(s, expr string) (min, hour int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 2 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("scheduler: invalid time %q in %q: want HH:MM (24-hour)", s, expr)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("scheduler: invalid time %q in %q: not a number", s, expr)
	}
	min, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("scheduler: invalid time %q in %q: not a number", s, expr)
	}
	if hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("scheduler: invalid hour %d in %q: want 0-23", hour, expr)
	}
	if min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("scheduler: invalid minute %d in %q: want 0-59", min, expr)
	}
	return min, hour, nil
}
