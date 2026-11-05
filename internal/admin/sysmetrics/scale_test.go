package sysmetrics

import "testing"

// A feed server mostly sends kilobits: from 1 Mb/s up, its chart was flat.
func TestScaleStartsAt10Kbps(t *testing.T) {
	for peak, want := range map[float64]float64{0: 0.01, 0.0367: 0.05, 0.2: 0.2, 0.9: 1, 1.8: 2, 2.4: 5, 120: 200} {
		if got := scale(peak); got != want {
			t.Errorf("scale(%v) = %v, want %v", peak, got, want)
		}
	}
}
