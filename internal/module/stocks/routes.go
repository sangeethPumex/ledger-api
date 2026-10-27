package stocks

import (
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	p, err := c.svc.Portfolio(r.Context(), time.Now())
	if err != nil {
		log.Printf("list stocks: %v", err)
		httpx.Err(w, 500, "could not list stocks")
		return
	}
	httpx.WriteJSON(w, 200, p)
}

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	if msg := in.Validate(time.Now()); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	l, err := c.svc.Create(r.Context(), in)
	if err != nil {
		log.Printf("create stock: %v", err)
		httpx.Err(w, 500, "could not add stock")
		return
	}
	httpx.WriteJSON(w, 201, l)
}

// write runs a Update/Delete style call and maps its error.
func write(w http.ResponseWriter, err error) {
	switch {
	case err == mongo.ErrNoDocuments:
		httpx.Err(w, 404, "stock not found")
	case err != nil:
		log.Printf("stock write: %v", err)
		httpx.Err(w, 500, "could not save stock")
	default:
		w.WriteHeader(204)
	}
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
	if msg := in.Validate(time.Now()); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	write(w, c.svc.Update(r.Context(), id, in))
}

func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	write(w, c.svc.Delete(r.Context(), id))
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return auth(f) }
	mux.Handle("GET /stocks", h(c.List))
	mux.Handle("POST /stocks", h(c.Create))
	mux.Handle("PUT /stocks/{id}", h(c.Update))
	mux.Handle("DELETE /stocks/{id}", h(c.Delete))
}
