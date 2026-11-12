package copilot

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

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
	"ledger-backend/internal/spend"
)

const (
	stateTTL     = 60 * time.Second // every reader shares one gathered State; a new transaction clears it
	recurringTTL = 6 * time.Hour    // the recurring scan reads a year of transactions, so it runs rarely
	nudgeKeep    = 180 * 24 * time.Hour
	historyDays  = 90
	maxWants     = 100
)

// Want is something parked on the cooling-off list. Status: parked, bought or dropped.
type Want struct {
	ID          bson.ObjectID `json:"id" bson:"_id"`
	Name        string        `json:"name" bson:"name"`
	AmountPaise int64         `json:"amountPaise" bson:"amount_paise"`
	Need        bool          `json:"need" bson:"need"`
	Uses        int           `json:"uses" bson:"uses"`
	Link        string        `json:"link,omitempty" bson:"link,omitempty"`
	RemindAt    *time.Time    `json:"remindAt,omitempty" bson:"remind_at,omitempty"`
	Status      string        `json:"status" bson:"status"`
	BoughtAt    *time.Time    `json:"boughtAt,omitempty" bson:"bought_at,omitempty"`
	Glad        *bool         `json:"glad,omitempty" bson:"glad,omitempty"`
	CreatedAt   time.Time     `json:"createdAt" bson:"created_at"`
}

type WantInput struct {
	Name        string `json:"name"`
	AmountPaise int64  `json:"amountPaise"`
	Need        bool   `json:"need"`
	Uses        int    `json:"uses"`
	Link        string `json:"link"`
	RemindOn    string `json:"remindOn"` // YYYY-MM-DD, optional
}

func (in *WantInput) Parse() (Want, string) {
	in.Name, in.Link = strings.TrimSpace(in.Name), strings.TrimSpace(in.Link)
	w := Want{Name: in.Name, AmountPaise: in.AmountPaise, Need: in.Need, Uses: in.Uses, Link: in.Link, Status: "parked"}
	switch {
	case in.Name == "" || len(in.Name) > 120:
		return w, "name required, at most 120 characters"
	case in.AmountPaise <= 0 || in.AmountPaise > maxAmount:
		return w, "amount must be above zero"
	case in.Uses < 0:
		return w, "uses cannot be negative"
	}
	if in.Link != "" {
		// The link is rendered as an href, so only web addresses may be stored (never javascript:).
		if u, err := url.Parse(in.Link); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(in.Link) > 500 {
			return w, "link must be a web address"
		}
	}
	if in.RemindOn != "" {
		d, err := time.ParseInLocation("2006-01-02", in.RemindOn, ist.Loc)
		if err != nil {
			return w, "remindOn must be YYYY-MM-DD"
		}
		d = d.Add(9 * time.Hour) // the morning of that day
		w.RemindAt = &d
	}
	return w, ""
}

type Service struct {
	nudges, mutes, wants, settings, txs *mongo.Collection

	accounts *accounts.Service
	cards    *cards.Service
	profile  *profile.Service
	emis     *emis.Service
	funds    *funds.Service
	mandates *mandates.Service
	budgets  *budgets.Service
	cats     *categories.Service
	insights *insights.Service
	snaps    *snapshots.Service
	push     *brainjogger.Service
	habits   HabitSource

	flight singleflight.Group
	mu     sync.Mutex
	state  *State
	at     time.Time
	rec    []insights.Recurring
	recAt  time.Time

	poke chan struct{}
}

func NewService(ctx context.Context, db *mongo.Database, a *accounts.Service, c *cards.Service, p *profile.Service, e *emis.Service, f *funds.Service, m *mandates.Service, b *budgets.Service, cat *categories.Service, in *insights.Service, sn *snapshots.Service, push *brainjogger.Service) (*Service, error) {
	s := &Service{nudges: db.Collection("nudges"), mutes: db.Collection("nudge_mutes"), wants: db.Collection("wants"), settings: db.Collection("copilot_settings"), txs: db.Collection("transactions"),
		accounts: a, cards: c, profile: p, emis: e, funds: f, mandates: m, budgets: b, cats: cat, insights: in, snaps: sn, push: push, poke: make(chan struct{}, 1)}
	// Old nudges expire on their own, so the collection stays small however long the app runs.
	_, err := s.nudges.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "created_at", Value: -1}}, Options: options.Index().SetExpireAfterSeconds(int32(nudgeKeep.Seconds()))},
		{Keys: bson.D{{Key: "state", Value: 1}, {Key: "created_at", Value: -1}}},
	})
	return s, err
}

// UseHabits lets the rules nudge before a food-order habit. Call it once, before the first Tick.
func (s *Service) UseHabits(h HabitSource) { s.habits = h }

// SetMode changes how hard the money has to last, for every number the app shows.
func (s *Service) SetMode(ctx context.Context, key string) error {
	_, err := s.settings.UpdateByID(ctx, "mode", bson.M{"$set": bson.M{"key": key}}, options.UpdateOne().SetUpsert(true))
	s.Invalidate()
	return err
}

// Poke says a transaction just landed: the cached numbers are stale and the rules should run. Non-blocking.
func (s *Service) Poke() {
	s.Invalidate()
	select {
	case s.poke <- struct{}{}:
	default: // a run is already queued
	}
}

func (s *Service) Invalidate() {
	s.mu.Lock()
	s.state = nil
	s.mu.Unlock()
}

// State returns the shared numbers; concurrent callers wait on one gather instead of each running it.
func (s *Service) State(ctx context.Context, now time.Time) (*State, error) {
	s.mu.Lock()
	st, at := s.state, s.at
	s.mu.Unlock()
	if st != nil && now.Sub(at) < stateTTL && !now.Before(at) {
		c := *st
		c.Now = now
		return &c, nil
	}
	v, err, _ := s.flight.Do("state", func() (any, error) {
		// A caller leaving must not fail the gather the others are waiting for.
		gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		st, err := s.gather(gctx, now)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.state, s.at = st, now
		s.mu.Unlock()
		return st, nil
	})
	if err != nil {
		return nil, err
	}
	c := *v.(*State)
	c.Now = now
	return &c, nil
}

func toPaise(rupees float64) int64 { return int64(rupees*100 + 0.5) }

// gather reads every input once, side by side. Daily history comes from the snapshot table by primary key, not from re-aggregating transactions;
// only today's and this fortnight's transactions are touched, on the occurred_at index.
func (s *Service) gather(ctx context.Context, now time.Time) (*State, error) {
	st := &State{Now: now}
	today := ist.Midnight(now)
	var (
		accs  []accounts.View
		prof  *profile.Profile
		emi   *emis.Summary
		fund  *funds.Summary
		plan  mandates.Plan
		stats []budgets.Status
		cats  []categories.Category
		rows  []snapshots.Snapshot
	)
	g, c := errgroup.WithContext(ctx)
	g.Go(func() (err error) { accs, err = s.accounts.List(c); return })
	g.Go(func() (err error) { prof, err = s.profile.Get(c); return })
	g.Go(func() (err error) { emi, err = s.emis.Summary(c, now); return })
	g.Go(func() (err error) { fund, err = s.funds.Summary(c, now); return })
	g.Go(func() (err error) { plan, err = s.mandates.Plan(c, now); return })
	g.Go(func() (err error) { st.Cards, err = s.cards.Views(c, now); return })
	g.Go(func() (err error) { stats, err = s.budgets.Statuses(c, now); return })
	g.Go(func() (err error) { cats, err = s.cats.List(c); return })
	g.Go(func() (err error) {
		rows, err = s.snaps.Range(c, today.AddDate(0, 0, -historyDays).Format("2006-01-02"), today.AddDate(0, 0, -1).Format("2006-01-02"))
		return
	})
	g.Go(func() error { return s.todaySpend(c, today, st) })
	g.Go(func() (err error) { st.Owed, err = s.owed(c); return })
	g.Go(func() (err error) { st.Vendors, err = s.vendorWeeks(c, today); return })
	g.Go(func() error {
		var d struct {
			Key string `bson:"key"`
		}
		st.Mode = DefaultMode
		if err := s.settings.FindOne(c, bson.M{"_id": "mode"}).Decode(&d); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return nil
			}
			return err
		}
		st.Mode = d.Key
		return nil
	})
	g.Go(func() error {
		cur, err := s.wants.Find(c, bson.D{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(maxWants))
		if err != nil {
			return err
		}
		st.Wants = []Want{}
		return cur.All(c, &st.Wants)
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	// Food habits are optional like the vendor scan: a failure here must not take safe-to-spend down.
	if s.habits != nil {
		var err error
		if st.FoodHabits, err = s.habits.Due(ctx, now); err != nil {
			log.Printf("copilot: food habits: %v", err)
		}
	}

	st.AccountsTotal = len(accs)
	for _, a := range accs {
		st.Cash += a.BalancePaise
	}
	st.Salary, st.SalaryDay, st.Buffer, st.Fixed = toPaise(prof.Salary), prof.SalaryDay, toPaise(prof.BufferTarget), toPaise(prof.FixedMonthly)
	st.EmiMonthly, st.SipMonthly = toPaise(emi.Monthly), toPaise(prof.SIP)
	if fund.Count > 0 {
		st.SipMonthly = toPaise(fund.SipMonthly)
	}
	for _, e := range emi.Active {
		st.Emis = append(st.Emis, Dated{e.Name, e.DueDay, toPaise(e.Amount)})
	}
	for _, f := range fund.Sips {
		st.Sips = append(st.Sips, Dated{f.Name, f.Day, toPaise(f.Amount)})
	}
	st.Mandates = plan.Calendar
	names := map[bson.ObjectID]string{}
	for _, c := range cats {
		names[c.ID] = c.Name
	}
	for _, b := range stats {
		st.Budgets = append(st.Budgets, BudgetRow{b, names[b.CategoryID]})
	}
	seenMoved := map[bson.ObjectID]bool{}
	first := map[bson.ObjectID]int64{}
	from30 := today.AddDate(0, 0, -30).Format("2006-01-02")
	for _, r := range rows {
		st.Days = append(st.Days, DayRow{r.Date, r.Spent, r.Income})
		if r.Date < from30 {
			continue
		}
		for _, a := range r.Accounts {
			if p, ok := first[a.ID]; !ok {
				first[a.ID] = a.Paise
			} else if p != a.Paise {
				seenMoved[a.ID] = true
			}
		}
	}
	st.AccountsMoved = len(seenMoved)

	// The vendor scan is optional: without it there are no subscription insights, but safe-to-spend still works.
	st.Recurring = s.recurring(ctx, now)
	for _, r := range st.Recurring {
		if r.Status != "active" || (r.Cadence != "yearly" && r.Cadence != "quarterly") || r.DaysUntil < 0 {
			continue
		}
		tracked := false
		for _, m := range plan.Items {
			a, b := strings.ToLower(m.Name), strings.ToLower(r.Name)
			tracked = tracked || strings.Contains(a, b) || strings.Contains(b, a)
		}
		if !tracked {
			st.Renewals = append(st.Renewals, Renewal{r.Name, r.AmountPaise, r.DaysUntil})
		}
	}
	return st, nil
}

func (s *Service) recurring(ctx context.Context, now time.Time) []insights.Recurring {
	s.mu.Lock()
	rec, at := s.rec, s.recAt
	s.mu.Unlock()
	if !at.IsZero() && now.Sub(at) < recurringTTL {
		return rec
	}
	fresh, err := s.insights.Recurring(ctx, now)
	if err != nil {
		log.Printf("copilot: recurring: %v", err)
		return rec // the last good scan beats none
	}
	s.mu.Lock()
	s.rec, s.recAt = fresh, now
	s.mu.Unlock()
	return fresh
}

func (s *Service) todaySpend(ctx context.Context, today time.Time, st *State) error {
	pipe := append(mongo.Pipeline{{{Key: "$match", Value: spend.Match(today, today.AddDate(0, 0, 1))}}}, spend.Net()...)
	pipe = append(pipe, mongo.Pipeline{
		{{Key: "$lookup", Value: bson.M{"from": "vendors", "localField": "vendor_id", "foreignField": "_id", "as": "ven"}}},
		{{Key: "$group", Value: bson.M{
			"_id":   nil,
			"total": bson.M{"$sum": "$amount_paise"},
			"card":  bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$ifNull": bson.A{"$card_id", false}}, "$amount_paise", 0}}},
			"top": bson.M{"$top": bson.M{"sortBy": bson.M{"amount_paise": -1}, "output": bson.M{"id": "$_id", "paise": "$amount_paise",
				"vendor": bson.M{"$ifNull": bson.A{bson.M{"$first": "$ven.name"}, "$raw_vendor", "$upi_id", "a payee"}}}}},
		}}},
	}...)
	cur, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return err
	}
	var res []struct {
		Total int64 `bson:"total"`
		Card  int64 `bson:"card"`
		Top   struct {
			ID     bson.ObjectID `bson:"id"`
			Paise  int64         `bson:"paise"`
			Vendor string        `bson:"vendor"`
		} `bson:"top"`
	}
	if err := cur.All(ctx, &res); err != nil || len(res) == 0 {
		return err
	}
	st.TodaySpent, st.TodayCard = res[0].Total, res[0].Card
	st.TopToday = &TopTxn{res[0].Top.ID.Hex(), res[0].Top.Vendor, res[0].Top.Paise}
	return nil
}

func (s *Service) owed(ctx context.Context) (int64, error) {
	cur, err := s.txs.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"owed_paise": bson.M{"$gt": 0}}}}, // rides the partial index
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

// vendorWeeks counts payments per saved vendor this week (from Monday) and last week; the name lookup runs once per vendor.
func (s *Service) vendorWeeks(ctx context.Context, today time.Time) ([]VendorWeek, error) {
	monday := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	match := spend.Match(monday.AddDate(0, 0, -7), today.AddDate(0, 0, 1))
	match["vendor_id"] = bson.M{"$ne": nil}
	isThis := bson.M{"$gte": bson.A{"$occurred_at", monday.UTC()}}
	cur, err := s.txs.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$group", Value: bson.M{"_id": "$vendor_id",
			"count": bson.M{"$sum": bson.M{"$cond": bson.A{isThis, 1, 0}}},
			"last":  bson.M{"$sum": bson.M{"$cond": bson.A{isThis, 0, 1}}},
			"paise": bson.M{"$sum": bson.M{"$cond": bson.A{isThis, "$amount_paise", 0}}},
		}}},
		{{Key: "$match", Value: bson.M{"count": bson.M{"$gte": 3}}}},
		{{Key: "$lookup", Value: bson.M{"from": "vendors", "localField": "_id", "foreignField": "_id", "as": "v"}}},
		{{Key: "$project", Value: bson.M{"count": 1, "last": 1, "paise": 1, "name": bson.M{"$first": "$v.name"}}}},
	})
	if err != nil {
		return nil, err
	}
	var res []struct {
		Name  string `bson:"name"`
		Count int    `bson:"count"`
		Last  int    `bson:"last"`
		Paise int64  `bson:"paise"`
	}
	if err := cur.All(ctx, &res); err != nil {
		return nil, err
	}
	out := make([]VendorWeek, 0, len(res))
	for _, r := range res {
		if r.Name != "" {
			out = append(out, VendorWeek{r.Name, r.Count, r.Last, r.Paise})
		}
	}
	return out, nil
}

// Overview is everything the app shows from the engine, in one response. The numbers are computed from the shared State, so no reader costs a query of its own.
type Overview struct {
	Safe          Safe           `json:"safe"`
	Runway        Runway         `json:"runway"`
	Week          Week           `json:"week"`
	Habits        Habits         `json:"habits"`
	Confidence    Confidence     `json:"confidence"`
	CardAdvice    []Advice       `json:"cardAdvice"`
	Subscriptions []Subscription `json:"subscriptions"`
	Top           *Nudge         `json:"top,omitempty"` // the one current nudge
	OwedPaise     int64          `json:"owedPaise"`
}

func (s *Service) Overview(ctx context.Context, now time.Time) (*Overview, error) {
	st, err := s.State(ctx, now)
	if err != nil {
		return nil, err
	}
	o := &Overview{Safe: st.SafeNow(), Runway: st.Runway(), Week: st.WeekAhead(7), Habits: st.Habits(), Confidence: st.Confidence(), CardAdvice: st.CardAdvice(), Subscriptions: st.Subscriptions(), OwedPaise: st.Owed}
	active, err := s.active(ctx, now, 1)
	if err != nil {
		return nil, err
	}
	if len(active) > 0 {
		o.Top = &active[0]
	}
	return o, nil
}

// active is what still waits for the user, biggest money first.
func (s *Service) active(ctx context.Context, now time.Time, limit int64) ([]Nudge, error) {
	cur, err := s.nudges.Find(ctx, bson.M{"state": "new", "$or": bson.A{bson.M{"snoozed_until": nil}, bson.M{"snoozed_until": bson.M{"$lte": now.UTC()}}}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(50))
	if err != nil {
		return nil, err
	}
	out := []Nudge{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	sortByStake(out)
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func sortByStake(ns []Nudge) {
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && ns[j].StakePaise > ns[j-1].StakePaise; j-- {
			ns[j], ns[j-1] = ns[j-1], ns[j]
		}
	}
}

// Inbox is the nudge history with what is muted. Active ones come first.
type Inbox struct {
	Active  []Nudge    `json:"active"`
	History []Nudge    `json:"history"`
	Muted   []MuteView `json:"muted"`
}

type MuteView struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Auto  bool   `json:"auto,omitempty"` // muted because they kept being ignored
}

func (s *Service) Inbox(ctx context.Context, now time.Time) (*Inbox, error) {
	active, err := s.active(ctx, now, 50)
	if err != nil {
		return nil, err
	}
	cur, err := s.nudges.Find(ctx, bson.M{"state": bson.M{"$ne": "new"}}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(30))
	if err != nil {
		return nil, err
	}
	history := []Nudge{}
	if err := cur.All(ctx, &history); err != nil {
		return nil, err
	}
	muted, auto, err := s.muted(ctx, now)
	if err != nil {
		return nil, err
	}
	in := &Inbox{Active: active, History: history, Muted: []MuteView{}}
	for k, m := range kinds {
		if muted[k] {
			in.Muted = append(in.Muted, MuteView{Kind: k, Label: m.label, Auto: auto[k]})
		}
	}
	return in, nil
}

// muted is what the user switched off, plus the kinds they keep dismissing without acting: five or more in 30 days, four in five dismissed, none acted on.
func (s *Service) muted(ctx context.Context, now time.Time) (all, auto map[string]bool, err error) {
	all, auto = map[string]bool{}, map[string]bool{}
	cur, err := s.mutes.Find(ctx, bson.D{})
	if err != nil {
		return nil, nil, err
	}
	var ms []struct {
		ID      string `bson:"_id"`
		Enabled bool   `bson:"enabled"` // set by un-muting a kind that was auto-muted, so it stays on
	}
	if err := cur.All(ctx, &ms); err != nil {
		return nil, nil, err
	}
	keep := map[string]bool{}
	for _, m := range ms {
		if m.Enabled {
			keep[m.ID] = true
		} else {
			all[m.ID] = true
		}
	}
	stats, err := s.nudges.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"created_at": bson.M{"$gte": now.AddDate(0, 0, -30).UTC()}}}},
		{{Key: "$group", Value: bson.M{"_id": "$kind", "n": bson.M{"$sum": 1},
			"gone":  bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$state", "dismissed"}}, 1, 0}}},
			"acted": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$state", "acted"}}, 1, 0}}}}}},
	})
	if err != nil {
		return nil, nil, err
	}
	var rows []struct {
		Kind           string `bson:"_id"`
		N, Gone, Acted int
	}
	if err := stats.All(ctx, &rows); err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if ignored(r.N, r.Gone, r.Acted) && !keep[r.Kind] && !all[r.Kind] && !kinds[r.Kind].urgent {
			all[r.Kind], auto[r.Kind] = true, true
		}
	}
	return all, auto, nil
}

func ignored(n, gone, acted int) bool { return n >= 5 && acted == 0 && gone*5 >= n*4 }

// SetNudge records what the user did: dismiss, acted (also "log as planned"), or snooze for a day.
func (s *Service) SetNudge(ctx context.Context, id, action string, now time.Time) error {
	set := bson.M{}
	switch action {
	case "dismiss":
		set["state"] = "dismissed"
	case "acted":
		set["state"] = "acted"
	case "snooze":
		set["snoozed_until"] = now.Add(24 * time.Hour).UTC()
	}
	r, err := s.nudges.UpdateByID(ctx, id, bson.M{"$set": set})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

// SetMute turns a kind off, or back on. Turning one on is remembered, so an ignored kind the user asked for is not auto-muted again.
func (s *Service) SetMute(ctx context.Context, kind string, muted bool) error {
	_, err := s.mutes.UpdateByID(ctx, kind, bson.M{"$set": bson.M{"enabled": !muted}}, options.UpdateOne().SetUpsert(true))
	return err
}

// Tick runs the rules and delivers what passes the cap. Inserting first means a crash never repeats a message.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	if h := now.In(ist.Loc).Hour(); h < 8 || h >= 22 {
		return nil // silence is a feature
	}
	st, err := s.State(ctx, now)
	if err != nil {
		return err
	}
	all := st.Rules()
	if len(all) == 0 {
		return nil
	}
	ids := make(bson.A, len(all))
	for i, n := range all {
		ids[i] = n.ID
	}
	cur, err := s.nudges.Find(ctx, bson.M{"_id": bson.M{"$in": ids}}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return err
	}
	var have []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &have); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, h := range have {
		seen[h.ID] = true
	}
	muted, _, err := s.muted(ctx, now)
	if err != nil {
		return err
	}
	sent, err := s.nudges.CountDocuments(ctx, bson.M{"created_at": bson.M{"$gte": ist.Midnight(now).UTC()}, "kind": bson.M{"$ne": kindWeek}})
	if err != nil {
		return err
	}
	for _, n := range pick(all, seen, muted, maxPerDay-int(sent)) {
		n.CreatedAt = now.UTC()
		if _, err := s.nudges.InsertOne(ctx, n); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				continue
			}
			return err
		}
		if kinds[n.Kind].urgent || n.Kind == kindWeek {
			s.push.Push(ctx, n.Title, n.Body, "nudge-"+n.Kind, "/copilot")
		}
	}
	return nil
}

// Run ticks every interval, and straight away when a transaction lands.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := s.Tick(tctx, time.Now()); err != nil {
			log.Printf("copilot: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.poke:
		}
	}
}

// WantView is a parked want with today's answer for it.
type WantView struct {
	Want
	Check *CheckResult `json:"check,omitempty"`
}

func (s *Service) Wants(ctx context.Context, now time.Time) ([]WantView, error) {
	st, err := s.State(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]WantView, 0, len(st.Wants))
	for _, w := range st.Wants {
		v := WantView{Want: w}
		if w.Status == "parked" {
			c := st.Check(CheckInput{Name: w.Name, AmountPaise: w.AmountPaise, Need: w.Need, Uses: w.Uses})
			v.Check = &c
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) CreateWant(ctx context.Context, w Want, now time.Time) error {
	if n, err := s.wants.CountDocuments(ctx, bson.D{}); err != nil {
		return err
	} else if n >= maxWants {
		return errTooMany
	}
	w.ID, w.CreatedAt = bson.NewObjectID(), now.UTC()
	_, err := s.wants.InsertOne(ctx, w)
	s.Invalidate()
	return err
}

// SetWant moves a want: bought, dropped or parked again, or records whether the user is glad they bought it.
func (s *Service) SetWant(ctx context.Context, id bson.ObjectID, status string, glad *bool, now time.Time) error {
	set := bson.M{}
	if status != "" {
		set["status"] = status
		if status == "bought" {
			set["bought_at"] = now.UTC()
		}
	}
	if glad != nil {
		set["glad"] = *glad
	}
	r, err := s.wants.UpdateByID(ctx, id, bson.M{"$set": set})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	s.Invalidate()
	return err
}

func (s *Service) DeleteWant(ctx context.Context, id bson.ObjectID) error {
	r, err := s.wants.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	s.Invalidate()
	return err
}
