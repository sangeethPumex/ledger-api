package emis

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// carLedger is the car loan statement, 23 Jan to 22 Oct 2026, as the lender printed it.
var carLedger = []Period{
	{"2026-01-23", "2026-02-22", 4905, 11578, 0, 6673, 743327},
	{"2026-02-23", "2026-03-22", 4391, 11578, 0, 7187, 736140},
	{"2026-03-23", "2026-04-22", 4814, 11578, 0, 6764, 729376},
	{"2026-04-23", "2026-05-22", 4615, 10578, 1000, 6963, 722413},
	{"2026-05-23", "2026-06-22", 4724, 11578, 0, 6854, 715559},
	{"2026-06-23", "2026-07-22", 4529, 11578, 0, 7049, 708510},
	{"2026-07-23", "2026-08-22", 4633, 11578, 0, 6945, 701565},
	{"2026-08-23", "2026-09-22", 4589, 11578, 0, 6989, 694576},
	{"2026-09-23", "2026-10-22", 4467, 11578, 150, 7261, 687315},
}

// seedCarLoan loads the statement above once, onto an existing loan with "car" in its name or a new one. Never runs again once a loan has a ledger.
// ponytail: tenure (84) is worked out from the amount and 7.70%; confirm it with the lender.
func (s *Service) seedCarLoan(ctx context.Context) error {
	var e Emi
	err := s.col.FindOne(ctx, bson.M{"kind": "loan", "name": bson.M{"$regex": "car", "$options": "i"}}).Decode(&e)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		e = Emi{ID: bson.NewObjectID(), Name: "Car Loan", Kind: "loan"}
	case err != nil:
		return err
	case len(e.Ledger) > 0:
		if e.DueDay == 22 { // first load guessed 22; the EMI is debited on the 23rd. Remove once every database has run this.
			_, err = s.col.UpdateByID(ctx, e.ID, bson.M{"$set": bson.M{"dueDay": 23}})
		}
		return err
	}
	rate, bal, orig := 7.95, carLedger[len(carLedger)-1].Closing, 750000.0
	e.Amount, e.DueDay, e.Total, e.Paid = 11578, 23, 84, len(carLedger)
	e.Rate, e.Balance, e.Original, e.RateType = &rate, &bal, &orig, "variable"
	e.Rates = []RateChange{{Rate: 7.70}, {From: "2026-09-23", Rate: 7.95}}
	e.Ledger = carLedger
	_, err = s.col.ReplaceOne(ctx, bson.M{"_id": e.ID}, e, options.Replace().SetUpsert(true))
	return err
}
