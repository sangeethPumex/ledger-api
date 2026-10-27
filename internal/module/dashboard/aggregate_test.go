package dashboard

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
)

// Needs a real MongoDB (5.2+ for $topN), e.g. MONGODB_TEST_URI=mongodb://localhost:27017. Skipped otherwise.
func TestAggregate(t *testing.T) {
	uri := os.Getenv("MONGODB_TEST_URI")
	if uri == "" {
		t.Skip("MONGODB_TEST_URI not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("catalyst_dashboard_test")
	defer db.Drop(ctx)

	food := bson.NewObjectID()
	_, _ = db.Collection("categories").InsertOne(ctx, bson.M{"_id": food, "name": "Food"})
	day := func(d int) time.Time { return time.Date(2026, 10, d, 10, 0, 0, 0, ist.Loc).UTC() }
	_, err = db.Collection("transactions").InsertMany(ctx, []bson.M{
		{"type": "income", "direction": "credit", "amount_paise": 100000, "occurred_at": day(1), "raw_vendor": "ACME"},
		{"type": "expense", "direction": "debit", "amount_paise": 2000, "occurred_at": day(2), "category_id": food, "raw_vendor": "Swiggy"},
		{"type": "expense", "direction": "debit", "amount_paise": 500, "occurred_at": day(2), "raw_vendor": "Shop"},
		{"type": "emi", "direction": "debit", "amount_paise": 7000, "occurred_at": day(3), "raw_vendor": "Bank"},
		{"type": "transfer", "direction": "credit", "amount_paise": 99999, "occurred_at": day(3)}, // never counted
		{"type": "refund", "direction": "credit", "amount_paise": 5, "occurred_at": day(3)},       // never counted
		{"type": "expense", "direction": "debit", "amount_paise": 9999, "occurred_at": day(25)},   // outside the window
	})
	if err != nil {
		t.Fatal(err)
	}

	s := &Service{txs: db.Collection("transactions")}
	flows, days, err := s.aggregate(ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, ist.Loc), time.Date(2026, 10, 10, 0, 0, 0, 0, ist.Loc))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, f := range flows {
		got[f.ID.Dir+"/"+f.ID.Name] = f.Paise
	}
	want := map[string]int64{"in/ACME": 100000, "out/Food": 2000, "out/Uncategorised": 500, "out/EMIs": 7000}
	if len(got) != len(want) {
		t.Fatalf("flows %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d want %d (all %v)", k, got[k], v, got)
		}
	}
	if len(days) != 2 || days[0].ID != "2026-10-02" || days[0].Sum != 2500 || days[0].Top.Vendor != "Swiggy" || days[0].Top.Cat != "Food" {
		t.Errorf("days %+v", days)
	}
}
