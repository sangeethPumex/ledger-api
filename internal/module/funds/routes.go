package funds

import (
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func notFound(w http.ResponseWriter, err error) {
	if err == mongo.ErrNoDocuments {
		httpx.Err(w, 404, "fund not found")
		return
	}
	httpx.Err(w, 500, "could not update fund")
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
	log.Printf("list funds: %v", err)
	httpx.Err(w, 500, "could not list funds")
}

func (c *Controller) Lookup(w http.ResponseWriter, r *http.Request) {
	l, err := c.svc.Lookup(r.Context(), r.PathValue("code"))
	switch {
	case err == errNotFound:
		httpx.Err(w, 404, err.Error())
	case err != nil:
		httpx.Err(w, 400, err.Error())
	default:
		httpx.WriteJSON(w, 200, l)
	}
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
	f, err := c.svc.Create(r.Context(), in)
	switch {
	case err == errNotFound:
		httpx.Err(w, 404, err.Error())
	case err != nil:
		httpx.Err(w, 502, err.Error())
	default:
		httpx.WriteJSON(w, 201, ViewOf(*f, time.Now()))
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
	if msg := in.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	if err := c.svc.Update(r.Context(), id, in); err != nil {
		if err == errNotFound {
			httpx.Err(w, 404, err.Error())
			return
		}
		if err != mongo.ErrNoDocuments {
			log.Printf("update fund: %v", err)
			httpx.Err(w, 502, "could not update fund: "+err.Error())
			return
		}
		notFound(w, err)
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
		notFound(w, err)
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) RecordSip(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	did, err := c.svc.RecordSip(r.Context(), id)
	switch {
	case err != nil:
		notFound(w, err)
	case !did:
		httpx.Err(w, 400, "fund needs a SIP and a fetched NAV")
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) Exit(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req ExitReq
	if !httpx.Decode(w, r, &req) {
		return
	}
	if msg := req.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	did, err := c.svc.Exit(r.Context(), id, req, time.Now())
	switch {
	case err != nil:
		notFound(w, err)
	case !did:
		httpx.Err(w, 400, "fund already moved out")
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) Reenter(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	did, err := c.svc.Reenter(r.Context(), id, time.Now())
	switch {
	case err != nil:
		notFound(w, err)
	case !did:
		httpx.Err(w, 400, "fund is not moved out")
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) SetGeniusSip(w http.ResponseWriter, r *http.Request) {
	var g GeniusSip
	if !httpx.Decode(w, r, &g) {
		return
	}
	if msg := g.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	if err := c.svc.SetGeniusSip(r.Context(), &g); err != nil {
		log.Printf("set genius sip: %v", err)
		httpx.Err(w, 500, "could not save the Genius SIP")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) ClearGeniusSip(w http.ResponseWriter, r *http.Request) {
	if err := c.svc.SetGeniusSip(r.Context(), nil); err != nil {
		httpx.Err(w, 500, "could not remove the Genius SIP")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) RecordGeniusSip(w http.ResponseWriter, r *http.Request) {
	did, err := c.svc.RecordGeniusSip(r.Context())
	switch {
	case err != nil:
		log.Printf("record genius sip: %v", err)
		httpx.Err(w, 500, "could not record the Genius SIP")
	case !did:
		httpx.Err(w, 400, "needs an active Genius SIP, a Genius fund, and a fetched NAV for each")
	default:
		w.WriteHeader(204)
	}
}

func (c *Controller) Refresh(w http.ResponseWriter, r *http.Request) {
	if err := c.svc.Refresh(r.Context()); err != nil {
		httpx.Err(w, 500, "could not refresh NAVs")
		return
	}
	w.WriteHeader(204)
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return auth(f) }
	mux.Handle("GET /funds", h(c.List))
	mux.Handle("GET /funds/lookup/{code}", h(c.Lookup))
	mux.Handle("POST /funds", h(c.Create))
	mux.Handle("POST /funds/refresh", h(c.Refresh))
	mux.Handle("PUT /funds/{id}", h(c.Update))
	mux.Handle("DELETE /funds/{id}", h(c.Delete))
	mux.Handle("PUT /funds/genius/sip", h(c.SetGeniusSip))
	mux.Handle("DELETE /funds/genius/sip", h(c.ClearGeniusSip))
	mux.Handle("POST /funds/genius/sip", h(c.RecordGeniusSip))
	mux.Handle("POST /funds/{id}/sip", h(c.RecordSip))
	mux.Handle("POST /funds/{id}/exit", h(c.Exit))
	mux.Handle("POST /funds/{id}/reenter", h(c.Reenter))
}
