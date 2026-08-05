package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
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
			code, report := runJSON(t, tt.args...)
			if code != contract.ExitNeedsSetup || report.Status != contract.NeedsSetup {
				t.Fatalf("code=%d status=%s", code, report.Status)
			}
		})
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
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), args, Dependencies{Stdout: &stdout, Stderr: &stderr})
	var report contract.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout was not JSON: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	return code, report
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
