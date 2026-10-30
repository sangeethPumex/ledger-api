package portfolio

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

// Entry is a dividend or interest credit typed in by hand. Rupees.
type Entry struct {
	ID     bson.ObjectID `json:"id" bson:"_id"`
	Date   string        `json:"date" bson:"date"` // YYYY-MM-DD
	Kind   string        `json:"kind" bson:"kind"` // dividend | interest
	Source string        `json:"source" bson:"source"`
	Amount float64       `json:"amount" bson:"amount"`
}

type EntryInput struct {
	Date   string  `json:"date"`
	Kind   string  `json:"kind"`
	Source string  `json:"source"`
	Amount float64 `json:"amount"`
}

func (in *EntryInput) Validate(now time.Time) string {
	in.Source = strings.TrimSpace(in.Source)
	d, ok := day(in.Date)
	switch {
	case !ok:
		return "date must be YYYY-MM-DD"
	case d.After(now):
		return "date is in the future"
	case in.Kind != "dividend" && in.Kind != "interest":
		return "kind must be dividend or interest"
	case in.Source == "" || utf8.RuneCountInString(in.Source) > 60:
		return "source needs 1-60 characters"
	case !(in.Amount > 0) || in.Amount > 1e9: // also rejects NaN
		return "amount must be above zero"
	}
	return ""
}

// row is one month's total for a kind, from either the typed entries or the credit SMS.
type row struct {
	YM    string  `bson:"ym"`
	Kind  string  `bson:"kind"`
	Total float64 `bson:"total"`
}

type Month struct {
	YM       string  `json:"ym"`
	Dividend float64 `json:"dividend"`
	Interest float64 `json:"interest"`
}

type Income struct {
	FY       string  `json:"fy"` // the Indian financial year, e.g. 2026-27
	Dividend float64 `json:"dividend"`
	Interest float64 `json:"interest"`
	LastFY   float64 `json:"lastFy"` // dividend plus interest in the previous financial year
	Months   []Month `json:"months"` // last 12 months, oldest first
	Entries  []Entry `json:"entries"`
}

// fyStart is April 1st of the financial year containing t.
func fyStart(t time.Time) time.Time {
	t = t.In(ist.Loc)
	y := t.Year()
	if t.Month() < time.April {
		y--
	}
	return time.Date(y, time.April, 1, 0, 0, 0, 0, ist.Loc)
}

// Summarize folds monthly rows into this financial year, last year's total and a 12-month series.
func Summarize(rows []row, entries []Entry, now time.Time) Income {
	fy := fyStart(now)
	in := Income{FY: fy.Format("2006") + "-" + fy.AddDate(1, 0, 0).Format("06"), Months: make([]Month, 12), Entries: entries}
	idx := map[string]int{}
	for i := range in.Months {
		ym := time.Date(now.Year(), now.Month()-time.Month(11-i), 1, 0, 0, 0, 0, ist.Loc).Format("2006-01")
		in.Months[i].YM, idx[ym] = ym, i
	}
	cur, prev := fy.Format("2006-01"), fy.AddDate(-1, 0, 0).Format("2006-01")
	for _, r := range rows {
		if i, ok := idx[r.YM]; ok {
			if r.Kind == "dividend" {
				in.Months[i].Dividend += r.Total
			} else {
				in.Months[i].Interest += r.Total
			}
		}
		switch {
		case r.YM >= cur && r.Kind == "dividend":
			in.Dividend += r.Total
		case r.YM >= cur:
			in.Interest += r.Total
		case r.YM >= prev:
			in.LastFY += r.Total
		}
	}
	sort.Slice(in.Entries, func(i, j int) bool { return in.Entries[i].Date > in.Entries[j].Date })
	return in
}
