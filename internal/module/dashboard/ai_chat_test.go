package dashboard

import "testing"

func TestParseAnswer(t *testing.T) {
	a := parseAnswer("```json\n{\"conclusion\":\"You spent ₹4,200.\",\"metrics\":[{\"label\":\"Swiggy\",\"rupees\":4200,\"source\":\"made-up\"},{\"label\":\"empty\"}],\"insights\":[\"x\"],\"links\":[{\"label\":\"Txns\",\"href\":\"/transactions?x=1\"},{\"label\":\"Bad\",\"href\":\"https://evil.example\"}]}\n```")
	if a.Conclusion != "You spent ₹4,200." {
		t.Fatalf("conclusion = %q", a.Conclusion)
	}
	if len(a.Metrics) != 1 || a.Metrics[0].Source != srcEstimated {
		t.Fatalf("metrics = %+v (want one, unknown source downgraded to estimated)", a.Metrics)
	}
	if len(a.Links) != 1 || a.Links[0].Href != "/transactions" {
		t.Fatalf("links = %+v (want only the app page, query dropped)", a.Links)
	}
}

func TestParseAnswerPlainText(t *testing.T) {
	if a := parseAnswer("just words"); a.Conclusion != "just words" || a.Metrics == nil || a.Links == nil {
		t.Fatalf("plain text fallback = %+v", a)
	}
	if a := parseAnswer(""); a.Conclusion == "" {
		t.Fatal("empty reply must still produce a conclusion")
	}
}

func TestParseAnswerSalvagesUnclosedArray(t *testing.T) {
	a := parseAnswer(`{"conclusion":"Spend fell.","metrics":[{"label":"Cash","rupees":100,"source":"recorded"}],"insights":["Review mandates.","links":[{"label":"/transactions","href":"/transactions"}]"}`)
	if a.Conclusion != "Spend fell." || len(a.Metrics) != 1 || len(a.Insights) != 0 {
		t.Fatalf("salvage = %+v", a)
	}
}

func TestChartIsCleaned(t *testing.T) {
	ok := parseAnswer(`{"conclusion":"x","chart":{"kind":"bar","title":"t","points":[{"label":"Oct","rupees":5},{"label":"Nov","rupees":2},{"label":"","rupees":9}]}}`)
	if ok.Chart == nil || len(ok.Chart.Points) != 2 {
		t.Fatalf("chart = %+v", ok.Chart)
	}
	for _, bad := range []string{
		`{"conclusion":"x","chart":{"kind":"pie","points":[{"label":"a","rupees":1},{"label":"b","rupees":2}]}}`,
		`{"conclusion":"x","chart":{"kind":"bar","points":[{"label":"a","rupees":1}]}}`,
	} {
		if parseAnswer(bad).Chart != nil {
			t.Fatalf("chart kept for %s", bad)
		}
	}
}

func TestValidJSON(t *testing.T) {
	if !validJSON("```json\n{\"conclusion\":\"x\"}\n```") || validJSON(`{"insights":["a","links":[]"}`) || validJSON("words") {
		t.Fatal("validJSON misjudged")
	}
}
