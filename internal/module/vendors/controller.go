package vendors

import (
	"errors"
	"net/http"
	"strings"

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

var sortFields = map[string]string{"name": "name", "created": "_id"}

// Search is the Vendors page's list: POST {page, limit, search, sort:{field,dir}, filter:{category_id, category}}.
// sort.field is name | created; filter.category_id is a category id and filter.category is none | set.
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
	f := PageFilter{Search: strings.TrimSpace(req.Search)}
	if s, _ := req.Filter["category_id"].(string); s != "" {
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			httpx.Err(w, 400, "category_id is not a valid id")
			return
		}
		f.CategoryID = &id
	}
	if f.Category, _ = req.Filter["category"].(string); f.Category != "" && f.Category != "none" && f.Category != "set" {
		httpx.Err(w, 400, "category must be none or set")
		return
	}
	items, total, err := c.svc.Page(r.Context(), f, sort, req.Skip(), req.Limit)
	if err != nil {
		c.fail(w, err, "list vendors")
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
