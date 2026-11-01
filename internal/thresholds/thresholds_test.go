package thresholds

import "testing"

func TestTSBStateForBoundaries(t *testing.T) {
	for _, tt := range []struct {
		tsb  float64
		want TSBState
	}{
		{10.1, Fresh},
		{10, Norm},
		{0, Norm},
		{-10, Norm},
		{-10.1, Loaded},
		{-24.9, Loaded},
		{-25, Loaded},
		{-25.1, Overloaded},
	} {
		if got := TSBStateFor(tt.tsb); got != tt.want {
			t.Errorf("TSBStateFor(%v) = %q, хочу %q", tt.tsb, got, tt.want)
		}
	}
}

func TestLowData(t *testing.T) {
	for _, tt := range []struct {
		dataDays, loadDays int
		want               bool
	}{
		{21, 9, false},
		{20, 9, true},
		{21, 8, true},
		{42, 21, false},
		{0, 0, true},
	} {
		if got := LowData(tt.dataDays, tt.loadDays); got != tt.want {
			t.Errorf("LowData(%d, %d) = %v, хочу %v", tt.dataDays, tt.loadDays, got, tt.want)
		}
	}
}
