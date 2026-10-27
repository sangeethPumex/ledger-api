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
	NeedsReview         bool           `bson:"needs_review" json:"needs_review"`
	ReviewReason        string         `bson:"review_reason,omitempty" json:"review_reason,omitempty"`
	SuggestedCategoryID *bson.ObjectID `bson:"suggested_category_id,omitempty" json:"suggested_category_id,omitempty"`
	Model               string         `bson:"model" json:"model"`
	PromptVersion       string         `bson:"prompt_version" json:"prompt_version"`
	CreatedAt           time.Time      `bson:"created_at" json:"created_at"`
}
