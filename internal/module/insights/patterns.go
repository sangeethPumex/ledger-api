package insights

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/spend"
)

const patternWeeks = 26

type Bucket struct {
	Paise int64 `bson:"paise" json:"paise"`
	Count int   `bson:"count" json:"count"`
}

// Patterns is when the user spends: per weekday (Sunday first), per hour of the day (IST), and plain-language notes about both.
type Patterns struct {
	Weekdays [7]Bucket  `json:"weekdays"`
	Hours    [24]Bucket `json:"hours"`
	Notes    []string   `json:"notes"`
}

type bucketDoc struct {
	ID     int `bson:"_id"`
	Bucket `bson:",inline"`
}

// patterns buckets the last 26 weeks of spending by IST weekday and hour in one $facet, so the collection is scanned once.
func (s *Service) patterns(ctx context.Context, now time.Time) (*Patterns, error) {
	end := ist.Midnight(now).AddDate(0, 0, 1)
	start := end.AddDate(0, 0, -patternWeeks*7)
	by := func(op string) bson.A {
		return bson.A{bson.M{"$group": bson.M{
			"_id":   bson.M{op: bson.M{"date": "$occurred_at", "timezone": "Asia/Kolkata"}},
			"paise": bson.M{"$sum": "$amount_paise"}, "count": bson.M{"$sum": 1},
		}}}
	}
	pipe := append(mongo.Pipeline{{{Key: "$match", Value: spend.Match(start, end)}}}, spend.Net()...)
	pipe = append(pipe, bson.D{{Key: "$facet", Value: bson.M{"dow": by("$dayOfWeek"), "hour": by("$hour")}}})
	cur, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	var res []struct {
		Dow  []bucketDoc `bson:"dow"`
		Hour []bucketDoc `bson:"hour"`
	}
	if err := cur.All(ctx, &res); err != nil {
		return nil, err
	}
	p := &Patterns{Notes: []string{}}
	if len(res) == 0 {
		return p, nil
	}
	for _, d := range res[0].Dow {
		if d.ID >= 1 && d.ID <= 7 { // Mongo: 1 = Sunday
			p.Weekdays[d.ID-1] = d.Bucket
		}
	}
	for _, h := range res[0].Hour {
		if h.ID >= 0 && h.ID < 24 {
			p.Hours[h.ID] = h.Bucket
		}
	}
	var occ [7]int // how many of each weekday fall in the window, so averages are per day, not per transaction
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		occ[d.Weekday()]++
	}
	p.Notes = commentary(p, occ, patternWeeks*7)
	return p, nil
}

// inr writes paise as ₹ with Indian digit grouping and no decimals (₹1,23,456).
func inr(paise int64) string {
	s := fmt.Sprint((paise + 50) / 100)
	if len(s) <= 3 {
		return "₹" + s
	}
	head, tail := s[:len(s)-3], s[len(s)-3:]
	parts := []string{tail}
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	return "₹" + strings.Join(append([]string{head}, parts...), ",")
}

var dayNames = [7]string{"Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"}

// commentary turns the buckets into a few observations worth reading. Pure.
func commentary(p *Patterns, occ [7]int, days int) []string {
	var total int64
	for _, b := range p.Weekdays {
		total += b.Paise
	}
	notes := []string{}
	if total == 0 || days == 0 {
		return notes
	}
	typical := float64(total) / float64(days) // an average day
	best, bestAvg := 0, 0.0
	for i, b := range p.Weekdays {
		if occ[i] == 0 {
			continue
		}
		if avg := float64(b.Paise) / float64(occ[i]); avg > bestAvg {
			best, bestAvg = i, avg
		}
	}
	if f := bestAvg / typical; f >= 1.3 {
		notes = append(notes, fmt.Sprintf("%s are your heaviest day: %s on average, %.1fx a typical day.", dayNames[best], inr(int64(bestAvg)), f))
	}
	weekend := p.Weekdays[0].Paise + p.Weekdays[6].Paise
	if share := float64(weekend) / float64(total); share >= 2.0/7*1.25 || share <= 2.0/7*0.75 {
		notes = append(notes, fmt.Sprintf("Weekends take %d%% of your spending, from 2 of every 7 days.", int(share*100+0.5)))
	}
	var late int64
	for _, h := range []int{22, 23, 0, 1, 2, 3} {
		late += p.Hours[h].Paise
	}
	if share := float64(late) / float64(total); share >= 0.15 {
		notes = append(notes, fmt.Sprintf("%d%% of your spending happens between 10pm and 4am.", int(share*100+0.5)))
	}
	parts := []struct {
		name  string
		hours []int
	}{
		{"morning (5am-12pm)", []int{5, 6, 7, 8, 9, 10, 11}},
		{"afternoon (12-5pm)", []int{12, 13, 14, 15, 16}},
		{"evening (5-10pm)", []int{17, 18, 19, 20, 21}},
		{"late night (10pm-5am)", []int{22, 23, 0, 1, 2, 3, 4}},
	}
	topName, top := "", int64(0)
	for _, part := range parts {
		var sum int64
		for _, h := range part.hours {
			sum += p.Hours[h].Paise
		}
		if sum > top {
			topName, top = part.name, sum
		}
	}
	if top > 0 {
		notes = append(notes, fmt.Sprintf("Most of it lands in the %s: %d%% of the total.", topName, int(float64(top)/float64(total)*100+0.5)))
	}
	return notes
}
