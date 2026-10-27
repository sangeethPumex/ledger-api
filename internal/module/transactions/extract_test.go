package transactions

import (
	"testing"
	"time"
)

func TestToPaise(t *testing.T) {
	for in, want := range map[string]int64{"569.00": 56900, "569": 56900, "569.5": 56950, "1,234.07": 123407, ".5": 50, "2.000": 200} {
		if got, err := toPaise(in); err != nil || got != want {
			t.Errorf("%q: got %d, %v want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"abc", "1.234", "-5"} {
		if _, err := toPaise(in); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestParseExtraction(t *testing.T) {
	msg := "Debited Rs 569.00 from a/c X2411 on 21Oct26 13:27 via UPI to Blinkit. Ref 629414010066."
	recv := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	reply := "```json\n{\"is_transaction\":true,\"type\":\"expense\",\"amount\":569.0,\"occurred_at\":\"2026-10-21 13:27\",\"account_last4\":\"2411\",\"ref\":\"629414010066\",\"raw_vendor\":\"Blinkit\"}\n```"
	e, paise, at, err := parseExtraction(reply, msg, recv)
	if err != nil || paise != 56900 || e.Ref != "629414010066" || !at.Equal(time.Date(2026, 10, 21, 7, 57, 0, 0, time.UTC)) {
		t.Fatalf("got %+v %d %v %v", e, paise, at, err)
	}
	// ref not in message is dropped; bad type is rejected; non-transaction passes through.
	e, _, _, _ = parseExtraction(`{"is_transaction":true,"type":"expense","amount":"5","ref":"999"}`, msg, recv)
	if e.Ref != "" {
		t.Error("hallucinated ref kept")
	}
	if _, _, _, err := parseExtraction(`{"is_transaction":true,"type":"gift","amount":5}`, msg, recv); err == nil {
		t.Error("bad type accepted")
	}
	if e, _, _, err := parseExtraction(`{"is_transaction":false}`, msg, recv); err != nil || e.IsTransaction {
		t.Error("non-transaction mishandled")
	}
}
