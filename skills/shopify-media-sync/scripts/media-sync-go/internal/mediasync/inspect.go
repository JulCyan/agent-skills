package mediasync

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	inspectIntegrityMatch = "MATCH"
	inspectIntegrityDrift = "DRIFT"
)

type InspectDigest struct {
	Version                int             `json:"version"`
	RunID                  string          `json:"run_id"`
	PlanPath               string          `json:"plan_path"`
	PlanSHA256             string          `json:"plan_sha256"`
	EvidencePath           string          `json:"evidence_path"`
	SummaryPath            string          `json:"summary_path"`
	AttemptsPath           string          `json:"attempts_path"`
	AttemptCount           int             `json:"attempt_count"`
	AttemptHistory         string          `json:"attempt_history"`
	LastAttempt            *InspectAttempt `json:"last_attempt,omitempty"`
	ExecutionLease         string          `json:"execution_lease"`
	FinalStatus            string          `json:"final_status"`
	CurrentStage           string          `json:"current_stage,omitempty"`
	FailedStage            string          `json:"failed_stage,omitempty"`
	ResourceIntegrity      string          `json:"resource_integrity"`
	PlanSHAMatchesEvidence bool            `json:"plan_sha_matches_evidence"`
	PlanSHAMatchesSummary  bool            `json:"plan_sha_matches_summary"`
	SafeToExecute          bool            `json:"safe_to_execute"`
	Stores                 []string        `json:"stores"`
	Rows                   int             `json:"rows"`
	Actions                map[string]int  `json:"actions"`
	PlanningErrors         int             `json:"planning_errors"`
	Counts                 ApplyCounts     `json:"counts"`
	NeedsAttention         bool            `json:"needs_attention"`
	NeedsExternal          bool            `json:"needs_external"`
	NextSafeAction         string          `json:"next_safe_action"`
	Problems               []string        `json:"problems"`
	Errors                 []ApplyRowError `json:"errors"`
	Authority              []string        `json:"authority"`
}

type InspectAttempt struct {
	AttemptID        string   `json:"attempt_id"`
	Mode             string   `json:"mode"`
	EvidencePath     string   `json:"evidence_path"`
	SummaryPath      string   `json:"summary_path"`
	FinalStatus      string   `json:"final_status"`
	CurrentStage     string   `json:"current_stage,omitempty"`
	FailedStage      string   `json:"failed_stage,omitempty"`
	RequestedStores  string   `json:"requested_stores"`
	SelectedStores   []string `json:"selected_stores"`
	StoresConfigPath string   `json:"stores_config_path,omitempty"`
	StartedAt        string   `json:"started_at"`
	FinishedAt       string   `json:"finished_at"`
	DurationMS       int64    `json:"duration_ms"`
}

func runInspect(stdout io.Writer, opts commandOptions) error {
	plan, planPath, err := loadPlanForInspect(opts)
	if err != nil {
		return err
	}
	planResource, err := inspectResource(planPath)
	if err != nil {
		return err
	}
	attemptsPath := filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl")
	attemptInspection, err := inspectApplyAttempts(attemptsPath, plan.RunID, planPath, planResource.SHA256)
	if err != nil {
		return err
	}
	evidenceExplicit := opts.evidencePath != ""
	evidencePath := opts.evidencePath
	if evidencePath == "" && attemptInspection.LastEvidencePath != "" {
		evidencePath = attemptInspection.LastEvidencePath
	}
	if evidencePath == "" {
		evidencePath = filepath.Join(filepath.Dir(planPath), "evidence.json")
	}
	summaryExplicit := opts.summaryPath != ""
	summaryPath := opts.summaryPath
	if summaryPath == "" && attemptInspection.LastSummaryPath != "" {
		summaryPath = attemptInspection.LastSummaryPath
	}
	if summaryPath == "" {
		summaryPath = filepath.Join(filepath.Dir(planPath), "apply-summary.json")
	}
	evidence, err := loadEvidenceForInspect(evidencePath, plan.RunID)
	if err != nil {
		return err
	}
	summary, summaryExists, err := loadApplySummaryForInspect(summaryPath)
	if err != nil {
		return err
	}

	digest := InspectDigest{
		Version: 1, RunID: plan.RunID, PlanPath: planPath, PlanSHA256: planResource.SHA256,
		EvidencePath: evidencePath, SummaryPath: summaryPath, AttemptsPath: attemptsPath,
		AttemptCount: attemptInspection.Count, LastAttempt: attemptInspection.LastAttempt, FinalStatus: "NOT_STARTED", ResourceIntegrity: inspectIntegrityMatch, ExecutionLease: "NOT_APPLICABLE",
		Stores: append([]string(nil), plan.Summary.Stores...), Rows: plan.Summary.Rows,
		Actions: copyActionCounts(plan.Summary.ByAction), Problems: []string{}, Errors: []ApplyRowError{},
		Authority: []string{planPath, evidencePath, summaryPath, attemptsPath},
	}
	if evidenceExplicit && attemptInspection.LastEvidencePath != "" && filepath.Clean(attemptInspection.LastEvidencePath) != evidencePath {
		digest.Problems = append(digest.Problems, fmt.Sprintf("显式 evidence 与最后 attempt authority 不一致: explicit=%s attempt=%s", evidencePath, attemptInspection.LastEvidencePath))
	}
	if summaryExplicit && attemptInspection.LastSummaryPath != "" && filepath.Clean(attemptInspection.LastSummaryPath) != summaryPath {
		digest.Problems = append(digest.Problems, fmt.Sprintf("显式 summary 与最后 attempt authority 不一致: explicit=%s attempt=%s", summaryPath, attemptInspection.LastSummaryPath))
	}
	if summaryExists {
		digest.FinalStatus = summary.FinalStatus
		digest.CurrentStage = summary.CurrentStage
		digest.FailedStage = summary.FailedStage
		digest.Counts = summary.Counts
		digest.NeedsAttention = summary.NeedsAttention
		digest.NeedsExternal = summary.NeedsExternal
		digest.NextSafeAction = summary.NextSafeAction
		digest.Errors = append([]ApplyRowError(nil), summary.Errors...)
		digest.PlanSHAMatchesSummary = summary.PlanSHA256 == planResource.SHA256
		if !digest.PlanSHAMatchesSummary {
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary plan SHA 与当前 plan 不一致: summary=%s actual=%s", summary.PlanSHA256, planResource.SHA256))
		}
		if summary.RunID != plan.RunID {
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary run_id 与 plan 不一致: summary=%s plan=%s", summary.RunID, plan.RunID))
		}
		if summary.PlanPath != "" && filepath.Clean(summary.PlanPath) != planPath {
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary plan_path 与显式 plan 不一致: summary=%s plan=%s", summary.PlanPath, planPath))
		}
		if summary.EvidencePath != "" && filepath.Clean(summary.EvidencePath) != evidencePath {
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary evidence_path 与当前 evidence 不一致: summary=%s evidence=%s", summary.EvidencePath, evidencePath))
		}
		if summary.SummaryPath != "" && filepath.Clean(summary.SummaryPath) != summaryPath {
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary summary_path 与当前 summary 不一致: summary=%s actual=%s", summary.SummaryPath, summaryPath))
		}
		if summary.AttemptsPath != "" && filepath.Clean(summary.AttemptsPath) != attemptsPath {
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary attempts_path 与当前 journal 不一致: summary=%s attempts=%s", summary.AttemptsPath, attemptsPath))
		}
	} else {
		digest.NextSafeAction = "先对显式 plan 执行 apply preview；不得直接 execute"
		if attemptInspection.Count > 0 {
			digest.Problems = append(digest.Problems, "attempt history 已存在，但对应 apply summary 缺失")
		}
	}
	switch {
	case attemptInspection.Status == "CORRUPT":
		digest.AttemptHistory = "CORRUPT"
		digest.Problems = append(digest.Problems, attemptInspection.Problem)
	case attemptInspection.Status == "MISMATCH":
		digest.AttemptHistory = "MISMATCH"
		digest.Problems = append(digest.Problems, attemptInspection.Problem)
	case attemptInspection.Status == "PRESENT":
		digest.AttemptHistory = "PRESENT"
	case summaryExists:
		digest.AttemptHistory = "MISSING_LEGACY"
	default:
		digest.AttemptHistory = "NONE"
	}
	if summaryExists && summary.FinalStatus != applyStatusRunning && summary.AttemptID != "" {
		if attemptInspection.Status == "MISSING" {
			digest.AttemptHistory = "MISMATCH"
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary attempt_id=%s 不存在于 attempt history", summary.AttemptID))
		} else if attemptInspection.LastAttemptID != summary.AttemptID || attemptInspection.LastFinalStatus != summary.FinalStatus {
			digest.AttemptHistory = "MISMATCH"
			digest.Problems = append(digest.Problems, fmt.Sprintf("summary attempt_id/终态与最后 attempt 不一致: summary=%s/%s attempt=%s/%s", summary.AttemptID, summary.FinalStatus, attemptInspection.LastAttemptID, attemptInspection.LastFinalStatus))
		}
	}
	staleRunning := false
	if summaryExists && summary.FinalStatus == applyStatusRunning {
		leaseOpts := opts
		leaseOpts.evidencePath = evidencePath
		active, leaseErr := executionLeaseActive(leaseOpts)
		if leaseErr != nil {
			digest.ExecutionLease = "UNKNOWN"
			digest.Problems = append(digest.Problems, "无法只读确认 execution lease: "+leaseErr.Error())
		} else if active {
			digest.ExecutionLease = "ACTIVE"
		} else {
			digest.ExecutionLease = "INACTIVE"
			staleRunning = true
			digest.Problems = append(digest.Problems, "summary 为 RUNNING，但 execution lease 已释放")
		}
	}
	digest.PlanningErrors = plan.Summary.Errors
	if plan.Summary.Errors > 0 {
		digest.Problems = append(digest.Problems, fmt.Sprintf("plan 包含 %d 个规划错误", plan.Summary.Errors))
		digest.NextSafeAction = "修复规划错误并重新生成 plan；不得执行当前 plan"
	}
	digest.PlanSHAMatchesEvidence = evidence.PlanSHA256 != "" && evidence.PlanSHA256 == planResource.SHA256
	if evidence.RunID != plan.RunID {
		digest.Problems = append(digest.Problems, fmt.Sprintf("evidence run_id 与 plan 不一致: evidence=%s plan=%s", evidence.RunID, plan.RunID))
	}
	if evidence.PlanSHA256 == "" {
		digest.Problems = append(digest.Problems, "evidence plan SHA 尚未通过 apply preview 绑定")
	}
	if evidence.PlanSHA256 != "" && !digest.PlanSHAMatchesEvidence {
		digest.Problems = append(digest.Problems, fmt.Sprintf("evidence plan SHA 与当前 plan 不一致: evidence=%s actual=%s", evidence.PlanSHA256, planResource.SHA256))
	}
	if resourceErr := validateApplyResources(plan.Changes); resourceErr != nil {
		digest.ResourceIntegrity = inspectIntegrityDrift
		digest.Problems = append(digest.Problems, resourceErr.Error())
		digest.NextSafeAction = "本地资源已漂移；重新生成 plan，不得执行旧 plan"
	}
	digest.SafeToExecute = digest.FinalStatus == applyStatusPreviewReady && digest.ResourceIntegrity == inspectIntegrityMatch && digest.PlanSHAMatchesEvidence && digest.PlanSHAMatchesSummary && len(digest.Problems) == 0
	if len(digest.Problems) > 0 && digest.ResourceIntegrity == inspectIntegrityMatch && digest.PlanningErrors == 0 {
		digest.NextSafeAction = "检查并修复 authority problems；必要时重新运行 apply preview，当前不得 execute"
	}
	if staleRunning {
		digest.NextSafeAction = "RUNNING 已失去 execution lease；读取 evidence/checkpoint 判断安全恢复，不要继续等待或盲目 execute"
	}
	if digest.FinalStatus == applyStatusSucceeded && len(digest.Problems) == 0 {
		digest.SafeToExecute = false
		digest.NextSafeAction = "本次 run 已成功；保留 authority artifacts，不要再次 apply"
	}
	if digest.Problems == nil {
		digest.Problems = []string{}
	}
	if digest.Errors == nil {
		digest.Errors = []ApplyRowError{}
	}

	if opts.format == "json" {
		return json.NewEncoder(stdout).Encode(digest)
	}
	fmt.Fprintf(stdout, "run_id=%s\nplan=%s\nplan_sha256=%s\nstatus=%s\nresource_integrity=%s\nsafe_to_execute=%t\nattempts=%d\nnext_safe_action=%s\n",
		digest.RunID, digest.PlanPath, digest.PlanSHA256, digest.FinalStatus, digest.ResourceIntegrity, digest.SafeToExecute, digest.AttemptCount, digest.NextSafeAction)
	return nil
}

func loadApplySummaryForInspect(path string) (ApplySummary, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ApplySummary{}, false, nil
		}
		return ApplySummary{}, false, err
	}
	var summary ApplySummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return ApplySummary{}, false, fmt.Errorf("解析 apply summary 失败: %w", err)
	}
	return summary, true, nil
}

func loadPlanForInspect(opts commandOptions) (Plan, string, error) {
	planPath, err := resolvePlanPath(opts)
	if err != nil {
		return Plan{}, "", err
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return Plan{}, "", err
	}
	var plan Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return Plan{}, "", fmt.Errorf("解析 plan.json 失败: %w", err)
	}
	return plan, planPath, nil
}

func loadEvidenceForInspect(path, runID string) (Evidence, error) {
	evidence, err := LoadEvidence(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Evidence{}, err
		}
		return Evidence{RunID: runID, Results: []EvidenceRecord{}}, nil
	}
	if evidence.RunID == "" {
		evidence.RunID = runID
	}
	return evidence, nil
}

type applyAttemptInspection struct {
	Count            int
	Status           string
	Problem          string
	LastAttemptID    string
	LastFinalStatus  string
	LastAttempt      *InspectAttempt
	LastEvidencePath string
	LastSummaryPath  string
}

func inspectApplyAttempts(path, runID, planPath, planSHA256 string) (applyAttemptInspection, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return applyAttemptInspection{Status: "MISSING"}, nil
		}
		return applyAttemptInspection{}, err
	}
	defer file.Close()
	result := applyAttemptInspection{Status: "PRESENT"}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	seenAttemptIDs := map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var attempt ApplyAttempt
		if err := json.Unmarshal([]byte(line), &attempt); err != nil {
			result.Status = "CORRUPT"
			result.Problem = fmt.Sprintf("apply attempt history 第 %d 行损坏: %v", result.Count+1, err)
			return result, nil
		}
		if attempt.Version != 1 || attempt.AttemptID == "" || seenAttemptIDs[attempt.AttemptID] || attempt.RunID != runID || filepath.Clean(attempt.PlanPath) != planPath || attempt.PlanSHA256 != planSHA256 || attempt.EvidencePath == "" || attempt.SummaryPath == "" || filepath.Clean(attempt.AttemptsPath) != path {
			result.Status = "MISMATCH"
			result.Problem = fmt.Sprintf("attempt authority 不匹配: attempt_id=%s attempt run_id=%s plan_path=%s plan_sha256=%s attempts_path=%s", attempt.AttemptID, attempt.RunID, attempt.PlanPath, attempt.PlanSHA256, attempt.AttemptsPath)
			return result, nil
		}
		seenAttemptIDs[attempt.AttemptID] = true
		result.Count++
		result.LastAttemptID = attempt.AttemptID
		result.LastFinalStatus = attempt.FinalStatus
		result.LastEvidencePath = filepath.Clean(attempt.EvidencePath)
		result.LastSummaryPath = filepath.Clean(attempt.SummaryPath)
		result.LastAttempt = &InspectAttempt{
			AttemptID: attempt.AttemptID, Mode: attempt.Mode, FinalStatus: attempt.FinalStatus,
			EvidencePath: attempt.EvidencePath, SummaryPath: attempt.SummaryPath,
			CurrentStage: attempt.CurrentStage, FailedStage: attempt.FailedStage,
			RequestedStores: attempt.RequestedStores, SelectedStores: append([]string(nil), attempt.SelectedStores...),
			StoresConfigPath: attempt.StoresConfigPath, StartedAt: attempt.StartedAt,
			FinishedAt: attempt.FinishedAt, DurationMS: attempt.DurationMS,
		}
	}
	if err := scanner.Err(); err != nil {
		result.Status = "CORRUPT"
		result.Problem = "读取 apply attempt history 失败: " + err.Error()
		return result, nil
	}
	return result, nil
}

func copyActionCounts(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
