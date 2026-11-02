package copilot

import (
	"errors"
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

var errTooMany = errors.New("limit of 100 wants reached; delete some first")

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Overview(w http.ResponseWriter, r *http.Request) {
	o, err := c.svc.Overview(r.Context(), time.Now())
	if err != nil {
		log.Printf("copilot: %v", err)
		httpx.Err(w, 500, "could not compute your numbers")
		return
	}
	httpx.WriteJSON(w, 200, o)
}

func (c *Controller) Check(w http.ResponseWriter, r *http.Request) {
	var in CheckInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	if msg := in.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	st, err := c.svc.State(r.Context(), time.Now())
	if err != nil {
		log.Printf("copilot: check: %v", err)
		httpx.Err(w, 500, "could not check this purchase")
		return
	}
	httpx.WriteJSON(w, 200, st.Check(in))
}

func (c *Controller) SetMode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if !ValidMode(in.Mode) {
		httpx.Err(w, 400, "unknown mode")
		return
	}
	reply(w, c.svc.SetMode(r.Context(), in.Mode), "mode")
}

func (c *Controller) Inbox(w http.ResponseWriter, r *http.Request) {
	in, err := c.svc.Inbox(r.Context(), time.Now())
	if err != nil {
		log.Printf("copilot: inbox: %v", err)
		httpx.Err(w, 500, "could not load nudges")
		return
	}
	httpx.WriteJSON(w, 200, in)
}

func (c *Controller) SetNudge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.Action != "dismiss" && in.Action != "acted" && in.Action != "snooze" {
		httpx.Err(w, 400, "action must be dismiss, acted or snooze")
		return
	}
	reply(w, c.svc.SetNudge(r.Context(), r.PathValue("id"), in.Action, time.Now()), "nudge")
}

func (c *Controller) SetMute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Muted bool `json:"muted"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	kind := r.PathValue("kind")
	if _, ok := kinds[kind]; !ok {
		httpx.Err(w, 400, "unknown kind")
		return
	}
	reply(w, c.svc.SetMute(r.Context(), kind, in.Muted), "setting")
}

func (c *Controller) Wants(w http.ResponseWriter, r *http.Request) {
	out, err := c.svc.Wants(r.Context(), time.Now())
	if err != nil {
		log.Printf("copilot: wants: %v", err)
		httpx.Err(w, 500, "could not load wants")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (c *Controller) CreateWant(w http.ResponseWriter, r *http.Request) {
	var in WantInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	want, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	switch err := c.svc.CreateWant(r.Context(), want, time.Now()); {
	case errors.Is(err, errTooMany):
		httpx.Err(w, 400, err.Error())
	case err != nil:
		httpx.Err(w, 500, "could not save this want")
	default:
		w.WriteHeader(201)
	}
}

func (c *Controller) SetWant(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
		Glad   *bool  `json:"glad"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if (in.Status == "" && in.Glad == nil) || (in.Status != "" && in.Status != "parked" && in.Status != "bought" && in.Status != "dropped") {
		httpx.Err(w, 400, "status must be parked, bought or dropped, or give glad")
		return
	}
	reply(w, c.svc.SetWant(r.Context(), id, in.Status, in.Glad, time.Now()), "want")
}

func (c *Controller) DeleteWant(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	reply(w, c.svc.DeleteWant(r.Context(), id), "want")
}

func reply(w http.ResponseWriter, err error, what string) {
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, what+" not found")
	default:
		log.Printf("copilot: %s: %v", what, err)
		httpx.Err(w, 500, "could not update "+what)
	}
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return auth(f) }
	mux.Handle("GET /copilot", h(c.Overview))
	mux.Handle("PUT /copilot/mode", h(c.SetMode))
	mux.Handle("POST /copilot/check", h(c.Check))
	mux.Handle("GET /copilot/nudges", h(c.Inbox))
	mux.Handle("POST /copilot/nudges/{id}", h(c.SetNudge))
	mux.Handle("PUT /copilot/mutes/{kind}", h(c.SetMute))
	mux.Handle("GET /copilot/wants", h(c.Wants))
	mux.Handle("POST /copilot/wants", h(c.CreateWant))
	mux.Handle("PATCH /copilot/wants/{id}", h(c.SetWant))
	mux.Handle("DELETE /copilot/wants/{id}", h(c.DeleteWant))
}
