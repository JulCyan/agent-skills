package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/collect"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
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

func TestFutureCommandsReturnStructuredNeedsSetup(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "doctor", args: []string{"--json", "doctor"}},
		{name: "engine status", args: []string{"--json", "engine", "status"}},
		{name: "engine setup", args: []string{"--json", "engine", "setup"}},
		{name: "collect", args: []string{"--json", "collect", "--url", "https://example.test", "--profile", "desktop-lab-v1", "--out", "run"}},
		{name: "inspect", args: []string{"--json", "inspect", "--run", "run"}},
		{name: "compare", args: []string{"--json", "compare", "--baseline", "base", "--candidate", "candidate"}},
		{name: "raw", args: []string{"--json", "raw", "lighthouse", "--", "--help"}},
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
