package connector

import "testing"

func TestDecideHeal(t *testing.T) {
	cases := []struct {
		name             string
		partExists       bool
		isFake           bool
		confirmedMissing bool
		want             bool
	}{
		{"no part — normal delivery handles it", false, false, false, false},
		{"no part even if flags set", false, true, true, false},
		{"healthy: real mxid, event present", true, false, false, false},
		{"fake mxid — never really sent", true, true, false, true},
		{"real mxid but event confirmed missing", true, false, true, true},
		{"fake and missing", true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decideHeal(c.partExists, c.isFake, c.confirmedMissing); got != c.want {
				t.Errorf("decideHeal(%v,%v,%v)=%v want %v", c.partExists, c.isFake, c.confirmedMissing, got, c.want)
			}
		})
	}
}
