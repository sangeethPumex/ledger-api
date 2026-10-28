package mandates

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

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	p, err := c.svc.Plan(r.Context(), time.Now())
	if err != nil {
		log.Printf("list mandates: %v", err)
		httpx.Err(w, 500, "could not list mandates")
		return
	}
	httpx.WriteJSON(w, 200, p)
}

func (c *Controller) Suggestions(w http.ResponseWriter, r *http.Request) {
	s, err := c.svc.Suggestions(r.Context(), time.Now())
	if err != nil {
		log.Printf("mandate suggestions: %v", err)
		httpx.Err(w, 500, "could not look for recurring payments")
		return
	}
	httpx.WriteJSON(w, 200, s)
}

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	m, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	if err := c.svc.Create(r.Context(), m); err != nil {
		httpx.Err(w, 500, "could not create mandate")
		return
	}
	w.WriteHeader(201)
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
	m, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	done(w, c.svc.Update(r.Context(), id, m), "update")
}

func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	done(w, c.svc.Delete(r.Context(), id), "delete")
}

// done replies 204 for success, 404 for a missing mandate and 500 otherwise.
func done(w http.ResponseWriter, err error, verb string) {
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "mandate not found")
	default:
		httpx.Err(w, 500, "could not "+verb+" mandate")
	}
}
