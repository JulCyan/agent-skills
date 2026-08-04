package mediasync

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectRequiresExplicitPlanAndRejectsExecute(t *testing.T) {
	if err := run(t.Context(), []string{"inspect", "--format", "json"}, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "inspect 必须显式指定 --plan") {
		t.Fatalf("inspect must require an exact plan, got %v", err)
	}
	if err := run(t.Context(), []string{"inspect", "--plan", "plan.json", "--execute"}, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "inspect 不接受 --execute") {
		t.Fatalf("inspect must remain read-only, got %v", err)
	}
}

func TestInspectReturnsStableReadOnlyResumeDigest(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename", "alt_update")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	paths := []string{planPath, evidencePath, summaryPath, filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl")}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = raw
	}

	var stdout bytes.Buffer
	args := []string{"inspect", "--plan", planPath, "--evidence", evidencePath, "--summary", summaryPath, "--format", "json"}
	if err := run(t.Context(), args, &stdout); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		Version        int    `json:"version"`
		RunID          string `json:"run_id"`
		PlanPath       string `json:"plan_path"`
		PlanSHA256     string `json:"plan_sha256"`
		EvidencePath   string `json:"evidence_path"`
		SummaryPath    string `json:"summary_path"`
		AttemptsPath   string `json:"attempts_path"`
		AttemptCount   int    `json:"attempt_count"`
		AttemptHistory string `json:"attempt_history"`
		LastAttempt    *struct {
			AttemptID   string `json:"attempt_id"`
			Mode        string `json:"mode"`
			FinalStatus string `json:"final_status"`
			DurationMS  int64  `json:"duration_ms"`
		} `json:"last_attempt"`
		FinalStatus       string         `json:"final_status"`
		ResourceIntegrity string         `json:"resource_integrity"`
		SafeToExecute     bool           `json:"safe_to_execute"`
		Stores            []string       `json:"stores"`
		Rows              int            `json:"rows"`
		Actions           map[string]int `json:"actions"`
		NextSafeAction    string         `json:"next_safe_action"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatalf("inspect stdout must be stable JSON: %v\n%s", err, stdout.String())
	}
	if digest.Version != 1 || digest.RunID != plan.RunID || digest.PlanPath != planPath || digest.PlanSHA256 == "" {
		t.Fatalf("inspect identity incomplete: %+v", digest)
	}
	if digest.EvidencePath != evidencePath || digest.SummaryPath != summaryPath || digest.AttemptCount != 1 || digest.AttemptHistory != "PRESENT" {
		t.Fatalf("inspect artifact links incomplete: %+v", digest)
	}
	if digest.LastAttempt == nil || digest.LastAttempt.AttemptID == "" || digest.LastAttempt.Mode != "preview" || digest.LastAttempt.FinalStatus != applyStatusPreviewReady || digest.LastAttempt.DurationMS < 0 {
		t.Fatalf("inspect last attempt timing/provenance incomplete: %+v", digest.LastAttempt)
	}
	if digest.FinalStatus != applyStatusPreviewReady || digest.ResourceIntegrity != "MATCH" || !digest.SafeToExecute {
		t.Fatalf("inspect authority state incorrect: %+v", digest)
	}
	if len(digest.Stores) != 1 || digest.Stores[0] != "store-a" || digest.Rows != 1 || digest.Actions["alt_update"] != 1 || digest.NextSafeAction == "" {
		t.Fatalf("inspect scope/next action incomplete: %+v", digest)
	}
	for _, path := range paths {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before[path], after) {
			t.Fatalf("inspect mutated authority artifact %s", path)
		}
	}
}

func TestInspectLegacySummaryDoesNotTreatMissingAttemptHistoryAsNotExecuted(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	planResource, err := inspectResource(planPath)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteJSON(t, evidencePath, Evidence{RunID: plan.RunID, PlanSHA256: planResource.SHA256})
	mustWriteJSON(t, summaryPath, ApplySummary{
		Version: 1, RunID: plan.RunID, PlanPath: planPath, PlanSHA256: planResource.SHA256,
		EvidencePath: evidencePath, SummaryPath: summaryPath, FinalStatus: applyStatusSucceeded,
		Counts: ApplyCounts{Total: 1, Success: 1}, NextSafeAction: "保留 artifacts",
	})
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		FinalStatus    string `json:"final_status"`
		AttemptCount   int    `json:"attempt_count"`
		AttemptHistory string `json:"attempt_history"`
		SafeToExecute  bool   `json:"safe_to_execute"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	if digest.FinalStatus != applyStatusSucceeded || digest.AttemptCount != 0 || digest.AttemptHistory != "MISSING_LEGACY" || digest.SafeToExecute {
		t.Fatalf("legacy successful summary was misclassified: %+v", digest)
	}
}

func TestInspectPlanningErrorsAreNeverSafeToExecute(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	plan.Summary.Errors = 1
	plan.Changes[0].Status = "error"
	plan.Changes[0].Reason = "missing resource mapping"
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		SafeToExecute  bool     `json:"safe_to_execute"`
		PlanningErrors int      `json:"planning_errors"`
		Problems       []string `json:"problems"`
		NextSafeAction string   `json:"next_safe_action"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	if digest.SafeToExecute || digest.PlanningErrors != 1 || len(digest.Problems) == 0 || !strings.Contains(digest.NextSafeAction, "规划错误") {
		t.Fatalf("planning errors were marked executable: %+v", digest)
	}
}

func TestInspectMismatchedAuthorityIdentityIsNeverSafeToExecute(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	var evidence Evidence
	readJSONFixture(t, evidencePath, &evidence)
	evidence.RunID = "different-evidence-run"
	mustWriteJSON(t, evidencePath, evidence)
	summary := readApplySummary(t, summaryPath)
	summary.RunID = "different-summary-run"
	summary.PlanPath = filepath.Join(filepath.Dir(planPath), "other-plan.json")
	mustWriteJSON(t, summaryPath, summary)

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		SafeToExecute bool     `json:"safe_to_execute"`
		Problems      []string `json:"problems"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(digest.Problems, "\n")
	if digest.SafeToExecute || !strings.Contains(joined, "evidence run_id") || !strings.Contains(joined, "summary run_id") || !strings.Contains(joined, "summary plan_path") {
		t.Fatalf("mismatched authority identity was accepted: %+v", digest)
	}
}

func TestInspectCorruptAttemptJournalReturnsStableFailClosedDigest(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	attemptsPath := filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl")
	file, err := os.OpenFile(attemptsPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{truncated\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatalf("corrupt journal must still return a machine digest: %v", err)
	}
	var digest struct {
		AttemptCount   int      `json:"attempt_count"`
		AttemptHistory string   `json:"attempt_history"`
		SafeToExecute  bool     `json:"safe_to_execute"`
		Problems       []string `json:"problems"`
		NextSafeAction string   `json:"next_safe_action"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	if digest.AttemptCount != 1 || digest.AttemptHistory != "CORRUPT" || digest.SafeToExecute || len(digest.Problems) == 0 || strings.Contains(digest.NextSafeAction, "追加 --execute") {
		t.Fatalf("corrupt journal was not reported fail-closed: %+v", digest)
	}
}

func TestInspectMissingEvidencePlanBindingRequiresPreview(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	mustWriteJSON(t, evidencePath, Evidence{RunID: plan.RunID})

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		SafeToExecute  bool     `json:"safe_to_execute"`
		Problems       []string `json:"problems"`
		NextSafeAction string   `json:"next_safe_action"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	if digest.SafeToExecute || len(digest.Problems) == 0 || !strings.Contains(strings.Join(digest.Problems, "\n"), "evidence plan SHA") || !strings.Contains(digest.NextSafeAction, "preview") {
		t.Fatalf("missing preview binding did not fail closed: %+v", digest)
	}
}

func TestInspectRejectsAttemptJournalAuthorityAndSummaryLinkMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ApplyAttempt, *ApplySummary)
		want   string
	}{
		{name: "journal run id", mutate: func(attempt *ApplyAttempt, _ *ApplySummary) { attempt.RunID = "other-run" }, want: "attempt run_id"},
		{name: "summary attempt link", mutate: func(_ *ApplyAttempt, summary *ApplySummary) { summary.AttemptID = "missing-attempt" }, want: "summary attempt_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := basicApplyPlan(t, "file_upload_same_filename")
			planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
			runner := &scriptedApplyStageRunner{}
			installApplyRunner(t, runner)
			if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
				t.Fatal(err)
			}
			summary := readApplySummary(t, summaryPath)
			attemptsPath := filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl")
			var attempt ApplyAttempt
			raw, err := os.ReadFile(attemptsPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(bytes.TrimSpace(raw), &attempt); err != nil {
				t.Fatal(err)
			}
			test.mutate(&attempt, &summary)
			mustWriteJSON(t, summaryPath, summary)
			encoded, err := json.Marshal(attempt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(attemptsPath, append(encoded, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}

			var stdout bytes.Buffer
			if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
				t.Fatal(err)
			}
			var digest struct {
				AttemptHistory string   `json:"attempt_history"`
				SafeToExecute  bool     `json:"safe_to_execute"`
				Problems       []string `json:"problems"`
				NextSafeAction string   `json:"next_safe_action"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
				t.Fatal(err)
			}
			if digest.AttemptHistory != "MISMATCH" || digest.SafeToExecute || !strings.Contains(strings.Join(digest.Problems, "\n"), test.want) || strings.Contains(digest.NextSafeAction, "追加 --execute") {
				t.Fatalf("journal authority mismatch was accepted: %+v", digest)
			}
		})
	}
}

func TestInspectAcceptsLargeValidAttemptLine(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	summary := readApplySummary(t, summaryPath)
	attemptsPath := filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl")
	attempt := ApplyAttempt{
		Version: 1, AttemptID: summary.AttemptID, RunID: plan.RunID, Mode: "preview",
		PlanPath: planPath, PlanSHA256: summary.PlanSHA256, EvidencePath: evidencePath,
		SummaryPath: summaryPath, AttemptsPath: attemptsPath, FinalStatus: summary.FinalStatus,
		Errors: []ApplyRowError{{Stage: "preview", Message: strings.Repeat("x", 80*1024)}},
	}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attemptsPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatalf("large valid journal line must remain inspectable: %v", err)
	}
	var digest struct {
		AttemptCount   int      `json:"attempt_count"`
		AttemptHistory string   `json:"attempt_history"`
		Problems       []string `json:"problems"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	if digest.AttemptCount != 1 || digest.AttemptHistory != "PRESENT" || len(digest.Problems) != 0 {
		t.Fatalf("large valid journal line was rejected: %+v", digest)
	}
}

func TestInspectDistinguishesActiveAndStaleRunningSummary(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	summary := readApplySummary(t, summaryPath)
	summary.AttemptID = "running-attempt"
	summary.FinalStatus = applyStatusRunning
	summary.CurrentStage = applyStageUpload
	summary.NextSafeAction = "等待当前阶段完成"
	mustWriteJSON(t, summaryPath, summary)

	inspect := func(t *testing.T) struct {
		ExecutionLease string   `json:"execution_lease"`
		Problems       []string `json:"problems"`
		NextSafeAction string   `json:"next_safe_action"`
	} {
		t.Helper()
		var stdout bytes.Buffer
		if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
			t.Fatal(err)
		}
		var digest struct {
			ExecutionLease string   `json:"execution_lease"`
			Problems       []string `json:"problems"`
			NextSafeAction string   `json:"next_safe_action"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
			t.Fatal(err)
		}
		return digest
	}

	stale := inspect(t)
	if stale.ExecutionLease != "INACTIVE" || len(stale.Problems) == 0 || !strings.Contains(stale.NextSafeAction, "checkpoint") {
		t.Fatalf("stale RUNNING summary was not identified: %+v", stale)
	}
	lease, err := acquireExecutionLease(commandOptions{command: "apply", planPath: planPath, evidencePath: evidencePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.release() })
	active := inspect(t)
	if active.ExecutionLease != "ACTIVE" || len(active.Problems) != 0 || !strings.Contains(active.NextSafeAction, "等待") {
		t.Fatalf("active RUNNING summary was misclassified: %+v", active)
	}
}

func TestInspectDiscoversCustomAuthorityPathsFromAttemptJournal(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, _, _ := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	authorityDir := t.TempDir()
	evidencePath := filepath.Join(authorityDir, "custom-evidence.json")
	summaryPath := filepath.Join(authorityDir, "custom-summary.json")
	mustWriteJSON(t, evidencePath, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var digest struct {
		EvidencePath   string   `json:"evidence_path"`
		SummaryPath    string   `json:"summary_path"`
		FinalStatus    string   `json:"final_status"`
		AttemptHistory string   `json:"attempt_history"`
		SafeToExecute  bool     `json:"safe_to_execute"`
		Problems       []string `json:"problems"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	if digest.EvidencePath != evidencePath || digest.SummaryPath != summaryPath || digest.FinalStatus != applyStatusPreviewReady || digest.AttemptHistory != "PRESENT" || !digest.SafeToExecute || len(digest.Problems) != 0 {
		t.Fatalf("inspect did not recover custom authority from journal: %+v", digest)
	}
}

func TestInspectReportsResourceDriftWithoutMutatingArtifacts(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Changes[0].Resource.Path, []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"inspect", "--plan", planPath, "--format", "json"}, &stdout); err != nil {
		t.Fatalf("inspect should report drift in JSON without executing: %v", err)
	}
	var digest struct {
		ResourceIntegrity string   `json:"resource_integrity"`
		SafeToExecute     bool     `json:"safe_to_execute"`
		Problems          []string `json:"problems"`
		NextSafeAction    string   `json:"next_safe_action"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &digest); err != nil {
		t.Fatal(err)
	}
	if digest.ResourceIntegrity != "DRIFT" || digest.SafeToExecute || len(digest.Problems) == 0 || !strings.Contains(digest.NextSafeAction, "重新生成 plan") {
		t.Fatalf("inspect did not fail closed on resource drift: %+v", digest)
	}
}
