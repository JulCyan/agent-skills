// Package stats provides deterministic summaries for independently collected samples.
package stats

import "sort"

// Distribution is calculated only from the supplied values. It does not contain
// a synthetic score assembled from other metric distributions.
type Distribution struct {
	Count  int     `json:"count"`
	Median float64 `json:"median"`
	MAD    float64 `json:"mad"`
	IQR    float64 `json:"iqr"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// Summarize returns median, median absolute deviation, Tukey hinges IQR, minimum, and
// maximum. It copies input before sorting so callers retain their sample order.
func Summarize(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	median := medianOf(sorted)
	deviations := make([]float64, len(sorted))
	for index, value := range sorted {
		deviations[index] = abs(value - median)
	}
	sort.Float64s(deviations)

	q1, q3 := tukeyHinges(sorted)
	return Distribution{
		Count:  len(sorted),
		Median: median,
		MAD:    medianOf(deviations),
		IQR:    q3 - q1,
		Min:    sorted[0],
		Max:    sorted[len(sorted)-1],
	}
}

func tukeyHinges(values []float64) (float64, float64) {
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return medianOf(values[:middle+1]), medianOf(values[middle:])
	}
	return medianOf(values[:middle]), medianOf(values[middle:])
}

func medianOf(values []float64) float64 {
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
