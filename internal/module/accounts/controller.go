package accounts

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

var last4Re = regexp.MustCompile(`^[0-9]{4}$`)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var req Account
	if !httpx.Decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Bank) == "" || !last4Re.MatchString(req.Last4) {
		httpx.Err(w, 400, "name, bank and 4-digit last4 required")
		return
	}
	a, err := c.svc.Create(r.Context(), req)
	switch {
	case mongo.IsDuplicateKeyError(err):
		httpx.Err(w, 409, "account exists")
	case err != nil:
		httpx.Err(w, 500, "could not create account")
	default:
		httpx.WriteJSON(w, 201, a)
	}
}

func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name                *string    `json:"name"`
		OpeningBalancePaise *int64     `json:"opening_balance_paise"`
		OpeningAt           *time.Time `json:"opening_at"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	switch err := c.svc.Update(r.Context(), id, req.Name, req.OpeningBalancePaise, req.OpeningAt); err {
	case nil:
		w.WriteHeader(204)
	case mongo.ErrNoDocuments:
		httpx.Err(w, 404, "account not found")
	default:
		httpx.Err(w, 500, "could not update account")
	}
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	v, err := c.svc.List(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not list accounts")
		return
	}
	httpx.WriteJSON(w, 200, v)
}
