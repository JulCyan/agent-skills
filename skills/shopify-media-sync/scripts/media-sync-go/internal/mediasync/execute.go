package mediasync

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func runUpload(ctx context.Context, stdout io.Writer, opts commandOptions) error {
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
	selectedStores, selectedStoreSet, err := resolvePlanStoreScope(plan, opts)
	if err != nil {
		return err
	}
	changes := changesWithAnyAction(plan, selectedStoreSet, "file_upload_same_filename", "file_replace_same_filename", "file_upload_new_filename")
	metricsPath := resolveMetricsPath(opts, evidencePath)
	metrics := newRunMetrics("upload", plan.RunID, started, selectedStores, len(changes), changes, opts.concurrency, !opts.execute)
	if !opts.execute {
		updateMetricsFromChanges(&metrics, changes)
		if err := writeMetricsFile(metricsPath, &metrics); err != nil {
			return err
		}
		if err := writeExecutionPreview(stdout, "upload", planPath, evidencePath, selectedStores, plan, changes); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
		return nil
	}
	if err := failOnPlanErrors(plan, selectedStoreSet); err != nil {
		updateMetricsFromChanges(&metrics, changes)
		metrics.Errors = countPlanErrors(plan, selectedStoreSet)
		if metricsErr := writeMetricsFile(metricsPath, &metrics); metricsErr != nil {
			return fmt.Errorf("%w; 写 metrics 失败: %v", err, metricsErr)
		}
		fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
		return err
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "没有需要上传/替换的图片。")
		updateMetricsFromChanges(&metrics, changes)
		if err := writeMetricsFile(metricsPath, &metrics); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
		return nil
	}
	duplicatePolicy, err := normalizeDuplicatePolicy(opts.duplicatePolicy)
	if err != nil {
		return err
	}
	stores, knownStoreIDs, err := storeResolver(opts)
	if err != nil {
		return err
	}
	if err := validateRemoteStoreTargets(selectedStores, stores, knownStoreIDs); err != nil {
		return err
	}
	if err := loadEnvForRemoteCommand(opts); err != nil {
		return err
	}
	client := newShopifyClient(nil)
	if err := writeTargetAPI(stdout, selectedStores, stores); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "target_stores=%s\n", strings.Join(selectedStores, ","))
	fmt.Fprintf(stdout, "data_scope=plan:%s file_changes:%d\n", planPath, len(changes))
	fmt.Fprintf(stdout, "concurrency=%d\n", opts.concurrency)
	fmt.Fprintln(stdout, "execute=true")
	uploadErr := executeUploadChanges(ctx, stdout, opts, changes, stores, &evidence, evidencePath, duplicatePolicy, client)
	updateMetricsFromEvidence(&metrics, evidence, changes)
	if err := writeMetricsFile(metricsPath, &metrics); err != nil {
		if uploadErr != nil {
			return fmt.Errorf("%w; 写 metrics 失败: %v", uploadErr, err)
		}
		return err
	}
	fmt.Fprintf(stdout, "evidence=%s\n", evidencePath)
	fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
	return uploadErr
}

type uploadJob struct {
	index  int
	change PlanChange
}

type uploadResult struct {
	index  int
	change PlanChange
	record EvidenceRecord
	err    error
}

var persistUploadEvidence = writeJSON

func executeUploadChanges(ctx context.Context, stdout io.Writer, opts commandOptions, changes []PlanChange, stores func(string) Store, evidence *Evidence, evidencePath string, duplicatePolicy string, client *ShopifyClient) error {
	executionEvidence := cloneEvidence(*evidence)
	for _, change := range changes {
		record := evidenceRecordFromChange(change)
		if existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename}); existing != nil {
			record = mergeEvidenceRecord(*existing, record)
		}
		state := record.Upload
		state.Status = stageStatusInProgress
		state.Attempts++
		state.MutationAccepted = false
		state.Retryable = true
		state.LastError = ""
		state.UpdatedAt = nowUTC()
		setRowStage(&record, applyStageUpload, state)
		record.Status = "IN_PROGRESS"
		evidence.Upsert(record)
	}
	if err := persistUploadEvidence(evidencePath, evidence); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}

	workerCount := opts.concurrency
	if workerCount > len(changes) {
		workerCount = len(changes)
	}
	jobs := make(chan uploadJob)
	results := make(chan uploadResult, len(changes))
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workerClient := client
		if worker > 0 {
			workerClient = newShopifyClient(nil)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			uploadWorker(workerCtx, jobs, results, stores, executionEvidence, duplicatePolicy, opts, workerClient)
		}()
	}
	submitted := 0
	cancelled := false
submitLoop:
	for index, change := range changes {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		select {
		case jobs <- uploadJob{index: index, change: change}:
			submitted++
		case <-ctx.Done():
			cancelled = true
			break submitLoop
		}
	}
	close(jobs)
	if cancelled {
		cancelWorkers()
	}

	var failures []uploadResult
	var persistErr error
	for range submitted {
		result := <-results
		evidence.Upsert(result.record)
		if err := persistUploadEvidence(evidencePath, evidence); err != nil {
			if persistErr == nil {
				persistErr = err
				cancelWorkers()
			}
		}
		if result.err != nil {
			failures = append(failures, result)
			fmt.Fprintf(stdout, "%s row=%s %s ERROR %s\n", result.change.Store, result.change.RowNo, result.change.TargetFilename, result.err.Error())
			continue
		}
		fmt.Fprintf(stdout, "%s row=%s %s READY %s\n", result.change.Store, result.change.RowNo, result.change.TargetFilename, result.record.CDNURL)
	}
	workers.Wait()
	if persistErr != nil {
		if finalPersistErr := persistUploadEvidence(evidencePath, evidence); finalPersistErr != nil {
			persistErr = fmt.Errorf("%w; workers 停止后的 evidence 补写仍失败: %v", persistErr, finalPersistErr)
		}
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), persistErr)
		}
		return persistErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(failures) > 0 {
		sort.Slice(failures, func(i, j int) bool { return failures[i].index < failures[j].index })
		return fmt.Errorf("upload 完成但 %d/%d 行失败，详见 evidence=%s；first_error=%s row=%s: %v", len(failures), len(changes), evidencePath, failures[0].change.Store, failures[0].change.RowNo, failures[0].err)
	}
	return nil
}

func uploadWorker(ctx context.Context, jobs <-chan uploadJob, results chan<- uploadResult, stores func(string) Store, evidence Evidence, duplicatePolicy string, opts commandOptions, client *ShopifyClient) {
	for job := range jobs {
		results <- executeUploadJob(ctx, job, stores, evidence, duplicatePolicy, opts, client)
	}
}

func executeUploadJob(ctx context.Context, job uploadJob, stores func(string) Store, evidence Evidence, duplicatePolicy string, opts commandOptions, client *ShopifyClient) uploadResult {
	change := job.change
	record := evidenceRecordFromChange(change)
	if existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename}); existing != nil {
		record = mergeEvidenceRecord(*existing, record)
	}
	state := record.Upload
	if state.Status != stageStatusInProgress {
		state.Attempts++
	}
	store := stores(change.Store)
	resource, err := resourceForChange(change)
	if err != nil {
		state.Status = stageStatusFailed
		state.MutationAccepted = false
		state.Retryable = true
		state.LastError = err.Error()
		state.UpdatedAt = nowUTC()
		setRowStage(&record, applyStageUpload, state)
		return uploadResult{index: job.index, change: change, record: record, err: err}
	}
	node, err := executeFileChange(ctx, client, store, change, resource, evidence, duplicatePolicy, opts)
	if err != nil {
		if node.ID != "" {
			record = mergeEvidenceRecord(record, evidenceRecordFromFile(change, resource, node))
		}
		state.Status = stageStatusFailed
		state.MutationAccepted = node.ID != ""
		state.Retryable = true
		var mutationErr mutationAttemptError
		if errors.As(err, &mutationErr) && !mutationErr.SafeRetry() {
			state.Retryable = false
		}
		if node.ID != "" {
			state.Status = stageStatusAwaitingReadback
		}
		state.LastError = err.Error()
		state.UpdatedAt = nowUTC()
		setRowStage(&record, applyStageUpload, state)
		return uploadResult{index: job.index, change: change, record: record, err: err}
	}
	record = evidenceRecordFromFile(change, resource, node)
	state.Status = stageStatusSucceeded
	state.MutationAccepted = true
	state.Retryable = true
	state.LastError = ""
	state.UpdatedAt = nowUTC()
	setRowStage(&record, applyStageUpload, state)
	return uploadResult{index: job.index, change: change, record: record}
}

func cloneEvidence(evidence Evidence) Evidence {
	cloned := evidence
	cloned.Results = append([]EvidenceRecord(nil), evidence.Results...)
	cloned.Rows = append([]EvidenceRecord(nil), evidence.Rows...)
	return cloned
}

func runAlt(ctx context.Context, stdout io.Writer, opts commandOptions) error {
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
	selectedStores, selectedStoreSet, err := resolvePlanStoreScope(plan, opts)
	if err != nil {
		return err
	}
	changes := changesWithAnyAction(plan, selectedStoreSet, "alt_update", "translation_update")
	metricsPath := resolveMetricsPath(opts, evidencePath)
	metrics := newRunMetrics("alt", plan.RunID, started, selectedStores, len(changes), changes, 1, !opts.execute)
	if !opts.execute {
		updateMetricsFromChanges(&metrics, changes)
		if err := writeMetricsFile(metricsPath, &metrics); err != nil {
			return err
		}
		if err := writeExecutionPreview(stdout, "alt", planPath, evidencePath, selectedStores, plan, changes); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
		return nil
	}
	if err := failOnPlanErrors(plan, selectedStoreSet); err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "没有需要更新的 alt / translations。")
		updateMetricsFromChanges(&metrics, changes)
		if err := writeMetricsFile(metricsPath, &metrics); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
		return nil
	}
	stores, knownStoreIDs, err := storeResolver(opts)
	if err != nil {
		return err
	}
	if err := validateRemoteStoreTargets(selectedStores, stores, knownStoreIDs); err != nil {
		return err
	}
	if err := loadEnvForRemoteCommand(opts); err != nil {
		return err
	}
	client := newShopifyClient(nil)
	if err := writeTargetAPI(stdout, selectedStores, stores); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "target_stores=%s\n", strings.Join(selectedStores, ","))
	fmt.Fprintf(stdout, "data_scope=plan:%s alt_changes:%d\n", planPath, len(changes))
	fmt.Fprintln(stdout, "execute=true")
	altErr := executeAltChanges(ctx, stdout, opts, changes, stores, &evidence, evidencePath, client)
	updateMetricsFromEvidence(&metrics, evidence, changes)
	if err := writeMetricsFile(metricsPath, &metrics); err != nil {
		return errors.Join(altErr, err)
	}
	fmt.Fprintf(stdout, "evidence=%s\n", evidencePath)
	fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
	return altErr
}

func executeAltChanges(ctx context.Context, stdout io.Writer, opts commandOptions, changes []PlanChange, stores func(string) Store, evidence *Evidence, evidencePath string, client *ShopifyClient) error {
	var failures []error
	persist := func(record EvidenceRecord) error {
		evidence.Upsert(record)
		return writeJSON(evidencePath, evidence)
	}
	recordFailure := func(record EvidenceRecord, state StageEvidence, err error) error {
		state.LastError = err.Error()
		state.UpdatedAt = nowUTC()
		setRowStage(&record, applyStageAlt, state)
		if writeErr := persist(record); writeErr != nil {
			return writeErr
		}
		failures = append(failures, err)
		return nil
	}
	for _, change := range changes {
		store := stores(change.Store)
		record := evidenceRecordFromChange(change)
		existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if existing != nil {
			record = mergeEvidenceRecord(*existing, record)
		}
		state := record.Alt
		resuming := state.Status == stageStatusAwaitingReadback || state.Checkpoint == "translation_pending"
		if !resuming {
			state.Status = stageStatusInProgress
			state.Attempts++
			state.MutationAccepted = false
			state.Retryable = true
			state.LastError = ""
			state.UpdatedAt = nowUTC()
			setRowStage(&record, applyStageAlt, state)
			if err := persist(record); err != nil {
				return err
			}
		}
		fileID := firstNonEmpty(record.MediaGID, record.FileID)
		if fileID == "" {
			node, err := findChangeFile(ctx, client, store, change)
			if err != nil {
				state.Status = stageStatusFailed
				state.Retryable = true
				state.MutationAccepted = false
				if writeErr := recordFailure(record, state, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			record.FileID = node.ID
			record.MediaGID = node.ID
			fileID = node.ID
		}
		if state.Status == stageStatusAwaitingReadback && state.Checkpoint == "translation_readback" {
			readback, err := client.VerifyAltTranslations(ctx, store, fileID, change.Translations)
			if err != nil {
				state.Status = stageStatusAwaitingReadback
				state.MutationAccepted = true
				state.Retryable = true
				if writeErr := recordFailure(record, state, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			record.TranslationReadback = readback
			state = markAltStageSucceeded(state)
			setRowStage(&record, applyStageAlt, state)
			if err := persist(record); err != nil {
				return err
			}
			continue
		}
		if containsAction(change.Actions, "alt_update") && !(state.Status == stageStatusAwaitingReadback && state.Checkpoint == "alt_file_readback") && state.Checkpoint != "translation_pending" {
			node, err := client.UpdateFileAlt(ctx, store, fileID, change.Alt)
			if err != nil {
				state.Status = stageStatusFailed
				state.MutationAccepted = false
				state.Retryable = mutationFailureRetryable(err)
				if writeErr := recordFailure(record, state, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			record.FileID = node.ID
			record.MediaGID = node.ID
			record.AltReadback = node.Alt
			record.FileStatus = node.FileStatus
			fileID = node.ID
			state.Status = stageStatusAwaitingReadback
			state.Checkpoint = "alt_file_readback"
			state.MutationAccepted = true
			state.Retryable = true
			state.LastError = ""
			state.UpdatedAt = nowUTC()
			setRowStage(&record, applyStageAlt, state)
			if err := persist(record); err != nil {
				return err
			}
		}
		if state.Checkpoint != "translation_pending" {
			node, err := pollChangeReady(ctx, client, store, change, fileID, opts.maxAttempts, opts.pollInterval)
			if err != nil {
				if state.MutationAccepted {
					state.Status = stageStatusAwaitingReadback
					state.Checkpoint = "alt_file_readback"
					state.Retryable = true
				} else {
					state.Status = stageStatusFailed
					state.Retryable = true
				}
				if writeErr := recordFailure(record, state, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			record = mergeEvidenceRecord(record, evidenceRecordFromFile(change, ResourceInfo{}, node))
			record.AltReadback = node.Alt
			if err := validateAltReadback(change, node.Alt); err != nil {
				state.Status = stageStatusFailed
				state.Retryable = false
				if writeErr := recordFailure(record, state, err); writeErr != nil {
					return writeErr
				}
				continue
			}
		}
		if containsAction(change.Actions, "translation_update") {
			state.Status = stageStatusInProgress
			state.Checkpoint = "translation_mutation"
			state.MutationAccepted = false
			state.Retryable = false
			state.UpdatedAt = nowUTC()
			setRowStage(&record, applyStageAlt, state)
			if err := persist(record); err != nil {
				return err
			}
			mutated, err := client.RegisterAltTranslationsMutation(ctx, store, fileID, change.Translations)
			if err != nil {
				state.Status = stageStatusFailed
				state.Checkpoint = "translation_pending"
				state.Retryable = true
				if !mutationFailureRetryable(err) {
					state.Checkpoint = "translation_mutation"
					state.Retryable = false
				}
				if writeErr := recordFailure(record, state, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			if mutated {
				state.Status = stageStatusAwaitingReadback
				state.Checkpoint = "translation_readback"
				state.MutationAccepted = true
				state.Retryable = true
				state.LastError = ""
				state.UpdatedAt = nowUTC()
				setRowStage(&record, applyStageAlt, state)
				if err := persist(record); err != nil {
					return err
				}
			}
			readback, err := client.VerifyAltTranslations(ctx, store, fileID, change.Translations)
			if err != nil {
				if mutated {
					state.Status = stageStatusAwaitingReadback
					state.Checkpoint = "translation_readback"
					state.MutationAccepted = true
					state.Retryable = true
				} else {
					state.Status = stageStatusFailed
					state.Checkpoint = "translation_pending"
					state.MutationAccepted = false
					state.Retryable = true
				}
				if writeErr := recordFailure(record, state, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			record.TranslationReadback = readback
		}
		state = markAltStageSucceeded(state)
		setRowStage(&record, applyStageAlt, state)
		if err := persist(record); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s row=%s %s alt_readback=%q translations=%d\n", change.Store, change.RowNo, change.TargetFilename, record.AltReadback, len(record.TranslationReadback))
	}
	if len(failures) > 0 {
		return fmt.Errorf("alt finished with %d error(s): %w", len(failures), errors.Join(failures...))
	}
	return nil
}

func markAltStageSucceeded(state StageEvidence) StageEvidence {
	state.Status = stageStatusSucceeded
	state.Checkpoint = ""
	state.MutationAccepted = true
	state.Retryable = true
	state.LastError = ""
	state.UpdatedAt = nowUTC()
	return state
}

func mutationFailureRetryable(err error) bool {
	var mutationErr mutationAttemptError
	if errors.As(err, &mutationErr) {
		return mutationErr.SafeRetry()
	}
	return true
}

func runVerify(ctx context.Context, stdout io.Writer, opts commandOptions) error {
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
	selectedStores, selectedStoreSet, err := resolvePlanStoreScope(plan, opts)
	if err != nil {
		return err
	}
	changes := changesForStores(plan, selectedStoreSet)
	jsonReceipt, err := loadJSONReplacementReceipt(opts, planPath)
	if err != nil {
		return err
	}
	metricsPath := resolveMetricsPath(opts, evidencePath)
	metrics := newRunMetrics("verify", plan.RunID, started, selectedStores, len(changes), changes, 1, false)
	if scopedErrors := countPlanErrors(plan, selectedStoreSet); scopedErrors > 0 {
		if evidenceErr := recordPlanErrorEvidence(evidencePath, &evidence, changes); evidenceErr != nil {
			return fmt.Errorf("plan 在所选 store 范围内含 %d 个预检错误，verify 未执行; 写 evidence 失败: %w", scopedErrors, evidenceErr)
		}
		updateMetricsFromChanges(&metrics, changes)
		metrics.Errors = scopedErrors
		if metricsErr := writeMetricsFile(metricsPath, &metrics); metricsErr != nil {
			return fmt.Errorf("plan 在所选 store 范围内含 %d 个预检错误，verify 未执行; 写 metrics 失败: %w", scopedErrors, metricsErr)
		}
		fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
		return fmt.Errorf("plan 在所选 store 范围内含 %d 个预检错误，verify 未执行", scopedErrors)
	}
	stores, knownStoreIDs, err := storeResolver(opts)
	if err != nil {
		return err
	}
	if err := validateRemoteStoreTargets(selectedStores, stores, knownStoreIDs); err != nil {
		return err
	}
	if err := loadEnvForRemoteCommand(opts); err != nil {
		return err
	}
	client := newShopifyClient(nil)
	if err := writeTargetAPI(stdout, selectedStores, stores); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "target_stores=%s\n", strings.Join(selectedStores, ","))
	fmt.Fprintf(stdout, "data_scope=plan:%s verify_rows:%d\n", planPath, len(changes))
	verifyErr := executeVerifyChanges(ctx, stdout, opts, changes, stores, &evidence, evidencePath, jsonReceipt, client)
	updateMetricsFromEvidence(&metrics, evidence, changes)
	if err := writeMetricsFile(metricsPath, &metrics); err != nil {
		return errors.Join(verifyErr, err)
	}
	fmt.Fprintf(stdout, "evidence=%s\n", evidencePath)
	fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
	if verifyErr != nil {
		return verifyErr
	}
	return verifyRunResult(metrics.Errors, len(changes), evidencePath)
}

func executeVerifyChanges(ctx context.Context, stdout io.Writer, opts commandOptions, changes []PlanChange, stores func(string) Store, evidence *Evidence, evidencePath string, jsonReceipt *jsonReplacementReceipt, client *ShopifyClient) error {
	for _, change := range changes {
		store := stores(change.Store)
		record := evidenceRecordFromChange(change)
		existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if existing != nil {
			record = mergeEvidenceRecord(*existing, record)
		}
		state := record.Verify
		state.Status = stageStatusInProgress
		state.Attempts++
		state.Retryable = true
		state.LastError = ""
		state.UpdatedAt = nowUTC()
		setRowStage(&record, applyStageVerify, state)
		evidence.Upsert(record)
		if err := writeJSON(evidencePath, evidence); err != nil {
			return err
		}
		jsonReplacementVerified := jsonReceiptConfirmsChange(jsonReceipt, change)
		if jsonReplacementVerified {
			clearResolvedJSONReplacementError(&record)
		}
		if err := validateFileWriteEvidenceForVerify(change, record); err != nil {
			state.Status = stageStatusFailed
			state.LastError = err.Error()
			state.UpdatedAt = nowUTC()
			setRowStage(&record, applyStageVerify, state)
			evidence.Upsert(record)
			_ = writeJSON(evidencePath, evidence)
			continue
		}
		fileID := firstNonEmpty(record.MediaGID, record.FileID)
		var node ShopifyFileNode
		var readErr error
		if fileID != "" {
			node, readErr = pollChangeReady(ctx, client, store, change, fileID, opts.maxAttempts, opts.pollInterval)
		} else {
			var found ShopifyFileNode
			found, readErr = findChangeFile(ctx, client, store, change)
			if readErr == nil {
				node, readErr = pollChangeReady(ctx, client, store, change, found.ID, opts.maxAttempts, opts.pollInterval)
			}
		}
		if readErr != nil {
			state.Status = stageStatusFailed
			state.LastError = readErr.Error()
			state.UpdatedAt = nowUTC()
			setRowStage(&record, applyStageVerify, state)
			evidence.Upsert(record)
			persistErr := writeJSON(evidencePath, evidence)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(ctxErr, persistErr)
			}
			if errors.Is(readErr, context.Canceled) {
				return errors.Join(readErr, persistErr)
			}
			if persistErr != nil {
				return persistErr
			}
			continue
		}
		record = mergeEvidenceRecord(record, evidenceRecordFromFile(change, ResourceInfo{}, node))
		record.AltReadback = node.Alt
		if err := validateAltReadback(change, node.Alt); err != nil {
			record.Status = "ERROR"
			record.LastError = err.Error()
		} else {
			markVerifyRecordReady(&record, change, jsonReplacementVerified)
		}
		var cancellationErr error
		if len(change.Translations) > 0 {
			readback, err := client.VerifyAltTranslations(ctx, store, node.ID, change.Translations)
			if err != nil {
				record.Status = "ERROR"
				record.LastError = appendErrorMessage(record.LastError, err.Error())
				cancellationErr = ctx.Err()
				if cancellationErr == nil && errors.Is(err, context.Canceled) {
					cancellationErr = err
				}
			} else {
				record.TranslationReadback = readback
			}
		}
		state = record.Verify
		if record.Status == "READY" && record.LastError == "" {
			state.Status = stageStatusSucceeded
			state.LastError = ""
		} else {
			state.Status = stageStatusFailed
			state.LastError = record.LastError
		}
		state.Retryable = true
		state.UpdatedAt = nowUTC()
		setRowStage(&record, applyStageVerify, state)
		evidence.Upsert(record)
		persistErr := writeJSON(evidencePath, evidence)
		if cancellationErr != nil {
			return errors.Join(cancellationErr, persistErr)
		}
		if persistErr != nil {
			return persistErr
		}
		fmt.Fprintf(stdout, "%s row=%s %s status=%s file=%s\n", change.Store, change.RowNo, change.TargetFilename, record.Status, node.FileStatus)
	}
	return nil
}

func runSyncStatus(_ context.Context, stdout io.Writer, opts commandOptions) error {
	plan, planPath, evidence, evidencePath, err := loadExecutionState(opts)
	if err != nil {
		return err
	}
	selectedStores, selectedStoreSet, err := resolvePlanStoreScope(plan, opts)
	if err != nil {
		return err
	}
	if opts.execute && (opts.sheetURL != "" || opts.spreadsheetToken != "") {
		return errors.New("sync-status 的飞书结果 sheet 回写尚未实现；当前只输出本地 report.csv")
	}
	reportPlan := plan
	reportPlan.Summary.Stores = selectedStores
	reportPlan.Changes = changesForStores(plan, selectedStoreSet)
	reportPath := filepath.Join(filepath.Dir(evidencePath), "status-report.csv")
	if err := writeStatusReport(reportPath, reportPlan, evidence); err != nil {
		return err
	}
	if opts.format == "json" {
		return json.NewEncoder(stdout).Encode(map[string]any{"plan": planPath, "evidence": evidencePath, "report": reportPath})
	}
	fmt.Fprintf(stdout, "plan=%s\n", planPath)
	fmt.Fprintf(stdout, "evidence=%s\n", evidencePath)
	fmt.Fprintf(stdout, "report=%s\n", reportPath)
	return nil
}

func verifyRunResult(errorCount, rowCount int, evidencePath string) error {
	if errorCount > 0 {
		return fmt.Errorf("verify failed: %d/%d rows failed; see evidence=%s", errorCount, rowCount, evidencePath)
	}
	return nil
}

func recordPlanErrorEvidence(evidencePath string, evidence *Evidence, changes []PlanChange) error {
	changed := false
	for _, change := range changes {
		if change.Status != "error" {
			continue
		}
		record := evidenceRecordFromChange(change)
		if existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename}); existing != nil {
			record = mergeEvidenceRecord(*existing, record)
		}
		record.Status = "ERROR"
		record.LastError = planErrorEvidenceMessage(change)
		record.UpdatedAt = nowUTC()
		evidence.Upsert(record)
		changed = true
	}
	if !changed {
		return nil
	}
	return writeJSON(evidencePath, evidence)
}

func planErrorEvidenceMessage(change PlanChange) string {
	message := fmt.Sprintf("%s row=%s plan 预检错误", change.Store, change.RowNo)
	if change.Reason != "" {
		message += ": " + change.Reason
	}
	return message
}

func validateFileWriteEvidenceForVerify(change PlanChange, record EvidenceRecord) error {
	if !changeHasFileWriteAction(change) {
		return nil
	}
	if !strings.EqualFold(record.Status, "READY") || record.LastError != "" {
		return fmt.Errorf("%s row=%s 缺少成功上传 evidence，不能验证文件写入结果", change.Store, change.RowNo)
	}
	if firstNonEmpty(record.MediaGID, record.FileID) == "" {
		return fmt.Errorf("%s row=%s 成功上传 evidence 缺少 media_gid/file_id", change.Store, change.RowNo)
	}
	if change.Resource != nil && change.Resource.SHA256 != "" && record.SHA256 != change.Resource.SHA256 {
		return fmt.Errorf("%s row=%s 成功上传 evidence sha256 mismatch: expected %s got %s", change.Store, change.RowNo, change.Resource.SHA256, record.SHA256)
	}
	return nil
}

func changeHasFileWriteAction(change PlanChange) bool {
	return containsAction(change.Actions, "file_upload_same_filename") ||
		containsAction(change.Actions, "file_replace_same_filename") ||
		containsAction(change.Actions, "file_upload_new_filename")
}

type jsonReplacementMappingResult struct {
	SourceFilename   string `json:"source_filename"`
	TargetFilename   string `json:"target_filename"`
	ReplacementCount int    `json:"replacement_count"`
	Matched          bool   `json:"matched"`
}

type jsonReplacementReceipt struct {
	Version     int    `json:"version"`
	Verified    bool   `json:"verified"`
	Template    string `json:"template"`
	TargetTheme string `json:"target_theme"`
	Results     []struct {
		Store string `json:"store"`
		OK    bool   `json:"ok"`
	} `json:"results"`
	MappingResults []jsonReplacementMappingResult `json:"mapping_results"`
}

func loadJSONReplacementReceipt(opts commandOptions, planPath string) (*jsonReplacementReceipt, error) {
	path := resolveJSONReplacementReceiptPath(opts, planPath)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 JSON 替换回执失败(%s): %w", path, err)
	}
	var receipt jsonReplacementReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, fmt.Errorf("解析 JSON 替换回执失败(%s): %w", path, err)
	}
	return &receipt, nil
}

func resolveJSONReplacementReceiptPath(opts commandOptions, planPath string) string {
	if opts.jsonReceipt != "" {
		return opts.jsonReceipt
	}
	return filepath.Join(filepath.Dir(planPath), "json-replacement-receipt.json")
}

func jsonReceiptConfirmsChange(receipt *jsonReplacementReceipt, change PlanChange) bool {
	if !containsAction(change.Actions, "json_replace_needed") {
		return false
	}
	// Version 1 only proves a store-level JSON readback. It does not prove this
	// source_filename -> target_filename mapping was replaced, so JSON changes
	// must fail closed until a v2 receipt is produced by an external theme JSON workflow.
	if receipt == nil || receipt.Version < 2 || !receipt.Verified || receipt.Template != change.Template {
		return false
	}
	if len(receipt.MappingResults) == 0 {
		return false
	}
	for _, mapping := range receipt.MappingResults {
		if !mapping.Matched || mapping.ReplacementCount <= 0 {
			return false
		}
	}
	if change.TargetTheme != "" && receipt.TargetTheme != change.TargetTheme {
		return false
	}
	for _, result := range receipt.Results {
		if result.Store == change.Store && result.OK {
			for _, mapping := range receipt.MappingResults {
				if mapping.SourceFilename == change.SourceFilename &&
					mapping.TargetFilename == change.TargetFilename &&
					mapping.Matched && mapping.ReplacementCount > 0 {
					return true
				}
			}
			return false
		}
	}
	return false
}

func clearResolvedJSONReplacementError(record *EvidenceRecord) {
	if strings.Contains(record.LastError, "pending json_replace_needed") ||
		strings.Contains(record.LastError, "缺少成功上传 evidence") {
		record.Status = "READY"
		record.LastError = ""
	}
}

func markVerifyRecordReady(record *EvidenceRecord, change PlanChange, jsonReplacementVerified bool) {
	if err := pendingJSONReplacementError(change); err != nil && !jsonReplacementVerified {
		record.Status = "ERROR"
		record.LastError = appendErrorMessage(record.LastError, err.Error())
		return
	}
	record.Status = "READY"
	record.LastError = ""
}

func pendingJSONReplacementError(change PlanChange) error {
	if !containsAction(change.Actions, "json_replace_needed") {
		return nil
	}
	message := fmt.Sprintf("%s row=%s pending json_replace_needed: json-replace is reserved/unimplemented", change.Store, change.RowNo)
	if change.Template != "" {
		message += "; template=" + change.Template
	}
	message += "; complete JSON replacement or explicit skip before final verify"
	return errors.New(message)
}

func executeFileChange(ctx context.Context, client *ShopifyClient, store Store, change PlanChange, resource ResourceInfo, evidence Evidence, duplicatePolicy string, opts commandOptions) (ShopifyFileNode, error) {
	isVideo := isVideoResource(resource)
	if isVideo && containsAction(change.Actions, "file_replace_same_filename") {
		return ShopifyFileNode{}, fmt.Errorf("%s row=%s VIDEO 同名替换尚无可验证的原子语义；请重新规划为新文件上传", change.Store, change.RowNo)
	}
	if isVideo {
		existing, err := client.FindVideoByFilename(ctx, store, change.TargetFilename)
		if err == nil {
			return ShopifyFileNode{}, newMutationAttemptError(fmt.Errorf(
				"%s row=%s 目标店已存在同名 VIDEO，但缺少 SHA 匹配的成功 evidence；拒绝复用或创建 UUID 后缀副本: filename=%s id=%s status=%s",
				change.Store, change.RowNo, change.TargetFilename, existing.ID, existing.FileStatus,
			), false)
		}
		if !errors.Is(err, errShopifyFileNotFound) {
			return ShopifyFileNode{}, err
		}
	}
	var replaceFileID string
	if containsAction(change.Actions, "file_replace_same_filename") {
		existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if existing == nil || firstNonEmpty(existing.MediaGID, existing.FileID) == "" {
			existing = change.Evidence
		}
		if existing == nil {
			return ShopifyFileNode{}, fmt.Errorf("%s row=%s 缺少同名替换所需 evidence", change.Store, change.RowNo)
		}
		replaceFileID = firstNonEmpty(existing.MediaGID, existing.FileID)
		if replaceFileID == "" {
			return ShopifyFileNode{}, fmt.Errorf("%s row=%s 缺少同名替换所需 media_gid/file_id", change.Store, change.RowNo)
		}
	}
	var target StagedTarget
	var err error
	if isVideo {
		target, err = client.CreateVideoStagedUpload(ctx, store, resource, change.TargetFilename)
	} else {
		target, err = client.CreateStagedUpload(ctx, store, resource, change.TargetFilename)
	}
	if err != nil {
		return ShopifyFileNode{}, err
	}
	if err := client.UploadToStagedTarget(ctx, target, resource); err != nil {
		return ShopifyFileNode{}, err
	}
	var node ShopifyFileNode
	if isVideo {
		node, err = client.CreateVideoFile(ctx, store, target.ResourceURL, change.TargetFilename)
	} else if replaceFileID != "" {
		node, err = client.UpdateFileOriginalSource(ctx, store, replaceFileID, target.ResourceURL)
	} else {
		node, err = client.CreateFile(ctx, store, target.ResourceURL, change.TargetFilename, duplicatePolicy)
	}
	if err != nil {
		return ShopifyFileNode{}, wrapMutationAttemptError(err)
	}
	readyNode, err := pollChangeReady(ctx, client, store, change, node.ID, opts.maxAttempts, opts.pollInterval)
	if err != nil {
		return node, err
	}
	return readyNode, nil
}

func isVideoResource(resource ResourceInfo) bool {
	return strings.EqualFold(strings.TrimSpace(resource.MimeType), "video/mp4") ||
		strings.EqualFold(filepath.Ext(resource.Filename), ".mp4") ||
		strings.EqualFold(filepath.Ext(resource.Path), ".mp4")
}

func isVideoChange(change PlanChange) bool {
	return isVideoResource(resourceOrZero(change)) || strings.EqualFold(filepath.Ext(change.TargetFilename), ".mp4")
}

func pollChangeReady(ctx context.Context, client *ShopifyClient, store Store, change PlanChange, fileID string, attempts int, interval time.Duration) (ShopifyFileNode, error) {
	if isVideoChange(change) {
		node, err := client.PollVideoReady(ctx, store, fileID, attempts, interval)
		if err != nil {
			return node, err
		}
		if err := validateVideoReadbackFilename(store, change.TargetFilename, node); err != nil {
			return node, err
		}
		return node, nil
	}
	return client.PollFileReady(ctx, store, fileID, attempts, interval)
}

func findChangeFile(ctx context.Context, client *ShopifyClient, store Store, change PlanChange) (ShopifyFileNode, error) {
	if isVideoChange(change) {
		return client.FindVideoByFilename(ctx, store, change.TargetFilename)
	}
	return client.FindFileByFilename(ctx, store, change.TargetFilename)
}

func loadExecutionState(opts commandOptions) (Plan, string, Evidence, string, error) {
	planPath, err := resolvePlanPath(opts)
	if err != nil {
		return Plan{}, "", Evidence{}, "", err
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return Plan{}, "", Evidence{}, "", err
	}
	var plan Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return Plan{}, "", Evidence{}, "", fmt.Errorf("解析 plan.json 失败: %w", err)
	}
	evidencePath := resolveEvidencePath(opts, planPath)
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Plan{}, "", Evidence{}, "", err
		}
		evidence = Evidence{RunID: plan.RunID, Results: []EvidenceRecord{}}
	}
	if evidence.RunID == "" {
		evidence.RunID = plan.RunID
	}
	return plan, planPath, evidence, evidencePath, nil
}

func resolvePlanPath(opts commandOptions) (string, error) {
	if opts.planPath != "" {
		return filepath.Clean(opts.planPath), nil
	}
	if opts.input != "" {
		return filepath.Clean(opts.input), nil
	}
	if opts.outDir != "" {
		return filepath.Join(filepath.Clean(opts.outDir), "plan.json"), nil
	}
	if opts.resume == "last" {
		return resolveLatestRunFile(opts, "plan.json")
	}
	return "", errors.New("必须指定 --plan <plan.json>，或 --out-dir <run-dir>，或 --resume last")
}

func resolveEvidencePath(opts commandOptions, planPath string) string {
	if opts.evidencePath != "" {
		return filepath.Clean(opts.evidencePath)
	}
	if opts.statePath != "" {
		return filepath.Clean(opts.statePath)
	}
	return filepath.Join(filepath.Dir(planPath), "evidence.json")
}

func resolveLatestRunFile(opts commandOptions, name string) (string, error) {
	root := defaultRunsRoot(opts)
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), name)
		info, err := os.Stat(path)
		if err == nil {
			candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("未找到最近一次 %s", name)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })
	return candidates[0].path, nil
}

func storeResolver(opts commandOptions) (func(string) Store, map[string]bool, error) {
	cfg, err := loadStoresConfig(opts.storesConfig)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]Store{}
	knownStoreIDs := map[string]bool{}
	for _, store := range cfg.Stores {
		store.Normalize()
		byID[store.ID] = store
		knownStoreIDs[store.ID] = true
	}
	for _, store := range cfg.Archived {
		store.Normalize()
		store.Archived = true
		byID[store.ID] = store
		knownStoreIDs[store.ID] = true
	}
	return func(id string) Store {
		if store, ok := byID[id]; ok {
			return store
		}
		return Store{ID: id, Label: id, ShopifyStoreFallback: true}
	}, knownStoreIDs, nil
}

func validateRemoteStoreTargets(storeIDs []string, resolve func(string) Store, knownStoreIDs map[string]bool) error {
	var unknown []string
	var disabled []string
	var invalid []string
	for _, id := range storeIDs {
		if !knownStoreIDs[id] {
			unknown = append(unknown, id)
			continue
		}
		store := resolve(id)
		if !store.Enabled && !store.Archived {
			disabled = append(disabled, store.ID)
			continue
		}
		if store.ShopifyStoreFallback && (store.Archived || isDevelopmentStore(store)) {
			invalid = append(invalid, store.ID)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("远端执行拒绝未知 store ID，plan store 必须存在于 stores.config.json；缺少显式 shopifyStore 或 store 配置: %s", strings.Join(unknown, ","))
	}
	if len(disabled) > 0 {
		return fmt.Errorf("远端执行拒绝 disabled/source store: %s", strings.Join(disabled, ","))
	}
	if len(invalid) > 0 {
		return fmt.Errorf("远端执行拒绝 archived/development store 缺少显式 shopifyStore: %s", strings.Join(invalid, ","))
	}
	return nil
}

func isDevelopmentStore(store Store) bool {
	for _, value := range []string{store.ID, store.Label, store.ShopifyStore} {
		if isDevelopmentStoreName(value) {
			return true
		}
	}
	return false
}

func isDevelopmentStoreName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "dev" ||
		value == "development" ||
		strings.HasPrefix(value, "dev-") ||
		strings.HasPrefix(value, "development-") ||
		strings.HasSuffix(value, "-dev") ||
		strings.HasSuffix(value, "-development")
}

func loadEnvForRemoteCommand(opts commandOptions) error {
	if opts.noEnvFile {
		return nil
	}
	envPath := opts.envFile
	if envPath == "" {
		start := opts.pathBase
		if start == "" {
			start, _ = os.Getwd()
		}
		found, _ := findUpFrom(start, ".env.local")
		envPath = found
	}
	if envPath == "" {
		return nil
	}
	return loadDotEnv(envPath)
}

func changesWithAnyAction(plan Plan, storeSet map[string]bool, actions ...string) []PlanChange {
	wanted := map[string]bool{}
	for _, action := range actions {
		wanted[action] = true
	}
	var out []PlanChange
	for _, change := range plan.Changes {
		if !storeSet[change.Store] {
			continue
		}
		for _, action := range change.Actions {
			if wanted[action] {
				out = append(out, change)
				break
			}
		}
	}
	return out
}

func changesForStores(plan Plan, storeSet map[string]bool) []PlanChange {
	var out []PlanChange
	for _, change := range plan.Changes {
		if storeSet[change.Store] {
			out = append(out, change)
		}
	}
	return out
}

func failOnPlanErrors(plan Plan, storeSet map[string]bool) error {
	errors := 0
	for _, change := range plan.Changes {
		if storeSet[change.Store] && change.Status == "error" {
			errors++
		}
	}
	if errors == 0 {
		return nil
	}
	return fmt.Errorf("plan 在所选 store 范围内含 %d 个预检错误，未执行任何远端写入", errors)
}

func writeExecutionPreview(stdout io.Writer, command, planPath, evidencePath string, selectedStores []string, plan Plan, changes []PlanChange) error {
	storeSet := storeSetFromIDs(selectedStores)
	if scopedErrors := countPlanErrors(plan, storeSet); scopedErrors > 0 {
		fmt.Fprintf(stdout, "plan_errors=%d\n", scopedErrors)
	}
	fmt.Fprintf(stdout, "dry_run=true\n")
	fmt.Fprintf(stdout, "command=%s\n", command)
	fmt.Fprintf(stdout, "target_api=Admin GraphQL %s\n", shopifyAPIVersion)
	fmt.Fprintf(stdout, "target_stores=%s\n", strings.Join(selectedStores, ","))
	fmt.Fprintf(stdout, "plan=%s\n", planPath)
	fmt.Fprintf(stdout, "evidence=%s\n", evidencePath)
	fmt.Fprintf(stdout, "matching_changes=%d\n", len(changes))
	fmt.Fprintln(stdout, "rerun_with=--execute")
	for _, change := range changes {
		fmt.Fprintf(stdout, "%s row=%s %s -> %s actions=%s\n", change.Store, change.RowNo, change.SourceFilename, change.TargetFilename, strings.Join(change.Actions, ","))
	}
	return nil
}

func writeTargetAPI(stdout io.Writer, storeIDs []string, resolve func(string) Store) error {
	pairs := make([]string, 0, len(storeIDs))
	for _, storeID := range storeIDs {
		version, err := shopifyAPIVersionForStore(resolve(storeID))
		if err != nil {
			return err
		}
		pairs = append(pairs, storeID+":"+version)
	}
	fmt.Fprintln(stdout, "target_api=Admin GraphQL")
	fmt.Fprintf(stdout, "target_api_versions=%s\n", strings.Join(pairs, ","))
	return nil
}

func countPlanErrors(plan Plan, storeSet map[string]bool) int {
	errors := 0
	for _, change := range plan.Changes {
		if storeSet[change.Store] && change.Status == "error" {
			errors++
		}
	}
	return errors
}

func resolvePlanStoreScope(plan Plan, opts commandOptions) ([]string, map[string]bool, error) {
	planStores := planStoreIDs(plan)
	if len(planStores) == 0 {
		return nil, nil, errors.New("plan 中没有 store 范围")
	}
	if storeSpecMeansAll(opts.stores) {
		return planStores, storeSetFromIDs(planStores), nil
	}
	planSet := storeSetFromIDs(planStores)
	selected := resolvePlanStoreIDsFromSpec(planSet, opts)
	if len(selected) == 0 {
		return nil, nil, fmt.Errorf("所选 store 不在 plan 中: %s", opts.stores)
	}
	var missing []string
	for _, id := range selected {
		if !planSet[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("所选 store 不在 plan 中: %s", strings.Join(missing, ","))
	}
	return selected, storeSetFromIDs(selected), nil
}

func resolvePlanStoreIDsFromSpec(planSet map[string]bool, opts commandOptions) []string {
	cfg, err := loadStoresConfig(opts.storesConfig)
	if err == nil {
		stores, selectErr := selectStores(cfg, opts.stores)
		if selectErr == nil {
			ids := make([]string, 0, len(stores))
			seen := map[string]bool{}
			for _, store := range stores {
				if !seen[store.ID] {
					seen[store.ID] = true
					ids = append(ids, store.ID)
				}
			}
			return ids
		}
	}
	var ids []string
	seen := map[string]bool{}
	for _, item := range splitCSV(opts.stores) {
		id := strings.ToLower(item)
		if planSet[id] && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func planStoreIDs(plan Plan) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range plan.Summary.Stores {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, change := range plan.Changes {
		if change.Store != "" && !seen[change.Store] {
			seen[change.Store] = true
			out = append(out, change.Store)
		}
	}
	return out
}

func storeSetFromIDs(ids []string) map[string]bool {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func storeSpecMeansAll(spec string) bool {
	items := splitCSV(spec)
	if len(items) == 0 {
		return true
	}
	for _, item := range items {
		if strings.EqualFold(item, "all") {
			return true
		}
	}
	return false
}

func evidenceRecordFromChange(change PlanChange) EvidenceRecord {
	return EvidenceRecord{
		Store:          change.Store,
		RowNo:          change.RowNo,
		SourceFilename: change.SourceFilename,
		TargetFilename: change.TargetFilename,
		Filename:       change.TargetFilename,
	}
}

func evidenceRecordFromFile(change PlanChange, resource ResourceInfo, node ShopifyFileNode) EvidenceRecord {
	record := evidenceRecordFromChange(change)
	record.FileID = node.ID
	record.MediaGID = node.ID
	record.FileStatus = node.FileStatus
	record.AltReadback = node.Alt
	if node.Image != nil {
		record.CDNURL = node.Image.URL
		record.Width = node.Image.Width
		record.Height = node.Image.Height
	} else if isVideoNode(node) {
		record.CDNURL = videoSourceURL(node)
	}
	record.Bytes = resource.Bytes
	record.SHA256 = resource.SHA256
	record.MimeType = resource.MimeType
	return record
}

func resourceForChange(change PlanChange) (ResourceInfo, error) {
	if change.Resource == nil {
		return ResourceInfo{}, fmt.Errorf("%s row=%s 缺少本地资源，不能执行上传/替换", change.Store, change.RowNo)
	}
	return *change.Resource, nil
}

func containsAction(actions []string, want string) bool {
	for _, action := range actions {
		if action == want {
			return true
		}
	}
	return false
}

func validateAltReadback(change PlanChange, actual string) error {
	if change.Alt == "" {
		return nil
	}
	if actual != change.Alt {
		return fmt.Errorf("%s row=%s alt readback mismatch: expected %q got %q", change.Store, change.RowNo, change.Alt, actual)
	}
	return nil
}

func appendErrorMessage(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "; " + next
}

func normalizeDuplicatePolicy(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "REPLACE":
		return "REPLACE", nil
	case "APPEND_UUID":
		return "APPEND_UUID", nil
	case "ERROR", "RAISE_ERROR":
		return "RAISE_ERROR", nil
	default:
		return "", fmt.Errorf("--duplicate-policy 只支持 REPLACE / APPEND_UUID / ERROR")
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func writeStatusReport(path string, plan Plan, evidence Evidence) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"run_id", "store", "row_no", "source图片名", "target图片文件名", "status", "file_id", "media_gid", "cdn_url", "sha256", "alt_readback", "last_error", "updated_at"}); err != nil {
		return err
	}
	for _, change := range plan.Changes {
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		if record == nil {
			record = &EvidenceRecord{Store: change.Store, RowNo: change.RowNo, SourceFilename: change.SourceFilename, TargetFilename: change.TargetFilename, Status: "MISSING"}
		}
		reportRecord := *record
		if err := pendingJSONReplacementError(change); err != nil && strings.EqualFold(reportRecord.Status, "READY") {
			reportRecord.Status = "PENDING_JSON_REPLACE"
			reportRecord.LastError = err.Error()
		}
		if err := writer.Write([]string{
			plan.RunID,
			change.Store,
			change.RowNo,
			change.SourceFilename,
			change.TargetFilename,
			reportRecord.Status,
			reportRecord.FileID,
			reportRecord.MediaGID,
			reportRecord.CDNURL,
			reportRecord.SHA256,
			reportRecord.AltReadback,
			reportRecord.LastError,
			reportRecord.UpdatedAt,
		}); err != nil {
			return err
		}
	}
	return writer.Error()
}
