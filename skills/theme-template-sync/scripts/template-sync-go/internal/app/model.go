package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/evidence"
	"theme-template-sync/internal/syncengine"
	"theme-template-sync/internal/templatejson"
)

type Status string

const (
	StatusNoOp             Status = "NO_OP"
	StatusPlanned          Status = "PLANNED"
	StatusExecuting        Status = "EXECUTING"
	StatusApplied          Status = "APPLIED"
	StatusFailed           Status = "FAILED"
	StatusReadbackMismatch Status = "READBACK_MISMATCH"
	StatusPartial          Status = "PARTIAL"
)

type TargetStatus string

const (
	TargetNoOp                TargetStatus = "NO_OP"
	TargetReady               TargetStatus = "READY"
	TargetPreflightFailed     TargetStatus = "PREFLIGHT_FAILED"
	TargetWriteInProgress     TargetStatus = "WRITE_IN_PROGRESS"
	TargetReadbackInProgress  TargetStatus = "READBACK_IN_PROGRESS"
	TargetApplied             TargetStatus = "APPLIED"
	TargetWriteFailed         TargetStatus = "WRITE_FAILED"
	TargetReadbackFailed      TargetStatus = "READBACK_FAILED"
	TargetReadbackMismatch    TargetStatus = "READBACK_MISMATCH"
	TargetEvidenceFailed      TargetStatus = "EVIDENCE_FAILED"
	TargetSkippedAfterFailure TargetStatus = "SKIPPED_AFTER_FAILURE"
)

type FailureKind string

const (
	FailureUsage       FailureKind = "USAGE"
	FailureNeedsSetup  FailureKind = "NEEDS_SETUP"
	FailurePreflight   FailureKind = "PREFLIGHT_FAILED"
	FailurePlan        FailureKind = "PLAN_INTEGRITY"
	FailureBinding     FailureKind = "BINDING_MISMATCH"
	FailureRemoval     FailureKind = "REMOVAL_NOT_AUTHORIZED"
	FailureWrite       FailureKind = "WRITE_FAILED"
	FailureReadback    FailureKind = "READBACK_FAILED"
	FailureMismatch    FailureKind = "READBACK_MISMATCH"
	FailureEvidence    FailureKind = "EVIDENCE_FAILED"
	FailureInterrupted FailureKind = "INTERRUPTED"
)

type Dependencies struct {
	Adapter       adapter.ThemeAdapter
	CallerCWD     string
	Now           func() time.Time
	Random        io.Reader
	writeManifest func(*evidence.Store, *Manifest) error
}

func (d Dependencies) normalized() Dependencies {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	if d.writeManifest == nil {
		d.writeManifest = writeManifest
	}
	return d
}

type PlannedSource struct {
	Input        EndpointRef           `json:"input"`
	Identity     adapter.ResolvedTheme `json:"identity"`
	BeforeSHA256 string                `json:"before_sha256"`
	Canonical    json.RawMessage       `json:"canonical"`
}

type PlannedTarget struct {
	Index         int                   `json:"index"`
	Input         EndpointRef           `json:"input"`
	Identity      adapter.ResolvedTheme `json:"identity"`
	BeforeSHA256  string                `json:"before_sha256"`
	PlannedSHA256 string                `json:"planned_sha256"`
	Before        json.RawMessage       `json:"before"`
	Planned       json.RawMessage       `json:"planned"`
	Diff          syncengine.Diff       `json:"diff"`
	Status        TargetStatus          `json:"status"`
}

type Plan struct {
	SchemaVersion int                      `json:"schema_version"`
	RunID         string                   `json:"run_id"`
	CreatedAt     string                   `json:"created_at"`
	Template      templatejson.TemplateKey `json:"template"`
	Scope         syncengine.Scope         `json:"scope"`
	AllowRemove   bool                     `json:"allow_remove"`
	Warnings      []string                 `json:"warnings"`
	Source        PlannedSource            `json:"source"`
	Targets       []PlannedTarget          `json:"targets"`
	Status        Status                   `json:"status"`
	PlanSHA256    string                   `json:"plan_sha256"`
}

type TargetResult struct {
	Index             int                     `json:"index"`
	Identity          adapter.ResolvedTheme   `json:"identity"`
	Status            TargetStatus            `json:"status"`
	BeforeSHA256      string                  `json:"before_sha256,omitempty"`
	PlannedSHA256     string                  `json:"planned_sha256,omitempty"`
	AfterSHA256       string                  `json:"after_sha256,omitempty"`
	WriteAttempted    bool                    `json:"write_attempted"`
	ReadbackAttempted bool                    `json:"readback_attempted"`
	Failure           *evidence.FailureRecord `json:"failure,omitempty"`
}

type Manifest struct {
	SchemaVersion  int                      `json:"schema_version"`
	RunID          string                   `json:"run_id"`
	PlanPath       string                   `json:"plan_path,omitempty"`
	PlanSHA256     string                   `json:"plan_sha256,omitempty"`
	Template       templatejson.TemplateKey `json:"template,omitempty"`
	Scope          syncengine.Scope         `json:"scope"`
	AllowRemove    bool                     `json:"allow_remove"`
	Status         Status                   `json:"status"`
	Targets        []TargetResult           `json:"targets"`
	PreviewBinding *evidence.PreviewBinding `json:"preview_binding,omitempty"`
	Failure        *evidence.FailureRecord  `json:"failure,omitempty"`
	UpdatedAt      string                   `json:"updated_at"`
	ManifestSHA256 string                   `json:"manifest_sha256"`
}

type Result struct {
	Status       Status         `json:"status"`
	FailureKind  FailureKind    `json:"failure_kind,omitempty"`
	Message      string         `json:"message,omitempty"`
	RunID        string         `json:"run_id,omitempty"`
	PlanPath     string         `json:"plan_path,omitempty"`
	PlanSHA256   string         `json:"plan_sha256,omitempty"`
	EvidenceRoot string         `json:"evidence_root,omitempty"`
	Targets      []TargetResult `json:"targets"`
	Warnings     []string       `json:"warnings"`
}

func (p Plan) computedSHA256() (string, error) {
	copy := p
	copy.PlanSHA256 = ""
	return evidence.SHA256JSON(copy)
}

func (p Plan) Validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("unsupported plan schema version %d", p.SchemaVersion)
	}
	if p.RunID == "" || p.CreatedAt == "" || p.Template == "" {
		return errors.New("plan run ID, creation time, and template are required")
	}
	if _, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err != nil {
		return fmt.Errorf("plan creation time is invalid: %w", err)
	}
	parsedTemplate, _, err := templatejson.ParseTemplateKey(string(p.Template), "")
	if err != nil || parsedTemplate != p.Template {
		return fmt.Errorf("plan template %q is not canonical", p.Template)
	}
	if err := p.Scope.Validate(); err != nil {
		return fmt.Errorf("validating plan scope: %w", err)
	}
	if len(p.Targets) == 0 {
		return errors.New("plan requires at least one target")
	}
	if p.Status != StatusNoOp && p.Status != StatusPlanned {
		return fmt.Errorf("invalid plan status %q", p.Status)
	}
	if !validSHA256(p.PlanSHA256) {
		return errors.New("plan SHA-256 is invalid")
	}
	computed, err := p.computedSHA256()
	if err != nil {
		return err
	}
	if computed != p.PlanSHA256 {
		return errors.New("plan SHA-256 does not match plan content")
	}
	if err := validateEndpointRef(p.Source.Input); err != nil {
		return fmt.Errorf("validating source input: %w", err)
	}
	if err := adapter.ValidateResolvedTheme(p.Source.Identity); err != nil {
		return fmt.Errorf("validating source identity: %w", err)
	}
	if canonicalStore(p.Source.Input.Store) != canonicalStore(adapter.StoreRef(p.Source.Identity.Store)) {
		return errors.New("source input and resolved identity stores do not match")
	}
	sourceSHA, err := canonicalRawSHA256(p.Source.Canonical, "source canonical")
	if err != nil {
		return err
	}
	if !validSHA256(p.Source.BeforeSHA256) || sourceSHA != p.Source.BeforeSHA256 {
		return errors.New("source canonical content does not match before SHA-256")
	}
	if err := validateUniqueTargetIdentities(p.Targets); err != nil {
		return err
	}
	derivedStatus := StatusNoOp
	for position, target := range p.Targets {
		if err := validatePlannedTarget(position, target); err != nil {
			return fmt.Errorf("validating target %d: %w", position, err)
		}
		if !target.Diff.Empty() {
			derivedStatus = StatusPlanned
		}
	}
	if p.Status != derivedStatus {
		return fmt.Errorf("plan status %q does not match derived status %q", p.Status, derivedStatus)
	}
	return nil
}

func validatePlannedTarget(position int, target PlannedTarget) error {
	if target.Index != position {
		return fmt.Errorf("target index %d does not match position %d", target.Index, position)
	}
	if err := validateEndpointRef(target.Input); err != nil {
		return fmt.Errorf("validating input: %w", err)
	}
	if err := adapter.ValidateResolvedTheme(target.Identity); err != nil {
		return fmt.Errorf("validating identity: %w", err)
	}
	if target.Identity.IsLive() {
		return errors.New("target identity must not be Live")
	}
	if canonicalStore(target.Input.Store) != canonicalStore(adapter.StoreRef(target.Identity.Store)) {
		return errors.New("target input and resolved identity stores do not match")
	}
	before, beforeSHA, err := canonicalRawDocument(target.Before, "target before")
	if err != nil {
		return err
	}
	planned, plannedSHA, err := canonicalRawDocument(target.Planned, "target planned")
	if err != nil {
		return err
	}
	if !validSHA256(target.BeforeSHA256) || beforeSHA != target.BeforeSHA256 {
		return errors.New("target before content does not match before SHA-256")
	}
	if !validSHA256(target.PlannedSHA256) || plannedSHA != target.PlannedSHA256 {
		return errors.New("target planned content does not match planned SHA-256")
	}
	derivedDiff, err := syncengine.Compare(before, planned)
	if err != nil {
		return fmt.Errorf("recomputing target diff: %w", err)
	}
	if !reflect.DeepEqual(derivedDiff, target.Diff) {
		return errors.New("target diff does not match before and planned content")
	}
	derivedStatus := TargetReady
	if derivedDiff.Empty() {
		derivedStatus = TargetNoOp
	}
	if target.Status != derivedStatus {
		return fmt.Errorf("target status %q does not match derived status %q", target.Status, derivedStatus)
	}
	return nil
}

func validateEndpointRef(endpoint EndpointRef) error {
	if err := adapter.ValidateStoreRef(endpoint.Store); err != nil {
		return err
	}
	return adapter.ValidateThemeRef(endpoint.Theme)
}

func canonicalStore(store adapter.StoreRef) string {
	return strings.TrimSuffix(strings.ToLower(string(store)), ".myshopify.com")
}

func canonicalRawSHA256(raw json.RawMessage, label string) (string, error) {
	_, digest, err := canonicalRawDocument(raw, label)
	return digest, err
}

func canonicalRawDocument(
	raw json.RawMessage,
	label string,
) (templatejson.Document, string, error) {
	document, err := templatejson.Parse(raw)
	if err != nil {
		return templatejson.Document{}, "", fmt.Errorf("parsing %s: %w", label, err)
	}
	if err := document.Validate(); err != nil {
		return templatejson.Document{}, "", fmt.Errorf("validating %s: %w", label, err)
	}
	digest, err := document.CanonicalSHA256()
	if err != nil {
		return templatejson.Document{}, "", fmt.Errorf("hashing %s: %w", label, err)
	}
	return document, digest, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func (m Manifest) computedSHA256() (string, error) {
	copy := m
	copy.ManifestSHA256 = ""
	return evidence.SHA256JSON(copy)
}

func (m *Manifest) seal() error {
	m.ManifestSHA256 = ""
	digest, err := m.computedSHA256()
	if err != nil {
		return fmt.Errorf("computing manifest SHA-256: %w", err)
	}
	m.ManifestSHA256 = digest
	return nil
}

func (m Manifest) ValidateIntegrity() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported manifest schema version %d", m.SchemaVersion)
	}
	if m.RunID == "" || m.UpdatedAt == "" {
		return errors.New("manifest run ID and updated time are required")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.UpdatedAt); err != nil {
		return fmt.Errorf("manifest updated time is invalid: %w", err)
	}
	if !validManifestStatus(m.Status) {
		return fmt.Errorf("invalid manifest status %q", m.Status)
	}
	if !validSHA256(m.ManifestSHA256) {
		return errors.New("manifest SHA-256 is invalid")
	}
	computed, err := m.computedSHA256()
	if err != nil {
		return err
	}
	if computed != m.ManifestSHA256 {
		return errors.New("manifest SHA-256 does not match manifest content")
	}
	return nil
}

func (m Manifest) ValidateAgainst(plan Plan, planPath string) error {
	if err := m.ValidateIntegrity(); err != nil {
		return err
	}
	if m.RunID != plan.RunID ||
		filepath.Clean(m.PlanPath) != filepath.Clean(planPath) ||
		m.PlanSHA256 != plan.PlanSHA256 ||
		m.Template != plan.Template ||
		!reflect.DeepEqual(m.Scope, plan.Scope) ||
		m.AllowRemove != plan.AllowRemove {
		return errors.New("manifest authority does not match the explicit plan")
	}
	if len(m.Targets) != len(plan.Targets) {
		return errors.New("manifest target count does not match the plan")
	}
	for position := range m.Targets {
		if err := validateManifestTarget(m.Targets[position], plan.Targets[position]); err != nil {
			return fmt.Errorf("validating manifest target %d: %w", position, err)
		}
	}
	if m.PreviewBinding != nil {
		expected := previewBindingFromPlan(plan)
		if err := evidence.MatchPreviewBinding(expected, *m.PreviewBinding); err != nil {
			return fmt.Errorf("validating manifest preview binding: %w", err)
		}
	}
	return validateManifestState(m, plan)
}

func validateManifestTarget(result TargetResult, planned PlannedTarget) error {
	if result.Index != planned.Index ||
		result.Identity != planned.Identity ||
		result.BeforeSHA256 != planned.BeforeSHA256 ||
		result.PlannedSHA256 != planned.PlannedSHA256 {
		return errors.New("manifest target authority does not match the plan")
	}
	if result.AfterSHA256 != "" && !validSHA256(result.AfterSHA256) {
		return errors.New("manifest target after SHA-256 is invalid")
	}
	if result.AfterSHA256 != "" &&
		result.Status != TargetApplied &&
		result.Status != TargetReadbackMismatch {
		return errors.New("manifest target status does not allow an after SHA-256")
	}
	if planned.Status == TargetNoOp &&
		result.Status != TargetNoOp &&
		result.Status != TargetPreflightFailed {
		return errors.New("no-op plan target has a mutating manifest status")
	}
	if result.ReadbackAttempted && !result.WriteAttempted {
		return errors.New("readback attempt requires a write attempt")
	}
	switch result.Status {
	case TargetNoOp:
		if planned.Status != TargetNoOp ||
			result.WriteAttempted ||
			result.ReadbackAttempted ||
			result.Failure != nil {
			return errors.New("invalid no-op target state")
		}
	case TargetReady:
		if planned.Status != TargetReady ||
			result.WriteAttempted ||
			result.ReadbackAttempted ||
			result.Failure != nil {
			return errors.New("invalid ready target state")
		}
	case TargetPreflightFailed:
		if result.WriteAttempted || result.ReadbackAttempted || result.Failure == nil {
			return errors.New("invalid preflight-failed target state")
		}
	case TargetWriteInProgress:
		if planned.Status != TargetReady ||
			!result.WriteAttempted ||
			result.ReadbackAttempted ||
			result.Failure != nil {
			return errors.New("invalid write-in-progress target state")
		}
	case TargetReadbackInProgress:
		if planned.Status != TargetReady ||
			!result.WriteAttempted ||
			!result.ReadbackAttempted ||
			result.Failure != nil {
			return errors.New("invalid readback-in-progress target state")
		}
	case TargetApplied:
		if planned.Status != TargetReady ||
			!result.WriteAttempted ||
			!result.ReadbackAttempted ||
			result.AfterSHA256 != planned.PlannedSHA256 {
			return errors.New("invalid applied target state")
		}
	case TargetWriteFailed:
		if planned.Status != TargetReady ||
			!result.WriteAttempted ||
			result.ReadbackAttempted ||
			result.Failure == nil {
			return errors.New("invalid write-failed target state")
		}
	case TargetReadbackFailed:
		if planned.Status != TargetReady ||
			!result.WriteAttempted ||
			!result.ReadbackAttempted ||
			result.Failure == nil {
			return errors.New("invalid readback-failed target state")
		}
	case TargetReadbackMismatch:
		if planned.Status != TargetReady ||
			!result.WriteAttempted ||
			!result.ReadbackAttempted ||
			result.AfterSHA256 == "" ||
			result.AfterSHA256 == planned.PlannedSHA256 ||
			result.Failure == nil {
			return errors.New("invalid readback-mismatch target state")
		}
	case TargetEvidenceFailed:
		if planned.Status != TargetReady || !result.WriteAttempted || result.Failure == nil {
			return errors.New("invalid evidence-failed target state")
		}
	case TargetSkippedAfterFailure:
		if planned.Status != TargetReady ||
			result.WriteAttempted ||
			result.ReadbackAttempted ||
			result.Failure != nil {
			return errors.New("invalid skipped target state")
		}
	default:
		return fmt.Errorf("unknown target status %q", result.Status)
	}
	return nil
}

func validateManifestState(manifest Manifest, plan Plan) error {
	failurePosition, err := validateManifestFailureAuthority(manifest, plan)
	if err != nil {
		return err
	}
	appliedCount := countTargetStatus(manifest.Targets, TargetApplied)
	hasMutation := targetsHaveMutationEvidence(manifest.Targets)

	switch manifest.Status {
	case StatusNoOp, StatusPlanned:
		if manifest.Status != plan.Status || manifest.Failure != nil {
			return errors.New("manifest planning status does not match the plan")
		}
		if !reflect.DeepEqual(manifest.Targets, targetResultsFromPlan(plan)) {
			return errors.New("manifest planning targets do not match the plan")
		}
	case StatusExecuting:
		if manifest.Failure != nil || !hasMutation || manifest.PreviewBinding == nil {
			return errors.New("invalid executing manifest state")
		}
		if err := validateExecutingTargets(manifest.Targets, plan.Targets); err != nil {
			return err
		}
	case StatusApplied:
		if manifest.Failure != nil || failurePosition >= 0 || manifest.PreviewBinding == nil {
			return errors.New("applied manifest must not contain a failure and requires preview authority")
		}
		for position, target := range manifest.Targets {
			expected := TargetNoOp
			if plan.Targets[position].Status == TargetReady {
				expected = TargetApplied
			}
			if target.Status != expected {
				return errors.New("applied manifest contains an unapplied target")
			}
		}
	case StatusFailed:
		if manifest.Failure == nil || appliedCount != 0 {
			return errors.New("failed manifest requires a failure record")
		}
		kind := FailureKind(manifest.Failure.Kind)
		if kind == FailureMismatch {
			return errors.New("failed manifest cannot contain a readback mismatch failure")
		}
		if failurePosition < 0 {
			if !validNonTargetFailureKind(kind) ||
				manifest.PreviewBinding != nil ||
				!reflect.DeepEqual(manifest.Targets, targetResultsFromPlan(plan)) {
				return errors.New("failed manifest without a target failure has invalid target state")
			}
			if kind == FailureRemoval && (!planHasRemoval(plan) || plan.AllowRemove) {
				return errors.New("removal failure does not match plan removal authority")
			}
			break
		}
		if hasMutation && manifest.PreviewBinding == nil {
			return errors.New("failed mutation manifest requires preview authority")
		}
		if hasMutation && manifest.Targets[failurePosition].Status == TargetPreflightFailed {
			return errors.New("preflight failure cannot contain mutation evidence")
		}
		if err := validateTargetsBeforeFailure(
			manifest.Targets,
			plan.Targets,
			failurePosition,
			hasMutation || manifest.PreviewBinding != nil,
		); err != nil {
			return err
		}
	case StatusReadbackMismatch:
		if manifest.Failure == nil || manifest.Failure.Kind != string(FailureMismatch) {
			return errors.New("readback-mismatch manifest requires a mismatch failure")
		}
		if failurePosition < 0 ||
			manifest.Targets[failurePosition].Status != TargetReadbackMismatch ||
			appliedCount != 0 ||
			!hasMutation ||
			manifest.PreviewBinding == nil {
			return errors.New("readback-mismatch manifest has contradictory terminal state")
		}
		if err := validateTargetsBeforeFailure(
			manifest.Targets,
			plan.Targets,
			failurePosition,
			true,
		); err != nil {
			return err
		}
	case StatusPartial:
		if manifest.Failure == nil ||
			failurePosition < 0 ||
			appliedCount == 0 ||
			!hasMutation ||
			manifest.PreviewBinding == nil {
			return errors.New("partial manifest requires mutation evidence and a failure")
		}
		if err := validateTargetsBeforeFailure(
			manifest.Targets,
			plan.Targets,
			failurePosition,
			true,
		); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid manifest status %q", manifest.Status)
	}
	return nil
}

func validateManifestFailureAuthority(manifest Manifest, plan Plan) (int, error) {
	failurePosition := -1
	for position, target := range manifest.Targets {
		if target.Failure == nil {
			continue
		}
		if failurePosition >= 0 {
			return -1, errors.New("manifest contains more than one target failure")
		}
		failurePosition = position
	}
	if manifest.Failure == nil {
		if failurePosition >= 0 {
			return -1, errors.New("target failure requires a manifest failure")
		}
		for _, target := range manifest.Targets {
			if target.Status == TargetSkippedAfterFailure {
				return -1, errors.New("skipped target requires a preceding target failure")
			}
		}
		return -1, nil
	}
	if !validFailureKind(FailureKind(manifest.Failure.Kind)) {
		return -1, fmt.Errorf("unknown manifest failure kind %q", manifest.Failure.Kind)
	}
	if failurePosition < 0 {
		for _, target := range manifest.Targets {
			if target.Status == TargetSkippedAfterFailure {
				return -1, errors.New("skipped target requires a target failure")
			}
		}
		return -1, nil
	}
	if *manifest.Targets[failurePosition].Failure != *manifest.Failure {
		return -1, errors.New("target failure does not match manifest failure")
	}
	if !targetStatusMatchesFailure(
		manifest.Targets[failurePosition].Status,
		FailureKind(manifest.Failure.Kind),
	) {
		return -1, errors.New("target status does not match manifest failure kind")
	}
	for position := failurePosition + 1; position < len(manifest.Targets); position++ {
		expected := TargetSkippedAfterFailure
		if plan.Targets[position].Status == TargetNoOp {
			expected = TargetNoOp
		}
		if manifest.Targets[position].Status != expected {
			return -1, fmt.Errorf("target %d has invalid status after failure", position)
		}
	}
	return failurePosition, nil
}

func validateTargetsBeforeFailure(
	targets []TargetResult,
	planned []PlannedTarget,
	failurePosition int,
	executionStarted bool,
) error {
	for position := 0; position < failurePosition; position++ {
		expected := TargetNoOp
		if planned[position].Status == TargetReady {
			expected = TargetReady
			if executionStarted {
				expected = TargetApplied
			}
		}
		if targets[position].Status != expected {
			return fmt.Errorf("target %d has invalid status before failure", position)
		}
	}
	return nil
}

func validateExecutingTargets(targets []TargetResult, planned []PlannedTarget) error {
	pendingStarted := false
	hasPending := false
	for position, target := range targets {
		if planned[position].Status == TargetNoOp {
			if target.Status != TargetNoOp {
				return fmt.Errorf("executing manifest changed no-op target %d", position)
			}
			continue
		}
		switch target.Status {
		case TargetApplied:
			if pendingStarted {
				return errors.New("executing manifest applies targets out of order")
			}
		case TargetWriteInProgress, TargetReadbackInProgress:
			if pendingStarted {
				return errors.New("executing manifest has multiple active phases")
			}
			pendingStarted = true
			hasPending = true
		case TargetReady:
			pendingStarted = true
			hasPending = true
		default:
			return fmt.Errorf("executing manifest has terminal target status %q", target.Status)
		}
	}
	if !hasPending {
		return errors.New("executing manifest has no in-progress or remaining target")
	}
	return nil
}

func targetStatusMatchesFailure(status TargetStatus, kind FailureKind) bool {
	switch status {
	case TargetPreflightFailed:
		return kind == FailurePreflight || kind == FailureInterrupted
	case TargetWriteFailed:
		return kind == FailureWrite
	case TargetReadbackFailed:
		return kind == FailureReadback
	case TargetReadbackMismatch:
		return kind == FailureMismatch
	case TargetEvidenceFailed, TargetApplied:
		return kind == FailureEvidence
	default:
		return false
	}
}

func validFailureKind(kind FailureKind) bool {
	switch kind {
	case FailureUsage,
		FailureNeedsSetup,
		FailurePreflight,
		FailurePlan,
		FailureBinding,
		FailureRemoval,
		FailureWrite,
		FailureReadback,
		FailureMismatch,
		FailureEvidence,
		FailureInterrupted:
		return true
	default:
		return false
	}
}

func validNonTargetFailureKind(kind FailureKind) bool {
	switch kind {
	case FailurePreflight,
		FailurePlan,
		FailureBinding,
		FailureRemoval,
		FailureInterrupted:
		return true
	default:
		return false
	}
}

func countTargetStatus(targets []TargetResult, status TargetStatus) int {
	count := 0
	for _, target := range targets {
		if target.Status == status {
			count++
		}
	}
	return count
}

func targetsHaveMutationEvidence(targets []TargetResult) bool {
	for _, target := range targets {
		if target.WriteAttempted || target.ReadbackAttempted {
			return true
		}
	}
	return false
}

func validManifestStatus(status Status) bool {
	switch status {
	case StatusNoOp,
		StatusPlanned,
		StatusExecuting,
		StatusApplied,
		StatusFailed,
		StatusReadbackMismatch,
		StatusPartial:
		return true
	default:
		return false
	}
}

func previewBindingFromPlan(plan Plan) evidence.PreviewBinding {
	targets := make([]evidence.BoundTarget, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		targets = append(targets, evidence.BoundTarget{
			Identity:      identity(target.Identity),
			BeforeSHA256:  target.BeforeSHA256,
			PlannedSHA256: target.PlannedSHA256,
		})
	}
	return evidence.PreviewBinding{
		PlanSHA256:         plan.PlanSHA256,
		AllowRemove:        plan.AllowRemove,
		Template:           string(plan.Template),
		Scope:              plan.Scope,
		Source:             identity(plan.Source.Identity),
		SourceBeforeSHA256: plan.Source.BeforeSHA256,
		Targets:            targets,
	}
}

func writeManifest(store *evidence.Store, manifest *Manifest) error {
	if err := manifest.seal(); err != nil {
		return err
	}
	return store.WriteJSON("manifest.json", *manifest)
}

func validateUniqueTargetIdentities(targets []PlannedTarget) error {
	seen := make(map[string]int, len(targets))
	for position, target := range targets {
		store := strings.TrimSuffix(strings.ToLower(target.Identity.Store), ".myshopify.com")
		key := store + "\x00" + target.Identity.ID
		if previous, exists := seen[key]; exists {
			return fmt.Errorf(
				"targets %d and %d resolve to the same theme %s/%s",
				previous,
				position,
				store,
				target.Identity.ID,
			)
		}
		seen[key] = position
	}
	return nil
}

func identity(theme adapter.ResolvedTheme) evidence.Identity {
	return evidence.Identity{
		Store: theme.Store,
		ID:    theme.ID,
		Name:  theme.Name,
		Role:  theme.Role,
	}
}

func targetResultsFromPlan(plan Plan) []TargetResult {
	results := make([]TargetResult, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		results = append(results, TargetResult{
			Index:         target.Index,
			Identity:      target.Identity,
			Status:        target.Status,
			BeforeSHA256:  target.BeforeSHA256,
			PlannedSHA256: target.PlannedSHA256,
		})
	}
	return results
}

func contextFailureKind(ctx context.Context) FailureKind {
	if ctx.Err() != nil {
		return FailureInterrupted
	}
	return FailurePreflight
}
