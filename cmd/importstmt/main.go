// importstmt adds the rows of a Union Bank statement (text from `pdftotext -raw`) that the SMS pipeline has not already recorded.
// It is a dry run unless -apply is given. Vendors and accounts are matched exactly as SMS parsing does, so unknown payees land in review.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/raw_transactions"
	"ledger-backend/internal/module/transactions"
	"ledger-backend/internal/module/vendors"
)

const (
	bank  = "Union Bank of India"
	last4 = "3109"
)

var (
	ist     = time.FixedZone("IST", 5*3600+1800)
	rowRe   = regexp.MustCompile(`^(\d{2}-\d{2}-\d{4}) (\S+) (.+) ([\d.]+)\((Cr|Dr)\) ([\d.]+)\((?:Cr|Dr)\)$`)
	refRe   = regexp.MustCompile(`\d{12}`)
	selfRe  = regexp.MustCompile(`(?i)sangeeth(promod)?\b`)
	remarks = regexp.MustCompile(`^UPIA[BR]/(\d+)/(CR|DR|REV)/([^/]*)/[^/]*/(.*)$`)
)

type row struct {
	date          time.Time
	id, text, ref string
	paise         int64
	credit        bool
	balance       int64
	name, upi     string
	typ           string
}

func paise(s string) int64 {
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f*100 + 0.5)
}

func parse(path string) []row {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var rows []row
	for _, l := range strings.Split(string(b), "\n") {
		m := rowRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		d, _ := time.ParseInLocation("02-01-2006", m[1], ist)
		r := row{date: d, id: m[2], text: m[3], paise: paise(m[4]), credit: m[5] == "Cr", balance: paise(m[6])}
		if x := remarks.FindStringSubmatch(r.text); x != nil {
			r.ref, r.name, r.upi = x[1], strings.TrimSpace(x[3]), strings.ToLower(strings.TrimSpace(x[4]))
			if !strings.Contains(r.upi, "@") {
				r.upi = ""
			}
			if x[2] == "REV" { // the payee slot holds the reversal id; the payee is whoever the same ref was paid to
				r.typ, r.name = "refund", ""
				for _, o := range rows {
					if o.ref == r.ref && o.name != "" {
						r.name = o.name
					}
				}
			}
		} else if strings.HasPrefix(r.text, "eTXN/") {
			r.name, r.typ = "emi", "emi"
		}
		rows = append(rows, r)
	}
	// ponytail: the first row's opening balance is unknown, so only the chain between rows is checked.
	for i := 1; i < len(rows); i++ {
		d := rows[i].paise
		if !rows[i].credit {
			d = -d
		}
		if rows[i-1].balance+d != rows[i].balance {
			log.Fatalf("balance chain breaks at %s %s: parse is wrong", rows[i].date.Format("02-01"), rows[i].id)
		}
	}
	// A credit from a payee we also paid in the statement is a refund; other credits are income; own-name rows are transfers.
	paid := map[string]bool{}
	for _, r := range rows {
		if !r.credit {
			paid[strings.ToLower(r.name)] = true
		}
	}
	for i := range rows {
		r := &rows[i]
		switch {
		case r.typ != "":
		case selfRe.MatchString(r.name):
			r.typ = "transfer"
		case r.credit && paid[strings.ToLower(r.name)]:
			r.typ = "refund"
		case r.credit:
			r.typ = "income"
		default:
			r.typ = "expense"
		}
	}
	return rows
}

func main() {
	apply := flag.Bool("apply", false, "write to the database (default: dry run)")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: importstmt [-apply] statement.txt")
	}
	rows := parse(flag.Arg(0))

	_ = godotenv.Load(".env")
	client, err := mongo.Connect(options.Client().ApplyURI(os.Getenv("MONGODB_URI")))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db := client.Database(os.Getenv("MONGODB_DB"))
	txCol, rawCol := db.Collection(transactions.Collection), db.Collection(raw_transactions.Collection)

	vSvc, err := vendors.NewService(ctx, db)
	check(err)
	aSvc, err := accounts.NewService(ctx, db)
	check(err)
	vs, err := vSvc.List(ctx)
	check(err)
	acc, err := aSvc.Match(ctx, bank, last4)
	check(err)

	lo, hi := rows[0].date.AddDate(0, 0, -1), rows[len(rows)-1].date.AddDate(0, 0, 2)
	cur, err := txCol.Find(ctx, bson.M{"account_last4": last4, "occurred_at": bson.M{"$gte": lo.UTC(), "$lt": hi.UTC()}})
	check(err)
	var existing []transactions.Transaction
	check(cur.All(ctx, &existing))
	used := make([]bool, len(existing))
	fmt.Printf("%d transactions already recorded for %s %s in this window (statement has %d rows)\n", len(existing), bank, last4, len(rows))

	// An SMS row is the same payment when its ref appears in the remarks (a reversal SMS quotes the original ref);
	// failing that, same day, amount and direction, each SMS used at most once.
	seen := func(r row) bool {
		credit := "debit"
		if r.credit {
			credit = "credit"
		}
		for i, t := range existing {
			if used[i] {
				continue
			}
			if (t.Ref != "" && strings.Contains(r.text, t.Ref) && t.AmountPaise == r.paise) ||
				(t.AmountPaise == r.paise && t.Direction == credit && t.OccurredAt.In(ist).Format("2006-01-02") == r.date.Format("2006-01-02")) {
				used[i] = true
				return true
			}
		}
		return false
	}

	added := 0
	for _, r := range rows {
		dir := "debit"
		if r.credit {
			dir = "credit"
		}
		label := fmt.Sprintf("%s %-8s %9.2f %-6s %-9s %-10s", r.date.Format("02-01"), r.typ, float64(r.paise)/100, dir, r.name, r.ref)
		if seen(r) {
			fmt.Println("skip (SMS already has it)", label)
			continue
		}
		msg := fmt.Sprintf("UBI STATEMENT %s %s %s %.2f %s", r.date.Format("02-01-2006"), r.id, r.text, float64(r.paise)/100, dir)
		if n, _ := rawCol.CountDocuments(ctx, bson.M{"message": msg}); n > 0 {
			fmt.Println("skip (already imported)     ", label)
			continue
		}
		t := transactions.Transaction{
			ID: bson.NewObjectID(), RawID: bson.NewObjectID(), Type: r.typ, AmountPaise: r.paise,
			OccurredAt: r.date.UTC(), ReceivedAt: r.date.UTC(), Bank: bank, AccountLast4: last4, Direction: dir,
			Ref: r.ref, RawVendor: r.name, UPIID: r.upi, Model: "statement-import", PromptVersion: "stmt-v1", CreatedAt: time.Now().UTC(),
		}
		if acc != nil {
			t.AccountID = &acc.ID
		}
		if v := vendors.MatchIn(vs, r.name, r.name, r.upi, r.text); v != nil {
			t.VendorID, t.CategoryID = &v.ID, v.CategoryID
		} else {
			t.NeedsReview, t.ReviewReason = true, "unknown_vendor"
		}
		note := "vendor=none (review)"
		if t.VendorID != nil {
			note = "vendor matched"
		}
		fmt.Println("ADD ", label, note)
		added++
		if !*apply {
			continue
		}
		_, err := rawCol.InsertOne(ctx, bson.M{"_id": t.RawID, "message": msg, "received_at": t.ReceivedAt, "status": raw_transactions.StatusParsed})
		check(err)
		_, err = txCol.InsertOne(ctx, t)
		check(err)
	}
	fmt.Printf("%d to add (apply=%v, account linked=%v)\n", added, *apply, acc != nil)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
