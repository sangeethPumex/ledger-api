package profile

import (
	"testing"
	"time"
)

func TestStampPrior(t *testing.T) {
	oct := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	nov := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	got := stampPrior(Prior{}, Prior{Spent: 40000, YM: "1999-01"}, oct)
	if got.YM != "2026-10" {
		t.Errorf("new amounts stamped %q", got.YM)
	}
	if got := stampPrior(got, Prior{Spent: 40000}, nov); got.YM != "2026-10" {
		t.Errorf("unchanged amounts re-stamped %q", got.YM)
	}
	if got := stampPrior(got, Prior{Spent: 100}, nov); got.YM != "2026-11" {
		t.Errorf("changed amounts kept %q", got.YM)
	}
}
