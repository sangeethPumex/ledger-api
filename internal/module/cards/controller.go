package cards

import (
	"errors"
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

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
