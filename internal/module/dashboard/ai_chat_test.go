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
