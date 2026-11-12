// importkotak reconciles a Kotak Bank statement (text from `pdftotext -raw -f 1 -l 2`) with the transactions already recorded for that account.
// It lists statement rows the SMS pipeline missed (ADD) and recorded rows the statement does not have (EXTRA).
// It is a dry run unless -apply is given; -apply inserts the ADD rows and, with -delete, removes the EXTRA rows.
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
	bank  = "Kotak Bank"
	last4 = "9959"
)

var (
	ist     = time.FixedZone("IST", 5*3600+1800)
	startRe = regexp.MustCompile(`^\d+ (\d{2} \w{3} \d{4}) (.*)$`)
	tailRe  = regexp.MustCompile(`^(.*?)\s*(UPI-\d{12})\s+([\d,]+\.\d\d)\s+([\d,]+\.\d\d)\s*$`)
	refRe   = regexp.MustCompile(`\d{12}`)
	selfRe  = regexp.MustCompile(`(?i)^SANGEETH`)
	openRe  = regexp.MustCompile(`Opening Balance[ -]+([\d,]+\.\d\d)`)
)

type row struct {
	date    time.Time
	text    string
	ref     string
	paise   int64
	credit  bool
	balance int64
	name    string
	upi     string
	typ     string
}

func paise(s string) int64 {
	f, _ := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if f < 0 {
		return -int64(-f*100 + 0.5)
	}
	return int64(f*100 + 0.5)
}

func parse(path string) []row {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	body := strings.ReplaceAll(string(b), "\r", "")
	om := openRe.FindStringSubmatch(body)
	if om == nil {
		log.Fatal("no opening balance")
	}
	prev := paise(om[1])
	var rows []row
	var open []string // narration lines of the row being read
	var date time.Time
	skip := []string{"Savings Account", "# Date", "Statement Generated", "SANGEETH PROMOD", "Account No", "Account Statement"}
scan:
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "Account Summary") {
			break
		}
		for _, s := range skip {
			if strings.HasPrefix(l, s) {
				continue scan
			}
		}
		if m := startRe.FindStringSubmatch(l); m != nil {
			date, _ = time.ParseInLocation("02 Jan 2006", m[1], ist)
			open, l = nil, m[2]
		} else if open == nil {
			if l != "" && len(rows) > 0 { // a row's narration can spill onto the next page
				rows[len(rows)-1].text += " " + l
			}
			continue
		}
		if t := tailRe.FindStringSubmatch(l); t != nil {
			open = append(open, t[1])
			rows = append(rows, row{date: date, text: strings.TrimSpace(strings.Join(open, " ")) + " " + t[2], paise: paise(t[3]), balance: paise(t[4])})
			open = nil
		} else if l != "" {
			open = append(open, l)
		}
	}
	// Direction comes from the balance chain, not the column.
	for i := range rows {
		r := &rows[i]
		switch {
		case prev-r.paise == r.balance:
		case prev+r.paise == r.balance:
			r.credit = true
		default:
			log.Fatalf("balance chain breaks at %s %.2f: parse is wrong", r.date.Format("02-01"), float64(r.paise)/100)
		}
		prev = r.balance
		classify(r)
	}
	return rows
}

func classify(r *row) {
	parts := strings.Split(r.text, "/")
	if parts[0] == "UPI" && len(parts) > 1 {
		r.name = strings.TrimSpace(parts[1])
		if strings.Contains(parts[1], "@") { // a bare VPA is the payee
			r.upi = strings.ToLower(r.name)
		}
	}
	r.ref = refRe.FindString(r.text)
	switch {
	case selfRe.MatchString(r.name):
		r.typ = "transfer"
	case r.credit:
		r.typ = "income"
	default:
		r.typ = "expense"
	}
}

func main() {
	apply := flag.Bool("apply", false, "write to the database (default: dry run)")
	del := flag.Bool("delete", false, "with -apply, also delete recorded rows the statement does not have")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: importkotak [-apply [-delete]] statement.txt")
	}
	rows := parse(flag.Arg(0))
	var dr, cr, dn, cn int64
	for _, r := range rows {
		if r.credit {
			cr, cn = cr+r.paise, cn+1
		} else {
			dr, dn = dr+r.paise, dn+1
		}
	}
	fmt.Printf("parsed %d rows: debits %.2f (%d), credits %.2f (%d)\n", len(rows), float64(dr)/100, dn, float64(cr)/100, cn)

	_ = godotenv.Load(".env")
	client, err := mongo.Connect(options.Client().ApplyURI(os.Getenv("SANGEETH_MONGODB_URI")))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db := client.Database(os.Getenv("SANGEETH_MONGODB_DB"))
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
	fmt.Printf("%d transactions already recorded for %s %s in this window\n", len(existing), bank, last4)

	day := func(t time.Time) string { return t.In(ist).Format("2006-01-02") }
	dirOf := func(c bool) string {
		if c {
			return "credit"
		}
		return "debit"
	}
	// Same payment: the recorded ref appears in the narration and the amount agrees; failing that, same day, amount and direction.
	// Each recorded row is used at most once; ref matches go first so a day/amount guess cannot steal a better pairing.
	matched := make([]bool, len(rows))
	for pass := 0; pass < 2; pass++ {
		for ri, r := range rows {
			if matched[ri] {
				continue
			}
			for i, t := range existing {
				if used[i] || t.AmountPaise != r.paise {
					continue
				}
				byRef := t.Ref != "" && strings.Contains(r.text+" "+r.ref, t.Ref)
				byDay := t.Direction == dirOf(r.credit) && day(t.OccurredAt) == r.date.Format("2006-01-02")
				if (pass == 0 && byRef) || (pass == 1 && byDay) {
					used[i], matched[ri] = true, true
					break
				}
			}
		}
	}

	added := 0
	for ri, r := range rows {
		if matched[ri] {
			continue
		}
		dir := dirOf(r.credit)
		label := fmt.Sprintf("%s %-8s %9.2f %-6s %-28.28s %s", r.date.Format("02-01"), r.typ, float64(r.paise)/100, dir, r.name, r.ref)
		msg := fmt.Sprintf("KOTAK STATEMENT %s %s %.2f %s", r.date.Format("02-01-2006"), r.text, float64(r.paise)/100, dir)
		if n, _ := rawCol.CountDocuments(ctx, bson.M{"message": msg}); n > 0 {
			fmt.Println("skip (already imported)", label)
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
		fmt.Println("ADD  ", label)
		added++
		if *apply {
			_, err := rawCol.InsertOne(ctx, bson.M{"_id": t.RawID, "message": msg, "received_at": t.ReceivedAt, "status": raw_transactions.StatusParsed})
			check(err)
			_, err = txCol.InsertOne(ctx, t)
			check(err)
		}
	}
	extra := 0
	for i, t := range existing {
		if used[i] {
			continue
		}
		extra++
		fmt.Printf("EXTRA %s %-8s %9.2f %-6s %-28.28s ref=%s id=%s model=%s\n", day(t.OccurredAt), t.Type, float64(t.AmountPaise)/100, t.Direction, t.RawVendor, t.Ref, t.ID.Hex(), t.Model)
		if *apply && *del {
			_, err := txCol.DeleteOne(ctx, bson.M{"_id": t.ID})
			check(err)
		}
	}
	fmt.Printf("%d to add, %d extra (apply=%v delete=%v, account linked=%v)\n", added, extra, *apply, *del, acc != nil)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
