package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
)

func TestServiceRunIsSequentialAndDoesNotRetry(t *testing.T) {
	runner := &recordingRunner{results: successfulResults(5)}
	request := validRequest(t, 5)

	summary, err := Service{Runner: runner}.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 5 || runner.maxConcurrent != 1 {
		t.Fatalf("calls=%d maxConcurrent=%d", runner.calls, runner.maxConcurrent)
	}
	if summary.Status != string(contract.OK) || summary.Metrics.LCP.Count != 5 {
		t.Fatalf("summary=%+v", summary)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		artifact := filepath.Join(request.Out, "samples", fmt.Sprintf("run-%d.lhr.json", attempt))
		info, statErr := os.Stat(artifact)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact %d mode=%v err=%v", attempt, info, statErr)
		}
	}
	store, openErr := bundle.Open(request.Out)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if len(store.Manifest().Attempts) != 5 {
		t.Fatalf("attempts=%d", len(store.Manifest().Attempts))
	}
}

func TestServiceRunRejectsIncompleteProtocolBeforeCreatingBundle(t *testing.T) {
	runner := &recordingRunner{results: successfulResults(3)}
	request := validRequest(t, 3)
	request.Protocol.ChromeVersion = ""

	_, err := Service{Runner: runner}.Run(context.Background(), request)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls=%d", runner.calls)
	}
	if _, statErr := os.Lstat(request.Out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bundle was created: %v", statErr)
	}
}

func TestServiceRunRejectsMismatchedProfileProtocolAndDisplayURL(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{
			name: "profile form factor",
			mutate: func(request *Request) {
				request.Protocol.FormFactor = "mobile"
			},
		},
		{
			name: "profile flags",
			mutate: func(request *Request) {
				request.Protocol.ResolvedFlags = []string{"--form-factor=mobile"}
			},
		},
		{
			name: "display URL",
			mutate: func(request *Request) {
				request.DisplayURL = "https://example.test/start?token=source-secret"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{results: successfulResults(3)}
			request := validRequest(t, 3)
			tt.mutate(&request)

			_, err := Service{Runner: runner}.Run(context.Background(), request)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err=%v", err)
			}
			if runner.calls != 0 {
				t.Fatalf("runner calls=%d", runner.calls)
			}
			if _, statErr := os.Lstat(request.Out); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("bundle was created: %v", statErr)
			}
		})
	}
}

func TestServiceRunExcludesLighthouseVersionMismatchFromAggregate(t *testing.T) {
	runner := &recordingRunner{results: []runnerResult{
		{lhr: fixtureLHRWithVersion(1, "13.3.0")},
		{lhr: fixtureLHRWithVersion(2, "13.3.0")},
		{lhr: fixtureLHRWithVersion(3, "13.3.0")},
	}}
	request := validRequest(t, 3)

	summary, err := Service{Runner: runner}.Run(context.Background(), request)
	if !errors.Is(err, ErrIncomplete) || summary.SuccessfulRuns != 0 {
		t.Fatalf("err=%v summary=%+v", err, summary)
	}
	store, openErr := bundle.Open(request.Out)
	if openErr != nil {
		t.Fatal(openErr)
	}
	for _, attempt := range store.Manifest().Attempts {
		if attempt.Status != string(contract.ParseFailed) || attempt.Error != "Lighthouse version mismatch" {
			t.Fatalf("attempt=%+v", attempt)
		}
	}
}

func TestServiceRunRequiresThreeSuccessfulSamplesAndKeepsEvidence(t *testing.T) {
	runner := &recordingRunner{results: []runnerResult{
		{lhr: fixtureLHR(1)},
		{err: errors.New("navigation failed")},
		{lhr: fixtureLHR(2)},
		{err: errors.New("engine failed")},
		{err: errors.New("engine failed")},
	}}
	request := validRequest(t, 5)

	summary, err := Service{Runner: runner}.Run(context.Background(), request)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err=%v", err)
	}
	if runner.calls != 5 || summary.Status != string(contract.Partial) || summary.Metrics.LCP.Count != 0 {
		t.Fatalf("calls=%d summary=%+v", runner.calls, summary)
	}
	store, openErr := bundle.Open(request.Out)
	if openErr != nil {
		t.Fatal(openErr)
	}
	manifest := store.Manifest()
	if manifest.Status != string(contract.Partial) || len(manifest.Attempts) != 5 {
		t.Fatalf("manifest=%+v", manifest)
	}
	if manifest.FinalURL != "https://example.test/landing" {
		t.Fatalf("manifest finalURL=%q", manifest.FinalURL)
	}
	if _, statErr := os.Stat(filepath.Join(request.Out, "samples", "run-1.lhr.json")); statErr != nil {
		t.Fatalf("successful evidence missing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(request.Out, "summary.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("incomplete run wrote aggregate summary: %v", statErr)
	}
}

func TestServiceRunMarksCompleteSamplesWithFailedAttemptsPartial(t *testing.T) {
	runner := &recordingRunner{results: []runnerResult{
		{lhr: fixtureLHR(1)},
		{err: errors.New("engine failed")},
		{lhr: fixtureLHR(2)},
		{lhr: fixtureLHR(3)},
	}}
	request := validRequest(t, 4)

	summary, err := Service{Runner: runner}.Run(context.Background(), request)
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("err=%v", err)
	}
	if summary.Status != string(contract.Partial) || summary.SuccessfulRuns != 3 || summary.Metrics.LCP.Count != 3 {
		t.Fatalf("summary=%+v", summary)
	}
	if !contains(fmt.Sprint(summary.Warnings), "attempts failed") {
		t.Fatalf("warnings=%v", summary.Warnings)
	}
	store, openErr := bundle.Open(request.Out)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if store.Manifest().Status != string(contract.Partial) {
		t.Fatalf("manifest=%+v", store.Manifest())
	}
	if _, statErr := os.Stat(filepath.Join(request.Out, "summary.json")); statErr != nil {
		t.Fatalf("partial aggregate summary missing: %v", statErr)
	}
}

func TestServiceRunRedactsFinalURLAndWarnsOnDrift(t *testing.T) {
	runner := &recordingRunner{results: []runnerResult{
		{lhr: addRunWarning(fixtureLHRWithURL(1, "https://example.test/landing?token=one#first", "main > img"))},
		{lhr: fixtureLHRWithURL(2, "https://example.test/landing?token=two#second", "main > img")},
		{lhr: fixtureLHRWithURL(3, "https://example.test/other?token=three", "article > img")},
	}}
	request := validRequest(t, 3)
	request.DisplayURL = "https://example.test/start?token=REDACTED"

	summary, err := Service{Runner: runner}.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FinalURL != "https://example.test/landing?token=REDACTED" {
		t.Fatalf("finalURL=%q", summary.FinalURL)
	}
	joined := fmt.Sprint(summary.Warnings)
	for _, forbidden := range []string{"one", "two", "three", "#first", "#second"} {
		if contains(joined, forbidden) {
			t.Fatalf("summary warning exposed %q: %s", forbidden, joined)
		}
	}
	if !contains(joined, "final URL changed") || !contains(joined, "LCP selector changed") {
		t.Fatalf("warnings=%v", summary.Warnings)
	}
	contents, readErr := os.ReadFile(filepath.Join(request.Out, "summary.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, forbidden := range []string{"token=one", "token=two", "token=three", "source-secret", "warning-token"} {
		if contains(string(contents), forbidden) {
			t.Fatalf("summary.json exposed %q: %s", forbidden, contents)
		}
	}
}

func TestServiceRunNeverPersistsUnsafeFinalURL(t *testing.T) {
	runner := &recordingRunner{results: []runnerResult{
		{lhr: fixtureLHRWithURL(1, "https://user:secret@example.test/landing?token=one", "main > img")},
		{lhr: fixtureLHR(2)},
		{lhr: fixtureLHR(3)},
	}}
	request := validRequest(t, 3)

	summary, err := Service{Runner: runner}.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	encoded := fmt.Sprintf("%+v", summary)
	if contains(encoded, "secret") || contains(encoded, "token=one") {
		t.Fatalf("summary exposed unsafe final URL: %s", encoded)
	}
	if !contains(fmt.Sprint(summary.Warnings), "invalid final URL") {
		t.Fatalf("warnings=%v", summary.Warnings)
	}
}

func TestServiceRunWarnsWhenOnlyFinalQueryValueChanges(t *testing.T) {
	runner := &recordingRunner{results: []runnerResult{
		{lhr: fixtureLHRWithURL(1, "https://example.test/landing?variant=one", "main > img")},
		{lhr: fixtureLHRWithURL(2, "https://example.test/landing?variant=two", "main > img")},
		{lhr: fixtureLHRWithURL(3, "https://example.test/landing?variant=three", "main > img")},
	}}
	request := validRequest(t, 3)

	summary, err := Service{Runner: runner}.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(fmt.Sprint(summary.Warnings), "final URL changed") {
		t.Fatalf("warnings=%v", summary.Warnings)
	}
	contents, readErr := os.ReadFile(filepath.Join(request.Out, "summary.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, forbidden := range []string{"variant=one", "variant=two", "variant=three"} {
		if contains(string(contents), forbidden) {
			t.Fatalf("summary exposed %q: %s", forbidden, contents)
		}
	}
}

func TestServiceRunStopsAfterCancellationAndMarksInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &recordingRunner{results: successfulResults(5), afterRun: func(calls int) {
		if calls == 1 {
			cancel()
		}
	}}
	request := validRequest(t, 5)

	summary, err := Service{Runner: runner}.Run(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if runner.calls != 1 || summary.Status != string(contract.Interrupted) {
		t.Fatalf("calls=%d summary=%+v", runner.calls, summary)
	}
	store, openErr := bundle.Open(request.Out)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if store.Manifest().Status != string(contract.Interrupted) {
		t.Fatalf("manifest=%+v", store.Manifest())
	}
}

func TestFinalizeCorrectsSuccessfulRunsWhenInterruptedWithoutAggregate(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := bundle.Create(target, bundle.Manifest{Status: "RUNNING"}, bundle.Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary := bundle.Summary{
		SuccessfulRuns: 99,
		Samples: []bundle.SuccessfulSample{
			{Attempt: 1, Sample: lhr.Sample{FinalURL: "https://example.test"}},
			{Attempt: 2, Sample: lhr.Sample{FinalURL: "https://example.test"}},
		},
	}
	got, err := (Service{}).finalize(ctx, store, bundle.Manifest{Status: "OK"}, summary, time.Now, &summary, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if got.SuccessfulRuns != len(got.Samples) || got.Status != string(contract.Interrupted) {
		t.Fatalf("summary=%+v", got)
	}
}

func TestServiceRunUsesUniqueBrowserDirectoriesAndRemovesThem(t *testing.T) {
	root := t.TempDir()
	var browserDirs []string
	runner := &recordingRunner{results: successfulResults(3), onRequest: func(request engine.Request) {
		dir := browserDirFrom(t, request.Args)
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("browser state directory did not exist during run: %v", err)
		}
		browserDirs = append(browserDirs, dir)
	}}
	request := validRequest(t, 3)

	_, err := (Service{
		Runner: runner,
		MkdirTemp: func(_ string, pattern string) (string, error) {
			return os.MkdirTemp(root, pattern)
		},
	}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(browserDirs) != 3 {
		t.Fatalf("browser dirs=%v", browserDirs)
	}
	seen := make(map[string]struct{}, len(browserDirs))
	for _, dir := range browserDirs {
		if _, duplicate := seen[dir]; duplicate {
			t.Fatalf("browser directory was reused: %q", dir)
		}
		seen[dir] = struct{}{}
		if _, statErr := os.Lstat(dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("browser directory remained after run: %v", statErr)
		}
	}
}

func TestServiceRunMarksCleanupFailurePartialWithoutPathLeak(t *testing.T) {
	runner := &recordingRunner{results: successfulResults(3)}
	request := validRequest(t, 3)

	summary, err := (Service{
		Runner:    runner,
		RemoveAll: func(string) error { return errors.New("cleanup failed /private/tmp/webperf-chrome-secret") },
	}).Run(context.Background(), request)
	if !errors.Is(err, ErrPartial) || summary.Status != string(contract.Partial) {
		t.Fatalf("err=%v summary=%+v", err, summary)
	}
	encoded := fmt.Sprintf("%+v", summary)
	if contains(encoded, "/private/tmp") || !contains(encoded, "temporary browser cleanup failed") {
		t.Fatalf("summary=%s", encoded)
	}
}

func TestServiceRunReportsCleanupFailureWhenEngineAttemptFails(t *testing.T) {
	runner := &recordingRunner{results: []runnerResult{
		{err: errors.New("engine failed")},
		{err: errors.New("engine failed")},
		{err: errors.New("engine failed")},
	}}
	request := validRequest(t, 3)

	summary, err := (Service{
		Runner:    runner,
		RemoveAll: func(string) error { return errors.New("cleanup failed /private/tmp/webperf-chrome-secret") },
	}).Run(context.Background(), request)
	if !errors.Is(err, ErrIncomplete) || !contains(fmt.Sprint(summary.Warnings), "temporary browser cleanup failed") {
		t.Fatalf("err=%v summary=%+v", err, summary)
	}
	if contains(fmt.Sprint(summary), "/private/tmp") {
		t.Fatalf("summary exposed temporary path: %+v", summary)
	}
}

func TestServiceRunInterruptsBeforeParseAndDoesNotCommitOK(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &recordingRunner{results: successfulResults(3)}
	request := validRequest(t, 3)

	summary, err := (Service{
		Runner:        runner,
		AfterArtifact: cancel,
	}).Run(ctx, request)
	if !errors.Is(err, context.Canceled) || summary.Status != string(contract.Interrupted) {
		t.Fatalf("err=%v summary=%+v", err, summary)
	}
	store, openErr := bundle.Open(request.Out)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if store.Manifest().Status != string(contract.Interrupted) {
		t.Fatalf("manifest=%+v", store.Manifest())
	}
}

func TestServiceRunInterruptsBeforeFinalCommitAndDoesNotWriteSummary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &recordingRunner{results: successfulResults(3)}
	request := validRequest(t, 3)

	summary, err := (Service{
		Runner:         runner,
		BeforeFinalize: cancel,
	}).Run(ctx, request)
	if !errors.Is(err, context.Canceled) || summary.Status != string(contract.Interrupted) {
		t.Fatalf("err=%v summary=%+v", err, summary)
	}
	store, openErr := bundle.Open(request.Out)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if store.Manifest().Status != string(contract.Interrupted) {
		t.Fatalf("manifest=%+v", store.Manifest())
	}
	if _, statErr := os.Lstat(filepath.Join(request.Out, "summary.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("summary was committed after cancellation: %v", statErr)
	}
}

func TestAddInstabilityWarningsIncludesZeroMedianAndMissingSelectorIdentity(t *testing.T) {
	summary := bundle.Summary{Samples: []bundle.SuccessfulSample{
		{Attempt: 1, Sample: lhr.Sample{PerformanceScore: 80, CLS: 0, TBT: 0, LCPSelector: ""}},
		{Attempt: 2, Sample: lhr.Sample{PerformanceScore: 80, CLS: 0, TBT: 0, LCPSelector: "main > img"}},
		{Attempt: 3, Sample: lhr.Sample{PerformanceScore: 95, CLS: .10, TBT: 100, LCPSelector: ""}},
	}}

	addInstabilityWarnings(&summary, false)
	joined := fmt.Sprint(summary.Warnings)
	for _, warning := range []string{
		"LCP selector changed",
		"high dispersion for Performance Score",
		"high dispersion for CLS",
		"high dispersion for TBT",
	} {
		if !contains(joined, warning) {
			t.Fatalf("warnings=%v missing %q", summary.Warnings, warning)
		}
	}
}

func validRequest(t *testing.T, runs int) Request {
	t.Helper()
	resolved, err := profile.Resolve("desktop-lab-v1")
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		URL:        "https://example.test/start?token=source-secret",
		DisplayURL: "https://example.test/start?token=REDACTED",
		Profile:    resolved,
		Runs:       runs,
		Out:        filepath.Join(t.TempDir(), "run"),
		Protocol: bundle.Protocol{
			SchemaVersion:     1,
			Profile:           resolved.Name,
			FormFactor:        resolved.FormFactor,
			ThrottlingMethod:  resolved.ThrottlingMethod,
			LighthouseVersion: "13.4.1",
			NodeVersion:       "22.19.0",
			ChromeVersion:     "150.0.0.0",
			OS:                "test",
			Arch:              "test",
			ResolvedFlags:     append([]string(nil), resolved.LighthouseArgs...),
			RuntimeFlags:      []string{"--output=json", "fresh-user-data-dir-per-attempt"},
		},
	}
}

type runnerResult struct {
	lhr string
	err error
}

type recordingRunner struct {
	mu            sync.Mutex
	results       []runnerResult
	calls         int
	active        int
	maxConcurrent int
	afterRun      func(int)
	onRequest     func(engine.Request)
}

func (r *recordingRunner) Run(_ context.Context, request engine.Request) engine.Result {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.active++
	if r.active > r.maxConcurrent {
		r.maxConcurrent = r.active
	}
	result := r.results[call-1]
	r.mu.Unlock()
	if r.onRequest != nil {
		r.onRequest(request)
	}

	if result.lhr != "" {
		if _, err := io.WriteString(request.Stdout, result.lhr); err != nil {
			r.mu.Lock()
			r.active--
			r.mu.Unlock()
			return engine.Result{Err: err}
		}
	}

	r.mu.Lock()
	r.active--
	afterRun := r.afterRun
	r.mu.Unlock()
	if afterRun != nil {
		afterRun(call)
	}
	return engine.Result{Err: result.err}
}

func successfulResults(count int) []runnerResult {
	results := make([]runnerResult, count)
	for index := range results {
		results[index].lhr = fixtureLHR(index + 1)
	}
	return results
}

func fixtureLHR(run int) string {
	return fixtureLHRWithURL(run, "https://example.test/landing", "main > img")
}

func fixtureLHRWithVersion(run int, version string) string {
	report := fixtureLHR(run)
	return strings.Replace(report, `"lighthouseVersion": "13.4.1"`, `"lighthouseVersion": "`+version+`"`, 1)
}

func fixtureLHRWithURL(run int, finalURL, selector string) string {
	return fmt.Sprintf(`{
  "lighthouseVersion": "13.4.1",
  "finalDisplayedUrl": %q,
  "categories": {"performance": {"score": 0.8}},
  "environment": {"benchmarkIndex": 1000},
  "audits": {
    "first-contentful-paint": {"numericValue": %d},
    "largest-contentful-paint": {"numericValue": %d},
    "speed-index": {"numericValue": %d},
    "total-blocking-time": {"numericValue": %d},
    "cumulative-layout-shift": {"numericValue": 0.02},
    "lcp-breakdown-insight": {"details": {"items": [{"type": "node", "selector": %q}]}}
  }
}`,
		finalURL,
		1000+run,
		1500+run,
		2000+run,
		100+run,
		selector,
	)
}

func addRunWarning(report string) string {
	return report[:len(report)-1] + `,
  "runWarnings": ["warning-token"]
}`
}

func browserDirFrom(t *testing.T, args []string) string {
	t.Helper()
	const prefix = "--chrome-flags=--user-data-dir="
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	t.Fatalf("missing browser directory flag: %v", args)
	return ""
}

func contains(value, fragment string) bool {
	return len(fragment) == 0 || (len(value) >= len(fragment) && stringContains(value, fragment))
}

func stringContains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
