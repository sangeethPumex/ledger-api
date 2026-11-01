package brainjogger

import (
	"testing"
	"time"
)

func TestParseAndDue(t *testing.T) {
	l, msg := Input{Title: "Forum Mall", Kind: "shopping", RemindAt: "2026-11-01T10:30:00Z", Items: []ItemInput{{Name: "Milk", EstPaise: 6000}, {Name: " "}}}.Parse()
	if msg != "" || len(l.Items) != 1 || l.Items[0].ID == "" {
		t.Fatalf("parse: %q %+v", msg, l)
	}
	if len(l.LeadMins) != 2 || l.LeadMins[0] != 15 || l.LeadMins[1] != 30 {
		t.Fatalf("default leads: %v", l.LeadMins)
	}
	at := l.RemindAt
	if d := l.Due(at.Add(-31 * time.Minute)); len(d) != 0 {
		t.Fatalf("too early: %v", d)
	}
	if d := l.Due(at.Add(-29 * time.Minute)); len(d) != 1 || d[0] != 30 {
		t.Fatalf("30 min: %v", d)
	}
	l.Notified = []int{30}
	if d := l.Due(at.Add(-14 * time.Minute)); len(d) != 1 || d[0] != 15 {
		t.Fatalf("15 min: %v", d)
	}
}

func TestParseRejects(t *testing.T) {
	const at = "2026-11-01T10:30:00Z"
	for name, in := range map[string]Input{
		"no title":   {Kind: "reminder", RemindAt: at},
		"bad kind":   {Title: "x", Kind: "other", RemindAt: at},
		"bad time":   {Title: "x", Kind: "reminder", RemindAt: "tomorrow"},
		"empty shop": {Title: "x", Kind: "shopping", RemindAt: at},
		"negative":   {Title: "x", Kind: "shopping", RemindAt: at, Items: []ItemInput{{Name: "a", EstPaise: -1}}},
		"far lead":   {Title: "x", Kind: "reminder", RemindAt: at, LeadMins: []int{99999}},
	} {
		if _, msg := in.Parse(); msg == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLeft(t *testing.T) {
	l := List{Items: []Item{{EstPaise: 100}, {EstPaise: 250, Bought: true}, {EstPaise: 50}}}
	if n, p := l.Left(); n != 2 || p != 150 {
		t.Fatalf("left: %d %d", n, p)
	}
}
