// Package xirr is the annualised return of dated cash flows.
package xirr

import (
	"math"
	"time"
)

// Flow is money moving on a date: negative paid in (invested), positive received (or the value today).
type Flow struct {
	At     time.Time
	Amount float64
}

// minSpan is the shortest history worth annualising; under it the rate is noise (a week's 1% reads as 68% a year).
const minSpan = 30 * 24 * time.Hour

// Rate returns the annual rate r (0.12 = 12%) that zeroes the net present value, and false when none is meaningful:
// flows of one sign only, under 30 days of history, or no root. Actual/365, as spreadsheets do.
func Rate(flows []Flow) (float64, bool) {
	var pos, neg bool
	first, last := flows[0].At, flows[0].At
	for _, f := range flows {
		pos, neg = pos || f.Amount > 0, neg || f.Amount < 0
		if f.At.Before(first) {
			first = f.At
		}
		if f.At.After(last) {
			last = f.At
		}
	}
	if !pos || !neg || last.Sub(first) < minSpan {
		return 0, false
	}
	npv := func(r float64) float64 {
		var s float64
		for _, f := range flows {
			s += f.Amount / math.Pow(1+r, f.At.Sub(first).Hours()/24/365)
		}
		return s
	}
	// npv falls as r rises when the money in comes first, so bisect a bracket; no sign change means no root in range.
	lo, hi := -0.99, 100.0
	flo, fhi := npv(lo), npv(hi)
	if math.IsNaN(flo) || math.IsNaN(fhi) || flo*fhi > 0 {
		return 0, false
	}
	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		fm := npv(mid)
		if fm == 0 || hi-lo < 1e-9 {
			return mid, true
		}
		if (fm > 0) == (flo > 0) {
			lo, flo = mid, fm
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2, true
}
