package brainjogger

import (
	"errors"
	"log"
	"net/http"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /jogger", auth(http.HandlerFunc(c.List)))
	mux.Handle("POST /jogger", auth(http.HandlerFunc(c.Create)))
	mux.Handle("PUT /jogger/{id}", auth(http.HandlerFunc(c.Update)))
	mux.Handle("DELETE /jogger/{id}", auth(http.HandlerFunc(c.Delete)))
	mux.Handle("PUT /jogger/{id}/items/{item}", auth(http.HandlerFunc(c.Bought)))
	mux.Handle("GET /jogger/push/key", auth(http.HandlerFunc(c.Key)))
	mux.Handle("POST /jogger/push", auth(http.HandlerFunc(c.Subscribe)))
	mux.Handle("POST /jogger/push/unsubscribe", auth(http.HandlerFunc(c.Unsubscribe)))
	mux.Handle("POST /jogger/push/test", auth(http.HandlerFunc(c.Test)))
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	ls, err := c.svc.List(r.Context())
	if err != nil {
		log.Printf("list jogger: %v", err)
		httpx.Err(w, 500, "could not list your lists")
		return
	}
	httpx.WriteJSON(w, 200, ls)
}

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	l, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	if err := c.svc.Create(r.Context(), l); err != nil {
		httpx.Err(w, 500, "could not save")
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
	l, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	done(w, c.svc.Update(r.Context(), id, l), "update")
}

func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	done(w, c.svc.Delete(r.Context(), id), "delete")
}

// Bought ticks or unticks one item; the reply says whether that finished the list (which is then gone).
func (c *Controller) Bought(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Bought bool `json:"bought"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	deleted, err := c.svc.SetBought(r.Context(), id, r.PathValue("item"), in.Bought)
	if err != nil {
		done(w, err, "update")
		return
	}
	httpx.WriteJSON(w, 200, map[string]bool{"deleted": deleted})
}

func (c *Controller) Key(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, 200, map[string]any{"enabled": c.svc.PushEnabled(), "key": c.svc.PublicKey()})
}

func (c *Controller) Subscribe(w http.ResponseWriter, r *http.Request) {
	var sub Subscription
	if !httpx.Decode(w, r, &sub) {
		return
	}
	if sub.Endpoint == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		httpx.Err(w, 400, "incomplete subscription")
		return
	}
	if err := c.svc.Subscribe(r.Context(), sub); err != nil {
		httpx.Err(w, 500, "could not save subscription")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if err := c.svc.Unsubscribe(r.Context(), in.Endpoint); err != nil {
		httpx.Err(w, 500, "could not remove subscription")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) Test(w http.ResponseWriter, r *http.Request) {
	if !c.svc.PushEnabled() {
		httpx.Err(w, 400, "push is not configured on the server")
		return
	}
	httpx.WriteJSON(w, 200, map[string]int{"sent": c.svc.Test(r.Context())})
}

// done replies 204 for success, 404 for a missing list and 500 otherwise.
func done(w http.ResponseWriter, err error, verb string) {
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "not found")
	default:
		httpx.Err(w, 500, "could not "+verb)
	}
}
