package transactions

import (
	"context"
	"log"
	"sort"
	"strings"
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

// Chatter is the LLM call (nim.Client satisfies it); a fake works in tests.
type Chatter interface {
	Chat(ctx context.Context, system, user string) (string, error)
}

type Service struct {
	col        *mongo.Collection
	raw        *raw_transactions.Service
	vendors    *vendors.Service
	categories *categories.Service
	accounts   *accounts.Service
	cards      *cards.Service
	llm        Chatter
	model      string
}

func NewService(ctx context.Context, db *mongo.Database, raw *raw_transactions.Service, v *vendors.Service, c *categories.Service, acc *accounts.Service, cd *cards.Service, llm Chatter, model string) (*Service, error) {
	col := db.Collection(Collection)
	// Unique raw_id makes parsing idempotent: one transaction per SMS, ever.
	_, err := col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "raw_id", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "needs_review", Value: 1}, {Key: "occurred_at", Value: -1}}},
		{Keys: bson.D{{Key: "occurred_at", Value: -1}}},
		{Keys: bson.D{{Key: "received_at", Value: -1}}},
	})
	if err == nil {
		err = backfillReceivedAt(ctx, db, col)
	}
	return &Service{col: col, raw: raw, vendors: v, categories: c, accounts: acc, cards: cd, llm: llm, model: model}, err
}

// Parse turns one raw message into a transaction. Safe to call repeatedly.
// Failures are recorded on the raw message; logs carry ids and reasons only.
func (s *Service) Parse(ctx context.Context, rawID bson.ObjectID) error {
	raw, err := s.raw.Get(ctx, rawID)
	if err != nil {
		return err
	}
	if raw.Status != raw_transactions.StatusPending && raw.Status != raw_transactions.StatusFailed {
		return nil
	}
	fail := func(reason string, cause error) error {
		log.Printf("parse %s failed: %s: %v", rawID.Hex(), reason, cause) // causes carry no message text
		_ = s.raw.SetStatus(ctx, rawID, raw_transactions.StatusFailed, reason)
		return cause
	}

	cats, err := s.categories.List(ctx)
	if err != nil {
		return fail("db_error", err)
	}
	names := make([]string, len(cats))
	for i, c := range cats {
		names[i] = c.Name
	}
	reply, err := s.llm.Chat(ctx, systemPrompt(names), raw.Message)
	if err != nil {
		return fail("llm_error", err)
	}
	e, paise, at, err := parseExtraction(reply, raw.Message, raw.ReceivedAt)
	if err != nil {
		return fail("invalid_extraction", err)
	}
	if !e.IsTransaction {
		return s.raw.SetStatus(ctx, rawID, raw_transactions.StatusIgnored, "")
	}

	t := Transaction{
		ID: bson.NewObjectID(), RawID: rawID, Type: e.Type, AmountPaise: paise, OccurredAt: at, ReceivedAt: raw.ReceivedAt,
		Bank: e.Bank, Direction: e.Direction, AccountLast4: e.AccountLast4, Ref: e.Ref,
		RawVendor: e.RawVendor, UPIID: strings.ToLower(e.UPIID),
		Model: s.model, PromptVersion: PromptVersion, CreatedAt: time.Now().UTC(),
	}
	v, err := s.vendors.Match(ctx, e.RawVendor, e.RawVendor, t.UPIID, raw.Message)
	if err != nil {
		return fail("db_error", err)
	}
	if v != nil {
		t.VendorID, t.CategoryID = &v.ID, v.CategoryID
	} else {
		t.NeedsReview, t.ReviewReason = true, "unknown_vendor"
		for _, c := range cats {
			if strings.EqualFold(c.Name, e.SuggestedCategory) {
				id := c.ID
				t.SuggestedCategoryID = &id
			}
		}
	}
	// Link to a known bank account or card by last4; unknown ones stay unlinked and are linked when created.
	acc, err := s.accounts.Match(ctx, e.Bank, e.AccountLast4)
	if err != nil {
		return fail("db_error", err)
	}
	if acc != nil {
		t.AccountID = &acc.ID
	} else if card, err := s.cards.Match(ctx, e.Bank, e.AccountLast4); err != nil {
		return fail("db_error", err)
	} else if card != nil {
		t.CardID = &card.ID
	}
	if _, err := s.col.InsertOne(ctx, t); err != nil && !mongo.IsDuplicateKeyError(err) {
		return fail("db_error", err)
	}
	return s.raw.SetStatus(ctx, rawID, raw_transactions.StatusParsed, "")
}

// Resync re-applies vendors to existing transactions after a vendor change:
// review items that now match get resolved, and each vendor's transactions
// take the vendor's current category.
func (s *Service) Resync(ctx context.Context) {
	if err := s.resync(ctx); err != nil {
		log.Printf("resync failed: %v", err)
	}
}

func (s *Service) resync(ctx context.Context) error {
	vs, err := s.vendors.List(ctx)
	if err != nil {
		return err
	}
	cur, err := s.col.Find(ctx, bson.M{"needs_review": true})
	if err != nil {
		return err
	}
	var pending []Transaction
	if err := cur.All(ctx, &pending); err != nil {
		return err
	}
	for _, t := range pending {
		raw, err := s.raw.Get(ctx, t.RawID)
		if err != nil {
			return err
		}
		v, err := s.vendors.Match(ctx, t.RawVendor, t.RawVendor, t.UPIID, raw.Message)
		if err != nil {
			return err
		}
		if v == nil {
			continue
		}
		set := bson.M{"vendor_id": v.ID, "needs_review": false}
		if v.CategoryID != nil {
			set["category_id"] = *v.CategoryID
		}
		if _, err := s.col.UpdateByID(ctx, t.ID, bson.M{"$set": set, "$unset": bson.M{"review_reason": ""}}); err != nil {
			return err
		}
	}
	for _, v := range vs {
		if v.CategoryID == nil {
			continue
		}
		if _, err := s.col.UpdateMany(ctx, bson.M{"vendor_id": v.ID}, bson.M{"$set": bson.M{"category_id": *v.CategoryID}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) List(ctx context.Context, needsReview *bool, accountID *bson.ObjectID, limit int64) ([]Transaction, error) {
	filter := bson.M{}
	if accountID != nil {
		filter["account_id"] = *accountID
	}
	if needsReview != nil {
		filter["needs_review"] = *needsReview
	}
	cur, err := s.col.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "received_at", Value: -1}, {Key: "occurred_at", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	out := []Transaction{}
	return out, cur.All(ctx, &out)
}

// ParsePending parses every message ledger-api has stored as pending.
// Failed messages are not retried here, to avoid looping LLM spend; use the reparse route.
func (s *Service) ParsePending(ctx context.Context) {
	ids, err := s.raw.Pending(ctx, 20)
	if err != nil {
		log.Printf("poll failed: %v", err)
		return
	}
	for _, id := range ids {
		_ = s.Parse(ctx, id)
	}
}

type CategoryTotal struct {
	CategoryID *bson.ObjectID `json:"category_id,omitempty"`
	Paise      int64          `json:"paise"`
}

type DayTotal struct {
	Date  string `json:"date"` // YYYY-MM-DD, IST
	Paise int64  `json:"paise"`
}

type Summary struct {
	SpentPaise  int64           `json:"spent_paise"`  // this month
	IncomePaise int64           `json:"income_paise"` // this month
	ReviewCount int64           `json:"review_count"`
	ByCategory  []CategoryTotal `json:"by_category"` // this month's spend, largest first
	Daily       []DayTotal      `json:"daily"`       // spend per day, last 30 days, oldest first
}

// Summary computes dashboard totals in IST over all transactions, not just a page of them.
func (s *Service) Summary(ctx context.Context) (*Summary, error) {
	now := time.Now().In(ist)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ist)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, ist)
	since := today.AddDate(0, 0, -29)
	if monthStart.Before(since) {
		since = monthStart
	}
	cur, err := s.col.Find(ctx, bson.M{"occurred_at": bson.M{"$gte": since.UTC()}})
	if err != nil {
		return nil, err
	}
	var ts []Transaction
	if err := cur.All(ctx, &ts); err != nil {
		return nil, err
	}
	review, err := s.col.CountDocuments(ctx, bson.M{"needs_review": true})
	if err != nil {
		return nil, err
	}

	out := &Summary{ReviewCount: review, ByCategory: []CategoryTotal{}, Daily: make([]DayTotal, 30)}
	idx := map[string]int{}
	for i := range out.Daily {
		d := today.AddDate(0, 0, i-29).Format("2006-01-02")
		out.Daily[i].Date, idx[d] = d, i
	}
	cats := map[bson.ObjectID]int64{}
	var uncategorised int64
	for _, t := range ts {
		at := t.OccurredAt.In(ist)
		spend := (t.Type == "expense" || t.Type == "atm" || t.Type == "emi") && t.Direction != "credit"
		if i, ok := idx[at.Format("2006-01-02")]; ok && spend {
			out.Daily[i].Paise += t.AmountPaise
		}
		if at.Before(monthStart) {
			continue
		}
		if t.Type == "income" {
			out.IncomePaise += t.AmountPaise
		}
		if spend {
			out.SpentPaise += t.AmountPaise
			if t.CategoryID != nil {
				cats[*t.CategoryID] += t.AmountPaise
			} else {
				uncategorised += t.AmountPaise
			}
		}
	}
	for id, p := range cats {
		id := id
		out.ByCategory = append(out.ByCategory, CategoryTotal{CategoryID: &id, Paise: p})
	}
	if uncategorised > 0 {
		out.ByCategory = append(out.ByCategory, CategoryTotal{Paise: uncategorised})
	}
	sort.Slice(out.ByCategory, func(i, j int) bool { return out.ByCategory[i].Paise > out.ByCategory[j].Paise })
	return out, nil
}
