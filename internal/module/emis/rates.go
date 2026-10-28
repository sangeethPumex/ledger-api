package emis

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

// RateReq is a rate reset: the new rate and the date it took (or takes) effect.
type RateReq struct {
	From string  `json:"from"`
	Rate float64 `json:"rate"`
}

// setRates puts a change into the history (replacing one on the same date) and points Rate at whatever is in force today.
func setRates(e *Emi, c RateChange, now time.Time) {
	if len(e.Rates) == 0 && e.Rate != nil {
		e.Rates = []RateChange{{Rate: *e.Rate}} // the rate before the first reset
	}
	kept := e.Rates[:0:0]
	for _, r := range e.Rates {
		if r.From != c.From {
			kept = append(kept, r)
		}
	}
	e.Rates = append(kept, c)
	sort.SliceStable(e.Rates, func(i, j int) bool { return e.Rates[i].From < e.Rates[j].From })
	e.Rate = nil
	cur := rateOn(&Emi{Rate: &c.Rate, Rates: e.Rates}, ist.Midnight(now).Format(day))
	e.Rate = &cur
}

// AddRate records a rate change; the message is safe to show for a 400.
func (s *Service) AddRate(ctx context.Context, id bson.ObjectID, req RateReq, now time.Time) (msg string, err error) {
	if _, perr := time.Parse(day, req.From); perr != nil {
		return "date must be YYYY-MM-DD", nil
	}
	if req.Rate <= 0 || req.Rate > 100 {
		return "rate must be above 0 and at most 100", nil
	}
	e, err := s.get(ctx, id)
	if err != nil {
		return "", err
	}
	if e.Kind != "loan" {
		return "only loans have an interest rate", nil
	}
	setRates(e, RateChange{From: req.From, Rate: req.Rate}, now)
	return "", s.save(ctx, e)
}

// RemoveRate deletes a change by its date. The starting rate (no date) cannot be removed.
func (s *Service) RemoveRate(ctx context.Context, id bson.ObjectID, from string, now time.Time) (bool, error) {
	e, err := s.get(ctx, id)
	if err != nil {
		return false, err
	}
	kept := e.Rates[:0:0]
	for _, r := range e.Rates {
		if r.From != from || from == "" {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(e.Rates) {
		return false, nil
	}
	e.Rates = kept
	if len(kept) > 0 {
		cur := rateOn(&Emi{Rate: &kept[0].Rate, Rates: kept}, ist.Midnight(now).Format(day))
		e.Rate = &cur
	}
	return true, s.save(ctx, e)
}
