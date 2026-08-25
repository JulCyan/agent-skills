package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/evidence"
	"theme-template-sync/internal/syncengine"
	"theme-template-sync/internal/templatejson"
)

type preflightTarget struct {
	plan     PlannedTarget
	identity adapter.ResolvedTheme
}

type preflightState struct {
	source  templatejson.Document
	binding evidence.PreviewBinding
	targets []preflightTarget
}

type targetPreflightError struct {
	position int
	cause    error
}

func (e *targetPreflightError) Error() string {
	return e.cause.Error()
}

func (e *targetPreflightError) Unwrap() error {
	return e.cause
}

func Apply(
	ctx context.Context,
	options ApplyOptions,
	dependencies Dependencies,
) (result Result, returnErr error) {
	dependencies = dependencies.normalized()
	if dependencies.Adapter == nil {
		return Result{Status: StatusFailed, FailureKind: FailureNeedsSetup}, errors.New("adapter is not configured")
	}
	if filepath.Base(options.PlanPath) != "plan.json" {
		return Result{Status: StatusFailed, FailureKind: FailurePlan}, errors.New("explicit plan path must end in plan.json")
	}
	runtimeRoot := filepath.Dir(options.PlanPath)
	store, err := openExistingRunStore(dependencies.CallerCWD, runtimeRoot)
	if err != nil {
		return Result{Status: StatusFailed, FailureKind: FailurePlan}, err
	}
	defer func() {
		if err := store.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	applyLease, err := store.AcquireApplyLease()
	if err != nil {
		failure := evidence.NewFailureRecord(string(FailurePlan), err)
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePlan,
			Message:      failure.Message,
			PlanPath:     options.PlanPath,
			EvidenceRoot: store.Path(),
			Targets:      []TargetResult{},
			Warnings:     []string{},
		}, err
	}
	defer func() {
		if err := applyLease.Finish(); err != nil {
			leaseErr := fmt.Errorf("finalizing apply lease: %w", err)
			failure := evidence.NewFailureRecord(string(FailureEvidence), leaseErr)
			result.Status = StatusFailed
			result.FailureKind = FailureEvidence
			result.Message = failure.Message
			if result.PlanPath == "" {
				result.PlanPath = options.PlanPath
			}
			if result.EvidenceRoot == "" {
				result.EvidenceRoot = store.Path()
			}
			if result.Targets == nil {
				result.Targets = []TargetResult{}
			}
			if result.Warnings == nil {
				result.Warnings = []string{}
			}
			returnErr = errors.Join(returnErr, leaseErr)
		}
	}()
	plan, err := loadPlanFromStore(store)
	if err != nil {
		return Result{Status: StatusFailed, FailureKind: FailurePlan}, err
	}
	manifest, err := loadManifestFromStore(store)
	if err != nil {
		return Result{Status: StatusFailed, FailureKind: FailurePlan}, err
	}
	if err := manifest.ValidateAgainst(plan, options.PlanPath); err != nil {
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePlan,
			RunID:        plan.RunID,
			PlanPath:     options.PlanPath,
			PlanSHA256:   plan.PlanSHA256,
			EvidenceRoot: store.Path(),
			Targets:      append([]TargetResult{}, manifest.Targets...),
			Warnings:     append([]string{}, plan.Warnings...),
		}, fmt.Errorf("validating manifest against plan: %w", err)
	}
	if manifestHasMutationEvidence(manifest) {
		err := fmt.Errorf(
			"run already contains immutable mutation evidence with status %s; create a new plan",
			manifest.Status,
		)
		failure := evidence.NewFailureRecord(string(FailurePlan), err)
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePlan,
			Message:      failure.Message,
			RunID:        plan.RunID,
			PlanPath:     options.PlanPath,
			PlanSHA256:   plan.PlanSHA256,
			EvidenceRoot: store.Path(),
			Targets:      append([]TargetResult{}, manifest.Targets...),
			Warnings:     append([]string{}, plan.Warnings...),
		}, err
	}
	if options.AllowRemove != plan.AllowRemove {
		return failApply(
			store,
			&manifest,
			plan,
			FailureBinding,
			errors.New("allow-remove does not match the reviewed plan"),
			dependencies.Now(),
		)
	}
	if options.Execute && manifest.PreviewBinding == nil {
		return failApply(
			store,
			&manifest,
			plan,
			FailureBinding,
			errors.New("execute requires a matching apply preview binding"),
			dependencies.Now(),
		)
	}

	preflight, err := preflightPlan(ctx, plan, dependencies.Adapter)
	if err != nil {
		failedPosition := -1
		var targetErr *targetPreflightError
		if errors.As(err, &targetErr) {
			failedPosition = targetErr.position
		}
		return failApplyPreflight(
			store,
			&manifest,
			plan,
			failedPosition,
			contextFailureKind(ctx),
			err,
			dependencies.Now(),
		)
	}
	if !options.Execute {
		manifest.PreviewBinding = &preflight.binding
		manifest.Status = plan.Status
		manifest.Failure = nil
		manifest.Targets = targetResultsFromPlan(plan)
		manifest.UpdatedAt = dependencies.Now().UTC().Format(time.RFC3339Nano)
		if err := writeManifest(store, &manifest); err != nil {
			return Result{Status: StatusFailed, FailureKind: FailurePlan}, err
		}
		return Result{
			Status:       plan.Status,
			RunID:        plan.RunID,
			PlanPath:     options.PlanPath,
			PlanSHA256:   plan.PlanSHA256,
			EvidenceRoot: runtimeRoot,
			Targets:      manifest.Targets,
			Warnings:     append([]string{}, plan.Warnings...),
		}, nil
	}
	if err := evidence.MatchPreviewBinding(*manifest.PreviewBinding, preflight.binding); err != nil {
		return failApply(
			store,
			&manifest,
			plan,
			FailureBinding,
			err,
			dependencies.Now(),
		)
	}
	if planHasRemoval(plan) && !plan.AllowRemove {
		return failApply(
			store,
			&manifest,
			plan,
			FailureRemoval,
			errors.New("plan contains removed content without allow-remove authorization"),
			dependencies.Now(),
		)
	}
	return executePlan(
		ctx,
		plan,
		preflight,
		dependencies,
		store,
		applyLease,
		manifest,
		options.PlanPath,
	)
}

func preflightPlan(
	ctx context.Context,
	plan Plan,
	themeAdapter adapter.ThemeAdapter,
) (preflightState, error) {
	sourceIdentity, err := themeAdapter.ResolveTheme(
		ctx,
		plan.Source.Input.Store,
		plan.Source.Input.Theme,
	)
	if err != nil {
		return preflightState{}, fmt.Errorf("resolving source during preflight: %w", err)
	}
	if sourceIdentity != plan.Source.Identity {
		return preflightState{}, errors.New("source identity changed after planning")
	}
	sourceRaw, err := themeAdapter.ReadTemplate(ctx, sourceIdentity, plan.Template)
	if err != nil {
		return preflightState{}, fmt.Errorf("reading source during preflight: %w", err)
	}
	source, err := parseAndValidateTemplate(sourceRaw, "source preflight")
	if err != nil {
		return preflightState{}, err
	}
	sourceSHA, err := source.CanonicalSHA256()
	if err != nil {
		return preflightState{}, err
	}
	if sourceSHA != plan.Source.BeforeSHA256 {
		return preflightState{}, errors.New("source template changed after planning")
	}

	state := preflightState{
		source: source,
		binding: evidence.PreviewBinding{
			PlanSHA256:         plan.PlanSHA256,
			AllowRemove:        plan.AllowRemove,
			Template:           string(plan.Template),
			Scope:              plan.Scope,
			Source:             identity(sourceIdentity),
			SourceBeforeSHA256: sourceSHA,
			Targets:            []evidence.BoundTarget{},
		},
		targets: []preflightTarget{},
	}
	for position, plannedTarget := range plan.Targets {
		target, err := preflightOneTarget(ctx, plan, plannedTarget, source, themeAdapter)
		if err != nil {
			return preflightState{}, &targetPreflightError{
				position: position,
				cause:    fmt.Errorf("preflighting target %d: %w", plannedTarget.Index, err),
			}
		}
		state.targets = append(state.targets, target)
		state.binding.Targets = append(state.binding.Targets, evidence.BoundTarget{
			Identity:      identity(target.identity),
			BeforeSHA256:  target.plan.BeforeSHA256,
			PlannedSHA256: target.plan.PlannedSHA256,
		})
	}
	if err := state.binding.Validate(); err != nil {
		return preflightState{}, fmt.Errorf("validating current preview binding: %w", err)
	}
	return state, nil
}

func preflightOneTarget(
	ctx context.Context,
	plan Plan,
	plannedTarget PlannedTarget,
	source templatejson.Document,
	themeAdapter adapter.ThemeAdapter,
) (preflightTarget, error) {
	resolved, err := themeAdapter.ResolveTheme(
		ctx,
		plannedTarget.Input.Store,
		plannedTarget.Input.Theme,
	)
	if err != nil {
		return preflightTarget{}, fmt.Errorf("resolving target: %w", err)
	}
	if resolved.IsLive() {
		return preflightTarget{}, fmt.Errorf("target theme %q is Live", resolved.Name)
	}
	if resolved != plannedTarget.Identity {
		return preflightTarget{}, errors.New("target identity changed after planning")
	}
	beforeRaw, err := themeAdapter.ReadTemplate(ctx, resolved, plan.Template)
	if err != nil {
		return preflightTarget{}, fmt.Errorf("reading target: %w", err)
	}
	before, err := parseAndValidateTemplate(beforeRaw, "target preflight")
	if err != nil {
		return preflightTarget{}, err
	}
	beforeSHA, err := before.CanonicalSHA256()
	if err != nil {
		return preflightTarget{}, err
	}
	if beforeSHA != plannedTarget.BeforeSHA256 {
		return preflightTarget{}, errors.New("target template changed after planning")
	}
	planned, err := syncengine.Transform(source, before, plan.Scope)
	if err != nil {
		return preflightTarget{}, err
	}
	plannedSHA, err := planned.CanonicalSHA256()
	if err != nil {
		return preflightTarget{}, err
	}
	if plannedSHA != plannedTarget.PlannedSHA256 {
		return preflightTarget{}, errors.New("planned template changed during preflight")
	}
	diff, err := syncengine.Compare(before, planned)
	if err != nil {
		return preflightTarget{}, err
	}
	if !reflect.DeepEqual(diff, plannedTarget.Diff) {
		return preflightTarget{}, errors.New("target diff changed during preflight")
	}
	return preflightTarget{
		plan:     plannedTarget,
		identity: resolved,
	}, nil
}

func failApply(
	store *evidence.Store,
	manifest *Manifest,
	plan Plan,
	kind FailureKind,
	err error,
	now time.Time,
) (Result, error) {
	return failApplyWithTargets(
		store,
		manifest,
		plan,
		targetResultsFromPlan(plan),
		kind,
		err,
		now,
	)
}

func failApplyPreflight(
	store *evidence.Store,
	manifest *Manifest,
	plan Plan,
	failedPosition int,
	kind FailureKind,
	err error,
	now time.Time,
) (Result, error) {
	targets := targetResultsFromPlan(plan)
	if failedPosition >= 0 && failedPosition < len(targets) {
		targets[failedPosition].Status = TargetPreflightFailed
		for position := failedPosition + 1; position < len(targets); position++ {
			if targets[position].Status == TargetReady {
				targets[position].Status = TargetSkippedAfterFailure
			}
		}
	}
	return failApplyWithTargets(store, manifest, plan, targets, kind, err, now)
}

func failApplyWithTargets(
	store *evidence.Store,
	manifest *Manifest,
	plan Plan,
	targets []TargetResult,
	kind FailureKind,
	err error,
	now time.Time,
) (Result, error) {
	failure := evidence.NewFailureRecord(string(kind), err)
	for position := range targets {
		if targets[position].Status == TargetPreflightFailed {
			targets[position].Failure = &failure
		}
	}
	manifest.Status = StatusFailed
	manifest.PreviewBinding = nil
	manifest.Failure = &failure
	manifest.Targets = targets
	manifest.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if writeErr := writeManifest(store, manifest); writeErr != nil {
		err, failure = addPersistenceFailure(
			kind,
			err,
			"terminal manifest",
			writeErr,
		)
		manifest.Failure = &failure
	}
	return Result{
		Status:       StatusFailed,
		FailureKind:  kind,
		Message:      failure.Message,
		RunID:        plan.RunID,
		PlanPath:     manifest.PlanPath,
		PlanSHA256:   plan.PlanSHA256,
		EvidenceRoot: store.Path(),
		Targets:      manifest.Targets,
		Warnings:     append([]string{}, plan.Warnings...),
	}, err
}

func planHasRemoval(plan Plan) bool {
	for _, target := range plan.Targets {
		if target.Diff.HasRemoval() {
			return true
		}
	}
	return false
}

func manifestHasMutationEvidence(manifest Manifest) bool {
	switch manifest.Status {
	case StatusExecuting, StatusApplied, StatusPartial, StatusReadbackMismatch:
		return true
	}
	for _, target := range manifest.Targets {
		if target.WriteAttempted || target.ReadbackAttempted {
			return true
		}
	}
	return false
}

func executePlan(
	ctx context.Context,
	plan Plan,
	preflight preflightState,
	dependencies Dependencies,
	store *evidence.Store,
	applyLease *evidence.ApplyLease,
	manifest Manifest,
	planPath string,
) (Result, error) {
	results := targetResultsFromPlan(plan)
	appliedCount := 0
	for position, target := range preflight.targets {
		if target.plan.Diff.Empty() {
			continue
		}

		resolved, err := dependencies.Adapter.ResolveTheme(
			ctx,
			target.plan.Input.Store,
			target.plan.Input.Theme,
		)
		if err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				fmt.Errorf("resolving target immediately before write: %w", err),
				dependencies.Now(),
			)
		}
		if resolved.IsLive() {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				fmt.Errorf("target theme %q became Live before write", resolved.Name),
				dependencies.Now(),
			)
		}
		if resolved != target.identity {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				errors.New("target identity changed immediately before write"),
				dependencies.Now(),
			)
		}
		beforeRaw, err := dependencies.Adapter.ReadTemplate(ctx, resolved, plan.Template)
		if err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				fmt.Errorf("reading target immediately before write: %w", err),
				dependencies.Now(),
			)
		}
		before, err := parseAndValidateTemplate(beforeRaw, "target immediately before write")
		if err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				err,
				dependencies.Now(),
			)
		}
		beforeSHA, err := before.CanonicalSHA256()
		if err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				err,
				dependencies.Now(),
			)
		}
		if beforeSHA != target.plan.BeforeSHA256 {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				errors.New("target template changed immediately before write"),
				dependencies.Now(),
			)
		}
		plannedImmediatelyBeforeWrite, err := syncengine.Transform(
			preflight.source,
			before,
			plan.Scope,
		)
		if err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				fmt.Errorf("rebuilding target immediately before write: %w", err),
				dependencies.Now(),
			)
		}
		plannedSHA, err := plannedImmediatelyBeforeWrite.CanonicalSHA256()
		if err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				err,
				dependencies.Now(),
			)
		}
		if plannedSHA != target.plan.PlannedSHA256 {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				errors.New("planned template changed immediately before write"),
				dependencies.Now(),
			)
		}
		plannedWrite, err := plannedImmediatelyBeforeWrite.Render()
		if err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailurePreflight,
				err,
				dependencies.Now(),
			)
		}

		applyLease.Retain()
		results[position].Status = TargetWriteInProgress
		results[position].WriteAttempted = true
		if err := checkpointExecution(
			store,
			&manifest,
			results,
			StatusExecuting,
			dependencies.Now(),
		); err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailureEvidence,
				fmt.Errorf("persisting write intent: %w", err),
				dependencies.Now(),
			)
		}
		writeErr := dependencies.Adapter.WriteTemplate(
			ctx,
			resolved,
			plan.Template,
			plannedWrite,
		)
		var postWriteCleanup error
		if writeErr != nil && adapter.IsPostWriteCleanupError(writeErr) {
			postWriteCleanup = writeErr
		} else if writeErr != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailureWrite,
				fmt.Errorf("writing target template: %w", writeErr),
				dependencies.Now(),
			)
		}

		results[position].Status = TargetReadbackInProgress
		results[position].ReadbackAttempted = true
		if err := checkpointExecution(
			store,
			&manifest,
			results,
			StatusExecuting,
			dependencies.Now(),
		); err != nil {
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailureEvidence,
				fmt.Errorf("persisting readback intent: %w", err),
				dependencies.Now(),
			)
		}
		afterRaw, err := dependencies.Adapter.ReadTemplate(ctx, resolved, plan.Template)
		if err != nil {
			err = errors.Join(fmt.Errorf("reading target after write: %w", err), postWriteCleanup)
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailureReadback,
				err,
				dependencies.Now(),
			)
		}
		prefix := fmt.Sprintf("targets/%03d", target.plan.Index)
		afterEvidenceErr := store.WriteBytes(prefix+"/after.json", afterRaw)
		if afterEvidenceErr != nil {
			afterEvidenceErr = fmt.Errorf("writing readback evidence: %w", afterEvidenceErr)
		}
		after, err := parseAndValidateTemplate(afterRaw, "target readback")
		if err != nil {
			err = errors.Join(err, afterEvidenceErr, postWriteCleanup)
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailureReadback,
				err,
				dependencies.Now(),
			)
		}
		afterSHA, err := after.CanonicalSHA256()
		if err != nil {
			err = errors.Join(err, afterEvidenceErr, postWriteCleanup)
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailureReadback,
				err,
				dependencies.Now(),
			)
		}
		results[position].AfterSHA256 = afterSHA
		if afterSHA != target.plan.PlannedSHA256 {
			err := errors.Join(
				errors.New("canonical readback does not match planned template"),
				afterEvidenceErr,
				postWriteCleanup,
			)
			return executionFailure(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				appliedCount,
				FailureMismatch,
				err,
				dependencies.Now(),
			)
		}

		results[position].Status = TargetApplied
		appliedCount++
		if afterEvidenceErr != nil || postWriteCleanup != nil {
			return executionFailureAfterApplied(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				errors.Join(afterEvidenceErr, postWriteCleanup),
				dependencies.Now(),
			)
		}
		checkpointStatus := StatusExecuting
		if !hasReadyTargetAfter(preflight.targets, position) {
			checkpointStatus = StatusApplied
		}
		if err := checkpointExecution(
			store,
			&manifest,
			results,
			checkpointStatus,
			dependencies.Now(),
		); err != nil {
			return executionFailureAfterApplied(
				store,
				manifest,
				plan,
				planPath,
				results,
				position,
				fmt.Errorf("recording applied target %d: %w", target.plan.Index, err),
				dependencies.Now(),
			)
		}
	}

	status := StatusNoOp
	if appliedCount > 0 {
		status = StatusApplied
	}
	return Result{
		Status:       status,
		RunID:        plan.RunID,
		PlanPath:     planPath,
		PlanSHA256:   plan.PlanSHA256,
		EvidenceRoot: store.Path(),
		Targets:      results,
		Warnings:     append([]string{}, plan.Warnings...),
	}, nil
}

func checkpointExecution(
	store *evidence.Store,
	manifest *Manifest,
	results []TargetResult,
	status Status,
	now time.Time,
) error {
	manifest.Status = status
	manifest.Targets = results
	manifest.Failure = nil
	manifest.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := writeManifest(store, manifest); err != nil {
		return fmt.Errorf("writing execution checkpoint: %w", err)
	}
	return nil
}

func hasReadyTargetAfter(targets []preflightTarget, position int) bool {
	for next := position + 1; next < len(targets); next++ {
		if !targets[next].plan.Diff.Empty() {
			return true
		}
	}
	return false
}

func executionFailureAfterApplied(
	store *evidence.Store,
	manifest Manifest,
	plan Plan,
	planPath string,
	results []TargetResult,
	position int,
	err error,
	now time.Time,
) (Result, error) {
	failure := evidence.NewFailureRecord(string(FailureEvidence), err)
	results[position].Status = TargetApplied
	results[position].Failure = &failure
	for next := position + 1; next < len(results); next++ {
		if results[next].Status != TargetNoOp {
			results[next].Status = TargetSkippedAfterFailure
		}
	}
	prefix := fmt.Sprintf("targets/%03d", results[position].Index)
	if writeErr := store.WriteJSON(prefix+"/failure.json", failure); writeErr != nil {
		err, failure = addPersistenceFailure(
			FailureEvidence,
			err,
			"target failure evidence",
			writeErr,
		)
		results[position].Failure = &failure
	}
	manifest.Status = StatusPartial
	manifest.Targets = results
	manifest.Failure = &failure
	manifest.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if writeErr := writeManifest(store, &manifest); writeErr != nil {
		err, failure = addPersistenceFailure(
			FailureEvidence,
			err,
			"terminal manifest",
			writeErr,
		)
		results[position].Failure = &failure
		manifest.Failure = &failure
	}
	return Result{
		Status:       StatusPartial,
		FailureKind:  FailureEvidence,
		Message:      failure.Message,
		RunID:        plan.RunID,
		PlanPath:     planPath,
		PlanSHA256:   plan.PlanSHA256,
		EvidenceRoot: store.Path(),
		Targets:      results,
		Warnings:     append([]string{}, plan.Warnings...),
	}, err
}

func executionFailure(
	store *evidence.Store,
	manifest Manifest,
	plan Plan,
	planPath string,
	results []TargetResult,
	failedPosition int,
	appliedCount int,
	kind FailureKind,
	err error,
	now time.Time,
) (Result, error) {
	failure := evidence.NewFailureRecord(string(kind), err)
	failedStatus := TargetWriteFailed
	status := StatusFailed
	switch kind {
	case FailurePreflight:
		failedStatus = TargetPreflightFailed
	case FailureReadback:
		failedStatus = TargetReadbackFailed
	case FailureMismatch:
		failedStatus = TargetReadbackMismatch
		status = StatusReadbackMismatch
	case FailureEvidence:
		failedStatus = TargetEvidenceFailed
	}
	if appliedCount > 0 {
		status = StatusPartial
	}
	results[failedPosition].Status = failedStatus
	results[failedPosition].Failure = &failure
	for position := failedPosition + 1; position < len(results); position++ {
		if results[position].Status != TargetNoOp {
			results[position].Status = TargetSkippedAfterFailure
		}
	}
	prefix := fmt.Sprintf("targets/%03d", results[failedPosition].Index)
	if writeErr := store.WriteJSON(prefix+"/failure.json", failure); writeErr != nil {
		err, failure = addPersistenceFailure(
			kind,
			err,
			"target failure evidence",
			writeErr,
		)
		results[failedPosition].Failure = &failure
	}
	manifest.Status = status
	manifest.Targets = results
	manifest.Failure = &failure
	manifest.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if writeErr := writeManifest(store, &manifest); writeErr != nil {
		err, failure = addPersistenceFailure(
			kind,
			err,
			"terminal manifest",
			writeErr,
		)
		results[failedPosition].Failure = &failure
		manifest.Failure = &failure
	}
	return Result{
		Status:       status,
		FailureKind:  kind,
		Message:      failure.Message,
		RunID:        plan.RunID,
		PlanPath:     planPath,
		PlanSHA256:   plan.PlanSHA256,
		EvidenceRoot: store.Path(),
		Targets:      results,
		Warnings:     append([]string{}, plan.Warnings...),
	}, err
}

func addPersistenceFailure(
	kind FailureKind,
	operationErr error,
	artifact string,
	persistenceErr error,
) (error, evidence.FailureRecord) {
	wrapped := fmt.Errorf("%s persistence failed: %w", artifact, persistenceErr)
	combined := errors.Join(operationErr, wrapped)
	display := errors.Join(wrapped, operationErr)
	return combined, evidence.NewFailureRecord(string(kind), display)
}
