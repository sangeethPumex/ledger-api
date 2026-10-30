package transactions

import (
	"testing"
	"time"
)

func TestCreateInputCheck(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ok := func(in CreateInput) CreateInput { in.OccurredAt = now; return in }

	in := ok(CreateInput{Type: "expense", AmountPaise: 500})
	if m := in.Check(now); m != "" || in.Direction != "debit" {
		t.Errorf("expense: %q dir %q", m, in.Direction)
	}
	in = ok(CreateInput{Type: "income", AmountPaise: 500})
	if m := in.Check(now); m != "" || in.Direction != "credit" {
		t.Errorf("income: %q dir %q", m, in.Direction)
	}
	for name, in := range map[string]CreateInput{
		"bad type":            ok(CreateInput{Type: "gift", AmountPaise: 1}),
		"zero amount":         ok(CreateInput{Type: "expense"}),
		"no date":             {Type: "expense", AmountPaise: 1},
		"future":              {Type: "expense", AmountPaise: 1, OccurredAt: now.AddDate(0, 0, 5)},
		"account and card":    ok(CreateInput{Type: "expense", AmountPaise: 1, AccountID: "a", CardID: "b"}),
		"transfer no way":     ok(CreateInput{Type: "transfer", AmountPaise: 1}),
		"owed on credit":      ok(CreateInput{Type: "income", AmountPaise: 100, OwedPaise: 10, OwedFrom: "Ravi"}),
		"owed more than paid": ok(CreateInput{Type: "expense", AmountPaise: 100, OwedPaise: 200, OwedFrom: "Ravi"}),
		"owed no name":        ok(CreateInput{Type: "expense", AmountPaise: 100, OwedPaise: 50}),
		"pays card wrong":     ok(CreateInput{Type: "expense", AmountPaise: 100, PaidCardID: "x"}),
	} {
		if in.Check(now) == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	if in := ok(CreateInput{Type: "transfer", Direction: "credit", AmountPaise: 1}); in.Check(now) != "" {
		t.Error("transfer with a direction rejected")
	}
}
