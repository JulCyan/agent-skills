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
	"time"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/safeurl"
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
	warningSignals := bundle.WarningSignals{}
	for attemptNumber := 1; attemptNumber <= request.Runs; attemptNumber++ {
		if err := ctx.Err(); err != nil {
			manifest.Status = string(contract.Interrupted)
			summary.Status = string(contract.Interrupted)
			return s.finalize(ctx, store, manifest, summary, warningSignals, now, nil, err)
		}

		attempt := bundle.Attempt{Number: attemptNumber, StartedAt: now().UTC().Format(time.RFC3339Nano)}
		raw, result, cleanupErr := s.runAttempt(ctx, request, attemptNumber)
		attempt.FinishedAt = now().UTC().Format(time.RFC3339Nano)
		attempt.CleanupFailed = cleanupErr != nil
		if ctxErr := ctx.Err(); ctxErr != nil {
			attempt.Status = string(contract.Interrupted)
			attempt.Error = "interrupted"
			manifest.Attempts = append(manifest.Attempts, attempt)
			manifest.Status = string(contract.Interrupted)
			summary.Status = string(contract.Interrupted)
			return s.finalize(ctx, store, manifest, summary, warningSignals, now, nil, ctxErr)
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
			return s.finalize(ctx, store, manifest, summary, warningSignals, now, nil, ctxErr)
		}
		artifactSHA256, err := store.WriteArtifact(artifactPath, raw)
		if err != nil {
			attempt.Status = string(contract.EngineFailed)
			attempt.Error = "evidence write failed"
			manifest.Attempts = append(manifest.Attempts, attempt)
			continue
		}
		attempt.Artifact = artifactPath
		manifest.Artifacts = append(manifest.Artifacts, bundle.Artifact{Kind: "lhr", Path: artifactPath, SHA256: artifactSHA256})
		if s.AfterArtifact != nil {
			s.AfterArtifact()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			attempt.Status = string(contract.Interrupted)
			attempt.Error = "interrupted"
			manifest.Attempts = append(manifest.Attempts, attempt)
			manifest.Status = string(contract.Interrupted)
			summary.Status = string(contract.Interrupted)
			return s.finalize(ctx, store, manifest, summary, warningSignals, now, nil, ctxErr)
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
		warningSignals.RawFinalURLs = append(warningSignals.RawFinalURLs, sample.FinalURL)
		sample, warning := sanitizeSample(sample)
		if warning != "" {
			warningSignals.InvalidFinalURL = true
		}
		if len(sample.Warnings) > 0 {
			warningSignals.LighthouseWarningAttempts = append(warningSignals.LighthouseWarningAttempts, attemptNumber)
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
	if len(summary.Samples) < minimumSuccessfulSamples {
		manifest.Status = string(contract.Partial)
		summary.Status = string(contract.Partial)
		return s.finalize(ctx, store, manifest, summary, warningSignals, now, nil, ErrIncomplete)
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
	return s.finalize(ctx, store, manifest, summary, warningSignals, now, &summary, resultErr)
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
	if err := bundle.ValidateProtocol(protocol); err != nil {
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

func (s Service) finalize(ctx context.Context, store *bundle.Store, manifest bundle.Manifest, summary bundle.Summary, warningSignals bundle.WarningSignals, now func() time.Time, aggregate *bundle.Summary, resultErr error) (bundle.Summary, error) {
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
	summary.Warnings = bundle.CanonicalWarnings(manifest.Attempts, summary.Samples, warningSignals)
	if aggregate != nil {
		summary.Metrics = bundle.SummarizeSamples(summary.Samples)
		aggregate.Metrics = summary.Metrics
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
	var signals bundle.WarningSignals
	if rawFinalURLChanged {
		signals.RawFinalURLs = []string{"first", "second"}
	}
	summary.Metrics = bundle.SummarizeSamples(summary.Samples)
	summary.Warnings = bundle.CanonicalWarnings(nil, summary.Samples, signals)
}
