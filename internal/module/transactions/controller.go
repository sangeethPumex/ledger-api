package transactions

import (
	"errors"
	"net/http"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

// Reparse retries one message (used for failed ones; parsed/ignored are no-ops).
func (c *Controller) Reparse(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	switch err := c.svc.Parse(r.Context(), id); {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "message not found")
	default:
		httpx.Err(w, 502, "parse failed; see fail_reason on the raw message")
	}
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	f, msg := ParseFilter(r.URL.Query())
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	ts, err := c.svc.List(r.Context(), f)
	if err != nil {
		httpx.Err(w, 500, "could not list transactions")
		return
	}
	httpx.WriteJSON(w, 200, ts)
}

// Patch sets a transaction's category and, for card payments, the card it paid.
func (c *Controller) Patch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req Patch
	if !httpx.Decode(w, r, &req) {
		return
	}
	err := c.svc.Patch(r.Context(), id, req)
	var bad ErrPatch
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.As(err, &bad):
		httpx.Err(w, 400, bad.Error())
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "transaction not found")
	default:
		httpx.Err(w, 500, "could not update transaction")
	}
}

func (c *Controller) Summary(w http.ResponseWriter, r *http.Request) {
	s, err := c.svc.Summary(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not compute summary")
		return
	}
	httpx.WriteJSON(w, 200, s)
}
