package core

import (
	"math"
	"time"
)

// Day returns local midnight of t's calendar date in loc.
func Day(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// AddMonthsClamp adds months to a date and clamps the day to the end of the
// target month: 30 Nov + 3 months = 28 Feb, not 2 Mar as time.AddDate gives.
func AddMonthsClamp(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(months), 1, 0, 0, 0, 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, t.Location())
}

// DaysBetween returns whole calendar days from a to b (negative if b is earlier).
func DaysBetween(a, b time.Time) int {
	a = time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	b = time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(math.Round(b.Sub(a).Hours() / 24))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// NearestWorkingDay moves d off weekends and holidays, preferring the
// earlier day on a tie. It returns d unchanged when d is a working day.
func NearestWorkingDay(d time.Time, isHoliday func(time.Time) bool) time.Time {
	working := func(t time.Time) bool {
		wd := t.Weekday()
		return wd != time.Saturday && wd != time.Sunday && !isHoliday(t)
	}
	if working(d) {
		return d
	}
	for i := 1; i <= 14; i++ {
		if b := d.AddDate(0, 0, -i); working(b) {
			return b
		}
		if a := d.AddDate(0, 0, i); working(a) {
			return a
		}
	}
	return d
}
