package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/evidence"
	"theme-template-sync/internal/templatejson"
)

func TestCreatePlan_WritesCompleteDryRunEvidence(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	if result.Status != StatusPlanned {
		t.Fatalf("status = %q, want %q", result.Status, StatusPlanned)
	}
	if result.PlanPath != filepath.Join(runtimeRoot, "plan.json") {
		t.Fatalf("plan path = %q, want runtime plan", result.PlanPath)
	}
	if len(result.PlanSHA256) != 64 {
		t.Fatalf("plan SHA-256 = %q, want 64 hex characters", result.PlanSHA256)
	}
	assertNoWrites(t, remote)

	plan, err := LoadPlan(result.PlanPath)
	if err != nil {
		t.Fatalf("LoadPlan(): %v", err)
	}
	if got, want := []string{plan.Targets[0].Identity.ID, plan.Targets[1].Identity.ID}, []string{"201", "202"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target IDs = %v, want %v", got, want)
	}
	for index, target := range plan.Targets {
		if target.Status != TargetReady {
			t.Fatalf("target %d status = %q, want READY", index, target.Status)
		}
		if target.Diff.Empty() {
			t.Fatalf("target %d diff is empty", index)
		}
	}

	for _, relative := range []string{
		"manifest.json",
		"plan.json",
		"source/before.json",
		"source/canonical.json",
		"targets/000/before.json",
		"targets/000/planned.json",
		"targets/000/diff.json",
		"targets/001/before.json",
		"targets/001/planned.json",
		"targets/001/diff.json",
	} {
		if _, err := os.Stat(filepath.Join(runtimeRoot, relative)); err != nil {
			t.Fatalf("missing evidence %s: %v", relative, err)
		}
	}
	before, err := os.ReadFile(filepath.Join(runtimeRoot, "targets", "000", "before.json"))
	if err != nil {
		t.Fatalf("reading before evidence: %v", err)
	}
	if !strings.Contains(string(before), "target one") {
		t.Fatalf("before evidence lost remote header: %q", before)
	}
	plannedBody, err := os.ReadFile(filepath.Join(runtimeRoot, "targets", "000", "planned.json"))
	if err != nil {
		t.Fatalf("reading planned evidence: %v", err)
	}
	if !strings.Contains(string(plannedBody), "target one") {
		t.Fatalf("planned evidence lost target header: %q", plannedBody)
	}
	plannedDocument, err := templatejson.Parse(plannedBody)
	if err != nil {
		t.Fatalf("parsing planned evidence: %v", err)
	}
	planned := plannedDocument.Root
	if planned["target_unknown"] != "one" {
		t.Fatalf("planned target_unknown = %v, want preserved one", planned["target_unknown"])
	}
	hero := planned["sections"].(map[string]any)["hero"].(map[string]any)
	if _, ok := hero["settings"]; !ok {
		t.Fatalf("planned hero lost target settings: %#v", hero)
	}
}

func TestCreatePlan_NoOpDoesNotWrite(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.setDocument(
		"201",
		`// formatting differs
{"target_unknown":"one","order":["hero"],"sections":{"hero":{"settings":{"keep":1},"custom_css":[".hero { color: red; }"]}}}`,
	)
	remote.setDocument(
		"202",
		`{"sections":{"hero":{"custom_css":[".hero { color: red; }"],"blocks":[{"id":"keep"}]}},"order":["hero"],"target_unknown":"two"}`,
	)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	result, err := CreatePlan(context.Background(), options, testDependencies(t, remote, caller))
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	if result.Status != StatusNoOp {
		t.Fatalf("status = %q, want NO_OP", result.Status)
	}
	for index, target := range result.Targets {
		if target.Status != TargetNoOp {
			t.Fatalf("target %d status = %q, want NO_OP", index, target.Status)
		}
	}
	assertNoWrites(t, remote)
}

func TestCreatePlan_AllowsLiveSourceRead(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.addTheme(
		"store-source",
		"Source",
		"101",
		"Source",
		"main",
		string(remote.document("101")),
	)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	result, err := CreatePlan(context.Background(), options, testDependencies(t, remote, caller))
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	if result.Status != StatusPlanned {
		t.Fatalf("status = %q, want PLANNED", result.Status)
	}
	assertNoWrites(t, remote)
}

func TestCreatePlan_FailsClosedBeforeMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*remoteFixture)
	}{
		{
			name: "live target",
			setup: func(remote *remoteFixture) {
				remote.addTheme("store-two", "Target Two", "202", "Target Two", "main", string(remote.document("202")))
			},
		},
		{
			name: "later target invalid JSON",
			setup: func(remote *remoteFixture) {
				remote.setDocument("202", `{"sections":`)
			},
		},
		{
			name: "target template missing",
			setup: func(remote *remoteFixture) {
				remote.setReadOverride(func(_ int, theme adapter.ResolvedTheme, key templatejson.TemplateKey) ([]byte, error) {
					if theme.ID == "202" {
						return nil, fmt.Errorf("%w: %s", adapter.ErrTemplateNotFound, key.AssetPath())
					}
					return remote.document(theme.ID), nil
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := t.TempDir()
			runtimeRoot := filepath.Join(caller, "run")
			remote := standardRemote(t)
			test.setup(remote)
			result, err := CreatePlan(
				context.Background(),
				standardPlanOptions(runtimeRoot),
				testDependencies(t, remote, caller),
			)
			if err == nil {
				t.Fatal("CreatePlan() error = nil")
			}
			if result.Status != StatusFailed {
				t.Fatalf("status = %q, want FAILED", result.Status)
			}
			assertNoWrites(t, remote)
			manifest, loadErr := LoadManifest(filepath.Join(runtimeRoot, "manifest.json"))
			if loadErr != nil {
				t.Fatalf("LoadManifest(): %v", loadErr)
			}
			if manifest.Status != StatusFailed || manifest.Failure == nil {
				t.Fatalf("manifest = %#v, want categorized failure", manifest)
			}
		})
	}
}

func TestCreatePlan_ResolutionFailureWritesFailureManifest(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	remote.setResolveOverride(func(
		_ int,
		store adapter.StoreRef,
		ref adapter.ThemeRef,
	) (adapter.ResolvedTheme, error) {
		if store == "store-source" {
			return adapter.ResolvedTheme{}, errors.New("synthetic source resolution failure")
		}
		return adapter.ResolvedTheme{Store: string(store), ID: "999", Name: string(ref), Role: "unpublished"}, nil
	})
	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want FAILED", result.Status)
	}
	assertNoWrites(t, remote)
	if _, err := LoadManifest(filepath.Join(runtimeRoot, "manifest.json")); err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
}

func TestCreatePlan_RejectsTargetsResolvingToSameTheme(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.addTheme(
		"store-one.myshopify.com",
		"201",
		"201",
		"Target One",
		"unpublished",
		string(remote.document("201")),
	)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = []EndpointRef{
		{Store: "store-one", Theme: "Target One"},
		{Store: "store-one.myshopify.com", Theme: "201"},
	}
	result, err := CreatePlan(
		context.Background(),
		options,
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePreflight {
		t.Fatalf("result = %#v, want FAILED/PREFLIGHT_FAILED", result)
	}
	if !strings.Contains(err.Error(), "same theme") {
		t.Fatalf("CreatePlan() error = %q, want duplicate theme rejection", err)
	}
	assertNoWrites(t, remote)
}

func TestCreatePlan_SelfValidatesResolvedAuthorityBeforeSuccess(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	remote.setResolveOverride(func(
		_ int,
		store adapter.StoreRef,
		ref adapter.ThemeRef,
	) (adapter.ResolvedTheme, error) {
		if store == "store-source" {
			return adapter.ResolvedTheme{
				Store: "different-store",
				ID:    "101",
				Name:  "Source",
				Role:  "unpublished",
			}, nil
		}
		return remote.themes[endpointKey(store, ref)], nil
	})

	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeRoot, "plan.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid plan was persisted, stat error = %v", statErr)
	}
	manifest, loadErr := LoadManifest(filepath.Join(runtimeRoot, "manifest.json"))
	if loadErr != nil {
		t.Fatalf("LoadManifest(): %v", loadErr)
	}
	if manifest.Status != StatusFailed || manifest.Failure == nil {
		t.Fatalf("manifest = %#v, want self-validation failure", manifest)
	}
	assertNoWrites(t, remote)
}

func TestCreatePlan_PlanPersistenceFailureKeepsKnownRunEvidence(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	createdConflict := false
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if !createdConflict {
			createdConflict = true
			if err := os.Mkdir(filepath.Join(runtimeRoot, "plan.json"), 0o700); err != nil {
				return nil, fmt.Errorf("creating synthetic plan conflict: %w", err)
			}
		}
		return remote.document(theme.ID), nil
	})

	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	if result.RunID == "" || result.PlanPath != filepath.Join(runtimeRoot, "plan.json") {
		t.Fatalf("result run metadata = %#v, want known run ID and plan path", result)
	}
	if result.EvidenceRoot != runtimeRoot || len(result.PlanSHA256) != 64 {
		t.Fatalf("result evidence metadata = %#v, want root and plan SHA", result)
	}
	if !strings.Contains(result.Message, "persisting plan") {
		t.Fatalf("result message = %q, want primary plan persistence failure", result.Message)
	}
	assertNoWrites(t, remote)

	manifestData, readErr := os.ReadFile(filepath.Join(runtimeRoot, "manifest.json"))
	if readErr != nil {
		t.Fatalf("reading failure manifest: %v", readErr)
	}
	var manifest Manifest
	if decodeErr := decodeJSONDocument(manifestData, &manifest); decodeErr != nil {
		t.Fatalf("decoding failure manifest: %v", decodeErr)
	}
	if integrityErr := manifest.ValidateIntegrity(); integrityErr != nil {
		t.Fatalf("ValidateIntegrity(failure manifest): %v", integrityErr)
	}
	if manifest.RunID != result.RunID || manifest.PlanPath != result.PlanPath ||
		manifest.PlanSHA256 != result.PlanSHA256 {
		t.Fatalf("manifest metadata = %#v, want result metadata %#v", manifest, result)
	}
	if manifest.Failure == nil || manifest.Failure.Kind != string(FailurePlan) ||
		!strings.Contains(manifest.Failure.Message, "persisting plan") {
		t.Fatalf("manifest failure = %#v, want plan persistence failure", manifest.Failure)
	}
}

func TestCreatePlan_ManifestPersistenceFailureKeepsKnownPlanEvidence(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	createdConflict := false
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if !createdConflict {
			createdConflict = true
			if err := os.Mkdir(filepath.Join(runtimeRoot, "manifest.json"), 0o700); err != nil {
				return nil, fmt.Errorf("creating synthetic manifest conflict: %w", err)
			}
		}
		return remote.document(theme.ID), nil
	})

	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	if result.RunID == "" || result.PlanPath != filepath.Join(runtimeRoot, "plan.json") ||
		result.EvidenceRoot != runtimeRoot || len(result.PlanSHA256) != 64 {
		t.Fatalf("result evidence metadata = %#v, want complete known plan metadata", result)
	}
	if !strings.Contains(result.Message, "persisting plan manifest") {
		t.Fatalf("result message = %q, want manifest persistence failure", result.Message)
	}
	if _, loadErr := LoadPlan(result.PlanPath); loadErr != nil {
		t.Fatalf("LoadPlan() after manifest failure: %v", loadErr)
	}
	assertNoWrites(t, remote)
}

func TestCreatePlan_TransientManifestFailureWritesPlanBoundFailureManifest(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	dependencies := testDependencies(t, remote, caller)
	writeAttempts := 0
	dependencies.writeManifest = func(store *evidence.Store, manifest *Manifest) error {
		writeAttempts++
		if writeAttempts == 1 {
			return errors.New("synthetic transient manifest persistence failure")
		}
		return writeManifest(store, manifest)
	}

	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		dependencies,
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePlan {
		t.Fatalf("result = %#v, want FAILED/PLAN_INTEGRITY", result)
	}
	if writeAttempts != 2 {
		t.Fatalf("manifest write attempts = %d, want 2", writeAttempts)
	}
	if !strings.Contains(result.Message, "persisting plan manifest") ||
		!strings.Contains(result.Message, "synthetic transient") {
		t.Fatalf("result message = %q, want primary manifest persistence failure", result.Message)
	}
	plan, loadPlanErr := LoadPlan(result.PlanPath)
	if loadPlanErr != nil {
		t.Fatalf("LoadPlan(): %v", loadPlanErr)
	}
	manifest, loadManifestErr := LoadManifest(filepath.Join(runtimeRoot, "manifest.json"))
	if loadManifestErr != nil {
		t.Fatalf("LoadManifest(): %v", loadManifestErr)
	}
	if manifest.Status != StatusFailed || manifest.Failure == nil ||
		manifest.Failure.Kind != string(FailurePlan) {
		t.Fatalf("manifest = %#v, want plan-bound FAILED/PLAN_INTEGRITY", manifest)
	}
	if !reflect.DeepEqual(manifest.Targets, targetResultsFromPlan(plan)) {
		t.Fatalf("manifest targets = %#v, want plan targets", manifest.Targets)
	}
	assertNoWrites(t, remote)
}

func TestPlanValidateRejectsRehashedDerivedInvariantTampering(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(filepath.Join(caller, "run")),
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	original, err := LoadPlan(result.PlanPath)
	if err != nil {
		t.Fatalf("LoadPlan(): %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{name: "noncanonical template", mutate: func(plan *Plan) { plan.Template = "page.Bad" }},
		{name: "source hash", mutate: func(plan *Plan) { plan.Source.BeforeSHA256 = strings.Repeat("a", 64) }},
		{name: "target index", mutate: func(plan *Plan) { plan.Targets[0].Index = 7 }},
		{name: "target content hash", mutate: func(plan *Plan) { plan.Targets[0].BeforeSHA256 = strings.Repeat("b", 64) }},
		{name: "target derived status", mutate: func(plan *Plan) { plan.Targets[0].Status = TargetNoOp }},
		{name: "overall derived status", mutate: func(plan *Plan) { plan.Status = StatusNoOp }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan := clonePlan(t, original)
			test.mutate(&plan)
			recomputePlanHash(t, &plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("Validate() error = nil after rehashed tampering")
			}
		})
	}
}

func clonePlan(t *testing.T, plan Plan) Plan {
	t.Helper()
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshaling plan clone: %v", err)
	}
	var clone Plan
	if err := decodeJSONDocument(data, &clone); err != nil {
		t.Fatalf("decoding plan clone: %v", err)
	}
	return clone
}

func recomputePlanHash(t *testing.T, plan *Plan) {
	t.Helper()
	plan.PlanSHA256 = ""
	digest, err := plan.computedSHA256()
	if err != nil {
		t.Fatalf("computing plan hash: %v", err)
	}
	plan.PlanSHA256 = digest
}

func TestCreatePlan_DefaultRuntimeRejectsCallerSymlink(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(caller, ".runtime")); err != nil {
		t.Fatalf("creating runtime symlink: %v", err)
	}
	remote := standardRemote(t)
	options := standardPlanOptions("")
	result, err := CreatePlan(
		context.Background(),
		options,
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want FAILED", result.Status)
	}
	assertNoWrites(t, remote)
	if _, statErr := os.Stat(filepath.Join(outside, "theme-template-sync")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside runtime was created, stat error = %v", statErr)
	}
}

func TestCreatePlan_RefusesToOverwriteExistingRuntimeEvidence(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatalf("creating existing runtime root: %v", err)
	}
	sentinelPath := filepath.Join(runtimeRoot, "manifest.json")
	sentinel := []byte("synthetic existing evidence\n")
	if err := os.WriteFile(sentinelPath, sentinel, 0o600); err != nil {
		t.Fatalf("writing existing evidence: %v", err)
	}
	remote := standardRemote(t)
	result, err := CreatePlan(
		context.Background(),
		standardPlanOptions(runtimeRoot),
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("CreatePlan() error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePreflight {
		t.Fatalf("result = %#v, want FAILED/PREFLIGHT_FAILED", result)
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("CreatePlan() error = %q, want non-empty root rejection", err)
	}
	if got := remote.Adapter.Calls(); len(got) != 0 {
		t.Fatalf("adapter calls = %v, want none", got)
	}
	after, readErr := os.ReadFile(sentinelPath)
	if readErr != nil {
		t.Fatalf("reading existing evidence: %v", readErr)
	}
	if !reflect.DeepEqual(after, sentinel) {
		t.Fatalf("existing evidence changed: %q, want %q", after, sentinel)
	}
}

func TestCreatePlan_RejectsUnsafeCustomRuntimeRootsWithoutMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rootPath func(string) string
	}{
		{
			name: "outside caller",
			rootPath: func(string) string {
				return t.TempDir()
			},
		},
		{
			name: "inside caller with broad permissions",
			rootPath: func(caller string) string {
				root := filepath.Join(caller, "existing-empty-run")
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatalf("creating broad runtime root: %v", err)
				}
				return root
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := t.TempDir()
			runtimeRoot := test.rootPath(caller)
			if err := os.Chmod(runtimeRoot, 0o755); err != nil {
				t.Fatalf("setting broad runtime mode: %v", err)
			}
			before, err := os.Stat(runtimeRoot)
			if err != nil {
				t.Fatalf("stating runtime root before plan: %v", err)
			}
			remote := standardRemote(t)
			result, err := CreatePlan(
				context.Background(),
				standardPlanOptions(runtimeRoot),
				testDependencies(t, remote, caller),
			)
			if err == nil {
				t.Fatal("CreatePlan() error = nil")
			}
			if result.Status != StatusFailed || result.FailureKind != FailurePreflight {
				t.Fatalf("result = %#v, want FAILED/PREFLIGHT_FAILED", result)
			}
			if got := remote.Adapter.Calls(); len(got) != 0 {
				t.Fatalf("adapter calls = %v, want none", got)
			}
			after, statErr := os.Stat(runtimeRoot)
			if statErr != nil {
				t.Fatalf("stating runtime root after plan: %v", statErr)
			}
			if after.Mode().Perm() != before.Mode().Perm() {
				t.Fatalf(
					"runtime root mode changed from %o to %o",
					before.Mode().Perm(),
					after.Mode().Perm(),
				)
			}
		})
	}
}
