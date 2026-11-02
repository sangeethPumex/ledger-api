package transactions

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// EditPatch corrects the facts of a transaction by hand: a wrong amount or day, the wrong account, a payee the parser misread.
// It cannot be combined with the other patch fields. Source and Vendor are nil to leave the link alone, so saving an SMS transaction
// whose bank has no account here does not wipe the bank and last four.
type EditPatch struct {
	Type        string       `json:"type"`
	Direction   string       `json:"direction"` // only read for types that can go either way
	AmountPaise int64        `json:"amount_paise"`
	OccurredAt  *time.Time   `json:"occurred_at"` // nil leaves both dates alone: an SMS keeps the day it was sent on
	Ref         string       `json:"ref"`
	Note        string       `json:"note"`
	Source      *SourcePatch `json:"source"` // both empty means cash
	Vendor      *string      `json:"vendor"` // empty clears the payee
}

type SourcePatch struct {
	AccountID string `json:"account_id"`
	CardID    string `json:"card_id"`
}

// planEdit works out the update for the plain fields of t, and checks the edit against what depends on them: a card link, a split,
// money owed back. Pure.
func planEdit(t Transaction, e *EditPatch, now time.Time) (set, unset bson.M, err error) {
	e.Ref, e.Note = strings.TrimSpace(e.Ref), strings.TrimSpace(e.Note)
	switch {
	case !validTypes[e.Type]:
		return nil, nil, ErrPatch("pick a transaction type")
	case e.AmountPaise <= 0 || e.AmountPaise > maxPaise:
		return nil, nil, ErrPatch("amount must be more than zero")
	case e.OccurredAt != nil && (e.OccurredAt.IsZero() || e.OccurredAt.Year() < 2000):
		return nil, nil, ErrPatch("date required")
	case e.OccurredAt != nil && e.OccurredAt.After(now.Add(24*time.Hour)):
		return nil, nil, ErrPatch("date cannot be in the future")
	case len(e.Ref) > 60 || len(e.Note) > 300:
		return nil, nil, ErrPatch("text is too long")
	}
	dir := e.Direction
	switch {
	case debitOnly[e.Type]:
		dir = "debit"
	case creditOnly[e.Type]:
		dir = "credit"
	case dir != "debit" && dir != "credit":
		return nil, nil, ErrPatch("say whether money went out or came in")
	}
	switch {
	case t.PaidCardID != nil && (e.Type != "card_bill_payment" || dir != "debit"):
		return nil, nil, ErrPatch("this pays a credit card: change its category to unlink the card first")
	case len(t.Splits) > 0 && (e.AmountPaise != t.AmountPaise || dir != "debit"):
		return nil, nil, ErrPatch("remove the split before changing the amount or direction")
	case t.OwedPaise > 0 && dir != "debit":
		return nil, nil, ErrPatch("remove what is owed back before making this money in")
	case t.OwedPaise > e.AmountPaise:
		return nil, nil, ErrPatch("the amount cannot be less than what is owed back")
	}
	set, unset = bson.M{"type": e.Type, "direction": dir, "amount_paise": e.AmountPaise}, bson.M{}
	if e.OccurredAt != nil {
		at := e.OccurredAt.UTC()
		set["occurred_at"], set["received_at"] = at, at
	}
	for k, v := range map[string]string{"ref": e.Ref, "note": e.Note} {
		if v == "" {
			unset[k] = ""
		} else {
			set[k] = v
		}
	}
	return set, unset, nil
}

// source finds the account or card a payment goes through. Both empty means cash or something with no account.
func (s *Service) source(ctx context.Context, accountID, cardID string) (acc, card *bson.ObjectID, bank, last4 string, err error) {
	if accountID != "" && cardID != "" {
		return nil, nil, "", "", ErrPatch("pick an account or a card, not both")
	}
	if acc, err = oid(accountID); err != nil {
		return nil, nil, "", "", err
	}
	if card, err = oid(cardID); err != nil {
		return nil, nil, "", "", err
	}
	if acc != nil {
		accs, err := s.accounts.Raw(ctx)
		if err != nil {
			return nil, nil, "", "", err
		}
		for _, a := range accs {
			if a.ID == *acc {
				return acc, nil, a.Bank, a.Last4, nil
			}
		}
		return nil, nil, "", "", ErrPatch("account not found")
	}
	if card != nil {
		cs, err := s.cards.List(ctx)
		if err != nil {
			return nil, nil, "", "", err
		}
		for _, c := range cs {
			if c.ID == *card {
				return nil, card, c.Bank, c.Last4, nil
			}
		}
		return nil, nil, "", "", ErrPatch("card not found")
	}
	return nil, nil, "", "", nil
}

func (s *Service) edit(ctx context.Context, id bson.ObjectID, e EditPatch) error {
	var t Transaction
	if err := s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		return err
	}
	set, unset, err := planEdit(t, &e, time.Now())
	if err != nil {
		return err
	}
	if e.Source != nil {
		if t.PaidCardID != nil && e.Source.CardID != "" {
			return ErrPatch("a card cannot pay its own bill: pick an account")
		}
		acc, card, bank, last4, err := s.source(ctx, e.Source.AccountID, e.Source.CardID)
		if err != nil {
			return err
		}
		for k, v := range map[string]any{"account_id": acc, "card_id": card, "bank": bank, "account_last4": last4} {
			switch x := v.(type) {
			case *bson.ObjectID:
				if x != nil {
					set[k] = *x
					continue
				}
			case string:
				if x != "" {
					set[k] = x
					continue
				}
			}
			unset[k] = ""
		}
	}
	if e.Vendor != nil {
		name := strings.TrimSpace(*e.Vendor)
		if len(name) > 100 {
			return ErrPatch("text is too long")
		}
		if name == "" {
			unset["raw_vendor"], unset["vendor_id"] = "", ""
		} else {
			set["raw_vendor"] = name
			v, err := s.vendors.Match(ctx, name, name, "", name)
			if err != nil {
				return err
			}
			if v == nil {
				unset["vendor_id"] = ""
			} else {
				// A known vendor settles the review, like a vendor change does; a category chosen by hand stays.
				set["vendor_id"], set["needs_review"] = v.ID, false
				unset["review_reason"] = ""
				if !t.CategorySet && v.CategoryID != nil && t.PaidCardID == nil && len(t.Splits) == 0 {
					set["category_id"] = *v.CategoryID
				}
			}
		}
	}
	for k := range set {
		delete(unset, k)
	}
	_, err = s.col.UpdateByID(ctx, id, bson.M{"$set": set, "$unset": unset})
	return err
}
