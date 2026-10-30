package cards

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const PaymentsCollection = "card_payments"

// ManualPayment is a bill payment recorded by hand, for a payment whose bank SMS never arrived or was not recognised.
// It is a payment record only: no bank transaction is made, so spending and account balances are untouched.
// AdjustedPaise is how much it took off a manually entered outstanding, so deleting it can put the same amount back.
type ManualPayment struct {
	ID            bson.ObjectID  `bson:"_id"`
	CardID        bson.ObjectID  `bson:"card_id"`
	AmountPaise   int64          `bson:"amount_paise"`
	PaidAt        time.Time      `bson:"paid_at"`
	AccountID     *bson.ObjectID `bson:"account_id,omitempty"`
	Note          string         `bson:"note,omitempty"`
	AdjustedPaise int64          `bson:"adjusted_paise,omitempty"`
	CreatedAt     time.Time      `bson:"created_at"`
}

// PaymentInput is a request to record a payment. PaidAt is an instant (the client sends the chosen day at IST midnight).
type PaymentInput struct {
	AmountPaise int64     `json:"amount_paise"`
	PaidAt      time.Time `json:"paid_at"`
	AccountID   string    `json:"account_id"`
	Note        string    `json:"note"`
}

// Validate returns a message when the payment cannot be recorded.
func (p *PaymentInput) Validate(now time.Time) string {
	switch {
	case p.AmountPaise <= 0 || p.AmountPaise > maxPaise:
		return "amount must be more than zero"
	case p.PaidAt.IsZero():
		return "payment date required"
	case p.PaidAt.After(now.Add(24 * time.Hour)):
		return "payment date cannot be in the future"
	case p.AccountID != "" && !validID(p.AccountID):
		return "account_id is not a valid account id"
	case len(p.Note) > 200:
		return "note is too long (max 200 characters)"
	}
	return ""
}

// RecordPayment stores a manual payment for the card. When the card's outstanding was entered by hand it comes down by the same amount
// (never below zero), since that figure would otherwise stay stale.
func (s *Service) RecordPayment(ctx context.Context, cardID bson.ObjectID, in PaymentInput) (*ManualPayment, error) {
	var c Card
	if err := s.col.FindOne(ctx, bson.M{"_id": cardID}).Decode(&c); err != nil {
		return nil, err
	}
	p := ManualPayment{ID: bson.NewObjectID(), CardID: cardID, AmountPaise: in.AmountPaise, PaidAt: in.PaidAt.UTC(), Note: strings.TrimSpace(in.Note), CreatedAt: time.Now().UTC()}
	if in.AccountID != "" {
		id, _ := bson.ObjectIDFromHex(in.AccountID)
		p.AccountID = &id
	}
	if c.OutstandingPaise != nil {
		p.AdjustedPaise = min(in.AmountPaise, *c.OutstandingPaise)
		if _, err := s.col.UpdateByID(ctx, cardID, bson.M{"$inc": bson.M{"outstanding_paise": -p.AdjustedPaise}}); err != nil {
			return nil, err
		}
	}
	if _, err := s.pays.InsertOne(ctx, p); err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePayment removes a manual payment and restores what it took off a manual outstanding.
func (s *Service) DeletePayment(ctx context.Context, cardID, id bson.ObjectID) error {
	var p ManualPayment
	if err := s.pays.FindOneAndDelete(ctx, bson.M{"_id": id, "card_id": cardID}).Decode(&p); err != nil {
		return err
	}
	if p.AdjustedPaise > 0 {
		_, err := s.col.UpdateOne(ctx, bson.M{"_id": cardID, "outstanding_paise": bson.M{"$exists": true}}, bson.M{"$inc": bson.M{"outstanding_paise": p.AdjustedPaise}})
		return err
	}
	return nil
}

// manualPaid adds, per card, the manual payments made since its running cycle began: the same rule linked bank payments follow.
func (s *Service) manualPaid(ctx context.Context, cards []Card, now time.Time, out map[bson.ObjectID]*sums) error {
	var scope bson.A
	for _, c := range cards {
		scope = append(scope, bson.M{"card_id": c.ID, "paid_at": bson.M{"$gte": CycleStart(c, now)}})
	}
	if len(scope) == 0 {
		return nil
	}
	cur, err := s.pays.Find(ctx, bson.M{"$or": scope})
	if err != nil {
		return err
	}
	var ps []ManualPayment
	if err := cur.All(ctx, &ps); err != nil {
		return err
	}
	for _, p := range ps {
		if t := out[p.CardID]; t != nil {
			t.paid += p.AmountPaise
		}
	}
	return nil
}

// manualLast is, per card, how many manual payments there are and the latest.
func (s *Service) manualLast(ctx context.Context) (map[bson.ObjectID]lastPayment, error) {
	cur, err := s.pays.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$group", Value: bson.M{
			"_id": "$card_id", "n": bson.M{"$sum": 1},
			"last": bson.M{"$top": bson.M{"sortBy": bson.M{"paid_at": -1, "_id": -1}, "output": bson.M{"at": "$paid_at", "paise": "$amount_paise"}}},
		}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID   bson.ObjectID `bson:"_id"`
		N    int           `bson:"n"`
		Last struct {
			At    time.Time `bson:"at"`
			Paise int64     `bson:"paise"`
		} `bson:"last"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[bson.ObjectID]lastPayment, len(rows))
	for _, r := range rows {
		out[r.ID] = lastPayment{r.N, r.Last.At, r.Last.Paise}
	}
	return out, nil
}

// mergeLast combines the linked-transaction and manual summaries of one card.
func mergeLast(a, b lastPayment) lastPayment {
	n := a.n + b.n
	if b.at.After(a.at) {
		return lastPayment{n, b.at, b.paise}
	}
	return lastPayment{n, a.at, a.paise}
}

// Activity is one line of a card's recent history: a purchase, a refund or a payment.
type Activity struct {
	ID          bson.ObjectID `json:"id"`
	At          time.Time     `json:"at"`
	AmountPaise int64         `json:"amount_paise"`
	Kind        string        `json:"kind"` // purchase | refund | payment
	Vendor      string        `json:"vendor"`
	Source      string        `json:"source,omitempty"` // for a payment: the bank it left
	Category    string        `json:"category,omitempty"`
	Imported    bool          `json:"imported,omitempty"` // read from a card statement, not an SMS
	NeedsReview bool          `json:"needs_review,omitempty"`
}

// Activity lists the card's latest transactions: spends and refunds on it, and bank payments linked to it. Newest first.
func (s *Service) Activity(ctx context.Context, id bson.ObjectID, limit int64) ([]Activity, error) {
	cur, err := s.txs.Find(ctx,
		bson.M{"$or": bson.A{bson.M{"card_id": id, "type": bson.M{"$in": bson.A{"expense", "atm", "refund"}}}, bson.M{"paid_card_id": id}}},
		options.Find().SetSort(bson.D{{Key: "occurred_at", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID          bson.ObjectID  `bson:"_id"`
		Type        string         `bson:"type"`
		AmountPaise int64          `bson:"amount_paise"`
		OccurredAt  time.Time      `bson:"occurred_at"`
		ReceivedAt  time.Time      `bson:"received_at"`
		RawVendor   string         `bson:"raw_vendor"`
		UPIID       string         `bson:"upi_id"`
		Bank        string         `bson:"bank"`
		VendorID    *bson.ObjectID `bson:"vendor_id"`
		CategoryID  *bson.ObjectID `bson:"category_id"`
		PaidCardID  *bson.ObjectID `bson:"paid_card_id"`
		Model       string         `bson:"model"`
		NeedsReview bool           `bson:"needs_review"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	var ids, cids bson.A
	for _, d := range docs {
		if d.VendorID != nil {
			ids = append(ids, *d.VendorID)
		}
		if d.CategoryID != nil {
			cids = append(cids, *d.CategoryID)
		}
	}
	cats := map[bson.ObjectID]string{}
	if len(cids) > 0 {
		cc, err := s.cats.Find(ctx, bson.M{"_id": bson.M{"$in": cids}}, options.Find().SetProjection(bson.M{"name": 1}))
		if err != nil {
			return nil, err
		}
		var cs []struct {
			ID   bson.ObjectID `bson:"_id"`
			Name string        `bson:"name"`
		}
		if err := cc.All(ctx, &cs); err != nil {
			return nil, err
		}
		for _, c := range cs {
			cats[c.ID] = c.Name
		}
	}
	names := map[bson.ObjectID]string{}
	if len(ids) > 0 { // a saved vendor's name beats the raw SMS payee
		vc, err := s.vens.Find(ctx, bson.M{"_id": bson.M{"$in": ids}}, options.Find().SetProjection(bson.M{"name": 1}))
		if err != nil {
			return nil, err
		}
		var vs []struct {
			ID   bson.ObjectID `bson:"_id"`
			Name string        `bson:"name"`
		}
		if err := vc.All(ctx, &vs); err != nil {
			return nil, err
		}
		for _, v := range vs {
			names[v.ID] = v.Name
		}
	}
	out := make([]Activity, len(docs))
	for i, d := range docs {
		a := Activity{ID: d.ID, At: d.ReceivedAt, AmountPaise: d.AmountPaise, Kind: "purchase", Vendor: d.RawVendor, Imported: d.Model == "statement-import", NeedsReview: d.NeedsReview}
		if d.CategoryID != nil {
			a.Category = cats[*d.CategoryID]
		}
		if a.At.IsZero() {
			a.At = d.OccurredAt
		}
		if d.VendorID != nil && names[*d.VendorID] != "" {
			a.Vendor = names[*d.VendorID]
		}
		if a.Vendor == "" {
			a.Vendor = d.UPIID
		}
		if a.Vendor == "" {
			a.Vendor = "Unknown"
		}
		switch {
		case d.PaidCardID != nil:
			a.Kind, a.Source, a.Vendor = "payment", d.Bank, "Card payment"
		case d.Type == "refund":
			a.Kind = "refund"
		}
		out[i] = a
	}
	return out, nil
}
