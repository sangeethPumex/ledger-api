// Package alerts fires once when a stock price or a fund NAV crosses a level. Stocks are priced through the stocks quote cache
// (one batched call for all symbols); funds through mfapi.in, one call per scheme code, cached for a while.
package alerts

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/sync/errgroup"

	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/stocks"
)

const (
	Collection = "alerts"
	maxAlerts  = 100 // each distinct fund is a network call per refresh, so keep the fan-out bounded
	navTTL     = 30 * time.Minute
)

// Alert is one level to watch. Price is rupees: the share price, or the NAV.
type Alert struct {
	ID    bson.ObjectID `json:"id" bson:"_id"`
	Kind  string        `json:"kind" bson:"kind"` // stock | fund
	Ref   string        `json:"ref" bson:"ref"`   // Yahoo symbol (RELIANCE.NS) or mfapi scheme code
	Name  string        `json:"name" bson:"name"`
	Op    string        `json:"op" bson:"op"` // above | below
	Price float64       `json:"price" bson:"price"`

	CreatedAt      time.Time  `json:"createdAt" bson:"createdAt"`
	TriggeredAt    *time.Time `json:"triggeredAt,omitempty" bson:"triggeredAt,omitempty"`
	TriggeredPrice *float64   `json:"triggeredPrice,omitempty" bson:"triggeredPrice,omitempty"`
}

type Input struct {
	Kind  string  `json:"kind"`
	Ref   string  `json:"ref"`
	Op    string  `json:"op"`
	Price float64 `json:"price"`
}

var codeRe = regexp.MustCompile(`^\d{1,8}$`)

func (in *Input) Validate() string {
	in.Ref = strings.ToUpper(strings.TrimSpace(in.Ref))
	switch {
	case in.Kind != "stock" && in.Kind != "fund":
		return "kind must be stock or fund"
	case in.Kind == "stock" && !stocks.ValidSymbol(in.Ref):
		return "symbol needs an exchange suffix, e.g. RELIANCE.NS or TCS.BO"
	case in.Kind == "fund" && !codeRe.MatchString(in.Ref):
		return "scheme code must be digits"
	case in.Op != "above" && in.Op != "below":
		return "op must be above or below"
	case !(in.Price > 0) || in.Price > 1e9: // also rejects NaN
		return "price must be above zero"
	}
	return ""
}

// Hit is whether price has reached the level.
func (a *Alert) Hit(price float64) bool {
	if a.Op == "above" {
		return price >= a.Price
	}
	return price <= a.Price
}

type Service struct {
	col    *mongo.Collection
	stocks *stocks.Service
	funds  *funds.Service

	mu   sync.Mutex
	navs map[string]nav // by scheme code
}

type nav struct {
	price float64
	at    time.Time
}

func NewService(ctx context.Context, db *mongo.Database, st *stocks.Service, f *funds.Service) (*Service, error) {
	col := db.Collection(Collection)
	// refresh only ever reads the alerts still waiting, so index that
	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "triggeredAt", Value: 1}}})
	return &Service{col: col, stocks: st, funds: f, navs: map[string]nav{}}, err
}

var errUnknown = fmt.Errorf("no such stock or fund")

// Create checks the symbol or scheme exists and stores the alert; the second result is the price now.
func (s *Service) Create(ctx context.Context, in Input, now time.Time) (*Alert, float64, error) {
	if n, err := s.col.CountDocuments(ctx, bson.D{}); err != nil {
		return nil, 0, err
	} else if n >= maxAlerts {
		return nil, 0, fmt.Errorf("limit of %d alerts reached; delete some first", maxAlerts)
	}
	a := &Alert{ID: bson.NewObjectID(), Kind: in.Kind, Ref: in.Ref, Op: in.Op, Price: in.Price, CreatedAt: now.UTC()}
	var price float64
	if in.Kind == "stock" {
		q, err := s.stocks.Quotes(ctx, []string{in.Ref}, now)
		if err != nil {
			return nil, 0, err
		}
		got, ok := q[in.Ref]
		if !ok || got.Price <= 0 {
			return nil, 0, errUnknown
		}
		a.Name, price = got.Name, got.Price
	} else {
		l, err := s.funds.Lookup(ctx, in.Ref)
		if err != nil {
			return nil, 0, errUnknown
		}
		a.Name, price = l.Name, l.Nav
		s.putNav(in.Ref, price, now)
	}
	_, err := s.col.InsertOne(ctx, a)
	return a, price, err
}

func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	r, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) putNav(code string, price float64, now time.Time) {
	s.mu.Lock()
	s.navs[code] = nav{price, now}
	s.mu.Unlock()
}

// prices returns the current price of every distinct stock symbol and fund code in as, keyed by Ref. A missing key means it could not be priced.
func (s *Service) prices(ctx context.Context, as []Alert, now time.Time) map[string]float64 {
	out := map[string]float64{}
	var symbols, codes []string
	seen := map[string]bool{}
	for _, a := range as {
		if seen[a.Kind+a.Ref] {
			continue
		}
		seen[a.Kind+a.Ref] = true
		if a.Kind == "stock" {
			symbols = append(symbols, a.Ref)
		} else {
			codes = append(codes, a.Ref)
		}
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	g.Go(func() error {
		qs, _ := s.stocks.Quotes(gctx, symbols, now) // an error leaves the symbols unpriced until the next refresh
		mu.Lock()
		defer mu.Unlock()
		for sym, q := range qs {
			if q.Price > 0 {
				out[sym] = q.Price
			}
		}
		return nil
	})
	for _, code := range codes {
		s.mu.Lock()
		c, ok := s.navs[code]
		s.mu.Unlock()
		if ok && now.Sub(c.at) < navTTL {
			out[code] = c.price
			continue
		}
		g.Go(func() error {
			if l, err := s.funds.Lookup(gctx, code); err == nil {
				s.putNav(code, l.Nav, now)
				mu.Lock()
				out[code] = l.Nav
				mu.Unlock()
			} else if ok {
				mu.Lock()
				out[code] = c.price // mfapi hiccup: the last NAV beats none
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait()
	return out
}

// Refresh prices every alert still waiting and fires those that crossed their level, once: the update only matches a
// still-waiting alert, so two overlapping refreshes cannot both fire it. It returns the prices it saw.
func (s *Service) Refresh(ctx context.Context, now time.Time) (map[string]float64, error) {
	cur, err := s.col.Find(ctx, bson.M{"triggeredAt": nil})
	if err != nil {
		return nil, err
	}
	var waiting []Alert
	if err := cur.All(ctx, &waiting); err != nil || len(waiting) == 0 {
		return map[string]float64{}, err
	}
	prices := s.prices(ctx, waiting, now)
	for _, a := range waiting {
		if p, ok := prices[a.Ref]; ok && a.Hit(p) {
			if _, err := s.col.UpdateOne(ctx, bson.M{"_id": a.ID, "triggeredAt": nil}, bson.M{"$set": bson.M{"triggeredAt": now.UTC(), "triggeredPrice": p}}); err != nil {
				return prices, err
			}
		}
	}
	return prices, nil
}

// View is an alert with the price it is being compared with now (nil when it could not be priced, or once it has fired).
type View struct {
	Alert
	Current *float64 `json:"current,omitempty"`
}

// List refreshes, then returns fired alerts first (newest), then waiting ones.
func (s *Service) List(ctx context.Context, now time.Time) ([]View, error) {
	prices, err := s.Refresh(ctx, now)
	if err != nil {
		return nil, err
	}
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "triggeredAt", Value: -1}, {Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var as []Alert
	if err := cur.All(ctx, &as); err != nil {
		return nil, err
	}
	out := make([]View, len(as))
	for i, a := range as {
		out[i].Alert = a
		if p, ok := prices[a.Ref]; ok && a.TriggeredAt == nil {
			out[i].Current = &p
		}
	}
	return out, nil
}

// Run refreshes every `every` until ctx ends, so alerts fire with no page open.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, time.Minute)
			if _, err := s.Refresh(rctx, time.Now()); err != nil {
				log.Printf("alerts refresh: %v", err)
			}
			cancel()
		}
	}
}
