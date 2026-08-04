package mediasync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	applyStageUpload         = "upload"
	applyStageUploadReadback = "upload_readback"
	applyStageAlt            = "alt"
	applyStageAltReadback    = "alt_readback"
	applyStageVerify         = "verify"

	stageStatusNotStarted       = "NOT_STARTED"
	stageStatusInProgress       = "IN_PROGRESS"
	stageStatusAwaitingReadback = "AWAITING_READBACK"
	stageStatusSucceeded        = "SUCCEEDED"
	stageStatusFailed           = "FAILED"
	stageStatusSkipped          = "SKIPPED"

	applyStatusPreviewReady    = "PREVIEW_READY"
	applyStatusRunning         = "RUNNING"
	applyStatusSucceeded       = "SUCCEEDED"
	applyStatusPartialFailure  = "PARTIAL_FAILURE"
	applyStatusFailed          = "FAILED"
	applyStatusNeedsAttention  = "NEEDS_ATTENTION"
	applyStatusWaitingExternal = "WAITING_EXTERNAL"
	applyStatusCancelled       = "CANCELLED"
)

type applyTerminalError struct {
	status string
	stage  string
	cause  error
}

func (e applyTerminalError) Error() string {
	detail := ""
	if e.cause != nil {
		detail = ": " + e.cause.Error()
	}
	if e.stage == "" {
		return "apply 终态=" + e.status + detail
	}
	return fmt.Sprintf("apply 终态=%s stage=%s%s", e.status, e.stage, detail)
}

type applyStageRunner interface {
	Run(context.Context, string, []PlanChange, commandOptions, *Evidence, string, io.Writer) error
}

var newApplyStageRunner = func() applyStageRunner { return defaultApplyStageRunner{} }

type defaultApplyStageRunner struct{}

func (defaultApplyStageRunner) Run(ctx context.Context, stage string, changes []PlanChange, opts commandOptions, evidence *Evidence, evidencePath string, stdout io.Writer) error {
	if len(changes) == 0 {
		return nil
	}
	if stage == applyStageUploadReadback {
		markStageInProgress(evidence, changes, stage)
		if err := writeJSON(evidencePath, evidence); err != nil {
			return err
		}
		return runApplyUploadReadback(ctx, changes, opts, evidence, evidencePath, stdout)
	}
	storeIDs := planStoreIDs(Plan{Changes: changes})
	stores, knownStoreIDs, err := storeResolver(opts)
	if err != nil {
		return err
	}
	if err := validateRemoteStoreTargets(storeIDs, stores, knownStoreIDs); err != nil {
		return err
	}
	if err := loadEnvForRemoteCommand(opts); err != nil {
		return err
	}
	client := newShopifyClient(nil)
	switch stage {
	case applyStageUpload:
		duplicatePolicy, err := normalizeDuplicatePolicy(opts.duplicatePolicy)
		if err != nil {
			return err
		}
		return executeUploadChanges(ctx, stdout, opts, changes, stores, evidence, evidencePath, duplicatePolicy, client)
	case applyStageAlt, applyStageAltReadback:
		return executeAltChanges(ctx, stdout, opts, changes, stores, evidence, evidencePath, client)
	case applyStageVerify:
		jsonReceipt, err := loadJSONReplacementReceipt(opts, opts.planPath)
		if err != nil {
			return err
		}
		return executeVerifyChanges(ctx, stdout, opts, changes, stores, evidence, evidencePath, jsonReceipt, client)
	default:
		return fmt.Errorf("未知 apply stage: %s", stage)
	}
}

func runApply(ctx context.Context, stdout io.Writer, opts commandOptions) error {
	lease, err := acquireExecutionLease(opts)
	if err != nil {
		return err
	}
	defer lease.release()

	started := time.Now().UTC()
	plan, planPath, evidence, evidencePath, err := loadExecutionState(opts)
	if err != nil {
		return err
	}
	planResource, err := inspectResource(planPath)
	if err != nil {
		return err
	}
	summaryPath := opts.summaryPath
	if summaryPath == "" {
		summaryPath = filepath.Join(filepath.Dir(planPath), "apply-summary.json")
	}
	attemptsPath := filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl")
	attemptID := plan.RunID + "-apply-" + started.Format("20060102T150405.000000000Z")
	summary := ApplySummary{
		Version:      1,
		AttemptID:    attemptID,
		RunID:        plan.RunID,
		PlanPath:     planPath,
		PlanSHA256:   planResource.SHA256,
		EvidencePath: evidencePath,
		SummaryPath:  summaryPath,
		AttemptsPath: attemptsPath,
		DryRun:       !opts.execute,
		StartedAt:    started.Format(time.RFC3339Nano),
		Artifacts:    []string{planPath, evidencePath, summaryPath, attemptsPath},
	}
	opts.summaryPath = summaryPath
	var selectedStoresForAttempt []string
	finish := func(status, stage, next string, cause error) error {
		finished := time.Now().UTC()
		summary.FinalStatus = status
		summary.CurrentStage = stage
		summary.NextSafeAction = next
		summary.NeedsAttention = status == applyStatusNeedsAttention || status == applyStatusPartialFailure || status == applyStatusFailed
		summary.NeedsExternal = status == applyStatusWaitingExternal
		summary.UpdatedAt = finished.Format(time.RFC3339Nano)
		summary.DurationMS = finished.Sub(started).Milliseconds()
		if cause != nil && len(summary.Errors) == 0 {
			summary.Errors = append(summary.Errors, ApplyRowError{Stage: stage, Message: cause.Error()})
			if status == applyStatusFailed || status == applyStatusPartialFailure || status == applyStatusNeedsAttention {
				summary.Counts.Errors = summary.Counts.Total - summary.Counts.Success
				summary.Counts.Skipped = 0
			}
		}
		if summary.Errors == nil {
			summary.Errors = []ApplyRowError{}
		}
		if err := writeJSON(summaryPath, summary); err != nil {
			return errors.Join(cause, err)
		}
		mode := "preview"
		if opts.execute {
			mode = "execute"
		}
		attempt := ApplyAttempt{
			Version: 1, AttemptID: summary.AttemptID, RunID: summary.RunID, Mode: mode,
			PlanPath: planPath, PlanSHA256: summary.PlanSHA256, EvidencePath: evidencePath,
			SummaryPath: summaryPath, AttemptsPath: attemptsPath, RequestedStores: opts.stores,
			SelectedStores: append([]string(nil), selectedStoresForAttempt...), StoresConfigPath: opts.storesConfig,
			FinalStatus: status, CurrentStage: stage, FailedStage: summary.FailedStage,
			Counts: summary.Counts, NeedsAttention: summary.NeedsAttention, NeedsExternal: summary.NeedsExternal,
			NextSafeAction: next, Errors: append([]ApplyRowError(nil), summary.Errors...),
			StartedAt: summary.StartedAt, FinishedAt: summary.UpdatedAt, DurationMS: summary.DurationMS,
		}
		if err := appendJSONLine(attemptsPath, attempt); err != nil {
			return errors.Join(cause, fmt.Errorf("追加 apply attempt history 失败: %w", err))
		}
		if opts.format == "json" {
			if err := json.NewEncoder(stdout).Encode(summary); err != nil {
				return errors.Join(cause, err)
			}
		} else {
			fmt.Fprintf(stdout, "run_id=%s\nplan=%s\nevidence=%s\nsummary=%s\nfinal_status=%s\nnext_safe_action=%s\n", summary.RunID, planPath, evidencePath, summaryPath, status, next)
		}
		if status == applyStatusSucceeded || status == applyStatusPreviewReady {
			return nil
		}
		return applyTerminalError{status: status, stage: stage, cause: cause}
	}

	selectedStores, selectedStoreSet, err := resolvePlanStoreScope(plan, opts)
	selectedStoresForAttempt = append([]string(nil), selectedStores...)
	if err != nil {
		return finish(applyStatusNeedsAttention, "load", "修正 store scope 后重新运行同一 plan", err)
	}
	changes := changesForStores(plan, selectedStoreSet)
	summary.Counts.Total = len(changes)
	if evidence.RunID != "" && evidence.RunID != plan.RunID {
		return finish(applyStatusNeedsAttention, "load", "使用与 plan run_id 匹配的 evidence", fmt.Errorf("evidence run_id=%s 与 plan run_id=%s 不一致", evidence.RunID, plan.RunID))
	}
	if opts.execute && evidence.PlanSHA256 != "" && evidence.PlanSHA256 != planResource.SHA256 {
		return finish(applyStatusNeedsAttention, "load", "重新检查已变化的 plan；确认后用 preview 绑定新的 plan SHA", fmt.Errorf("plan SHA 漂移: approved=%s actual=%s", evidence.PlanSHA256, planResource.SHA256))
	}
	if err := validateApplyResources(changes); err != nil {
		return finish(applyStatusNeedsAttention, "resource_validation", "重新生成 plan；不要让旧 plan 自动采用新资源", err)
	}
	if err := ctx.Err(); err != nil {
		return finish(applyStatusCancelled, "load", "确认取消原因后从同一 plan/evidence 恢复", err)
	}
	currentPreview, err := buildApplyPreviewBinding(planResource.SHA256, selectedStores, opts.storesConfig)
	if err != nil {
		return finish(applyStatusNeedsAttention, "preview", "修复 stores config 后重新运行 preview", err)
	}
	if !opts.execute {
		evidence.PlanSHA256 = planResource.SHA256
		currentPreview.PreviewedAt = nowUTC()
		evidence.Preview = &currentPreview
		if err := writeJSON(evidencePath, evidence); err != nil {
			return finish(applyStatusFailed, "preview", "修复 evidence 写入后重新 preview", err)
		}
		summary.CurrentStage = "preview"
		summary.NextSafeAction = "检查 plan 与 summary，获得明确授权后追加 --execute"
		return finish(applyStatusPreviewReady, "preview", summary.NextSafeAction, nil)
	}
	if err := validateApplyPreviewBinding(evidence.Preview, currentPreview); err != nil {
		return finish(applyStatusNeedsAttention, "preview", "使用完全相同的 plan、store scope 与 stores config 重新运行 preview", err)
	}
	evidence.PlanSHA256 = planResource.SHA256
	if countPlanErrors(plan, selectedStoreSet) > 0 {
		for _, change := range changes {
			if change.Status == "error" {
				summary.Errors = append(summary.Errors, applyErrorFromChange(change, "plan", change.Reason))
			}
		}
		summary.Counts.Errors = len(summary.Errors)
		summary.Counts.Skipped = summary.Counts.Total - summary.Counts.Errors
		return finish(applyStatusFailed, "plan", "修复规划错误并重新生成 plan", errors.New("plan 包含规划阶段错误"))
	}

	normalizeLegacyEvidence(plan, changes, &evidence)
	if err := writeJSON(evidencePath, evidence); err != nil {
		return finish(applyStatusFailed, "load", "修复 evidence 写入错误后重试", err)
	}
	if attention := uploadAttentionErrors(changes, evidence); len(attention) > 0 {
		summary.Errors = attention
		summary.Counts.Errors = len(attention)
		return finish(applyStatusNeedsAttention, applyStageUpload, "检查含糊的旧 evidence/远端状态，确认后生成明确阶段状态再恢复", fmt.Errorf("upload evidence 需要人工确认: %s", attention[0].Message))
	}

	runner := newApplyStageRunner()
	runStage := func(stage string, stageChanges []PlanChange) error {
		if len(stageChanges) == 0 {
			return nil
		}
		summary.CurrentStage = stage
		summary.FinalStatus = applyStatusRunning
		summary.NextSafeAction = "等待当前阶段完成；持续观察 summary 与 evidence"
		summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := writeJSON(summaryPath, summary); err != nil {
			return err
		}
		stageOutput := stdout
		if opts.format == "json" {
			stageOutput = io.Discard
		}
		return runner.Run(ctx, stage, stageChanges, opts, &evidence, evidencePath, stageOutput)
	}

	readbackChanges := filterUploadReadbackChanges(changes, evidence)
	if err := runStage(applyStageUploadReadback, readbackChanges); err != nil {
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageUpload
		if errors.Is(err, context.Canceled) {
			return finish(applyStatusCancelled, applyStageUpload, "确认取消原因后从同一 plan/evidence 恢复", err)
		}
		return finish(applyStatusPartialFailure, applyStageUpload, "读取 evidence 后只重试可安全恢复的 upload readback", err)
	}
	uploadChanges := filterPendingUploadChanges(changes, evidence)
	if err := runStage(applyStageUpload, uploadChanges); err != nil {
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageUpload
		if errors.Is(err, context.Canceled) {
			return finish(applyStatusCancelled, applyStageUpload, "确认取消原因后从同一 plan/evidence 恢复", err)
		}
		return finish(applyStatusPartialFailure, applyStageUpload, "根据 row upload 状态重试；不要盲目重跑已成功行", err)
	}
	if errs := stageErrors(changes, evidence, applyStageUpload); len(errs) > 0 {
		summary.Errors = errs
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageUpload
		return finish(applyStatusPartialFailure, applyStageUpload, "根据 row upload 状态重试；不要进入 alt", errors.New("upload 阶段未安全完成"))
	}

	if attention := altAttentionErrors(changes, evidence); len(attention) > 0 {
		summary.Errors = attention
		summary.Counts.Errors = len(attention)
		return finish(applyStatusNeedsAttention, applyStageAlt, "检查含糊的 alt/translation mutation 状态，确认后再恢复", fmt.Errorf("alt evidence 需要人工确认: %s", attention[0].Message))
	}
	altRecoveryChanges := filterAltRecoveryChanges(changes, evidence)
	if err := runStage(applyStageAltReadback, altRecoveryChanges); err != nil {
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageAlt
		if errors.Is(err, context.Canceled) {
			return finish(applyStatusCancelled, applyStageAlt, "确认取消原因后从同一 plan/evidence 恢复", err)
		}
		return finish(applyStatusPartialFailure, applyStageAlt, "根据 alt checkpoint 只恢复未完成的 readback/translation 子阶段", err)
	}
	altChanges := filterPendingAltChanges(changes, evidence)
	if err := runStage(applyStageAlt, altChanges); err != nil {
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageAlt
		if errors.Is(err, context.Canceled) {
			return finish(applyStatusCancelled, applyStageAlt, "确认取消原因后从同一 plan/evidence 恢复", err)
		}
		return finish(applyStatusPartialFailure, applyStageAlt, "修复失败的 alt/translation 行后从同一 plan/evidence 恢复", err)
	}
	if errs := stageErrors(changes, evidence, applyStageAlt); len(errs) > 0 {
		summary.Errors = errs
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageAlt
		return finish(applyStatusPartialFailure, applyStageAlt, "修复失败的 alt/translation 行后再进入 verify", errors.New("alt 阶段失败"))
	}

	jsonReceipt, receiptErr := loadJSONReplacementReceipt(opts, planPath)
	if receiptErr != nil {
		populateApplySummary(&summary, changes, evidence)
		return finish(applyStatusNeedsAttention, "external_json", "修复或重新生成 mapping-level JSON receipt 后从同一 plan/evidence 恢复", receiptErr)
	}
	if hasExternalJSONActions(changes) && !jsonReceiptConfirmsAllChanges(jsonReceipt, changes) {
		populateApplySummary(&summary, changes, evidence)
		return finish(applyStatusWaitingExternal, "external_json", "完成受限 theme JSON 替换并提供逐 mapping readback receipt 后，再用同一 plan/evidence 继续 verify", errors.New("等待外部 JSON 处理"))
	}
	if jsonReceipt != nil {
		summary.Artifacts = append(summary.Artifacts, resolveJSONReplacementReceiptPath(opts, planPath))
	}

	verifyChanges := filterPendingVerifyChanges(changes, evidence)
	if err := runStage(applyStageVerify, verifyChanges); err != nil {
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageVerify
		if errors.Is(err, context.Canceled) {
			return finish(applyStatusCancelled, applyStageVerify, "确认取消原因后从同一 plan/evidence 恢复", err)
		}
		return finish(applyStatusPartialFailure, applyStageVerify, "读取 row verify evidence，修复失败项后从同一 plan 恢复", err)
	}
	if errs := stageErrors(changes, evidence, applyStageVerify); len(errs) > 0 {
		summary.Errors = errs
		populateApplySummary(&summary, changes, evidence)
		summary.FailedStage = applyStageVerify
		return finish(applyStatusPartialFailure, applyStageVerify, "修复 verify readback 后从同一 plan/evidence 恢复", errors.New("verify 阶段失败"))
	}
	populateApplySummary(&summary, changes, evidence)
	return finish(applyStatusSucceeded, applyStageVerify, "保留 plan、evidence 与 summary 作为本次执行 authority", nil)
}

func buildApplyPreviewBinding(planSHA string, selectedStores []string, storesConfigPath string) (ApplyPreviewBinding, error) {
	stores := append([]string(nil), selectedStores...)
	sort.Strings(stores)
	binding := ApplyPreviewBinding{
		Version:        1,
		PlanSHA256:     planSHA,
		SelectedStores: stores,
	}
	if storesConfigPath == "" {
		return binding, nil
	}
	absolutePath, err := filepath.Abs(storesConfigPath)
	if err != nil {
		return ApplyPreviewBinding{}, fmt.Errorf("解析 stores config path 失败: %w", err)
	}
	resource, err := inspectResource(absolutePath)
	if err != nil {
		return ApplyPreviewBinding{}, fmt.Errorf("读取 stores config identity 失败: %w", err)
	}
	binding.StoresConfigPath = filepath.Clean(absolutePath)
	binding.StoresConfigSHA256 = resource.SHA256
	return binding, nil
}

func validateApplyPreviewBinding(previewed *ApplyPreviewBinding, current ApplyPreviewBinding) error {
	if previewed == nil || previewed.Version != 1 {
		return errors.New("缺少 apply preview scope binding")
	}
	if previewed.PlanSHA256 != current.PlanSHA256 {
		return fmt.Errorf("preview plan SHA 漂移: previewed=%s current=%s", previewed.PlanSHA256, current.PlanSHA256)
	}
	if !sameStringSlice(previewed.SelectedStores, current.SelectedStores) {
		return fmt.Errorf("preview store scope 漂移: previewed=%s current=%s", strings.Join(previewed.SelectedStores, ","), strings.Join(current.SelectedStores, ","))
	}
	if filepath.Clean(previewed.StoresConfigPath) != filepath.Clean(current.StoresConfigPath) || previewed.StoresConfigSHA256 != current.StoresConfigSHA256 {
		return fmt.Errorf("preview stores config 漂移: previewed_path=%s current_path=%s previewed_sha=%s current_sha=%s", previewed.StoresConfigPath, current.StoresConfigPath, previewed.StoresConfigSHA256, current.StoresConfigSHA256)
	}
	return nil
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateApplyResources(changes []PlanChange) error {
	var problems []error
	for _, change := range changes {
		if !changeHasFileWriteAction(change) {
			continue
		}
		if change.Resource == nil || strings.TrimSpace(change.Resource.Path) == "" || strings.TrimSpace(change.Resource.SHA256) == "" {
			problems = append(problems, fmt.Errorf("%s row=%s 缺少冻结的本地资源 path/SHA", change.Store, change.RowNo))
			continue
		}
		actual, err := inspectResource(change.Resource.Path)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s row=%s 读取 plan 资源失败: %w", change.Store, change.RowNo, err))
			continue
		}
		if actual.SHA256 != change.Resource.SHA256 {
			problems = append(problems, fmt.Errorf("%s row=%s 资源 SHA 漂移: plan=%s actual=%s", change.Store, change.RowNo, change.Resource.SHA256, actual.SHA256))
		}
	}
	return errors.Join(problems...)
}

func normalizeLegacyEvidence(plan Plan, changes []PlanChange, evidence *Evidence) {
	if evidence.RunID == "" {
		evidence.RunID = plan.RunID
	}
	for _, change := range changes {
		record := evidenceRecordFromChange(change)
		if existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename}); existing != nil {
			record = mergeEvidenceRecord(*existing, record)
		}
		if changeHasFileWriteAction(change) && record.Upload.Status == "" {
			record.Upload = legacyUploadStage(change, record)
		}
		if !changeHasFileWriteAction(change) && record.Upload.Status == "" {
			record.Upload = StageEvidence{Status: stageStatusSkipped, UpdatedAt: nowUTC()}
		}
		if !changeHasAltAction(change) && record.Alt.Status == "" {
			record.Alt = StageEvidence{Status: stageStatusSkipped, UpdatedAt: nowUTC()}
		}
		evidence.Upsert(record)
	}
}

func legacyUploadStage(change PlanChange, record EvidenceRecord) StageEvidence {
	now := nowUTC()
	legacy := strings.ToUpper(strings.TrimSpace(record.Status))
	id := firstNonEmpty(record.MediaGID, record.FileID)
	shaMatches := change.Resource != nil && change.Resource.SHA256 != "" && record.SHA256 == change.Resource.SHA256
	switch legacy {
	case "READY":
		if id != "" && shaMatches {
			return StageEvidence{Status: stageStatusSucceeded, MutationAccepted: true, Retryable: true, UpdatedAt: now}
		}
		return StageEvidence{Status: stageStatusFailed, MutationAccepted: id != "", Retryable: false, LastError: "legacy READY 缺少匹配的 media id/SHA，不能判定 upload 成功", UpdatedAt: now}
	case "ERROR":
		if id != "" {
			return StageEvidence{Status: stageStatusFailed, MutationAccepted: true, Retryable: false, LastError: "legacy ERROR 含远端 id，mutation 状态含糊，拒绝自动重传: " + record.LastError, UpdatedAt: now}
		}
		return StageEvidence{Status: stageStatusFailed, Retryable: true, LastError: record.LastError, UpdatedAt: now}
	case "IN_PROGRESS":
		return StageEvidence{Status: stageStatusFailed, MutationAccepted: id != "", Retryable: false, LastError: "legacy IN_PROGRESS 无法证明 mutation 是否发生，需人工确认", UpdatedAt: now}
	default:
		return StageEvidence{Status: stageStatusNotStarted, Retryable: true, UpdatedAt: now}
	}
}

func filterPendingUploadChanges(changes []PlanChange, evidence Evidence) []PlanChange {
	var out []PlanChange
	for _, change := range changes {
		if !changeHasFileWriteAction(change) {
			continue
		}
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil || record.Upload.Status == stageStatusNotStarted || (record.Upload.Status == stageStatusFailed && record.Upload.Retryable && !record.Upload.MutationAccepted) {
			out = append(out, change)
		}
	}
	return out
}

func filterUploadReadbackChanges(changes []PlanChange, evidence Evidence) []PlanChange {
	var out []PlanChange
	for _, change := range changes {
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record != nil && record.Upload.Status == stageStatusAwaitingReadback && record.Upload.MutationAccepted && firstNonEmpty(record.MediaGID, record.FileID) != "" {
			out = append(out, change)
		}
	}
	return out
}

func filterPendingAltChanges(changes []PlanChange, evidence Evidence) []PlanChange {
	var out []PlanChange
	for _, change := range changes {
		if !changeHasAltAction(change) {
			continue
		}
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil || record.Alt.Status == "" || record.Alt.Status == stageStatusNotStarted || (record.Alt.Status == stageStatusFailed && record.Alt.Retryable && record.Alt.Checkpoint == "") {
			out = append(out, change)
		}
	}
	return out
}

func filterAltRecoveryChanges(changes []PlanChange, evidence Evidence) []PlanChange {
	var out []PlanChange
	for _, change := range changes {
		if !changeHasAltAction(change) {
			continue
		}
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record != nil && (record.Alt.Status == stageStatusAwaitingReadback || (record.Alt.Status == stageStatusFailed && record.Alt.Retryable && record.Alt.Checkpoint == "translation_pending")) {
			out = append(out, change)
		}
	}
	return out
}

func altAttentionErrors(changes []PlanChange, evidence Evidence) []ApplyRowError {
	var out []ApplyRowError
	for _, change := range changes {
		if !changeHasAltAction(change) {
			continue
		}
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil {
			continue
		}
		state := record.Alt
		if state.Status == stageStatusInProgress || (state.Status == stageStatusFailed && !state.Retryable) {
			out = append(out, applyErrorFromChange(change, applyStageAlt, firstNonEmpty(state.LastError, "alt/translation mutation 状态含糊")))
		}
		if state.Status == stageStatusAwaitingReadback && (firstNonEmpty(record.MediaGID, record.FileID) == "" || (state.Checkpoint != "alt_file_readback" && state.Checkpoint != "translation_readback")) {
			out = append(out, applyErrorFromChange(change, applyStageAlt, "alt/translation readback checkpoint 不完整"))
		}
	}
	return out
}

func filterPendingVerifyChanges(changes []PlanChange, evidence Evidence) []PlanChange {
	var out []PlanChange
	for _, change := range changes {
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil || record.Verify.Status != stageStatusSucceeded {
			out = append(out, change)
		}
	}
	return out
}

func uploadAttentionErrors(changes []PlanChange, evidence Evidence) []ApplyRowError {
	var out []ApplyRowError
	for _, change := range changes {
		if !changeHasFileWriteAction(change) {
			continue
		}
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil {
			continue
		}
		if record.Upload.Status == stageStatusFailed && !record.Upload.Retryable {
			out = append(out, applyErrorFromChange(change, applyStageUpload, record.Upload.LastError))
		}
		if (record.Upload.Status == stageStatusSucceeded || record.Upload.Status == stageStatusAwaitingReadback) && change.Resource != nil && record.SHA256 != change.Resource.SHA256 {
			out = append(out, applyErrorFromChange(change, applyStageUpload, fmt.Sprintf("upload evidence SHA 与 plan 不一致: plan=%s evidence=%s", change.Resource.SHA256, record.SHA256)))
		}
		if record.Upload.Status == stageStatusInProgress {
			out = append(out, applyErrorFromChange(change, applyStageUpload, "上次执行停在 IN_PROGRESS，无法证明 mutation 是否发生"))
		}
		if record.Upload.Status == stageStatusAwaitingReadback && firstNonEmpty(record.MediaGID, record.FileID) == "" {
			out = append(out, applyErrorFromChange(change, applyStageUpload, "等待 readback 但缺少 media id"))
		}
	}
	return out
}

func stageErrors(changes []PlanChange, evidence Evidence, stage string) []ApplyRowError {
	var out []ApplyRowError
	for _, change := range changes {
		if stage == applyStageUpload && !changeHasFileWriteAction(change) || stage == applyStageAlt && !changeHasAltAction(change) {
			continue
		}
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil {
			out = append(out, applyErrorFromChange(change, stage, "缺少 row evidence"))
			continue
		}
		state := rowStage(*record, stage)
		if state.Status == stageStatusFailed || state.Status == stageStatusAwaitingReadback {
			out = append(out, applyErrorFromChange(change, stage, state.LastError))
		}
	}
	return out
}

func populateApplySummary(summary *ApplySummary, changes []PlanChange, evidence Evidence) {
	summary.Counts = ApplyCounts{Total: len(changes)}
	summary.Errors = nil
	for _, change := range changes {
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil {
			summary.Counts.Errors++
			summary.Errors = append(summary.Errors, applyErrorFromChange(change, summary.CurrentStage, "缺少 row evidence"))
			continue
		}
		rowErrors := collectRowErrors(change, *record)
		if len(rowErrors) > 0 {
			summary.Counts.Errors++
			summary.Errors = append(summary.Errors, rowErrors...)
			continue
		}
		if len(change.Actions) == 0 {
			summary.Counts.Skipped++
		} else if applyRowCompleted(change, *record) {
			summary.Counts.Success++
		} else {
			summary.Counts.Skipped++
		}
	}
	if len(summary.Errors) > 1 {
		sort.SliceStable(summary.Errors, func(i, j int) bool {
			if summary.Errors[i].Store != summary.Errors[j].Store {
				return summary.Errors[i].Store < summary.Errors[j].Store
			}
			return summary.Errors[i].RowNo < summary.Errors[j].RowNo
		})
	}
}

func applyRowCompleted(change PlanChange, record EvidenceRecord) bool {
	if changeHasFileWriteAction(change) && record.Upload.Status != stageStatusSucceeded {
		return false
	}
	if changeHasAltAction(change) && record.Alt.Status != stageStatusSucceeded {
		return false
	}
	return record.Verify.Status == stageStatusSucceeded
}

func collectRowErrors(change PlanChange, record EvidenceRecord) []ApplyRowError {
	var out []ApplyRowError
	for _, item := range []struct {
		stage string
		state StageEvidence
	}{
		{applyStageUpload, record.Upload},
		{applyStageAlt, record.Alt},
		{applyStageVerify, record.Verify},
	} {
		if item.state.Status == stageStatusFailed || item.state.Status == stageStatusAwaitingReadback {
			out = append(out, applyErrorFromChange(change, item.stage, item.state.LastError))
		}
	}
	return out
}

func applyErrorFromChange(change PlanChange, stage, message string) ApplyRowError {
	return ApplyRowError{Store: change.Store, RowNo: change.RowNo, SourceFilename: change.SourceFilename, TargetFilename: change.TargetFilename, Stage: stage, Message: message}
}

func changeHasAltAction(change PlanChange) bool {
	return containsAction(change.Actions, "alt_update") || containsAction(change.Actions, "translation_update")
}

func hasExternalJSONActions(changes []PlanChange) bool {
	for _, change := range changes {
		if containsAction(change.Actions, "json_replace_needed") {
			return true
		}
	}
	return false
}

func jsonReceiptConfirmsAllChanges(receipt *jsonReplacementReceipt, changes []PlanChange) bool {
	for _, change := range changes {
		if containsAction(change.Actions, "json_replace_needed") && !jsonReceiptConfirmsChange(receipt, change) {
			return false
		}
	}
	return true
}

func setRowStage(record *EvidenceRecord, stage string, state StageEvidence) {
	if state.UpdatedAt == "" {
		state.UpdatedAt = nowUTC()
	}
	switch stage {
	case applyStageUpload, applyStageUploadReadback:
		record.Upload = state
		record.CurrentStage = applyStageUpload
	case applyStageAlt, applyStageAltReadback:
		record.Alt = state
		record.CurrentStage = applyStageAlt
	case applyStageVerify:
		record.Verify = state
		record.CurrentStage = applyStageVerify
	}
	record.UpdatedAt = state.UpdatedAt
	if state.Status == stageStatusFailed || state.Status == stageStatusAwaitingReadback {
		record.Status = "ERROR"
		record.LastError = state.LastError
	} else if state.Status == stageStatusSucceeded {
		record.Status = "READY"
		record.LastError = ""
	}
}

func rowStage(record EvidenceRecord, stage string) StageEvidence {
	switch stage {
	case applyStageUpload, applyStageUploadReadback:
		return record.Upload
	case applyStageAlt, applyStageAltReadback:
		return record.Alt
	case applyStageVerify:
		return record.Verify
	default:
		return StageEvidence{}
	}
}

func markStageInProgress(evidence *Evidence, changes []PlanChange, stage string) {
	for _, change := range changes {
		record := evidenceRecordFromChange(change)
		if existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename}); existing != nil {
			record = *existing
		}
		state := rowStage(record, stage)
		state.Status = stageStatusInProgress
		state.Attempts++
		state.LastError = ""
		state.UpdatedAt = nowUTC()
		setRowStage(&record, stage, state)
		evidence.Upsert(record)
	}
}

func runApplyUploadReadback(ctx context.Context, changes []PlanChange, opts commandOptions, evidence *Evidence, evidencePath string, stdout io.Writer) error {
	stores, knownStoreIDs, err := storeResolver(opts)
	if err != nil {
		return err
	}
	storeIDs := planStoreIDs(Plan{Changes: changes})
	if err := validateRemoteStoreTargets(storeIDs, stores, knownStoreIDs); err != nil {
		return err
	}
	if err := loadEnvForRemoteCommand(opts); err != nil {
		return err
	}
	client := newShopifyClient(nil)
	var failures []error
	for _, change := range changes {
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil {
			failures = append(failures, fmt.Errorf("%s row=%s 缺少 readback evidence", change.Store, change.RowNo))
			continue
		}
		state := record.Upload
		node, readErr := pollChangeReady(ctx, client, stores(change.Store), change, firstNonEmpty(record.MediaGID, record.FileID), opts.maxAttempts, opts.pollInterval)
		if readErr != nil {
			state.Status = stageStatusAwaitingReadback
			state.MutationAccepted = true
			state.Retryable = true
			state.LastError = readErr.Error()
			state.UpdatedAt = nowUTC()
			setRowStage(record, applyStageUpload, state)
			failures = append(failures, readErr)
		} else {
			merged := mergeEvidenceRecord(*record, evidenceRecordFromFile(change, resourceOrZero(change), node))
			*record = merged
			state.Status = stageStatusSucceeded
			state.MutationAccepted = true
			state.Retryable = true
			state.LastError = ""
			state.UpdatedAt = nowUTC()
			setRowStage(record, applyStageUpload, state)
			fmt.Fprintf(stdout, "%s row=%s upload readback READY\n", change.Store, change.RowNo)
		}
		if err := writeJSON(evidencePath, evidence); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

func resourceOrZero(change PlanChange) ResourceInfo {
	if change.Resource == nil {
		return ResourceInfo{}
	}
	return *change.Resource
}
