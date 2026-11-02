package transactions

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/categories"
	"ledger-backend/internal/module/raw_transactions"
	"ledger-backend/internal/module/vendors"
)

// Edits through the real service: source and vendor lookups, and what ends up stored. Needs a real MongoDB, e.g.
// MONGODB_TEST_URI=mongodb://localhost:27017. Skipped otherwise.
func TestEditEndToEnd(t *testing.T) {
	uri := os.Getenv("MONGODB_TEST_URI")
	if uri == "" {
		t.Skip("MONGODB_TEST_URI not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("txn_edit_it_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	cat, err := categories.NewService(ctx, db)
	must(err)
	ven, err := vendors.NewService(ctx, db)
	must(err)
	acc, err := accounts.NewService(ctx, db)
	must(err)
	crd, err := cards.NewService(ctx, db)
	must(err)
	s, err := NewService(ctx, db, raw_transactions.NewService(db), ven, cat, acc, crd, nil, "test")
	must(err)

	food, err := cat.Create(ctx, "Food", "")
	must(err)
	_, err = ven.Create(ctx, "Zomato", &food.ID, nil)
	must(err)
	a1, err := acc.Create(ctx, accounts.Account{Name: "Main", Bank: "HDFC", Last4: "1111"})
	must(err)
	a2, err := acc.Create(ctx, accounts.Account{Name: "Other", Bank: "FEDERAL", Last4: "2222"})
	must(err)

	made, err := s.Create(ctx, CreateInput{Type: "expense", AmountPaise: 45_000, OccurredAt: time.Now().Add(-48 * time.Hour), AccountID: a1.ID.Hex(), Vendor: "ZOMATO*1234", Note: "dinner", Ref: "R1"})
	must(err)
	get := func() Transaction {
		t.Helper()
		var got Transaction
		must(s.col.FindOne(ctx, bson.M{"_id": made.ID}).Decode(&got))
		return got
	}
	when := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	edit := func(e EditPatch) error { return s.Patch(ctx, made.ID, Patch{Edit: &e}) }
	same := func() EditPatch {
		g := get()
		return EditPatch{Type: g.Type, Direction: g.Direction, AmountPaise: g.AmountPaise, Ref: g.Ref, Note: g.Note}
	}

	// Amount and day change; account and payee are left alone, so the bank details stay.
	e := same()
	e.AmountPaise, e.OccurredAt, e.Note = 52_500, &when, ""
	must(edit(e))
	g := get()
	if g.AmountPaise != 52_500 || !g.OccurredAt.Equal(when) || !g.ReceivedAt.Equal(when) || g.Note != "" {
		t.Fatalf("amount/date/note = %d %v %v %q", g.AmountPaise, g.OccurredAt, g.ReceivedAt, g.Note)
	}
	if g.AccountID == nil || *g.AccountID != a1.ID || g.Bank != "HDFC" || g.AccountLast4 != "1111" || g.Ref != "R1" {
		t.Fatalf("an edit that sent no source must keep it: %+v", g)
	}

	// No date sent: the day stays.
	e = same()
	e.AmountPaise = 53_000
	must(edit(e))
	if g = get(); !g.OccurredAt.Equal(when) || g.AmountPaise != 53_000 {
		t.Fatalf("date moved without being sent: %v", g.OccurredAt)
	}

	// Another account: ids and bank details follow.
	e = same()
	e.Source = &SourcePatch{AccountID: a2.ID.Hex()}
	must(edit(e))
	if g = get(); g.AccountID == nil || *g.AccountID != a2.ID || g.Bank != "FEDERAL" || g.AccountLast4 != "2222" || g.CardID != nil {
		t.Fatalf("source = %+v", g)
	}
	// Cash: everything about the account goes.
	e = same()
	e.Source = &SourcePatch{}
	must(edit(e))
	if g = get(); g.AccountID != nil || g.CardID != nil || g.Bank != "" || g.AccountLast4 != "" {
		t.Fatalf("cash left account details: %+v", g)
	}
	for _, bad := range []*SourcePatch{{AccountID: bson.NewObjectID().Hex()}, {CardID: bson.NewObjectID().Hex()}, {AccountID: a1.ID.Hex(), CardID: bson.NewObjectID().Hex()}, {AccountID: "nope"}} {
		e = same()
		e.Source = bad
		var perr ErrPatch
		if err := edit(e); !errors.As(err, &perr) {
			t.Fatalf("source %+v: err = %v, want a 400-type error", bad, err)
		}
	}

	// A new payee: a known vendor settles the review and gives its category; an unknown one is kept as text.
	if err := s.col.FindOneAndUpdate(ctx, bson.M{"_id": made.ID}, bson.M{"$set": bson.M{"needs_review": true, "review_reason": "unknown_vendor"}, "$unset": bson.M{"category_id": "", "vendor_id": ""}}).Err(); err != nil {
		t.Fatal(err)
	}
	e = same()
	name := "zomato"
	e.Vendor = &name
	must(edit(e))
	g = get()
	if g.VendorID == nil || g.CategoryID == nil || *g.CategoryID != food.ID || g.NeedsReview || g.ReviewReason != "" || g.RawVendor != "zomato" {
		t.Fatalf("known vendor: %+v", g)
	}
	other := "Corner shop"
	e.Vendor = &other
	must(edit(e))
	if g = get(); g.VendorID != nil || g.RawVendor != "Corner shop" {
		t.Fatalf("unknown vendor: %+v", g)
	}
	// A category picked by hand survives a vendor change.
	must(s.Patch(ctx, made.ID, Patch{CategoryID: ptr(food.ID.Hex())}))
	other2, _ := cat.Create(ctx, "Treats", "")
	must(s.Patch(ctx, made.ID, Patch{CategoryID: ptr(other2.ID.Hex())}))
	e = same()
	e.Vendor = &name
	must(edit(e))
	if g = get(); g.CategoryID == nil || *g.CategoryID != other2.ID {
		t.Fatalf("a hand-picked category was overwritten: %v", g.CategoryID)
	}
	empty := " "
	e.Vendor = &empty
	must(edit(e))
	if g = get(); g.VendorID != nil || g.RawVendor != "" {
		t.Fatalf("cleared vendor: %+v", g)
	}

	// Rules that depend on other data.
	must(s.Patch(ctx, made.ID, Patch{Splits: &[]SplitPatch{{CategoryID: food.ID.Hex(), Paise: 20_000}, {CategoryID: other2.ID.Hex(), Paise: 33_000}}}))
	e = same()
	e.AmountPaise = 60_000
	if err := edit(e); err == nil {
		t.Fatal("changing the amount of a split payment should be refused")
	}
	e.AmountPaise = 53_000
	must(edit(e)) // same amount is fine
	must(s.Patch(ctx, made.ID, Patch{Splits: &[]SplitPatch{}}))

	// An edit cannot ride along with the other patch fields, and a missing transaction is a 404 case.
	e = same()
	if err := s.Patch(ctx, made.ID, Patch{Edit: &e, CategoryID: ptr(food.ID.Hex())}); err == nil {
		t.Fatal("edit plus category should be refused")
	}
	if err := s.Patch(ctx, bson.NewObjectID(), Patch{Edit: &e}); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatalf("missing transaction: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
