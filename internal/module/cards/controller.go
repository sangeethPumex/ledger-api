package cards

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

// Payments is GET /cards/{id}/payments: the card's payment history, newest first (up to 100).
func (c *Controller) Payments(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	if found, err := c.svc.Exists(r.Context(), id); err != nil {
		httpx.Err(w, 500, "could not list payments")
		return
	} else if !found {
		httpx.Err(w, 404, "card not found")
		return
	}
	ps, err := c.svc.Payments(r.Context(), id, 100)
	if err != nil {
		log.Printf("cards: payments: %v", err)
		httpx.Err(w, 500, "could not list payments")
		return
	}
	httpx.WriteJSON(w, 200, ps)
}

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var req Card
	if !httpx.Decode(w, r, &req) {
		return
	}
	if msg := req.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	card, err := c.svc.Create(r.Context(), req)
	var share ErrShare
	switch {
	case errors.As(err, &share):
		httpx.Err(w, 400, share.Error())
	case mongo.IsDuplicateKeyError(err):
		httpx.Err(w, 409, "card exists")
	case err != nil:
		httpx.Err(w, 500, "could not create card")
	default:
		httpx.WriteJSON(w, 201, card)
	}
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	cards, err := c.svc.Views(r.Context(), time.Now())
	if err != nil {
		log.Printf("list cards: %v", err)
		httpx.Err(w, 500, "could not list cards")
		return
	}
	httpx.WriteJSON(w, 200, cards)
}

func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req Patch
	if !httpx.Decode(w, r, &req) {
		return
	}
	if msg := req.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	err := c.svc.Update(r.Context(), id, req)
	var share ErrShare
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.As(err, &share):
		httpx.Err(w, 400, share.Error())
	case err == mongo.ErrNoDocuments:
		httpx.Err(w, 404, "card not found")
	case mongo.IsDuplicateKeyError(err):
		httpx.Err(w, 409, "card exists")
	default:
		httpx.Err(w, 500, "could not update card")
	}
}

// RecordPayment is POST /cards/{id}/payments: record a bill payment by hand. No bank transaction is created.
func (c *Controller) RecordPayment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req PaymentInput
	if !httpx.Decode(w, r, &req) {
		return
	}
	if msg := req.Validate(time.Now()); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	p, err := c.svc.RecordPayment(r.Context(), id, req)
	switch {
	case err == mongo.ErrNoDocuments:
		httpx.Err(w, 404, "card not found")
	case err != nil:
		log.Printf("cards: record payment: %v", err)
		httpx.Err(w, 500, "could not record payment")
	default:
		httpx.WriteJSON(w, 201, map[string]string{"id": p.ID.Hex()})
	}
}

// DeletePayment is DELETE /cards/{id}/payments/{pid}: remove a payment recorded by hand.
func (c *Controller) DeletePayment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	pid, err := bson.ObjectIDFromHex(r.PathValue("pid"))
	if err != nil {
		httpx.Err(w, 400, "invalid payment id")
		return
	}
	switch err := c.svc.DeletePayment(r.Context(), id, pid); {
	case err == nil:
		w.WriteHeader(204)
	case err == mongo.ErrNoDocuments:
		httpx.Err(w, 404, "payment not found (only payments recorded by hand can be removed)")
	default:
		log.Printf("cards: delete payment: %v", err)
		httpx.Err(w, 500, "could not remove payment")
	}
}

// Activity is GET /cards/{id}/activity: the card's latest spends, refunds and payments.
func (c *Controller) Activity(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	limit := int64(8)
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = int64(min(n, 200))
	}
	a, err := c.svc.Activity(r.Context(), id, limit)
	if err != nil {
		log.Printf("cards: activity: %v", err)
		httpx.Err(w, 500, "could not load card activity")
		return
	}
	httpx.WriteJSON(w, 200, a)
}
