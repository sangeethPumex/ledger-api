package vehicles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/brainjogger"
)

var ErrNoStore = errors.New("file storage is not set up")

type Service struct {
	col, logs, txs, cats *mongo.Collection
	files                *Store // nil when no bucket is configured
	push                 *brainjogger.Service
}

func NewService(ctx context.Context, db *mongo.Database, files *Store, push *brainjogger.Service) (*Service, error) {
	s := &Service{col: db.Collection(Collection), logs: db.Collection(LogCollection), txs: db.Collection("transactions"), cats: db.Collection("categories"), files: files, push: push}
	_, err := s.logs.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "vehicle_id", Value: 1}, {Key: "date", Value: -1}}})
	return s, err
}

// Overview is a vehicle with what its logs add up to; Detail adds the logs, newest first.
type Overview struct {
	Vehicle
	Stats Stats `json:"stats"`
}

type Detail struct {
	Overview
	Logs []Log `json:"logs"`
}

func (s *Service) findLogs(ctx context.Context, q bson.M) ([]Log, error) {
	cur, err := s.logs.Find(ctx, q, options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "odometer_km", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, err
	}
	out := []Log{}
	return out, cur.All(ctx, &out)
}

func (s *Service) List(ctx context.Context, now time.Time) ([]Overview, error) {
	cur, err := s.col.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var vs []Vehicle
	if err := cur.All(ctx, &vs); err != nil {
		return nil, err
	}
	logs, err := s.findLogs(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	by := map[bson.ObjectID][]Log{}
	for _, l := range logs {
		by[l.VehicleID] = append(by[l.VehicleID], l)
	}
	out := make([]Overview, len(vs))
	for i, v := range vs {
		out[i] = Overview{v, Build(v, by[v.ID], now)}
	}
	return out, nil
}

func (s *Service) one(ctx context.Context, id bson.ObjectID) (Vehicle, error) {
	var v Vehicle
	return v, s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&v)
}

func (s *Service) Get(ctx context.Context, id bson.ObjectID, now time.Time) (Detail, error) {
	v, err := s.one(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	logs, err := s.findLogs(ctx, bson.M{"vehicle_id": id})
	if err != nil {
		return Detail{}, err
	}
	return Detail{Overview{v, Build(v, logs, now)}, logs}, nil
}

func (s *Service) Create(ctx context.Context, v Vehicle) (Vehicle, error) {
	v.ID, v.CreatedAt, v.Docs = bson.NewObjectID(), time.Now().UTC(), []Doc{}
	_, err := s.col.InsertOne(ctx, v)
	return v, err
}

// Update replaces what the form edits; photo, files and sent reminders stay.
func (s *Service) Update(ctx context.Context, id bson.ObjectID, v Vehicle) error {
	r, err := s.col.UpdateByID(ctx, id, bson.M{"$set": bson.M{"kind": v.Kind, "name": v.Name, "make": v.Make, "model": v.Model, "year": v.Year, "plate": v.Plate,
		"fuel": v.Fuel, "tank_litres": v.TankLitres, "claimed_kmpl": v.ClaimedKmpl, "odometer_km": v.OdometerKm, "insurance": v.Insurance,
		"puc_expires": v.PUCExpires, "service_km": v.ServiceKm, "service_months": v.ServiceMonths, "bought_on": v.BoughtOn, "service_planned": v.ServicePlanned}})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

// Delete removes the vehicle, its logs and its files in the bucket.
func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	v, err := s.one(ctx, id)
	if err != nil {
		return err
	}
	if s.files != nil {
		keys := []string{}
		if v.Photo != "" {
			keys = append(keys, v.Photo)
		}
		for _, d := range v.Docs {
			keys = append(keys, d.Key)
		}
		if err := s.files.Delete(ctx, keys...); err != nil {
			return err
		}
	}
	if _, err := s.logs.DeleteMany(ctx, bson.M{"vehicle_id": id}); err != nil {
		return err
	}
	_, err = s.col.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *Service) AddLog(ctx context.Context, vid bson.ObjectID, l Log) error {
	if _, err := s.one(ctx, vid); err != nil {
		return err
	}
	l.ID, l.VehicleID, l.CreatedAt = bson.NewObjectID(), vid, time.Now().UTC()
	_, err := s.logs.InsertOne(ctx, l)
	return err
}

func (s *Service) UpdateLog(ctx context.Context, vid, id bson.ObjectID, l Log) error {
	l.ID, l.VehicleID = id, vid
	var old Log
	if err := s.logs.FindOne(ctx, bson.M{"_id": id, "vehicle_id": vid}).Decode(&old); err != nil {
		return err
	}
	l.CreatedAt = old.CreatedAt
	_, err := s.logs.ReplaceOne(ctx, bson.M{"_id": id, "vehicle_id": vid}, l)
	return err
}

func (s *Service) DeleteLog(ctx context.Context, vid, id bson.ObjectID) error {
	r, err := s.logs.DeleteOne(ctx, bson.M{"_id": id, "vehicle_id": vid})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

// ---- Bank SMS that look like vehicle spending ----

var (
	fuelRe = regexp.MustCompile(`(?i)petrol|diesel|fuel|filling|hpcl|bpcl|iocl|indian ?oil|bharat ?petro|hindustan ?petro|nayara|shell|jio-?bp|reliance ?bp|\bcng\b|ev ?charg`)
	tollRe = regexp.MustCompile(`(?i)fastag|toll|nhai`)
	carRe  = regexp.MustCompile(`(?i)vehicle|motor|garage|service centre|car ?wash|tyre|automobile`)
)

type Suggestion struct {
	TxnID       string `json:"txn_id"`
	Date        string `json:"date"`
	Vendor      string `json:"vendor"`
	AmountPaise int64  `json:"amount_paise"`
	Kind        string `json:"kind"` // the log kind it most likely is
}

// Suggestions lists debits of the last 60 days whose payee or category looks like fuel, tolls or car upkeep and that no log has claimed yet.
func (s *Service) Suggestions(ctx context.Context, now time.Time) ([]Suggestion, error) {
	cur, err := s.cats.Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{"name": 1}))
	if err != nil {
		return nil, err
	}
	var cats []struct {
		ID   bson.ObjectID `bson:"_id"`
		Name string        `bson:"name"`
	}
	if err := cur.All(ctx, &cats); err != nil {
		return nil, err
	}
	catName := map[bson.ObjectID]string{}
	carCats := bson.A{}
	for _, c := range cats {
		if fuelRe.MatchString(c.Name) || tollRe.MatchString(c.Name) || carRe.MatchString(c.Name) {
			catName[c.ID] = c.Name
			carCats = append(carCats, c.ID)
		}
	}
	pat := fuelRe.String() + "|" + tollRe.String()[4:] + "|" + carRe.String()[4:] // one (?i) prefix for the joined pattern
	cur, err = s.txs.Find(ctx, bson.M{"type": "expense", "direction": bson.M{"$ne": "credit"}, "occurred_at": bson.M{"$gte": now.AddDate(0, 0, -60).UTC()},
		"$or": bson.A{bson.M{"category_id": bson.M{"$in": carCats}}, bson.M{"raw_vendor": bson.M{"$regex": pat}}, bson.M{"upi_id": bson.M{"$regex": pat}}}},
		options.Find().SetSort(bson.D{{Key: "occurred_at", Value: -1}}).SetLimit(100).SetProjection(bson.M{"raw_vendor": 1, "upi_id": 1, "amount_paise": 1, "occurred_at": 1, "category_id": 1}))
	if err != nil {
		return nil, err
	}
	var txs []struct {
		ID         bson.ObjectID  `bson:"_id"`
		RawVendor  string         `bson:"raw_vendor"`
		UPIID      string         `bson:"upi_id"`
		Paise      int64          `bson:"amount_paise"`
		At         time.Time      `bson:"occurred_at"`
		CategoryID *bson.ObjectID `bson:"category_id"`
	}
	if err := cur.All(ctx, &txs); err != nil {
		return nil, err
	}
	var used []bson.ObjectID
	if err := s.logs.Distinct(ctx, "txn_id", bson.M{"txn_id": bson.M{"$exists": true}}).Decode(&used); err != nil {
		return nil, err
	}
	claimed := map[bson.ObjectID]bool{}
	for _, id := range used {
		claimed[id] = true
	}
	out := []Suggestion{}
	for _, t := range txs {
		if claimed[t.ID] {
			continue
		}
		label := t.RawVendor
		if label == "" {
			label = t.UPIID
		}
		what := label + " " + t.UPIID
		if t.CategoryID != nil {
			what += " " + catName[*t.CategoryID]
		}
		kind := "other"
		switch {
		case tollRe.MatchString(what):
			kind = "toll"
		case fuelRe.MatchString(what):
			kind = "fuel"
		case strings.Contains(strings.ToLower(what), "wash"):
			kind = "wash"
		case carRe.MatchString(what):
			kind = "service"
		}
		out = append(out, Suggestion{t.ID.Hex(), t.At.In(ist.Loc).Format(day), label, t.Paise, kind})
	}
	return out, nil
}

// ---- Files ----

// folder is where a file goes in the bucket: vehicles/{id}/{kind}/...
func fileKey(id bson.ObjectID, kind, name string, now time.Time) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	if len(clean) > 80 {
		clean = clean[len(clean)-80:]
	}
	return fmt.Sprintf("vehicles/%s/%s/%d-%s", id.Hex(), kind, now.UnixMilli(), clean)
}

// SaveFile stores a file: kind "photo" replaces the vehicle's picture, any doc kind adds to its papers.
func (s *Service) SaveFile(ctx context.Context, id bson.ObjectID, kind, name, ctype string, data []byte) (Doc, error) {
	if s.files == nil {
		return Doc{}, ErrNoStore
	}
	v, err := s.one(ctx, id)
	if err != nil {
		return Doc{}, err
	}
	d := Doc{ID: bson.NewObjectID(), Kind: kind, Name: name, Key: fileKey(id, kind, name, time.Now()), Type: ctype, Size: int64(len(data)), At: time.Now().UTC()}
	if err := s.files.Put(ctx, d.Key, ctype, data); err != nil {
		return Doc{}, err
	}
	if kind == "photo" {
		if _, err := s.col.UpdateByID(ctx, id, bson.M{"$set": bson.M{"photo": d.Key}}); err != nil {
			return Doc{}, err
		}
		if v.Photo != "" {
			if err := s.files.Delete(ctx, v.Photo); err != nil {
				log.Printf("vehicles: old photo %s left in the bucket: %v", v.Photo, err)
			}
		}
		return d, nil
	}
	_, err = s.col.UpdateByID(ctx, id, bson.M{"$push": bson.M{"docs": d}})
	return d, err
}

// file finds a stored file: "photo" or a doc id.
func (v Vehicle) file(ref string) (key, ctype string, ok bool) {
	if ref == "photo" {
		return v.Photo, "", v.Photo != ""
	}
	i := slices.IndexFunc(v.Docs, func(d Doc) bool { return d.ID.Hex() == ref })
	if i < 0 {
		return "", "", false
	}
	return v.Docs[i].Key, v.Docs[i].Type, true
}

// File opens a stored file; the caller closes it. ctype is empty for the photo (the browser sniffs images).
func (s *Service) File(ctx context.Context, id bson.ObjectID, ref string) (io.ReadCloser, string, error) {
	if s.files == nil {
		return nil, "", ErrNoStore
	}
	v, err := s.one(ctx, id)
	if err != nil {
		return nil, "", err
	}
	key, ctype, ok := v.file(ref)
	if !ok {
		return nil, "", mongo.ErrNoDocuments
	}
	body, err := s.files.Get(ctx, key)
	return body, ctype, err
}

func (s *Service) DeleteFile(ctx context.Context, id bson.ObjectID, ref string) error {
	if s.files == nil {
		return ErrNoStore
	}
	v, err := s.one(ctx, id)
	if err != nil {
		return err
	}
	key, _, ok := v.file(ref)
	if !ok {
		return mongo.ErrNoDocuments
	}
	if err := s.files.Delete(ctx, key); err != nil {
		return err
	}
	upd := bson.M{"$unset": bson.M{"photo": ""}}
	if ref != "photo" {
		upd = bson.M{"$pull": bson.M{"docs": bson.M{"key": key}}}
	}
	_, err = s.col.UpdateByID(ctx, id, upd)
	return err
}

// ---- Reminder pushes ----

// Remind pushes each insurance, PUC and service date 30, 7 and 1 day before and on the day, once each, between 9 am and 9 pm.
func (s *Service) Remind(ctx context.Context, now time.Time) error {
	if h := now.In(ist.Loc).Hour(); h < 9 || h >= 21 {
		return nil
	}
	all, err := s.List(ctx, now)
	if err != nil {
		return err
	}
	for _, o := range all {
		for _, r := range o.Stats.Reminders {
			t, ok := stage(r.Days)
			if !ok {
				continue
			}
			// A service date predicted from km drifts each day, so its key is the last service instead; a booked date is fixed.
			anchor := r.Date
			if ns := o.Stats.NextService; r.Kind == "service" && ns.Planned == "" {
				anchor = "last:" + ns.Last
			}
			key := fmt.Sprintf("%s|%s|%d", r.Kind, anchor, t)
			if slices.Contains(o.Reminded, key) {
				continue
			}
			// Mark first: a crash after this never sends the same push twice.
			if _, err := s.col.UpdateByID(ctx, o.ID, bson.M{"$addToSet": bson.M{"reminded": key}}); err != nil {
				return err
			}
			s.push.Push(ctx, o.Name+": "+r.Title, o.Plate, "vehicle-"+r.Kind+"-"+o.ID.Hex(), "/vehicles/"+o.ID.Hex())
		}
	}
	return nil
}

func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := s.Remind(rctx, time.Now()); err != nil {
			log.Printf("vehicles: reminders: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
