// Package ist holds the one time zone the app reasons in, plus day helpers.
package ist

import "time"

var Loc = time.FixedZone("IST", 5*3600+1800)

// Midnight is the start of t's day in IST.
func Midnight(t time.Time) time.Time {
	t = t.In(Loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Loc)
}

// DaysUntil is the number of days until day-of-month `day` next arrives (1..31, never 0: today counts as passed).
func DaysUntil(day int, now time.Time) int {
	now = now.In(Loc)
	today := Midnight(now)
	on := func(month int) time.Time {
		last := time.Date(now.Year(), time.Month(month)+1, 0, 0, 0, 0, 0, Loc).Day()
		d := day
		if d > last {
			d = last
		}
		return time.Date(now.Year(), time.Month(month), d, 0, 0, 0, 0, Loc)
	}
	next := on(int(now.Month()))
	if !next.After(today) {
		next = on(int(now.Month()) + 1)
	}
	return int(next.Sub(today).Hours()/24 + 0.5)
}
