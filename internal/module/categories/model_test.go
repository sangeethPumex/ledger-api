package categories

import "testing"

func TestValidIcon(t *testing.T) {
	for _, k := range []string{"", "ForkKnife", "Car2"} {
		if !ValidIcon(k) {
			t.Errorf("%q rejected", k)
		}
	}
	for _, k := range []string{"fork knife", "<script>", "a/b", "ééé", string(make([]byte, 41))} {
		if ValidIcon(k) {
			t.Errorf("%q accepted", k)
		}
	}
}
