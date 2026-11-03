// Package vehicles is the garage: cars and bikes with their papers (insurance, PUC, RC) and photo in S3, and a log of fuel, service,
// repairs, washes and trips that turns into mileage, cost per km, forecasts and renewal reminders.
package vehicles

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

const (
	Collection    = "vehicles"
	LogCollection = "vehicle_logs"
	day           = "2006-01-02"
	maxPaise      = 10_000_000_000 // sanity cap (10 crore rupees), as for transactions
)

var (
	vehicleKinds = map[string]bool{"car": true, "bike": true}
	fuels        = map[string]bool{"petrol": true, "diesel": true, "cng": true, "electric": true}
	// LogKinds: fuel feeds mileage; trip is a place visited, its amount the trip's own costs; the rest are running costs.
	logKinds = map[string]bool{"fuel": true, "service": true, "repair": true, "parts": true, "wash": true, "toll": true, "parking": true, "fine": true, "trip": true, "other": true}
	// docKinds are also the folder names under vehicles/{id}/ in the bucket.
	docKinds = map[string]bool{"insurance": true, "rc": true, "puc": true, "service": true, "receipt": true, "other": true}
	// insuranceTypes: comprehensive covers your own damage and third party; third_party is the legal minimum; own_damage (first party) is a standalone cover.
	insuranceTypes = map[string]bool{"": true, "comprehensive": true, "third_party": true, "own_damage": true}
)

// Dates are stored as YYYY-MM-DD strings (IST), so they sort and group by month as plain text.
type Insurance struct {
	Type         string `bson:"type,omitempty" json:"type,omitempty"` // see insuranceTypes
	Insurer      string `bson:"insurer,omitempty" json:"insurer,omitempty"`
	PolicyNo     string `bson:"policy_no,omitempty" json:"policy_no,omitempty"`
	Expires      string `bson:"expires,omitempty" json:"expires,omitempty"`
	PremiumPaise int64  `bson:"premium_paise,omitempty" json:"premium_paise,omitempty"`
	IDVPaise     int64  `bson:"idv_paise,omitempty" json:"idv_paise,omitempty"` // insured declared value
}

// Doc is a file in the bucket: a policy, the RC, a PUC certificate, a bill.
type Doc struct {
	ID   bson.ObjectID `bson:"_id" json:"id"`
	Kind string        `bson:"kind" json:"kind"`
	Name string        `bson:"name" json:"name"`
	Key  string        `bson:"key" json:"-"`
	Type string        `bson:"type" json:"type"`
	Size int64         `bson:"size" json:"size"`
	At   time.Time     `bson:"at" json:"at"`
}

type Vehicle struct {
	ID            bson.ObjectID `bson:"_id" json:"id"`
	Kind          string        `bson:"kind" json:"kind"`
	Name          string        `bson:"name" json:"name"`
	Make          string        `bson:"make,omitempty" json:"make,omitempty"`
	Model         string        `bson:"model,omitempty" json:"model,omitempty"`
	Year          int           `bson:"year,omitempty" json:"year,omitempty"`
	Plate         string        `bson:"plate" json:"plate"`
	Fuel          string        `bson:"fuel" json:"fuel"`
	TankLitres    float64       `bson:"tank_litres,omitempty" json:"tank_litres,omitempty"`
	ClaimedKmpl   float64       `bson:"claimed_kmpl,omitempty" json:"claimed_kmpl,omitempty"` // the maker's figure, to compare with real mileage
	OdometerKm    int           `bson:"odometer_km,omitempty" json:"odometer_km,omitempty"`   // the reading when it was added
	Insurance     Insurance     `bson:"insurance" json:"insurance"`
	PUCExpires    string        `bson:"puc_expires,omitempty" json:"puc_expires,omitempty"`
	ServiceKm     int           `bson:"service_km,omitempty" json:"service_km,omitempty"` // service every N km
	ServiceMonths int           `bson:"service_months,omitempty" json:"service_months,omitempty"`
	// BoughtOn anchors the maker's service schedule until a service is logged; ServicePlanned is a date the user booked, which wins.
	BoughtOn       string    `bson:"bought_on,omitempty" json:"bought_on,omitempty"`
	ServicePlanned string    `bson:"service_planned,omitempty" json:"service_planned,omitempty"`
	Photo          string    `bson:"photo,omitempty" json:"photo,omitempty"` // bucket key; also busts the browser cache when it changes
	Docs           []Doc     `bson:"docs" json:"docs"`
	Reminded       []string  `bson:"reminded,omitempty" json:"-"` // reminder pushes already sent, see stage
	CreatedAt      time.Time `bson:"created_at" json:"created_at"`
}

type Log struct {
	ID          bson.ObjectID  `bson:"_id" json:"id"`
	VehicleID   bson.ObjectID  `bson:"vehicle_id" json:"vehicle_id"`
	Kind        string         `bson:"kind" json:"kind"`
	Date        string         `bson:"date" json:"date"`
	OdometerKm  int            `bson:"odometer_km,omitempty" json:"odometer_km,omitempty"`
	AmountPaise int64          `bson:"amount_paise" json:"amount_paise"`
	Litres      float64        `bson:"litres,omitempty" json:"litres,omitempty"` // kWh for an electric
	FullTank    bool           `bson:"full_tank,omitempty" json:"full_tank,omitempty"`
	Place       string         `bson:"place,omitempty" json:"place,omitempty"` // pump, garage, or a trip's destination
	From        string         `bson:"from,omitempty" json:"from,omitempty"`   // a trip's start
	TripKm      int            `bson:"trip_km,omitempty" json:"trip_km,omitempty"`
	Note        string         `bson:"note,omitempty" json:"note,omitempty"`
	TxnID       *bson.ObjectID `bson:"txn_id,omitempty" json:"txn_id,omitempty"` // the bank SMS this came from
	CreatedAt   time.Time      `bson:"created_at" json:"created_at"`
}

type VehicleInput struct {
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	Make           string    `json:"make"`
	Model          string    `json:"model"`
	Year           int       `json:"year"`
	Plate          string    `json:"plate"`
	Fuel           string    `json:"fuel"`
	TankLitres     float64   `json:"tank_litres"`
	ClaimedKmpl    float64   `json:"claimed_kmpl"`
	OdometerKm     int       `json:"odometer_km"`
	Insurance      Insurance `json:"insurance"`
	PUCExpires     string    `json:"puc_expires"`
	ServiceKm      int       `json:"service_km"`
	ServiceMonths  int       `json:"service_months"`
	BoughtOn       string    `json:"bought_on"`
	ServicePlanned string    `json:"service_planned"`
}

func validDay(s string) bool {
	_, err := time.Parse(day, s)
	return s == "" || err == nil
}

func tooLong(max int, ss ...string) bool {
	for _, s := range ss {
		if len(s) > max {
			return true
		}
	}
	return false
}

// Parse trims and checks the input; msg is non-empty when it is bad.
func (in VehicleInput) Parse() (v Vehicle, msg string) {
	t := strings.TrimSpace
	in.Name, in.Make, in.Model = t(in.Name), t(in.Make), t(in.Model)
	in.Plate = strings.Join(strings.Fields(strings.ToUpper(in.Plate)), " ")
	in.Insurance.Insurer, in.Insurance.PolicyNo = t(in.Insurance.Insurer), t(in.Insurance.PolicyNo)
	switch {
	case !vehicleKinds[in.Kind]:
		return v, "kind must be car or bike"
	case in.Name == "" || in.Plate == "":
		return v, "name and number plate are required"
	case !fuels[in.Fuel]:
		return v, "fuel must be petrol, diesel, cng or electric"
	case tooLong(60, in.Name, in.Make, in.Model, in.Insurance.Insurer, in.Insurance.PolicyNo) || len(in.Plate) > 16:
		return v, "text is too long"
	case in.Year != 0 && (in.Year < 1950 || in.Year > 2100):
		return v, "year looks wrong"
	case in.TankLitres < 0 || in.TankLitres > 500 || in.ClaimedKmpl < 0 || in.ClaimedKmpl > 200 || in.OdometerKm < 0 || in.OdometerKm > 5_000_000:
		return v, "tank, mileage or odometer looks wrong"
	case in.ServiceKm < 0 || in.ServiceKm > 100_000 || in.ServiceMonths < 0 || in.ServiceMonths > 60:
		return v, "service interval looks wrong"
	case in.Insurance.PremiumPaise < 0 || in.Insurance.PremiumPaise > maxPaise || in.Insurance.IDVPaise < 0 || in.Insurance.IDVPaise > maxPaise:
		return v, "insurance amounts look wrong"
	case !insuranceTypes[in.Insurance.Type]:
		return v, "insurance type must be comprehensive, third_party or own_damage"
	case !validDay(in.Insurance.Expires) || !validDay(in.PUCExpires) || !validDay(in.BoughtOn) || !validDay(in.ServicePlanned):
		return v, "dates must look like 2026-12-31"
	}
	return Vehicle{Kind: in.Kind, Name: in.Name, Make: in.Make, Model: in.Model, Year: in.Year, Plate: in.Plate, Fuel: in.Fuel, TankLitres: in.TankLitres,
		ClaimedKmpl: in.ClaimedKmpl, OdometerKm: in.OdometerKm, Insurance: in.Insurance, PUCExpires: in.PUCExpires, ServiceKm: in.ServiceKm, ServiceMonths: in.ServiceMonths,
		BoughtOn: in.BoughtOn, ServicePlanned: in.ServicePlanned}, ""
}

type LogInput struct {
	Kind        string  `json:"kind"`
	Date        string  `json:"date"`
	OdometerKm  int     `json:"odometer_km"`
	AmountPaise int64   `json:"amount_paise"`
	Litres      float64 `json:"litres"`
	FullTank    bool    `json:"full_tank"`
	Place       string  `json:"place"`
	From        string  `json:"from"`
	TripKm      int     `json:"trip_km"`
	Note        string  `json:"note"`
	TxnID       string  `json:"txn_id"`
}

func (in LogInput) Parse() (l Log, msg string) {
	in.Place, in.From, in.Note = strings.TrimSpace(in.Place), strings.TrimSpace(in.From), strings.TrimSpace(in.Note)
	switch {
	case !logKinds[in.Kind]:
		return l, "unknown kind of entry"
	case in.Date == "" || !validDay(in.Date):
		return l, "date must look like 2026-12-31"
	case in.AmountPaise < 0 || in.AmountPaise > maxPaise:
		return l, "amount looks wrong"
	case in.Kind != "trip" && in.AmountPaise == 0:
		return l, "amount must be above zero"
	case in.Kind == "fuel" && (in.Litres <= 0 || in.Litres > 1000):
		return l, "litres are required for fuel"
	case in.Kind == "trip" && in.Place == "":
		return l, "where did you go?"
	case in.OdometerKm < 0 || in.OdometerKm > 5_000_000 || in.TripKm < 0 || in.TripKm > 100_000:
		return l, "odometer or distance looks wrong"
	case tooLong(80, in.Place, in.From) || len(in.Note) > 500:
		return l, "text is too long"
	}
	l = Log{Kind: in.Kind, Date: in.Date, OdometerKm: in.OdometerKm, AmountPaise: in.AmountPaise, Place: in.Place, From: in.From, TripKm: in.TripKm, Note: in.Note}
	if in.Kind == "fuel" {
		l.Litres, l.FullTank = in.Litres, in.FullTank
	}
	if in.TxnID != "" {
		id, err := bson.ObjectIDFromHex(in.TxnID)
		if err != nil {
			return Log{}, "txn_id is not a valid id"
		}
		l.TxnID = &id
	}
	return l, ""
}

// ---- What the logs add up to ----

// Fill is one measured tank: the km driven between two full tanks and the fuel it took.
type Fill struct {
	Date      string  `json:"date"`
	Km        int     `json:"km"`
	Litres    float64 `json:"litres"`
	Kmpl      float64 `json:"kmpl"`
	PricePerL float64 `json:"price_per_l"` // rupees
}

type Month struct {
	Month      string  `json:"month"` // YYYY-MM
	FuelPaise  int64   `json:"fuel_paise"`
	OtherPaise int64   `json:"other_paise"`
	Litres     float64 `json:"litres"`
}

type KindTotal struct {
	Kind  string `json:"kind"`
	Paise int64  `json:"paise"`
	Count int    `json:"count"`
}

type Reminder struct {
	Kind  string `json:"kind"` // insurance | puc | service
	Title string `json:"title"`
	Date  string `json:"date"`
	Days  int    `json:"days"`
	Tone  string `json:"tone"` // good | warn | bad
}

type NextService struct {
	DueKm   int    `json:"due_km,omitempty"`
	KmLeft  int    `json:"km_left,omitempty"`
	DueDate string `json:"due_date"` // the planned date if one is booked, else the suggested one
	// Suggested is the maker's schedule: the earlier of the month rule and when the km rule will be reached at the usual pace.
	Suggested string `json:"suggested,omitempty"`
	Planned   string `json:"planned,omitempty"`
	Days      int    `json:"days"`
	Last      string `json:"last,omitempty"` // date of the last service
}

type Stats struct {
	OdometerKm int     `json:"odometer_km"`
	AvgKmpl    float64 `json:"avg_kmpl,omitempty"`
	LastKmpl   float64 `json:"last_kmpl,omitempty"`
	BestKmpl   float64 `json:"best_kmpl,omitempty"`
	Fills      []Fill  `json:"fills"`
	PricePerL  float64 `json:"price_per_l,omitempty"` // latest, rupees
	KmPerMonth float64 `json:"km_per_month,omitempty"`
	RangeKm    int     `json:"range_km,omitempty"` // on a full tank at the average mileage

	FuelPerKmPaise int64 `json:"fuel_per_km_paise,omitempty"`
	RunPerKmPaise  int64 `json:"run_per_km_paise,omitempty"` // every running cost over the last 12 months, per km driven in them

	TotalPaise int64       `json:"total_paise"`
	MonthPaise int64       `json:"month_paise"`
	YearPaise  int64       `json:"year_paise"` // last 12 months
	ByKind     []KindTotal `json:"by_kind"`
	Months     []Month     `json:"months"` // the last 12, oldest first

	NextMonthFuelPaise int64 `json:"next_month_fuel_paise,omitempty"`
	NextYearPaise      int64 `json:"next_year_paise,omitempty"` // running costs at the recent pace plus the insurance premium

	NextService *NextService `json:"next_service,omitempty"`
	Reminders   []Reminder   `json:"reminders"`
	Trips       int          `json:"trips"`
	TripKm      int          `json:"trip_km"`
}

func round(f float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(f*p) / p
}

func daysUntil(date string, today time.Time) int {
	d, _ := time.ParseInLocation(day, date, ist.Loc)
	return int(math.Round(d.Sub(today).Hours() / 24))
}

// mileage walks the fuel logs in order: the km between two full tanks, over every litre bought after the first of them. A fill with no
// odometer reading breaks the chain, as the km cannot be known.
func mileage(logs []Log) []Fill {
	var fuel []Log
	for _, l := range logs {
		if l.Kind == "fuel" {
			fuel = append(fuel, l)
		}
	}
	sort.SliceStable(fuel, func(i, j int) bool {
		if fuel[i].Date != fuel[j].Date {
			return fuel[i].Date < fuel[j].Date
		}
		return fuel[i].OdometerKm < fuel[j].OdometerKm
	})
	out := []Fill{}
	anchor, litres := -1, 0.0
	for _, f := range fuel {
		switch {
		case f.OdometerKm == 0:
			anchor = -1
		case !f.FullTank:
			litres += f.Litres
		default:
			if km := f.OdometerKm - anchor; anchor >= 0 && km > 0 {
				l := litres + f.Litres
				out = append(out, Fill{Date: f.Date, Km: km, Litres: round(l, 2), Kmpl: round(float64(km)/l, 2), PricePerL: round(float64(f.AmountPaise)/100/f.Litres, 2)})
			}
			anchor, litres = f.OdometerKm, 0
		}
	}
	return out
}

// Build adds up a vehicle's logs as of now. Pure.
func Build(v Vehicle, logs []Log, now time.Time) Stats {
	today := ist.Midnight(now)
	thisMonth, yearAgo := today.Format("2006-01"), today.AddDate(-1, 0, 0).Format(day)
	s := Stats{OdometerKm: v.OdometerKm, Fills: mileage(logs), ByKind: []KindTotal{}, Reminders: []Reminder{}}

	keys, months := make([]string, 12), map[string]*Month{}
	for i := range keys {
		keys[i] = today.AddDate(0, i-11, 1-today.Day()).Format("2006-01")
		months[keys[i]] = &Month{Month: keys[i]}
	}
	kinds := map[string]*KindTotal{}
	type reading struct {
		date string
		km   int
	}
	reads := []reading{}
	if v.OdometerKm > 0 {
		reads = append(reads, reading{v.CreatedAt.In(ist.Loc).Format(day), v.OdometerKm})
	}
	var lastFuel *Log
	var lastService *Log
	var recentRun, recentFuel int64 // last 6 months: running costs (not trips) and fuel
	sixAgo, ninetyAgo := today.AddDate(0, -6, 0).Format(day), today.AddDate(0, 0, -90).Format(day)
	firstLog := ""
	for i := range logs {
		l := &logs[i]
		if l.OdometerKm > 0 {
			reads = append(reads, reading{l.Date, l.OdometerKm})
			s.OdometerKm = max(s.OdometerKm, l.OdometerKm)
		}
		if firstLog == "" || l.Date < firstLog {
			firstLog = l.Date
		}
		if l.Kind == "trip" {
			s.Trips++
			s.TripKm += l.TripKm
		}
		k := kinds[l.Kind]
		if k == nil {
			k = &KindTotal{Kind: l.Kind}
			kinds[l.Kind] = k
		}
		k.Paise += l.AmountPaise
		k.Count++
		s.TotalPaise += l.AmountPaise
		if strings.HasPrefix(l.Date, thisMonth) {
			s.MonthPaise += l.AmountPaise
		}
		if l.Date > yearAgo {
			s.YearPaise += l.AmountPaise
		}
		if m := months[l.Date[:7]]; m != nil {
			if l.Kind == "fuel" {
				m.FuelPaise += l.AmountPaise
				m.Litres = round(m.Litres+l.Litres, 2)
			} else {
				m.OtherPaise += l.AmountPaise
			}
		}
		if l.Kind != "trip" && l.Date > sixAgo {
			recentRun += l.AmountPaise
		}
		if l.Kind == "fuel" && l.Date > ninetyAgo {
			recentFuel += l.AmountPaise
		}
		if l.Kind == "fuel" && (lastFuel == nil || l.Date >= lastFuel.Date) {
			lastFuel = l
		}
		if l.Kind == "service" && (lastService == nil || l.Date >= lastService.Date) {
			lastService = l
		}
	}
	for _, k := range keys {
		s.Months = append(s.Months, *months[k])
	}
	for _, k := range kinds {
		s.ByKind = append(s.ByKind, *k)
	}
	sort.Slice(s.ByKind, func(i, j int) bool { return s.ByKind[i].Paise > s.ByKind[j].Paise })

	// Mileage
	var km int
	var litres float64
	for _, f := range s.Fills {
		km += f.Km
		litres += f.Litres
		s.BestKmpl = max(s.BestKmpl, f.Kmpl)
	}
	if litres > 0 {
		s.AvgKmpl = round(float64(km)/litres, 2)
		s.LastKmpl = s.Fills[len(s.Fills)-1].Kmpl
	}
	if lastFuel != nil && lastFuel.Litres > 0 {
		s.PricePerL = round(float64(lastFuel.AmountPaise)/100/lastFuel.Litres, 2)
	}
	if s.AvgKmpl > 0 {
		s.RangeKm = int(v.TankLitres * s.AvgKmpl)
		s.FuelPerKmPaise = int64(math.Round(s.PricePerL * 100 / s.AvgKmpl))
	}

	// Pace: km a month from the first and last odometer readings, once they are two weeks apart.
	sort.Slice(reads, func(i, j int) bool {
		return reads[i].date < reads[j].date || reads[i].date == reads[j].date && reads[i].km < reads[j].km
	})
	if n := len(reads); n >= 2 {
		if d := daysUntil(reads[n-1].date, mustDay(reads[0].date)); d >= 14 {
			s.KmPerMonth = round(float64(reads[n-1].km-reads[0].km)/float64(d)*30.44, 0)
		}
		lo, hi := -1, 0
		for _, r := range reads {
			if r.date > yearAgo {
				if lo < 0 {
					lo = r.km
				}
				hi = max(hi, r.km)
			}
		}
		var run int64
		for _, l := range logs {
			if l.Date > yearAgo && l.Kind != "trip" {
				run += l.AmountPaise
			}
		}
		if lo >= 0 && hi > lo {
			s.RunPerKmPaise = run / int64(hi-lo)
		}
	}

	// Forecasts
	switch {
	case s.KmPerMonth > 0 && s.AvgKmpl > 0 && s.PricePerL > 0:
		s.NextMonthFuelPaise = int64(math.Round(s.KmPerMonth / s.AvgKmpl * s.PricePerL * 100))
	case recentFuel > 0:
		s.NextMonthFuelPaise = recentFuel / 3
	}
	if firstLog != "" {
		span := min(6, max(1, int(math.Ceil(float64(-daysUntil(firstLog, today))/30.44))))
		s.NextYearPaise = recentRun / int64(span) * 12
	}
	s.NextYearPaise += v.Insurance.PremiumPaise

	// Service: the maker's schedule counts from the last logged service. Before one is logged it follows the book from the purchase date
	// (bought Feb 2026 on a 12-month cycle: Feb 2027, Feb 2028...) and the km rule falls on the next multiple of the interval.
	if v.ServiceKm > 0 || v.ServiceMonths > 0 || v.ServicePlanned != "" {
		ns := &NextService{}
		var due time.Time
		if lastService != nil {
			ns.Last = lastService.Date
		}
		if v.ServiceMonths > 0 {
			if lastService != nil {
				due = mustDay(lastService.Date).AddDate(0, v.ServiceMonths, 0)
			} else {
				from := ist.Midnight(v.CreatedAt)
				if v.BoughtOn != "" {
					from = mustDay(v.BoughtOn)
				}
				for n := 1; ; n++ { // counted from the start each time, so 31 Jan does not drift to the 28th
					if due = from.AddDate(0, n*v.ServiceMonths, 0); !due.Before(today) {
						break
					}
				}
			}
		}
		switch {
		case v.ServiceKm == 0:
		case lastService != nil && lastService.OdometerKm > 0:
			ns.DueKm = lastService.OdometerKm + v.ServiceKm
		case lastService == nil && s.OdometerKm > 0:
			ns.DueKm = (s.OdometerKm/v.ServiceKm + 1) * v.ServiceKm
		}
		if ns.DueKm > 0 {
			ns.KmLeft = ns.DueKm - s.OdometerKm
			if s.KmPerMonth > 0 {
				byKm := today.AddDate(0, 0, int(float64(ns.KmLeft)/s.KmPerMonth*30.44))
				if due.IsZero() || byKm.Before(due) {
					due = byKm
				}
			}
		}
		if !due.IsZero() {
			ns.Suggested = due.Format(day)
		}
		ns.DueDate = ns.Suggested
		// A booked date wins until a service is logged on or after it.
		if v.ServicePlanned != "" && (lastService == nil || v.ServicePlanned > lastService.Date) {
			ns.Planned, ns.DueDate = v.ServicePlanned, v.ServicePlanned
		}
		if ns.DueDate != "" {
			ns.Days = daysUntil(ns.DueDate, today)
			s.NextService = ns
		}
	}

	// Reminders
	add := func(kind, what, date string) {
		if date == "" {
			return
		}
		r := Reminder{Kind: kind, Date: date, Days: daysUntil(date, today), Tone: "good"}
		switch {
		case r.Days < 0:
			r.Tone, r.Title = "bad", fmt.Sprintf("%s %s %s ago", what, map[bool]string{true: "was due", false: "expired"}[kind == "service"], plural(-r.Days, "day"))
		case r.Days == 0:
			r.Tone, r.Title = "bad", what+" due today"
		default:
			if r.Days <= 15 {
				r.Tone = "bad"
			} else if r.Days <= 45 {
				r.Tone = "warn"
			}
			r.Title = fmt.Sprintf("%s due in %s", what, plural(r.Days, "day"))
		}
		s.Reminders = append(s.Reminders, r)
	}
	add("insurance", "Insurance renewal", v.Insurance.Expires)
	add("puc", "PUC certificate", v.PUCExpires)
	if s.NextService != nil {
		add("service", "Service", s.NextService.DueDate)
	}
	if s.NextService != nil && s.NextService.DueKm > 0 && s.NextService.KmLeft <= 0 {
		s.Reminders[len(s.Reminders)-1].Tone = "bad"
		s.Reminders[len(s.Reminders)-1].Title = fmt.Sprintf("Service overdue by %d km", -s.NextService.KmLeft)
	}
	sort.SliceStable(s.Reminders, func(i, j int) bool { return s.Reminders[i].Days < s.Reminders[j].Days })
	return s
}

func mustDay(s string) time.Time {
	t, _ := time.ParseInLocation(day, s, ist.Loc)
	return t
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// stage is the reminder step a date is at: pushes go out 30, 7 and 1 day before and on the day (0), once each. ok is false when it is further off.
func stage(days int) (int, bool) {
	for _, t := range []int{0, 1, 7, 30} {
		if days <= t {
			return t, true
		}
	}
	return 0, false
}
