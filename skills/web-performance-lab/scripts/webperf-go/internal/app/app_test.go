package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/collect"
	webcompare "github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/compare"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	webreport "github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/report"
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
	if !strings.Contains(stdout.String(), "report --run <dir> [--run <dir> ...] --out <new.html>") || !strings.Contains(stdout.String(), "report compare --baseline <dir> --candidate <dir> --out <new.html>") {
		t.Fatalf("help did not include report commands: %q", stdout.String())
	}
}

func TestReportSubcommandHelpExitsSuccessfully(t *testing.T) {
	for _, args := range [][]string{{"report", "--help"}, {"report", "compare", "--help"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := Run(context.Background(), args, Dependencies{Stdout: &stdout, Stderr: &stderr})
		if code != contract.ExitOK {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "Usage of report") {
			t.Fatalf("args=%v help=%q", args, stderr.String())
		}
		if !strings.Contains(stderr.String(), "-locale string") {
			t.Fatalf("args=%v help did not include locale: %q", args, stderr.String())
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

func TestEngineSetupReportsInvalidExistingTargetWithoutRetry(t *testing.T) {
	code, report := runJSONWith(t, Dependencies{Engine: invalidTargetEngine{}}, "--json", "engine", "setup")
	if code != contract.ExitNeedsSetup || report.Status != contract.NeedsSetup {
		t.Fatalf("code=%d status=%s", code, report.Status)
	}
	if report.Error == nil || report.Error.Code != "engine_target_invalid" {
		t.Fatalf("error=%+v", report.Error)
	}
	if report.Error.Message != "existing locked Lighthouse engine target is invalid" {
		t.Fatalf("message=%q", report.Error.Message)
	}
	if report.Error.Remediation != "stop and inspect the existing engine cache; do not delete, replace, or rerun setup automatically" {
		t.Fatalf("remediation=%q", report.Error.Remediation)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/private/cache") {
		t.Fatalf("report exposed engine path: %s", encoded)
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
	unsafeUserinfoURL := "https://user:secret" + "@" + "example.test"
	code := Run(context.Background(), []string{"--json", "collect", "--url", unsafeUserinfoURL, "--profile", "desktop-lab-v1", "--out", "run"}, Dependencies{Stdout: &stdout, Stderr: &stderr})
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

func TestReportCreatesDeterministicStandaloneHTMLFromVerifiedBundle(t *testing.T) {
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	out := filepath.Join(t.TempDir(), "report.html")

	code, envelope := runJSON(t, "--json", "report", "--run", directory, "--out", out)
	if code != contract.ExitOK || envelope.Status != contract.OK || envelope.Command != "report" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("report mode=%#o", got)
	}
	for _, required := range []string{
		"<!doctype html>",
		`<html lang="en">`,
		"Web Performance Evidence Report",
		"VERIFIED LAB EVIDENCE",
		"desktop-lab-v1",
		"5 / 5",
		"Performance score",
		"Largest Contentful Paint",
		"Median",
		"MAD",
		"IQR",
		"LCP element identity is withheld",
		"Protocol fingerprint",
		"Cannot Claim",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("HTML missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"<script",
		"<link rel=\"stylesheet\"",
		"src=\"http",
		"href=\"http",
		"main &gt; img",
		directory,
	} {
		if strings.Contains(string(contents), forbidden) {
			t.Fatalf("HTML contains forbidden %q", forbidden)
		}
	}

	second := filepath.Join(t.TempDir(), "report.html")
	code, secondEnvelope := runJSON(
		t,
		"--json",
		"report",
		"--locale",
		"en",
		"--run",
		directory,
		"--out",
		second,
	)
	if code != contract.ExitOK || secondEnvelope.Status != contract.OK {
		t.Fatalf("second render code=%d report=%+v", code, secondEnvelope)
	}
	secondContents, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, secondContents) {
		t.Fatal("default and explicit English rendered different HTML bytes")
	}
}

func TestReportSupportsExplicitSimplifiedChineseLocale(t *testing.T) {
	directory := finalizedBundle(t, "PARTIAL", 1500, 0.1, 70)
	outputDirectory := t.TempDir()
	first := filepath.Join(outputDirectory, "report-zh-cn.html")
	second := filepath.Join(outputDirectory, "report-zh-cn-repeat.html")

	code, envelope := runJSON(
		t,
		"--json",
		"report",
		"--locale",
		"zh-CN",
		"--run",
		directory,
		"--out",
		first,
	)
	if code != contract.ExitOK || envelope.Status != contract.OK {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	contents, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`<html lang="zh-CN">`,
		"网页性能证据报告",
		"证据状态",
		"性能得分",
		"最大内容绘制（LCP）",
		"LCP 诊断",
		"临时浏览器清理失败",
		"证据解读",
		"不可据此声明",
		"DIAGNOSTIC ONLY",
		"PARTIAL",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("Chinese HTML missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"Web Performance Evidence Report",
		"Evidence interpretation",
		"Cannot Claim",
		"No canonical warnings.",
	} {
		if strings.Contains(string(contents), forbidden) {
			t.Fatalf("Chinese HTML contains English UI copy %q", forbidden)
		}
	}

	code, repeatedEnvelope := runJSON(
		t,
		"--json",
		"report",
		"--locale",
		"zh-CN",
		"--run",
		directory,
		"--out",
		second,
	)
	if code != contract.ExitOK || repeatedEnvelope.Status != contract.OK {
		t.Fatalf("repeat code=%d report=%+v", code, repeatedEnvelope)
	}
	repeatedContents, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, repeatedContents) {
		t.Fatal("same evidence and locale rendered different HTML bytes")
	}
	encoded, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "locale") {
		t.Fatalf("locale changed the stable report success JSON: %s", encoded)
	}
}

func TestReportCompareSupportsExplicitSimplifiedChineseLocale(t *testing.T) {
	baseline := finalizedBundle(t, "OK", 1500, 0.1, 70)
	candidate := finalizedBundle(t, "OK", 1200, 0.2, 80)
	out := filepath.Join(t.TempDir(), "comparison-zh-cn.html")

	code, envelope := runJSON(
		t,
		"--json",
		"report",
		"compare",
		"--locale",
		"zh-CN",
		"--baseline",
		baseline,
		"--candidate",
		candidate,
		"--out",
		out,
	)
	if code != contract.ExitOK || envelope.Status != contract.OK {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`<html lang="zh-CN">`,
		"网页性能对比报告",
		"对比结论",
		"基线中位数",
		"候选中位数",
		"实质性阈值",
		"基线",
		"候选",
		"改善 · IMPROVEMENT",
		"回退 · REGRESSION",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("Chinese comparison HTML missing %q", required)
		}
	}
}

func TestReportRejectsUnsupportedLocaleBeforeReadingEvidence(t *testing.T) {
	out := filepath.Join(t.TempDir(), "report.html")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--json", "report", "--locale", "fr", "--run", "/missing-evidence", "--out", out},
		Dependencies{Stdout: &stdout, Stderr: &stderr},
	)
	var envelope contract.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout was not JSON: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if code != contract.ExitInvalidInput || envelope.Status != contract.InvalidInput {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "unsupported_locale" {
		t.Fatalf("report=%+v", envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("unsupported locale created output: %v", err)
	}
}

func TestReportCompareRejectsUnsupportedLocaleBeforeReadingEvidence(t *testing.T) {
	out := filepath.Join(t.TempDir(), "comparison.html")
	code, envelope := runJSON(
		t,
		"--json",
		"report",
		"compare",
		"--locale",
		"fr",
		"--baseline",
		"/missing-baseline",
		"--candidate",
		"/missing-candidate",
		"--out",
		out,
	)
	if code != contract.ExitInvalidInput || envelope.Status != contract.InvalidInput {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "unsupported_locale" {
		t.Fatalf("report=%+v", envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("unsupported locale created comparison output: %v", err)
	}
}

func TestReportAllowsVerifiedPartialBundleOnlyAsDiagnostic(t *testing.T) {
	directory := finalizedBundle(t, "PARTIAL", 1500, 0.1, 70)
	out := filepath.Join(t.TempDir(), "diagnostic.html")

	code, envelope := runJSON(t, "--json", "report", "--run", directory, "--out", out)
	if code != contract.ExitOK || envelope.Status != contract.OK {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"DIAGNOSTIC ONLY", "PARTIAL", "not release acceptance"} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("diagnostic HTML missing %q", required)
		}
	}
	encoded, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"reportClass":"DIAGNOSTIC ONLY"`, `"evidenceStatus":"PARTIAL"`, `"outputCreated":true`} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("diagnostic JSON missing %q: %s", required, encoded)
		}
	}
}

func TestInspectExposesVerifiedPartialAggregateBranch(t *testing.T) {
	directory := finalizedBundle(t, "PARTIAL", 1500, 0.1, 70)
	code, envelope := runJSON(t, "--json", "inspect", "--run", directory)
	if code != contract.ExitGeneralError || envelope.Status != contract.Inconclusive || envelope.Error == nil || envelope.Error.Code != "aggregate_not_final" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	encoded, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"status":"PARTIAL"`, `"aggregateAvailable":true`} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("partial inspect JSON missing %q: %s", required, encoded)
		}
	}
}

func TestReportCombinesDistinctProfilesForOneTarget(t *testing.T) {
	desktop := finalizedBundleWithProfile(t, "OK", "desktop-lab-v1", 1500, 0.1, 70)
	mobile := finalizedBundleWithProfile(t, "OK", "mobile-lab-v1", 2200, 0.12, 60)
	out := filepath.Join(t.TempDir(), "profiles.html")

	code, envelope := runJSON(t, "--json", "report", "--run", mobile, "--run", desktop, "--out", out)
	if code != contract.ExitOK || envelope.Status != contract.OK {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, profileName := range []string{"desktop-lab-v1", "mobile-lab-v1"} {
		if strings.Count(string(contents), ">"+profileName+"<") < 1 {
			t.Fatalf("combined report missing %q", profileName)
		}
	}
	if first, second := strings.Index(string(contents), "desktop-lab-v1"), strings.Index(string(contents), "mobile-lab-v1"); first < 0 || second < 0 || first > second {
		t.Fatalf("profiles were not rendered deterministically: desktop=%d mobile=%d", first, second)
	}
}

func TestReportRejectsDuplicateProfileWithoutCreatingOutput(t *testing.T) {
	first := finalizedBundle(t, "OK", 1500, 0.1, 70)
	second := finalizedBundle(t, "OK", 1600, 0.1, 68)
	out := filepath.Join(t.TempDir(), "profiles.html")

	code, envelope := runJSON(t, "--json", "report", "--run", first, "--run", second, "--out", out)
	if code != contract.ExitInvalidInput || envelope.Error == nil || envelope.Error.Code != "duplicate_profile" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("duplicate-profile output exists: %v", err)
	}
}

func TestReportRejectsDifferentRequestedURLsWithoutCreatingOutput(t *testing.T) {
	first := finalizedBundleWithProfileAndURL(t, "OK", "desktop-lab-v1", "https://example.test/", 1500, 0.1, 70)
	second := finalizedBundleWithProfileAndURL(t, "OK", "mobile-lab-v1", "https://other.example.test/", 1600, 0.1, 68)
	out := filepath.Join(t.TempDir(), "profiles.html")

	code, envelope := runJSON(t, "--json", "report", "--run", first, "--run", second, "--out", out)
	if code != contract.ExitInvalidInput || envelope.Error == nil || envelope.Error.Code != "different_target" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("different-target output exists: %v", err)
	}
}

func TestReportRejectsMultipleProfilesWhenRequestedURLHasQuery(t *testing.T) {
	requestedURL := "https://example.test/?variant=REDACTED"
	first := finalizedBundleWithProfileAndURL(t, "OK", "desktop-lab-v1", requestedURL, 1500, 0.1, 70)
	second := finalizedBundleWithProfileAndURL(t, "OK", "mobile-lab-v1", requestedURL, 1600, 0.1, 68)
	out := filepath.Join(t.TempDir(), "profiles.html")

	code, envelope := runJSON(t, "--json", "report", "--run", first, "--run", second, "--out", out)
	if code != contract.ExitInvalidInput || envelope.Error == nil || envelope.Error.Code != "ambiguous_target" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("ambiguous-target output exists: %v", err)
	}
}

func TestReportRejectsMultipleProfilesWhenRequestedURLHasEmptyQuery(t *testing.T) {
	requestedURL := "https://example.test/?"
	first := finalizedBundleWithProfileAndURL(t, "OK", "desktop-lab-v1", requestedURL, 1500, 0.1, 70)
	second := finalizedBundleWithProfileAndURL(t, "OK", "mobile-lab-v1", requestedURL, 1600, 0.1, 68)
	out := filepath.Join(t.TempDir(), "profiles.html")

	code, envelope := runJSON(t, "--json", "report", "--run", first, "--run", second, "--out", out)
	if code != contract.ExitInvalidInput || envelope.Error == nil || envelope.Error.Code != "ambiguous_target" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("empty-query target output exists: %v", err)
	}
}

func TestReportCleanupFailureTakesPriorityOverInvalidOutput(t *testing.T) {
	envelope := reportWriteFailure("report", errors.Join(webreport.ErrInvalidOutput, webreport.ErrCleanupFailed))
	if envelope.Status != contract.ReportFailed || envelope.Error == nil || envelope.Error.Code != "report_cleanup_failed" {
		t.Fatalf("report=%+v", envelope)
	}
}

func TestReportCompareCreatesOneStrictComparisonHTML(t *testing.T) {
	baseline := finalizedBundle(t, "OK", 1500, 0.1, 70)
	candidate := finalizedBundle(t, "OK", 1200, 0.2, 80)
	out := filepath.Join(t.TempDir(), "comparison.html")

	code, envelope := runJSON(t, "--json", "report", "compare", "--baseline", baseline, "--candidate", candidate, "--out", out)
	if code != contract.ExitOK || envelope.Status != contract.OK || envelope.Command != "report compare" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Web Performance Comparison Report",
		"MIXED",
		"Baseline",
		"Candidate",
		"NO MATERIAL CHANGE",
		"IMPROVEMENT",
		"REGRESSION",
		"Materiality floor",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("comparison HTML missing %q", required)
		}
	}
}

func TestReportCompareRejectsPartialEvidenceWithoutCreatingOutput(t *testing.T) {
	baseline := finalizedBundle(t, "OK", 1500, 0.1, 70)
	candidate := finalizedBundle(t, "PARTIAL", 1200, 0.1, 80)
	out := filepath.Join(t.TempDir(), "comparison.html")

	code, envelope := runJSON(t, "--json", "report", "compare", "--baseline", baseline, "--candidate", candidate, "--out", out)
	if code == contract.ExitOK || envelope.Status != contract.Inconclusive || envelope.Error == nil || envelope.Error.Code != "incomplete_bundle" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("comparison output exists after rejection: %v", err)
	}
}

func TestReportCompareRejectsIncompatibleProfilesWithoutCreatingOutput(t *testing.T) {
	baseline := finalizedBundleWithProfile(t, "OK", "desktop-lab-v1", 1500, 0.1, 70)
	candidate := finalizedBundleWithProfile(t, "OK", "mobile-lab-v1", 1200, 0.1, 80)
	out := filepath.Join(t.TempDir(), "comparison.html")

	code, envelope := runJSON(t, "--json", "report", "compare", "--baseline", baseline, "--candidate", candidate, "--out", out)
	if code == contract.ExitOK || envelope.Status != contract.IncompatibleProtocol || envelope.Error == nil || envelope.Error.Code != "incompatible_protocol" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("incompatible comparison output exists: %v", err)
	}
}

func TestReportNeverOverwritesExistingOutput(t *testing.T) {
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	out := filepath.Join(t.TempDir(), "report.html")
	if err := os.WriteFile(out, []byte("keep me"), 0o640); err != nil {
		t.Fatal(err)
	}

	code, envelope := runJSON(t, "--json", "report", "--run", directory, "--out", out)
	if code != contract.ExitInvalidInput || envelope.Status != contract.InvalidInput || envelope.Error == nil || envelope.Error.Code != "out_exists" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "keep me" {
		t.Fatalf("existing output changed: %q", contents)
	}
}

func TestReportDoesNotEchoCallerOutputPath(t *testing.T) {
	if !webreport.OutputSupported() {
		t.Skip("private report output is unsupported on this platform")
	}
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	out := filepath.Join(t.TempDir(), "private-user-path", "report.html")
	if err := os.Mkdir(filepath.Dir(out), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"--json", "report", "--run", directory, "--out", out}, Dependencies{Stdout: &stdout, Stderr: &stderr})
	if code != contract.ExitOK {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), out) || strings.Contains(stderr.String(), out) || strings.Contains(stdout.String(), filepath.Dir(out)) {
		t.Fatalf("report output leaked caller path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	for _, required := range []string{`"outputCreated":true`, `"evidenceStatus":"OK"`} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("report output missing %q: %s", required, stdout.String())
		}
	}
}

func TestReportRejectsOutputInsideInputEvidenceBundle(t *testing.T) {
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	before := directoryEntries(t, directory)
	out := filepath.Join(directory, "report.html")

	code, envelope := runJSON(t, "--json", "report", "--run", directory, "--out", out)
	if code != contract.ExitInvalidInput || envelope.Status != contract.InvalidInput || envelope.Error == nil || envelope.Error.Code != "out_inside_bundle" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("inside-bundle output exists: %v", err)
	}
	if got := directoryEntries(t, directory); !equalStrings(got, before) {
		t.Fatalf("bundle changed: before=%v after=%v", before, got)
	}
}

func TestReportRejectsSymlinkedOutputParentInsideInputEvidenceBundle(t *testing.T) {
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	link := filepath.Join(t.TempDir(), "evidence-link")
	if err := os.Symlink(directory, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	out := filepath.Join(link, "report.html")

	code, envelope := runJSON(t, "--json", "report", "--run", directory, "--out", out)
	if code != contract.ExitInvalidInput || envelope.Error == nil || envelope.Error.Code != "out_inside_bundle" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(filepath.Join(directory, "report.html")); !os.IsNotExist(err) {
		t.Fatalf("symlinked inside-bundle output exists: %v", err)
	}
}

func TestReportRequiresExplicitOutputAndVerifiedAggregate(t *testing.T) {
	verified := finalizedBundle(t, "OK", 1500, 0.1, 70)
	code, envelope := runJSON(t, "--json", "report", "--run", verified)
	if code != contract.ExitInvalidInput || envelope.Error == nil || envelope.Error.Code != "out_required" {
		t.Fatalf("missing out code=%d report=%+v", code, envelope)
	}

	pending := pendingBundle(t)
	out := filepath.Join(t.TempDir(), "report.html")
	code, envelope = runJSON(t, "--json", "report", "--run", pending, "--out", out)
	if code == contract.ExitOK || envelope.Status != contract.Inconclusive || envelope.Error == nil || envelope.Error.Code != "aggregate_unavailable" {
		t.Fatalf("pending code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("report output exists for pending evidence: %v", err)
	}
}

func TestReportRejectsTamperedLHRWithoutCreatingOutput(t *testing.T) {
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	if err := os.WriteFile(filepath.Join(directory, "samples", "run-1.lhr.json"), []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "report.html")

	code, envelope := runJSON(t, "--json", "report", "--run", directory, "--out", out)
	if code == contract.ExitOK || envelope.Status != contract.Inconclusive || envelope.Error == nil || envelope.Error.Code != "invalid_bundle" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("tampered-evidence output exists: %v", err)
	}
}

func TestInspectAndCompareDoNotCreateHTMLArtifacts(t *testing.T) {
	baseline := finalizedBundle(t, "OK", 1500, 0.1, 70)
	candidate := finalizedBundle(t, "OK", 1200, 0.1, 80)
	beforeBaseline := directoryEntries(t, baseline)
	beforeCandidate := directoryEntries(t, candidate)

	if code, envelope := runJSON(t, "--json", "inspect", "--run", baseline); code != contract.ExitOK || envelope.Status != contract.OK {
		t.Fatalf("inspect code=%d report=%+v", code, envelope)
	}
	if code, envelope := runJSON(t, "--json", "compare", "--baseline", baseline, "--candidate", candidate); code != contract.ExitOK || envelope.Status != contract.OK {
		t.Fatalf("compare code=%d report=%+v", code, envelope)
	}
	if got := directoryEntries(t, baseline); !equalStrings(got, beforeBaseline) {
		t.Fatalf("inspect changed baseline bundle: before=%v after=%v", beforeBaseline, got)
	}
	if got := directoryEntries(t, candidate); !equalStrings(got, beforeCandidate) {
		t.Fatalf("compare changed candidate bundle: before=%v after=%v", beforeCandidate, got)
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
	if !webreport.OutputSupported() {
		for _, arg := range args {
			if arg == "--json" {
				continue
			}
			if arg == "report" {
				t.Skip("private report output is unsupported on this platform")
			}
			break
		}
	}
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
	return finalizedBundleWithProfile(t, status, "desktop-lab-v1", lcp, cls, score)
}

func finalizedBundleWithProfile(t *testing.T, status, profileName string, lcp, cls, score float64) string {
	return finalizedBundleWithProfileAndURL(t, status, profileName, "https://example.test/", lcp, cls, score)
}

func finalizedBundleWithProfileAndURL(t *testing.T, status, profileName, requestedURL string, lcp, cls, score float64) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "run")
	protocol := appProtocolFor(t, profileName)
	store, err := bundle.Create(directory, bundle.Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: requestedURL}, protocol)
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
		Profile:        profileName,
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

func directoryEntries(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

func appProtocol() bundle.Protocol {
	resolved, err := profile.Resolve("desktop-lab-v1")
	if err != nil {
		panic(err)
	}
	return bundle.CompleteProtocol(bundle.Protocol{
		SchemaVersion:     1,
		Profile:           resolved.Name,
		FormFactor:        resolved.FormFactor,
		ThrottlingMethod:  resolved.ThrottlingMethod,
		ResolvedFlags:     resolved.LighthouseArgs,
		RuntimeFlags:      bundle.ExpectedRuntimeFlags(),
		LighthouseVersion: "13.4.1",
		NodeVersion:       "24.16.0",
		ChromeVersion:     "150.0.0.0",
		OS:                "darwin",
		Arch:              "arm64",
	})
}

func appProtocolFor(t *testing.T, profileName string) bundle.Protocol {
	t.Helper()
	resolved, err := profile.Resolve(profileName)
	if err != nil {
		t.Fatal(err)
	}
	return bundle.CompleteProtocol(bundle.Protocol{
		SchemaVersion:     1,
		Profile:           resolved.Name,
		FormFactor:        resolved.FormFactor,
		ThrottlingMethod:  resolved.ThrottlingMethod,
		ResolvedFlags:     resolved.LighthouseArgs,
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

type invalidTargetEngine struct{}

func (invalidTargetEngine) Status(context.Context) engine.Status { return engine.NeedsSetup }

func (invalidTargetEngine) Setup(context.Context) (engine.Status, error) {
	return engine.NeedsSetup, fmt.Errorf("/private/cache: %w", engine.ErrInvalidTarget)
}

func (invalidTargetEngine) Runtime(context.Context) (engine.Runtime, error) {
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
