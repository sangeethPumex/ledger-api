package insights

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/spend"
)

// Pace is this month's spending against last month's: so far, over the same days last month, in total, and where it is heading.
type Pace struct {
	MonthPaise     int64 `json:"month_paise"`
	PrevSpanPaise  int64 `json:"prev_span_paise"` // last month, same days
	PrevMonthPaise int64 `json:"prev_month_paise"`
	ProjectedPaise int64 `json:"projected_paise"` // at this month's daily average
	DailyAvgPaise  int64 `json:"daily_avg_paise"`
	Day            int   `json:"day"`
	DaysInMonth    int   `json:"days_in_month"`
}

// Mover is one category's spending this month so far against the same days last month.
type Mover struct {
	CategoryID *bson.ObjectID `json:"category_id,omitempty"`
	Name       string         `json:"name"`
	Paise      int64          `json:"paise"`
	PrevPaise  int64          `json:"prev_paise"`
}

// TopVendor is where this month's money went, by payee.
type TopVendor struct {
	Name  string `json:"name"`
	Paise int64  `json:"paise"`
	Count int    `json:"count"`
}

type catSum struct {
	ID struct {
		Cat *bson.ObjectID `bson:"cat"`
		Cur bool           `bson:"cur"`
	} `bson:"_id"`
	Paise int64 `bson:"paise"`
	Span  int64 `bson:"span"`
}

// monthSpend compares this month with last in one pass over two months of spending, grouped by category and month.
func (s *Service) monthSpend(ctx context.Context, now time.Time) (*Pace, []Mover, error) {
	n := now.In(ist.Loc)
	cur := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, ist.Loc)
	prev := cur.AddDate(0, -1, 0)
	spanEnd := prev.Add(now.Sub(cur)) // the same stretch of last month
	pipe := append(mongo.Pipeline{{{Key: "$match", Value: spend.Match(prev, now.Add(time.Hour))}}}, spend.Parts()...)
	pipe = append(pipe, mongo.Pipeline{
		{{Key: "$group", Value: bson.M{
			"_id":   bson.M{"cat": "$category_id", "cur": bson.M{"$gte": bson.A{"$occurred_at", cur.UTC()}}},
			"paise": bson.M{"$sum": "$amount_paise"},
			"span":  bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$lt": bson.A{"$occurred_at", spanEnd.UTC()}}, "$amount_paise", 0}}},
		}}},
	}...)
	c, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return nil, nil, err
	}
	var rows []catSum
	if err := c.All(ctx, &rows); err != nil {
		return nil, nil, err
	}
	p := &Pace{Day: n.Day(), DaysInMonth: cur.AddDate(0, 1, -1).Day()}
	by := map[bson.ObjectID]*Mover{}
	var none Mover
	for _, r := range rows {
		m := &none
		if r.ID.Cat != nil {
			if by[*r.ID.Cat] == nil {
				by[*r.ID.Cat] = &Mover{CategoryID: r.ID.Cat}
			}
			m = by[*r.ID.Cat]
		}
		if r.ID.Cur {
			m.Paise += r.Paise
			p.MonthPaise += r.Paise
		} else {
			m.PrevPaise += r.Span
			p.PrevSpanPaise += r.Span
			p.PrevMonthPaise += r.Paise
		}
	}
	if p.Day > 0 {
		p.DailyAvgPaise = p.MonthPaise / int64(p.Day)
		p.ProjectedPaise = p.DailyAvgPaise * int64(p.DaysInMonth)
	}
	movers := make([]Mover, 0, len(by)+1)
	for _, m := range by {
		movers = append(movers, *m)
	}
	if none.Paise > 0 || none.PrevPaise > 0 {
		none.Name = "Uncategorised"
		movers = append(movers, none)
	}
	if len(by) > 0 {
		ids := bson.A{}
		for id := range by {
			ids = append(ids, id)
		}
		cc, err := s.cats.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
		if err != nil {
			return nil, nil, err
		}
		var cs []struct {
			ID   bson.ObjectID `bson:"_id"`
			Name string        `bson:"name"`
		}
		if err := cc.All(ctx, &cs); err != nil {
			return nil, nil, err
		}
		names := map[bson.ObjectID]string{}
		for _, c := range cs {
			names[c.ID] = c.Name
		}
		for i := range movers {
			if movers[i].CategoryID != nil {
				movers[i].Name = names[*movers[i].CategoryID]
			}
		}
	}
	sort.Slice(movers, func(i, j int) bool { return movers[i].Paise+movers[i].PrevPaise > movers[j].Paise+movers[j].PrevPaise })
	if len(movers) > 8 {
		movers = movers[:8]
	}
	return p, movers, nil
}

// topVendors ranks this month's payees from the vendor series already read for the recurring scan.
func topVendors(all []vendorSeries, now time.Time, n int) []TopVendor {
	nn := now.In(ist.Loc)
	from := time.Date(nn.Year(), nn.Month(), 1, 0, 0, 0, 0, ist.Loc)
	out := []TopVendor{}
	for _, v := range all {
		t := TopVendor{Name: v.Name}
		for _, p := range v.Txns {
			if !p.At.Before(from) {
				t.Paise += p.Paise
				t.Count++
			}
		}
		if t.Paise > 0 {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Paise > out[j].Paise })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
