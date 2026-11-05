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

// RealMover is one category's last three full months against the same three months a year earlier.
// The frontend splits the change into price (the inflation rate) and volume (what is left).
type RealMover struct {
	CategoryID *bson.ObjectID `json:"category_id,omitempty"`
	Name       string         `json:"name"`
	Paise      int64          `json:"paise"`
	PrevPaise  int64          `json:"prev_paise"` // the same three months a year ago
}

// Inflation is the yearly price rise assumed for every category. There is no CPI feed, so it is one configured number.
type Inflation struct {
	Pct    float64     `json:"pct"`
	Movers []RealMover `json:"movers"`
}

type realSum struct {
	ID struct {
		Cat    *bson.ObjectID `bson:"cat"`
		Recent bool           `bson:"recent"`
	} `bson:"_id"`
	Paise int64 `bson:"paise"`
}

// inflation groups two three-month windows by category in one pass. Categories with no spend a year ago are left out: there is nothing to compare.
func (s *Service) inflation(ctx context.Context, now time.Time) (*Inflation, error) {
	n := now.In(ist.Loc)
	cur := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, ist.Loc)
	recent, priorFrom, priorTo := cur.AddDate(0, -3, 0), cur.AddDate(0, -15, 0), cur.AddDate(0, -12, 0)
	pipe := mongo.Pipeline{
		{{Key: "$match", Value: spend.Match(priorFrom, cur)}},
		{{Key: "$match", Value: bson.M{"$or": bson.A{
			bson.M{"occurred_at": bson.M{"$gte": recent.UTC()}},
			bson.M{"occurred_at": bson.M{"$lt": priorTo.UTC()}},
		}}}},
	}
	pipe = append(pipe, spend.Parts()...)
	pipe = append(pipe, bson.D{{Key: "$group", Value: bson.M{
		"_id":   bson.M{"cat": "$category_id", "recent": bson.M{"$gte": bson.A{"$occurred_at", recent.UTC()}}},
		"paise": bson.M{"$sum": "$amount_paise"},
	}}})
	c, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	var rows []realSum
	if err := c.All(ctx, &rows); err != nil {
		return nil, err
	}
	by := map[bson.ObjectID]*RealMover{}
	var none RealMover
	for _, r := range rows {
		m := &none
		if r.ID.Cat != nil {
			if by[*r.ID.Cat] == nil {
				by[*r.ID.Cat] = &RealMover{CategoryID: r.ID.Cat}
			}
			m = by[*r.ID.Cat]
		}
		if r.ID.Recent {
			m.Paise += r.Paise
		} else {
			m.PrevPaise += r.Paise
		}
	}
	movers := make([]RealMover, 0, len(by)+1)
	for _, m := range by {
		movers = append(movers, *m)
	}
	if none.PrevPaise > 0 {
		none.Name = "Uncategorised"
		movers = append(movers, none)
	}
	names, err := s.catNames(ctx, movers)
	if err != nil {
		return nil, err
	}
	kept := movers[:0]
	for _, m := range movers {
		if m.CategoryID != nil {
			m.Name = names[*m.CategoryID]
		}
		if m.PrevPaise > 0 {
			kept = append(kept, m)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Paise+kept[i].PrevPaise > kept[j].Paise+kept[j].PrevPaise })
	if len(kept) > 8 {
		kept = kept[:8]
	}
	return &Inflation{Pct: s.inflationPct, Movers: kept}, nil
}

func (s *Service) catNames(ctx context.Context, movers []RealMover) (map[bson.ObjectID]string, error) {
	ids := bson.A{}
	for _, m := range movers {
		if m.CategoryID != nil {
			ids = append(ids, *m.CategoryID)
		}
	}
	names := map[bson.ObjectID]string{}
	if len(ids) == 0 {
		return names, nil
	}
	cc, err := s.cats.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	var cs []struct {
		ID   bson.ObjectID `bson:"_id"`
		Name string        `bson:"name"`
	}
	if err := cc.All(ctx, &cs); err != nil {
		return nil, err
	}
	for _, c := range cs {
		names[c.ID] = c.Name
	}
	return names, nil
}
