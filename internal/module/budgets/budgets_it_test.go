package budgets

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

// Needs a real MongoDB, e.g. MONGODB_TEST_URI=mongodb://localhost:27017. Skipped otherwise.
func TestStatuses(t *testing.T) {
	uri := os.Getenv("MONGODB_TEST_URI")
	if uri == "" {
		t.Skip("MONGODB_TEST_URI not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("catalyst_budgets_test")
	defer db.Drop(ctx)

	food, gifts, fun := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, ist.Loc)
	at := func(d int) time.Time { return time.Date(2026, 10, d, 10, 0, 0, 0, ist.Loc).UTC() }
	_, err = db.Collection("transactions").InsertMany(ctx, []bson.M{
		{"type": "expense", "direction": "debit", "amount_paise": 40000, "occurred_at": at(2), "category_id": food},
		{"type": "expense", "direction": "debit", "amount_paise": 50000, "occurred_at": at(3), "category_id": food, "owed_paise": 10000}, // friend owes 100: counts 400
		{"type": "expense", "direction": "debit", "amount_paise": 30000, "occurred_at": at(4), "category_id": food, // split 200 food + 100 gifts
			"splits": bson.A{bson.M{"category_id": food, "paise": 20000}, bson.M{"category_id": gifts, "paise": 10000}}},
		{"type": "expense", "direction": "debit", "amount_paise": 99999, "occurred_at": time.Date(2026, 9, 30, 10, 0, 0, 0, ist.Loc).UTC(), "category_id": food}, // last month
		{"type": "transfer", "direction": "debit", "amount_paise": 77777, "occurred_at": at(5), "category_id": food},                                             // not spending
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewService(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for cat, limit := range map[bson.ObjectID]int64{food: 100000, gifts: 8000, fun: 5000} {
		if err := s.Set(ctx, cat, limit); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Set(ctx, fun, 6000); err != nil { // upsert replaces, not duplicates
		t.Fatal(err)
	}
	got, err := s.Statuses(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 budgets, got %d", len(got))
	}
	by := map[bson.ObjectID]Status{}
	for _, st := range got {
		by[st.CategoryID] = st
	}
	if st := by[food]; st.SpentPaise != 100000 || st.Level != LevelOver { // 400 + (500-100 owed) + 200 split share = exactly the limit
		t.Errorf("food = %+v", st)
	}
	if st := by[gifts]; st.SpentPaise != 10000 || st.Level != LevelOver {
		t.Errorf("gifts = %+v", st)
	}
	if st := by[fun]; st.SpentPaise != 0 || st.Level != LevelOK || st.LimitPaise != 6000 {
		t.Errorf("fun = %+v", st)
	}
	if got[0].Ratio < got[len(got)-1].Ratio {
		t.Error("most used budget should come first")
	}
}
