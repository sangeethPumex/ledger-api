package transactions

import (
	"testing"
	"time"
)

// All names, numbers and references here are made up.
func TestMatchRules(t *testing.T) {
	const holder = "JANE DOE"
	cases := []struct {
		msg, bank, kind, direction, vendor, upi, ref, at string
		paise                                            int64
	}{
		{"Debited Rs 125.71 from a/c X1234 on 26Oct26 10:52 via UPI to shop@okbank. Ref 100000000001.Bal Rs 9635.93. Not you?Call 1800000 -Federal Bank",
			"Federal Bank", "expense", "debit", "shop@okbank", "shop@okbank", "100000000001", "2026-10-26 10:52", 12571},
		{"Debited Rs 500.00 from a/c X1234 on 5Nov26 11:28 via UPI to JANE D. Ref 100000000009.Bal Rs 1.00. -Federal Bank",
			"Federal Bank", "transfer", "debit", "JANE D", "", "100000000009", "2026-11-05 11:28", 50000},
		{"Dear Customer, Rs.8500 credited to your A/c XX1234 on 26OCT2026 10:24:02. BAL-Rs.9828.01-Federal Bank",
			"Federal Bank", "income", "credit", "", "", "", "2026-10-26 10:24", 850000},
		{"Sent Rs.1000.00 \n From HDFC Bank A/C *5678 \n To JOHN SMITH \n On 22/10/26 \n Ref 100000000002 \n Not You? \n Call 1800000",
			"HDFC Bank", "expense", "debit", "JOHN SMITH", "", "100000000002", "2026-10-22 00:00", 100000},
		{"Credit Alert!Rs.265.00 credited to HDFC Bank A/c XX5678 on 26-10-26 from VPA *fr-2@oksbi (UPI 100000000003)",
			"HDFC Bank", "income", "credit", "fr-2@oksbi", "fr-2@oksbi", "100000000003", "2026-10-26 00:00", 26500},
		{"Union Bank of India A/c *3333 Debited Rs:246.00 on 25-10-2026 19:44:12 by Mob Bk ref no 100000000004, Fvg: SWIGGY I Avl Bal Rs:894.00. Not you?",
			"Union Bank of India", "expense", "debit", "SWIGGY I", "", "100000000004", "2026-10-25 19:44", 24600},
		{"A/c *3333 Credited for Rs.2.00 on 08-10-2026 17:33:34 by Mob Bk Rev Tran ref no 100000000005 Avl Bal Rs.5198.00 -Union Bank of India",
			"Union Bank of India", "refund", "credit", "", "", "100000000005", "2026-10-08 17:33", 200},
		{"Sent Rs.1,060.50 from Kotak Bank A/c X9999 to Cafe on Main on 26-10-26. UPI Ref 100000000006. Not done by you? Tap https://example.test",
			"Kotak Bank", "expense", "debit", "Cafe on Main", "", "100000000006", "2026-10-26 00:00", 106050},
		{"Received Rs.1.00 in your Kotak Bank AC 9999 from JANE DOE on 26-10-26.UPI Ref:100000000007",
			"Kotak Bank", "transfer", "credit", "JANE DOE", "", "100000000007", "2026-10-26 00:00", 100},
	}
	for _, c := range cases {
		m := matchRules(c.msg, holder, "")
		if m == nil {
			t.Errorf("no rule matched %q", c.msg)
			continue
		}
		e, paise, at, err := checkExtraction(m, c.msg, time.Time{})
		got := at.In(ist).Format("2006-01-02 15:04")
		if err != nil || paise != c.paise || got != c.at || e.Bank != c.bank || e.Type != c.kind || e.Direction != c.direction ||
			e.RawVendor != c.vendor || e.UPIID != c.upi || e.Ref != c.ref || len(e.AccountLast4) != 4 {
			t.Errorf("%q: got %+v paise %d at %s err %v", c.msg, *e, paise, got, err)
		}
	}
}

func TestMatchRulesDefersToModel(t *testing.T) {
	sent := func(to string) string {
		return "Sent Rs.10.00 from Kotak Bank A/c X9999 to " + to + " on 26-10-26. UPI Ref 100000000008."
	}
	for _, msg := range []string{
		"Your OTP for login is 482910. Do not share it.",
		"Alert: " + sent("Cafe"),
		"Rs.500 will be debited from HDFC Bank A/c XX5678 on 26-10-26",
		"Your statement for Axis Bank Credit Card no. XX0000 is generated.",
		"Sent Rs.10.00 from Kotak Bank A/c X9999 to Cafe on 99-99-26. UPI Ref 1.", // no such date
		sent("Amazon Pay"), sent("CRED Club"), sent("cred.club@axisb"), sent("Home Loan"),
		sent("JANE"), sent("JAN"), sent("MARY DOE"), sent("jane99@oksbi"), // only partly the holder's name
	} {
		if e := matchRules(msg, "JANE DOE", ""); e != nil {
			t.Errorf("%q: rule matched %+v", msg, *e)
		}
	}
	if matchRules(sent("Cafe"), "JANE DOE", "Treat Cafe as a transfer.") != nil {
		t.Error("a parse hint must go to the model")
	}
	if matchRules(sent("Cafe"), "JANE DOE", "") == nil {
		t.Error("plain payee should match")
	}
}
