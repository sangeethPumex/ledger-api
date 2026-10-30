package categories

import (
	"net/http"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Icon string `json:"icon"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.Err(w, 400, "name required")
		return
	}
	if !ValidIcon(req.Icon) {
		httpx.Err(w, 400, "icon is not valid")
		return
	}
	cat, err := c.svc.Create(r.Context(), req.Name, req.Icon)
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

var sortFields = map[string]string{"name": "name", "created": "_id"}

// Search is the Categories page's list: POST {page, limit, search, sort:{field,dir}}. sort.field is name | created.
func (c *Controller) Search(w http.ResponseWriter, r *http.Request) {
	var req httpx.PageQuery
	if !httpx.Decode(w, r, &req) {
		return
	}
	if msg := req.Check(10, 100); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	sort, msg := req.SortBy(sortFields, bson.D{{Key: "name", Value: 1}, {Key: "_id", Value: 1}})
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	items, total, err := c.svc.Page(r.Context(), strings.TrimSpace(req.Search), sort, req.Skip(), req.Limit)
	if err != nil {
		httpx.Err(w, 500, "could not list categories")
		return
	}
	httpx.WriteJSON(w, 200, req.Reply(items, total))
}

func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name *string `json:"name"`
		Icon *string `json:"icon"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	switch {
	case req.Name == nil && req.Icon == nil:
		httpx.Err(w, 400, "nothing to change")
		return
	case req.Name != nil && strings.TrimSpace(*req.Name) == "":
		httpx.Err(w, 400, "name required")
		return
	case req.Icon != nil && !ValidIcon(*req.Icon):
		httpx.Err(w, 400, "icon is not valid")
		return
	}
	switch err := c.svc.Update(r.Context(), id, req.Name, req.Icon); {
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
