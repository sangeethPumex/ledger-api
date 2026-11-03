// Package debts holds money people owe the user that no bank payment backs (a GPay group, cash lent).
// It stays apart from transactions, so it never touches balances or spending.
package debts

import (
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/httpx"
)

const Collection = "debts"

type Debt struct {
	ID          bson.ObjectID `bson:"_id" json:"id"`
	Person      string        `bson:"person" json:"person"`
	Group       string        `bson:"group,omitempty" json:"group,omitempty"` // the UPI app's group name
	Source      string        `bson:"source" json:"source"`                   // GPay, PhonePe, Paytm, UPI, Cash, Other
	Note        string        `bson:"note,omitempty" json:"note,omitempty"`
	AmountPaise int64         `bson:"amount_paise" json:"amount_paise"`
	PaidAt      *time.Time    `bson:"paid_at,omitempty" json:"paid_at,omitempty"`
	CreatedAt   time.Time     `bson:"created_at" json:"created_at"`
}

type Input struct {
	Person, Group, Source, Note string
	AmountPaise                 int64
}

// Clean trims in and returns what is wrong with it, or "". Pure.
func Clean(in *Input) string {
	in.Person, in.Group, in.Source, in.Note = strings.TrimSpace(in.Person), strings.TrimSpace(in.Group), strings.TrimSpace(in.Source), strings.TrimSpace(in.Note)
	switch {
	case in.Person == "" || in.Source == "":
		return "person and source are required"
	case len(in.Person) > 60 || len(in.Group) > 60 || len(in.Source) > 30 || len(in.Note) > 300:
		return "text is too long"
	case in.AmountPaise <= 0:
		return "amount must be above zero"
	}
	return ""
}

type Service struct{ col *mongo.Collection }

func NewService(db *mongo.Database) *Service { return &Service{db.Collection(Collection)} }

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /debts", auth(http.HandlerFunc(c.create)))
	mux.Handle("GET /debts", auth(http.HandlerFunc(c.list)))
	mux.Handle("PATCH /debts/{id}", auth(http.HandlerFunc(c.setPaid)))
	mux.Handle("DELETE /debts/{id}", auth(http.HandlerFunc(c.delete)))
}

func (c *Controller) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Person      string `json:"person"`
		Group       string `json:"group"`
		Source      string `json:"source"`
		Note        string `json:"note"`
		AmountPaise int64  `json:"amount_paise"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	in := Input{req.Person, req.Group, req.Source, req.Note, req.AmountPaise}
	if msg := Clean(&in); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	d := Debt{ID: bson.NewObjectID(), Person: in.Person, Group: in.Group, Source: in.Source, Note: in.Note, AmountPaise: in.AmountPaise, CreatedAt: time.Now().UTC()}
	if _, err := c.svc.col.InsertOne(r.Context(), d); err != nil {
		httpx.Err(w, 500, "could not save")
		return
	}
	httpx.WriteJSON(w, 201, d)
}

func (c *Controller) list(w http.ResponseWriter, r *http.Request) {
	cur, err := c.svc.col.Find(r.Context(), bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(1000))
	out := []Debt{}
	if err == nil {
		err = cur.All(r.Context(), &out)
	}
	if err != nil {
		httpx.Err(w, 500, "could not list")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

// setPaid marks a debt paid (now) or unpaid again.
func (c *Controller) setPaid(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Paid bool `json:"paid"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	upd := bson.M{"$unset": bson.M{"paid_at": ""}}
	if req.Paid {
		upd = bson.M{"$set": bson.M{"paid_at": time.Now().UTC()}}
	}
	res, err := c.svc.col.UpdateByID(r.Context(), id, upd)
	switch {
	case err != nil:
		httpx.Err(w, 500, "could not update")
	case res.MatchedCount == 0:
		httpx.Err(w, 404, "not found")
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	if _, err := c.svc.col.DeleteOne(r.Context(), bson.M{"_id": id}); err != nil {
		httpx.Err(w, 500, "could not delete")
		return
	}
	w.WriteHeader(204)
}
