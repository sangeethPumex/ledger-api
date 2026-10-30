package portfolio

import (
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Overview(w http.ResponseWriter, r *http.Request) {
	o, err := c.svc.Overview(r.Context(), time.Now())
	if err != nil {
		log.Printf("portfolio: %v", err)
		httpx.Err(w, 500, "could not build the portfolio")
		return
	}
	httpx.WriteJSON(w, 200, o)
}

func (c *Controller) SetTarget(w http.ResponseWriter, r *http.Request) {
	var t Target
	if !httpx.Decode(w, r, &t) {
		return
	}
	if msg := t.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	c.done(w, c.svc.SetTarget(r.Context(), &t))
}

func (c *Controller) ClearTarget(w http.ResponseWriter, r *http.Request) {
	c.done(w, c.svc.SetTarget(r.Context(), nil))
}

func (c *Controller) done(w http.ResponseWriter, err error) {
	switch {
	case err == mongo.ErrNoDocuments:
		httpx.Err(w, 404, "entry not found")
	case err != nil:
		log.Printf("portfolio write: %v", err)
		httpx.Err(w, 500, "could not save")
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) Income(w http.ResponseWriter, r *http.Request) {
	in, err := c.svc.Income(r.Context(), time.Now())
	if err != nil {
		log.Printf("income: %v", err)
		httpx.Err(w, 500, "could not read income")
		return
	}
	httpx.WriteJSON(w, 200, in)
}

func (c *Controller) AddEntry(w http.ResponseWriter, r *http.Request) {
	var in EntryInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	if msg := in.Validate(time.Now()); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	e, err := c.svc.AddEntry(r.Context(), in)
	if err != nil {
		c.done(w, err)
		return
	}
	httpx.WriteJSON(w, 201, e)
}

func (c *Controller) DeleteEntry(w http.ResponseWriter, r *http.Request) {
	if id, ok := httpx.PathID(w, r); ok {
		c.done(w, c.svc.DeleteEntry(r.Context(), id))
	}
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return auth(f) }
	mux.Handle("GET /portfolio", h(c.Overview))
	mux.Handle("PUT /portfolio/target", h(c.SetTarget))
	mux.Handle("DELETE /portfolio/target", h(c.ClearTarget))
	mux.Handle("GET /portfolio/income", h(c.Income))
	mux.Handle("POST /portfolio/income", h(c.AddEntry))
	mux.Handle("DELETE /portfolio/income/{id}", h(c.DeleteEntry))
}
