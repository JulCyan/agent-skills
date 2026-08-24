// Package report renders verified webperf evidence as deterministic, offline
// HTML. Reports are derived artifacts and are never written into evidence
// bundles.
package report

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	webcompare "github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/compare"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

var (
	ErrInvalidEvidence      = errors.New("invalid evidence for report")
	ErrAggregateUnavailable = errors.New("aggregate unavailable for report")
	ErrIncompleteBundle     = errors.New("complete evidence required for comparison report")
	ErrDuplicateProfile     = errors.New("duplicate report profile")
	ErrDifferentTarget      = errors.New("single report requires one requested URL")
	ErrAmbiguousTarget      = errors.New("multiple-profile target identity is ambiguous after query redaction")
)

const (
	KindSingle     = "single"
	KindComparison = "comparison"
)

type Document struct {
	Locale         Locale
	Messages       Messages
	Kind           string
	Title          string
	ReportClass    string
	EvidenceStatus string
	TargetURL      string
	StartedAt      string
	FinishedAt     string
	Runs           []Run
	Comparison     *Comparison
	Notice         string
	CannotClaims   []string
}

type Run struct {
	ID                 string
	Role               string
	EvidenceStatus     string
	RequestedURL       string
	FinalURL           string
	StartedAt          string
	FinishedAt         string
	Profile            string
	FormFactor         string
	ThrottlingMethod   string
	RequestedRuns      int
	SuccessfulRuns     int
	Metrics            []Metric
	Attempts           []Attempt
	Warnings           []string
	Representative     Representative
	Protocol           Protocol
	HasPartialEvidence bool
}

type Metric struct {
	Key            string
	Label          string
	Unit           string
	Distribution   stats.Distribution
	Points         []Point
	MedianPosition float64
}

type Point struct {
	Attempt  int
	Value    float64
	Position float64
}

type Attempt struct {
	Number     int
	Status     string
	StartedAt  string
	FinishedAt string
	Sample     *Sample
}

type Sample struct {
	PerformanceScore float64
	FCP              float64
	LCP              float64
	SpeedIndex       float64
	TBT              float64
	CLS              float64
	BenchmarkIndex   float64
}

type Representative struct {
	Attempt       int
	LCP           float64
	LCPBreakdown  []LCPPhase
	Opportunities []Opportunity
	Signals       []DiagnosticSignal
}

type Protocol struct {
	Fingerprint       string
	LighthouseVersion string
	NodeVersion       string
	ChromeVersion     string
	OS                string
	Arch              string
	ResolvedFlags     []string
	RuntimeFlags      []string
}

type Comparison struct {
	Overall string
	Metrics []ComparisonMetric
}

type ComparisonMetric struct {
	Key              string
	Label            string
	Unit             string
	Baseline         stats.Distribution
	Candidate        stats.Distribution
	Delta            float64
	MaterialityFloor float64
	Classification   string
}

type loadedRun struct {
	Run      Run
	Summary  bundle.Summary
	Protocol bundle.Protocol
}

type metricDefinition struct {
	key          string
	label        string
	unit         string
	value        func(lhr.Sample) float64
	distribution func(bundle.Metrics) stats.Distribution
}

var metricDefinitions = []metricDefinition{
	{
		key: "performanceScore", label: "Performance score", unit: "0–100",
		value:        func(sample lhr.Sample) float64 { return sample.PerformanceScore },
		distribution: func(metrics bundle.Metrics) stats.Distribution { return metrics.PerformanceScore },
	},
	{
		key: "fcp", label: "First Contentful Paint", unit: "time",
		value:        func(sample lhr.Sample) float64 { return sample.FCP },
		distribution: func(metrics bundle.Metrics) stats.Distribution { return metrics.FCP },
	},
	{
		key: "lcp", label: "Largest Contentful Paint", unit: "time",
		value:        func(sample lhr.Sample) float64 { return sample.LCP },
		distribution: func(metrics bundle.Metrics) stats.Distribution { return metrics.LCP },
	},
	{
		key: "speedIndex", label: "Speed Index", unit: "time",
		value:        func(sample lhr.Sample) float64 { return sample.SpeedIndex },
		distribution: func(metrics bundle.Metrics) stats.Distribution { return metrics.SpeedIndex },
	},
	{
		key: "tbt", label: "Total Blocking Time", unit: "time",
		value:        func(sample lhr.Sample) float64 { return sample.TBT },
		distribution: func(metrics bundle.Metrics) stats.Distribution { return metrics.TBT },
	},
	{
		key: "cls", label: "Cumulative Layout Shift", unit: "ratio",
		value:        func(sample lhr.Sample) float64 { return sample.CLS },
		distribution: func(metrics bundle.Metrics) stats.Distribution { return metrics.CLS },
	},
}

func BuildSingle(paths []string) (Document, error) {
	if len(paths) == 0 {
		return Document{}, ErrAggregateUnavailable
	}
	runs := make([]Run, 0, len(paths))
	profiles := make(map[string]struct{}, len(paths))
	targetURL := ""
	partial := false
	for _, path := range paths {
		loaded, err := loadRun(path, true)
		if err != nil {
			return Document{}, err
		}
		run := loaded.Run
		if _, exists := profiles[run.Profile]; exists {
			return Document{}, fmt.Errorf("%w: %s", ErrDuplicateProfile, run.Profile)
		}
		profiles[run.Profile] = struct{}{}
		if targetURL == "" {
			targetURL = run.RequestedURL
		} else if targetURL != run.RequestedURL {
			return Document{}, ErrDifferentTarget
		}
		partial = partial || run.HasPartialEvidence
		runs = append(runs, run)
	}
	if len(runs) > 1 && requestedURLHasQuery(targetURL) {
		return Document{}, ErrAmbiguousTarget
	}
	sort.Slice(runs, func(left, right int) bool { return runs[left].Profile < runs[right].Profile })
	for index := range runs {
		runs[index].ID = "profile-" + safeID(runs[index].Profile)
	}
	startedAt, finishedAt := evidenceWindow(runs)
	document := Document{
		Kind:           KindSingle,
		Title:          "Web Performance Evidence Report",
		ReportClass:    "VERIFIED LAB EVIDENCE",
		EvidenceStatus: "OK",
		TargetURL:      targetURL,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		Runs:           runs,
		CannotClaims: []string{
			"Field performance from CrUX, RUM, or PageSpeed Insights",
			"Real-user INP or interaction quality",
			"Production deployment state or causal attribution",
			"Equivalence between different measurement profiles",
		},
	}
	if partial {
		document.ReportClass = "DIAGNOSTIC ONLY"
		document.EvidenceStatus = "PARTIAL"
		document.Notice = "This verified PARTIAL bundle is useful for diagnosis, but it is not release acceptance."
		document.CannotClaims = append([]string{"Release acceptance or a complete successful collection"}, document.CannotClaims...)
	}
	return document, nil
}

func requestedURLHasQuery(value string) bool {
	parsed, err := url.Parse(value)
	return err != nil || parsed.RawQuery != "" || parsed.ForceQuery
}

func BuildComparison(baselinePath, candidatePath string) (Document, error) {
	baselineLoaded, err := loadRun(baselinePath, false)
	if err != nil {
		return Document{}, err
	}
	candidateLoaded, err := loadRun(candidatePath, false)
	if err != nil {
		return Document{}, err
	}
	compared, err := webcompare.Bundles(
		baselineLoaded.Summary,
		candidateLoaded.Summary,
		baselineLoaded.Protocol,
		candidateLoaded.Protocol,
	)
	if err != nil {
		return Document{}, err
	}

	baseline := baselineLoaded.Run
	candidate := candidateLoaded.Run
	baseline.ID = "baseline"
	baseline.Role = "Baseline"
	candidate.ID = "candidate"
	candidate.Role = "Candidate"
	metrics := make([]ComparisonMetric, 0, len(compared.Metrics))
	for _, result := range compared.Metrics {
		definition, ok := metricDefinitionFor(result.Name)
		if !ok {
			continue
		}
		metrics = append(metrics, ComparisonMetric{
			Key:              result.Name,
			Label:            definition.label,
			Unit:             definition.unit,
			Baseline:         definition.distribution(baselineLoaded.Summary.Metrics),
			Candidate:        definition.distribution(candidateLoaded.Summary.Metrics),
			Delta:            result.Delta,
			MaterialityFloor: result.MaterialityFloor,
			Classification:   string(result.Classification),
		})
	}
	runs := []Run{baseline, candidate}
	startedAt, finishedAt := evidenceWindow(runs)
	return Document{
		Kind:           KindComparison,
		Title:          "Web Performance Comparison Report",
		ReportClass:    "VERIFIED COMPARISON",
		EvidenceStatus: "OK",
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		Runs:           runs,
		Comparison: &Comparison{
			Overall: string(compared.Overall),
			Metrics: metrics,
		},
		CannotClaims: []string{
			"Field performance from CrUX, RUM, or PageSpeed Insights",
			"Real-user INP or interaction quality",
			"Production deployment state or proof that one code change caused the delta",
			"Comparisons outside the exact shared protocol fingerprint",
		},
	}, nil
}

func loadRun(path string, allowPartial bool) (loadedRun, error) {
	store, err := bundle.Open(path)
	if err != nil {
		return loadedRun{}, invalidEvidenceError(err)
	}
	summary, err := store.ValidateEvidence()
	if err != nil {
		return loadedRun{}, invalidEvidenceError(err)
	}
	if summary == nil {
		return loadedRun{}, ErrAggregateUnavailable
	}
	manifest := store.Manifest()
	if manifest.Status != "OK" && (!allowPartial || manifest.Status != "PARTIAL") {
		return loadedRun{}, ErrIncompleteBundle
	}
	if summary.SuccessfulRuns < 3 || summary.SuccessfulRuns != len(summary.Samples) {
		return loadedRun{}, ErrAggregateUnavailable
	}

	protocol := store.Protocol()
	samples := append([]bundle.SuccessfulSample(nil), summary.Samples...)
	sort.Slice(samples, func(left, right int) bool { return samples[left].Attempt < samples[right].Attempt })
	representative, err := representativeFor(store, manifest, *summary, samples)
	if err != nil {
		return loadedRun{}, err
	}

	return loadedRun{Run: Run{
		EvidenceStatus:     manifest.Status,
		RequestedURL:       summary.RequestedURL,
		FinalURL:           summary.FinalURL,
		StartedAt:          manifest.StartedAt,
		FinishedAt:         manifest.FinishedAt,
		Profile:            protocol.Profile,
		FormFactor:         protocol.FormFactor,
		ThrottlingMethod:   protocol.ThrottlingMethod,
		RequestedRuns:      summary.RequestedRuns,
		SuccessfulRuns:     summary.SuccessfulRuns,
		Metrics:            metricsFor(*summary, samples),
		Attempts:           attemptsFor(manifest, samples),
		Warnings:           append([]string(nil), summary.Warnings...),
		Representative:     representative,
		HasPartialEvidence: manifest.Status == "PARTIAL",
		Protocol: Protocol{
			Fingerprint:       protocol.Fingerprint,
			LighthouseVersion: protocol.LighthouseVersion,
			NodeVersion:       protocol.NodeVersion,
			ChromeVersion:     protocol.ChromeVersion,
			OS:                protocol.OS,
			Arch:              protocol.Arch,
			ResolvedFlags:     append([]string(nil), protocol.ResolvedFlags...),
			RuntimeFlags:      append([]string(nil), protocol.RuntimeFlags...),
		},
	}, Summary: *summary, Protocol: protocol}, nil
}

func invalidEvidenceError(cause error) error {
	return fmt.Errorf("%w: %v", ErrInvalidEvidence, cause)
}

func representativeFor(store *bundle.Store, manifest bundle.Manifest, summary bundle.Summary, samples []bundle.SuccessfulSample) (Representative, error) {
	if len(samples) == 0 {
		return Representative{}, ErrAggregateUnavailable
	}
	representative := samples[0]
	distance := math.Abs(representative.Sample.LCP - summary.Metrics.LCP.Median)
	for _, candidate := range samples[1:] {
		candidateDistance := math.Abs(candidate.Sample.LCP - summary.Metrics.LCP.Median)
		if candidateDistance < distance || candidateDistance == distance && candidate.Attempt < representative.Attempt {
			representative = candidate
			distance = candidateDistance
		}
	}
	artifactPath := ""
	for _, attempt := range manifest.Attempts {
		if attempt.Number == representative.Attempt {
			artifactPath = attempt.Artifact
			break
		}
	}
	if artifactPath == "" {
		return Representative{}, invalidEvidenceError(errors.New("representative artifact missing"))
	}
	raw, err := store.ReadArtifact(artifactPath)
	if err != nil {
		return Representative{}, invalidEvidenceError(err)
	}
	diagnostics, err := parseDiagnostics(raw)
	if err != nil {
		return Representative{}, invalidEvidenceError(err)
	}
	return Representative{
		Attempt:       representative.Attempt,
		LCP:           representative.Sample.LCP,
		LCPBreakdown:  append([]LCPPhase(nil), diagnostics.LCPBreakdown...),
		Opportunities: append([]Opportunity(nil), diagnostics.Opportunities...),
		Signals:       append([]DiagnosticSignal(nil), diagnostics.Signals...),
	}, nil
}

func metricsFor(summary bundle.Summary, samples []bundle.SuccessfulSample) []Metric {
	metrics := make([]Metric, 0, len(metricDefinitions))
	for _, definition := range metricDefinitions {
		distribution := definition.distribution(summary.Metrics)
		points := make([]Point, len(samples))
		for index, item := range samples {
			value := definition.value(item.Sample)
			points[index] = Point{Attempt: item.Attempt, Value: value, Position: distributionPosition(distribution, value)}
		}
		metrics = append(metrics, Metric{
			Key:            definition.key,
			Label:          definition.label,
			Unit:           definition.unit,
			Distribution:   distribution,
			Points:         points,
			MedianPosition: distributionPosition(distribution, distribution.Median),
		})
	}
	return metrics
}

func attemptsFor(manifest bundle.Manifest, samples []bundle.SuccessfulSample) []Attempt {
	byAttempt := make(map[int]lhr.Sample, len(samples))
	for _, item := range samples {
		byAttempt[item.Attempt] = item.Sample
	}
	attempts := make([]Attempt, 0, len(manifest.Attempts))
	for _, item := range manifest.Attempts {
		attempt := Attempt{Number: item.Number, Status: item.Status, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt}
		if sample, exists := byAttempt[item.Number]; exists {
			attempt.Sample = &Sample{
				PerformanceScore: sample.PerformanceScore,
				FCP:              sample.FCP,
				LCP:              sample.LCP,
				SpeedIndex:       sample.SpeedIndex,
				TBT:              sample.TBT,
				CLS:              sample.CLS,
				BenchmarkIndex:   sample.BenchmarkIndex,
			}
		}
		attempts = append(attempts, attempt)
	}
	return attempts
}

func distributionPosition(distribution stats.Distribution, value float64) float64 {
	if distribution.Max <= distribution.Min {
		return 50
	}
	position := 5 + 90*(value-distribution.Min)/(distribution.Max-distribution.Min)
	if position < 5 {
		return 5
	}
	if position > 95 {
		return 95
	}
	return position
}

func metricDefinitionFor(key string) (metricDefinition, bool) {
	for _, definition := range metricDefinitions {
		if definition.key == key {
			return definition, true
		}
	}
	return metricDefinition{}, false
}

func evidenceWindow(runs []Run) (string, string) {
	var startedAt time.Time
	var finishedAt time.Time
	for _, run := range runs {
		started, err := time.Parse(time.RFC3339Nano, run.StartedAt)
		if err == nil && (startedAt.IsZero() || started.Before(startedAt)) {
			startedAt = started
		}
		finished, err := time.Parse(time.RFC3339Nano, run.FinishedAt)
		if err == nil && (finishedAt.IsZero() || finished.After(finishedAt)) {
			finishedAt = finished
		}
	}
	return canonicalTimestamp(startedAt), canonicalTimestamp(finishedAt)
}

func canonicalTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func safeID(value string) string {
	var result strings.Builder
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			result.WriteRune(character)
		} else {
			result.WriteByte('-')
		}
	}
	return strings.Trim(result.String(), "-")
}
