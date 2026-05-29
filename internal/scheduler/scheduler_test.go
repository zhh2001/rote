package scheduler

import (
	"os"
	"testing"
	"time"
)

// TestMain pins the process time zone to UTC so that schedule semantics (which
// use time.Local) are deterministic regardless of the host machine's time zone.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

// at builds a UTC time with zero seconds and nanoseconds.
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

// ats builds a UTC time including seconds.
func ats(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

func mustParse(t *testing.T, expr string) Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) returned unexpected error: %v", expr, err)
	}
	return s
}

func assertNext(t *testing.T, expr string, ref, want time.Time) {
	t.Helper()
	got := mustParse(t, expr).Next(ref)
	if !got.Equal(want) {
		t.Errorf("Parse(%q).Next(%s) = %s, want %s", expr, ref, got, want)
	}
}

// 1. Every humanized form produces the expected exact next time.
func TestHumanizeForms(t *testing.T) {
	ref := ats(2026, 5, 29, 10, 0, 0) // Friday
	cases := []struct {
		expr string
		want time.Time
	}{
		{"every 5m", ats(2026, 5, 29, 10, 5, 0)},
		{"every 90s", ats(2026, 5, 29, 10, 1, 30)},
		{"every 2h", ats(2026, 5, 29, 12, 0, 0)},
		{"every 1h30m", ats(2026, 5, 29, 11, 30, 0)},
		{"hourly", ats(2026, 5, 29, 11, 0, 0)},
		{"daily", at(2026, 5, 30, 0, 0)},
		{"weekly", at(2026, 5, 31, 0, 0)}, // next Sunday
		{"monthly", at(2026, 6, 1, 0, 0)}, // first of next month
	}
	for _, c := range cases {
		assertNext(t, c.expr, ref, c.want)
	}
}

// Case-insensitivity and tolerated whitespace.
func TestHumanizeCaseAndSpacing(t *testing.T) {
	ref := at(2026, 5, 29, 2, 0)
	want := at(2026, 5, 29, 3, 0)
	for _, expr := range []string{
		"daily at 03:00",
		"DAILY AT 03:00",
		"  daily   at   03:00  ",
		"Daily At 3:00",
	} {
		assertNext(t, expr, ref, want)
	}
}

// 2. "daily at 03:00" before and after the trigger on the same day.
func TestDailyAt(t *testing.T) {
	assertNext(t, "daily at 03:00", at(2026, 5, 29, 2, 0), at(2026, 5, 29, 3, 0))
	assertNext(t, "daily at 03:00", at(2026, 5, 29, 4, 0), at(2026, 5, 30, 3, 0))
}

// 3. "every monday at 09:00": Monday before/after the trigger, and a non-Monday.
func TestWeekdayAt(t *testing.T) {
	// 2026-06-01 is a Monday.
	assertNext(t, "every monday at 09:00", at(2026, 6, 1, 8, 0), at(2026, 6, 1, 9, 0))
	assertNext(t, "every monday at 09:00", at(2026, 6, 1, 10, 0), at(2026, 6, 8, 9, 0))
	assertNext(t, "every monday at 09:00", at(2026, 5, 29, 10, 0), at(2026, 6, 1, 9, 0)) // Friday

	// "weekly on monday at 09:00" must behave identically.
	assertNext(t, "weekly on monday at 09:00", at(2026, 5, 29, 10, 0), at(2026, 6, 1, 9, 0))

	// Every weekday name is recognized. From Friday 2026-05-29 10:00, the next
	// occurrence of each weekday at 09:00 is:
	days := []struct {
		name string
		want time.Time
	}{
		{"saturday", at(2026, 5, 30, 9, 0)},
		{"sunday", at(2026, 5, 31, 9, 0)},
		{"monday", at(2026, 6, 1, 9, 0)},
		{"tuesday", at(2026, 6, 2, 9, 0)},
		{"wednesday", at(2026, 6, 3, 9, 0)},
		{"thursday", at(2026, 6, 4, 9, 0)},
		{"friday", at(2026, 6, 5, 9, 0)}, // today's 09:00 already passed
	}
	ref := at(2026, 5, 29, 10, 0) // Friday
	for _, d := range days {
		assertNext(t, "every "+d.name+" at 09:00", ref, d.want)
	}
}

// 4. Wraparound across midnight and across the week.
func TestWraparound(t *testing.T) {
	// Across midnight.
	assertNext(t, "daily at 03:00", at(2026, 5, 29, 23, 30), at(2026, 5, 30, 3, 0))
	// From Sunday into the next week's Monday.
	assertNext(t, "every monday at 09:00", at(2026, 5, 31, 12, 0), at(2026, 6, 1, 9, 0))
	// From Saturday across the week boundary.
	assertNext(t, "every monday at 09:00", at(2026, 5, 30, 9, 0), at(2026, 6, 1, 9, 0))
}

// 5. Sub-minute intervals.
func TestSubMinute(t *testing.T) {
	ref := ats(2026, 5, 29, 10, 0, 0)
	assertNext(t, "@every 30s", ref, ats(2026, 5, 29, 10, 0, 30))
	assertNext(t, "every 30s", ref, ats(2026, 5, 29, 10, 0, 30))
}

// 6. Raw cron expressions pass through unchanged.
func TestRawCron(t *testing.T) {
	assertNext(t, "*/15 * * * *", ats(2026, 5, 29, 10, 7, 0), at(2026, 5, 29, 10, 15))
	assertNext(t, "30 14 * * *", at(2026, 5, 29, 10, 0), at(2026, 5, 29, 14, 30))
	assertNext(t, "0 0 1 1 *", at(2026, 5, 29, 10, 0), at(2027, 1, 1, 0, 0))
}

// 7. Invalid inputs all return an error.
func TestParseInvalid(t *testing.T) {
	bad := []string{
		"",
		"   ",
		"garbage",
		"every",
		"every 5",
		"every 5x",
		"every 5M",
		"every 100ms",
		"every monday",
		"every funday at 09:00",
		"daily at",
		"daily at 25:00",
		"daily at 12:60",
		"daily at 9:5",
		"weekly on funday at 09:00",
		"weekly on monday",
		"*/15 * * *",
		"60 * * * *",
	}
	for _, expr := range bad {
		if s, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) = %v, want error", expr, s)
		}
	}
}

// "Never fires" schedules yield a zero time and stop NextN early.
func TestNeverFires(t *testing.T) {
	s := mustParse(t, "0 0 30 2 *") // February 30th never occurs
	ref := at(2026, 5, 29, 10, 0)
	if got := s.Next(ref); !got.IsZero() {
		t.Errorf("Next on impossible schedule = %s, want zero time", got)
	}
	if got := NextN(s, ref, 5); len(got) != 0 {
		t.Errorf("NextN on impossible schedule = %v, want empty", got)
	}
}

// 8. NextN: correct count, strictly ascending, and consistent with Next.
func TestNextN(t *testing.T) {
	s := mustParse(t, "daily at 03:00")
	ref := at(2026, 5, 29, 4, 0)
	got := NextN(s, ref, 3)

	want := []time.Time{
		at(2026, 5, 30, 3, 0),
		at(2026, 5, 31, 3, 0),
		at(2026, 6, 1, 3, 0),
	}
	if len(got) != len(want) {
		t.Fatalf("NextN count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("NextN[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	for i := 1; i < len(got); i++ {
		if !got[i].After(got[i-1]) {
			t.Errorf("NextN not strictly ascending at %d: %s !> %s", i, got[i], got[i-1])
		}
	}

	// Consistency with repeated Next calls.
	cron := mustParse(t, "*/15 * * * *")
	cref := ats(2026, 5, 29, 10, 7, 0)
	const n = 6
	manual := make([]time.Time, 0, n)
	cur := cref
	for i := 0; i < n; i++ {
		cur = cron.Next(cur)
		manual = append(manual, cur)
	}
	via := NextN(cron, cref, n)
	if len(via) != len(manual) {
		t.Fatalf("NextN count = %d, want %d", len(via), len(manual))
	}
	for i := range manual {
		if !via[i].Equal(manual[i]) {
			t.Errorf("NextN[%d] = %s, want %s (from repeated Next)", i, via[i], manual[i])
		}
	}

	// Non-positive counts yield nothing.
	if got := NextN(s, ref, 0); len(got) != 0 {
		t.Errorf("NextN(n=0) = %v, want empty", got)
	}
	if got := NextN(s, ref, -1); len(got) != 0 {
		t.Errorf("NextN(n=-1) = %v, want empty", got)
	}
}
