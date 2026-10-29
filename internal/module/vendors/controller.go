package vendors

import (
	"errors"
	"net/http"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) fail(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, ErrInvalid):
		httpx.Err(w, 400, "invalid input")
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "vendor not found")
	case mongo.IsDuplicateKeyError(err):
		httpx.Err(w, 409, "name or alias already used by a vendor")
	default:
		httpx.Err(w, 500, "could not "+what)
	}
}

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string         `json:"name"`
		CategoryID *bson.ObjectID `json:"category_id"`
		Aliases    []string       `json:"aliases"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	v, err := c.svc.Create(r.Context(), req.Name, req.CategoryID, req.Aliases)
	if err != nil {
		c.fail(w, err, "create vendor")
		return
	}
	httpx.WriteJSON(w, 201, v)
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	vs, err := c.svc.List(r.Context())
	if err != nil {
		c.fail(w, err, "list vendors")
		return
	}
	httpx.WriteJSON(w, 200, vs)
}

func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name       *string        `json:"name"`
		CategoryID *bson.ObjectID `json:"category_id"`
		Aliases    *[]string      `json:"aliases"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if err := c.svc.Update(r.Context(), id, req.Name, req.CategoryID, req.Aliases); err != nil {
		c.fail(w, err, "update vendor")
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
		c.fail(w, err, "delete vendor")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) Merge(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Into bson.ObjectID `json:"into"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if err := c.svc.Merge(r.Context(), id, req.Into); err != nil {
		c.fail(w, err, "merge vendors")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) AddAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Alias string `json:"alias"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if err := c.svc.AddAlias(r.Context(), id, req.Alias); err != nil {
		c.fail(w, err, "add alias")
		return
	}
	w.WriteHeader(204)
}
