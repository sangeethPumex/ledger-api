package accounts

import (
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const Collection = "accounts"

// slugRe is the "••2411" tail an account label picks up (symbols, then the last digits). U+FFFD counts: a bullet that was mangled in transit still matches.
var slugRe = regexp.MustCompile(`[^\p{L}\p{N}\s]{1,6}\s*\d{2,4}\s*$`)

// CleanBank is the bank's name alone: "Federal Bank ••2411" becomes "Federal Bank". The last 4 digits identify the account
// and live in their own field, so they do not belong in the name.
func CleanBank(s string) string {
	s = strings.TrimSpace(s)
	if c := strings.TrimSpace(slugRe.ReplaceAllString(s, "")); c != "" {
		return c
	}
	return s
}

// Account is a bank account. Balance = opening balance + transactions on or after OpeningAt.
type Account struct {
	ID                  bson.ObjectID `bson:"_id" json:"id"`
	Name                string        `bson:"name" json:"name"`
	Bank                string        `bson:"bank" json:"bank"`
	Last4               string        `bson:"last4" json:"last4"`
	OpeningBalancePaise int64         `bson:"opening_balance_paise" json:"opening_balance_paise"`
	OpeningAt           time.Time     `bson:"opening_at" json:"opening_at"`
	CreatedAt           time.Time     `bson:"created_at" json:"created_at"`
}

// View is an Account with its computed balance.
type View struct {
	Account      `bson:",inline"`
	BalancePaise int64 `json:"balance_paise"`
}
