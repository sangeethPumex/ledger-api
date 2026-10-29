package budgets

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/sync/errgroup"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/spend"
)

const Collection = "budgets"

// Alert levels: warn from 80% of the limit, over from 100%.
const (
	LevelOK   = "ok"
	LevelWarn = "warn"
	LevelOver = "over"
)

type Budget struct {
	ID         bson.ObjectID `bson:"_id" json:"id"`
	CategoryID bson.ObjectID `bson:"category_id" json:"category_id"`
	LimitPaise int64         `bson:"limit_paise" json:"limit_paise"`
}

// Status is a budget with this month's spending against it.
type Status struct {
	Budget
	SpentPaise int64   `json:"spent_paise"`
	Ratio      float64 `json:"ratio"` // spent / limit; above 1 once over
	Level      string  `json:"level"`
}

type Service struct {
	col *mongo.Collection
	txs *mongo.Collection // owned by the transactions module; read only
}

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	// One budget per category; Set upserts on it, so two racing saves cannot create two.
	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "category_id", Value: 1}}, Options: options.Index().SetUnique(true)})
	return &Service{col: col, txs: db.Collection("transactions")}, err
}

// Set creates or replaces the monthly limit of a category in one atomic upsert.
func (s *Service) Set(ctx context.Context, cat bson.ObjectID, limit int64) error {
	_, err := s.col.UpdateOne(ctx, bson.M{"category_id": cat},
		bson.M{"$set": bson.M{"limit_paise": limit}, "$setOnInsert": bson.M{"_id": bson.NewObjectID()}}, options.UpdateOne().SetUpsert(true))
	return err
}

func (s *Service) Delete(ctx context.Context, cat bson.ObjectID) error {
	res, err := s.col.DeleteOne(ctx, bson.M{"category_id": cat})
	if err == nil && res.DeletedCount == 0 {
		err = mongo.ErrNoDocuments
	}
	return err
}

func level(ratio float64) string {
	switch {
	case ratio >= 1:
		return LevelOver
	case ratio >= 0.8:
		return LevelWarn
	}
	return LevelOK
}

// Statuses is every budget with its spending so far this IST month, the most used first. The budget list and the month's spend per
// category are independent reads, so they run side by side; the spend comes from one aggregation, split payments counted per category.
func (s *Service) Statuses(ctx context.Context, now time.Time) ([]Status, error) {
	start := ist.Midnight(now)
	start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, ist.Loc)
	var budgets []Budget
	spent := map[bson.ObjectID]int64{}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		cur, err := s.col.Find(gctx, bson.D{})
		if err != nil {
			return err
		}
		return cur.All(gctx, &budgets)
	})
	g.Go(func() error {
		pipe := append(mongo.Pipeline{{{Key: "$match", Value: spend.Match(start, start.AddDate(0, 1, 0))}}}, spend.Parts()...)
		pipe = append(pipe, bson.D{{Key: "$group", Value: bson.M{"_id": "$category_id", "paise": bson.M{"$sum": "$amount_paise"}}}})
		cur, err := s.txs.Aggregate(gctx, pipe)
		if err != nil {
			return err
		}
		var rows []struct {
			ID    *bson.ObjectID `bson:"_id"`
			Paise int64          `bson:"paise"`
		}
		if err := cur.All(gctx, &rows); err != nil {
			return err
		}
		for _, r := range rows {
			if r.ID != nil {
				spent[*r.ID] = r.Paise
			}
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	out := make([]Status, len(budgets))
	for i, b := range budgets {
		st := Status{Budget: b, SpentPaise: spent[b.CategoryID]}
		if b.LimitPaise > 0 {
			st.Ratio = float64(st.SpentPaise) / float64(b.LimitPaise)
		}
		st.Level = level(st.Ratio)
		out[i] = st
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ratio > out[j].Ratio })
	return out, nil
}
