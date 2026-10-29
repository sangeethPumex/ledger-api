package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/nim"
	"ledger-backend/internal/spend"
)

func tool(name, desc string, props map[string]any, required ...string) nim.Tool {
	t := nim.Tool{Type: "function"}
	t.Function.Name, t.Function.Description = name, desc
	t.Function.Parameters = map[string]any{"type": "object", "properties": props, "required": required}
	return t
}

var strProp = func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// aiTools are small, read-only lookups (plus remember); every figure they return is recorded data, in rupees.
var aiTools = []nim.Tool{
	tool("spend_by_category", "Total spending per category for one calendar month, biggest first.",
		map[string]any{"month": strProp("YYYY-MM")}, "month"),
	tool("transactions", "Spending at one vendor between two dates: total, count and the latest transactions. Vendor is a case-insensitive name match.",
		map[string]any{"vendor": strProp("vendor name or part of it"), "from": strProp("YYYY-MM-DD, default 90 days ago"), "to": strProp("YYYY-MM-DD inclusive, default today"),
			"limit": map[string]any{"type": "integer", "description": "latest transactions to list, 1-20, default 10"}}, "vendor"),
	tool("compare_months", "Per-category spending in two months side by side, with totals and the change.",
		map[string]any{"month_a": strProp("YYYY-MM"), "month_b": strProp("YYYY-MM")}, "month_a", "month_b"),
	tool("remember", "Save one lasting goal or preference the user just stated (for example a savings target). Not for questions or one-off facts.",
		map[string]any{"fact": strProp("one short sentence in the user's words, at most 200 characters")}, "fact"),
}

const maxToolCalls = 4 // per model turn

func monthRange(ym string) (Range, error) {
	t, err := time.ParseInLocation("2006-01", ym, ist.Loc)
	if err != nil || t.Year() < 2000 {
		return Range{}, errors.New("month must be YYYY-MM")
	}
	return Range{From: t, To: t.AddDate(0, 1, 0)}, nil
}

func parseDay(v string, def time.Time) (time.Time, error) {
	if v == "" {
		return def, nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, ist.Loc)
	if err != nil {
		return t, errors.New("dates must be YYYY-MM-DD")
	}
	return t, nil
}

type catRow struct {
	Category string  `json:"category"`
	Rupees   float64 `json:"rupees"`
	Count    int     `json:"count,omitempty"`
}

// spendByCategory sums the same classified flows the dashboard shows, so the AI and the dashboard cannot disagree.
func (s *Service) spendByCategory(ctx context.Context, r Range) (map[string]int64, map[string]int, int64, error) {
	flows, _, err := s.aggregate(ctx, r.From, r.To)
	if err != nil {
		return nil, nil, 0, err
	}
	paise, count, total := map[string]int64{}, map[string]int{}, int64(0)
	for _, f := range flows {
		if f.ID.Dir == "out" {
			paise[f.ID.Name], count[f.ID.Name] = f.Paise, f.Count
			total += f.Paise
		}
	}
	return paise, count, total, nil
}

func (s *Service) toolSpendByCategory(ctx context.Context, month string) (any, error) {
	r, err := monthRange(month)
	if err != nil {
		return nil, err
	}
	paise, count, total, err := s.spendByCategory(ctx, r)
	if err != nil {
		return nil, err
	}
	rows := []catRow{}
	for name, p := range paise {
		rows = append(rows, catRow{name, rupees(p), count[name]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Rupees > rows[j].Rupees })
	return map[string]any{"month": month, "totalRupees": rupees(total), "categories": rows}, nil
}

func (s *Service) toolCompareMonths(ctx context.Context, a, b string) (any, error) {
	var res [2]struct {
		paise map[string]int64
		total int64
	}
	for i, m := range []string{a, b} {
		r, err := monthRange(m)
		if err != nil {
			return nil, err
		}
		if res[i].paise, _, res[i].total, err = s.spendByCategory(ctx, r); err != nil {
			return nil, err
		}
	}
	seen, rows := map[string]bool{}, []map[string]any{}
	for _, side := range res {
		for name := range side.paise {
			if !seen[name] {
				seen[name] = true
				rows = append(rows, map[string]any{"category": name, a: rupees(res[0].paise[name]), b: rupees(res[1].paise[name]), "changeRupees": rupees(res[1].paise[name] - res[0].paise[name])})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return abs(rows[i]["changeRupees"].(float64)) > abs(rows[j]["changeRupees"].(float64)) })
	return map[string]any{"totals": map[string]any{a: rupees(res[0].total), b: rupees(res[1].total), "changeRupees": rupees(res[1].total - res[0].total)}, "categories": rows}, nil
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

type vendorTxn struct {
	Date   string  `bson:"date" json:"date"`
	Vendor string  `bson:"vendor" json:"vendor"`
	Rupees float64 `bson:"rupees" json:"rupees"`
}

// toolTransactions totals spending whose counterparty matches vendor. The total counts the user's own part (money a friend owes back is excluded),
// like the dashboard; one pipeline returns the total and the latest rows.
func (s *Service) toolTransactions(ctx context.Context, vendor, from, to string, limit int, now time.Time) (any, error) {
	if vendor == "" || len(vendor) > 100 {
		return nil, errors.New("vendor required")
	}
	today := ist.Midnight(now)
	f, err := parseDay(from, today.AddDate(0, 0, -90))
	if err != nil {
		return nil, err
	}
	t, err := parseDay(to, today)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 20 {
		limit = 10
	}
	pipe := mongo.Pipeline{{{Key: "$match", Value: spend.Match(f, t.AddDate(0, 0, 1))}}}
	pipe = append(pipe, spend.Net()...)
	pipe = append(pipe, mongo.Pipeline{
		{{Key: "$lookup", Value: bson.M{"from": "vendors", "localField": "vendor_id", "foreignField": "_id", "as": "ven"}}},
		{{Key: "$addFields", Value: bson.M{"_label": labelExpr()}}},
		{{Key: "$match", Value: bson.M{"_label": bson.M{"$regex": regexp.QuoteMeta(vendor), "$options": "i"}}}},
		{{Key: "$facet", Value: bson.M{
			"rows": bson.A{
				bson.M{"$sort": bson.M{"occurred_at": -1}},
				bson.M{"$limit": limit},
				bson.M{"$project": bson.M{"_id": 0, "vendor": "$_label", "rupees": bson.M{"$divide": bson.A{"$amount_paise", 100}},
					"date": bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$occurred_at", "timezone": "Asia/Kolkata"}}}},
			},
			"total": bson.A{bson.M{"$group": bson.M{"_id": nil, "paise": bson.M{"$sum": "$amount_paise"}, "count": bson.M{"$sum": 1}}}},
		}}},
	}...)
	cur, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	var out []struct {
		Rows  []vendorTxn `bson:"rows"`
		Total []struct {
			Paise int64 `bson:"paise"`
			Count int   `bson:"count"`
		} `bson:"total"`
	}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	res := map[string]any{"vendor": vendor, "from": f.Format("2006-01-02"), "to": t.Format("2006-01-02"), "totalRupees": 0.0, "count": 0, "latest": []vendorTxn{}}
	if len(out) > 0 {
		if len(out[0].Total) > 0 {
			res["totalRupees"], res["count"] = rupees(out[0].Total[0].Paise), out[0].Total[0].Count
		}
		if out[0].Rows != nil {
			res["latest"] = out[0].Rows
		}
	}
	return res, nil
}

// runTool executes one model tool call and returns the text handed back to the model. Failures are reported to the model, not the user,
// so it can correct a bad argument or say it could not look something up.
func (s *Service) runTool(ctx context.Context, tc nim.ToolCall, now time.Time) string {
	var a struct {
		Month, MonthA, MonthB, Vendor, From, To, Fact string
		Limit                                          int
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return `{"error":"arguments must be a JSON object"}`
	}
	a.Month, _ = args["month"].(string)
	a.MonthA, _ = args["month_a"].(string)
	a.MonthB, _ = args["month_b"].(string)
	a.Vendor, _ = args["vendor"].(string)
	a.From, _ = args["from"].(string)
	a.To, _ = args["to"].(string)
	a.Fact, _ = args["fact"].(string)
	if l, ok := args["limit"].(float64); ok {
		a.Limit = int(l)
	}
	var (
		res any
		err error
	)
	switch tc.Function.Name {
	case "spend_by_category":
		res, err = s.toolSpendByCategory(ctx, a.Month)
	case "compare_months":
		res, err = s.toolCompareMonths(ctx, a.MonthA, a.MonthB)
	case "transactions":
		res, err = s.toolTransactions(ctx, a.Vendor, a.From, a.To, a.Limit, now)
	case "remember":
		if err = s.profile.Remember(ctx, a.Fact); err == nil {
			res = map[string]string{"saved": a.Fact}
		}
	default:
		err = errors.New("unknown tool")
	}
	if err != nil {
		return mustJSON(map[string]string{"error": err.Error()})
	}
	return mustJSON(res)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
