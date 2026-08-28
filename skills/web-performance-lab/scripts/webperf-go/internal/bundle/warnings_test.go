package bundle

import (
	"reflect"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
)

func TestCanonicalWarningsDerivesSortedUniqueSignals(t *testing.T) {
	attempts := []Attempt{
		{Number: 1, Status: "PARTIAL", Artifact: "samples/run-1.lhr.json", Error: "temporary browser cleanup failed", CleanupFailed: true},
		{Number: 2, Status: "ENGINE_FAILED", Error: "engine failed"},
		{Number: 3, Status: "OK", Artifact: "samples/run-3.lhr.json"},
	}
	samples := []SuccessfulSample{
		{Attempt: 1, Sample: lhr.Sample{PerformanceScore: 80, FCP: 1000, LCP: 1500, SpeedIndex: 2000, TBT: 0, CLS: 0, BenchmarkIndex: 1}},
		{Attempt: 3, Sample: lhr.Sample{PerformanceScore: 95, FCP: 1100, LCP: 1700, SpeedIndex: 2300, TBT: 100, CLS: .10, BenchmarkIndex: 1, LCPSelector: "main > img"}},
	}

	got := CanonicalWarnings(attempts, samples, WarningSignals{
		InvalidFinalURL:           true,
		RawFinalURLs:              []string{"https://example.test/one?token=one", "https://example.test/two?token=two"},
		LighthouseWarningAttempts: []int{3, 3},
	})
	want := []string{
		"LCP selector changed across successful samples",
		"Lighthouse reported an invalid final URL",
		"Lighthouse reported warnings on attempt 3",
		"final URL changed across successful samples",
		"high dispersion for CLS",
		"high dispersion for LCP",
		"high dispersion for Performance Score",
		"high dispersion for Speed Index",
		"high dispersion for TBT",
		"one or more attempts failed",
		"temporary browser cleanup failed",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CanonicalWarnings() = %v, want %v", got, want)
	}
}
