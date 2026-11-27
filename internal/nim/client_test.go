package nim

import (
	"testing"
	"time"
)

func TestGapFromEnv(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "2": 2 * time.Second, "0.5": 500 * time.Millisecond, "0": 0, "-1": 0, "x": 0, "NaN": 0, "Inf": 0} {
		t.Setenv("GAP", in)
		if got := GapFromEnv("GAP"); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}
