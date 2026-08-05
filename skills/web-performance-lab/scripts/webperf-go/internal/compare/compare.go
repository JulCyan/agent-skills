// Package compare evaluates two finalized evidence summaries under one exact
// measurement protocol. It deliberately never reconstructs a Lighthouse score.
package compare

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

var (
	// ErrIncompatibleProtocol means one or both bundles cannot be safely compared.
	ErrIncompatibleProtocol = errors.New("incompatible measurement protocol")
	// ErrIncompleteBundle means a bundle has no finalized aggregate suitable for comparison.
	ErrIncompleteBundle = errors.New("incomplete evidence bundle")
)

type Classification string

const (
	Improvement      Classification = "improvement"
	NoMaterialChange Classification = "no_material_change"
	Regression       Classification = "regression"
	Mixed            Classification = "mixed"
	Inconclusive     Classification = "inconclusive"
)

// Metric holds one independently evaluated Lighthouse result distribution.
type Metric struct {
	Name             string         `json:"name"`
	BaselineMedian   float64        `json:"baselineMedian"`
	CandidateMedian  float64        `json:"candidateMedian"`
	BaselineMAD      float64        `json:"baselineMad"`
	CandidateMAD     float64        `json:"candidateMad"`
	Delta            float64        `json:"delta"`
	MaterialityFloor float64        `json:"materialityFloor"`
	Classification   Classification `json:"classification"`
}

// Report is an analysis result. A regression is a successful result; callers
// use Overall and each independent Metric rather than an exit code.
type Report struct {
	Status  string         `json:"status"`
	Overall Classification `json:"overall"`
	Metrics []Metric       `json:"metrics"`
}

// IncompatibleError contains only stable field labels, never artifact paths or
// caller-provided URLs.
type IncompatibleError struct {
	Fields []string
}

func (e *IncompatibleError) Error() string {
	if len(e.Fields) == 0 {
		return ErrIncompatibleProtocol.Error()
	}
	return fmt.Sprintf("%s: %v", ErrIncompatibleProtocol, e.Fields)
}

func (e *IncompatibleError) Is(target error) bool { return target == ErrIncompatibleProtocol }

type materialityPolicy struct {
	staticFloor float64
	relative    bool
}

var (
	timingPolicy = materialityPolicy{relative: true}
	scorePolicy  = materialityPolicy{staticFloor: 5}
	clsPolicy    = materialityPolicy{staticFloor: 0.02}
)

// Bundles compares two strictly compatible, finalized aggregate summaries.
func Bundles(baseline, candidate bundle.Summary, baselineProtocol, candidateProtocol bundle.Protocol) (Report, error) {
	if err := validateBundle(baseline, baselineProtocol); err != nil {
		return Report{}, err
	}
	if err := validateBundle(candidate, candidateProtocol); err != nil {
		return Report{}, err
	}
	if err := compatibleProtocols(baselineProtocol, candidateProtocol); err != nil {
		return Report{}, err
	}

	metrics := []struct {
		name        string
		baseline    stats.Distribution
		candidate   stats.Distribution
		policy      materialityPolicy
		lowerBetter bool
	}{
		{name: "performanceScore", baseline: baseline.Metrics.PerformanceScore, candidate: candidate.Metrics.PerformanceScore, policy: scorePolicy},
		{name: "fcp", baseline: baseline.Metrics.FCP, candidate: candidate.Metrics.FCP, policy: timingPolicy, lowerBetter: true},
		{name: "lcp", baseline: baseline.Metrics.LCP, candidate: candidate.Metrics.LCP, policy: timingPolicy, lowerBetter: true},
		{name: "speedIndex", baseline: baseline.Metrics.SpeedIndex, candidate: candidate.Metrics.SpeedIndex, policy: timingPolicy, lowerBetter: true},
		{name: "tbt", baseline: baseline.Metrics.TBT, candidate: candidate.Metrics.TBT, policy: timingPolicy, lowerBetter: true},
		{name: "cls", baseline: baseline.Metrics.CLS, candidate: candidate.Metrics.CLS, policy: clsPolicy, lowerBetter: true},
	}

	report := Report{Status: "OK", Metrics: make([]Metric, 0, len(metrics))}
	for _, item := range metrics {
		var result Metric
		if item.lowerBetter {
			result = classifyLowerBetter(item.baseline.Median, item.candidate.Median, item.baseline.MAD, item.candidate.MAD, item.policy)
		} else {
			result = classifyHigherBetter(item.baseline.Median, item.candidate.Median, item.baseline.MAD, item.candidate.MAD, item.policy)
		}
		result.Name = item.name
		report.Metrics = append(report.Metrics, result)
	}
	report.Overall = overall(report.Metrics)
	return report, nil
}

func validateBundle(summary bundle.Summary, protocol bundle.Protocol) error {
	if err := validateSummary(summary); err != nil {
		return err
	}
	if fields := invalidProtocolFields(protocol); len(fields) > 0 {
		return &IncompatibleError{Fields: fields}
	}
	if summary.Profile != protocol.Profile {
		return &IncompatibleError{Fields: []string{"profile"}}
	}
	return nil
}

func validateSummary(summary bundle.Summary) error {
	if summary.SchemaVersion != 1 || summary.Status != "OK" || summary.SuccessfulRuns < 3 || summary.SuccessfulRuns != len(summary.Samples) || summary.RequestedRuns < summary.SuccessfulRuns {
		return ErrIncompleteBundle
	}
	if summary.Profile == "" {
		return ErrIncompleteBundle
	}
	metrics := []struct {
		actual stats.Distribution
		values []float64
		score  bool
	}{
		{actual: summary.Metrics.PerformanceScore, values: sampleValues(summary.Samples, func(sample bundle.SuccessfulSample) float64 { return sample.Sample.PerformanceScore }), score: true},
		{actual: summary.Metrics.FCP, values: sampleValues(summary.Samples, func(sample bundle.SuccessfulSample) float64 { return sample.Sample.FCP })},
		{actual: summary.Metrics.LCP, values: sampleValues(summary.Samples, func(sample bundle.SuccessfulSample) float64 { return sample.Sample.LCP })},
		{actual: summary.Metrics.SpeedIndex, values: sampleValues(summary.Samples, func(sample bundle.SuccessfulSample) float64 { return sample.Sample.SpeedIndex })},
		{actual: summary.Metrics.TBT, values: sampleValues(summary.Samples, func(sample bundle.SuccessfulSample) float64 { return sample.Sample.TBT })},
		{actual: summary.Metrics.CLS, values: sampleValues(summary.Samples, func(sample bundle.SuccessfulSample) float64 { return sample.Sample.CLS })},
		{actual: summary.Metrics.BenchmarkIndex, values: sampleValues(summary.Samples, func(sample bundle.SuccessfulSample) float64 { return sample.Sample.BenchmarkIndex })},
	}
	for _, metric := range metrics {
		if metric.actual.Count != summary.SuccessfulRuns || !finiteDistribution(metric.actual) || !validMetricValues(metric.values, metric.score) || !sameDistribution(metric.actual, stats.Summarize(metric.values)) {
			return ErrIncompleteBundle
		}
	}
	return nil
}

func sampleValues(samples []bundle.SuccessfulSample, value func(bundle.SuccessfulSample) float64) []float64 {
	values := make([]float64, len(samples))
	for index, sample := range samples {
		values[index] = value(sample)
	}
	return values
}

func validMetricValues(values []float64, score bool) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || score && value > 100 {
			return false
		}
	}
	return true
}

func sameDistribution(left, right stats.Distribution) bool {
	return left.Count == right.Count && left.Min == right.Min && left.Max == right.Max && left.Median == right.Median && left.MAD == right.MAD && left.IQR == right.IQR
}

func invalidProtocolFields(protocol bundle.Protocol) []string {
	fields := make([]string, 0)
	if protocol.SchemaVersion != 1 {
		fields = append(fields, "schemaVersion")
	}
	if protocol.Profile == "" {
		fields = append(fields, "profile")
	}
	if protocol.FormFactor == "" {
		fields = append(fields, "formFactor")
	}
	if protocol.ThrottlingMethod == "" {
		fields = append(fields, "throttlingMethod")
	}
	if len(protocol.ResolvedFlags) == 0 {
		fields = append(fields, "resolvedFlags")
	}
	if len(protocol.RuntimeFlags) == 0 {
		fields = append(fields, "runtimeFlags")
	}
	if protocol.LighthouseVersion == "" {
		fields = append(fields, "lighthouseVersion")
	}
	if protocol.NodeVersion == "" {
		fields = append(fields, "nodeVersion")
	}
	if protocol.ChromeVersion == "" {
		fields = append(fields, "chromeVersion")
	}
	if protocol.OS == "" {
		fields = append(fields, "os")
	}
	if protocol.Arch == "" {
		fields = append(fields, "arch")
	}
	if protocol.Fingerprint == "" || protocol.Fingerprint != bundle.ProtocolFingerprint(protocol) {
		fields = append(fields, "fingerprint")
	}
	if err := bundle.ValidateProtocol(protocol); err != nil && len(fields) == 0 {
		fields = append(fields, "compiledProtocol")
	}
	sort.Strings(fields)
	return fields
}

func finiteDistribution(distribution stats.Distribution) bool {
	if distribution.Count < 3 || distribution.MAD < 0 || distribution.IQR < 0 || distribution.Min > distribution.Median || distribution.Median > distribution.Max {
		return false
	}
	for _, value := range []float64{
		distribution.Min,
		distribution.Max,
		distribution.Median,
		distribution.MAD,
		distribution.IQR,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func compatibleProtocols(baseline, candidate bundle.Protocol) error {
	fields := make([]string, 0)
	if baseline.SchemaVersion != candidate.SchemaVersion {
		fields = append(fields, "schemaVersion")
	}
	if baseline.Profile != candidate.Profile {
		fields = append(fields, "profile")
	}
	if baseline.FormFactor != candidate.FormFactor {
		fields = append(fields, "formFactor")
	}
	if baseline.ThrottlingMethod != candidate.ThrottlingMethod {
		fields = append(fields, "throttlingMethod")
	}
	if !equalStrings(baseline.ResolvedFlags, candidate.ResolvedFlags) {
		fields = append(fields, "resolvedFlags")
	}
	if !equalStrings(baseline.RuntimeFlags, candidate.RuntimeFlags) {
		fields = append(fields, "runtimeFlags")
	}
	if baseline.LighthouseVersion != candidate.LighthouseVersion {
		fields = append(fields, "lighthouseVersion")
	}
	if baseline.NodeVersion != candidate.NodeVersion {
		fields = append(fields, "nodeVersion")
	}
	if baseline.ChromeVersion != candidate.ChromeVersion {
		fields = append(fields, "chromeVersion")
	}
	if baseline.OS != candidate.OS {
		fields = append(fields, "os")
	}
	if baseline.Arch != candidate.Arch {
		fields = append(fields, "arch")
	}
	if baseline.Fingerprint != candidate.Fingerprint {
		fields = append(fields, "fingerprint")
	}
	if len(fields) > 0 {
		sort.Strings(fields)
		return &IncompatibleError{Fields: fields}
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func classifyLowerBetter(baseline, candidate, baselineMAD, candidateMAD float64, policy materialityPolicy) Metric {
	floor := materialityFloor(baseline, baselineMAD, candidateMAD, policy)
	result := Metric{BaselineMedian: baseline, CandidateMedian: candidate, BaselineMAD: baselineMAD, CandidateMAD: candidateMAD, Delta: candidate - baseline, MaterialityFloor: floor, Classification: NoMaterialChange}
	if result.Delta < -floor {
		result.Classification = Improvement
	}
	if result.Delta > floor {
		result.Classification = Regression
	}
	return result
}

func classifyHigherBetter(baseline, candidate, baselineMAD, candidateMAD float64, policy materialityPolicy) Metric {
	floor := materialityFloor(baseline, baselineMAD, candidateMAD, policy)
	result := Metric{BaselineMedian: baseline, CandidateMedian: candidate, BaselineMAD: baselineMAD, CandidateMAD: candidateMAD, Delta: candidate - baseline, MaterialityFloor: floor, Classification: NoMaterialChange}
	if result.Delta > floor {
		result.Classification = Improvement
	}
	if result.Delta < -floor {
		result.Classification = Regression
	}
	return result
}

func materialityFloor(baseline, baselineMAD, candidateMAD float64, policy materialityPolicy) float64 {
	floor := policy.staticFloor
	if policy.relative {
		floor = math.Max(floor, 0.1*math.Abs(baseline))
	}
	return math.Max(floor, 2*math.Max(baselineMAD, candidateMAD))
}

func overall(metrics []Metric) Classification {
	hasImprovement := false
	hasRegression := false
	for _, metric := range metrics {
		switch metric.Classification {
		case Regression:
			hasRegression = true
		case Inconclusive:
			return Inconclusive
		case Improvement:
			hasImprovement = true
		}
	}
	if hasImprovement && hasRegression {
		return Mixed
	}
	if hasRegression {
		return Regression
	}
	if hasImprovement {
		return Improvement
	}
	return NoMaterialChange
}
