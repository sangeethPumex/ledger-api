package funds

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/legacy"
)

var codeRe = regexp.MustCompile(`^\d{1,8}$`)

type Service struct {
	col  *mongo.Collection
	set  *mongo.Collection // one-document settings, e.g. the Genius basket SIP
	http *http.Client
	mu   sync.Mutex
	last time.Time // last background NAV refresh
}

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	return &Service{col: col, set: db.Collection("fund_settings"), http: &http.Client{Timeout: 10 * time.Second}}, legacy.StringIDs(ctx, col)
}

// Lookup is a scheme's identity and latest NAV from mfapi.in.
type Lookup struct {
	Code      int64   `json:"code"`
	Name      string  `json:"name"`
	FundHouse string  `json:"fundHouse"`
	Category  string  `json:"category"`
	Nav       float64 `json:"nav"`
	Date      string  `json:"date"` // YYYY-MM-DD
}

var errNotFound = fmt.Errorf("no fund found for that code")

func (s *Service) Lookup(ctx context.Context, code string) (*Lookup, error) {
	if !codeRe.MatchString(code) {
		return nil, fmt.Errorf("scheme code must be digits")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.mfapi.in/mf/"+code+"/latest", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach mfapi.in")
	}
	defer resp.Body.Close()
	var j struct {
		Meta struct {
			FundHouse string `json:"fund_house"`
			Category  string `json:"scheme_category"`
			Name      string `json:"scheme_name"`
		} `json:"meta"`
		Data []struct {
			Date string `json:"date"` // DD-MM-YYYY
			Nav  string `json:"nav"`
		} `json:"data"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil || resp.StatusCode != 200 || j.Status != "SUCCESS" || len(j.Data) == 0 {
		return nil, errNotFound
	}
	nav, err := strconv.ParseFloat(j.Data[0].Nav, 64)
	d, derr := time.Parse("02-01-2006", j.Data[0].Date)
	if err != nil || derr != nil || nav <= 0 {
		return nil, errNotFound
	}
	n, _ := strconv.ParseInt(code, 10, 64)
	return &Lookup{Code: n, Name: j.Meta.Name, FundHouse: j.Meta.FundHouse, Category: j.Meta.Category, Nav: nav, Date: d.Format("2006-01-02")}, nil
}

func (s *Service) get(ctx context.Context, id bson.ObjectID) (*Fund, error) {
	var f Fund
	return &f, s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&f)
}

func (s *Service) all(ctx context.Context) ([]Fund, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var out []Fund
	return out, cur.All(ctx, &out)
}

func (s *Service) List(ctx context.Context, now time.Time) ([]View, error) {
	fs, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(fs))
	for i, f := range fs {
		out[i] = ViewOf(f, now)
	}
	return out, nil
}

// Create looks the scheme up so name and NAV are the registry's, not the client's.
func (s *Service) Create(ctx context.Context, in Input) (*Fund, error) {
	l, err := s.Lookup(ctx, strconv.FormatInt(in.Code, 10))
	if err != nil {
		return nil, err
	}
	f := &Fund{ID: bson.NewObjectID(), Code: l.Code, Name: l.Name, FundHouse: l.FundHouse, Platform: strings.TrimSpace(in.Platform), AccountID: in.AccountID, Type: in.Type, Membership: in.Membership,
		Units: in.Units, AvgNav: in.AvgNav, Invested: in.invested(), Nav: &l.Nav, NavDate: l.Date, Sip: in.Sip, JoinedAt: in.JoinedAt}
	if f.JoinedAt == "" {
		f.JoinedAt = ist.Midnight(time.Now()).Format("2006-01-02")
	}
	_, err = s.col.InsertOne(ctx, f)
	return f, err
}

// Update changes the holding, platform, type, membership and SIP. A different scheme code re-reads the registry,
// so name, fund house and NAV follow the new code (errNotFound when there is no such scheme).
func (s *Service) Update(ctx context.Context, id bson.ObjectID, in Input) error {
	cur, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	set := bson.M{"platform": strings.TrimSpace(in.Platform), "accountId": in.AccountID, "type": in.Type, "membership": in.Membership, "units": in.Units, "avgNav": in.AvgNav, "invested": in.invested()}
	if in.Code != cur.Code {
		l, err := s.Lookup(ctx, strconv.FormatInt(in.Code, 10))
		if err != nil {
			return err
		}
		set["code"], set["name"], set["fundHouse"], set["nav"], set["navDate"] = l.Code, l.Name, l.FundHouse, l.Nav, l.Date
	}
	if in.JoinedAt != "" {
		set["joinedAt"] = in.JoinedAt
	}
	upd := bson.M{"$set": set}
	if in.Membership == "Genius" {
		in.Sip = nil // the basket SIP covers it
	}
	if in.Sip != nil {
		set["sip"] = in.Sip
	} else {
		upd["$unset"] = bson.M{"sip": ""}
	}
	r, err := s.col.UpdateByID(ctx, id, upd)
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	r, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

const geniusKey = "genius"

// GeniusSip returns the basket SIP, nil when none is set.
func (s *Service) GeniusSip(ctx context.Context) (*GeniusSip, error) {
	var d struct {
		Sip *GeniusSip `bson:"sip"`
	}
	err := s.set.FindOne(ctx, bson.M{"_id": geniusKey}).Decode(&d)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	return d.Sip, err
}

// SetGeniusSip stores the basket SIP; nil removes it.
func (s *Service) SetGeniusSip(ctx context.Context, g *GeniusSip) error {
	if g == nil {
		_, err := s.set.UpdateByID(ctx, geniusKey, bson.M{"$unset": bson.M{"sip": ""}})
		return err
	}
	_, err := s.set.UpdateByID(ctx, geniusKey, bson.M{"$set": bson.M{"sip": g}}, options.UpdateOne().SetUpsert(true))
	return err
}

// RecordGeniusSip books one basket instalment across the active Genius funds. ok is false without a live SIP, a fund or a NAV.
func (s *Service) RecordGeniusSip(ctx context.Context) (ok bool, err error) {
	g, err := s.GeniusSip(ctx)
	if err != nil || g == nil {
		return false, err
	}
	all, err := s.all(ctx)
	if err != nil {
		return false, err
	}
	ptrs := make([]*Fund, len(all))
	for i := range all {
		ptrs[i] = &all[i]
	}
	if !AddGeniusSip(ptrs, &g.Sip) {
		return false, nil
	}
	for _, f := range ptrs {
		if f.Membership != "Genius" || f.Exited() {
			continue
		}
		if _, err := s.col.ReplaceOne(ctx, bson.M{"_id": f.ID}, f); err != nil {
			return true, err
		}
	}
	return true, nil
}

// Exit moves a fund out of the portfolio (a Genius rebalance, or a full redemption). ok is false when it already left.
func (s *Service) Exit(ctx context.Context, id bson.ObjectID, req ExitReq, now time.Time) (ok bool, err error) {
	f, err := s.get(ctx, id)
	if err != nil {
		return false, err
	}
	if f.Exited() {
		return false, nil
	}
	date, proceeds := req.Date, Value(f)
	if date == "" {
		date = ist.Midnight(now).Format("2006-01-02")
	}
	if req.Proceeds != nil {
		proceeds = *req.Proceeds
	}
	Exit(f, date, proceeds)
	_, err = s.col.ReplaceOne(ctx, bson.M{"_id": id}, f)
	return true, err
}

// Reenter puts an exited fund back. ok is false when it was never out.
func (s *Service) Reenter(ctx context.Context, id bson.ObjectID, now time.Time) (ok bool, err error) {
	f, err := s.get(ctx, id)
	if err != nil {
		return false, err
	}
	if !f.Exited() {
		return false, nil
	}
	Reenter(f)
	f.JoinedAt = ist.Midnight(now).Format("2006-01-02")
	_, err = s.col.ReplaceOne(ctx, bson.M{"_id": id}, f)
	return true, err
}

// RecordSip books one instalment; ok is false when the fund has no SIP or no NAV yet.
func (s *Service) RecordSip(ctx context.Context, id bson.ObjectID) (ok bool, err error) {
	f, err := s.get(ctx, id)
	if err != nil {
		return false, err
	}
	if !AddSip(f) {
		return false, nil
	}
	_, err = s.col.ReplaceOne(ctx, bson.M{"_id": id}, f)
	return true, err
}

// Refresh fetches the latest NAV for every fund; a fund whose lookup fails keeps its old NAV.
func (s *Service) Refresh(ctx context.Context) error {
	fs, err := s.all(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, f := range fs {
		if f.Exited() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := s.Lookup(ctx, strconv.FormatInt(f.Code, 10))
			if err != nil {
				log.Printf("nav refresh %d: %v", f.Code, err)
				return
			}
			if _, err := s.col.UpdateByID(ctx, f.ID, bson.M{"$set": bson.M{"nav": l.Nav, "navDate": l.Date}}); err != nil {
				log.Printf("nav save %d: %v", f.Code, err)
			}
		}()
	}
	wg.Wait()
	return nil
}

// RefreshIfStale starts a background refresh unless one ran in the last 30 minutes. Never blocks the caller.
func (s *Service) RefreshIfStale() {
	s.mu.Lock()
	if time.Since(s.last) < 30*time.Minute {
		s.mu.Unlock()
		return
	}
	s.last = time.Now()
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.Refresh(ctx); err != nil {
			log.Printf("nav refresh: %v", err)
		}
	}()
}

type Slice struct {
	Name  string  `json:"name" bson:"_id"`
	Value float64 `json:"value" bson:"value"`
}

type SipBrief struct {
	Name   string  `json:"name" bson:"name"`
	Amount float64 `json:"amount"`
	Day    int     `json:"day"`
}

type NextSip struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
	Days   int     `json:"days"`
}

type Summary struct {
	Invested   float64    `json:"invested"`
	Value      float64    `json:"value"`
	Pnl        float64    `json:"pnl"`
	Count      int        `json:"count"`
	SipMonthly float64    `json:"sipMonthly"`
	AsOf       string     `json:"asOf,omitempty"`
	ByType     []Slice    `json:"byType"`
	ByPlatform []Slice    `json:"byPlatform"`
	Sips       []SipBrief `json:"sips"`
	NextSip    *NextSip   `json:"nextSip,omitempty"`
	Realised   float64    `json:"realised"` // gain or loss booked on funds that were sold out
	Exited     int        `json:"exited"`
	Genius     Genius     `json:"genius"`
}

// Summary totals and splits every fund in one aggregation.
func (s *Service) Summary(ctx context.Context, now time.Time) (*Summary, error) {
	hasNav := bson.M{"$gt": bson.A{bson.M{"$ifNull": bson.A{"$nav", 0}}, 0}}
	live := bson.M{"$match": bson.M{"status": bson.M{"$ne": "exited"}}} // exited funds stay out of every current figure
	slice := func(key string) bson.A {
		return bson.A{
			live,
			bson.M{"$group": bson.M{"_id": key, "value": bson.M{"$sum": "$_value"}}},
			bson.M{"$sort": bson.M{"value": -1}},
		}
	}
	pipe := mongo.Pipeline{
		{{Key: "$addFields", Value: bson.M{"_value": bson.M{"$cond": bson.A{hasNav, bson.M{"$multiply": bson.A{"$units", "$nav"}}, "$invested"}}}}},
		{{Key: "$facet", Value: bson.M{
			"totals": bson.A{live, bson.M{"$group": bson.M{
				"_id": nil, "invested": bson.M{"$sum": "$invested"}, "value": bson.M{"$sum": "$_value"}, "count": bson.M{"$sum": 1},
				"sip": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$or": bson.A{bson.M{"$eq": bson.A{"$membership", "Genius"}}, bson.M{"$in": bson.A{bson.M{"$ifNull": bson.A{"$sip.status", "active"}}, bson.A{"paused", "cancelled"}}}}}, 0, bson.M{"$ifNull": bson.A{"$sip.amount", 0}}}}}, "asOf": bson.M{"$max": "$navDate"},
			}}},
			"byType":     slice("$type"),
			"byPlatform": slice("$platform"),
			"sips": bson.A{bson.M{"$match": bson.M{"status": bson.M{"$ne": "exited"}, "membership": bson.M{"$ne": "Genius"}, "sip": bson.M{"$exists": true}, "sip.status": bson.M{"$nin": bson.A{"paused", "cancelled"}}}},
				bson.M{"$project": bson.M{"_id": 0, "name": 1, "amount": "$sip.amount", "day": "$sip.day"}}},
		}}},
	}
	cur, err := s.col.Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	var res []struct {
		Totals []struct {
			Invested float64 `bson:"invested"`
			Value    float64 `bson:"value"`
			Count    int     `bson:"count"`
			Sip      float64 `bson:"sip"`
			AsOf     *string `bson:"asOf"` // null when no fund has a NAV date yet
		} `bson:"totals"`
		ByType     []Slice    `bson:"byType"`
		ByPlatform []Slice    `bson:"byPlatform"`
		Sips       []SipBrief `bson:"sips"`
	}
	if err := cur.All(ctx, &res); err != nil {
		return nil, err
	}
	out := &Summary{ByType: []Slice{}, ByPlatform: []Slice{}, Sips: []SipBrief{}}
	all, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	out.Genius = GeniusOf(all, now)
	for i := range all {
		if all[i].Exited() {
			out.Exited++
			out.Realised += Value(&all[i]) - all[i].Invested
		}
	}
	if len(res) == 0 {
		return out, nil
	}
	if t := res[0].Totals; len(t) > 0 {
		out.Invested, out.Value, out.Count, out.SipMonthly = t[0].Invested, t[0].Value, t[0].Count, t[0].Sip
		if t[0].AsOf != nil {
			out.AsOf = *t[0].AsOf
		}
		out.Pnl = out.Value - out.Invested
	}
	out.ByType, out.ByPlatform, out.Sips = append(out.ByType, res[0].ByType...), append(out.ByPlatform, res[0].ByPlatform...), append(out.Sips, res[0].Sips...)
	// the Genius basket has one SIP of its own, counted once whatever the number of funds in it
	if gs, err := s.GeniusSip(ctx); err != nil {
		return nil, err
	} else if gs != nil {
		out.Genius.Sip = gs
		if gs.Live() {
			d := ist.DaysUntil(gs.Day, now)
			out.Genius.SipInDays = &d
			out.SipMonthly += gs.Amount
			out.Sips = append(out.Sips, SipBrief{Name: "Genius basket", Amount: gs.Amount, Day: gs.Day})
		}
	}
	sort.Slice(out.Sips, func(i, j int) bool { return ist.DaysUntil(out.Sips[i].Day, now) < ist.DaysUntil(out.Sips[j].Day, now) })
	if len(out.Sips) > 0 {
		a := out.Sips[0]
		out.NextSip = &NextSip{Name: a.Name, Amount: a.Amount, Days: ist.DaysUntil(a.Day, now)}
	}
	return out, nil
}
