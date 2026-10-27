package emis

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/legacy"
)

type Service struct{ col *mongo.Collection }

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	return &Service{col}, legacy.StringIDs(ctx, col)
}

func (s *Service) get(ctx context.Context, id bson.ObjectID) (*Emi, error) {
	var e Emi
	return &e, s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&e)
}

func (s *Service) save(ctx context.Context, e *Emi) error {
	_, err := s.col.ReplaceOne(ctx, bson.M{"_id": e.ID}, e)
	return err
}

func (s *Service) List(ctx context.Context, now time.Time) ([]View, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var all []Emi
	if err := cur.All(ctx, &all); err != nil {
		return nil, err
	}
	out := make([]View, len(all))
	for i, e := range all {
		out[i] = ViewOf(e, now)
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, in Input) (*Emi, error) {
	e := &Emi{ID: bson.NewObjectID(), Name: trim(in.Name), Kind: in.Kind, Amount: in.Amount, DueDay: in.DueDay, Total: in.Total, Paid: in.Paid, Rate: in.Rate, Balance: in.Balance, Original: in.Original}
	if e.Kind == "card" {
		e.Total, e.Paid = 0, 0
	}
	_, err := s.col.InsertOne(ctx, e)
	return e, err
}

// Update replaces the definition and keeps the payment history.
func (s *Service) Update(ctx context.Context, id bson.ObjectID, in Input) error {
	e, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	e.Name, e.Kind, e.Amount, e.DueDay, e.Total, e.Paid = trim(in.Name), in.Kind, in.Amount, in.DueDay, in.Total, in.Paid
	e.Rate, e.Balance, e.Original = in.Rate, in.Balance, in.Original
	if e.Kind == "card" {
		e.Total, e.Paid = 0, 0
	}
	return s.save(ctx, e)
}

func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	r, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

// PaymentReq: kind "emi" records one ordinary instalment (split estimated); "extra" records what the user entered.
type PaymentReq struct {
	Kind     string  `json:"kind"`
	Amount   float64 `json:"amount"`
	Interest float64 `json:"interest"`
	Fees     float64 `json:"fees"`
}

// AddPayment applies a payment; the error text is safe to show for a 400.
func (s *Service) AddPayment(ctx context.Context, id bson.ObjectID, req PaymentReq) (msg string, err error) {
	e, err := s.get(ctx, id)
	if err != nil {
		return "", err
	}
	p := Payment{ID: bson.NewObjectID().Hex(), Date: time.Now().UTC().Format(time.RFC3339), Kind: req.Kind}
	switch req.Kind {
	case "emi":
		p.Amount = e.Amount
		p.Principal, p.Interest = Split(e)
	case "extra":
		if req.Amount <= 0 || bad(req.Interest, req.Fees) || req.Interest+req.Fees > req.Amount {
			return "amount must be positive and cover interest and fees", nil
		}
		p.Amount, p.Interest, p.Fees, p.Principal = req.Amount, req.Interest, req.Fees, r2(req.Amount-req.Interest-req.Fees)
	default:
		return "kind must be emi or extra", nil
	}
	Apply(e, p)
	return "", s.save(ctx, e)
}

func (s *Service) UndoLast(ctx context.Context, id bson.ObjectID) (bool, error) {
	e, err := s.get(ctx, id)
	if err != nil {
		return false, err
	}
	if !Undo(e) {
		return false, nil
	}
	return true, s.save(ctx, e)
}

// SetBalance reconciles the balance with a lender statement.
func (s *Service) SetBalance(ctx context.Context, id bson.ObjectID, balance float64) error {
	r, err := s.col.UpdateByID(ctx, id, bson.M{"$set": bson.M{"balance": r2(balance)}})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) Impact(ctx context.Context, id bson.ObjectID, extra float64) (map[string]any, error) {
	e, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	saved, months, left, ok := Impact(e, extra)
	if !ok {
		return map[string]any{"available": false}, nil
	}
	return map[string]any{"available": true, "interestSaved": saved, "monthsSaved": months, "monthsLeft": left}, nil
}

type Brief struct {
	Name   string  `json:"name" bson:"name"`
	Amount float64 `json:"amount" bson:"amount"`
	DueDay int     `json:"dueDay" bson:"dueDay"`
}

type Next struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
	Days   int     `json:"days"`
}

type Summary struct {
	Monthly       float64 `json:"monthly"`     // sum of active instalments
	Outstanding   float64 `json:"outstanding"` // amount left across active ones
	ActiveCount   int     `json:"activeCount"`
	PaidSoFar     float64 `json:"paidSoFar"`
	PrincipalPaid float64 `json:"principalPaid"`
	InterestPaid  float64 `json:"interestPaid"` // interest and fees
	Active        []Brief `json:"active"`
	Next          *Next   `json:"next,omitempty"`
}

// Summary totals every EMI in one aggregation; per-loan figures are not needed here.
func (s *Service) Summary(ctx context.Context, now time.Time) (*Summary, error) {
	hasBalance := bson.M{"$ne": bson.A{bson.M{"$type": "$balance"}, "missing"}}
	payments := bson.M{"$ifNull": bson.A{"$payments", bson.A{}}}
	pipe := mongo.Pipeline{
		{{Key: "$addFields", Value: bson.M{
			"_active": bson.M{"$cond": bson.A{hasBalance, bson.M{"$gt": bson.A{"$balance", 0}}, bson.M{"$lt": bson.A{"$paid", "$total"}}}},
			"_left":   bson.M{"$ifNull": bson.A{"$balance", bson.M{"$multiply": bson.A{"$amount", bson.M{"$subtract": bson.A{"$total", "$paid"}}}}}},
			"_paid":   bson.M{"$cond": bson.A{bson.M{"$gt": bson.A{bson.M{"$size": payments}, 0}}, bson.M{"$sum": "$payments.amount"}, bson.M{"$multiply": bson.A{"$amount", "$paid"}}}},
			"_princ":  bson.M{"$sum": "$payments.principal"},
			"_int":    bson.M{"$add": bson.A{bson.M{"$sum": "$payments.interest"}, bson.M{"$sum": "$payments.fees"}}},
		}}},
		{{Key: "$facet", Value: bson.M{
			"totals": bson.A{bson.M{"$group": bson.M{
				"_id":         nil,
				"monthly":     bson.M{"$sum": bson.M{"$cond": bson.A{"$_active", "$amount", 0}}},
				"outstanding": bson.M{"$sum": bson.M{"$cond": bson.A{"$_active", "$_left", 0}}},
				"active":      bson.M{"$sum": bson.M{"$cond": bson.A{"$_active", 1, 0}}},
				"paid":        bson.M{"$sum": "$_paid"},
				"princ":       bson.M{"$sum": "$_princ"},
				"interest":    bson.M{"$sum": "$_int"},
			}}},
			"active": bson.A{bson.M{"$match": bson.M{"_active": true}}, bson.M{"$project": bson.M{"_id": 0, "name": 1, "amount": 1, "dueDay": 1}}},
		}}},
	}
	cur, err := s.col.Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	var res []struct {
		Totals []struct {
			Monthly     float64 `bson:"monthly"`
			Outstanding float64 `bson:"outstanding"`
			Active      int     `bson:"active"`
			Paid        float64 `bson:"paid"`
			Princ       float64 `bson:"princ"`
			Interest    float64 `bson:"interest"`
		} `bson:"totals"`
		Active []Brief `bson:"active"`
	}
	if err := cur.All(ctx, &res); err != nil {
		return nil, err
	}
	out := &Summary{Active: []Brief{}}
	if len(res) == 0 {
		return out, nil
	}
	if t := res[0].Totals; len(t) > 0 {
		out.Monthly, out.Outstanding, out.ActiveCount, out.PaidSoFar, out.PrincipalPaid, out.InterestPaid = t[0].Monthly, t[0].Outstanding, t[0].Active, t[0].Paid, t[0].Princ, t[0].Interest
	}
	out.Active = append(out.Active, res[0].Active...)
	sort.Slice(out.Active, func(i, j int) bool {
		return ist.DaysUntil(out.Active[i].DueDay, now) < ist.DaysUntil(out.Active[j].DueDay, now)
	})
	if len(out.Active) > 0 {
		a := out.Active[0]
		out.Next = &Next{Name: a.Name, Amount: a.Amount, Days: ist.DaysUntil(a.DueDay, now)}
	}
	return out, nil
}
