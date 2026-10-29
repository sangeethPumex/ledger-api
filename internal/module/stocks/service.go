package stocks

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct {
	col    *mongo.Collection
	quotes *mongo.Collection
	prices PriceProvider
}

func NewService(db *mongo.Database, prices PriceProvider) *Service {
	return &Service{col: db.Collection(Collection), quotes: db.Collection(QuoteCollection), prices: prices}
}

// Lots lists every purchase, oldest first.
func (s *Service) Lots(ctx context.Context) ([]Lot, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "buyDate", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var out []Lot
	return out, cur.All(ctx, &out)
}

// Portfolio values every lot. Only missing or stale symbols hit the provider, in one batch; if it fails,
// the last cached quote is used and flagged stale instead of returning an error.
func (s *Service) Portfolio(ctx context.Context, now time.Time) (*Portfolio, error) {
	lots, err := s.Lots(ctx)
	if err != nil {
		return nil, err
	}
	seen, symbols := map[string]bool{}, []string{}
	for _, l := range lots {
		if !seen[l.Symbol] {
			seen[l.Symbol] = true
			symbols = append(symbols, l.Symbol)
		}
	}
	quotes := map[string]Quote{}
	if len(symbols) > 0 {
		cur, err := s.quotes.Find(ctx, bson.M{"_id": bson.M{"$in": symbols}})
		if err != nil {
			return nil, err
		}
		var cached []Quote
		if err := cur.All(ctx, &cached); err != nil {
			return nil, err
		}
		fresh := freshAt(now)
		for _, q := range cached {
			quotes[q.Symbol] = q
		}
		var need []string
		for _, sym := range symbols {
			if q, ok := quotes[sym]; !ok || q.Name == "" || !q.DomainTried || q.FetchedAt.Before(fresh) {
				need = append(need, sym)
			}
		}
		if len(need) > 0 {
			got, err := s.prices.Quotes(ctx, need)
			if err != nil {
				log.Printf("quotes: %v (serving cache)", err)
			}
			for sym, q := range got {
				quotes[sym] = q
				if _, err := s.quotes.ReplaceOne(ctx, bson.M{"_id": sym}, q, options.Replace().SetUpsert(true)); err != nil {
					log.Printf("quote save %s: %v", sym, err)
				}
			}
		}
	}
	p := Compute(lots, quotes, freshAt(now))
	return &p, nil
}

func (s *Service) Create(ctx context.Context, in Input) (*Lot, error) {
	l := &Lot{ID: bson.NewObjectID(), Symbol: in.Symbol, Quantity: in.Quantity, BuyPrice: in.BuyPrice, BuyDate: in.BuyDate, Platform: in.Platform}
	_, err := s.col.InsertOne(ctx, l)
	return l, err
}

func (s *Service) Update(ctx context.Context, id bson.ObjectID, in Input) error {
	r, err := s.col.UpdateByID(ctx, id, bson.M{"$set": bson.M{"symbol": in.Symbol, "quantity": in.Quantity, "buyPrice": in.BuyPrice, "buyDate": in.BuyDate, "platform": in.Platform}})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	r, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}
