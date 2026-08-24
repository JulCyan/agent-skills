//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
)

func TestReportReturnsStructuredUnsupportedPlatformFailure(t *testing.T) {
	directory := finalizedBundle(t, "OK", 1500, 0.1, 70)
	output := filepath.Join(t.TempDir(), "report.html")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"--json", "report", "--run", directory, "--out", output}, Dependencies{Stdout: &stdout, Stderr: &stderr})
	var envelope contract.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if code != contract.ExitGeneralError || envelope.Status != contract.ReportFailed || envelope.Error == nil || envelope.Error.Code != "report_platform_unsupported" {
		t.Fatalf("code=%d report=%+v", code, envelope)
	}
}
