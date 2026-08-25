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
	"time"

	"theme-template-sync/internal/evidence"
	"theme-template-sync/internal/syncengine"
	"theme-template-sync/internal/templatejson"
)

func CreatePlan(
	ctx context.Context,
	options PlanOptions,
	dependencies Dependencies,
) (result Result, returnErr error) {
	dependencies = dependencies.normalized()
	if dependencies.Adapter == nil {
		return Result{Status: StatusFailed, FailureKind: FailureNeedsSetup}, errors.New("adapter is not configured")
	}
	if err := validatePlanOptions(options); err != nil {
		return Result{Status: StatusFailed, FailureKind: FailureUsage}, err
	}

	defaultRoot, runID, err := evidence.NewRunRoot(
		dependencies.CallerCWD,
		dependencies.Now(),
		dependencies.Random,
	)
	if err != nil {
		return Result{Status: StatusFailed, FailureKind: FailureNeedsSetup}, err
	}
	runtimeRoot := defaultRoot
	if options.RuntimeRoot != "" {
		runtimeRoot = options.RuntimeRoot
	}
	store, err := createRunStore(dependencies.CallerCWD, runtimeRoot)
	if err != nil {
		return Result{Status: StatusFailed, FailureKind: FailurePreflight}, err
	}
	defer func() {
		if err := store.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	if err := store.RequireEmpty(); err != nil {
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePreflight,
			EvidenceRoot: store.Path(),
			Targets:      []TargetResult{},
			Warnings:     append([]string{}, options.Warnings...),
		}, err
	}
	if err := store.Reserve(); err != nil {
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePreflight,
			EvidenceRoot: store.Path(),
			Targets:      []TargetResult{},
			Warnings:     append([]string{}, options.Warnings...),
		}, err
	}

	planPath := filepath.Join(store.Path(), "plan.json")
	manifest := Manifest{
		SchemaVersion: 1,
		RunID:         runID,
		PlanPath:      planPath,
		Template:      options.Template,
		Scope:         options.Scope,
		AllowRemove:   options.AllowRemove,
		Status:        StatusFailed,
		Targets:       []TargetResult{},
		UpdatedAt:     dependencies.Now().UTC().Format(time.RFC3339Nano),
	}
	plan, err := buildPlan(ctx, options, runID, dependencies, store)
	if err != nil {
		kind := contextFailureKind(ctx)
		failure := evidence.NewFailureRecord(string(kind), err)
		manifest.Failure = &failure
		if writeErr := dependencies.writeManifest(store, &manifest); writeErr != nil {
			err, failure = addPersistenceFailure(
				kind,
				err,
				"failure manifest",
				writeErr,
			)
			manifest.Failure = &failure
		}
		return Result{
			Status:       StatusFailed,
			FailureKind:  kind,
			Message:      failure.Message,
			RunID:        runID,
			PlanPath:     planPath,
			EvidenceRoot: store.Path(),
			Targets:      manifest.Targets,
			Warnings:     append([]string{}, options.Warnings...),
		}, err
	}

	planSHA, err := plan.computedSHA256()
	if err != nil {
		return Result{Status: StatusFailed, FailureKind: FailurePlan}, err
	}
	plan.PlanSHA256 = planSHA
	if err := plan.Validate(); err != nil {
		err = fmt.Errorf("validating generated plan: %w", err)
		failure := evidence.NewFailureRecord(string(FailurePlan), err)
		manifest.PlanSHA256 = planSHA
		manifest.Status = StatusFailed
		manifest.Targets = targetResultsFromPlan(plan)
		manifest.Failure = &failure
		manifest.UpdatedAt = dependencies.Now().UTC().Format(time.RFC3339Nano)
		if writeErr := dependencies.writeManifest(store, &manifest); writeErr != nil {
			err, failure = addPersistenceFailure(
				FailurePlan,
				err,
				"failure manifest",
				writeErr,
			)
			manifest.Failure = &failure
		}
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePlan,
			Message:      failure.Message,
			RunID:        runID,
			PlanPath:     planPath,
			PlanSHA256:   planSHA,
			EvidenceRoot: store.Path(),
			Targets:      manifest.Targets,
			Warnings:     append([]string{}, options.Warnings...),
		}, err
	}
	if writeErr := store.WriteJSON("plan.json", plan); writeErr != nil {
		err = fmt.Errorf("persisting plan: %w", writeErr)
		failure := evidence.NewFailureRecord(string(FailurePlan), err)
		manifest.PlanSHA256 = planSHA
		manifest.Status = StatusFailed
		manifest.Targets = targetResultsFromPlan(plan)
		manifest.Failure = &failure
		manifest.UpdatedAt = dependencies.Now().UTC().Format(time.RFC3339Nano)
		if manifestErr := dependencies.writeManifest(store, &manifest); manifestErr != nil {
			err, failure = addPersistenceFailure(
				FailurePlan,
				err,
				"failure manifest",
				manifestErr,
			)
			manifest.Failure = &failure
		}
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePlan,
			Message:      failure.Message,
			RunID:        runID,
			PlanPath:     planPath,
			PlanSHA256:   planSHA,
			EvidenceRoot: store.Path(),
			Targets:      manifest.Targets,
			Warnings:     append([]string{}, options.Warnings...),
		}, err
	}
	manifest.PlanSHA256 = planSHA
	manifest.Status = plan.Status
	manifest.Targets = targetResultsFromPlan(plan)
	manifest.UpdatedAt = dependencies.Now().UTC().Format(time.RFC3339Nano)
	if writeErr := dependencies.writeManifest(store, &manifest); writeErr != nil {
		err = fmt.Errorf("persisting plan manifest: %w", writeErr)
		failure := evidence.NewFailureRecord(string(FailurePlan), err)
		manifest.Status = StatusFailed
		manifest.Failure = &failure
		manifest.UpdatedAt = dependencies.Now().UTC().Format(time.RFC3339Nano)
		if failureManifestErr := dependencies.writeManifest(store, &manifest); failureManifestErr != nil {
			err, failure = addPersistenceFailure(
				FailurePlan,
				err,
				"failure manifest",
				failureManifestErr,
			)
			manifest.Failure = &failure
		}
		return Result{
			Status:       StatusFailed,
			FailureKind:  FailurePlan,
			Message:      failure.Message,
			RunID:        runID,
			PlanPath:     planPath,
			PlanSHA256:   planSHA,
			EvidenceRoot: store.Path(),
			Targets:      manifest.Targets,
			Warnings:     append([]string{}, options.Warnings...),
		}, err
	}
	return Result{
		Status:       plan.Status,
		RunID:        runID,
		PlanPath:     planPath,
		PlanSHA256:   planSHA,
		EvidenceRoot: store.Path(),
		Targets:      manifest.Targets,
		Warnings:     append([]string{}, options.Warnings...),
	}, nil
}

func createRunStore(callerCWD string, runtimeRoot string) (*evidence.Store, error) {
	caller, relative, err := runStoreLocation(callerCWD, runtimeRoot)
	if err != nil {
		return nil, err
	}
	return evidence.OpenUnder(caller, relative)
}

func openExistingRunStore(callerCWD string, runtimeRoot string) (*evidence.Store, error) {
	caller, relative, err := runStoreLocation(callerCWD, runtimeRoot)
	if err != nil {
		return nil, err
	}
	return evidence.OpenExistingUnder(caller, relative)
}

func runStoreLocation(callerCWD string, runtimeRoot string) (string, string, error) {
	caller, err := filepath.Abs(callerCWD)
	if err != nil {
		return "", "", fmt.Errorf("resolving caller working directory: %w", err)
	}
	relative, err := filepath.Rel(caller, runtimeRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolving evidence root relative to caller: %w", err)
	}
	if relative == "." || !filepath.IsLocal(relative) {
		return "", "", errors.New("evidence root must be a child of the caller working directory")
	}
	return caller, relative, nil
}

func LoadPlan(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, fmt.Errorf("reading plan: %w", err)
	}
	var plan Plan
	if err := decodeJSONDocument(data, &plan); err != nil {
		return Plan{}, fmt.Errorf("decoding plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("reading manifest: %w", err)
	}
	var manifest Manifest
	if err := decodeJSONDocument(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decoding manifest: %w", err)
	}
	if err := manifest.ValidateIntegrity(); err != nil {
		return Manifest{}, err
	}
	planPath := filepath.Join(filepath.Dir(path), "plan.json")
	plan, err := LoadPlan(planPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && standalonePlanFailure(manifest) {
			return manifest, nil
		}
		return Manifest{}, fmt.Errorf("loading manifest authority plan: %w", err)
	}
	if err := manifest.ValidateAgainst(plan, planPath); err != nil {
		return Manifest{}, fmt.Errorf("validating manifest against authority plan: %w", err)
	}
	return manifest, nil
}

func standalonePlanFailure(manifest Manifest) bool {
	if manifest.Status != StatusFailed ||
		manifest.Failure == nil ||
		manifest.PreviewBinding != nil ||
		targetsHaveMutationEvidence(manifest.Targets) {
		return false
	}
	if manifest.PlanSHA256 == "" {
		return len(manifest.Targets) == 0 &&
			(manifest.Failure.Kind == string(FailurePreflight) ||
				manifest.Failure.Kind == string(FailureInterrupted))
	}
	if manifest.Failure.Kind != string(FailurePlan) {
		return false
	}
	for _, target := range manifest.Targets {
		if target.Status != TargetReady && target.Status != TargetNoOp {
			return false
		}
		if target.WriteAttempted || target.ReadbackAttempted || target.Failure != nil {
			return false
		}
	}
	return true
}

func loadPlanFromStore(store *evidence.Store) (Plan, error) {
	data, err := store.ReadBytes("plan.json")
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := decodeJSONDocument(data, &plan); err != nil {
		return Plan{}, fmt.Errorf("decoding plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func loadManifestFromStore(store *evidence.Store) (Manifest, error) {
	data, err := store.ReadBytes("manifest.json")
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := decodeJSONDocument(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decoding manifest: %w", err)
	}
	if err := manifest.ValidateIntegrity(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func decodeJSONDocument(data []byte, value any) error {
	if err := templatejson.ValidateUniqueObjectKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains more than one value")
		}
		return fmt.Errorf("decoding trailing JSON content: %w", err)
	}
	return nil
}

func buildPlan(
	ctx context.Context,
	options PlanOptions,
	runID string,
	dependencies Dependencies,
	store *evidence.Store,
) (Plan, error) {
	sourceTheme, err := dependencies.Adapter.ResolveTheme(
		ctx,
		options.Source.Store,
		options.Source.Theme,
	)
	if err != nil {
		return Plan{}, fmt.Errorf("resolving source theme: %w", err)
	}
	sourceRaw, err := dependencies.Adapter.ReadTemplate(ctx, sourceTheme, options.Template)
	if err != nil {
		return Plan{}, fmt.Errorf("reading source template: %w", err)
	}
	source, err := parseAndValidateTemplate(sourceRaw, "source")
	if err != nil {
		return Plan{}, err
	}
	sourceCanonical, err := source.Canonical()
	if err != nil {
		return Plan{}, err
	}
	sourceSHA, err := source.CanonicalSHA256()
	if err != nil {
		return Plan{}, err
	}
	if err := store.WriteBytes("source/before.json", sourceRaw); err != nil {
		return Plan{}, err
	}
	if err := store.WriteBytes("source/canonical.json", sourceCanonical); err != nil {
		return Plan{}, err
	}

	plan := Plan{
		SchemaVersion: 1,
		RunID:         runID,
		CreatedAt:     dependencies.Now().UTC().Format(time.RFC3339Nano),
		Template:      options.Template,
		Scope:         options.Scope,
		AllowRemove:   options.AllowRemove,
		Warnings:      append([]string{}, options.Warnings...),
		Source: PlannedSource{
			Input:        options.Source,
			Identity:     sourceTheme,
			BeforeSHA256: sourceSHA,
			Canonical:    append(json.RawMessage{}, sourceCanonical...),
		},
		Targets: []PlannedTarget{},
		Status:  StatusNoOp,
	}
	for index, targetInput := range options.Targets {
		target, err := planTarget(
			ctx,
			index,
			targetInput,
			options,
			source,
			dependencies,
			store,
		)
		if err != nil {
			return Plan{}, fmt.Errorf("planning target %d: %w", index, err)
		}
		if target.Status == TargetReady {
			plan.Status = StatusPlanned
		}
		plan.Targets = append(plan.Targets, target)
		if err := validateUniqueTargetIdentities(plan.Targets); err != nil {
			return Plan{}, err
		}
	}
	return plan, nil
}

func planTarget(
	ctx context.Context,
	index int,
	input EndpointRef,
	options PlanOptions,
	source templatejson.Document,
	dependencies Dependencies,
	store *evidence.Store,
) (PlannedTarget, error) {
	theme, err := dependencies.Adapter.ResolveTheme(ctx, input.Store, input.Theme)
	if err != nil {
		return PlannedTarget{}, fmt.Errorf("resolving target theme: %w", err)
	}
	if theme.IsLive() {
		return PlannedTarget{}, fmt.Errorf("target theme %q is Live", theme.Name)
	}
	beforeRaw, err := dependencies.Adapter.ReadTemplate(ctx, theme, options.Template)
	if err != nil {
		return PlannedTarget{}, fmt.Errorf("reading target template: %w", err)
	}
	before, err := parseAndValidateTemplate(beforeRaw, "target")
	if err != nil {
		return PlannedTarget{}, err
	}
	planned, err := syncengine.Transform(source, before, options.Scope)
	if err != nil {
		return PlannedTarget{}, err
	}
	diff, err := syncengine.Compare(before, planned)
	if err != nil {
		return PlannedTarget{}, err
	}
	beforeCanonical, err := before.Canonical()
	if err != nil {
		return PlannedTarget{}, err
	}
	plannedCanonical, err := planned.Canonical()
	if err != nil {
		return PlannedTarget{}, err
	}
	plannedWrite, err := planned.Render()
	if err != nil {
		return PlannedTarget{}, err
	}
	beforeSHA, err := before.CanonicalSHA256()
	if err != nil {
		return PlannedTarget{}, err
	}
	plannedSHA, err := planned.CanonicalSHA256()
	if err != nil {
		return PlannedTarget{}, err
	}
	prefix := fmt.Sprintf("targets/%03d", index)
	if err := store.WriteBytes(prefix+"/before.json", beforeRaw); err != nil {
		return PlannedTarget{}, err
	}
	if err := store.WriteBytes(prefix+"/planned.json", plannedWrite); err != nil {
		return PlannedTarget{}, err
	}
	if err := store.WriteJSON(prefix+"/diff.json", diff); err != nil {
		return PlannedTarget{}, err
	}
	status := TargetReady
	if diff.Empty() {
		status = TargetNoOp
	}
	return PlannedTarget{
		Index:         index,
		Input:         input,
		Identity:      theme,
		BeforeSHA256:  beforeSHA,
		PlannedSHA256: plannedSHA,
		Before:        append(json.RawMessage{}, beforeCanonical...),
		Planned:       append(json.RawMessage{}, plannedCanonical...),
		Diff:          diff,
		Status:        status,
	}, nil
}

func parseAndValidateTemplate(data []byte, label string) (templatejson.Document, error) {
	document, err := templatejson.Parse(data)
	if err != nil {
		return templatejson.Document{}, fmt.Errorf("parsing %s template: %w", label, err)
	}
	if err := document.Validate(); err != nil {
		return templatejson.Document{}, fmt.Errorf("validating %s template: %w", label, err)
	}
	return document, nil
}

func validatePlanOptions(options PlanOptions) error {
	if options.Template == "" {
		return errors.New("template is required")
	}
	if err := options.Scope.Validate(); err != nil {
		return err
	}
	if options.Source.Store == "" || options.Source.Theme == "" {
		return errors.New("source store and theme are required")
	}
	if len(options.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	for index, target := range options.Targets {
		if target.Store == "" || target.Theme == "" {
			return fmt.Errorf("target %d store and theme are required", index)
		}
	}
	return nil
}
