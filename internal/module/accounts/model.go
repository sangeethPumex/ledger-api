package accounts

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const Collection = "accounts"

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
