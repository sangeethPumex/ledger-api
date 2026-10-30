package transactions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/module/raw_transactions"
)

// CreateInput is a transaction typed in by hand: for a payment with no SMS, cash, or one the parser missed.
// Pay with an account or a card (or neither, for cash). Vendor is free text and is matched like an SMS payee.
type CreateInput struct {
	Type        string    `json:"type"`
	Direction   string    `json:"direction"` // debit | credit; implied by most types
	AmountPaise int64     `json:"amount_paise"`
	OccurredAt  time.Time `json:"occurred_at"`
	AccountID   string    `json:"account_id"`
	CardID      string    `json:"card_id"`
	Vendor      string    `json:"vendor"`
	CategoryID  string    `json:"category_id"`
	Ref         string    `json:"ref"`
	Note        string    `json:"note"`
	// PaidCardID: for money leaving an account to pay this credit card's bill.
	PaidCardID string `json:"paid_card_id"`
	// OwedFrom and OwedPaise: someone else owes back part of this payment.
	OwedFrom  string `json:"owed_from"`
	OwedPaise int64  `json:"owed_paise"`
}

// debitOnly and creditOnly are the types whose direction is fixed; transfers and card payments can go either way.
var (
	debitOnly  = map[string]bool{"expense": true, "atm": true, "emi": true}
	creditOnly = map[string]bool{"income": true, "refund": true}
)

// Check returns a message when the input cannot become a transaction, and fills in the direction when the type implies it. Pure.
func (in *CreateInput) Check(now time.Time) string {
	in.Vendor, in.Ref, in.Note, in.OwedFrom = strings.TrimSpace(in.Vendor), strings.TrimSpace(in.Ref), strings.TrimSpace(in.Note), strings.TrimSpace(in.OwedFrom)
	switch {
	case !validTypes[in.Type]:
		return "pick a transaction type"
	case in.AmountPaise <= 0 || in.AmountPaise > maxPaise:
		return "amount must be more than zero"
	case in.OccurredAt.IsZero():
		return "date required"
	case in.OccurredAt.After(now.Add(24 * time.Hour)):
		return "date cannot be in the future"
	case in.AccountID != "" && in.CardID != "":
		return "pick an account or a card, not both"
	case len(in.Vendor) > 100 || len(in.Ref) > 60 || len(in.Note) > 300 || len(in.OwedFrom) > 60:
		return "text is too long"
	}
	switch {
	case debitOnly[in.Type]:
		in.Direction = "debit"
	case creditOnly[in.Type]:
		in.Direction = "credit"
	case in.Direction != "debit" && in.Direction != "credit":
		return "say whether money went out or came in"
	}
	if in.PaidCardID != "" {
		switch {
		case in.Type != "card_bill_payment" || in.Direction != "debit":
			return "only a card bill payment going out can pay a card"
		case in.CardID != "":
			return "a card cannot pay its own bill: pay from an account"
		}
	}
	if in.OwedPaise != 0 {
		switch {
		case in.Direction != "debit":
			return "only money going out can be owed back"
		case in.OwedPaise < 0 || in.OwedPaise > in.AmountPaise:
			return "the amount owed back cannot be more than the payment"
		case in.OwedFrom == "":
			return "say who owes it back"
		}
	}
	return ""
}

func oid(s string) (*bson.ObjectID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := bson.ObjectIDFromHex(s)
	if err != nil {
		return nil, ErrPatch("an id is not valid")
	}
	return &id, nil
}

// Create records a transaction by hand. It is stored like a parsed one (with a stand-in raw message, since every transaction has one),
// so totals, budgets, balances and card cycles count it the same way. Nothing is sent to the model.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Transaction, error) {
	if msg := in.Check(time.Now()); msg != "" {
		return nil, ErrPatch(msg)
	}
	acc, err := oid(in.AccountID)
	if err != nil {
		return nil, err
	}
	card, err := oid(in.CardID)
	if err != nil {
		return nil, err
	}
	paid, err := oid(in.PaidCardID)
	if err != nil {
		return nil, err
	}
	cat, err := oid(in.CategoryID)
	if err != nil {
		return nil, err
	}

	t := Transaction{
		ID: bson.NewObjectID(), RawID: bson.NewObjectID(), Type: in.Type, AmountPaise: in.AmountPaise, Direction: in.Direction,
		OccurredAt: in.OccurredAt.UTC(), ReceivedAt: in.OccurredAt.UTC(), Ref: in.Ref, RawVendor: in.Vendor, Note: in.Note,
		OwedFrom: in.OwedFrom, OwedPaise: in.OwedPaise, Model: "manual", PromptVersion: "manual", CreatedAt: time.Now().UTC(),
	}
	if acc != nil {
		accs, err := s.accounts.Raw(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, a := range accs {
			if a.ID == *acc {
				t.AccountID, t.Bank, t.AccountLast4, found = acc, a.Bank, a.Last4, true
			}
		}
		if !found {
			return nil, ErrPatch("account not found")
		}
	}
	if card != nil {
		cs, err := s.cards.List(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, c := range cs {
			if c.ID == *card {
				t.CardID, t.Bank, t.AccountLast4, found = card, c.Bank, c.Last4, true
			}
		}
		if !found {
			return nil, ErrPatch("card not found")
		}
	}
	if paid != nil {
		if ok, err := s.cards.Exists(ctx, *paid); err != nil {
			return nil, err
		} else if !ok {
			return nil, ErrPatch("the card to pay was not found")
		}
		cp, err := s.categories.CardPaymentsID(ctx)
		if err != nil {
			return nil, err
		}
		t.PaidCardID, t.CategoryID = paid, &cp
	}
	if cat != nil {
		if ok, err := s.categories.Exists(ctx, *cat); err != nil {
			return nil, err
		} else if !ok {
			return nil, ErrPatch("category not found")
		}
		if t.PaidCardID == nil {
			t.CategoryID, t.CategorySet = cat, true
		}
	}
	if in.Vendor != "" {
		v, err := s.vendors.Match(ctx, in.Vendor, in.Vendor, "", in.Vendor)
		if err != nil {
			return nil, err
		}
		if v != nil {
			t.VendorID = &v.ID
			if t.CategoryID == nil {
				t.CategoryID = v.CategoryID
			}
		}
	}
	// A typed transaction needs no review when the user already chose its category or the vendor is known.
	if t.VendorID == nil && t.CategoryID == nil && t.Type != "transfer" {
		t.NeedsReview, t.ReviewReason = true, "unknown_vendor"
	}

	msg := fmt.Sprintf("MANUAL ENTRY %s %s %d %s", t.ID.Hex(), t.OccurredAt.Format(time.RFC3339), t.AmountPaise, t.Type)
	if _, err := s.rawCol.InsertOne(ctx, bson.M{"_id": t.RawID, "message": msg, "received_at": t.ReceivedAt, "status": raw_transactions.StatusParsed}); err != nil {
		return nil, err
	}
	if _, err := s.col.InsertOne(ctx, t); err != nil {
		_, _ = s.rawCol.DeleteOne(ctx, bson.M{"_id": t.RawID}) // no orphan stand-in message
		return nil, err
	}
	return &t, nil
}
