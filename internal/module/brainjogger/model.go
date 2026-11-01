// Package brainjogger holds shopping lists and reminders, and pushes a notification to the phone shortly before each one is due.
package brainjogger

import (
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	Collection     = "jogger_lists"
	PushCollection = "jogger_push"
	maxPaise       = 10_000_000_000 // sanity cap, as for transactions
	maxItems       = 100
	maxLeads       = 5
	maxLeadMins    = 7 * 24 * 60
)

// DefaultLeads are the minutes before the due time at which a notification goes out.
var DefaultLeads = []int{30, 15}

type Item struct {
	ID       string `bson:"id" json:"id"`
	Name     string `bson:"name" json:"name"`
	EstPaise int64  `bson:"est_paise" json:"est_paise"`
	Bought   bool   `bson:"bought" json:"bought"`
}

// List is a shopping list (items with an expected price) or a plain reminder (no items), due at RemindAt.
type List struct {
	ID        bson.ObjectID `bson:"_id" json:"id"`
	Title     string        `bson:"title" json:"title"`
	Kind      string        `bson:"kind" json:"kind"` // shopping | reminder
	Note      string        `bson:"note,omitempty" json:"note,omitempty"`
	RemindAt  time.Time     `bson:"remind_at" json:"remind_at"`
	LeadMins  []int         `bson:"lead_mins" json:"lead_mins"`
	Notified  []int         `bson:"notified" json:"-"` // lead times already pushed
	Items     []Item        `bson:"items" json:"items"`
	CreatedAt time.Time     `bson:"created_at" json:"created_at"`
}

type ItemInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	EstPaise int64  `json:"est_paise"`
	Bought   bool   `json:"bought"`
}

// Input is the body of a create or update; RemindAt is RFC 3339.
type Input struct {
	Title    string      `json:"title"`
	Kind     string      `json:"kind"`
	Note     string      `json:"note"`
	RemindAt string      `json:"remind_at"`
	LeadMins []int       `json:"lead_mins"`
	Items    []ItemInput `json:"items"`
}

// Parse validates the input and returns the list it describes; msg is non-empty when the input is bad.
func (in Input) Parse() (l List, msg string) {
	at, err := time.Parse(time.RFC3339, in.RemindAt)
	switch {
	case strings.TrimSpace(in.Title) == "":
		return l, "title required"
	case in.Kind != "shopping" && in.Kind != "reminder":
		return l, "kind must be shopping or reminder"
	case err != nil:
		return l, "remind_at must be a date and time"
	case len(in.Items) > maxItems:
		return l, "too many items"
	case len(in.LeadMins) > maxLeads:
		return l, "too many alert times"
	}
	leads := slices.Clone(in.LeadMins)
	if leads == nil {
		leads = slices.Clone(DefaultLeads)
	}
	for _, m := range leads {
		if m < 0 || m > maxLeadMins {
			return l, "alert time must be within a week"
		}
	}
	slices.Sort(leads)
	leads = slices.Compact(leads)

	items := make([]Item, 0, len(in.Items))
	for _, it := range in.Items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		if it.EstPaise < 0 || it.EstPaise > maxPaise {
			return l, "amount out of range"
		}
		id := it.ID
		if id == "" {
			id = bson.NewObjectID().Hex()
		}
		items = append(items, Item{ID: id, Name: name, EstPaise: it.EstPaise, Bought: it.Bought})
	}
	if in.Kind == "shopping" && len(items) == 0 {
		return l, "add at least one item"
	}
	return List{Title: strings.TrimSpace(in.Title), Kind: in.Kind, Note: strings.TrimSpace(in.Note), RemindAt: at.UTC(), LeadMins: leads, Notified: []int{}, Items: items}, ""
}

// Left counts the items still to buy and their expected cost.
func (l List) Left() (n int, paise int64) {
	for _, it := range l.Items {
		if !it.Bought {
			n++
			paise += it.EstPaise
		}
	}
	return
}

// Due lists the lead times whose moment has come and that have not been pushed yet.
func (l List) Due(now time.Time) []int {
	var out []int
	for _, m := range l.LeadMins {
		if !slices.Contains(l.Notified, m) && !now.Before(l.RemindAt.Add(-time.Duration(m)*time.Minute)) {
			out = append(out, m)
		}
	}
	return out
}
