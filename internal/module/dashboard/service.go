package dashboard

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/spend"
	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/emis"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/mandates"
	"ledger-backend/internal/module/profile"
	"ledger-backend/internal/module/stocks"
	"ledger-backend/internal/nim"
)

// Chatter is the LLM call (nim.Client satisfies it).
type Chatter interface {
	Chat(ctx context.Context, system, user string) (string, error)
	Complete(ctx context.Context, system string, msgs []nim.Message, tools []nim.Tool) (nim.Message, error)
}

type Service struct {
	txs      *mongo.Collection // owned by the transactions module; read only
	months   *mongo.Collection // month-end net worth, owned here
	accounts *accounts.Service
	cards    *cards.Service
	profile  *profile.Service
	emis     *emis.Service
	funds    *funds.Service
	stocks   *stocks.Service
	mandates *mandates.Service
	llm      Chatter
	chats    *mongo.Collection // saved AI conversations, owned here

	sumMu sync.Mutex
	sums  map[string]cachedSummary // 60 s dashboard summaries for the AI paths
}

func NewService(db *mongo.Database, a *accounts.Service, c *cards.Service, p *profile.Service, e *emis.Service, f *funds.Service, st *stocks.Service, m *mandates.Service, llm Chatter) *Service {
	s := &Service{txs: db.Collection("transactions"), months: db.Collection("networth"), accounts: a, cards: c, profile: p, emis: e, funds: f, stocks: st, mandates: m, llm: llm, chats: db.Collection("ai_chats"), sums: map[string]cachedSummary{}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.chats.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "updatedAt", Value: -1}}}); err != nil {
		log.Printf("dashboard: chat index: %v", err)
	}
	if err := s.seed(ctx); err != nil {
		log.Printf("dashboard: seed net worth history: %v", err)
	}
	return s
}

type txnDoc struct {
	ID     bson.ObjectID  `bson:"id"`
	Vendor string         `bson:"vendor"`
	At     time.Time      `bson:"at"`
	Paise  int64          `bson:"paise"`
	Acc    *bson.ObjectID `bson:"acc"`
	Card   *bson.ObjectID `bson:"card"`
}

type flowDoc struct {
	ID struct {
		Dir  string `bson:"dir"`
		Name string `bson:"name"`
	} `bson:"_id"`
	Paise int64    `bson:"paise"`
	Count int      `bson:"count"`
	Txns  []txnDoc `bson:"txns"`
}

type dayDoc struct {
	ID  string `bson:"_id"`
	Sum int64  `bson:"paise"`
	Top struct {
		Vendor string `bson:"vendor"`
		Cat    string `bson:"cat"`
		Paise  int64  `bson:"paise"`
	} `bson:"top"`
}

// labelExpr names a transaction's counterparty after a $lookup of vendors into "ven": a saved vendor's name beats the raw SMS payee / UPI id.
func labelExpr() bson.M {
	str := func(path string) bson.M { return bson.M{"$ifNull": bson.A{path, ""}} }
	nonEmpty := func(path string) bson.M { return bson.M{"$gt": bson.A{bson.M{"$strLenCP": str(path)}, 0}} }
	return bson.M{"$ifNull": bson.A{bson.M{"$first": "$ven.name"}, bson.M{"$cond": bson.A{nonEmpty("$raw_vendor"), "$raw_vendor", bson.M{"$cond": bson.A{nonEmpty("$upi_id"), "$upi_id", "Unknown"}}}}}}
}

// aggregate classifies a window of transactions in one pipeline: totals per flow (income by payer, spend by category) with each flow's
// biggest transactions, and spend per IST day with the day's biggest transaction. Transfers, refunds and card-bill payments never match.
func (s *Service) aggregate(ctx context.Context, from, to time.Time) ([]flowDoc, []dayDoc, error) {
	pipe := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"occurred_at": bson.M{"$gte": from.UTC(), "$lt": to.UTC()},
			"$or": bson.A{
				bson.M{"type": "income", "direction": bson.M{"$ne": "debit"}},
				bson.M{"type": bson.M{"$in": bson.A{"expense", "atm", "emi"}}, "direction": bson.M{"$ne": "credit"}},
			},
		}}},
	}
	// The part a friend owes back is not the user's spend, and a split payment counts once per category; everything below sees only that.
	pipe = append(pipe, spend.Parts()...)
	pipe = append(pipe, mongo.Pipeline{
		{{Key: "$lookup", Value: bson.M{"from": "categories", "localField": "category_id", "foreignField": "_id", "as": "cat"}}},
		{{Key: "$lookup", Value: bson.M{"from": "vendors", "localField": "vendor_id", "foreignField": "_id", "as": "ven"}}},
		{{Key: "$addFields", Value: bson.M{
			"_dir": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$type", "income"}}, "in", "out"}},
			"_label": labelExpr(),
			"_cat":  bson.M{"$ifNull": bson.A{bson.M{"$first": "$cat.name"}, "Uncategorised"}},
		}}},
		{{Key: "$addFields", Value: bson.M{"_name": bson.M{"$switch": bson.M{
			"branches": bson.A{
				bson.M{"case": bson.M{"$eq": bson.A{"$_dir", "in"}}, "then": "$_label"},
				bson.M{"case": bson.M{"$eq": bson.A{"$type", "emi"}}, "then": "EMIs"},
			},
			"default": "$_cat",
		}}}}},
		{{Key: "$facet", Value: bson.M{
			"flows": bson.A{bson.M{"$group": bson.M{
				"_id":   bson.M{"dir": "$_dir", "name": "$_name"},
				"paise": bson.M{"$sum": "$amount_paise"},
				"count": bson.M{"$sum": 1},
				"txns": bson.M{"$topN": bson.M{"n": 8, "sortBy": bson.M{"amount_paise": -1},
					"output": bson.M{"id": "$_id", "vendor": "$_label", "at": bson.M{"$ifNull": bson.A{"$received_at", "$occurred_at"}}, "paise": "$amount_paise", "acc": "$account_id", "card": "$card_id"}}},
			}}},
			"daily": bson.A{
				bson.M{"$match": bson.M{"_dir": "out"}},
				bson.M{"$match": bson.M{"$expr": bson.M{"$not": bson.A{bson.M{"$regexMatch": bson.M{"input": "$_name", "regex": investPattern, "options": "i"}}}}}},
				bson.M{"$group": bson.M{
					"_id":   bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$occurred_at", "timezone": "Asia/Kolkata"}},
					"paise": bson.M{"$sum": "$amount_paise"},
					"top":   bson.M{"$top": bson.M{"sortBy": bson.M{"amount_paise": -1}, "output": bson.M{"vendor": "$_label", "cat": "$_cat", "paise": "$amount_paise"}}},
				}},
				bson.M{"$sort": bson.M{"_id": 1}},
			},
		}}},
	}...)
	cur, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return nil, nil, err
	}
	var res []struct {
		Flows []flowDoc `bson:"flows"`
		Daily []dayDoc  `bson:"daily"`
	}
	if err := cur.All(ctx, &res); err != nil || len(res) == 0 {
		return nil, nil, err
	}
	return res[0].Flows, res[0].Daily, nil
}

type names struct{ acc, card map[bson.ObjectID]string }

func (s *Service) names(ctx context.Context) (names, []accounts.View, error) {
	accs, err := s.accounts.List(ctx)
	if err != nil {
		return names{}, nil, err
	}
	cs, err := s.cards.List(ctx)
	if err != nil {
		return names{}, nil, err
	}
	n := names{acc: map[bson.ObjectID]string{}, card: map[bson.ObjectID]string{}}
	for _, a := range accs {
		n.acc[a.ID] = a.Name
	}
	for _, c := range cs {
		n.card[c.ID] = c.Name
	}
	return n, accs, nil
}

func (n names) source(acc, card *bson.ObjectID) string {
	if acc != nil {
		return n.acc[*acc]
	}
	if card != nil {
		return n.card[*card]
	}
	return ""
}

func (n names) flows(docs []flowDoc, dir string) []Flow {
	var out []Flow
	for _, d := range docs {
		if d.ID.Dir != dir {
			continue
		}
		f := Flow{ID: dir + "-" + d.ID.Name, Name: d.ID.Name, Paise: d.Paise, Count: d.Count, Txns: make([]TxnView, 0, len(d.Txns))}
		f.Tone = "income"
		if dir == "out" {
			f.Tone = classify(d.ID.Name)
		}
		for _, t := range d.Txns {
			f.Txns = append(f.Txns, TxnView{ID: t.ID.Hex(), Vendor: t.Vendor, Source: n.source(t.Acc, t.Card), At: t.At.UTC().Format(time.RFC3339), Paise: t.Paise})
		}
		out = append(out, f)
	}
	return out
}

type recentDoc struct {
	ID          bson.ObjectID  `bson:"_id"`
	Type        string         `bson:"type"`
	Direction   string         `bson:"direction"`
	AmountPaise int64          `bson:"amount_paise"`
	OccurredAt  time.Time      `bson:"occurred_at"`
	ReceivedAt  *time.Time     `bson:"received_at"`
	RawVendor   string         `bson:"raw_vendor"`
	UPIID       string         `bson:"upi_id"`
	Bank        string         `bson:"bank"`
	Last4       string         `bson:"account_last4"`
	AccountID   *bson.ObjectID `bson:"account_id"`
	CardID      *bson.ObjectID `bson:"card_id"`
}

func (s *Service) recent(ctx context.Context, r Range, n names, limit int64) ([]TxnView, error) {
	cur, err := s.txs.Find(ctx, bson.M{"occurred_at": bson.M{"$gte": r.From.UTC(), "$lt": r.To.UTC()}},
		options.Find().SetSort(bson.D{{Key: "received_at", Value: -1}, {Key: "occurred_at", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var docs []recentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]TxnView, 0, len(docs))
	for _, d := range docs {
		vendor := d.RawVendor
		if vendor == "" {
			vendor = d.UPIID
		}
		if vendor == "" {
			vendor = "Unknown"
		}
		src := n.source(d.AccountID, d.CardID)
		if src == "" && d.Last4 != "" {
			src = d.Bank
		}
		credit := d.Direction == "credit" || (d.Direction == "" && (d.Type == "income" || d.Type == "refund"))
		at := d.OccurredAt
		if d.ReceivedAt != nil {
			at = *d.ReceivedAt
		}
		out = append(out, TxnView{ID: d.ID.Hex(), Vendor: vendor, Source: src, At: at.UTC().Format(time.RFC3339), Paise: d.AmountPaise, Credit: credit})
	}
	return out, nil
}

type Position struct {
	Cash           int64        `json:"cashPaise"`
	Accounts       int          `json:"accounts"`
	NetWorth       int64        `json:"netWorthPaise"`                 // cash + investments; money owed is not subtracted
	Liabilities    int64        `json:"liabilitiesPaise"`              // money owed: outstanding EMIs and loans
	NetWorthChange *int64       `json:"netWorthChangePaise,omitempty"` // against the last recorded month-end
	ChangeSince    string       `json:"netWorthChangeSince,omitempty"`
	Invested       int64        `json:"investedPaise"`
	Current        int64        `json:"currentPaise"`
	Pnl            int64        `json:"pnlPaise"`
	Allocation     []Allocation `json:"allocation"`
	UsesFunds      bool         `json:"usesFunds"` // mutual funds come from tracked funds, not the typed bucket
}

type Allocation struct {
	Name  string `json:"name"`
	Paise int64  `json:"paise"`
}

// Mandates is what must be paid in the next 90 days (see the mandates module), soonest first.
type Mandates struct {
	Next30   int64          `json:"next30Paise"`
	Next90   int64          `json:"next90Paise"`
	SetAside int64          `json:"setAsidePaise"` // per month, across all mandates
	Due      []mandates.Occ `json:"due"`
}

type Response struct {
	Period       Period    `json:"period"`
	Position     Position  `json:"position"`
	Cashflow     Cashflow  `json:"cashflow"`
	Upcoming     Upcoming  `json:"upcoming"`
	OwedToYou    int64     `json:"owedToYouPaise"` // still to come back from people the user paid for
	Mandates     Mandates  `json:"mandates"`
	Recent       []TxnView `json:"recent"`
	NeedsProfile bool      `json:"needsProfile"`
}

// owedToYou is what is still outstanding on payments made for others.
func (s *Service) owedToYou(ctx context.Context) (int64, error) {
	cur, err := s.txs.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"owed_paise": bson.M{"$gt": 0}}}},
		{{Key: "$group", Value: bson.M{"_id": nil, "paise": bson.M{"$sum": bson.M{"$subtract": bson.A{"$owed_paise", bson.M{"$ifNull": bson.A{"$repaid_paise", 0}}}}}}}},
	})
	if err != nil {
		return 0, err
	}
	var res []struct {
		Paise int64 `bson:"paise"`
	}
	if err := cur.All(ctx, &res); err != nil || len(res) == 0 {
		return 0, err
	}
	return res[0].Paise, nil
}

// Get builds the whole dashboard for a period. It also nudges a background NAV refresh so fund values are fresh next time.
func (s *Service) Get(ctx context.Context, r Range, key string, now time.Time) (*Response, error) {
	s.funds.RefreshIfStale()
	n, accs, err := s.names(ctx)
	if err != nil {
		return nil, err
	}
	prof, err := s.profile.Get(ctx)
	if err != nil {
		return nil, err
	}
	emiSum, err := s.emis.Summary(ctx, now)
	if err != nil {
		return nil, err
	}
	fundSum, err := s.funds.Summary(ctx, now)
	if err != nil {
		return nil, err
	}

	flowDocs, periodDays, err := s.aggregate(ctx, r.From, r.To)
	if err != nil {
		return nil, err
	}
	// Spend rate inputs: a window long enough for both "last 30 days" and "since the 1st".
	today := ist.Midnight(now)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, ist.Loc)
	since := today.AddDate(0, 0, -29)
	if monthStart.Before(since) {
		since = monthStart
	}
	_, recentDays, err := s.aggregate(ctx, since, today.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	var last30, monthSpent, todaySpent int64
	todayKey, monthKey, from30 := today.Format("2006-01-02"), monthStart.Format("2006-01-02"), today.AddDate(0, 0, -29).Format("2006-01-02")
	for _, d := range recentDays {
		if d.ID >= from30 {
			last30 += d.Sum
		}
		if d.ID >= monthKey {
			monthSpent += d.Sum
		}
		if d.ID == todayKey {
			todaySpent = d.Sum
		}
	}

	var earlierIncome, earlierSpent int64
	if prof.Prior.YM == now.In(ist.Loc).Format("2006-01") && !now.Before(r.From) && now.Before(r.To) {
		earlierIncome, earlierSpent = paise(prof.Prior.Income), paise(prof.Prior.Spent)
	}
	cf := buildCashflow(n.flows(flowDocs, "in"), n.flows(flowDocs, "out"), earlierIncome, earlierSpent)
	cf.Daily = fillDays(periodDays, r, now)

	var cash int64
	for _, a := range accs {
		cash += a.BalancePaise
	}
	pos := Position{Cash: cash, Accounts: len(accs), UsesFunds: fundSum.Count > 0}
	mfInvested, mfCurrent := paise(prof.MF.Invested), paise(prof.MF.Current)
	if pos.UsesFunds {
		mfInvested, mfCurrent = paise(fundSum.Invested), paise(fundSum.Value)
	}
	port, err := s.stocks.Portfolio(ctx, now)
	if err != nil {
		return nil, err
	}
	stInvested, stCurrent := paise(port.Summary.Invested), paise(port.Summary.Value)
	pos.Invested = mfInvested + stInvested + paise(prof.Other.Invested)
	pos.Current = mfCurrent + stCurrent + paise(prof.Other.Current)
	pos.Liabilities = paise(emiSum.Outstanding)
	pos.Pnl, pos.NetWorth = pos.Current-pos.Invested, cash+pos.Current
	if err := s.record(ctx, now, pos); err != nil {
		log.Printf("dashboard: net-worth record: %v", err)
	}
	if ch, since, ok, err := s.lastMonthChange(ctx, now, pos.NetWorth); err != nil {
		log.Printf("dashboard: net-worth change: %v", err)
	} else if ok {
		pos.NetWorthChange, pos.ChangeSince = &ch, since
	}
	pos.Allocation = []Allocation{{"Cash", cash}, {"Mutual Funds", mfCurrent}, {"Stocks", stCurrent}, {"Other", paise(prof.Other.Current)}}

	recent, err := s.recent(ctx, r, n, 8)
	if err != nil {
		return nil, err
	}
	owed, err := s.owedToYou(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := s.mandates.Plan(ctx, now)
	if err != nil {
		return nil, err
	}
	m := Mandates{Next30: plan.Next30, Next90: plan.Next90, SetAside: plan.SetAside, Due: []mandates.Occ{}}
	for _, o := range plan.Calendar {
		if o.Days < 90 {
			m.Due = append(m.Due, o)
		}
	}
	return &Response{
		Period:   Period{Key: key, From: r.From.Format(time.RFC3339), To: r.To.Format(time.RFC3339)},
		Position: pos, Cashflow: cf, Recent: recent, Mandates: m, OwedToYou: owed,
		Upcoming:     upcoming(upcomingIn{Cash: cash, Profile: prof, Emis: emiSum, Funds: fundSum, Last30: last30, MonthSpent: monthSpent, Today: todaySpent, Now: now}),
		NeedsProfile: prof.Salary == 0 && pos.Invested == 0,
	}, nil
}

// fillDays lists every IST day of the range up to today, zero where nothing was spent.
func fillDays(docs []dayDoc, r Range, now time.Time) []Day {
	by := map[string]dayDoc{}
	for _, d := range docs {
		by[d.ID] = d
	}
	var out []Day
	end := ist.Midnight(now).AddDate(0, 0, 1)
	if r.To.Before(end) {
		end = r.To
	}
	for d := r.From; d.Before(end); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		day := Day{Date: k}
		if doc, ok := by[k]; ok {
			day.Paise = doc.Sum
			day.Top = &struct {
				Vendor   string `json:"vendor"`
				Category string `json:"category"`
				Paise    int64  `json:"paise"`
			}{doc.Top.Vendor, doc.Top.Cat, doc.Top.Paise}
		}
		out = append(out, day)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}
