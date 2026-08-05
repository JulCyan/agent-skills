// Package collect runs a fixed number of sequential Lighthouse attempts and
// turns successful reports into an inspectable evidence bundle.
package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/safeurl"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

var (
	ErrIncomplete     = errors.New("fewer than three Lighthouse samples succeeded")
	ErrPartial        = errors.New("one or more Lighthouse attempts did not complete cleanly")
	ErrInvalidRequest = errors.New("invalid collection request")
)

const minimumSuccessfulSamples = 3

// Runner is the bounded engine capability required by collection. It is kept
// narrow so collection tests run without a browser or installed engine.
type Runner interface {
	Run(context.Context, engine.Request) engine.Result
}

// Request separates the navigation URL from its safe display form. URL is
// handed only to the engine; DisplayURL is the only requested URL written to
// manifest or summary files.
type Request struct {
	URL        string
	DisplayURL string
	Profile    profile.Profile
	Runs       int
	Out        string
	Protocol   bundle.Protocol
}

// Service owns one collection operation. Its zero value uses the filesystem
// for short-lived, per-attempt Chrome user-data directories.
type Service struct {
	Runner         Runner
	Now            func() time.Time
	MkdirTemp      func(string, string) (string, error)
	RemoveAll      func(string) error
	AfterArtifact  func()
	BeforeFinalize func()
}

// Run creates evidence before navigation, makes exactly the requested number
// of sequential attempts unless canceled, and never retries a failed attempt.
func (s Service) Run(ctx context.Context, request Request) (bundle.Summary, error) {
	if err := validateRequest(request); err != nil {
		return bundle.Summary{}, err
	}
	if s.Runner == nil {
		return bundle.Summary{}, fmt.Errorf("%w: runner is required", ErrInvalidRequest)
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	store, err := bundle.Create(request.Out, bundle.Manifest{
		SchemaVersion: 1,
		Status:        "RUNNING",
		RequestedURL:  request.DisplayURL,
		StartedAt:     now().UTC().Format(time.RFC3339Nano),
	}, request.Protocol)
	if err != nil {
		return bundle.Summary{}, fmt.Errorf("create evidence bundle: %w", err)
	}

	manifest := store.Manifest()
	summary := bundle.Summary{
		SchemaVersion:  1,
		Profile:        request.Profile.Name,
		RequestedURL:   request.DisplayURL,
		RequestedRuns:  request.Runs,
		SuccessfulRuns: 0,
	}
	rawFinalURLs := make(map[string]struct{})
	for attemptNumber := 1; attemptNumber <= request.Runs; attemptNumber++ {
		if err := ctx.Err(); err != nil {
			manifest.Status = string(contract.Interrupted)
			summary.Status = string(contract.Interrupted)
			return s.finalize(ctx, store, manifest, summary, now, nil, err)
		}

		attempt := bundle.Attempt{Number: attemptNumber, StartedAt: now().UTC().Format(time.RFC3339Nano)}
		raw, result, cleanupErr := s.runAttempt(ctx, request, attemptNumber)
		attempt.FinishedAt = now().UTC().Format(time.RFC3339Nano)
		if cleanupErr != nil {
			addWarning(&summary, "temporary browser cleanup failed")
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			attempt.Status = string(contract.Interrupted)
			attempt.Error = "interrupted"
			manifest.Attempts = append(manifest.Attempts, attempt)
			manifest.Status = string(contract.Interrupted)
			summary.Status = string(contract.Interrupted)
			return s.finalize(ctx, store, manifest, summary, now, nil, ctxErr)
		}
		if result.Err != nil || result.ExitCode != 0 {
			attempt.Status = string(contract.EngineFailed)
			attempt.ExitCode = result.ExitCode
			attempt.Error = "engine failed"
			manifest.Attempts = append(manifest.Attempts, attempt)
			continue
		}

		artifactPath := filepath.ToSlash(filepath.Join("samples", fmt.Sprintf("run-%d.lhr.json", attemptNumber)))
		if ctxErr := ctx.Err(); ctxErr != nil {
			attempt.Status = string(contract.Interrupted)
			attempt.Error = "interrupted"
			manifest.Attempts = append(manifest.Attempts, attempt)
			manifest.Status = string(contract.Interrupted)
			summary.Status = string(contract.Interrupted)
			return s.finalize(ctx, store, manifest, summary, now, nil, ctxErr)
		}
		if err := store.WriteArtifact(artifactPath, raw); err != nil {
			attempt.Status = string(contract.EngineFailed)
			attempt.Error = "evidence write failed"
			manifest.Attempts = append(manifest.Attempts, attempt)
			continue
		}
		attempt.Artifact = artifactPath
		manifest.Artifacts = append(manifest.Artifacts, bundle.Artifact{Kind: "lhr", Path: artifactPath})
		if s.AfterArtifact != nil {
			s.AfterArtifact()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			attempt.Status = string(contract.Interrupted)
			attempt.Error = "interrupted"
			manifest.Attempts = append(manifest.Attempts, attempt)
			manifest.Status = string(contract.Interrupted)
			summary.Status = string(contract.Interrupted)
			return s.finalize(ctx, store, manifest, summary, now, nil, ctxErr)
		}

		sample, err := lhr.Parse(raw)
		if err != nil {
			attempt.Status = string(contract.ParseFailed)
			attempt.Error = "Lighthouse report parse failed"
			manifest.Attempts = append(manifest.Attempts, attempt)
			continue
		}
		if sample.LighthouseVersion != request.Protocol.LighthouseVersion {
			attempt.Status = string(contract.ParseFailed)
			attempt.Error = "Lighthouse version mismatch"
			manifest.Attempts = append(manifest.Attempts, attempt)
			continue
		}
		rawFinalURLs[sample.FinalURL] = struct{}{}
		sample, warning := sanitizeSample(sample)
		if warning != "" {
			addWarning(&summary, warning)
		}
		if len(sample.Warnings) > 0 {
			addWarning(&summary, fmt.Sprintf("Lighthouse reported warnings on attempt %d", attemptNumber))
			sample.Warnings = nil
		}
		if summary.FinalURL == "" && sample.FinalURL != "" {
			summary.FinalURL = sample.FinalURL
			manifest.FinalURL = sample.FinalURL
		}
		attempt.Status = string(contract.OK)
		if cleanupErr != nil {
			attempt.Status = string(contract.Partial)
			attempt.Error = "temporary browser cleanup failed"
		}
		manifest.Attempts = append(manifest.Attempts, attempt)
		summary.Samples = append(summary.Samples, bundle.SuccessfulSample{Attempt: attemptNumber, Sample: sample})
	}

	summary.SuccessfulRuns = len(summary.Samples)
	if hasFailedAttempt(manifest.Attempts) {
		addWarning(&summary, "one or more attempts failed")
	}
	if len(summary.Samples) < minimumSuccessfulSamples {
		manifest.Status = string(contract.Partial)
		summary.Status = string(contract.Partial)
		return s.finalize(ctx, store, manifest, summary, now, nil, ErrIncomplete)
	}
	resultErr := error(nil)
	if hasNonOKAttempt(manifest.Attempts) {
		summary.Status = string(contract.Partial)
		manifest.Status = string(contract.Partial)
		resultErr = ErrPartial
	} else {
		summary.Status = string(contract.OK)
		manifest.Status = string(contract.OK)
	}
	addInstabilityWarnings(&summary, len(rawFinalURLs) > 1)
	return s.finalize(ctx, store, manifest, summary, now, &summary, resultErr)
}

func validateRequest(request Request) error {
	if request.Runs < minimumSuccessfulSamples {
		return fmt.Errorf("%w: at least %d runs are required", ErrInvalidRequest, minimumSuccessfulSamples)
	}
	if request.URL == "" || request.DisplayURL == "" || request.Out == "" || request.Profile.Name == "" {
		return fmt.Errorf("%w: URL, display URL, profile, and output are required", ErrInvalidRequest)
	}
	parsedURL, err := safeurl.Validate(request.URL)
	if err != nil || safeurl.Display(parsedURL) != request.DisplayURL {
		return fmt.Errorf("%w: display URL must be the safe rendering of the requested URL", ErrInvalidRequest)
	}
	protocol := request.Protocol
	if protocol.Profile == "" || protocol.FormFactor == "" || protocol.ThrottlingMethod == "" || protocol.LighthouseVersion == "" || protocol.NodeVersion == "" || protocol.ChromeVersion == "" || protocol.OS == "" || protocol.Arch == "" || len(protocol.ResolvedFlags) == 0 || len(protocol.RuntimeFlags) == 0 {
		return fmt.Errorf("%w: complete measurement protocol is required", ErrInvalidRequest)
	}
	if request.Profile.Name != protocol.Profile || request.Profile.FormFactor != protocol.FormFactor || request.Profile.ThrottlingMethod != protocol.ThrottlingMethod || !equalStrings(request.Profile.LighthouseArgs, protocol.ResolvedFlags) {
		return fmt.Errorf("%w: profile and protocol must match exactly", ErrInvalidRequest)
	}
	return nil
}

func hasNonOKAttempt(attempts []bundle.Attempt) bool {
	for _, attempt := range attempts {
		if attempt.Status != string(contract.OK) {
			return true
		}
	}
	return false
}

func hasFailedAttempt(attempts []bundle.Attempt) bool {
	for _, attempt := range attempts {
		if attempt.Status != string(contract.OK) && attempt.Status != string(contract.Partial) {
			return true
		}
	}
	return false
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

func (s Service) runAttempt(ctx context.Context, request Request, attempt int) (raw []byte, result engine.Result, cleanupErr error) {
	mkdirTemp := s.MkdirTemp
	if mkdirTemp == nil {
		mkdirTemp = os.MkdirTemp
	}
	removeAll := s.RemoveAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	browserDir, err := mkdirTemp("", "webperf-chrome-")
	if err != nil {
		return nil, engine.Result{Err: fmt.Errorf("create fresh browser state: %w", err)}, nil
	}
	defer func() {
		if err := removeAll(browserDir); err != nil {
			cleanupErr = err
		}
	}()

	var stdout bytes.Buffer
	result = s.Runner.Run(ctx, engine.Request{
		URL:     request.URL,
		Profile: request.Profile,
		Args: []string{
			"--only-categories=performance",
			"--output=json",
			"--quiet",
			"--no-enable-error-reporting",
			"--chrome-flags=--user-data-dir=" + browserDir,
		},
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	return stdout.Bytes(), result, nil
}

func (s Service) finalize(ctx context.Context, store *bundle.Store, manifest bundle.Manifest, summary bundle.Summary, now func() time.Time, aggregate *bundle.Summary, resultErr error) (bundle.Summary, error) {
	summary.SuccessfulRuns = len(summary.Samples)
	if s.BeforeFinalize != nil {
		s.BeforeFinalize()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		manifest.Status = string(contract.Interrupted)
		summary.Status = string(contract.Interrupted)
		aggregate = nil
		resultErr = ctxErr
	}
	manifest.FinishedAt = now().UTC().Format(time.RFC3339Nano)
	normalizeWarnings(&summary)
	if aggregate != nil {
		aggregate.Warnings = append([]string(nil), summary.Warnings...)
	}
	if err := store.Finalize(manifest, aggregate); err != nil {
		return summary, fmt.Errorf("finalize evidence bundle: %w", err)
	}
	return summary, resultErr
}

func sanitizeSample(sample lhr.Sample) (lhr.Sample, string) {
	parsed, err := safeurl.Validate(sample.FinalURL)
	if err != nil {
		sample.FinalURL = ""
		return sample, "Lighthouse reported an invalid final URL"
	}
	sample.FinalURL = safeurl.Display(parsed)
	return sample, ""
}

func addInstabilityWarnings(summary *bundle.Summary, rawFinalURLChanged bool) {
	if len(summary.Samples) == 0 {
		return
	}
	finalURLs := make(map[string]struct{})
	selectors := make(map[string]struct{})
	values := metricValues(summary.Samples)
	for _, item := range summary.Samples {
		finalURLs[item.Sample.FinalURL] = struct{}{}
		selector := item.Sample.LCPSelector
		if selector == "" {
			selector = "<missing>"
		}
		selectors[selector] = struct{}{}
	}
	if rawFinalURLChanged || len(finalURLs) > 1 {
		addWarning(summary, "final URL changed across successful samples")
	}
	if len(selectors) > 1 {
		addWarning(summary, "LCP selector changed across successful samples")
	}
	summary.Metrics = summarizeMetrics(values)
	// Timing uses a relative 10% IQR floor with a 100 ms absolute floor (50 ms
	// for TBT); score and CLS use their comparison materiality floors. These
	// fixed floors prevent a zero median from hiding instability or generating
	// warnings for minute numerical noise.
	for _, item := range []struct {
		name  string
		data  stats.Distribution
		floor float64
	}{
		{name: "FCP", data: summary.Metrics.FCP, floor: 100},
		{name: "LCP", data: summary.Metrics.LCP, floor: 100},
		{name: "Speed Index", data: summary.Metrics.SpeedIndex, floor: 100},
		{name: "TBT", data: summary.Metrics.TBT, floor: 50},
	} {
		if item.data.IQR >= max(item.floor, 0.10*item.data.Median) {
			addWarning(summary, "high dispersion for "+item.name)
		}
	}
	if summary.Metrics.PerformanceScore.IQR >= 5 {
		addWarning(summary, "high dispersion for Performance Score")
	}
	if summary.Metrics.CLS.IQR >= 0.02 {
		addWarning(summary, "high dispersion for CLS")
	}
	normalizeWarnings(summary)
}

func addWarning(summary *bundle.Summary, warning string) {
	if warning == "" {
		return
	}
	summary.Warnings = append(summary.Warnings, warning)
}

func normalizeWarnings(summary *bundle.Summary) {
	if len(summary.Warnings) == 0 {
		return
	}
	sort.Strings(summary.Warnings)
	unique := summary.Warnings[:0]
	for _, warning := range summary.Warnings {
		if len(unique) == 0 || unique[len(unique)-1] != warning {
			unique = append(unique, warning)
		}
	}
	summary.Warnings = unique
}

type collectedValues struct {
	performanceScore []float64
	fcp              []float64
	lcp              []float64
	speedIndex       []float64
	tbt              []float64
	cls              []float64
	benchmarkIndex   []float64
}

func metricValues(samples []bundle.SuccessfulSample) collectedValues {
	values := collectedValues{}
	for _, item := range samples {
		sample := item.Sample
		values.performanceScore = append(values.performanceScore, sample.PerformanceScore)
		values.fcp = append(values.fcp, sample.FCP)
		values.lcp = append(values.lcp, sample.LCP)
		values.speedIndex = append(values.speedIndex, sample.SpeedIndex)
		values.tbt = append(values.tbt, sample.TBT)
		values.cls = append(values.cls, sample.CLS)
		values.benchmarkIndex = append(values.benchmarkIndex, sample.BenchmarkIndex)
	}
	return values
}

func summarizeMetrics(values collectedValues) bundle.Metrics {
	return bundle.Metrics{
		PerformanceScore: stats.Summarize(values.performanceScore),
		FCP:              stats.Summarize(values.fcp),
		LCP:              stats.Summarize(values.lcp),
		SpeedIndex:       stats.Summarize(values.speedIndex),
		TBT:              stats.Summarize(values.tbt),
		CLS:              stats.Summarize(values.cls),
		BenchmarkIndex:   stats.Summarize(values.benchmarkIndex),
	}
}
