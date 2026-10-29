package transactions

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

// Reparse retries one message (used for failed ones; parsed/ignored are no-ops).
func (c *Controller) Reparse(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	switch err := c.svc.Parse(r.Context(), id); {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "message not found")
	default:
		httpx.Err(w, 502, "parse failed; see fail_reason on the raw message")
	}
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	f, msg := ParseFilter(r.URL.Query())
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	ts, err := c.svc.List(r.Context(), f)
	if err != nil {
		httpx.Err(w, 500, "could not list transactions")
		return
	}
	httpx.WriteJSON(w, 200, ts)
}

var sortFields = map[string]string{"date": "received_at", "amount": "amount_paise", "vendor": "raw_vendor", "type": "type"}

// Search is the table's list: POST {page, limit, search, sort:{field,dir}, filter}, where filter takes the same keys as the GET query string
// (q, category_id, account_id, type, direction, from, to, min, max, owed, needs_review) plus status: review | uncategorised | attention.
// search is the same as filter.q; sort.field is date | amount | vendor | type (default: newest first).
func (c *Controller) Search(w http.ResponseWriter, r *http.Request) {
	var req httpx.PageQuery
	if !httpx.Decode(w, r, &req) {
		return
	}
	if msg := req.Check(50, 200); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	v := url.Values{}
	for k, x := range req.Filter { // one parser and one set of rules for both the GET and POST list
		switch x.(type) {
		case string, float64, bool:
			v.Set(k, fmt.Sprint(x))
		case nil:
		default:
			httpx.Err(w, 400, "filter."+k+" must be a string, number or boolean")
			return
		}
	}
	v.Del("limit")
	if s := strings.TrimSpace(req.Search); s != "" {
		v.Set("q", s)
	}
	f, msg := ParseFilter(v)
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	if f.Sort, msg = req.SortBy(sortFields, nil); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	f.Limit = req.Limit
	items, total, err := c.svc.Page(r.Context(), f, req.Page)
	if err != nil {
		httpx.Err(w, 500, "could not list transactions")
		return
	}
	httpx.WriteJSON(w, 200, req.Reply(items, total))
}

// Patch sets a transaction's category and, for card payments, the card it paid.
func (c *Controller) Patch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req Patch
	if !httpx.Decode(w, r, &req) {
		return
	}
	err := c.svc.Patch(r.Context(), id, req)
	var bad ErrPatch
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.As(err, &bad):
		httpx.Err(w, 400, bad.Error())
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "transaction not found")
	default:
		httpx.Err(w, 500, "could not update transaction")
	}
}

func (c *Controller) Summary(w http.ResponseWriter, r *http.Request) {
	s, err := c.svc.Summary(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not compute summary")
		return
	}
	httpx.WriteJSON(w, 200, s)
}
