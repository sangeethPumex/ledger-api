package transactions

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// PromptVersion is stored on every transaction; bump it when the prompt changes.
const PromptVersion = "v4"

var ist = time.FixedZone("IST", 5*3600+1800)

type extraction struct {
	IsTransaction     bool        `json:"is_transaction"`
	Type              string      `json:"type"`
	Direction         string      `json:"direction"`
	Amount            json.Number `json:"amount"`
	OccurredAt        string      `json:"occurred_at"`
	Bank              string      `json:"bank"`
	AccountLast4      string      `json:"account_last4"`
	Ref               string      `json:"ref"`
	RawVendor         string      `json:"raw_vendor"`
	UPIID             string      `json:"upi_id"`
	SuggestedCategory string      `json:"suggested_category"`
}

func systemPrompt(categories []string) string {
	return `You extract data from one Indian bank SMS. Reply with ONE JSON object only, no prose, with exactly these keys:
is_transaction (bool; false for OTPs, promos, balance alerts, anything that is not a money movement),
type (one of: expense, income, transfer, card_bill_payment, refund, atm, emi),
direction ("debit" if money left the account, "credit" if money came in),
amount (rupees as a number, e.g. 569.00; NOT the balance),
occurred_at ("YYYY-MM-DD HH:MM" 24h in the local time written in the SMS; "YYYY-MM-DD" if the SMS has no time; null if no date),
bank (e.g. "Federal Bank"), account_last4 (last 4 digits of the account or card the SMS is about),
ref (UPI/bank reference number exactly as written, or ""),
raw_vendor (merchant or payee name exactly as written, or ""),
upi_id (payee/sender UPI id like name@bank if present, else ""),
suggested_category (pick exactly one from: ` + strings.Join(categories, ", ") + `; "" if none fits).

Rules:
- Amount is the money moved. Ignore "Bal", "BAL-Rs", "Avl Bal". Formats: Rs 125.71, Rs.8500, Rs:246.00.
- Dates are day-first; a 2-digit year is 20xx. 22/10/26, 26Oct26, 26OCT2026, 08-10-2026 are all October 2026. Never swap day and month.
- account_last4 follows A/c, AC, a/c, X, XX or *, e.g. "A/c XX2411", "A/C *5963", "AC 9959".
- ref is the UPI/reference number after "Ref", "UPI Ref", "UPI", "ref no". Phone numbers in "Not you? Call ..." / "SMS BLOCK ... to ..." are NEVER a ref.
- Debit words: Debited, Sent. Credit words: credited, Credited, Received. Debits to merchants or people are "expense"; salary and other credits are "income".
- Debit: raw_vendor is the payee ("to X", "To X", "Fvg: X", or the UPI id when no name is given). Credit: raw_vendor is the sender ("from X", "from VPA x@bank"), or "" when the SMS names no sender.
- A leading "*" on a UPI id is masking, drop it: "VPA *ms-2@oksbi" gives upi_id "ms-2@oksbi". Truncated ids like "reyvanthrm@o" stay as written.
- A credit described as a reversal ("Rev Tran", "reversal") is "refund".
- The account holder is SANGEETH PROMOD. Money sent to or received from that name is the user's own money moving between accounts: type "transfer".
- Delivery apps and restaurants (e.g. SWIGGY) are "expense"; use the nearest food category.

Examples (SMS => JSON):
Debited Rs 125.71 from a/c X2411 on 26Oct26 10:52 via UPI to reyvanthrm@o. Ref 629929284247.Bal Rs 9635.93. Not you?Call 18004251199 -Federal Bank
=> {"is_transaction":true,"type":"expense","direction":"debit","amount":125.71,"occurred_at":"2026-10-26 10:52","bank":"Federal Bank","account_last4":"2411","ref":"629929284247","raw_vendor":"reyvanthrm@o","upi_id":"reyvanthrm@o","suggested_category":""}
Dear Customer, Rs.8500 credited to your A/c XX2411 on 26OCT2026 10:24:02. BAL-Rs.9828.01-Federal Bank
=> {"is_transaction":true,"type":"income","direction":"credit","amount":8500,"occurred_at":"2026-10-26 10:24","bank":"Federal Bank","account_last4":"2411","ref":"","raw_vendor":"","upi_id":"","suggested_category":""}
Credit Alert!Rs.265.00 credited to HDFC Bank A/c XX5963 on 26-10-26 from VPA *ms-2@oksbi (UPI 629978501489)
=> {"is_transaction":true,"type":"income","direction":"credit","amount":265,"occurred_at":"2026-10-26","bank":"HDFC Bank","account_last4":"5963","ref":"629978501489","raw_vendor":"ms-2@oksbi","upi_id":"ms-2@oksbi","suggested_category":""}
Sent Rs.1000.00 From HDFC Bank A/C *5963 To NIMESH R On 22/10/26 Ref 131700808679 Not You? Call 18002586161/SMS BLOCK UPI to 7308080808
=> {"is_transaction":true,"type":"expense","direction":"debit","amount":1000,"occurred_at":"2026-10-22","bank":"HDFC Bank","account_last4":"5963","ref":"131700808679","raw_vendor":"NIMESH R","upi_id":"","suggested_category":""}
Union Bank of India A/c *3109 Debited Rs:246.00 on 25-10-2026 19:44:12 by Mob Bk ref no 629862911438, Fvg: SWIGGY I Avl Bal Rs:894.00. Not you?Call 18002333/SMS BLOCK 3109 to 8879365472
=> {"is_transaction":true,"type":"expense","direction":"debit","amount":246,"occurred_at":"2026-10-25 19:44","bank":"Union Bank of India","account_last4":"3109","ref":"629862911438","raw_vendor":"SWIGGY I","upi_id":"","suggested_category":"Food"}
A/c *3109 Credited for Rs.2.00 on 08-10-2026 17:33:34 by Mob Bk Rev Tran ref no 628168514875 Avl Bal Rs.5198.00 -Union Bank of India
=> {"is_transaction":true,"type":"refund","direction":"credit","amount":2,"occurred_at":"2026-10-08 17:33","bank":"Union Bank of India","account_last4":"3109","ref":"628168514875","raw_vendor":"","upi_id":"","suggested_category":""}
Sent Rs.60.00 from Kotak Bank A/c X9959 to Transasia on 26-10-26. UPI Ref 629972201789. Not done by you? Tap https://kotak.bank.in/KBANKT/Fraud
=> {"is_transaction":true,"type":"expense","direction":"debit","amount":60,"occurred_at":"2026-10-26","bank":"Kotak Bank","account_last4":"9959","ref":"629972201789","raw_vendor":"Transasia","upi_id":"","suggested_category":""}
Received Rs.1.00 in your Kotak Bank AC 9959 from SANGEETH PROMOD on 26-10-26.UPI Ref:629939471266
=> {"is_transaction":true,"type":"transfer","direction":"credit","amount":1,"occurred_at":"2026-10-26","bank":"Kotak Bank","account_last4":"9959","ref":"629939471266","raw_vendor":"SANGEETH PROMOD","upi_id":"","suggested_category":""}
Your OTP for login is 482910. Do not share it.
=> {"is_transaction":false}

In the examples suggested_category is a placeholder: always pick from the allowed list above, or "" if none fits.`
}

// parseExtraction pulls the JSON object out of the model reply and validates it.
// For a non-transaction it returns (e, 0, zero, nil) with e.IsTransaction false.
func parseExtraction(reply, message string, receivedAt time.Time) (*extraction, int64, time.Time, error) {
	i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if i < 0 || j < i {
		return nil, 0, time.Time{}, errors.New("no json in reply")
	}
	var e extraction
	if err := json.Unmarshal([]byte(reply[i:j+1]), &e); err != nil {
		return nil, 0, time.Time{}, err
	}
	if !e.IsTransaction {
		return &e, 0, time.Time{}, nil
	}
	if !validTypes[e.Type] {
		return nil, 0, time.Time{}, errors.New("bad type")
	}
	paise, err := toPaise(e.Amount.String())
	if err != nil || paise <= 0 || paise > maxPaise {
		return nil, 0, time.Time{}, errors.New("bad amount")
	}
	if e.Direction != "debit" && e.Direction != "credit" {
		e.Direction = ""
	}
	if len(e.AccountLast4) != 4 || strings.Trim(e.AccountLast4, "0123456789") != "" {
		e.AccountLast4 = ""
	}
	if e.Ref != "" && !strings.Contains(message, e.Ref) {
		e.Ref = ""
	}
	at := receivedAt
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, e.OccurredAt, ist); err == nil {
			at = t.UTC()
			break
		}
	}
	return &e, paise, at, nil
}

// toPaise converts a decimal rupee string to paise without floats.
func toPaise(s string) (int64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 {
		if strings.Trim(frac[2:], "0") != "" {
			return 0, errors.New("sub-paise amount")
		}
		frac = frac[:2]
	}
	frac += strings.Repeat("0", 2-len(frac))
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil || w < 0 || f < 0 {
		return 0, errors.New("bad number")
	}
	return w*100 + f, nil
}
