package budgets

import (
	"errors"
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

// maxLimitPaise is a sanity cap (10 crore rupees) on a monthly limit.
const maxLimitPaise = 10_000_000_000

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	out, err := c.svc.Statuses(r.Context(), time.Now())
	if err != nil {
		log.Printf("budgets: %v", err)
		httpx.Err(w, 500, "could not load budgets")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

// Set creates or changes the budget of the category in the path.
func (c *Controller) Set(w http.ResponseWriter, r *http.Request) {
	cat, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		LimitPaise int64 `json:"limit_paise"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if req.LimitPaise <= 0 || req.LimitPaise > maxLimitPaise {
		httpx.Err(w, 400, "limit must be above zero")
		return
	}
	if err := c.svc.Set(r.Context(), cat, req.LimitPaise); err != nil {
		httpx.Err(w, 500, "could not save budget")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	cat, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	switch err := c.svc.Delete(r.Context(), cat); {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "budget not found")
	default:
		httpx.Err(w, 500, "could not delete budget")
	}
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /budgets", auth(http.HandlerFunc(c.List)))
	mux.Handle("PUT /budgets/{id}", auth(http.HandlerFunc(c.Set))) // {id} is the category id
	mux.Handle("DELETE /budgets/{id}", auth(http.HandlerFunc(c.Delete)))
}
