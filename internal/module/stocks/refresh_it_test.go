package stocks

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// gated answers only once released, like a provider that is slow.
type gated struct{ release chan struct{} }

func (g gated) Quotes(_ context.Context, symbols []string) (map[string]Quote, error) {
	<-g.release
	out := map[string]Quote{}
	for _, s := range symbols {
		out[s] = Quote{Symbol: s, Name: s, Price: 200, DomainTried: true, FetchedAt: time.Now()}
	}
	return out, nil
}

// Needs a real MongoDB, e.g. MONGODB_TEST_URI=mongodb://localhost:27017. Skipped otherwise.
func TestPortfolioDoesNotWaitForOldPrices(t *testing.T) {
	uri := os.Getenv("MONGODB_TEST_URI")
	if uri == "" {
		t.Skip("MONGODB_TEST_URI not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("stocks_it_" + bson.NewObjectID().Hex())
	defer db.Drop(context.Background())

	g := gated{make(chan struct{})}
	s := NewService(db, g)
	refreshed := make(chan struct{}, 1)
	s.OnRefresh = func() { refreshed <- struct{}{} }
	if _, err := s.Create(ctx, Input{Symbol: "A.NS", Quantity: 1, BuyPrice: 50, BuyDate: "2026-01-01"}); err != nil {
		t.Fatal(err)
	}
	old := Quote{Symbol: "A.NS", Name: "A", Price: 100, DomainTried: true, FetchedAt: time.Now().Add(-30 * 24 * time.Hour)}
	if _, err := s.quotes.InsertOne(ctx, old); err != nil {
		t.Fatal(err)
	}

	// The provider is stuck: the saved price comes back anyway, flagged stale. Asking twice starts one refresh.
	for range 2 {
		p, err := s.Portfolio(ctx, time.Now())
		if err != nil || p.Summary.Value != 100 || !p.Summary.Stale {
			t.Fatalf("saved price: %+v %v", p, err)
		}
	}
	g.release <- struct{}{}
	select {
	case <-refreshed:
	case <-ctx.Done():
		t.Fatal("no background refresh")
	}
	p, err := s.Portfolio(ctx, time.Now())
	if err != nil || p.Summary.Value != 200 || p.Summary.Stale {
		t.Fatalf("refreshed price: %+v %v", p, err)
	}

	// A background job waits for the current price.
	if _, err := s.quotes.ReplaceOne(ctx, bson.M{"_id": "A.NS"}, old); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	if p, err := s.LivePortfolio(ctx, time.Now()); err != nil || p.Summary.Value != 200 {
		t.Fatalf("live: %+v %v", p, err)
	}
}
