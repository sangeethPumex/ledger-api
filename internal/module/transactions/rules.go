package transactions

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// rulesVersion is stored as the prompt version of transactions read by a rule; bump it when a template changes.
const rulesVersion = "rules-v1"

// smsTemplate is one fixed bank SMS format. re runs over the message with whitespace collapsed and names its groups
// amount, last4, date and, where the format has them, time, name and ref. layout parses "date" or "date time".
type smsTemplate struct {
	bank, direction, kind, layout string
	re                            *regexp.Regexp
}

// tmpl anchors the pattern at the start, so a promo or a "will be debited" notice that quotes a format does not match.
func tmpl(bank, direction, kind, layout, pattern string) smsTemplate {
	pattern = strings.NewReplacer(
		"AMT", `Rs[.: ]*(?P<amount>[\d,]+(?:\.\d{1,2})?)`,
		"L4", `(?P<last4>\d{4})`,
		"REF", `(?P<ref>\d+)`,
	).Replace(pattern)
	return smsTemplate{bank, direction, kind, layout, regexp.MustCompile("^" + pattern)}
}

var smsTemplates = []smsTemplate{
	tmpl("Federal Bank", "debit", "expense", "2Jan06 15:04",
		`Debited AMT from a/c X+L4 on (?P<date>\d{1,2}[A-Za-z]{3}\d{2}) (?P<time>\d{1,2}:\d{2}) via UPI to (?P<name>.+?)\. ?Ref REF\..*-Federal Bank$`),
	tmpl("Federal Bank", "credit", "income", "2Jan2006 15:04:05",
		`Dear Customer, AMT credited to your A/c X+L4 on (?P<date>\d{1,2}[A-Za-z]{3}\d{4}) (?P<time>\d{1,2}:\d{2}:\d{2})\. ?BAL-.*Federal Bank$`),
	tmpl("HDFC Bank", "debit", "expense", "02/01/06",
		`Sent AMT From HDFC Bank A/C \*L4 To (?P<name>.+?) On (?P<date>\d{2}/\d{2}/\d{2}) Ref REF Not You\?`),
	tmpl("HDFC Bank", "credit", "income", "02-01-06",
		`Credit Alert! ?AMT credited to HDFC Bank A/c X+L4 on (?P<date>\d{2}-\d{2}-\d{2}) from VPA (?P<name>\S+) \(UPI REF\)`),
	tmpl("Union Bank of India", "debit", "expense", "02-01-2006 15:04:05",
		`Union Bank of India A/c \*L4 Debited AMT on (?P<date>\d{2}-\d{2}-\d{4}) (?P<time>\d{2}:\d{2}:\d{2}) by Mob Bk ref no REF, Fvg: (?P<name>.+?) Avl Bal`),
	tmpl("Union Bank of India", "credit", "refund", "02-01-2006 15:04:05",
		`A/c \*L4 Credited for AMT on (?P<date>\d{2}-\d{2}-\d{4}) (?P<time>\d{2}:\d{2}:\d{2}) by Mob Bk Rev Tran ref no REF .*-Union Bank of India$`),
	tmpl("Kotak Bank", "debit", "expense", "02-01-06",
		`Sent AMT from Kotak Bank A/c X+L4 to (?P<name>.+?) on (?P<date>\d{2}-\d{2}-\d{2})\. ?UPI Ref:? ?REF\b`),
	tmpl("Kotak Bank", "credit", "income", "02-01-06",
		`Received AMT in your Kotak Bank AC X*L4 from (?P<name>.+?) on (?P<date>\d{2}-\d{2}-\d{2})\. ?UPI Ref:? ?REF\b`),
}

// Payees that can make a payment something other than a plain expense or income (card bill, EMI, pay later): the model decides those.
var needsModel = regexp.MustCompile(`(?i)\bcred\b|cred\.?club|amazon ?pay|credit|card|bill|loan|\bemi\b|lazypay|simpl|pay ?later`)

// matchRules reads a message in a known format without the model. It returns nil, meaning "ask the model", for any other
// message, for a payee the format alone cannot classify, and for users with a parse hint, which only the model can follow.
func matchRules(message, holder, hint string) *extraction {
	if hint != "" {
		return nil
	}
	message = strings.Join(strings.Fields(message), " ")
	for _, t := range smsTemplates {
		m := t.re.FindStringSubmatch(message)
		if m == nil {
			continue
		}
		get := func(group string) string {
			if i := t.re.SubexpIndex(group); i >= 0 {
				return m[i]
			}
			return ""
		}
		when, format := get("date"), "2006-01-02"
		if clock := get("time"); clock != "" {
			when, format = when+" "+clock, "2006-01-02 15:04"
		}
		at, err := time.Parse(t.layout, when)
		if err != nil {
			return nil
		}
		e := &extraction{IsTransaction: true, Type: t.kind, Direction: t.direction, Amount: json.Number(get("amount")),
			OccurredAt: at.Format(format), Bank: t.bank, AccountLast4: get("last4"), Ref: get("ref"), RawVendor: get("name")}
		if strings.Contains(e.RawVendor, "@") {
			e.RawVendor = strings.TrimLeft(e.RawVendor, "*") // a leading * is masking
			e.UPIID = e.RawVendor
		}
		if needsModel.MatchString(e.RawVendor) {
			return nil
		}
		self, sure := ownName(e.RawVendor, holder)
		if !sure {
			return nil
		}
		if self {
			e.Type = "transfer"
		}
		return e
	}
	return nil
}

// ownName reports whether the other party is the account holder. Banks cut names short, so the full name or a cut that
// reaches past the first word counts. sure is false for a name that only partly looks like the holder's.
func ownName(name, holder string) (self, sure bool) {
	clean := func(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }
	n, h := clean(name), clean(holder)
	if n == "" || h == "" {
		return false, true
	}
	first, _, _ := strings.Cut(h, " ")
	if n == h || (strings.HasPrefix(h, n) && len(n) > len(first)+1) {
		return true, true
	}
	if strings.HasPrefix(h, n) {
		return false, false
	}
	for _, word := range strings.Fields(h) {
		if len(word) > 2 && strings.Contains(n, word) {
			return false, false
		}
	}
	return false, true
}
