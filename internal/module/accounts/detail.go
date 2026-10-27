package accounts

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
)

// flow is the part of a transaction the account page needs. The transactions package imports this one, so it cannot be used here.
type flow struct {
	Type        string         `bson:"type"`
	Direction   string         `bson:"direction"`
	AmountPaise int64          `bson:"amount_paise"`
	OccurredAt  time.Time      `bson:"occurred_at"`
	ReceivedAt  time.Time      `bson:"received_at"`
	RawVendor   string         `bson:"raw_vendor"`
	UPIID       string         `bson:"upi_id"`
	CategoryID  *bson.ObjectID `bson:"category_id"`
	NeedsReview bool           `bson:"needs_review"`
}

// at is when the money moved: the SMS arrival time, falling back to the date written in it.
func (f flow) at() time.Time {
	if f.ReceivedAt.IsZero() {
		return f.OccurredAt
	}
	return f.ReceivedAt
}

// signedPaise is the effect on the balance. Direction decides; older rows without it fall back to type. Same rule as List.
func (f flow) signedPaise() int64 {
	switch {
	case f.Direction == "credit":
		return f.AmountPaise
	case f.Direction == "debit":
		return -f.AmountPaise
	}
	switch f.Type {
	case "income", "refund":
		return f.AmountPaise
	case "expense", "atm", "emi", "card_bill_payment":
		return -f.AmountPaise
	}
	return 0
}

func (f flow) payee() string {
	if f.RawVendor != "" {
		return f.RawVendor
	}
	if f.UPIID != "" {
		return f.UPIID
	}
	return "Unknown"
}

// isSpend is real spending: not a transfer or a card bill payment.
func (f flow) isSpend() bool {
	return (f.Type == "expense" || f.Type == "atm" || f.Type == "emi") && f.Direction != "credit"
}

type DayBalance struct {
	Date  string `json:"date"` // YYYY-MM-DD, IST
	Paise int64  `json:"paise"`
}

type Payee struct {
	Name  string `json:"name"`
	Paise int64  `json:"paise"`
	Count int    `json:"count"`
}

type CategoryTotal struct {
	CategoryID *bson.ObjectID `json:"category_id,omitempty"`
	Paise      int64          `json:"paise"`
}

// LinkedSip is a SIP that debits this account.
type LinkedSip struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"` // rupees, as in the funds module
	Day    int     `json:"day"`
	InDays int     `json:"inDays"`
	Genius bool    `json:"genius,omitempty"`
}

// Detail is everything the account page shows besides the transaction list.
type Detail struct {
	View
	MonthStart  string          `json:"month_start"`
	InPaise     int64           `json:"in_paise"`  // money in so far this month
	OutPaise    int64           `json:"out_paise"` // money out so far this month
	MonthTxns   int             `json:"month_txns"`
	TotalTxns   int             `json:"total_txns"` // since the opening balance
	NeedsReview int             `json:"needs_review"`
	Series      []DayBalance    `json:"series"` // end-of-day balance, last 30 days, oldest first
	TopPayees   []Payee         `json:"top_payees"`
	ByCategory  []CategoryTotal `json:"by_category"` // spending, last 30 days
	Sips        []LinkedSip     `json:"sips"`
	SipMonthly  float64         `json:"sip_monthly"`
}

const seriesDays = 30

// Series walks back from today's balance: the balance at the end of a day is today's minus everything that moved after that day.
func Series(balance int64, flows []flow, now time.Time) []DayBalance {
	out := make([]DayBalance, seriesDays)
	today := ist.Midnight(now)
	for i := 0; i < seriesDays; i++ {
		day := today.AddDate(0, 0, -(seriesDays - 1 - i))
		end := day.AddDate(0, 0, 1)
		b := balance
		for _, f := range flows {
			if !f.at().Before(end) { // moved after the end of that day
				b -= f.signedPaise()
			}
		}
		out[i] = DayBalance{Date: day.Format("2006-01-02"), Paise: b}
	}
	return out
}

// MonthFlows totals money in and out since the first of the month.
func MonthFlows(flows []flow, now time.Time) (in, out int64, n int) {
	n0 := now.In(ist.Loc)
	start := time.Date(n0.Year(), n0.Month(), 1, 0, 0, 0, 0, ist.Loc)
	for _, f := range flows {
		if f.at().Before(start) {
			continue
		}
		n++
		if s := f.signedPaise(); s > 0 {
			in += s
		} else {
			out -= s
		}
	}
	return in, out, n
}

// TopPayees are where the most money went since `since`, biggest first.
func TopPayees(flows []flow, since time.Time, limit int) []Payee {
	by := map[string]*Payee{}
	for _, f := range flows {
		if f.at().Before(since) || f.signedPaise() >= 0 || !f.isSpend() {
			continue
		}
		k := strings.ToLower(f.payee())
		if by[k] == nil {
			by[k] = &Payee{Name: f.payee()}
		}
		by[k].Paise -= f.signedPaise()
		by[k].Count++
	}
	out := make([]Payee, 0, len(by))
	for _, p := range by {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Paise != out[j].Paise {
			return out[i].Paise > out[j].Paise
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// SpendByCategory is spending since `since` per category, biggest first; uncategorised spending has no id.
func SpendByCategory(flows []flow, since time.Time) []CategoryTotal {
	totals := map[bson.ObjectID]int64{}
	var none int64
	for _, f := range flows {
		if f.at().Before(since) || f.signedPaise() >= 0 || !f.isSpend() {
			continue
		}
		if f.CategoryID == nil {
			none -= f.signedPaise()
		} else {
			totals[*f.CategoryID] -= f.signedPaise()
		}
	}
	out := []CategoryTotal{}
	for id, p := range totals {
		id := id
		out = append(out, CategoryTotal{CategoryID: &id, Paise: p})
	}
	if none > 0 {
		out = append(out, CategoryTotal{Paise: none})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Paise > out[j].Paise })
	return out
}

// Detail builds the account page's figures. mongo.ErrNoDocuments when there is no such account.
func (s *Service) Detail(ctx context.Context, id bson.ObjectID, now time.Time) (*Detail, error) {
	views, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var v *View
	for i := range views {
		if views[i].ID == id {
			v = &views[i]
		}
	}
	if v == nil {
		return nil, mongo.ErrNoDocuments
	}
	cur, err := s.txs.Find(ctx, bson.M{"account_id": id, "$expr": bson.M{"$gte": bson.A{bson.M{"$ifNull": bson.A{"$received_at", "$occurred_at"}}, v.OpeningAt}}},
		options.Find().SetSort(bson.D{{Key: "received_at", Value: -1}}).SetLimit(20000))
	if err != nil {
		return nil, err
	}
	var flows []flow
	if err := cur.All(ctx, &flows); err != nil {
		return nil, err
	}

	d := &Detail{View: *v, TotalTxns: len(flows), Series: Series(v.BalancePaise, flows, now), Sips: []LinkedSip{}}
	d.InPaise, d.OutPaise, d.MonthTxns = MonthFlows(flows, now)
	n := now.In(ist.Loc)
	d.MonthStart = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, ist.Loc).Format("2006-01-02")
	since := ist.Midnight(now).AddDate(0, 0, -(seriesDays - 1))
	d.TopPayees, d.ByCategory = TopPayees(flows, since, 5), SpendByCategory(flows, since)
	for _, f := range flows {
		if f.NeedsReview {
			d.NeedsReview++
		}
	}
	if d.Sips, err = s.linkedSips(ctx, id, now); err != nil {
		return nil, err
	}
	for _, p := range d.Sips {
		d.SipMonthly += p.Amount
	}
	return d, nil
}

// linkedSips reads the funds module's data: live SIPs that debit this account, standalone ones and the Genius basket's.
func (s *Service) linkedSips(ctx context.Context, id bson.ObjectID, now time.Time) ([]LinkedSip, error) {
	hex := id.Hex()
	live := func(status string) bool { return status == "" || status == "active" }
	out := []LinkedSip{}

	cur, err := s.funds.Find(ctx, bson.M{"accountId": hex, "membership": bson.M{"$ne": "Genius"}, "status": bson.M{"$ne": "exited"}, "sip": bson.M{"$exists": true}})
	if err != nil {
		return nil, err
	}
	var fs []struct {
		Name string `bson:"name"`
		Sip  struct {
			Amount float64 `bson:"amount"`
			Day    int     `bson:"day"`
			Status string  `bson:"status"`
		} `bson:"sip"`
	}
	if err := cur.All(ctx, &fs); err != nil {
		return nil, err
	}
	for _, f := range fs {
		if live(f.Sip.Status) {
			out = append(out, LinkedSip{Name: f.Name, Amount: f.Sip.Amount, Day: f.Sip.Day, InDays: ist.DaysUntil(f.Sip.Day, now)})
		}
	}

	var g struct {
		Sip struct {
			Amount    float64 `bson:"amount"`
			Day       int     `bson:"day"`
			Status    string  `bson:"status"`
			AccountID string  `bson:"accountId"`
		} `bson:"sip"`
	}
	switch err := s.fundSettings.FindOne(ctx, bson.M{"_id": "genius"}).Decode(&g); {
	case err == mongo.ErrNoDocuments:
	case err != nil:
		return nil, err
	case g.Sip.AccountID == hex && g.Sip.Amount > 0 && live(g.Sip.Status):
		out = append(out, LinkedSip{Name: "Genius basket", Amount: g.Sip.Amount, Day: g.Sip.Day, InDays: ist.DaysUntil(g.Sip.Day, now), Genius: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InDays < out[j].InDays })
	return out, nil
}
