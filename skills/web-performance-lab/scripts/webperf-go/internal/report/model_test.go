package report

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

func TestBuildSingleSelectsLowestAttemptWhenMedianLCPDistanceTies(t *testing.T) {
	directory := reportBundleWithLCPs(t, []float64{1400, 1490, 1510, 1600})
	document, err := BuildSingle([]string{directory})
	if err != nil {
		t.Fatal(err)
	}
	representative := document.Runs[0].Representative
	if representative.Attempt != 2 {
		t.Fatalf("representative attempt=%d", representative.Attempt)
	}
	if len(representative.Opportunities) != 1 || representative.Opportunities[0].SavingsMS != 222 {
		t.Fatalf("representative opportunities=%+v", representative.Opportunities)
	}
	contents, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "data-secret") || strings.Contains(string(contents), "customer-42") {
		t.Fatalf("report exposed raw LCP selector: %s", contents)
	}
}

func reportBundleWithLCPs(t *testing.T, lcps []float64) string {
	t.Helper()
	resolved, err := profile.Resolve("desktop-lab-v1")
	if err != nil {
		t.Fatal(err)
	}
	protocol := bundle.CompleteProtocol(bundle.Protocol{
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
	directory := filepath.Join(t.TempDir(), "run")
	store, err := bundle.Create(directory, bundle.Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]bundle.SuccessfulSample, len(lcps))
	attempts := make([]bundle.Attempt, len(lcps))
	artifacts := make([]bundle.Artifact, len(lcps))
	for index, value := range lcps {
		attempt := index + 1
		sample := lhr.Sample{
			LighthouseVersion: "13.4.1",
			FinalURL:          "https://example.test/landing",
			PerformanceScore:  70,
			FCP:               value,
			LCP:               value,
			SpeedIndex:        value,
			TBT:               value,
			CLS:               0.1,
			BenchmarkIndex:    1,
			LCPSelector:       `main > img[data-secret='customer-42']`,
		}
		samples[index] = bundle.SuccessfulSample{Attempt: attempt, Sample: sample}
		artifactPath := fmt.Sprintf("samples/run-%d.lhr.json", attempt)
		digest, err := store.WriteArtifact(artifactPath, []byte(reportLHR(value, float64(attempt*111))))
		if err != nil {
			t.Fatal(err)
		}
		attempts[index] = bundle.Attempt{Number: attempt, Status: "OK", Artifact: artifactPath}
		artifacts[index] = bundle.Artifact{Kind: "lhr", Path: artifactPath, SHA256: digest}
	}
	values := append([]float64(nil), lcps...)
	metrics := bundle.Metrics{
		PerformanceScore: stats.Summarize(repeatedValue(len(lcps), 70)),
		FCP:              stats.Summarize(values),
		LCP:              stats.Summarize(values),
		SpeedIndex:       stats.Summarize(values),
		TBT:              stats.Summarize(values),
		CLS:              stats.Summarize(repeatedValue(len(lcps), 0.1)),
		BenchmarkIndex:   stats.Summarize(repeatedValue(len(lcps), 1)),
	}
	summary := &bundle.Summary{
		SchemaVersion:  1,
		Status:         "OK",
		Profile:        resolved.Name,
		RequestedURL:   "https://example.test/",
		FinalURL:       "https://example.test/landing",
		RequestedRuns:  len(lcps),
		SuccessfulRuns: len(lcps),
		Samples:        samples,
		Metrics:        metrics,
	}
	if err := store.Finalize(bundle.Manifest{
		SchemaVersion: 1,
		Status:        "OK",
		RequestedURL:  summary.RequestedURL,
		FinalURL:      summary.FinalURL,
		Attempts:      attempts,
		Artifacts:     artifacts,
	}, summary); err != nil {
		t.Fatal(err)
	}
	return directory
}

func repeatedValue(count int, value float64) []float64 {
	values := make([]float64, count)
	for index := range values {
		values[index] = value
	}
	return values
}

func reportLHR(value, savings float64) string {
	return fmt.Sprintf(`{
  "lighthouseVersion": "13.4.1",
  "finalDisplayedUrl": "https://example.test/landing",
  "categories": {"performance": {"score": 0.7}},
  "environment": {"benchmarkIndex": 1},
  "audits": {
    "first-contentful-paint": {"numericValue": %[1]g},
    "largest-contentful-paint": {"numericValue": %[1]g},
    "speed-index": {"numericValue": %[1]g},
    "total-blocking-time": {"numericValue": %[1]g},
    "cumulative-layout-shift": {"numericValue": 0.1},
    "lcp-breakdown-insight": {"details": {"items": [{"type":"node","selector":"main > img[data-secret='customer-42']"}]}},
    "server-response-time": {"details": {"overallSavingsMs": %[2]g}}
  }
}`, value, savings)
}
