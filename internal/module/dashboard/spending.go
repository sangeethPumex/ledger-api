package dashboard

import (
	"context"
	"sort"
	"time"

	"ledger-backend/internal/ist"
)

// SpendDay is a day of spending with its heat level: 0 for none, 1-4 by quartile of the busy days in the window.
type SpendDay struct {
	Day
	Level int `json:"level"`
}

const spendWeeks = 26

// levels grades days against each other, so one huge purchase does not wash out the rest.
func levels(days []Day) []SpendDay {
	var sums []int64
	for _, d := range days {
		if d.Paise > 0 {
			sums = append(sums, d.Paise)
		}
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i] < sums[j] })
	out := make([]SpendDay, len(days))
	for i, d := range days {
		out[i] = SpendDay{Day: d}
		for q := 1; q <= 4 && d.Paise > 0; q++ {
			out[i].Level = q
			if d.Paise <= sums[max(min(len(sums)*q/4, len(sums))-1, 0)] { // with under 4 busy days the first quartiles fall on the smallest one
				break
			}
		}
	}
	return out
}

// DailySpend is every day's spending over the last 26 weeks, starting on a Sunday so the grid lines up in week columns.
func (s *Service) DailySpend(ctx context.Context, now time.Time) ([]SpendDay, error) {
	today := ist.Midnight(now)
	r := Range{today.AddDate(0, 0, -int(today.Weekday())-(spendWeeks-1)*7), today.AddDate(0, 0, 1)}
	_, docs, err := s.aggregate(ctx, r.From, r.To)
	if err != nil {
		return nil, err
	}
	return levels(fillDays(docs, r, now)), nil
}
