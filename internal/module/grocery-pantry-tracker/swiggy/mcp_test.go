package swiggy

import (
	"strings"
	"testing"
)

func TestReadReplySSE(t *testing.T) {
	stream := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"structuredContent\":{\"success\":true,\"data\":{\"n\":3}}}}\n\n"
	r, err := readReply(strings.NewReader(stream), true, 7)
	if err != nil || r.Result == nil {
		t.Fatalf("reply %+v %v", r, err)
	}
	var out struct{ N int }
	if err := decodeResult("x", r.Result, &out); err != nil || out.N != 3 {
		t.Errorf("decoded %+v %v", out, err)
	}
	if _, err := readReply(strings.NewReader("data: {\"id\":1}\n\n"), true, 2); err == nil {
		t.Error("took a reply meant for another request")
	}
}

func TestDecodeResultError(t *testing.T) {
	err := decodeResult("get_orders", []byte(`{"isError":true,"structuredContent":{"success":false,"error":{"message":"Invalid count"}}}`), &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "Invalid count") {
		t.Errorf("got %v", err)
	}
}

// The live server sends the data bare in structuredContent, not in the documented {success, data} wrapper.
func TestDecodeResultBare(t *testing.T) {
	var out struct{ Orders []struct{ OrderID string } }
	if err := decodeResult("get_orders", []byte(`{"content":[{"type":"text","text":"Found 1"}],"structuredContent":{"orders":[{"OrderID":"9"}]}}`), &out); err != nil || len(out.Orders) != 1 {
		t.Errorf("bare: %+v %v", out, err)
	}
	if err := decodeResult("x", []byte(`{"isError":true,"content":[{"text":"Not allowed"}]}`), &out); err == nil || !strings.Contains(err.Error(), "Not allowed") {
		t.Errorf("isError without structuredContent: %v", err)
	}
}
