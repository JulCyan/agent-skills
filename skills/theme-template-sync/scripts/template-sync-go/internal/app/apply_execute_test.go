package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/evidence"
	"theme-template-sync/internal/syncengine"
	"theme-template-sync/internal/templatejson"
)

func TestApplyExecute_RequiresPreviewBinding(t *testing.T) {
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
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.FailureKind != FailureBinding {
		t.Fatalf("failure kind = %q, want BINDING_MISMATCH", result.FailureKind)
	}
	assertNoWrites(t, remote)
}

func TestApplyExecute_AllTargetPreflightFailureIdentifiesFailedTarget(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.addTheme(
		"store-three",
		"Target Three",
		"203",
		"Target Three",
		"unpublished",
		`{"sections":{"hero":{"custom_css":["different"]}},"order":["hero"]}`,
	)
	remote.addTheme(
		"store-four",
		"Target Four",
		"204",
		"Target Four",
		"unpublished",
		`{"sections":{"hero":{"custom_css":[".hero { color: red; }"]}},"order":["hero"]}`,
	)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = append(
		options.Targets,
		EndpointRef{Store: "store-three", Theme: "Target Three"},
		EndpointRef{Store: "store-four", Theme: "Target Four"},
	)
	planResult := createAndPreview(t, caller, remote, options)
	targetTwoBefore := string(remote.document("202"))
	remote.setDocument("202", `{"sections":`)

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailurePreflight {
		t.Fatalf("result = %#v, want FAILED/PREFLIGHT_FAILED", result)
	}
	wantStatuses := []TargetStatus{
		TargetReady,
		TargetPreflightFailed,
		TargetSkippedAfterFailure,
		TargetNoOp,
	}
	for index, want := range wantStatuses {
		if result.Targets[index].Status != want {
			t.Fatalf("target %d status = %q, want %q", index, result.Targets[index].Status, want)
		}
	}
	if result.Targets[1].Failure == nil || result.Targets[1].Failure.Kind != string(FailurePreflight) {
		t.Fatalf("failed target = %#v, want categorized preflight failure", result.Targets[1])
	}
	assertNoWrites(t, remote)

	manifest, loadErr := LoadManifest(filepath.Join(result.EvidenceRoot, "manifest.json"))
	if loadErr != nil {
		t.Fatalf("LoadManifest(): %v", loadErr)
	}
	if !reflect.DeepEqual(manifest.Targets, result.Targets) {
		t.Fatalf("manifest targets = %#v, want result targets %#v", manifest.Targets, result.Targets)
	}

	remote.setDocument("202", targetTwoBefore)
	retry, retryErr := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	)
	if retryErr != nil || retry.Status != StatusPlanned {
		t.Fatalf("Apply(retry preview) = %#v, %v, want PLANNED", retry, retryErr)
	}
	assertNoWrites(t, remote)
}

func TestManifestValidationRejectsResealedContradictoryTerminalStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{
			name: "partial without an applied target",
			mutate: func(manifest *Manifest) {
				failure := evidence.NewFailureRecord(
					string(FailureWrite),
					errors.New("synthetic write failure"),
				)
				manifest.Status = StatusPartial
				manifest.Failure = &failure
				manifest.Targets[0].Status = TargetWriteFailed
				manifest.Targets[0].WriteAttempted = true
				manifest.Targets[0].Failure = &failure
				manifest.Targets[1].Status = TargetSkippedAfterFailure
			},
		},
		{
			name: "first-mismatch status after an applied target",
			mutate: func(manifest *Manifest) {
				failure := evidence.NewFailureRecord(
					string(FailureMismatch),
					errors.New("synthetic readback mismatch"),
				)
				manifest.Status = StatusReadbackMismatch
				manifest.Failure = &failure
				manifest.Targets[0].Status = TargetApplied
				manifest.Targets[0].WriteAttempted = true
				manifest.Targets[0].ReadbackAttempted = true
				manifest.Targets[0].AfterSHA256 = manifest.Targets[0].PlannedSHA256
				manifest.Targets[1].Status = TargetReadbackMismatch
				manifest.Targets[1].WriteAttempted = true
				manifest.Targets[1].ReadbackAttempted = true
				manifest.Targets[1].AfterSHA256 = manifest.Targets[1].BeforeSHA256
				manifest.Targets[1].Failure = &failure
			},
		},
		{
			name: "applied target contains a failure",
			mutate: func(manifest *Manifest) {
				failure := evidence.NewFailureRecord(
					string(FailureEvidence),
					errors.New("synthetic evidence failure"),
				)
				manifest.Status = StatusApplied
				manifest.Failure = nil
				for position := range manifest.Targets {
					manifest.Targets[position].Status = TargetApplied
					manifest.Targets[position].WriteAttempted = true
					manifest.Targets[position].ReadbackAttempted = true
					manifest.Targets[position].AfterSHA256 = manifest.Targets[position].PlannedSHA256
				}
				manifest.Targets[0].Failure = &failure
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			caller := t.TempDir()
			remote := standardRemote(t)
			planResult := createAndPreview(
				t,
				caller,
				remote,
				standardPlanOptions(filepath.Join(caller, "run")),
			)
			plan, err := LoadPlan(planResult.PlanPath)
			if err != nil {
				t.Fatalf("LoadPlan(): %v", err)
			}
			manifestPath := filepath.Join(caller, "run", "manifest.json")
			manifest, err := LoadManifest(manifestPath)
			if err != nil {
				t.Fatalf("LoadManifest(): %v", err)
			}
			test.mutate(&manifest)
			if err := manifest.seal(); err != nil {
				t.Fatalf("sealing mutated manifest: %v", err)
			}
			if err := manifest.ValidateAgainst(plan, planResult.PlanPath); err == nil {
				t.Fatal("ValidateAgainst() error = nil for contradictory terminal state")
			}
			writeManifestTestFile(t, manifestPath, &manifest, false)
			if _, err := LoadManifest(manifestPath); err == nil {
				t.Fatal("LoadManifest() error = nil for contradictory terminal state")
			}
		})
	}
}

func TestApplyExecute_WritesSeriallyWithImmediateReadback(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult := createAndPreview(t, caller, remote, standardPlanOptions(filepath.Join(caller, "run")))
	callOffset := len(remote.Adapter.Calls())

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusApplied {
		t.Fatalf("status = %q, want APPLIED", result.Status)
	}
	for index, target := range result.Targets {
		if target.Status != TargetApplied || !target.WriteAttempted || !target.ReadbackAttempted {
			t.Fatalf("target %d = %#v, want applied write/readback", index, target)
		}
	}
	if remote.Adapter.WriteCount() != 2 {
		t.Fatalf("write count = %d, want 2", remote.Adapter.WriteCount())
	}
	if remote.Adapter.MaxConcurrentWrites() != 1 {
		t.Fatalf("max concurrent writes = %d, want 1", remote.Adapter.MaxConcurrentWrites())
	}
	for themeID, header := range map[string]string{
		"201": "/* target one */",
		"202": "/* target two */",
	} {
		body := remote.document(themeID)
		if !strings.HasPrefix(string(body), header) {
			t.Fatalf("written theme %s lost target header: %q", themeID, body)
		}
	}

	calls := remote.Adapter.Calls()[callOffset:]
	operations := make([]adapter.Operation, 0, len(calls))
	for _, call := range calls {
		operations = append(operations, call.Operation)
	}
	wantOperations := []adapter.Operation{
		adapter.OperationResolve, adapter.OperationRead,
		adapter.OperationResolve, adapter.OperationRead,
		adapter.OperationResolve, adapter.OperationRead,
		adapter.OperationResolve, adapter.OperationRead, adapter.OperationWrite, adapter.OperationRead,
		adapter.OperationResolve, adapter.OperationRead, adapter.OperationWrite, adapter.OperationRead,
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", operations, wantOperations)
	}
	writeIDs := []string{}
	for _, call := range calls {
		if call.Operation == adapter.OperationWrite {
			writeIDs = append(writeIDs, call.Theme.ID)
		}
	}
	if !reflect.DeepEqual(writeIDs, []string{"201", "202"}) {
		t.Fatalf("write IDs = %v, want [201 202]", writeIDs)
	}
	for _, relative := range []string{"targets/000/after.json", "targets/001/after.json"} {
		if _, err := os.Stat(filepath.Join(caller, "run", relative)); err != nil {
			t.Fatalf("missing readback evidence %s: %v", relative, err)
		}
	}
	manifest, err := LoadManifest(filepath.Join(caller, "run", "manifest.json"))
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	if manifest.Status != StatusApplied || manifest.Failure != nil {
		t.Fatalf("manifest = %#v, want APPLIED without failure", manifest)
	}
}

func TestApplyExecute_SamePlanConcurrentExecuteIsRejectedBeforeAdapterCalls(t *testing.T) {
	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	options := standardPlanOptions(runtimeRoot)
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)

	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseWrite) })
	}
	defer release()
	remote.setWriteOverride(func(
		_ int,
		_ adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		_ []byte,
	) error {
		close(writeStarted)
		<-releaseWrite
		return nil
	})

	type applyOutcome struct {
		result Result
		err    error
	}
	firstDone := make(chan applyOutcome, 1)
	go func() {
		result, err := Apply(
			context.Background(),
			ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
			testDependencies(t, remote, caller),
		)
		firstDone <- applyOutcome{result: result, err: err}
	}()
	select {
	case <-writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first execute did not reach the write boundary")
	}

	callsBeforeSecond := len(remote.Adapter.Calls())
	second, secondErr := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if secondErr == nil {
		t.Fatal("second Apply(execute) error = nil")
	}
	if second.Status != StatusFailed || second.FailureKind != FailurePlan {
		t.Fatalf("second result = %#v, want FAILED/PLAN_INTEGRITY", second)
	}
	if callsAfterSecond := len(remote.Adapter.Calls()); callsAfterSecond != callsBeforeSecond {
		t.Fatalf(
			"second execute made %d adapter calls, want zero",
			callsAfterSecond-callsBeforeSecond,
		)
	}
	if remote.Adapter.WriteCount() != 1 || remote.Adapter.MaxConcurrentWrites() != 1 {
		t.Fatalf(
			"writes/max concurrency = %d/%d, want 1/1",
			remote.Adapter.WriteCount(),
			remote.Adapter.MaxConcurrentWrites(),
		)
	}

	release()
	select {
	case first := <-firstDone:
		if first.err != nil || first.result.Status != StatusApplied {
			t.Fatalf("first Apply(execute) = %#v, %v, want APPLIED", first.result, first.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first execute did not finish")
	}
}

func TestApplyPreview_CannotOverwriteConcurrentExecuteManifest(t *testing.T) {
	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	options := standardPlanOptions(runtimeRoot)
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)

	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseWrite) })
	}
	defer release()
	remote.setWriteOverride(func(
		_ int,
		_ adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		_ []byte,
	) error {
		close(writeStarted)
		<-releaseWrite
		return nil
	})

	type applyOutcome struct {
		result Result
		err    error
	}
	firstDone := make(chan applyOutcome, 1)
	go func() {
		result, err := Apply(
			context.Background(),
			ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
			testDependencies(t, remote, caller),
		)
		firstDone <- applyOutcome{result: result, err: err}
	}()
	select {
	case <-writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("execute did not reach the write boundary")
	}

	manifestPath := filepath.Join(runtimeRoot, "manifest.json")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading mutation checkpoint: %v", err)
	}
	checkpoint, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest(checkpoint): %v", err)
	}
	if checkpoint.Status != StatusExecuting || checkpoint.Targets[0].Status != TargetWriteInProgress {
		t.Fatalf("checkpoint = %#v, want EXECUTING/WRITE_IN_PROGRESS", checkpoint)
	}
	callsBeforePreview := len(remote.Adapter.Calls())
	preview, previewErr := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	)
	if previewErr == nil {
		t.Fatal("concurrent Apply(preview) error = nil")
	}
	if preview.Status != StatusFailed || preview.FailureKind != FailurePlan {
		t.Fatalf("preview result = %#v, want FAILED/PLAN_INTEGRITY", preview)
	}
	if callsAfterPreview := len(remote.Adapter.Calls()); callsAfterPreview != callsBeforePreview {
		t.Fatalf(
			"concurrent preview made %d adapter calls, want zero",
			callsAfterPreview-callsBeforePreview,
		)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading checkpoint after concurrent preview: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("concurrent preview overwrote mutation checkpoint:\nbefore=%s\nafter=%s", before, after)
	}

	release()
	select {
	case first := <-firstDone:
		if first.err != nil || first.result.Status != StatusApplied {
			t.Fatalf("Apply(execute) = %#v, %v, want APPLIED", first.result, first.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execute did not finish")
	}
}

func TestApplyExecute_PersistsIntentBeforeWriteAndReadback(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	options := standardPlanOptions(runtimeRoot)
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)
	var writeCheckpoint Manifest
	var writeCheckpointErr error
	remote.setWriteOverride(func(
		_ int,
		_ adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		_ []byte,
	) error {
		writeCheckpoint, writeCheckpointErr = LoadManifest(filepath.Join(runtimeRoot, "manifest.json"))
		return nil
	})
	var readbackCheckpoint Manifest
	var readbackCheckpointErr error
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID == "201" && remote.Adapter.WriteCount() > 0 {
			readbackCheckpoint, readbackCheckpointErr = LoadManifest(
				filepath.Join(runtimeRoot, "manifest.json"),
			)
		}
		return remote.document(theme.ID), nil
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusApplied {
		t.Fatalf("status = %q, want APPLIED", result.Status)
	}
	if writeCheckpointErr != nil {
		t.Fatalf("loading write checkpoint: %v", writeCheckpointErr)
	}
	if writeCheckpoint.Status != StatusExecuting ||
		writeCheckpoint.Targets[0].Status != TargetWriteInProgress ||
		!writeCheckpoint.Targets[0].WriteAttempted ||
		writeCheckpoint.Targets[0].ReadbackAttempted {
		t.Fatalf("write checkpoint = %#v, want durable write intent", writeCheckpoint)
	}
	if readbackCheckpointErr != nil {
		t.Fatalf("loading readback checkpoint: %v", readbackCheckpointErr)
	}
	if readbackCheckpoint.Status != StatusExecuting ||
		readbackCheckpoint.Targets[0].Status != TargetReadbackInProgress ||
		!readbackCheckpoint.Targets[0].ReadbackAttempted {
		t.Fatalf("readback checkpoint = %#v, want durable readback intent", readbackCheckpoint)
	}
}

func TestApplyExecute_SkipsNoOpTarget(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.setDocument("201", `{"sections":{"hero":{"custom_css":[".hero { color: red; }"],"settings":{"keep":1}}},"order":["hero"],"target_unknown":"one"}`)
	planResult := createAndPreview(t, caller, remote, standardPlanOptions(filepath.Join(caller, "run")))
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusApplied {
		t.Fatalf("status = %q, want APPLIED", result.Status)
	}
	if result.Targets[0].Status != TargetNoOp || result.Targets[1].Status != TargetApplied {
		t.Fatalf("target statuses = %q/%q, want NO_OP/APPLIED", result.Targets[0].Status, result.Targets[1].Status)
	}
	if remote.Adapter.WriteCount() != 1 {
		t.Fatalf("write count = %d, want 1", remote.Adapter.WriteCount())
	}
}

func TestApplyExecute_AllNoOpNeverWrites(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.setDocument("201", `{"sections":{"hero":{"custom_css":[".hero { color: red; }"],"settings":{"keep":1}}},"order":["hero"],"target_unknown":"one"}`)
	remote.setDocument("202", `{"sections":{"hero":{"custom_css":[".hero { color: red; }"],"blocks":[{"id":"keep"}]}},"order":["hero"],"target_unknown":"two"}`)
	planResult := createAndPreview(t, caller, remote, standardPlanOptions(filepath.Join(caller, "run")))
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusNoOp {
		t.Fatalf("status = %q, want NO_OP", result.Status)
	}
	assertNoWrites(t, remote)
}

func TestApplyExecute_AuthorizedRemovalIsApplied(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.setDocument("201", `{"sections":{"hero":{"custom_css":["one","two"],"settings":{"keep":1}}},"order":["hero"]}`)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = options.Targets[:1]
	options.AllowRemove = true
	planResult := createAndPreview(t, caller, remote, options)
	result, err := Apply(
		context.Background(),
		ApplyOptions{
			PlanPath:    planResult.PlanPath,
			Execute:     true,
			AllowRemove: true,
		},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusApplied || result.Targets[0].Status != TargetApplied {
		t.Fatalf("result = %#v, want authorized APPLIED", result)
	}
	if remote.Adapter.WriteCount() != 1 {
		t.Fatalf("write count = %d, want 1", remote.Adapter.WriteCount())
	}
}

func TestApplyExecute_CanonicalReadbackAcceptsFormatting(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		body := remote.document(theme.ID)
		if theme.ID == "201" && remote.Adapter.WriteCount() > 0 {
			return append([]byte("/* Shopify generated */\n"), body...), nil
		}
		return body, nil
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusApplied || result.Targets[0].Status != TargetApplied {
		t.Fatalf("result = %#v, want canonical APPLIED", result)
	}
}

func TestApplyExecute_PreservesExactNumbersInPlanDiff(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.setDocument(
		"101",
		`{"sections":{"hero":{"custom_css":["source"],"settings":{"decimal":1.2300,"large":9007199254740993}}},"order":["hero"]}`,
	)
	remote.setDocument(
		"201",
		`{"sections":{"hero":{"custom_css":["target"],"settings":{"decimal":9.8700,"large":9007199254740995}}},"order":["hero"],"target_unknown":"one"}`,
	)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = options.Targets[:1]
	options.Scope = syncengine.Scope{
		Mode:       syncengine.ModeSection,
		SectionKey: "hero",
	}
	planResult := createAndPreview(t, caller, remote, options)
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusApplied {
		t.Fatalf("status = %q, want APPLIED", result.Status)
	}
	written := string(remote.document("201"))
	for _, token := range []string{"1.2300", "9007199254740993"} {
		if !strings.Contains(written, token) {
			t.Fatalf("written template lost numeric token %s: %s", token, written)
		}
	}
}

func TestApplyExecute_PreservesHeaderFromImmediatePreWriteRead(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)

	document, err := templatejson.Parse(remote.document("201"))
	if err != nil {
		t.Fatalf("Parse(target): %v", err)
	}
	canonical, err := document.Canonical()
	if err != nil {
		t.Fatalf("Canonical(target): %v", err)
	}
	refreshedHeaderBody := append([]byte("/* refreshed immediately before write */\n"), canonical...)
	targetReads := 0
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID != "201" || remote.Adapter.WriteCount() > 0 {
			return remote.document(theme.ID), nil
		}
		targetReads++
		if targetReads >= 2 {
			return append([]byte{}, refreshedHeaderBody...), nil
		}
		return remote.document(theme.ID), nil
	})
	var written []byte
	remote.setWriteOverride(func(
		_ int,
		_ adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		body []byte,
	) error {
		written = append([]byte{}, body...)
		return nil
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("Apply(execute): %v", err)
	}
	if result.Status != StatusApplied {
		t.Fatalf("status = %q, want APPLIED", result.Status)
	}
	if !strings.HasPrefix(string(written), "/* refreshed immediately before write */") {
		t.Fatalf("written body did not preserve immediate header: %q", written)
	}
}

func TestApplyExecute_PreflightFailureWritesNothing(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult := createAndPreview(t, caller, remote, standardPlanOptions(filepath.Join(caller, "run")))
	remote.setDocument("202", `{"sections":`)
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want FAILED", result.Status)
	}
	assertNoWrites(t, remote)
}

func TestApplyExecute_JITLiveDriftStopsBeforeWrite(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult := createAndPreview(t, caller, remote, standardPlanOptions(filepath.Join(caller, "run")))
	targetOneResolves := 0
	remote.setResolveOverride(func(
		_ int,
		store adapter.StoreRef,
		ref adapter.ThemeRef,
	) (adapter.ResolvedTheme, error) {
		switch store {
		case "store-source":
			return adapter.ResolvedTheme{Store: "store-source", ID: "101", Name: "Source", Role: "unpublished"}, nil
		case "store-one":
			targetOneResolves++
			role := "unpublished"
			if targetOneResolves > 1 {
				role = "main"
			}
			return adapter.ResolvedTheme{Store: "store-one", ID: "201", Name: "Target One", Role: role}, nil
		case "store-two":
			return adapter.ResolvedTheme{Store: "store-two", ID: "202", Name: "Target Two", Role: "development"}, nil
		default:
			return adapter.ResolvedTheme{}, errors.New("unexpected synthetic endpoint " + string(ref))
		}
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed || len(result.Targets) == 0 || result.Targets[0].Status != TargetPreflightFailed {
		t.Fatalf("result = %#v, want FAILED at first target", result)
	}
	assertNoWrites(t, remote)
}

func TestApplyExecute_JITBeforeHashDriftStopsBeforeWrite(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)
	targetReads := 0
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID != "201" {
			return remote.document(theme.ID), nil
		}
		targetReads++
		if targetReads > 1 {
			return []byte(`{"sections":{"hero":{"custom_css":["drift"]}},"order":["hero"]}`), nil
		}
		return remote.document(theme.ID), nil
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed || result.Targets[0].Status != TargetPreflightFailed {
		t.Fatalf("result = %#v, want FAILED before write", result)
	}
	assertNoWrites(t, remote)
}

func TestApplyExecute_FirstReadbackMismatchStopsLaterTargets(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult := createAndPreview(t, caller, remote, standardPlanOptions(filepath.Join(caller, "run")))
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID == "201" && remote.Adapter.WriteCount() > 0 {
			return []byte(`{"sections":{"hero":{"custom_css":["mismatch"]}},"order":["hero"]}`), nil
		}
		return remote.document(theme.ID), nil
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusReadbackMismatch {
		t.Fatalf("status = %q, want READBACK_MISMATCH", result.Status)
	}
	if result.Targets[0].Status != TargetReadbackMismatch || result.Targets[1].Status != TargetSkippedAfterFailure {
		t.Fatalf("target statuses = %q/%q, want mismatch/skipped", result.Targets[0].Status, result.Targets[1].Status)
	}
	if remote.Adapter.WriteCount() != 1 {
		t.Fatalf("write count = %d, want 1", remote.Adapter.WriteCount())
	}
}

func TestApplyExecute_ReadbackFailureIsNotReportedAsMismatch(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID == "201" && remote.Adapter.WriteCount() > 0 {
			return nil, errors.New("synthetic readback transport failure")
		}
		return remote.document(theme.ID), nil
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailureReadback {
		t.Fatalf("result = %#v, want FAILED/READBACK_FAILED", result)
	}
	if result.Targets[0].Status != TargetReadbackFailed {
		t.Fatalf("target status = %q, want READBACK_FAILED", result.Targets[0].Status)
	}
	if !result.Targets[0].WriteAttempted || !result.Targets[0].ReadbackAttempted {
		t.Fatalf("target attempts = %#v, want write/readback attempted", result.Targets[0])
	}
}

func TestApplyExecute_PostPushCleanupFailureStillReadsBack(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)
	remote.setWriteOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		body []byte,
	) error {
		remote.setDocument(theme.ID, string(body))
		return adapter.NewPostWriteCleanupError(errors.New("synthetic cleanup failure"))
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusPartial || result.FailureKind != FailureEvidence {
		t.Fatalf("result = %#v, want PARTIAL/EVIDENCE_FAILED", result)
	}
	if result.Targets[0].Status != TargetApplied || !result.Targets[0].ReadbackAttempted {
		t.Fatalf("target = %#v, want applied with immediate readback", result.Targets[0])
	}
}

func TestApplyExecute_AppliedCheckpointFailureReportsPartialAndStops(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	planResult := createAndPreview(
		t,
		caller,
		remote,
		standardPlanOptions(runtimeRoot),
	)
	permissionsChanged := false
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID == "201" && remote.Adapter.WriteCount() > 0 && !permissionsChanged {
			if err := os.Chmod(runtimeRoot, 0o500); err != nil {
				return nil, err
			}
			permissionsChanged = true
		}
		return remote.document(theme.ID), nil
	})
	t.Cleanup(func() {
		if err := os.Chmod(runtimeRoot, 0o700); err != nil {
			t.Errorf("restoring runtime permissions: %v", err)
		}
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if !permissionsChanged {
		t.Fatal("test did not inject checkpoint failure")
	}
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if chmodErr := os.Chmod(runtimeRoot, 0o700); chmodErr != nil {
		t.Fatalf("restoring runtime permissions: %v", chmodErr)
	}
	if result.Status != StatusPartial || result.FailureKind != FailureEvidence {
		t.Fatalf("result = %#v, want PARTIAL/EVIDENCE_FAILED", result)
	}
	if result.Targets[0].Status != TargetApplied ||
		result.Targets[1].Status != TargetSkippedAfterFailure {
		t.Fatalf("target statuses = %q/%q, want APPLIED/SKIPPED", result.Targets[0].Status, result.Targets[1].Status)
	}
	if remote.Adapter.WriteCount() != 1 {
		t.Fatalf("write count = %d, want 1", remote.Adapter.WriteCount())
	}
	checkpoint, loadErr := LoadManifest(filepath.Join(runtimeRoot, "manifest.json"))
	if loadErr != nil {
		t.Fatalf("LoadManifest(checkpoint): %v", loadErr)
	}
	if checkpoint.Status != StatusExecuting || !checkpoint.Targets[0].ReadbackAttempted {
		t.Fatalf("durable checkpoint = %#v, want EXECUTING readback intent", checkpoint)
	}
	callsBefore := len(remote.Adapter.Calls())
	if _, retryErr := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath},
		testDependencies(t, remote, caller),
	); retryErr == nil {
		t.Fatal("Apply(retry) error = nil")
	}
	if got := len(remote.Adapter.Calls()); got != callsBefore {
		t.Fatalf("adapter calls after retry = %d, want %d", got, callsBefore)
	}
}

func TestApplyExecute_AfterEvidenceFailureStillCompletesCanonicalReadback(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	options := standardPlanOptions(runtimeRoot)
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)
	targetEvidence := filepath.Join(runtimeRoot, "targets", "000")
	permissionsChanged := false
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID == "201" && remote.Adapter.WriteCount() > 0 && !permissionsChanged {
			if err := os.Chmod(targetEvidence, 0o500); err != nil {
				return nil, err
			}
			permissionsChanged = true
		}
		return remote.document(theme.ID), nil
	})
	t.Cleanup(func() {
		if err := os.Chmod(targetEvidence, 0o700); err != nil {
			t.Errorf("restoring target evidence permissions: %v", err)
		}
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if !permissionsChanged {
		t.Fatal("test did not inject after evidence failure")
	}
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusPartial || result.FailureKind != FailureEvidence {
		t.Fatalf("result = %#v, want PARTIAL/EVIDENCE_FAILED", result)
	}
	if result.Targets[0].Status != TargetApplied ||
		result.Targets[0].AfterSHA256 != result.Targets[0].PlannedSHA256 {
		t.Fatalf("target = %#v, want matching canonical readback despite evidence failure", result.Targets[0])
	}
	if remote.Adapter.WriteCount() != 1 {
		t.Fatalf("write count = %d, want 1", remote.Adapter.WriteCount())
	}
}

func TestApplyExecute_WriteFailureAfterAppliedReportsPartial(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.addTheme(
		"store-three",
		"Target Three",
		"203",
		"Target Three",
		"unpublished",
		`{"sections":{"hero":{"custom_css":["three"]}},"order":["hero"]}`,
	)
	options := standardPlanOptions(filepath.Join(caller, "run"))
	options.Targets = append(options.Targets, EndpointRef{Store: "store-three", Theme: "Target Three"})
	planResult := createAndPreview(t, caller, remote, options)
	remote.setWriteOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		_ []byte,
	) error {
		if theme.ID == "202" {
			return errors.New("synthetic write failure")
		}
		return nil
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusPartial {
		t.Fatalf("status = %q, want PARTIAL", result.Status)
	}
	wantStatuses := []TargetStatus{TargetApplied, TargetWriteFailed, TargetSkippedAfterFailure}
	gotStatuses := []TargetStatus{result.Targets[0].Status, result.Targets[1].Status, result.Targets[2].Status}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("target statuses = %v, want %v", gotStatuses, wantStatuses)
	}
	if remote.Adapter.WriteCount() != 2 {
		t.Fatalf("write count = %d, want 2", remote.Adapter.WriteCount())
	}
	if _, err := os.Stat(filepath.Join(caller, "run", "targets", "001", "failure.json")); err != nil {
		t.Fatalf("missing failure evidence: %v", err)
	}
}

func TestApplyExecute_FirstWriteFailureReportsFailedAndSkipsLaterTargets(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult := createAndPreview(
		t,
		caller,
		remote,
		standardPlanOptions(filepath.Join(caller, "run")),
	)
	remote.setWriteOverride(func(
		_ int,
		_ adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		_ []byte,
	) error {
		return errors.New("synthetic first write failure")
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailureWrite {
		t.Fatalf("result = %#v, want FAILED/WRITE_FAILED", result)
	}
	wantStatuses := []TargetStatus{TargetWriteFailed, TargetSkippedAfterFailure}
	gotStatuses := []TargetStatus{result.Targets[0].Status, result.Targets[1].Status}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("target statuses = %v, want %v", gotStatuses, wantStatuses)
	}
	if !result.Targets[0].WriteAttempted || result.Targets[0].ReadbackAttempted {
		t.Fatalf("first target attempts = %#v, want write intent only", result.Targets[0])
	}
	if remote.Adapter.WriteCount() != 1 {
		t.Fatalf("write count = %d, want 1", remote.Adapter.WriteCount())
	}
	if got := exitCodeForResult(result); got != 3 {
		t.Fatalf("exit code = %d, want 3", got)
	}
	if _, err := os.Stat(filepath.Join(caller, "run", "targets", "000", "failure.json")); err != nil {
		t.Fatalf("missing first-write failure evidence: %v", err)
	}
	manifest, err := LoadManifest(filepath.Join(caller, "run", "manifest.json"))
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	if manifest.Status != StatusFailed ||
		manifest.Targets[0].Status != TargetWriteFailed ||
		!manifest.Targets[0].WriteAttempted ||
		manifest.Failure == nil {
		t.Fatalf("manifest = %#v, want durable first-write failure", manifest)
	}
}

func TestApplyExecute_SecondaryFailureEvidenceErrorIsReported(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	runtimeRoot := filepath.Join(caller, "run")
	remote := standardRemote(t)
	options := standardPlanOptions(runtimeRoot)
	options.Targets = options.Targets[:1]
	planResult := createAndPreview(t, caller, remote, options)
	targetEvidence := filepath.Join(runtimeRoot, "targets", "000")
	remote.setWriteOverride(func(
		_ int,
		_ adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		_ []byte,
	) error {
		if err := os.Chmod(targetEvidence, 0o500); err != nil {
			return err
		}
		return errors.New("synthetic primary write failure")
	})
	t.Cleanup(func() {
		if err := os.Chmod(targetEvidence, 0o700); err != nil {
			t.Errorf("restoring target evidence permissions: %v", err)
		}
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusFailed || result.FailureKind != FailureWrite {
		t.Fatalf("result = %#v, want FAILED/WRITE_FAILED", result)
	}
	if !strings.Contains(result.Message, "target failure evidence persistence failed") {
		t.Fatalf("result message = %q, want secondary persistence failure", result.Message)
	}
	manifest, loadErr := LoadManifest(filepath.Join(runtimeRoot, "manifest.json"))
	if loadErr != nil {
		t.Fatalf("LoadManifest(): %v", loadErr)
	}
	if manifest.Failure == nil ||
		!strings.Contains(manifest.Failure.Message, "target failure evidence persistence failed") {
		t.Fatalf("manifest failure = %#v, want secondary persistence failure", manifest.Failure)
	}
}

func TestApplyExecute_FailureKeepsLaterNoOpTargetNoOp(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	remote.setDocument(
		"202",
		`{"sections":{"hero":{"custom_css":[".hero { color: red; }"],"blocks":[{"id":"keep"}]}},"order":["hero"],"target_unknown":"two"}`,
	)
	planResult := createAndPreview(
		t,
		caller,
		remote,
		standardPlanOptions(filepath.Join(caller, "run")),
	)
	remote.setWriteOverride(func(
		_ int,
		_ adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
		_ []byte,
	) error {
		return errors.New("synthetic first write failure")
	})

	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Targets[0].Status != TargetWriteFailed || result.Targets[1].Status != TargetNoOp {
		t.Fatalf("target statuses = %q/%q, want WRITE_FAILED/NO_OP", result.Targets[0].Status, result.Targets[1].Status)
	}
	if _, err := LoadManifest(filepath.Join(caller, "run", "manifest.json")); err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
}

func TestApplyExecute_ReadbackMismatchAfterAppliedReportsPartial(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	remote := standardRemote(t)
	planResult := createAndPreview(t, caller, remote, standardPlanOptions(filepath.Join(caller, "run")))
	remote.setReadOverride(func(
		_ int,
		theme adapter.ResolvedTheme,
		_ templatejson.TemplateKey,
	) ([]byte, error) {
		if theme.ID == "202" && remote.Adapter.WriteCount() >= 2 {
			return []byte(`{"sections":{"hero":{"custom_css":["mismatch"]}},"order":["hero"]}`), nil
		}
		return remote.document(theme.ID), nil
	})
	result, err := Apply(
		context.Background(),
		ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
		testDependencies(t, remote, caller),
	)
	if err == nil {
		t.Fatal("Apply(execute) error = nil")
	}
	if result.Status != StatusPartial {
		t.Fatalf("status = %q, want PARTIAL", result.Status)
	}
	if result.Targets[0].Status != TargetApplied || result.Targets[1].Status != TargetReadbackMismatch {
		t.Fatalf("target statuses = %q/%q, want APPLIED/READBACK_MISMATCH", result.Targets[0].Status, result.Targets[1].Status)
	}
	manifest, err := LoadManifest(filepath.Join(caller, "run", "manifest.json"))
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	if manifest.Failure == nil || manifest.Failure.Kind != string(FailureMismatch) {
		t.Fatalf("manifest failure = %#v, want READBACK_MISMATCH", manifest.Failure)
	}
}

func TestApply_TerminalMutationEvidenceIsImmutable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		execute func(*testing.T, string, *remoteFixture, Result) Result
	}{
		{
			name: "applied",
			execute: func(t *testing.T, caller string, remote *remoteFixture, planResult Result) Result {
				result, err := Apply(
					context.Background(),
					ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
					testDependencies(t, remote, caller),
				)
				if err != nil {
					t.Fatalf("Apply(execute): %v", err)
				}
				return result
			},
		},
		{
			name: "partial",
			execute: func(t *testing.T, caller string, remote *remoteFixture, planResult Result) Result {
				remote.setWriteOverride(func(
					_ int,
					theme adapter.ResolvedTheme,
					_ templatejson.TemplateKey,
					_ []byte,
				) error {
					if theme.ID == "202" {
						return errors.New("synthetic second-target failure")
					}
					return nil
				})
				result, err := Apply(
					context.Background(),
					ApplyOptions{PlanPath: planResult.PlanPath, Execute: true},
					testDependencies(t, remote, caller),
				)
				if err == nil {
					t.Fatal("Apply(execute) error = nil")
				}
				return result
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := t.TempDir()
			remote := standardRemote(t)
			planResult := createAndPreview(
				t,
				caller,
				remote,
				standardPlanOptions(filepath.Join(caller, "run")),
			)
			terminal := test.execute(t, caller, remote, planResult)
			if terminal.Status != StatusApplied && terminal.Status != StatusPartial {
				t.Fatalf("terminal status = %q, want APPLIED or PARTIAL", terminal.Status)
			}
			manifestPath := filepath.Join(caller, "run", "manifest.json")
			before, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatalf("reading terminal manifest: %v", err)
			}
			writesBefore := remote.Adapter.WriteCount()
			callsBefore := len(remote.Adapter.Calls())

			retry, err := Apply(
				context.Background(),
				ApplyOptions{PlanPath: planResult.PlanPath},
				testDependencies(t, remote, caller),
			)
			if err == nil {
				t.Fatal("Apply(retry) error = nil")
			}
			if retry.Status != StatusFailed || retry.FailureKind != FailurePlan {
				t.Fatalf("retry = %#v, want FAILED/PLAN_INTEGRITY", retry)
			}
			if got := remote.Adapter.WriteCount(); got != writesBefore {
				t.Fatalf("write count after retry = %d, want %d", got, writesBefore)
			}
			if got := len(remote.Adapter.Calls()); got != callsBefore {
				t.Fatalf("adapter calls after retry = %d, want %d", got, callsBefore)
			}
			after, readErr := os.ReadFile(manifestPath)
			if readErr != nil {
				t.Fatalf("reading manifest after retry: %v", readErr)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("terminal manifest was overwritten:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func createAndPreview(
	t *testing.T,
	caller string,
	remote *remoteFixture,
	options PlanOptions,
) Result {
	t.Helper()

	planResult, err := CreatePlan(
		context.Background(),
		options,
		testDependencies(t, remote, caller),
	)
	if err != nil {
		t.Fatalf("CreatePlan(): %v", err)
	}
	previewOptions := ApplyOptions{
		PlanPath:    planResult.PlanPath,
		AllowRemove: options.AllowRemove,
	}
	if _, err := Apply(
		context.Background(),
		previewOptions,
		testDependencies(t, remote, caller),
	); err != nil {
		t.Fatalf("Apply(preview): %v", err)
	}
	return planResult
}
