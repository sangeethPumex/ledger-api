package transactions

import (
	"errors"
	"net/http"
	"strconv"

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
	var nr *bool
	if q := r.URL.Query().Get("needs_review"); q != "" {
		b, err := strconv.ParseBool(q)
		if err != nil {
			httpx.Err(w, 400, "needs_review must be true or false")
			return
		}
		nr = &b
	}
	limit := int64(50)
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n < 1 || n > 1000 {
			httpx.Err(w, 400, "limit must be 1-1000")
			return
		}
		limit = n
	}
	ts, err := c.svc.List(r.Context(), nr, limit)
	if err != nil {
		httpx.Err(w, 500, "could not list transactions")
		return
	}
	httpx.WriteJSON(w, 200, ts)
}

func (c *Controller) Summary(w http.ResponseWriter, r *http.Request) {
	s, err := c.svc.Summary(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not compute summary")
		return
	}
	httpx.WriteJSON(w, 200, s)
}
