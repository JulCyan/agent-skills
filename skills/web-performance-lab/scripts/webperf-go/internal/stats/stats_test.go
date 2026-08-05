package stats

import (
	"reflect"
	"testing"
)

func TestSummarizeUsesMedianMADAndIQR(t *testing.T) {
	got := Summarize([]float64{1, 2, 3, 4, 100})
	if got.Median != 3 || got.MAD != 1 || got.IQR != 2 || got.Min != 1 || got.Max != 100 {
		t.Fatalf("distribution=%+v", got)
	}
}

func TestSummarizeHandlesDistributionSizesWithoutMutatingInput(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want Distribution
	}{
		{name: "empty", in: nil, want: Distribution{}},
		{name: "single", in: []float64{7}, want: Distribution{Count: 1, Median: 7, MAD: 0, IQR: 0, Min: 7, Max: 7}},
		{name: "even", in: []float64{4, 1, 3, 2}, want: Distribution{Count: 4, Median: 2.5, MAD: 1, IQR: 2, Min: 1, Max: 4}},
		{name: "three values", in: []float64{9, 1, 5}, want: Distribution{Count: 3, Median: 5, MAD: 4, IQR: 4, Min: 1, Max: 9}},
		{name: "five values", in: []float64{100, 4, 3, 2, 1}, want: Distribution{Count: 5, Median: 3, MAD: 1, IQR: 2, Min: 1, Max: 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]float64(nil), tc.in...)
			if got := Summarize(tc.in); got != tc.want {
				t.Fatalf("distribution=%+v want=%+v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.in, before) {
				t.Fatalf("input mutated: got=%v want=%v", tc.in, before)
			}
		})
	}
}
