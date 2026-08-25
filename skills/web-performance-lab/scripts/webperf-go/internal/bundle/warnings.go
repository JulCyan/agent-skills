package bundle

import (
	"sort"
	"strconv"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

// WarningSignals holds facts which are observed only while descriptor-bound
// Lighthouse reports are read. Its values are transient and must never be
// rendered into a summary because raw final URLs may contain sensitive data.
type WarningSignals struct {
	InvalidFinalURL           bool
	RawFinalURLs              []string
	LighthouseWarningAttempts []int
}

// SummarizeSamples computes every persisted distribution from the successful
// Lighthouse samples. Collection and evidence validation share this function
// so warning thresholds cannot silently drift from the stored aggregate.
func SummarizeSamples(samples []SuccessfulSample) Metrics {
	values := summaryValues(samples)
	return Metrics{
		PerformanceScore: stats.Summarize(values.score),
		FCP:              stats.Summarize(values.fcp),
		LCP:              stats.Summarize(values.lcp),
		SpeedIndex:       stats.Summarize(values.speedIndex),
		TBT:              stats.Summarize(values.tbt),
		CLS:              stats.Summarize(values.cls),
		BenchmarkIndex:   stats.Summarize(values.benchmarkIndex),
	}
}

// CanonicalWarnings is the single warning policy for both collection and
// verification. It derives a sorted, de-duplicated warning set only from the
// immutable attempt ledger, sanitized samples, recomputed metrics, and raw
// report facts gathered through bound descriptors.
func CanonicalWarnings(attempts []Attempt, samples []SuccessfulSample, signals WarningSignals) []string {
	warnings := make([]string, 0)
	add := func(warning string) {
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}

	hasFailure := false
	for _, attempt := range attempts {
		if attempt.CleanupFailed {
			add("temporary browser cleanup failed")
		}
		switch attempt.Status {
		case "ENGINE_FAILED", "PARSE_FAILED", "INTERRUPTED":
			hasFailure = true
		}
	}
	if hasFailure {
		add("one or more attempts failed")
	}
	if signals.InvalidFinalURL {
		add("Lighthouse reported an invalid final URL")
	}
	for _, attempt := range signals.LighthouseWarningAttempts {
		if attempt > 0 {
			add("Lighthouse reported warnings on attempt " + strconv.Itoa(attempt))
		}
	}

	if len(samples) > 0 {
		finalURLs := make(map[string]struct{}, len(samples))
		selectors := make(map[string]struct{}, len(samples))
		for _, item := range samples {
			finalURLs[item.Sample.FinalURL] = struct{}{}
			selector := item.Sample.LCPSelector
			if selector == "" {
				selector = "<missing>"
			}
			selectors[selector] = struct{}{}
		}
		if len(uniqueStrings(signals.RawFinalURLs)) > 1 || len(finalURLs) > 1 {
			add("final URL changed across successful samples")
		}
		if len(selectors) > 1 {
			add("LCP selector changed across successful samples")
		}

		metrics := SummarizeSamples(samples)
		for _, item := range []struct {
			name  string
			data  stats.Distribution
			floor float64
		}{
			{name: "FCP", data: metrics.FCP, floor: 100},
			{name: "LCP", data: metrics.LCP, floor: 100},
			{name: "Speed Index", data: metrics.SpeedIndex, floor: 100},
			{name: "TBT", data: metrics.TBT, floor: 50},
		} {
			if item.data.IQR >= max(item.floor, 0.10*item.data.Median) {
				add("high dispersion for " + item.name)
			}
		}
		if metrics.PerformanceScore.IQR >= 5 {
			add("high dispersion for Performance Score")
		}
		if metrics.CLS.IQR >= .02 {
			add("high dispersion for CLS")
		}
	}

	return uniqueStrings(warnings)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	unique := copyValues[:0]
	for _, value := range copyValues {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}
