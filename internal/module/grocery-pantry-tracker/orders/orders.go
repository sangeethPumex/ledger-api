// Package orders keeps the user's Swiggy Food and Instamart orders and adds up what they spent each month.
// Orders are stored because Swiggy only looks back a little: Instamart get_orders covers 15 days, Food get_food_orders the last 15 orders.
package orders

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/grocery-pantry-tracker/swiggy"
)

const Collection = "grocery_orders"

// Sources.
const (
	SourceInstamart = "instamart"
	SourceFood      = "food"
)

type Item struct {
	Name string `bson:"name" json:"name"`
	Qty  int    `bson:"qty" json:"qty"`
}

// Bill is the breakdown of what was paid (Instamart sends it; Food's list does not).
type Bill struct {
	ItemPaise      int64 `bson:"item_paise" json:"item_paise"`
	DeliveryPaise  int64 `bson:"delivery_paise" json:"delivery_paise"`
	PackagingPaise int64 `bson:"packaging_paise" json:"packaging_paise"`
}

type Order struct {
	Source     string    `bson:"source" json:"source"`
	OrderID    string    `bson:"order_id" json:"order_id"`
	PlacedAt   time.Time `bson:"placed_at" json:"placed_at"`
	PlacedRaw  string    `bson:"placed_raw" json:"-"` // as Swiggy sent it; the format is undocumented
	TotalPaise int64     `bson:"total_paise" json:"total_paise"`
	Status     string    `bson:"status" json:"status"`
	Store      string    `bson:"store,omitempty" json:"store,omitempty"`
	Summary    string    `bson:"summary,omitempty" json:"summary,omitempty"`
	Items      []Item    `bson:"items,omitempty" json:"items,omitempty"`
	ItemCount  int       `bson:"item_count" json:"item_count"`
	Bill       *Bill     `bson:"bill,omitempty" json:"bill,omitempty"`
	Payment    string    `bson:"payment,omitempty" json:"payment,omitempty"`
	SyncedAt   time.Time `bson:"synced_at" json:"synced_at"`
}

// Cancelled orders are listed but not counted as spend.
var cancelled = regexp.MustCompile(`(?i)cancel`)

func (o Order) Counts() bool { return !cancelled.MatchString(o.Status) }

// caller is the Swiggy MCP client; an interface so tests can stand in for it.
type caller interface {
	Call(ctx context.Context, server, tool string, args map[string]any, out any) error
}

type Service struct {
	col    *mongo.Collection
	swiggy caller
}

func NewService(ctx context.Context, db *mongo.Database, sw caller) (*Service, error) {
	col := db.Collection(Collection)
	// One row per Swiggy order; Sync upserts on it, so a repeat sync never doubles spend.
	_, err := col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "source", Value: 1}, {Key: "order_id", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "placed_at", Value: -1}}},
	})
	return &Service{col: col, swiggy: sw}, err
}

// flex takes a JSON string or number as text: the docs type some numbers as strings (orderTotal) and some ids as numbers.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	if s, err := strconv.Unquote(string(b)); err == nil {
		*f = flex(s)
	} else if string(b) != "null" {
		*f = flex(b)
	}
	return nil
}

// paise reads an amount in rupees ("₹1,234.50", "1234.5", 1234) as paise; ok is false when there is no number in it.
func paise(s flex) (int64, bool) {
	clean := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r == '.' {
			return r
		}
		return -1
	}, string(s))
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, false
	}
	return int64(f*100 + 0.5), true
}

// layouts are tried in order; ones without a zone are read as IST.
var layouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "02-01-2006 15:04:05", "Jan 2, 2006, 3:04 PM", "2 Jan 2006, 3:04 PM", "2006-01-02"}

// yearless are Swiggy Food's "October 10, 8:26 PM": no year is sent.
var yearless = []string{"January 2, 3:04 PM", "Jan 2, 3:04 PM"}

// when reads a Swiggy timestamp: one of the layouts, or Unix seconds / milliseconds.
func when(s string) (time.Time, bool) { return whenAt(s, time.Now()) }

// whenAt is when with the clock given. A date without a year is the latest such date that is not in the future
// (ponytail: an order over a year old would be placed a year late; Swiggy only lists recent orders, so this does not arise).
func whenAt(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range yearless {
		if t, err := time.ParseInLocation(l, s, ist.Loc); err == nil {
			now = now.In(ist.Loc)
			t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, ist.Loc)
			if t.After(now.Add(24 * time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			return t, true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		if n > 1e12 {
			return time.UnixMilli(n), true
		}
		return time.Unix(n, 0), true
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, ist.Loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type imOrder struct {
	OrderID       flex   `json:"orderId"`
	Status        string `json:"status"`
	HistoryStatus string `json:"historyStatus"` // DELIVERED, CANCELLED...; currentStatus is a sentence
	CreatedAt     flex   `json:"createdAt"`
	ItemCount     int    `json:"itemCount"`
	TotalAmount   flex   `json:"totalAmount"`
	StoreName     string `json:"storeName"`
	Items         []struct {
		Name     string `json:"name"`
		Quantity int    `json:"quantity"`
	} `json:"items"`
	PaymentMethod string `json:"paymentMethod"`
	BillDetails   *struct {
		ItemTotal    flex `json:"itemTotal"`
		DeliveryFee  flex `json:"deliveryFee"`
		PackagingFee flex `json:"packagingFee"`
		GrandTotal   flex `json:"grandTotal"`
	} `json:"billDetails"`
}

type foodOrder struct {
	OrderID        flex   `json:"orderId"`
	RestaurantName string `json:"restaurantName"`
	OrderTotal     flex   `json:"orderTotal"`
	OrderStatus    string `json:"orderStatus"`
	OrderedItems   string `json:"orderedItems"`
	OrderedTime    flex   `json:"orderedTime"`
}

func fromInstamart(o imOrder) Order {
	total := o.TotalAmount
	var bill *Bill
	if b := o.BillDetails; b != nil {
		if b.GrandTotal != "" {
			total = b.GrandTotal // the exact amount paid; totalAmount is rounded
		}
		item, _ := paise(b.ItemTotal)
		del, _ := paise(b.DeliveryFee)
		pack, _ := paise(b.PackagingFee)
		bill = &Bill{item, del, pack}
	}
	p, _ := paise(total)
	status := o.HistoryStatus
	if status == "" {
		status = o.Status
	}
	out := Order{Source: SourceInstamart, OrderID: string(o.OrderID), PlacedRaw: string(o.CreatedAt), TotalPaise: p, Status: status, Store: o.StoreName, ItemCount: o.ItemCount, Bill: bill, Payment: o.PaymentMethod}
	names := make([]string, 0, len(o.Items))
	for _, it := range o.Items {
		out.Items = append(out.Items, Item{it.Name, it.Quantity})
		names = append(names, it.Name)
	}
	out.Summary = strings.Join(names, ", ")
	if out.ItemCount == 0 {
		out.ItemCount = len(o.Items)
	}
	return out
}

var (
	foodSplit = regexp.MustCompile(`\)\s*,\s*`) // between items: names may hold commas, so split on ")," not ","
	foodItem  = regexp.MustCompile(`^(.+?)\s*\((\d+)\)?$`)
)

// foodItems reads Swiggy Food's "Kuboos (4),Masala Shawai Chicken (1)" into items; a piece that does not fit the shape is kept whole with quantity 1.
func foodItems(s string) []Item {
	var out []Item
	for _, part := range foodSplit.Split(strings.TrimSpace(s), -1) {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		if m := foodItem.FindStringSubmatch(part); m != nil {
			q, _ := strconv.Atoi(m[2])
			out = append(out, Item{strings.TrimSpace(m[1]), q})
		} else {
			out = append(out, Item{part, 1})
		}
	}
	return out
}

func fromFood(o foodOrder) Order {
	p, _ := paise(o.OrderTotal)
	out := Order{Source: SourceFood, OrderID: string(o.OrderID), PlacedRaw: string(o.OrderedTime), TotalPaise: p, Status: o.OrderStatus, Store: o.RestaurantName, Summary: o.OrderedItems, Items: foodItems(o.OrderedItems)}
	for _, it := range out.Items {
		out.ItemCount += it.Qty
	}
	return out
}

// AddressID is the user's default saved Swiggy address on a server (swiggy.Food or swiggy.Instamart), or the first one.
func (s *Service) AddressID(ctx context.Context, server string) (string, error) {
	var addr struct {
		Addresses []struct {
			ID string `json:"id"`
		} `json:"addresses"`
		Resolution struct {
			DefaultAddressID string `json:"defaultAddressId"`
		} `json:"resolution"`
	}
	if err := s.swiggy.Call(ctx, server, "get_addresses", map[string]any{}, &addr); err != nil {
		return "", err
	}
	id := addr.Resolution.DefaultAddressID
	if id == "" && len(addr.Addresses) > 0 {
		id = addr.Addresses[0].ID
	}
	if id == "" {
		return "", errors.New("no saved Swiggy address; add one in the Swiggy app")
	}
	return id, nil
}

// Since lists the counted (not cancelled) orders of a source placed at or after t, oldest first.
func (s *Service) Since(ctx context.Context, source string, t time.Time) ([]Order, error) {
	cur, err := s.col.Find(ctx, bson.M{"source": source, "placed_at": bson.M{"$gte": t}}, options.Find().SetSort(bson.D{{Key: "placed_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var all, out []Order
	if err := cur.All(ctx, &all); err != nil {
		return nil, err
	}
	for _, o := range all {
		if o.Counts() {
			out = append(out, o)
		}
	}
	return out, nil
}

// Key reduces an item name to lowercase words, so "Amul Taaza Toned Milk 500ml" and "amul taaza toned milk, 500 ml" compare equal enough to group.
func Key(name string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }), " ")
}

// fetch pulls the recent orders from both Swiggy servers, one after the other (Swiggy asks for no parallel sessions).
// A failing source does not stop the other; its error comes back by name.
func (s *Service) fetch(ctx context.Context) ([]Order, map[string]string, error) {
	var out []Order
	errs := map[string]string{}

	var im struct {
		Orders []imOrder `json:"orders"`
	}
	if err := s.swiggy.Call(ctx, swiggy.Instamart, "get_orders", map[string]any{"count": 20}, &im); errors.Is(err, swiggy.ErrNotConnected) {
		return nil, nil, err
	} else if err != nil {
		errs[SourceInstamart] = err.Error()
	}
	for _, o := range im.Orders {
		out = append(out, fromInstamart(o))
	}

	// get_food_orders needs an addressId, only to resolve coordinates: it does not filter, so any saved address will do.
	id, err := s.AddressID(ctx, swiggy.Food)
	if err != nil {
		errs[SourceFood] = err.Error()
		return out, errs, nil
	}
	var food struct {
		Orders []foodOrder `json:"orders"`
	}
	if err := s.swiggy.Call(ctx, swiggy.Food, "get_food_orders", map[string]any{"addressId": id, "orderCount": 15}, &food); err != nil {
		errs[SourceFood] = err.Error()
	}
	for _, o := range food.Orders {
		out = append(out, fromFood(o))
	}
	return out, errs, nil
}

// Sync stores the recent orders, updating ones already kept (status and totals can change while an order is live).
// Errors come back per source; a missing Swiggy link is returned as swiggy.ErrNotConnected.
func (s *Service) Sync(ctx context.Context) (int, map[string]string, error) {
	orders, errs, err := s.fetch(ctx)
	if err != nil {
		return 0, nil, err
	}
	now, n := time.Now(), 0
	for _, o := range orders {
		if o.OrderID == "" {
			continue
		}
		t, ok := when(o.PlacedRaw)
		if !ok {
			log.Printf("grocery: %s order %s has an unreadable time %q; kept without a date", o.Source, o.OrderID, o.PlacedRaw)
		}
		o.PlacedAt, o.SyncedAt = t, now
		if _, err := s.col.UpdateOne(ctx, bson.M{"source": o.Source, "order_id": o.OrderID}, bson.M{"$set": o}, options.UpdateOne().SetUpsert(true)); err != nil {
			return n, errs, err
		}
		n++
	}
	return n, errs, nil
}

// Spend is one source's total for the month, cancelled orders left out.
type Spend struct {
	Paise int64 `json:"paise"`
	Count int   `json:"count"`
}

type Month struct {
	Month      string           `json:"month"` // YYYY-MM, IST
	Orders     []Order          `json:"orders"`
	Spend      map[string]Spend `json:"spend"` // by source
	TotalPaise int64            `json:"total_paise"`
}

// Totals adds up the orders by source, skipping cancelled ones.
func Totals(orders []Order) (map[string]Spend, int64) {
	by := map[string]Spend{SourceInstamart: {}, SourceFood: {}}
	var total int64
	for _, o := range orders {
		if !o.Counts() {
			continue
		}
		sp := by[o.Source]
		sp.Paise += o.TotalPaise
		sp.Count++
		by[o.Source] = sp
		total += o.TotalPaise
	}
	return by, total
}

// Month lists the orders placed in the IST month that starts at start, newest first, with the spend.
func (s *Service) Month(ctx context.Context, start time.Time) (Month, error) {
	start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, ist.Loc)
	cur, err := s.col.Find(ctx, bson.M{"placed_at": bson.M{"$gte": start, "$lt": start.AddDate(0, 1, 0)}}, options.Find().SetSort(bson.D{{Key: "placed_at", Value: -1}}))
	if err != nil {
		return Month{}, err
	}
	out := Month{Month: start.Format("2006-01"), Orders: []Order{}}
	if err := cur.All(ctx, &out.Orders); err != nil {
		return Month{}, err
	}
	out.Spend, out.TotalPaise = Totals(out.Orders)
	return out, nil
}

// Run syncs every interval while Swiggy is connected, so orders are kept before they fall out of Swiggy's 15-day window.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	for {
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if _, errs, err := s.Sync(sctx); err != nil && !errors.Is(err, swiggy.ErrNotConnected) {
			log.Printf("grocery sync: %v", err)
		} else if len(errs) > 0 {
			log.Printf("grocery sync: %v", errs)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
