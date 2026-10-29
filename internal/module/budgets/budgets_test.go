package budgets

import "testing"

func TestLevel(t *testing.T) {
	for _, c := range []struct {
		ratio float64
		want  string
	}{{0, LevelOK}, {0.79, LevelOK}, {0.8, LevelWarn}, {0.99, LevelWarn}, {1, LevelOver}, {2.5, LevelOver}} {
		if got := level(c.ratio); got != c.want {
			t.Errorf("level(%v) = %s, want %s", c.ratio, got, c.want)
		}
	}
}
