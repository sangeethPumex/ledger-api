package copilot

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/brainjogger"
	"ledger-backend/internal/module/budgets"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/categories"
	"ledger-backend/internal/module/emis"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/insights"
	"ledger-backend/internal/module/mandates"
	"ledger-backend/internal/module/profile"
	"ledger-backend/internal/module/snapshots"
	"ledger-backend/internal/module/stocks"
)

// Runs every real pipeline end to end. Needs a real MongoDB, e.g. MONGODB_TEST_URI=mongodb://localhost:27017. Skipped otherwise.
func TestServiceEndToEnd(t *testing.T) {
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
	db := client.Database("copilot_it_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	acc, err := accounts.NewService(ctx, db)
	must(err)
	crd, err := cards.NewService(ctx, db)
	must(err)
	emi, err := emis.NewService(ctx, db)
	must(err)
	fnd, err := funds.NewService(ctx, db)
	must(err)
	bud, err := budgets.NewService(ctx, db)
	must(err)
	cat, err := categories.NewService(ctx, db)
	must(err)
	prof := profile.NewService(db)
	st := stocks.NewService(db, stocks.NewYahoo("http://127.0.0.1:1"))
	snap, err := snapshots.NewService(ctx, db, acc, crd, emi, fnd, st, prof)
	must(err)
	s, err := NewService(ctx, db, acc, crd, prof, emi, fnd, mandates.NewService(db), bud, cat, insights.NewService(db), snap, brainjogger.NewService(db))
	must(err)

	// 10 am IST, inside the hours nudges may go out.
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, ist.Loc)
	vendor := bson.NewObjectID()
	_, err = db.Collection("vendors").InsertOne(ctx, bson.M{"_id": vendor, "name": "Swiggy"})
	must(err)
	// Three orders this week, the largest today, with part of it owed back by a friend.
	var docs []any
	for i, paise := range []int64{50_000, 60_000, 900_000} {
		docs = append(docs, bson.M{"raw_id": bson.NewObjectID(), "type": "expense", "direction": "debit", "amount_paise": paise, "vendor_id": vendor, "occurred_at": now.Add(time.Duration(i-2) * time.Minute).UTC(), "owed_paise": int64(1000)})
	}
	_, err = db.Collection("transactions").InsertMany(ctx, docs)
	must(err)

	o, err := s.Overview(ctx, now)
	must(err)
	if want := int64(1_010_000 - 3000); o.Safe.SpentPaise != want {
		t.Fatalf("today's spend = %d, want %d (the owed part is not the user's)", o.Safe.SpentPaise, want)
	}
	if o.OwedPaise != 3000 {
		t.Fatalf("owed = %d", o.OwedPaise)
	}

	got, err := s.State(ctx, now)
	must(err)
	if len(got.Vendors) != 1 || got.Vendors[0].Name != "Swiggy" || got.Vendors[0].Count != 3 {
		t.Fatalf("vendors = %+v", got.Vendors)
	}
	if got.TopToday == nil || got.TopToday.Vendor != "Swiggy" {
		t.Fatalf("top = %+v", got.TopToday)
	}

	// The rules store at most the cap (the week brief is on top) and running again repeats nothing.
	must(s.Tick(ctx, now))
	first, err := s.nudges.CountDocuments(ctx, bson.D{})
	must(err)
	if first == 0 || first > maxPerDay+1 {
		t.Fatalf("stored %d nudges", first)
	}
	s.Invalidate()
	must(s.Tick(ctx, now))
	if again, _ := s.nudges.CountDocuments(ctx, bson.D{}); again != first {
		t.Fatalf("second run stored more: %d then %d", first, again)
	}

	// Night is silent.
	must(s.nudges.Drop(ctx))
	must(s.Tick(ctx, time.Date(2026, 11, 2, 23, 0, 0, 0, ist.Loc)))
	if n, _ := s.nudges.CountDocuments(ctx, bson.D{}); n != 0 {
		t.Fatalf("stored %d nudges at night", n)
	}

	// Inbox actions: snooze hides, dismiss moves to history, muting a kind stops it.
	must(s.Tick(ctx, now))
	in, err := s.Inbox(ctx, now)
	must(err)
	if len(in.Active) == 0 {
		t.Fatal("no active nudges")
	}
	id, kind, count := in.Active[0].ID, in.Active[0].Kind, len(in.Active)
	must(s.SetNudge(ctx, id, "snooze", now))
	if in, _ = s.Inbox(ctx, now); len(in.Active) != count-1 {
		t.Fatalf("snooze left %d active", len(in.Active))
	}
	if in, _ = s.Inbox(ctx, now.Add(25*time.Hour)); len(in.Active) != count {
		t.Fatalf("snooze should end after a day: %d active", len(in.Active))
	}
	must(s.SetNudge(ctx, id, "dismiss", now))
	must(s.SetMute(ctx, kind, true))
	in, err = s.Inbox(ctx, now)
	must(err)
	if len(in.History) != 1 || len(in.Muted) != 1 || in.Muted[0].Kind != kind {
		t.Fatalf("history %d, muted %+v", len(in.History), in.Muted)
	}
	if err := s.SetNudge(ctx, "nope", "dismiss", now); err != mongo.ErrNoDocuments {
		t.Fatalf("unknown id: %v", err)
	}

	// Wants: a parked one gets today's verdict; regret feeds the State.
	must(s.CreateWant(ctx, Want{Name: "Phone", AmountPaise: 100, Status: "parked"}, now))
	ws, err := s.Wants(ctx, now)
	must(err)
	if len(ws) != 1 || ws[0].Check == nil {
		t.Fatalf("wants = %+v", ws)
	}
	no := false
	must(s.SetWant(ctx, ws[0].ID, "bought", &no, now))
	got, err = s.State(ctx, now)
	must(err)
	if bad, all := got.regrets(); bad != 1 || all != 1 || got.Wants[0].BoughtAt == nil {
		t.Fatalf("regrets %d/%d, want %+v", bad, all, got.Wants[0])
	}
	must(s.DeleteWant(ctx, ws[0].ID))

	// The AI's tools answer in rupees.
	res, err := s.Tool(ctx, "purchase_check", 500, "", 0, now)
	must(err)
	if res.(map[string]any)["amountRupees"] != 500.0 {
		t.Fatalf("tool = %v", res)
	}
	if _, err := s.Tool(ctx, "purchase_check", 0, "", 0, now); err == nil {
		t.Fatal("a zero amount should be refused")
	}
}
