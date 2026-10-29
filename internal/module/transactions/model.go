package transactions

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const Collection = "transactions"

var validTypes = map[string]bool{
	"expense": true, "income": true, "transfer": true, "card_bill_payment": true,
	"refund": true, "atm": true, "emi": true,
}

// maxPaise is a sanity cap (10 crore rupees); anything above is a bad extraction.
const maxPaise = 10_000_000_000

// Split is one category's share of a payment.
type Split struct {
	CategoryID bson.ObjectID `bson:"category_id" json:"category_id"`
	Paise      int64         `bson:"paise" json:"paise"`
}

type Transaction struct {
	ID                  bson.ObjectID  `bson:"_id" json:"id"`
	RawID               bson.ObjectID  `bson:"raw_id" json:"raw_id"`
	Type                string         `bson:"type" json:"type"`
	AmountPaise         int64          `bson:"amount_paise" json:"amount_paise"`
	OccurredAt          time.Time      `bson:"occurred_at" json:"occurred_at"`
	ReceivedAt          time.Time      `bson:"received_at" json:"received_at"` // when the SMS arrived (raw_transactions.received_at): the real payment time
	Bank                string         `bson:"bank,omitempty" json:"bank,omitempty"`
	AccountLast4        string         `bson:"account_last4,omitempty" json:"account_last4,omitempty"`
	Direction           string         `bson:"direction,omitempty" json:"direction,omitempty"` // debit | credit
	AccountID           *bson.ObjectID `bson:"account_id,omitempty" json:"account_id,omitempty"`
	CardID              *bson.ObjectID `bson:"card_id,omitempty" json:"card_id,omitempty"`
	Ref                 string         `bson:"ref,omitempty" json:"ref,omitempty"`
	RawVendor           string         `bson:"raw_vendor,omitempty" json:"raw_vendor,omitempty"`
	UPIID               string         `bson:"upi_id,omitempty" json:"upi_id,omitempty"`
	VendorID            *bson.ObjectID `bson:"vendor_id,omitempty" json:"vendor_id,omitempty"`
	CategoryID          *bson.ObjectID `bson:"category_id,omitempty" json:"category_id,omitempty"`
	// PaidCardID is the credit card this payment settles (category "Credit card payments"). The type then becomes card_bill_payment
	// so the payment is not counted as spending; TypeBefore remembers the parsed type to restore when unlinked.
	PaidCardID *bson.ObjectID `bson:"paid_card_id,omitempty" json:"paid_card_id,omitempty"`
	TypeBefore string         `bson:"type_before,omitempty" json:"-"`
	// CategorySet marks a category chosen on this transaction, which vendor changes must not overwrite.
	CategorySet         bool `bson:"category_set,omitempty" json:"category_set,omitempty"`
	// Owed: part of this payment someone else owes back (paid for a friend). It is not the user's own spending, so totals leave it out.
	OwedFrom    string `bson:"owed_from,omitempty" json:"owed_from,omitempty"`
	OwedPaise   int64  `bson:"owed_paise,omitempty" json:"owed_paise,omitempty"`
	RepaidPaise int64  `bson:"repaid_paise,omitempty" json:"repaid_paise,omitempty"`
	// Splits spreads one payment over several categories (Amazon: groceries plus a gift). They add up to the whole payment, so a split
	// payment cannot also be owed. Empty means the payment sits in CategoryID alone.
	Splits              []Split        `bson:"splits,omitempty" json:"splits,omitempty"`
	NeedsReview         bool          `bson:"needs_review" json:"needs_review"`
	ReviewReason        string         `bson:"review_reason,omitempty" json:"review_reason,omitempty"`
	SuggestedCategoryID *bson.ObjectID `bson:"suggested_category_id,omitempty" json:"suggested_category_id,omitempty"`
	Model               string         `bson:"model" json:"model"`
	PromptVersion       string         `bson:"prompt_version" json:"prompt_version"`
	CreatedAt           time.Time      `bson:"created_at" json:"created_at"`
}
