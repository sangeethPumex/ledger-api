package httpx

import (
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// PageQuery is the body of every paged list (POST .../search): which page, a text search, a sort and per-list filters.
type PageQuery struct {
	Page   int64  `json:"page"`
	Limit  int64  `json:"limit"`
	Search string `json:"search"`
	Sort   struct {
		Field string `json:"field"`
		Dir   string `json:"dir"` // asc | desc
	} `json:"sort"`
	Filter map[string]any `json:"filter"`
}

// Check fills the defaults (page 1, defLimit rows) and returns a message safe to show when a value is out of range.
func (p *PageQuery) Check(defLimit, maxLimit int64) string {
	if p.Page == 0 {
		p.Page = 1
	}
	if p.Limit == 0 {
		p.Limit = defLimit
	}
	switch {
	case p.Page < 1 || p.Page > 100000 || p.Limit < 1 || p.Limit > maxLimit:
		return "page must be 1 or more and limit 1-" + strconv.FormatInt(maxLimit, 10)
	case len(p.Search) > 100:
		return "search is too long"
	case p.Sort.Dir != "" && p.Sort.Dir != "asc" && p.Sort.Dir != "desc":
		return "sort.dir must be asc or desc"
	}
	return ""
}

func (p PageQuery) Skip() int64 { return (p.Page - 1) * p.Limit }

// SortBy turns the requested sort into a Mongo sort. fields maps the names clients may sort by to stored fields; def is used when none is
// asked for. _id is always the last key so rows with equal values never move between pages.
func (p PageQuery) SortBy(fields map[string]string, def bson.D) (bson.D, string) {
	if p.Sort.Field == "" {
		return def, ""
	}
	stored, ok := fields[p.Sort.Field]
	if !ok {
		return nil, "sort.field is not sortable"
	}
	dir := 1
	if p.Sort.Dir == "desc" {
		dir = -1
	}
	out := bson.D{{Key: stored, Value: dir}}
	if stored != "_id" {
		out = append(out, bson.E{Key: "_id", Value: dir})
	}
	return out, ""
}

// Pages is how many pages total rows make at limit per page.
func Pages(total, limit int64) int64 { return (total + limit - 1) / limit }

// Reply is the body of a paged list of a paged list.
func (p PageQuery) Reply(items any, total int64) map[string]any {
	return map[string]any{"items": items, "page": p.Page, "limit": p.Limit, "total": total, "pages": Pages(total, p.Limit)}
}
