package stocks

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct {
	col    *mongo.Collection
	quotes *mongo.Collection
	prices PriceProvider
	// OnRefresh, when set, runs after new quotes are saved.
	OnRefresh  func()
	refreshing atomic.Bool // one background refresh at a time
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

// Quotes prices the symbols. Only missing or stale ones hit the provider, in one batch, and the call waits for it; if it
// fails, the last cached quote is used (its FetchedAt then predates freshAt(now)) instead of returning an error.
func (s *Service) Quotes(ctx context.Context, symbols []string, now time.Time) (map[string]Quote, error) {
	quotes, err := s.cached(ctx, symbols)
	if err != nil {
		return nil, err
	}
	missing, stale := outdated(symbols, quotes, freshAt(now))
	s.fetch(ctx, append(missing, stale...), quotes)
	return quotes, nil
}

// quotesNow is Quotes for a page load: saved quotes are returned at once and the stale ones are refreshed in the
// background. Only a symbol that has never been priced is waited for, as there is nothing to show for it.
func (s *Service) quotesNow(ctx context.Context, symbols []string, now time.Time) (map[string]Quote, error) {
	quotes, err := s.cached(ctx, symbols)
	if err != nil {
		return nil, err
	}
	missing, stale := outdated(symbols, quotes, freshAt(now))
	s.fetch(ctx, missing, quotes)
	if len(stale) > 0 && s.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer s.refreshing.Store(false)
			bg, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.fetch(bg, stale, map[string]Quote{})
		}()
	}
	return quotes, nil
}

func (s *Service) cached(ctx context.Context, symbols []string) (map[string]Quote, error) {
	quotes := map[string]Quote{}
	if len(symbols) == 0 {
		return quotes, nil
	}
	cur, err := s.quotes.Find(ctx, bson.M{"_id": bson.M{"$in": symbols}})
	if err != nil {
		return nil, err
	}
	var saved []Quote
	if err := cur.All(ctx, &saved); err != nil {
		return nil, err
	}
	for _, q := range saved {
		quotes[q.Symbol] = q
	}
	return quotes, nil
}

// outdated splits the symbols that need the provider: missing have no saved quote, stale have one that is old or incomplete.
func outdated(symbols []string, quotes map[string]Quote, fresh time.Time) (missing, stale []string) {
	for _, sym := range symbols {
		if q, ok := quotes[sym]; !ok {
			missing = append(missing, sym)
		} else if q.Name == "" || !q.DomainTried || q.FetchedAt.Before(fresh) {
			stale = append(stale, sym)
		}
	}
	return missing, stale
}

// fetch prices need from the provider, saves what comes back and puts it into quotes.
func (s *Service) fetch(ctx context.Context, need []string, quotes map[string]Quote) {
	if len(need) == 0 {
		return
	}
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
	if len(got) > 0 && s.OnRefresh != nil {
		s.OnRefresh()
	}
}

// Portfolio values every lot at the saved prices, refreshing old ones in the background; Summary.Stale says some are old.
func (s *Service) Portfolio(ctx context.Context, now time.Time) (*Portfolio, error) {
	return s.value(ctx, now, s.quotesNow)
}

// LivePortfolio is Portfolio after waiting for current prices, for background jobs that record them.
func (s *Service) LivePortfolio(ctx context.Context, now time.Time) (*Portfolio, error) {
	return s.value(ctx, now, s.Quotes)
}

func (s *Service) value(ctx context.Context, now time.Time, price func(context.Context, []string, time.Time) (map[string]Quote, error)) (*Portfolio, error) {
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
	quotes, err := price(ctx, symbols, now)
	if err != nil {
		return nil, err
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
