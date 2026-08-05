package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/collect"
	webcompare "github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/compare"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

func TestCollectRequiresExplicitProfile(t *testing.T) {
	code, report := runJSON(t, "--json", "collect", "--url", "https://example.test", "--out", "run")
	if code != contract.ExitInvalidInput || report.Status != contract.InvalidInput {
		t.Fatalf("code=%d status=%s", code, report.Status)
	}
	if report.Error == nil || report.Error.Code != "profile_required" {
		t.Fatalf("error=%+v", report.Error)
	}
	if got := availableProfileNames(t, report.Data); !equalStrings(got, []string{"desktop-observed-v1", "desktop-lab-v1", "mobile-lab-v1"}) {
		t.Fatalf("available profiles=%v", got)
	}
}

func TestRootHelpListsAvailableProfiles(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(context.Background(), []string{"--help"}, Dependencies{Stdout: &stdout})
	if code != contract.ExitOK {
		t.Fatalf("code=%d", code)
	}
	for _, name := range []string{"desktop-observed-v1", "desktop-lab-v1", "mobile-lab-v1"} {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("help did not include %q: %q", name, stdout.String())
		}
	}
}

func TestProfilesListUsesStableJSONEnvelope(t *testing.T) {
	code, report := runJSON(t, "--json", "profiles", "list")
	if code != contract.ExitOK || report.Status != contract.OK {
		t.Fatalf("code=%d status=%s", code, report.Status)
	}
	if report.SchemaVersion != contract.SchemaVersion || report.Command != "profiles list" {
		t.Fatalf("report=%+v", report)
	}
}

func TestProfilesListPrintsAvailableProfilesForHumans(t *testing.T) {
	var stdout bytes.Buffer
	code := Run(context.Background(), []string{"profiles", "list"}, Dependencies{Stdout: &stdout})
	if code != contract.ExitOK {
		t.Fatalf("code=%d", code)
	}
	for _, name := range []string{"desktop-observed-v1", "desktop-lab-v1", "mobile-lab-v1"} {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("output did not include %q: %q", name, stdout.String())
		}
	}
}

func TestEngineBackedCommandsReturnStructuredNeedsSetup(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "doctor", args: []string{"--json", "doctor"}},
		{name: "engine status", args: []string{"--json", "engine", "status"}},
		{name: "engine setup", args: []string{"--json", "engine", "setup"}},
		{name: "collect", args: []string{"--json", "collect", "--url", "https://example.test", "--profile", "desktop-lab-v1", "--out", "run"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, report := runJSONWith(t, Dependencies{Engine: needsSetupEngine{}}, tt.args...)
			if code != contract.ExitNeedsSetup || report.Status != contract.NeedsSetup {
				t.Fatalf("code=%d status=%s", code, report.Status)
			}
			if report.Error == nil || report.Error.Code != "engine_needs_setup" || strings.Contains(report.Error.Message, "command") {
				t.Fatalf("needs-setup error=%+v", report.Error)
			}
		})
	}
}

func TestDoctorReportsHostEnvironmentWithoutExecutablePaths(t *testing.T) {
	code, report := runJSONWith(t, Dependencies{Engine: needsSetupEngine{}}, "--json", "doctor")
	if code != contract.ExitNeedsSetup || report.Status != contract.NeedsSetup {
		t.Fatalf("code=%d status=%s", code, report.Status)
	}
	encoded, err := json.Marshal(report.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "goVersion") || !strings.Contains(string(encoded), "NEEDS_SETUP") || strings.Contains(string(encoded), "Path") {
		t.Fatalf("doctor data=%s", encoded)
	}
}

func TestEngineStatusReportsNeedsSetupData(t *testing.T) {
	code, report := runJSONWith(t, Dependencies{Engine: needsSetupEngine{}}, "--json", "engine", "status")
	if code != contract.ExitNeedsSetup || report.Status != contract.NeedsSetup {
		t.Fatalf("code=%d status=%s", code, report.Status)
	}
	encoded, err := json.Marshal(report.Data)
	if err != nil || !strings.Contains(string(encoded), "NEEDS_SETUP") {
		t.Fatalf("data=%s err=%v", encoded, err)
	}
}

func TestEngineStatusReportsReadyData(t *testing.T) {
	code, report := runJSONWith(t, Dependencies{Engine: readyStatusEngine{}}, "--json", "engine", "status")
	if code != contract.ExitOK || report.Status != contract.OK {
		t.Fatalf("code=%d status=%s", code, report.Status)
	}
	encoded, err := json.Marshal(report.Data)
	if err != nil || !strings.Contains(string(encoded), "OK") {
		t.Fatalf("data=%s err=%v", encoded, err)
	}
}

func TestDoctorAndEngineCommandsMapMidCommandCancellationToInterrupted(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "doctor"},
		{"--json", "engine", "status"},
		{"--json", "engine", "setup"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Run(ctx, args, Dependencies{
				Stdout: &stdout,
				Stderr: &stderr,
				Engine: cancelingEngine{cancel: cancel},
			})
			if code != contract.ExitInterrupted {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			var report contract.Envelope
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.Status != contract.Interrupted {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestCollectionFailureMapsPartialAttemptsToPartialStatus(t *testing.T) {
	report := collectionFailure(bundle.Summary{Status: string(contract.Partial)}, collect.ErrPartial)
	if report.Status != contract.Partial || report.Error == nil || report.Error.Code != "partial_attempts" {
		t.Fatalf("report=%+v", report)
	}
}

func TestRenderHumanCollectSummaryIncludesProtocolMetricsAndWarnings(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	report := success("collect", bundle.Summary{
		SchemaVersion:  1,
		Profile:        "desktop-lab-v1",
		RequestedRuns:  5,
		SuccessfulRuns: 5,
		FinalURL:       "https://example.test/landing?token=REDACTED",
		Metrics: bundle.Metrics{
			PerformanceScore: stats.Distribution{Count: 5, Median: 80, MAD: 2, IQR: 4},
			LCP:              stats.Distribution{Count: 5, Median: 1500, MAD: 100, IQR: 200},
		},
		Warnings: []string{"final URL changed across successful samples"},
	})
	if code := render(&stdout, &stderr, false, report); code != contract.ExitOK {
		t.Fatalf("code=%d", code)
	}
	for _, fragment := range []string{
		"profile: desktop-lab-v1",
		"samples: 5/5",
		"final URL: https://example.test/landing?token=REDACTED",
		"official performance score: median=80",
		"LCP: median=1500",
		"warning: final URL changed across successful samples",
	} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Fatalf("output missing %q: %q", fragment, stdout.String())
		}
	}
}

func TestRenderHumanCollectMarksIncompleteAggregateUnavailable(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	report := success("collect", bundle.Summary{
		SchemaVersion:  1,
		Profile:        "desktop-lab-v1",
		RequestedRuns:  5,
		SuccessfulRuns: 2,
		Metrics: bundle.Metrics{
			PerformanceScore: stats.Distribution{Count: 2, Median: 81},
			LCP:              stats.Distribution{Count: 2, Median: 1500},
		},
	})
	if code := render(&stdout, &stderr, false, report); code != contract.ExitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout.String(), "aggregate: unavailable") {
		t.Fatalf("output=%q", stdout.String())
	}
	for _, forbidden := range []string{"official performance score", "LCP:"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("incomplete aggregate printed %q: %q", forbidden, stdout.String())
		}
	}
}

func TestRenderHumanCollectPreBundleFailureOmitsEmptySummary(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	report := failure("collect", contract.EngineFailed, "collection_failed", "collection failed", "run again", nil)
	report.Data = bundle.Summary{}
	if code := render(&stdout, &stderr, false, report); code != contract.ExitGeneralError {
		t.Fatalf("code=%d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("pre-bundle failure rendered empty summary: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "collection failed") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestInvalidInputIsSafeAndUsesOneJSONDocument(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"--json", "collect", "--url", "https://user:secret@example.test", "--profile", "desktop-lab-v1", "--out", "run"}, Dependencies{Stdout: &stdout, Stderr: &stderr})
	if code != contract.ExitInvalidInput {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(stdout.String(), "secret") || strings.Contains(stderr.String(), "secret") {
		t.Fatalf("output exposed userinfo")
	}
	var report contract.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout was not one JSON document: %v; stdout=%q", err, stdout.String())
	}
	if report.Status != contract.InvalidInput {
		t.Fatalf("status=%s", report.Status)
	}
}

func TestUnknownCommandReturnsInvalidInput(t *testing.T) {
	code, report := runJSON(t, "--json", "unknown")
	if code != contract.ExitInvalidInput || report.Status != contract.InvalidInput {
		t.Fatalf("code=%d status=%s", code, report.Status)
	}
}

func TestInspectReportsFinalizedAggregateWithoutRawLHR(t *testing.T) {
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	code, report := runJSON(t, "--json", "inspect", "--run", directory)
	if code != contract.ExitOK || report.Status != contract.OK {
		t.Fatalf("code=%d report=%+v", code, report)
	}
	encoded, err := json.Marshal(report.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"aggregateAvailable", "protocol", "metrics"} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("inspect data missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range []string{"lcp-breakdown-insight", directory} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("inspect data leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestInspectFailsClosedWhenAggregateIsUnavailable(t *testing.T) {
	directory := pendingBundle(t)
	code, report := runJSON(t, "--json", "inspect", "--run", directory)
	if code == contract.ExitOK || report.Status != contract.Inconclusive {
		t.Fatalf("code=%d report=%+v", code, report)
	}
	encoded, err := json.Marshal(report.Data)
	if err != nil || strings.Contains(string(encoded), "samples") || strings.Contains(string(encoded), "artifact") {
		t.Fatalf("data=%s err=%v", encoded, err)
	}
}

func TestInspectFailsClosedWithoutEchoingHashValidSecrets(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "run")
	protocol := appProtocol()
	secretURL := "https://example.test/?token=manifest-secret"
	store, err := bundle.Create(directory, bundle.Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: secretURL}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	samples := []bundle.SuccessfulSample{
		{Attempt: 1, Sample: appSample(1500, 0.1, 70)},
		{Attempt: 2, Sample: appSample(1500, 0.1, 70)},
		{Attempt: 3, Sample: appSample(1500, 0.1, 70)},
	}
	summary := bundle.Summary{SchemaVersion: 1, Status: "OK", Profile: "desktop-lab-v1", RequestedURL: secretURL, RequestedRuns: 3, Samples: samples, Metrics: appMetrics(samples), Warnings: []string{"secret-warning /private/secret"}}
	attempts := make([]bundle.Attempt, len(samples))
	artifacts := make([]bundle.Artifact, len(samples))
	for index := range samples {
		path := fmt.Sprintf("samples/run-%d.lhr.json", index+1)
		digest, err := store.WriteArtifact(path, []byte(appLHR(1500, 0.1, 70)))
		if err != nil {
			t.Fatal(err)
		}
		attempts[index] = bundle.Attempt{Number: index + 1, Status: "OK", Artifact: path}
		artifacts[index] = bundle.Artifact{Kind: "lhr", Path: path, SHA256: digest}
	}
	if err := store.Finalize(bundle.Manifest{SchemaVersion: 1, Status: "OK", RequestedURL: secretURL, Attempts: attempts, Artifacts: artifacts}, &summary); err != nil {
		t.Fatal(err)
	}
	code, report := runJSON(t, "--json", "inspect", "--run", directory)
	if code == contract.ExitOK || report.Status != contract.Inconclusive {
		t.Fatalf("code=%d report=%+v", code, report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"manifest-secret", "secret-warning", "/private/secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("inspect exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestCompareReportsMixedAnalysisAsSuccessfulResult(t *testing.T) {
	baseline := finalizedBundle(t, "OK", 1500, 0.1, 70)
	candidate := finalizedBundle(t, "OK", 1200, 0.2, 80)
	code, report := runJSON(t, "--json", "compare", "--baseline", baseline, "--candidate", candidate)
	if code != contract.ExitOK || report.Status != contract.OK {
		t.Fatalf("code=%d report=%+v", code, report)
	}
	encoded, err := json.Marshal(report.Data)
	if err != nil || !strings.Contains(string(encoded), string(webcompare.Mixed)) {
		t.Fatalf("data=%s err=%v", encoded, err)
	}
}

func TestCompareMakesPartialBundleInconclusive(t *testing.T) {
	baseline := finalizedBundle(t, "OK", 1500, 0.1, 70)
	candidate := finalizedBundle(t, "PARTIAL", 1200, 0.1, 80)
	code, report := runJSON(t, "--json", "compare", "--baseline", baseline, "--candidate", candidate)
	if code == contract.ExitOK || report.Status != contract.Inconclusive || report.Error == nil || report.Error.Code != "incomplete_bundle" {
		t.Fatalf("code=%d report=%+v", code, report)
	}
}

func TestRawRequiresSeparatorAndRejectsGlobalJSON(t *testing.T) {
	code, report := runJSON(t, "--json", "raw", "lighthouse", "--", "--help")
	if code != contract.ExitInvalidInput || report.Status != contract.InvalidInput || report.Error == nil || report.Error.Code != "raw_json_unsupported" {
		t.Fatalf("code=%d report=%+v", code, report)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code = Run(context.Background(), []string{"raw", "lighthouse", "--help"}, Dependencies{Stdout: &stdout, Stderr: &stderr})
	if code != contract.ExitInvalidInput || !strings.Contains(stderr.String(), "expected raw lighthouse --") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRawForwardsOnlySeparatedArgumentsThroughInjectedLockedRunner(t *testing.T) {
	runner := &recordingRawRunner{result: engine.Result{ExitCode: 23, Err: errors.New("official command failed")}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"raw", "lighthouse", "--", "--output=json", "https://example.test"}, Dependencies{
		Stdout:    &stdout,
		Stderr:    &stderr,
		Engine:    readyStatusEngine{},
		RawRunner: runner,
	})
	if code != 23 || !equalStrings(runner.args, []string{"--output=json", "https://example.test"}) {
		t.Fatalf("code=%d args=%v", code, runner.args)
	}
	if !strings.Contains(stdout.String(), "official stdout") || !strings.Contains(stderr.String(), "official stderr") || !strings.Contains(stderr.String(), "does not use profiles") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRawReportsFixedChromeOverrideDiagnostic(t *testing.T) {
	runner := &recordingRawRunner{result: engine.Result{ExitCode: contract.ExitInvalidInput, Err: engine.ErrRawChromePath}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"raw", "lighthouse", "--", "--chrome-path=/private/secret-chrome"}, Dependencies{
		Stdout:    &stdout,
		Stderr:    &stderr,
		Engine:    readyStatusEngine{},
		RawRunner: runner,
	})
	if code != contract.ExitInvalidInput || !strings.Contains(stderr.String(), "cannot override the resolved Chrome runtime") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "secret-chrome") {
		t.Fatalf("raw diagnostic leaked caller path: %q", stderr.String())
	}
}

func runJSON(t *testing.T, args ...string) (int, contract.Envelope) {
	return runJSONWith(t, Dependencies{}, args...)
}

func runJSONWith(t *testing.T, deps Dependencies, args ...string) (int, contract.Envelope) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	deps.Stdout = &stdout
	deps.Stderr = &stderr
	code := Run(context.Background(), args, deps)
	var report contract.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout was not JSON: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	return code, report
}

func finalizedBundle(t *testing.T, status string, lcp, cls, score float64) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "run")
	requestedURL := "https://example.test/"
	store, err := bundle.Create(directory, bundle.Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: requestedURL}, appProtocol())
	if err != nil {
		t.Fatal(err)
	}
	samples := []bundle.SuccessfulSample{
		{Attempt: 1, Sample: appSample(lcp, cls, score)},
		{Attempt: 2, Sample: appSample(lcp, cls, score)},
		{Attempt: 3, Sample: appSample(lcp, cls, score)},
		{Attempt: 4, Sample: appSample(lcp, cls, score)},
		{Attempt: 5, Sample: appSample(lcp, cls, score)},
	}
	summary := bundle.Summary{
		SchemaVersion:  1,
		Status:         status,
		Profile:        "desktop-lab-v1",
		RequestedURL:   requestedURL,
		FinalURL:       "https://example.test/landing",
		RequestedRuns:  5,
		SuccessfulRuns: 5,
		Samples:        samples,
		Metrics:        appMetrics(samples),
	}
	if status == string(contract.Partial) {
		summary.Warnings = []string{"temporary browser cleanup failed"}
	}
	attempts := make([]bundle.Attempt, len(samples))
	artifacts := make([]bundle.Artifact, len(samples))
	for index := range samples {
		path := fmt.Sprintf("samples/run-%d.lhr.json", index+1)
		digest, err := store.WriteArtifact(path, []byte(appLHR(lcp, cls, score)))
		if err != nil {
			t.Fatal(err)
		}
		attempts[index] = bundle.Attempt{Number: index + 1, Status: "OK", Artifact: path}
		if status == string(contract.Partial) && index == 0 {
			attempts[index].Status = string(contract.Partial)
			attempts[index].Error = "temporary browser cleanup failed"
			attempts[index].CleanupFailed = true
		}
		artifacts[index] = bundle.Artifact{Kind: "lhr", Path: path, SHA256: digest}
	}
	if err := store.Finalize(bundle.Manifest{SchemaVersion: 1, Status: status, RequestedURL: requestedURL, FinalURL: summary.FinalURL, Attempts: attempts, Artifacts: artifacts}, &summary); err != nil {
		t.Fatal(err)
	}
	return directory
}

func pendingBundle(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "run")
	if _, err := bundle.Create(directory, bundle.Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, appProtocol()); err != nil {
		t.Fatal(err)
	}
	return directory
}

func appProtocol() bundle.Protocol {
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

func appSample(value, cls, score float64) lhr.Sample {
	return lhr.Sample{LighthouseVersion: "13.4.1", FinalURL: "https://example.test/landing", PerformanceScore: score, FCP: value, LCP: value, SpeedIndex: value, TBT: value, CLS: cls, BenchmarkIndex: 1, LCPSelector: "main > img"}
}

func appLHR(value, cls, score float64) string {
	return fmt.Sprintf(`{
  "lighthouseVersion": "13.4.1",
  "finalDisplayedUrl": "https://example.test/landing",
  "categories": {"performance": {"score": %g}},
  "environment": {"benchmarkIndex": 1},
  "audits": {
    "first-contentful-paint": {"numericValue": %g},
    "largest-contentful-paint": {"numericValue": %g},
    "speed-index": {"numericValue": %g},
    "total-blocking-time": {"numericValue": %g},
    "cumulative-layout-shift": {"numericValue": %g},
    "lcp-breakdown-insight": {"details": {"items": [{"type": "node", "selector": "main > img"}]}}
  }
}`,
		score/100,
		value,
		value,
		value,
		value,
		cls,
	)
}

func appMetrics(samples []bundle.SuccessfulSample) bundle.Metrics {
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

type recordingRawRunner struct {
	args   []string
	result engine.Result
}

func (r *recordingRawRunner) Run(_ context.Context, _ engine.Runtime, args []string, stdout, stderr io.Writer) engine.Result {
	r.args = append([]string(nil), args...)
	_, _ = io.WriteString(stdout, "official stdout\n")
	_, _ = io.WriteString(stderr, "official stderr\n")
	return r.result
}

type needsSetupEngine struct{}

func (needsSetupEngine) Status(context.Context) engine.Status { return engine.NeedsSetup }

func (needsSetupEngine) Setup(context.Context) (engine.Status, error) {
	return engine.NeedsSetup, engine.ErrNeedsSetup
}

func (needsSetupEngine) Runtime(context.Context) (engine.Runtime, error) {
	return engine.Runtime{}, engine.ErrNeedsSetup
}

type cancelingEngine struct {
	cancel context.CancelFunc
}

type readyStatusEngine struct{}

func (readyStatusEngine) Status(context.Context) engine.Status { return engine.Ready }

func (readyStatusEngine) Setup(context.Context) (engine.Status, error) {
	return engine.Ready, nil
}

func (readyStatusEngine) Runtime(context.Context) (engine.Runtime, error) {
	return engine.Runtime{}, nil
}

func (e cancelingEngine) Status(context.Context) engine.Status {
	e.cancel()
	return engine.NeedsSetup
}

func (e cancelingEngine) Setup(context.Context) (engine.Status, error) {
	e.cancel()
	return engine.NeedsSetup, context.Canceled
}

func (e cancelingEngine) Runtime(context.Context) (engine.Runtime, error) {
	e.cancel()
	return engine.Runtime{}, context.Canceled
}

func availableProfileNames(t *testing.T, data any) []string {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		AvailableProfiles []struct {
			Name string `json:"name"`
		} `json:"availableProfiles"`
	}
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(value.AvailableProfiles))
	for i, item := range value.AvailableProfiles {
		names[i] = item.Name
	}
	return names
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
