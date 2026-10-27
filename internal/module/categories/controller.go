package categories

import (
	"net/http"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.Err(w, 400, "name required")
		return
	}
	cat, err := c.svc.Create(r.Context(), req.Name)
	switch {
	case mongo.IsDuplicateKeyError(err):
		httpx.Err(w, 409, "category exists")
	case err != nil:
		httpx.Err(w, 500, "could not create category")
	default:
		httpx.WriteJSON(w, 201, cat)
	}
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	cats, err := c.svc.List(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not list categories")
		return
	}
	httpx.WriteJSON(w, 200, cats)
}

func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.Err(w, 400, "name required")
		return
	}
	switch err := c.svc.Rename(r.Context(), id, req.Name); {
	case err == nil:
		w.WriteHeader(204)
	case err == mongo.ErrNoDocuments:
		httpx.Err(w, 404, "category not found")
	case mongo.IsDuplicateKeyError(err):
		httpx.Err(w, 409, "category exists")
	default:
		httpx.Err(w, 500, "could not update category")
	}
}
