// Package profile stores the single "about me" document: salary, SIP/commitment totals, buffer, holdings typed by hand, goals.
package profile

import (
	"context"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/httpx"
	"ledger-backend/internal/ist"
)

const (
	Collection = "profile"
	docID      = "me"
)

// Amounts are rupees as typed by the user.
type Holding struct {
	Invested float64 `json:"invested" bson:"invested"`
	Current  float64 `json:"current" bson:"current"`
}

// Prior is spend and income from before tracking began, for one month ("YYYY-MM").
type Prior struct {
	YM     string  `json:"ym" bson:"ym"`
	Spent  float64 `json:"spent" bson:"spent"`
	Income float64 `json:"income" bson:"income"`
}

type Profile struct {
	ID           string    `json:"-" bson:"_id"`
	Salary       float64   `json:"salary" bson:"salary"`
	SalaryDay    int       `json:"salaryDay" bson:"salaryDay"`
	SIP          float64   `json:"sip" bson:"sip"` // used only while no funds are tracked
	FixedMonthly float64   `json:"fixedMonthly" bson:"fixedMonthly"`
	BufferTarget float64   `json:"bufferTarget" bson:"bufferTarget"`
	MF           Holding   `json:"mf" bson:"mf"` // used only while no funds are tracked
	Stocks       Holding   `json:"stocks" bson:"stocks"`
	Other        Holding   `json:"other" bson:"other"`
	Goals        string    `json:"goals" bson:"goals"`
	Prior        Prior     `json:"prior" bson:"prior"`
	UpdatedAt    time.Time `json:"updatedAt" bson:"updatedAt"`
}

func neg(v ...float64) bool {
	for _, x := range v {
		if x < 0 || x != x { // negative or NaN
			return true
		}
	}
	return false
}

func (p *Profile) Validate() string {
	switch {
	case p.SalaryDay < 1 || p.SalaryDay > 31:
		return "salaryDay must be 1-31"
	case neg(p.Salary, p.SIP, p.FixedMonthly, p.BufferTarget, p.MF.Invested, p.MF.Current, p.Stocks.Invested, p.Stocks.Current, p.Other.Invested, p.Other.Current, p.Prior.Spent, p.Prior.Income):
		return "amounts must not be negative"
	case len(p.Goals) > 5000:
		return "goals too long"
	}
	return ""
}

type Service struct{ col *mongo.Collection }

func NewService(db *mongo.Database) *Service { return &Service{db.Collection(Collection)} }

// Get returns the saved profile, or a blank one (salary day 1, zero UpdatedAt) when none exists yet.
func (s *Service) Get(ctx context.Context) (*Profile, error) {
	p := &Profile{SalaryDay: 1}
	err := s.col.FindOne(ctx, bson.M{"_id": docID}).Decode(p)
	if err == mongo.ErrNoDocuments {
		return &Profile{SalaryDay: 1}, nil
	}
	return p, err
}

// stampPrior decides which month the before-Catalyst figures belong to: the client never sets it. Unchanged amounts keep their old
// month, so re-saving next month does not re-apply last month's spend.
func stampPrior(old, in Prior, now time.Time) Prior {
	if in.Spent == old.Spent && in.Income == old.Income && old.YM != "" {
		in.YM = old.YM
	} else {
		in.YM = now.In(ist.Loc).Format("2006-01")
	}
	return in
}

func (s *Service) Put(ctx context.Context, p Profile) error {
	old, err := s.Get(ctx)
	if err != nil {
		return err
	}
	p.ID, p.UpdatedAt = docID, time.Now().UTC()
	p.Prior = stampPrior(old.Prior, p.Prior, p.UpdatedAt)
	_, err = s.col.ReplaceOne(ctx, bson.M{"_id": docID}, p, options.Replace().SetUpsert(true))
	return err
}

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Get(w http.ResponseWriter, r *http.Request) {
	p, err := c.svc.Get(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not load profile")
		return
	}
	httpx.WriteJSON(w, 200, p)
}

func (c *Controller) Put(w http.ResponseWriter, r *http.Request) {
	var p Profile
	if !httpx.Decode(w, r, &p) {
		return
	}
	if msg := p.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	if err := c.svc.Put(r.Context(), p); err != nil {
		httpx.Err(w, 500, "could not save profile")
		return
	}
	w.WriteHeader(204)
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /profile", auth(http.HandlerFunc(c.Get)))
	mux.Handle("PUT /profile", auth(http.HandlerFunc(c.Put)))
}
