// Package views stores saved transaction searches, so they follow the user across devices.
package views

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/httpx"
)

const Collection = "saved_views"

var ErrInvalid = errors.New("invalid view")

// maxViews and the filter keys below bound what a view can hold; keys are the transactions list's query parameters.
const maxViews = 30

var filterKeys = map[string]bool{"q": true, "category_id": true, "account_id": true, "direction": true, "from": true, "to": true, "min": true, "max": true, "owed": true, "type": true}

type View struct {
	ID     bson.ObjectID     `bson:"_id" json:"id"`
	Name   string            `bson:"name" json:"name"`
	Filter map[string]string `bson:"filter" json:"filter"`
}

type Service struct{ col *mongo.Collection }

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true)})
	return &Service{col}, err
}

// Save stores the view under its name, replacing an older view of that name (one upsert, so a double click cannot make two).
func (s *Service) Save(ctx context.Context, name string, filter map[string]string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 || len(filter) == 0 {
		return ErrInvalid
	}
	for k, v := range filter {
		if !filterKeys[k] || len(v) > 100 {
			return ErrInvalid
		}
	}
	if n, err := s.col.CountDocuments(ctx, bson.M{"name": bson.M{"$ne": name}}); err != nil {
		return err
	} else if n >= maxViews {
		return ErrInvalid
	}
	_, err := s.col.UpdateOne(ctx, bson.M{"name": name}, bson.M{"$set": bson.M{"filter": filter}, "$setOnInsert": bson.M{"_id": bson.NewObjectID()}}, options.UpdateOne().SetUpsert(true))
	return err
}

func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && res.DeletedCount == 0 {
		err = mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) List(ctx context.Context) ([]View, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []View{}
	return out, cur.All(ctx, &out)
}

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	vs, err := c.svc.List(r.Context())
	if err != nil {
		log.Printf("views: %v", err)
		httpx.Err(w, 500, "could not list views")
		return
	}
	httpx.WriteJSON(w, 200, vs)
}

func (c *Controller) Save(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string            `json:"name"`
		Filter map[string]string `json:"filter"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	switch err := c.svc.Save(r.Context(), req.Name, req.Filter); {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, ErrInvalid):
		httpx.Err(w, 400, "give the view a name and at least one filter (at most 30 views)")
	default:
		httpx.Err(w, 500, "could not save view")
	}
}

func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	switch err := c.svc.Delete(r.Context(), id); {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "view not found")
	default:
		httpx.Err(w, 500, "could not delete view")
	}
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /views", auth(http.HandlerFunc(c.List)))
	mux.Handle("PUT /views", auth(http.HandlerFunc(c.Save)))
	mux.Handle("DELETE /views/{id}", auth(http.HandlerFunc(c.Delete)))
}
