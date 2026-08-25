package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/templatejson"
)

func TestApplyPreview_BindsCurrentReadOnlyPreflight(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult, err := CreatePlan(
		context.Background(),
		standardPlanOptions(filepath.Join(caller, "run")),
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(preview): %v", err)
	}
	if result.Status != StatusPlanned {
		t.Fatalf("status = %q, want PLANNED", result.Status)
	}
	assertNoWrites(t, remote)

	manifest, err := LoadManifest(filepath.Join(caller, "run", "manifest.json"))
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	if manifest.PreviewBinding == nil {
		t.Fatal("preview binding = nil")
	}
	if manifest.PreviewBinding.PlanSHA256 != planResult.PlanSHA256 {
		t.Fatalf("binding plan SHA = %q, want %q", manifest.PreviewBinding.PlanSHA256, planResult.PlanSHA256)
	}
	if len(manifest.PreviewBinding.Targets) != 2 {
		t.Fatalf("binding targets = %d, want 2", len(manifest.PreviewBinding.Targets))
	}
	if manifest.Status != StatusPlanned {
		t.Fatalf("manifest status = %q, want PLANNED", manifest.Status)
	}
}

func TestApplyPreview_NoOpDoesNotWrite(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.setDocument("201", `{"sections":{"hero":{"custom_css":[".hero { color: red; }"],"settings":{"keep":1}}},"order":["hero"],"target_unknown":"one"}`)
	remote.setDocument("202", `{"sections":{"hero":{"custom_css":[".hero { color: red; }"],"blocks":[{"id":"keep"}]}},"order":["hero"],"target_unknown":"two"}`)
	planResult, err := CreatePlan(
		context.Background(),
		standardPlanOptions(filepath.Join(caller, "run")),
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(preview): %v", err)
	}
	if result.Status != StatusNoOp {
		t.Fatalf("status = %q, want NO_OP", result.Status)
	}
	assertNoWrites(t, remote)
}

func TestApply_RejectsRuntimeSymlinkInsideCaller(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	remote := standardRemote(t)
	outsideRuntime := filepath.Join(outside, "theme-template-sync", "run")
	planResult, err := CreatePlan(
		context.Background(),
		standardPlanOptions(outsideRuntime),
		testDependencies(t, remote, outside),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}

	caller := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(caller, ".runtime")); err != nil {
		t.Fatalf("creating runtime symlink: %v", err)
	}
	planThroughSymlink := filepath.Join(
		caller,
		".runtime",
		"theme-template-sync",
		"run",
		"plan.json",
	)
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planThroughSymlink},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Apply() error = %q, want symlink rejection", err)
	}
	if planResult.Status != StatusPlanned {
		t.Fatalf("initial plan status = %q, want PLANNED", planResult.Status)
	}
	assertNoWrites(t, remote)
}

func TestApply_MissingPlanDoesNotCreateRuntimePath(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	missingParent := filepath.Join(caller, "src", "typo")
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: filepath.Join(missingParent, "plan.json")},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	if _, statErr := os.Lstat(filepath.Join(caller, "src")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid plan path created directories, stat error = %v", statErr)
	}
	if len(remote.Adapter.Calls()) != 0 {
		t.Fatalf("adapter calls = %v, want none", remote.Adapter.Calls())
	}
}

func TestApplyPreview_RejectsTamperedPlan(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult, err := CreatePlan(
		context.Background(),
		standardPlanOptions(filepath.Join(caller, "run")),
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	data, err := os.ReadFile(planResult.PlanPath)
	if err != nil {
		t.Fatalf("reading plan: %v", err)
	}
	var plan map[string]any
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decoding plan: %v", err)
	}
	plan["template"] = "product.tampered"
	data, err = json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatalf("encoding tampered plan: %v", err)
	}
	if err := os.WriteFile(planResult.PlanPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("writing tampered plan: %v", err)
	}

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(preview) error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	assertNoWrites(t, remote)
}

func TestApplyRejectsUnknownOrDuplicateEvidenceFieldsBeforeAdapterCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		artifact string
		member   string
	}{
		{name: "plan unknown field", artifact: "plan.json", member: `"unexpected":true`},
		{name: "plan duplicate field", artifact: "plan.json", member: `"schema_version":1`},
		{name: "manifest unknown field", artifact: "manifest.json", member: `"unexpected":true`},
		{name: "manifest duplicate field", artifact: "manifest.json", member: `"schema_version":1`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			caller := t.TempDir()
			remote := standardRemote(t)
			planResult, err := CreatePlan(
				context.Background(),
				standardPlanOptions(filepath.Join(caller, "run")),
				testDependencies(t, remote, caller),
			)
			if err != nil {
				t.Fatalf("CreatePlan(): %v", err)
			}
			artifactPath := filepath.Join(caller, "run", test.artifact)
			appendObjectMember(t, artifactPath, test.member)
			callsBefore := len(remote.Adapter.Calls())

			result, err := Apply(
				context.Background(),
				ApplyOptions{PlanPath: planResult.PlanPath},
				testDependencies(t, remote, caller),
			)
			if err == nil {
				t.Fatal("Apply() error = nil")
			}
			if result.Status != StatusFailed || result.FailureKind != FailurePlan {
				t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
			}
			if callsAfter := len(remote.Adapter.Calls()); callsAfter != callsBefore {
				t.Fatalf("adapter calls = %d, want unchanged %d", callsAfter, callsBefore)
			}
		})
	}
}

func appendObjectMember(t *testing.T, path string, member string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[len(trimmed)-1] != '}' {
		t.Fatalf("artifact %s is not a JSON object", path)
	}
	updated := append([]byte{}, trimmed[:len(trimmed)-1]...)
	updated = append(updated, []byte(",\n  "+member+"\n}\n")...)
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestApplyRejectsManifestTamperingBeforeAdapterCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reseal bool
		mutate func(*Manifest)
	}{
		{
			name:   "broken manifest hash",
			reseal: false,
			mutate: func(manifest *Manifest) { manifest.RunID = "tampered-run" },
		},
		{
			name:   "rehashed run id",
			reseal: true,
			mutate: func(manifest *Manifest) { manifest.RunID = "tampered-run" },
		},
		{
			name:   "rehashed template",
			reseal: true,
			mutate: func(manifest *Manifest) { manifest.Template = "product.tampered" },
		},
		{
			name:   "rehashed scope",
			reseal: true,
			mutate: func(manifest *Manifest) { manifest.Scope.SectionKey = "other" },
		},
		{
			name:   "rehashed target index",
			reseal: true,
			mutate: func(manifest *Manifest) { manifest.Targets[0].Index = 7 },
		},
		{
			name:   "rehashed target status",
			reseal: true,
			mutate: func(manifest *Manifest) { manifest.Targets[0].Status = TargetNoOp },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			caller := t.TempDir()
			remote := standardRemote(t)
			planResult, err := CreatePlan(
				context.Background(),
				standardPlanOptions(filepath.Join(caller, "run")),
				testDependencies(t, remote, caller),
			)
			if err != nil {
				t.Fatalf("CreatePlan(): %v", err)
			}
			manifestPath := filepath.Join(caller, "run", "manifest.json")
			manifest, err := LoadManifest(manifestPath)
			if err != nil {
				t.Fatalf("LoadManifest(): %v", err)
			}
			test.mutate(&manifest)
			writeManifestTestFile(t, manifestPath, &manifest, test.reseal)
			callsBefore := len(remote.Adapter.Calls())

			result, err := Apply(
				context.Background(),
				ApplyOptions{PlanPath: planResult.PlanPath},
				testDependencies(t, remote, caller),
			)
			if err == nil {
				t.Fatal("Apply() error = nil")
			}
			if result.Status != StatusFailed || result.FailureKind != FailurePlan {
				t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
			}
			if callsAfter := len(remote.Adapter.Calls()); callsAfter != callsBefore {
				t.Fatalf("adapter calls = %d after tampering, want unchanged %d", callsAfter, callsBefore)
			}
		})
	}
}

func TestApplyRejectsRehashedPreviewBindingTamperingBeforeAdapterCalls(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult, err := CreatePlan(
		context.Background(),
		standardPlanOptions(filepath.Join(caller, "run")),
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	if _, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	); err != nil {
		t.Fatalf("Apply(preview): %v", err)
	}
	manifestPath := filepath.Join(caller, "run", "manifest.json")
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	manifest.PreviewBinding.Template = "product.tampered"
	writeManifestTestFile(t, manifestPath, &manifest, true)
	callsBefore := len(remote.Adapter.Calls())

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	if callsAfter := len(remote.Adapter.Calls()); callsAfter != callsBefore {
		t.Fatalf("adapter calls = %d after tampering, want unchanged %d", callsAfter, callsBefore)
	}
}

func writeManifestTestFile(
	t *testing.T,
	path string,
	manifest *Manifest,
	reseal bool,
) {
	t.Helper()
	if reseal {
		if err := manifest.seal(); err != nil {
			t.Fatalf("sealing manifest: %v", err)
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshaling manifest: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
}

func TestApplyPreview_FailsOnRemoteDriftBeforeMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		drift func(*remoteFixture)
	}{
		{
			name: "source template drift",
			drift: func(remote *remoteFixture) {
				remote.setDocument("101", `{"sections":{"hero":{"custom_css":["changed"]}},"order":["hero"]}`)
			},
		},
		{
			name: "target template drift",
			drift: func(remote *remoteFixture) {
				remote.setDocument("201", `{"sections":{"hero":{"custom_css":["changed"],"settings":{"keep":1}}},"order":["hero"]}`)
			},
		},
		{
			name: "later target invalid JSON",
			drift: func(remote *remoteFixture) {
				remote.setDocument("202", `{"sections":`)
			},
		},
		{
			name: "target becomes Live",
			drift: func(remote *remoteFixture) {
				remote.addTheme("store-two", "Target Two", "202", "Target Two", "main", string(remote.document("202")))
			},
		},
		{
			name: "target identity changes",
			drift: func(remote *remoteFixture) {
				remote.addTheme("store-two", "Target Two", "999", "Target Two", "unpublished", string(remote.document("202")))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := t.TempDir()
			remote := standardRemote(t)
			planResult, err := CreatePlan(
				context.Background(),
				standardPlanOptions(filepath.Join(caller, "run")),
				testDependencies(t, remote, caller),
			)
			if err != nil {
				t.Fatalf("CreatePlan(): %v", err)
			}
			test.drift(remote)
			result, err := Apply(
				context.Background(),
				ApplyOptions{PlanPath: planResult.PlanPath},
				testDependencies(t, remote, caller),
			)
			if err == nil {
				t.Fatal("Apply(preview) error = nil")
			}
			if result.Status != StatusFailed {
				t.Fatalf("status = %q, want FAILED", result.Status)
			}
			assertNoWrites(t, remote)
		})
	}
}

func TestApplyPreview_LeaseCleanupFailureFailsClosed(t *testing.T) {
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
				return nil, fmt.Errorf("removing synthetic apply lease: %w", err)
			}
			if err := os.Mkdir(leasePath, 0o700); err != nil {
				return nil, fmt.Errorf("creating synthetic apply lease directory: %w", err)
			}
			if err := os.WriteFile(filepath.Join(leasePath, "sentinel"), []byte("held"), 0o600); err != nil {
				return nil, fmt.Errorf("writing synthetic apply lease sentinel: %w", err)
			}
		}
		return remote.document(theme.ID), nil
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(preview) error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailureEvidence {
		t.Fatalf("result = %#v, want FAILED/EVIDENCE_FAILED", result)
	}
	if !strings.Contains(result.Message, "apply lease") {
		t.Fatalf("result message = %q, want apply lease cleanup failure", result.Message)
	}
	assertNoWrites(t, remote)

	callsBeforeRetry := len(remote.Adapter.Calls())
	retry, retryErr := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	)
	if retryErr == nil {
		t.Fatal("Apply(retry) error = nil")
	}
	if retry.Status != StatusFailed || retry.FailureKind != FailurePlan {
		t.Fatalf("retry = %#v, want FAILED/PLAN_INTEGRITY", retry)
	}
	if callsAfterRetry := len(remote.Adapter.Calls()); callsAfterRetry != callsBeforeRetry {
		t.Fatalf("retry made %d adapter calls, want zero", callsAfterRetry-callsBeforeRetry)
	}
}

func TestApply_RemovalRequiresMatchingExplicitAuthorization(t *testing.T) {
	t.Parallel()

	t.Run("unapproved removal previews but execute is blocked", func(t *testing.T) {
		caller := t.TempDir()
		remote := standardRemote(t)
		remote.setDocument("201", `{"sections":{"hero":{"custom_css":["one","two"],"settings":{"keep":1}}},"order":["hero"]}`)
		options := standardPlanOptions(filepath.Join(caller, "run"))
		planResult, err := CreatePlan(context.Background(), options, testDependencies(t, remote, caller))
		if err != nil {
			t.Fatalf("CreatePlan(): %v", err)
		}
		preview, err := Apply(
			context.Background(),
			ApplyOptions{PlanPath: planResult.PlanPath},
			testDependencies(t, remote, caller),
		)
		if err != nil || preview.Status != StatusPlanned {
			t.Fatalf("Apply(preview) = %#v, %v, want PLANNED", preview, err)
		}
		execute, err := Apply(
			context.Background(),
			ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
			testDependencies(t, remote, caller),
		)
		if err == nil {
			t.Fatal("Apply(execute) error = nil")
		}
		if execute.FailureKind != FailureRemoval {
			t.Fatalf("failure kind = %q, want REMOVAL_NOT_AUTHORIZED", execute.FailureKind)
		}
		assertNoWrites(t, remote)
	})

	t.Run("plan allow-remove must match preview", func(t *testing.T) {
		caller := t.TempDir()
		remote := standardRemote(t)
		options := standardPlanOptions(filepath.Join(caller, "run"))
		options.AllowRemove = true
		planResult, err := CreatePlan(context.Background(), options, testDependencies(t, remote, caller))
		if err != nil {
			t.Fatalf("CreatePlan(): %v", err)
		}
		result, err := Apply(
			context.Background(),
			ApplyOptions{PlanPath: planResult.PlanPath},
			testDependencies(t, remote, caller),
		)
		if err == nil {
			t.Fatal("Apply(preview) error = nil")
		}
		if result.FailureKind != FailureBinding {
			t.Fatalf("failure kind = %q, want BINDING_MISMATCH", result.FailureKind)
		}
		assertNoWrites(t, remote)
	})
}
