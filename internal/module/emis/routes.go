package emis

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

func trim(s string) string { return strings.TrimSpace(s) }

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

// notFound maps a missing document to 404 and anything else to 500.
func notFound(w http.ResponseWriter, err error, what string) {
	if err == mongo.ErrNoDocuments {
		httpx.Err(w, 404, what+" not found")
		return
	}
	httpx.Err(w, 500, "could not update "+what)
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	items, err := c.svc.List(r.Context(), now)
	if err == nil {
		var sum *Summary
		if sum, err = c.svc.Summary(r.Context(), now); err == nil {
			httpx.WriteJSON(w, 200, map[string]any{"items": items, "summary": sum})
			return
		}
	}
	log.Printf("list emis: %v", err)
	httpx.Err(w, 500, "could not list EMIs")
}

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	if msg := in.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	e, err := c.svc.Create(r.Context(), in)
	if errors.Is(err, ErrNoCard) {
		httpx.Err(w, 400, err.Error())
		return
	}
	if err != nil {
		httpx.Err(w, 500, "could not create EMI")
		return
	}
	httpx.WriteJSON(w, 201, ViewOf(*e, time.Now()))
}

func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	if msg := in.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	if err := c.svc.Update(r.Context(), id, in); err != nil {
		if errors.Is(err, ErrNoCard) {
			httpx.Err(w, 400, err.Error())
			return
		}
		notFound(w, err, "EMI")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	if err := c.svc.Delete(r.Context(), id); err != nil {
		notFound(w, err, "EMI")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) AddPayment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req PaymentReq
	if !httpx.Decode(w, r, &req) {
		return
	}
	msg, err := c.svc.AddPayment(r.Context(), id, req)
	switch {
	case err != nil:
		notFound(w, err, "EMI")
	case msg != "":
		httpx.Err(w, 400, msg)
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) UndoLast(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	did, err := c.svc.UndoLast(r.Context(), id)
	switch {
	case err != nil:
		notFound(w, err, "EMI")
	case !did:
		httpx.Err(w, 400, "no payments to undo")
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) SetBalance(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Balance float64 `json:"balance"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if bad(req.Balance) {
		httpx.Err(w, 400, "balance must not be negative")
		return
	}
	if err := c.svc.SetBalance(r.Context(), id, req.Balance); err != nil {
		notFound(w, err, "EMI")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) Impact(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	extra, err := strconv.ParseFloat(r.URL.Query().Get("extra"), 64)
	if err != nil || bad(extra) {
		httpx.Err(w, 400, "extra must be a non-negative number")
		return
	}
	out, err := c.svc.Impact(r.Context(), id, extra)
	if err != nil {
		notFound(w, err, "EMI")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (c *Controller) Schedule(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	out, err := c.svc.Schedule(r.Context(), id, time.Now())
	if err != nil {
		notFound(w, err, "EMI")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

// Planner compares avalanche and snowball for ?extra= rupees of prepayment a month (default 0).
func (c *Controller) Planner(w http.ResponseWriter, r *http.Request) {
	extra := 0.0
	if q := r.URL.Query().Get("extra"); q != "" {
		var err error
		if extra, err = strconv.ParseFloat(q, 64); err != nil || bad(extra) || extra > 1e8 {
			httpx.Err(w, 400, "extra must be a number between 0 and 100000000")
			return
		}
	}
	out, err := c.svc.Plan(r.Context(), extra)
	if err != nil {
		log.Printf("emi plan: %v", err)
		httpx.Err(w, 500, "could not build payoff plan")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (c *Controller) AddRate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req RateReq
	if !httpx.Decode(w, r, &req) {
		return
	}
	msg, err := c.svc.AddRate(r.Context(), id, req, time.Now())
	switch {
	case err != nil:
		notFound(w, err, "EMI")
	case msg != "":
		httpx.Err(w, 400, msg)
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) RemoveRate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	did, err := c.svc.RemoveRate(r.Context(), id, r.PathValue("from"), time.Now())
	switch {
	case err != nil:
		notFound(w, err, "EMI")
	case !did:
		httpx.Err(w, 400, "no rate change on that date")
	default:
		w.WriteHeader(204)
	}
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return auth(f) }
	mux.Handle("GET /emis", h(c.List))
	mux.Handle("POST /emis", h(c.Create))
	mux.Handle("PUT /emis/{id}", h(c.Update))
	mux.Handle("DELETE /emis/{id}", h(c.Delete))
	mux.Handle("POST /emis/{id}/payments", h(c.AddPayment))
	mux.Handle("DELETE /emis/{id}/payments/last", h(c.UndoLast))
	mux.Handle("PUT /emis/{id}/balance", h(c.SetBalance))
	mux.Handle("GET /emis/payoff", h(c.Planner))
	mux.Handle("GET /emis/{id}/impact", h(c.Impact))
	mux.Handle("GET /emis/{id}/schedule", h(c.Schedule))
	mux.Handle("POST /emis/{id}/rates", h(c.AddRate))
	mux.Handle("DELETE /emis/{id}/rates/{from}", h(c.RemoveRate))
}
