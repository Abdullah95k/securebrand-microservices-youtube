package capacity

import "testing"

func TestStretchFor(t *testing.T) {
	for _, c := range []struct {
		name                                       string
		supply, fixed, stretchable, current, floor float64
		want                                       float64
	}{
		{"fits without a stretch", 1000, 200, 500, 1, 0, 1},
		{"needs 2", 1000, 200, 1600, 2, 0, 2},
		{"one step up at most", 1000, 200, 1600, 1, 0, 1.5},
		{"one step down at most", 1000, 200, 100, 3, 0, 2},
		{"fixed work fills the IPs", 1000, 1000, 500, 4, 0, 6},
		{"never past the cap", 1000, 1200, 500, 7, 0, MaxStretch},
		{"the floor at once", 1000, 0, 10, 1, 3, 3},
		{"no supply: kept", 0, 0, 1600, 2.5, 0, 2.5},
		{"no supply: the floor still holds", 0, 0, 1600, 1, 2, 2},
	} {
		if got := stretchFor(c.supply, c.fixed, c.stretchable, c.current, c.floor); got != c.want {
			t.Errorf("%s: stretchFor = %v; want %v", c.name, got, c.want)
		}
	}
}
