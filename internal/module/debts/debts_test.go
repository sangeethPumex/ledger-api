package debts

import "testing"

func TestClean(t *testing.T) {
	for name, tc := range map[string]struct {
		in   Input
		want string
	}{
		"ok":         {Input{" Ravi ", "Goa trip", "GPay", "", 50000}, ""},
		"no person":  {Input{"", "", "GPay", "", 100}, "person and source are required"},
		"no source":  {Input{"Ravi", "", "", "", 100}, "person and source are required"},
		"zero":       {Input{"Ravi", "", "Cash", "", 0}, "amount must be above zero"},
		"negative":   {Input{"Ravi", "", "Cash", "", -5}, "amount must be above zero"},
		"long group": {Input{"Ravi", string(make([]byte, 61)), "GPay", "", 100}, "text is too long"},
	} {
		if got := Clean(&tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
