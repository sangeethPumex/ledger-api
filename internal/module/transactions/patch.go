package transactions

import (
	"context"
	"maps"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Patch changes one transaction by hand. Nil leaves a field alone; an empty PaidCardID unlinks the card.
type Patch struct {
	CategoryID *string `json:"category_id"`
	PaidCardID *string `json:"paid_card_id"`
	// Owed records money someone else owes back on this payment; Paise 0 clears it. RepaidPaise is how much of it has come back.
	Owed        *OwedPatch `json:"owed"`
	RepaidPaise *int64     `json:"repaid_paise"`
	// Splits replaces the split of this payment; an empty list removes it.
	Splits *[]SplitPatch `json:"splits"`
}

type SplitPatch struct {
	CategoryID string `json:"category_id"`
	Paise      int64  `json:"paise"`
}

type OwedPatch struct {
	From  string `json:"from"`
	Paise int64  `json:"paise"`
}

// ErrPatch carries a message safe to show for a 400.
type ErrPatch string

func (e ErrPatch) Error() string { return string(e) }

// planPatch works out the update for t. cp is the "Credit card payments" category; cat and card are the parsed ids of the patch
// (card nil with clearCard false leaves the link alone). Pure.
// A card link needs that category, so moving a transaction out of it drops the link. Linking turns the type into card_bill_payment so the
// payment is not counted as spending, and unlinking puts the parsed type back.
func planPatch(t Transaction, cp bson.ObjectID, cat, card *bson.ObjectID, clearCard bool) (set, unset bson.M, err error) {
	set, unset = bson.M{}, bson.M{}
	newCat := t.CategoryID
	if cat != nil {
		newCat = cat
		set["category_id"], set["category_set"] = *cat, true
	}
	inCP := newCat != nil && *newCat == cp
	link := t.PaidCardID
	switch {
	case card != nil && !inCP:
		return nil, nil, ErrPatch("pick the Credit card payments category to choose a card")
	case card != nil && t.Direction == "credit":
		return nil, nil, ErrPatch("only money going out can pay a card")
	case card != nil:
		link = card
	case clearCard || !inCP:
		link = nil
	}
	switch {
	case link != nil:
		set["paid_card_id"] = *link
		if t.Type != "card_bill_payment" {
			set["type"], set["type_before"] = "card_bill_payment", t.Type
		}
	default:
		unset["paid_card_id"] = ""
		if t.PaidCardID != nil && t.TypeBefore != "" {
			set["type"] = t.TypeBefore
			unset["type_before"] = ""
		}
	}
	return set, unset, nil
}

// planOwed works out the update for the owed fields of t. Pure.
// Owed needs money going out, a name, and no more than the payment itself; repaid can never exceed owed.
func planOwed(t Transaction, owed *OwedPatch, repaid *int64) (set, unset bson.M, err error) {
	set, unset = bson.M{}, bson.M{}
	total, back := t.OwedPaise, t.RepaidPaise
	if owed != nil {
		switch {
		case owed.Paise == 0:
			return nil, bson.M{"owed_from": "", "owed_paise": "", "repaid_paise": ""}, nil
		case t.Direction == "credit":
			return nil, nil, ErrPatch("only money going out can be owed back")
		case owed.Paise < 0 || owed.Paise > t.AmountPaise:
			return nil, nil, ErrPatch("owed must be above zero and no more than the payment")
		case strings.TrimSpace(owed.From) == "":
			return nil, nil, ErrPatch("say who owes it")
		}
		total = owed.Paise
		set["owed_from"], set["owed_paise"] = strings.TrimSpace(owed.From), total
		back = min(back, total)
		set["repaid_paise"] = back
	}
	if repaid != nil {
		switch {
		case total == 0:
			return nil, nil, ErrPatch("mark it as owed first")
		case *repaid < 0 || *repaid > total:
			return nil, nil, ErrPatch("repaid must be between zero and the amount owed")
		}
		set["repaid_paise"] = *repaid
	}
	return set, unset, nil
}

// planSplits checks a split of t: two to ten parts, each above zero, adding up to the whole payment, on money going out that is not owed back. Pure.
func planSplits(t Transaction, parts []Split) error {
	if len(parts) == 0 {
		return nil
	}
	var sum int64
	for _, p := range parts {
		if p.Paise <= 0 {
			return ErrPatch("each part must be above zero")
		}
		sum += p.Paise
	}
	switch {
	case len(parts) < 2 || len(parts) > 10:
		return ErrPatch("split a payment into 2 to 10 parts")
	case t.Direction == "credit":
		return ErrPatch("only money going out can be split")
	case t.OwedPaise > 0:
		return ErrPatch("remove what is owed back before splitting")
	case sum != t.AmountPaise:
		return ErrPatch("the parts must add up to the whole payment")
	}
	return nil
}

func (s *Service) Patch(ctx context.Context, id bson.ObjectID, p Patch) error {
	var cat, card *bson.ObjectID
	var clearCard bool
	if p.CategoryID != nil {
		c, err := bson.ObjectIDFromHex(*p.CategoryID)
		if err != nil {
			return ErrPatch("category_id is not a valid id")
		}
		if ok, err := s.categories.Exists(ctx, c); err != nil {
			return err
		} else if !ok {
			return ErrPatch("category not found")
		}
		cat = &c
	}
	if p.PaidCardID != nil {
		if clearCard = *p.PaidCardID == ""; !clearCard {
			c, err := bson.ObjectIDFromHex(*p.PaidCardID)
			if err != nil {
				return ErrPatch("paid_card_id is not a valid id")
			}
			if ok, err := s.cards.Exists(ctx, c); err != nil {
				return err
			} else if !ok {
				return ErrPatch("card not found")
			}
			card = &c
		}
	}
	var parts []Split
	if p.Splits != nil {
		for _, sp := range *p.Splits {
			c, err := bson.ObjectIDFromHex(sp.CategoryID)
			if err != nil {
				return ErrPatch("split category is not a valid id")
			}
			if ok, err := s.categories.Exists(ctx, c); err != nil {
				return err
			} else if !ok {
				return ErrPatch("split category not found")
			}
			parts = append(parts, Split{CategoryID: c, Paise: sp.Paise})
		}
	}
	hasOwed := p.Owed != nil || p.RepaidPaise != nil
	if cat == nil && card == nil && !clearCard && !hasOwed && p.Splits == nil {
		return ErrPatch("nothing to change")
	}
	var t Transaction
	if err := s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		return err
	}
	set, unset := bson.M{}, bson.M{}
	if cat != nil || card != nil || clearCard {
		cp, err := s.categories.CardPaymentsID(ctx)
		if err != nil {
			return err
		}
		if set, unset, err = planPatch(t, cp, cat, card, clearCard); err != nil {
			return err
		}
	}
	if hasOwed {
		oset, ounset, err := planOwed(t, p.Owed, p.RepaidPaise)
		if err != nil {
			return err
		}
		maps.Copy(set, oset)
		maps.Copy(unset, ounset)
	}
	switch {
	case p.Splits != nil:
		if err := planSplits(t, parts); err != nil {
			return err
		}
		if len(parts) > 0 {
			set["splits"] = parts
		} else {
			unset["splits"] = ""
		}
	case hasOwed && len(t.Splits) > 0 && p.Owed != nil && p.Owed.Paise > 0:
		return ErrPatch("remove the split before marking money owed back")
	case cat != nil && len(t.Splits) > 0:
		unset["splits"] = "" // picking one category replaces the split
	}
	upd := bson.M{}
	if len(set) > 0 {
		upd["$set"] = set
	}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	_, err := s.col.UpdateByID(ctx, id, upd)
	return err
}
