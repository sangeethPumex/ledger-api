package insights

import (
	"math"
	"slices"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// cadences the detector knows: the gap in days, how far a gap may stray, how many charges prove it, and how one charge maps to a month.
var cadences = []struct {
	name    string
	days    float64
	tol     float64
	min     int
	perMo   float64
	advance func(time.Time) time.Time
}{
	{"weekly", 7, 1.5, 4, 52.0 / 12, func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }},
	{"monthly", 30.4, 4, 3, 1, func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }},
	{"quarterly", 91, 8, 3, 1.0 / 3, func(t time.Time) time.Time { return t.AddDate(0, 3, 0) }},
	{"yearly", 365, 15, 2, 1.0 / 12, func(t time.Time) time.Time { return t.AddDate(1, 0, 0) }},
}

type Recurring struct {
	Name         string         `json:"name"`
	VendorID     *bson.ObjectID `json:"vendor_id,omitempty"`
	CategoryID   *bson.ObjectID `json:"category_id,omitempty"`
	Cadence      string         `json:"cadence"`
	AmountPaise  int64          `json:"amount_paise"`  // the usual charge
	MonthlyPaise int64          `json:"monthly_paise"` // the usual charge spread over a month
	Count        int            `json:"count"`
	LastAt       time.Time      `json:"last_at"`
	NextAt       time.Time      `json:"next_at"`
	DaysUntil    int            `json:"days_until"` // negative once the charge is late
	Status       string         `json:"status"`     // active, or lapsed when a whole cycle has passed with no charge (cancelled, or forgotten)
}

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// detectRecurring finds vendors charged at a steady interval for a steady amount. Pure.
// The median gap picks the cadence; then 70% of the gaps must sit near it and 70% of the amounts within 15% of the usual one, which lets
// a missed month or a price bump through but rejects a vendor you merely visit often (Swiggy) or a one-off.
func detectRecurring(all []vendorSeries, now time.Time) []Recurring {
	out := []Recurring{}
	for _, s := range all {
		n := len(s.Txns)
		if n < 2 {
			continue
		}
		gaps := make([]int64, 0, n-1)
		amts := make([]int64, n)
		for i, p := range s.Txns {
			amts[i] = p.Paise
			if i > 0 {
				gaps = append(gaps, int64(math.Round(p.At.Sub(s.Txns[i-1].At).Hours()/24)))
			}
		}
		med := float64(median(gaps))
		usual := median(amts)
		for _, c := range cadences {
			if n < c.min || math.Abs(med-c.days) > c.tol {
				continue
			}
			near, same := 0, 0
			for _, g := range gaps {
				if math.Abs(float64(g)-c.days) <= c.tol*1.5 {
					near++
				}
			}
			for _, a := range amts {
				if math.Abs(float64(a-usual)) <= 0.15*float64(usual) {
					same++
				}
			}
			if float64(near) < 0.7*float64(len(gaps)) || float64(same) < 0.7*float64(n) {
				continue
			}
			last := s.Txns[n-1].At
			next := c.advance(last)
			status := "active"
			if now.After(c.advance(next)) { // a full extra cycle has passed without a charge
				status = "lapsed"
			}
			out = append(out, Recurring{
				Name: s.Name, VendorID: s.VendorID, CategoryID: s.CategoryID, Cadence: c.name, AmountPaise: usual,
				MonthlyPaise: int64(math.Round(float64(usual) * c.perMo)), Count: n, LastAt: last, NextAt: next,
				DaysUntil: int(math.Round(next.Sub(now).Hours() / 24)), Status: status,
			})
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Status == "active") != (out[j].Status == "active") {
			return out[i].Status == "active"
		}
		return out[i].NextAt.Before(out[j].NextAt)
	})
	return out
}
