package compare

import (
	"errors"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

func TestBundlesRejectsFingerprintMismatch(t *testing.T) {
	baselineProtocol := protocol()
	candidateProtocol := protocol()
	candidateProtocol.ChromeVersion = "151.0.0.0"
	candidateProtocol = bundle.CompleteProtocol(candidateProtocol)
	_, err := Bundles(summary(1000, 10), summary(900, 10), baselineProtocol, candidateProtocol)
	if !errors.Is(err, ErrIncompatibleProtocol) {
		t.Fatalf("err=%v", err)
	}
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) || len(incompatible.Fields) != 2 || incompatible.Fields[0] != "chromeVersion" || incompatible.Fields[1] != "fingerprint" {
		t.Fatalf("incompatible=%+v", incompatible)
	}
}

func TestBundlesRejectsPartialAggregate(t *testing.T) {
	partial := summary(1000, 10)
	partial.Status = "PARTIAL"
	_, err := Bundles(partial, summary(900, 10), protocol(), protocol())
	if !errors.Is(err, ErrIncompleteBundle) {
		t.Fatalf("err=%v", err)
	}
}

func TestTimingUsesGreaterOfTenPercentAndTwiceMAD(t *testing.T) {
	got := classifyLowerBetter(1000, 880, 20, 30, timingPolicy)
	if got.Classification != Improvement || got.MaterialityFloor != 100 {
		t.Fatalf("result=%+v", got)
	}
}

func TestTimingBoundaryIsNoMaterialChange(t *testing.T) {
	got := classifyLowerBetter(1000, 900, 0, 0, timingPolicy)
	if got.Classification != NoMaterialChange || got.MaterialityFloor != 100 {
		t.Fatalf("result=%+v", got)
	}
}

func TestBundlesClassifiesEachMetricWithoutSynthesizingScore(t *testing.T) {
	baseline := summary(1000, 10)
	candidate := summary(800, 10)
	candidate = summaryWithValues(800, 80, 0.2)
	report, err := Bundles(baseline, candidate, protocol(), protocol())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "OK" || report.Overall != Mixed || len(report.Metrics) != 6 {
		t.Fatalf("report=%+v", report)
	}
	for _, metric := range report.Metrics {
		if metric.Name == "performanceScore" && metric.Classification != Improvement {
			t.Fatalf("score=%+v", metric)
		}
	}
}

func TestBundlesReportsImprovementOnlyWhenNoMetricRegresses(t *testing.T) {
	baseline := summary(1000, 10)
	candidate := summary(800, 10)
	candidate = summaryWithValues(800, 80, 0.05)
	report, err := Bundles(baseline, candidate, protocol(), protocol())
	if err != nil {
		t.Fatal(err)
	}
	if report.Overall != Improvement {
		t.Fatalf("overall=%s metrics=%+v", report.Overall, report.Metrics)
	}
}

func TestBundlesRejectsProtocolWithStaleFingerprint(t *testing.T) {
	broken := protocol()
	broken.Fingerprint = "stale"
	_, err := Bundles(summary(1000, 10), summary(900, 10), broken, protocol())
	if !errors.Is(err, ErrIncompatibleProtocol) {
		t.Fatalf("err=%v", err)
	}
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) || len(incompatible.Fields) != 1 || incompatible.Fields[0] != "fingerprint" {
		t.Fatalf("incompatible=%+v", incompatible)
	}
}

func TestBundlesRejectsSelfFingerprintedUncompiledProtocol(t *testing.T) {
	broken := protocol()
	broken.RuntimeFlags = []string{"--output=json"}
	broken = bundle.CompleteProtocol(broken)
	_, err := Bundles(summary(1000, 10), summary(900, 10), broken, protocol())
	if !errors.Is(err, ErrIncompatibleProtocol) {
		t.Fatalf("err=%v", err)
	}
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) || len(incompatible.Fields) != 1 || incompatible.Fields[0] != "compiledProtocol" {
		t.Fatalf("incompatible=%+v", incompatible)
	}
}

func TestBundlesRejectsForgedPersistedMetrics(t *testing.T) {
	forged := summary(1000, 10)
	forged.Metrics.LCP.Median = 1
	_, err := Bundles(forged, summary(900, 10), protocol(), protocol())
	if !errors.Is(err, ErrIncompleteBundle) {
		t.Fatalf("err=%v", err)
	}
}

func TestBundlesRejectsForgedBenchmarkDistribution(t *testing.T) {
	forged := summary(1000, 10)
	forged.Metrics.BenchmarkIndex.Median = 2
	_, err := Bundles(forged, summary(900, 10), protocol(), protocol())
	if !errors.Is(err, ErrIncompleteBundle) {
		t.Fatalf("err=%v", err)
	}
}

func TestBundlesRejectsSummaryProfileMismatchedToProtocol(t *testing.T) {
	baseline := summary(1000, 10)
	baseline.Profile = "mobile-lab-v1"
	_, err := Bundles(baseline, summary(900, 10), protocol(), protocol())
	if !errors.Is(err, ErrIncompatibleProtocol) {
		t.Fatalf("err=%v", err)
	}
}

func summary(lcp, mad float64) bundle.Summary {
	return summaryWithValues(lcp, 70, 0.1)
}

func summaryWithValues(lcp, score, cls float64) bundle.Summary {
	samples := []bundle.SuccessfulSample{
		{Attempt: 1, Sample: sample(lcp, score, cls)},
		{Attempt: 2, Sample: sample(lcp, score, cls)},
		{Attempt: 3, Sample: sample(lcp, score, cls)},
		{Attempt: 4, Sample: sample(lcp, score, cls)},
		{Attempt: 5, Sample: sample(lcp, score, cls)},
	}
	return bundle.Summary{
		SchemaVersion:  1,
		Status:         "OK",
		Profile:        "desktop-lab-v1",
		RequestedRuns:  5,
		SuccessfulRuns: 5,
		Samples:        samples,
		Metrics:        metricsFor(samples),
	}
}

func sample(value, score, cls float64) lhr.Sample {
	return lhr.Sample{PerformanceScore: score, FCP: value, LCP: value, SpeedIndex: value, TBT: value, CLS: cls, BenchmarkIndex: 1}
}

func metricsFor(samples []bundle.SuccessfulSample) bundle.Metrics {
	values := make([]float64, len(samples))
	scores := make([]float64, len(samples))
	classes := make([]float64, len(samples))
	for index, item := range samples {
		values[index] = item.Sample.LCP
		scores[index] = item.Sample.PerformanceScore
		classes[index] = item.Sample.CLS
	}
	return bundle.Metrics{
		PerformanceScore: stats.Summarize(scores),
		FCP:              stats.Summarize(values),
		LCP:              stats.Summarize(values),
		SpeedIndex:       stats.Summarize(values),
		TBT:              stats.Summarize(values),
		CLS:              stats.Summarize(classes),
		BenchmarkIndex:   stats.Summarize([]float64{1, 1, 1, 1, 1}),
	}
}

func protocol() bundle.Protocol {
	return bundle.CompleteProtocol(bundle.Protocol{
		SchemaVersion:     1,
		Profile:           "desktop-lab-v1",
		FormFactor:        "desktop",
		ThrottlingMethod:  "simulate",
		ResolvedFlags:     []string{"--preset=desktop", "--throttling-method=simulate"},
		RuntimeFlags:      bundle.ExpectedRuntimeFlags(),
		LighthouseVersion: "13.4.1",
		NodeVersion:       "24.16.0",
		ChromeVersion:     "150.0.0.0",
		OS:                "darwin",
		Arch:              "arm64",
	})
}
