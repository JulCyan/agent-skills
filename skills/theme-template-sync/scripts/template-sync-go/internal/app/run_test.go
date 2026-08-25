package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/templatejson"
)

func TestRun_Help(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--help"},
		&stdout,
		&stderr,
		Dependencies{},
	)

	if code != 0 {
		t.Fatalf("Run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "theme-template-sync safely synchronizes Shopify JSON templates") {
		t.Fatalf("Run() stdout = %q, want help summary", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run() stderr = %q, want empty", stderr.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"destroy"},
		&stdout,
		&stderr,
		Dependencies{},
	)

	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if result.Status != StatusFailed || result.FailureKind != FailureUsage {
		t.Fatalf("Run() result = %#v, want FAILED/USAGE", result)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("Run() stderr = %q, want unknown command diagnostic", stderr.String())
	}
}

func TestRun_PlanWithoutAdapterDoesNotPanic(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"plan"},
		&stdout,
		&stderr,
		Dependencies{},
	)

	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decoding stdout: %v", err)
	}
	if result.FailureKind != FailureNeedsSetup {
		t.Fatalf("Run() failure kind = %q, want %q", result.FailureKind, FailureNeedsSetup)
	}
}

func TestRun_PlanEmitsOneMachineReadableResult(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{
			"plan",
			"--template", "page.example",
			"--source-store", "store-source",
			"--source-theme", "Source",
			"--target-store", "store-one",
			"--target-theme", "Target One",
			"--mode", "field",
			"--section", "hero",
			"--field", "custom_css",
			"--runtime-root", "run",
		},
		&stdout,
		&stderr,
		testDependencies(t, remote, caller),
	)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	var result Result
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains trailing JSON content: %v", err)
	}
	if result.Status != StatusPlanned || result.PlanPath != filepath.Join(caller, "run", "plan.json") {
		t.Fatalf("result = %#v, want PLANNED caller-relative plan", result)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoWrites(t, remote)
}

func TestRun_InvalidTargetPairingCallsNoAdapter(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{
			"plan",
			"--template", "page.example",
			"--source-store", "store-source",
			"--source-theme", "Source",
			"--target-store", "store-one",
		},
		&stdout,
		&stderr,
		testDependencies(t, remote, caller),
	)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if len(remote.Adapter.Calls()) != 0 {
		t.Fatalf("adapter calls = %v, want none", remote.Adapter.Calls())
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if result.FailureKind != FailureUsage {
		t.Fatalf("failure kind = %q, want USAGE", result.FailureKind)
	}
}

func TestRun_UsageFailureRedactsCredentialShapedArguments(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	const secret = "synthetic-usage-secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"plan", "SHOPIFY_ACCESS_TOKEN=" + secret},
		&stdout,
		&stderr,
		testDependencies(t, remote, caller),
	)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	for stream, value := range map[string]string{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	} {
		if strings.Contains(value, secret) {
			t.Fatalf("%s leaked credential-shaped argument: %q", stream, value)
		}
		if !strings.Contains(value, "[redacted]") {
			t.Fatalf("%s = %q, want redaction marker", stream, value)
		}
	}
	if len(remote.Adapter.Calls()) != 0 {
		t.Fatalf("adapter calls = %v, want none", remote.Adapter.Calls())
	}
}

func TestRun_UnknownCommandRedactsEscapedJSONCredential(t *testing.T) {
	t.Parallel()

	const secret = "synthetic-command-secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{`"password":"` + secret + `"`},
		&stdout,
		&stderr,
		Dependencies{},
	)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	for stream, value := range map[string]string{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	} {
		if strings.Contains(value, secret) {
			t.Fatalf("%s leaked escaped JSON credential: %q", stream, value)
		}
		if !strings.Contains(value, "[redacted]") {
			t.Fatalf("%s = %q, want redaction marker", stream, value)
		}
	}
}

func TestRun_UsageFailureRedactsAuthorizationBasicCredential(t *testing.T) {
	t.Parallel()

	const secret = "synthetic-basic-secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"Authorization: Basic " + secret},
		&stdout,
		&stderr,
		Dependencies{},
	)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	for stream, value := range map[string]string{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	} {
		if strings.Contains(value, secret) {
			t.Fatalf("%s leaked authorization credential: %q", stream, value)
		}
		if !strings.Contains(value, "[redacted]") {
			t.Fatalf("%s = %q, want redaction marker", stream, value)
		}
	}
}

func TestRun_UsageFailureRedactsWholeCookieHeader(t *testing.T) {
	t.Parallel()

	const firstSecret = "synthetic-cookie-first"
	const secondSecret = "synthetic-cookie-second"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"Cookie: first=" + firstSecret + "; session=" + secondSecret},
		&stdout,
		&stderr,
		Dependencies{},
	)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	for stream, value := range map[string]string{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	} {
		for _, secret := range []string{firstSecret, secondSecret} {
			if strings.Contains(value, secret) {
				t.Fatalf("%s leaked cookie credential: %q", stream, value)
			}
		}
		if !strings.Contains(value, "[redacted]") {
			t.Fatalf("%s = %q, want redaction marker", stream, value)
		}
	}
}

func TestRun_UsageFailureRedactsWholeAuthorizationDigestHeader(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"synthetic-digest-user",
		"synthetic-digest-response",
		"synthetic-digest-nonce",
	}
	argument := `Authorization: Digest username="` + secrets[0] + `", response="` +
		secrets[1] + `", nonce="` + secrets[2] + `"`
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{argument},
		&stdout,
		&stderr,
		Dependencies{},
	)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	for stream, value := range map[string]string{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	} {
		for _, secret := range secrets {
			if strings.Contains(value, secret) {
				t.Fatalf("%s leaked authorization credential: %q", stream, value)
			}
		}
		if !strings.Contains(value, "[redacted]") {
			t.Fatalf("%s = %q, want redaction marker", stream, value)
		}
	}
}

func TestRun_ReadOnlyCancellationExitsTwo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		execute bool
		plan    bool
	}{
		{name: "plan", plan: true},
		{name: "apply preview"},
		{name: "execute all-target preflight", execute: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			caller := t.TempDir()
			remote := standardRemote(t)
			dependencies := testDependencies(t, remote, caller)
			args := []string{
				"plan",
				"--template", "page.example",
				"--source-store", "store-source",
				"--source-theme", "Source",
				"--target-store", "store-one",
				"--target-theme", "Target One",
				"--mode", "field",
				"--section", "hero",
				"--field", "custom_css",
				"--runtime-root", "run",
			}
			if !test.plan {
				planResult, err := CreatePlan(
					context.Background(),
					standardPlanOptions(filepath.Join(caller, "run")),
					dependencies,
				)
				if err != nil {
					t.Fatalf("CreatePlan(): %v", err)
				}
				if test.execute {
					if _, err := Apply(
						context.Background(),
						ApplyOptions{PlanPath: planResult.PlanPath},
						dependencies,
					); err != nil {
						t.Fatalf("Apply() preview: %v", err)
					}
				}
				args = []string{"apply", "--plan", planResult.PlanPath}
				if test.execute {
					args = append(args, "--execute")
				}
			}

			remote.setResolveOverride(func(
				_ int,
				_ adapter.StoreRef,
				_ adapter.ThemeRef,
			) (adapter.ResolvedTheme, error) {
				return adapter.ResolvedTheme{}, context.Canceled
			})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Run(ctx, args, &stdout, &stderr, dependencies)
			if code != 2 {
				t.Fatalf("Run() code = %d, want 2; stderr = %q", code, stderr.String())
			}
			var result Result
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("decoding result: %v", err)
			}
			if result.Status != StatusFailed || result.FailureKind != FailureInterrupted {
				t.Fatalf("result = %#v, want FAILED/INTERRUPTED", result)
			}
			assertNoWrites(t, remote)
		})
	}
}

func TestRun_PreviewLeaseCleanupFailureExitsThree(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	planResult, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	tamperedLease := false
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if !tamperedLease {
			tamperedLease = true
			leasePath := filepath.Join(runtimeRoot, ".apply-lease")
			if err := os.Remove(leasePath); err != nil {
				return nil, err
			}
			if err := os.Mkdir(leasePath, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(leasePath, "sentinel"), []byte("held"), 0o600); err != nil {
				return nil, err
			}
		}
		return remote.document(theme.ID), nil
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"apply", "--plan", planResult.PlanPath},
		&stdout,
		&stderr,
		testDependencies(t, remote, caller),
	)
	if code != 3 {
		t.Fatalf("Run() code = %d, want 3; stderr = %q", code, stderr.String())
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if result.Status != StatusFailed || result.FailureKind != FailureEvidence {
		t.Fatalf("result = %#v, want FAILED/EVIDENCE_FAILED", result)
	}
	assertNoWrites(t, remote)
}

func TestExitCodeForResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result Result
		want   int
	}{
		{name: "no-op", result: Result{Status: StatusNoOp}, want: 0},
		{name: "planned", result: Result{Status: StatusPlanned}, want: 0},
		{name: "applied", result: Result{Status: StatusApplied}, want: 0},
		{name: "usage", result: Result{Status: StatusFailed, FailureKind: FailureUsage}, want: 2},
		{name: "preflight", result: Result{Status: StatusFailed, FailureKind: FailurePreflight}, want: 2},
		{name: "read-only interruption", result: Result{Status: StatusFailed, FailureKind: FailureInterrupted}, want: 2},
		{name: "binding", result: Result{Status: StatusFailed, FailureKind: FailureBinding}, want: 2},
		{name: "write", result: Result{Status: StatusFailed, FailureKind: FailureWrite}, want: 3},
		{name: "evidence", result: Result{Status: StatusFailed, FailureKind: FailureEvidence}, want: 3},
		{name: "readback", result: Result{Status: StatusReadbackMismatch, FailureKind: FailureMismatch}, want: 3},
		{name: "partial", result: Result{Status: StatusPartial, FailureKind: FailureWrite}, want: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCodeForResult(test.result); got != test.want {
				t.Fatalf("exitCodeForResult() = %d, want %d", got, test.want)
			}
		})
	}
}
