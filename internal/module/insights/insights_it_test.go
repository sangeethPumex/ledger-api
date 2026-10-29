package insights

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
func TestOverview(t *testing.T) {
	uri := os.Getenv("MONGODB_TEST_URI")
	if uri == "" {
		t.Skip("MONGODB_TEST_URI not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("catalyst_insights_test")
	defer db.Drop(ctx)

	netflix := bson.NewObjectID()
	_, _ = db.Collection("vendors").InsertOne(ctx, bson.M{"_id": netflix, "name": "Netflix"})
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, ist.Loc)
	var docs []any
	for _, d := range []int{92, 61, 31, 1} { // monthly, saved vendor
		docs = append(docs, bson.M{"type": "expense", "direction": "debit", "amount_paise": 64900, "occurred_at": at.AddDate(0, 0, -d).UTC(), "vendor_id": netflix, "raw_vendor": "NETFLIX.COM"})
	}
	for _, d := range []int{50, 36, 22, 8} { // small payee, then one big
		docs = append(docs, bson.M{"type": "expense", "direction": "debit", "amount_paise": 20000, "occurred_at": at.AddDate(0, 0, -d).UTC(), "raw_vendor": "Cafe"})
	}
	docs = append(docs,
		bson.M{"type": "expense", "direction": "debit", "amount_paise": 840000, "occurred_at": at.AddDate(0, 0, -2).UTC(), "raw_vendor": "Cafe"},
		bson.M{"type": "expense", "direction": "debit", "amount_paise": 900000, "occurred_at": at.AddDate(0, 0, -3).UTC(), "upi_id": "x@upi", "needs_review": true},
		bson.M{"type": "transfer", "direction": "debit", "amount_paise": 5000000, "occurred_at": at.AddDate(0, 0, -3).UTC(), "raw_vendor": "Self"}, // not spending
	)
	if _, err := db.Collection("transactions").InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}

	o, err := NewService(db).Overview(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Recurring) != 1 || o.Recurring[0].Name != "Netflix" || o.Recurring[0].Count != 4 || o.MonthlyPaise != 64900 {
		t.Errorf("recurring = %+v, monthly %d", o.Recurring, o.MonthlyPaise)
	}
	kinds := map[string]string{}
	for _, a := range o.Anomalies {
		kinds[a.Vendor] = a.Kind
	}
	if len(o.Anomalies) != 2 || kinds["Cafe"] != "spike" || kinds["x@upi"] != "new_vendor" {
		t.Errorf("anomalies = %+v", o.Anomalies)
	}
	var count int
	for _, b := range o.Patterns.Weekdays {
		count += b.Count
	}
	if count != 10 { // the transfer is not spending
		t.Errorf("patterns counted %d spends, want 10", count)
	}
}
