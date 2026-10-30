// importcard adds the rows of a credit card statement (a JSON file of rows read from the PDF) that the SMS pipeline has not already recorded.
// It is a dry run unless -apply is given. The file's rows must reconcile with the statement's own totals, or nothing is imported.
// Vendors are matched exactly as SMS parsing does, so unknown merchants land in review.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/raw_transactions"
	"ledger-backend/internal/module/transactions"
	"ledger-backend/internal/module/vendors"
)

var ist = time.FixedZone("IST", 5*3600+1800)

type stmtRow struct {
	Date     string `json:"date"`
	Vendor   string `json:"vendor"`
	Paise    int64  `json:"paise"`
	Kind     string `json:"kind"` // spend | refund | payment | points
	Place    string `json:"place"`
	Category string `json:"category"`
	Note     string `json:"note"`
}

type statement struct {
	CardLast4     string    `json:"card_last4"`
	Bank          string    `json:"bank"`
	StatementDate string    `json:"statement_date"`
	OpeningPaise  int64     `json:"opening_paise"`
	Rows          []stmtRow `json:"rows"`
	RawTotals     struct {
		Spends              int64 `json:"spends"`
		RefundsAndRepayment int64 `json:"refunds_and_repayments"`
		Points              int64 `json:"points"`
		TotalDue            int64 `json:"total_due"`
	} `json:"totals"`
}

// reconcile checks the rows against the statement's printed totals: opening + spends - refunds and repayments - points = total due.
func (s *statement) reconcile() error {
	var spend, rr, pts int64
	for _, r := range s.Rows {
		switch r.Kind {
		case "spend":
			spend += r.Paise
		case "refund", "payment":
			rr += r.Paise
		case "points":
			pts += r.Paise
		default:
			return fmt.Errorf("row %s %s: unknown kind %q", r.Date, r.Vendor, r.Kind)
		}
	}
	t := s.RawTotals
	switch {
	case spend != t.Spends:
		return fmt.Errorf("spends add up to %d but the statement says %d", spend, t.Spends)
	case rr != t.RefundsAndRepayment:
		return fmt.Errorf("refunds and repayments add up to %d but the statement says %d", rr, t.RefundsAndRepayment)
	case pts != t.Points:
		return fmt.Errorf("points add up to %d but the statement says %d", pts, t.Points)
	case s.OpeningPaise+spend-rr-pts != t.TotalDue:
		return fmt.Errorf("opening + spends - repayments - points = %d but the statement says %d is due", s.OpeningPaise+spend-rr-pts, t.TotalDue)
	}
	return nil
}

func main() {
	apply := flag.Bool("apply", false, "write to the database (default: dry run)")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: importcard [-apply] statement.json")
	}
	b, err := os.ReadFile(flag.Arg(0))
	check(err)
	var st statement
	check(json.Unmarshal(b, &st))
	if err := st.reconcile(); err != nil {
		log.Fatalf("statement does not reconcile, nothing imported: %v", err)
	}
	fmt.Printf("statement reconciles: %d rows, total due %.2f\n", len(st.Rows), float64(st.RawTotals.TotalDue)/100)

	_ = godotenv.Load(".env")
	client, err := mongo.Connect(options.Client().ApplyURI(os.Getenv("MONGODB_URI")))
	check(err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db := client.Database(os.Getenv("MONGODB_DB"))
	txCol, rawCol := db.Collection(transactions.Collection), db.Collection(raw_transactions.Collection)

	cSvc, err := cards.NewService(ctx, db)
	check(err)
	vSvc, err := vendors.NewService(ctx, db)
	check(err)
	card, err := cSvc.Match(ctx, st.Bank, st.CardLast4)
	check(err)
	if card == nil {
		log.Fatalf("no card with last 4 digits %s in the app: add it on the Cards page first", st.CardLast4)
	}
	vs, err := vSvc.List(ctx)
	check(err)
	fmt.Printf("card: %s (%s)\n", card.Name, card.ID.Hex())

	first, _ := time.ParseInLocation("2006-01-02", st.Rows[0].Date, ist)
	last, _ := time.ParseInLocation("2006-01-02", st.Rows[len(st.Rows)-1].Date, ist)
	cur, err := txCol.Find(ctx, bson.M{"occurred_at": bson.M{"$gte": first.AddDate(0, 0, -3).UTC(), "$lt": last.AddDate(0, 0, 4).UTC()},
		"$or": bson.A{bson.M{"card_id": card.ID}, bson.M{"paid_card_id": card.ID}, bson.M{"account_last4": st.CardLast4}}})
	check(err)
	var existing []transactions.Transaction
	check(cur.All(ctx, &existing))
	used := make([]bool, len(existing))
	fmt.Printf("%d transactions already recorded for this card in the window\n", len(existing))

	day := func(t time.Time) string { return t.In(ist).Format("2006-01-02") }
	// The same payment when the amount and the day match (the SMS can be a day off), each existing row used at most once.
	seen := func(r stmtRow, d time.Time) *transactions.Transaction {
		for i := range existing {
			t := &existing[i]
			if used[i] || t.AmountPaise != r.Paise {
				continue
			}
			if day(t.OccurredAt) == day(d) || day(t.OccurredAt) == day(d.AddDate(0, 0, 1)) || day(t.OccurredAt) == day(d.AddDate(0, 0, -1)) {
				used[i] = true
				return t
			}
		}
		return nil
	}

	added := 0
	for _, r := range st.Rows {
		d, err := time.ParseInLocation("2006-01-02", r.Date, ist)
		check(err)
		label := fmt.Sprintf("%s %-7s %10.2f %-16s %s", r.Date, r.Kind, float64(r.Paise)/100, r.Vendor, r.Note)
		if t := seen(r, d); t != nil {
			extra := ""
			if r.Kind == "payment" && t.PaidCardID == nil {
				extra = "  <- a bank debit of this amount exists but is not linked to the card: pick this card on it in Transactions"
			}
			fmt.Println("skip (already recorded)   ", label, extra)
			continue
		}
		msg := fmt.Sprintf("CARD STATEMENT %s %s %s %s %d %s", st.CardLast4, st.StatementDate, r.Date, r.Vendor, r.Paise, r.Kind)
		if n, _ := rawCol.CountDocuments(ctx, bson.M{"message": msg}); n > 0 {
			fmt.Println("skip (already imported)   ", label)
			continue
		}
		t := transactions.Transaction{
			ID: bson.NewObjectID(), RawID: bson.NewObjectID(), Type: "expense", AmountPaise: r.Paise, Direction: "debit",
			OccurredAt: d.UTC(), ReceivedAt: d.UTC(), Bank: st.Bank, AccountLast4: st.CardLast4, CardID: &card.ID,
			RawVendor: r.Vendor, Model: "statement-import", PromptVersion: "stmt-v1", CreatedAt: time.Now().UTC(),
		}
		switch r.Kind {
		case "refund", "points": // points redeemed reduce the bill like a refund; both come off the net spend
			t.Type, t.Direction = "refund", "credit"
		case "payment":
			t.Type, t.Direction, t.PaidCardID, t.TypeBefore = "card_bill_payment", "credit", &card.ID, "transfer"
		}
		if r.Kind == "spend" || r.Kind == "refund" {
			if v := vendors.MatchIn(vs, r.Vendor, r.Vendor, "", r.Vendor); v != nil {
				t.VendorID, t.CategoryID = &v.ID, v.CategoryID
			} else {
				t.NeedsReview, t.ReviewReason = true, "unknown_vendor"
			}
		}
		note := "vendor matched"
		if t.VendorID == nil && (r.Kind == "spend" || r.Kind == "refund") {
			note = "vendor unknown (goes to review)"
		}
		fmt.Println("ADD ", label, note)
		added++
		if !*apply {
			continue
		}
		_, err = rawCol.InsertOne(ctx, bson.M{"_id": t.RawID, "message": msg, "received_at": t.ReceivedAt, "status": raw_transactions.StatusParsed})
		check(err)
		_, err = txCol.InsertOne(ctx, t)
		check(err)
	}
	fmt.Printf("%d to add (apply=%v)\n", added, *apply)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
