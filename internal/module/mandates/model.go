// Package mandates holds the payments you must make (insurance, annual fees, tax, school fees...) and works out when they fall due.
package mandates

import (
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/vendors"
)

const Collection = "mandates"

// frequencies maps each frequency to its step in months; one-off has none.
var frequencies = map[string]int{"monthly": 1, "quarterly": 3, "yearly": 12, "one-off": 0}

type Mandate struct {
	ID          bson.ObjectID  `bson:"_id" json:"id"`
	Name        string         `bson:"name" json:"name"`
	AmountPaise int64          `bson:"amount_paise" json:"amount_paise"`
	Frequency   string         `bson:"frequency" json:"frequency"`
	NextDue     time.Time      `bson:"next_due" json:"-"` // IST midnight of a due date; recurring ones roll forward from it
	AccountID   *bson.ObjectID `bson:"account_id,omitempty" json:"account_id,omitempty"`
	CategoryID  *bson.ObjectID `bson:"category_id,omitempty" json:"category_id,omitempty"`
	CreatedAt   time.Time      `bson:"created_at" json:"created_at"`
}

// Input is the body of a create or update; NextDue is YYYY-MM-DD.
type Input struct {
	Name        string `json:"name"`
	AmountPaise int64  `json:"amount_paise"`
	Frequency   string `json:"frequency"`
	NextDue     string `json:"next_due"`
	AccountID   string `json:"account_id"`
	CategoryID  string `json:"category_id"`
}

const day = "2006-01-02"

// maxPaise is a sanity cap (10 crore rupees), as for transactions.
const maxPaise = 10_000_000_000

// Parse validates the input and returns the mandate it describes; msg is non-empty when the input is bad.
func (in Input) Parse() (m Mandate, msg string) {
	_, freqOK := frequencies[in.Frequency]
	due, err := time.ParseInLocation(day, in.NextDue, ist.Loc)
	switch {
	case strings.TrimSpace(in.Name) == "":
		return m, "name required"
	case in.AmountPaise <= 0 || in.AmountPaise > maxPaise:
		return m, "amount must be above zero"
	case !freqOK:
		return m, "frequency must be monthly, quarterly, yearly or one-off"
	case err != nil:
		return m, "next_due must be a date like 2026-12-31"
	}
	m = Mandate{Name: strings.TrimSpace(in.Name), AmountPaise: in.AmountPaise, Frequency: in.Frequency, NextDue: due}
	for _, f := range []struct {
		hex string
		dst **bson.ObjectID
	}{{in.AccountID, &m.AccountID}, {in.CategoryID, &m.CategoryID}} {
		if f.hex == "" {
			continue
		}
		id, err := bson.ObjectIDFromHex(f.hex)
		if err != nil {
			return Mandate{}, "account_id or category_id is not a valid id"
		}
		*f.dst = &id
	}
	return m, ""
}

// due is the n-th due date counted from first, keeping first's day of month (clamped in short months: 31 Jan, 28 Feb, 31 Mar).
func due(first time.Time, step, n int) time.Time {
	f := first.In(ist.Loc)
	m := time.Date(f.Year(), f.Month()+time.Month(step*n), 1, 0, 0, 0, 0, ist.Loc)
	return time.Date(m.Year(), m.Month(), min(f.Day(), m.AddDate(0, 1, -1).Day()), 0, 0, 0, 0, ist.Loc)
}

// Occurrences lists the due dates in [from, to). A recurring mandate whose date has passed is assumed paid and rolled forward;
// a one-off that has passed is dropped.
func (m Mandate) Occurrences(from, to time.Time) []time.Time {
	step := frequencies[m.Frequency]
	var out []time.Time
	for n := 0; ; n++ {
		d := due(m.NextDue, step, n)
		if !d.Before(to) {
			return out
		}
		if !d.Before(from) {
			out = append(out, d)
		}
		if step == 0 {
			return out
		}
	}
}

type View struct {
	Mandate
	NextDue string `json:"next_due"`       // the date entered, YYYY-MM-DD
	Next    string `json:"next,omitempty"` // the next date it falls due from today; empty when a one-off has passed
	Days    int    `json:"days"`
	// SetAsidePaise is what to put away each month to meet it: the amount for a monthly one, a twelfth of a yearly one, and so on.
	SetAsidePaise int64 `json:"set_aside_paise"`
}

type Occ struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Paise int64  `json:"paise"`
	Date  string `json:"date"`
	Days  int    `json:"days"`
}

type Plan struct {
	Items    []View `json:"items"`
	Calendar []Occ  `json:"calendar"` // everything falling due in the next 12 months, soonest first
	Next30   int64  `json:"next30_paise"`
	Next90   int64  `json:"next90_paise"`
	Year     int64  `json:"year_paise"`
	SetAside int64  `json:"set_aside_paise"` // per month, across all mandates
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// BuildPlan works out every mandate's next date, the 12-month calendar and the monthly set-aside, as of now.
func BuildPlan(ms []Mandate, now time.Time) Plan {
	today := ist.Midnight(now)
	end := today.AddDate(1, 0, 0)
	p := Plan{Items: make([]View, 0, len(ms)), Calendar: []Occ{}}
	for _, m := range ms {
		v := View{Mandate: m, NextDue: ist.Midnight(m.NextDue).Format(day)}
		for i, d := range m.Occurrences(today, end) {
			days := int(math.Round(d.Sub(today).Hours() / 24))
			p.Calendar = append(p.Calendar, Occ{m.ID.Hex(), m.Name, m.AmountPaise, d.Format(day), days})
			p.Year += m.AmountPaise
			if days < 30 {
				p.Next30 += m.AmountPaise
			}
			if days < 90 {
				p.Next90 += m.AmountPaise
			}
			if i == 0 {
				v.Next, v.Days = d.Format(day), days
			}
		}
		switch step := frequencies[m.Frequency]; {
		case step > 0:
			v.SetAsidePaise = ceilDiv(m.AmountPaise, int64(step))
		case v.Next != "":
			v.SetAsidePaise = ceilDiv(m.AmountPaise, max(1, int64(v.Days+29)/30))
		}
		p.SetAside += v.SetAsidePaise
		p.Items = append(p.Items, v)
	}
	sort.SliceStable(p.Calendar, func(i, j int) bool { return p.Calendar[i].Date < p.Calendar[j].Date })
	// Soonest first; one-offs that have passed (no next date) go last.
	sort.SliceStable(p.Items, func(i, j int) bool {
		return p.Items[i].Next != "" && (p.Items[j].Next == "" || p.Items[i].Next < p.Items[j].Next)
	})
	return p
}

// Sample is one past payment, the input to Detect.
type Sample struct {
	Label string
	Paise int64
	At    time.Time
}

type Suggestion struct {
	Name        string `json:"name"`
	AmountPaise int64  `json:"amount_paise"`
	Frequency   string `json:"frequency"`
	NextDue     string `json:"next_due"`
	Count       int    `json:"count"`
}

// cadences are the intervals worth suggesting: the days between payments, how far each gap may stray, and how many payments prove it.
var cadences = []struct {
	freq string
	days int
	tol  int
	n    int
}{{"monthly", 30, 5, 3}, {"quarterly", 91, 10, 3}, {"yearly", 365, 15, 2}}

// Detect finds payments to the same label at a regular interval for a similar amount (within 15% of the latest), skipping names already
// in known and ones that have lapsed (the next date is over a week past). Pure.
func Detect(samples []Sample, known []string, now time.Time) []Suggestion {
	groups := map[string][]Sample{}
	for _, s := range samples {
		if k := vendors.Normalize(s.Label); k != "" {
			groups[k] = append(groups[k], s)
		}
	}
	today := ist.Midnight(now)
	out := []Suggestion{}
	for key, g := range groups {
		if isKnown(key, known) {
			continue
		}
		sort.Slice(g, func(i, j int) bool { return g[i].At.Before(g[j].At) })
		last := g[len(g)-1]
		for _, c := range cadences {
			if len(g) < c.n || !regular(g, c.days, c.tol, last.Paise) {
				continue
			}
			if due(last.At, frequencies[c.freq], 1).Before(today.AddDate(0, 0, -7)) {
				break
			}
			next := Mandate{NextDue: ist.Midnight(last.At), Frequency: c.freq}.Occurrences(today, today.AddDate(2, 0, 0))
			out = append(out, Suggestion{Name: last.Label, AmountPaise: last.Paise, Frequency: c.freq, NextDue: next[0].Format(day), Count: len(g)})
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextDue < out[j].NextDue })
	return out
}

func isKnown(key string, known []string) bool {
	for _, k := range known {
		if k = vendors.Normalize(k); k != "" && (strings.Contains(key, k) || strings.Contains(k, key)) {
			return true
		}
	}
	return false
}

// regular reports whether every gap between payments is about `days` apart and every amount is within 15% of latest.
func regular(g []Sample, days, tol int, latest int64) bool {
	for i, s := range g {
		if math.Abs(float64(s.Paise-latest)) > 0.15*float64(latest) {
			return false
		}
		if i > 0 {
			gap := int(math.Round(s.At.Sub(g[i-1].At).Hours() / 24))
			if gap < days-tol || gap > days+tol {
				return false
			}
		}
	}
	return true
}
