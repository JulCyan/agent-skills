package mediasync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type scriptedApplyStageRunner struct {
	calls       []string
	failStage   string
	failErr     error
	failRows    map[string]string
	mutationIDs map[string]string
	statuses    []string
	observeErr  error
}

func (r *scriptedApplyStageRunner) Run(_ context.Context, stage string, changes []PlanChange, opts commandOptions, evidence *Evidence, evidencePath string, _ io.Writer) error {
	r.calls = append(r.calls, stage)
	if opts.summaryPath != "" {
		var summary ApplySummary
		raw, err := os.ReadFile(opts.summaryPath)
		if err == nil {
			err = json.Unmarshal(raw, &summary)
		}
		if err != nil {
			r.observeErr = err
		} else {
			r.statuses = append(r.statuses, summary.FinalStatus+":"+summary.CurrentStage)
		}
	}
	for _, change := range changes {
		record := evidenceRecordFromChange(change)
		if existing := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename}); existing != nil {
			record = mergeEvidenceRecord(*existing, record)
		}
		stageEvidence := StageEvidence{Status: stageStatusSucceeded, UpdatedAt: nowUTC()}
		switch stage {
		case applyStageUpload:
			stageEvidence.Attempts = record.Upload.Attempts + 1
			stageEvidence.MutationAccepted = true
			stageEvidence.Retryable = true
			record.FileID = "gid://shopify/MediaImage/" + change.RowNo
			record.MediaGID = record.FileID
			if change.Resource != nil {
				record.SHA256 = change.Resource.SHA256
			}
		case applyStageUploadReadback:
			stageEvidence = record.Upload
			stageEvidence.Status = stageStatusSucceeded
			stageEvidence.LastError = ""
			stageEvidence.UpdatedAt = nowUTC()
		case applyStageAlt:
			stageEvidence.Attempts = record.Alt.Attempts + 1
		case applyStageVerify:
			stageEvidence.Attempts = record.Verify.Attempts + 1
		}
		if message := r.failRows[stage+":"+change.RowNo]; message != "" {
			stageEvidence.Status = stageStatusFailed
			stageEvidence.LastError = message
			stageEvidence.Retryable = true
		}
		setRowStage(&record, stage, stageEvidence)
		evidence.Upsert(record)
	}
	if err := writeJSON(evidencePath, evidence); err != nil {
		return err
	}
	if r.failStage == stage {
		if r.failErr != nil {
			return r.failErr
		}
		return errors.New("forced " + stage + " failure")
	}
	return nil
}

func installApplyRunner(t *testing.T, runner applyStageRunner) {
	t.Helper()
	old := newApplyStageRunner
	newApplyStageRunner = func() applyStageRunner { return runner }
	t.Cleanup(func() { newApplyStageRunner = old })
}

func writeApplyFixture(t *testing.T, plan Plan, evidence Evidence) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	summaryPath := filepath.Join(dir, "apply-summary.json")
	mustWriteJSON(t, planPath, plan)
	planResource, err := inspectResource(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.PlanSHA256 == "" {
		evidence.PlanSHA256 = planResource.SHA256
	}
	if evidence.Preview == nil {
		binding, err := buildApplyPreviewBinding(planResource.SHA256, planStoreIDs(plan), "")
		if err != nil {
			t.Fatal(err)
		}
		binding.PreviewedAt = nowUTC()
		evidence.Preview = &binding
	}
	mustWriteJSON(t, evidencePath, evidence)
	return planPath, evidencePath, summaryPath
}

func basicApplyPlan(t *testing.T, actions ...string) Plan {
	t.Helper()
	resourcePath := filepath.Join(t.TempDir(), "same.svg")
	if err := os.WriteFile(resourcePath, []byte("<svg></svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource, err := inspectResource(resourcePath)
	if err != nil {
		t.Fatal(err)
	}
	return Plan{
		RunID: "run-apply-test",
		Summary: PlanSummary{
			Stores:   []string{"store-a"},
			Rows:     1,
			Changes:  1,
			ByAction: actionCounts([]PlanChange{{Actions: actions}}),
		},
		Changes: []PlanChange{{
			Store:          "store-a",
			RowNo:          "1",
			SourceFilename: "same.svg",
			TargetFilename: "same.svg",
			Actions:        actions,
			Status:         "planned",
			Resource:       &resource,
			NeedsExecute:   true,
		}},
	}
}

func runApplyArgs(planPath, evidencePath, summaryPath string, execute bool) []string {
	args := []string{"apply", "--plan", planPath, "--evidence", evidencePath, "--summary", summaryPath, "--no-env-file", "--format", "json"}
	if execute {
		args = append(args, "--execute")
	}
	return args
}

func readApplySummary(t *testing.T, path string) ApplySummary {
	t.Helper()
	var summary ApplySummary
	readJSONFixture(t, path, &summary)
	return summary
}

func readApplyAttemptLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	items := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("invalid apply attempt JSONL line: %v\n%s", err, line)
		}
		items = append(items, item)
	}
	return items
}

func TestApplyRequiresExplicitPlan(t *testing.T) {
	err := run(t.Context(), []string{"apply", "--execute", "--no-env-file"}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "apply 必须显式指定 --plan") {
		t.Fatalf("expected explicit plan error, got %v", err)
	}
}

func TestPlanRejectsExecuteFlag(t *testing.T) {
	err := run(t.Context(), []string{"plan", "--execute"}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "plan 不接受 --execute") {
		t.Fatalf("plan must never accept a remote-write flag, got %v", err)
	}
}

func TestApplyRejectsLatestPlanInference(t *testing.T) {
	err := run(t.Context(), []string{"apply", "--resume", "last", "--execute", "--no-env-file"}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("expected latest-plan rejection, got %v", err)
	}
}

func TestApplyPreviewDoesNotRunRemoteStages(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename", "alt_update")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	var stdout bytes.Buffer
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), &stdout); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("preview must not run remote stages: %v", runner.calls)
	}
	summary := readApplySummary(t, summaryPath)
	if summary.FinalStatus != applyStatusPreviewReady || !summary.DryRun || summary.PlanPath != planPath {
		t.Fatalf("unexpected preview summary: %+v", summary)
	}
	if !strings.Contains(stdout.String(), `"final_status":"PREVIEW_READY"`) {
		t.Fatalf("expected stable JSON stdout, got %s", stdout.String())
	}
}

func TestApplyExecuteRequiresMatchingPreviewBinding(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	evidence.Preview = nil
	mustWriteJSON(t, evidencePath, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	err = run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "preview scope binding") {
		t.Fatalf("expected missing preview binding rejection, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("missing preview binding must stop before remote stages: %v", runner.calls)
	}
}

func TestApplyAttemptHistoryIsAppendOnlyAndKeepsExecutionProvenance(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)

	previewArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, false),
		"--stores", "store-a", "--stores-config", filepath.Join("testdata", "stores.config.json"))
	if err := run(t.Context(), previewArgs, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	executeArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, true),
		"--stores", "store-a", "--stores-config", filepath.Join("testdata", "stores.config.json"))
	if err := run(t.Context(), executeArgs, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	attemptsPath := filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl")
	attempts := readApplyAttemptLines(t, attemptsPath)
	if len(attempts) != 2 {
		t.Fatalf("expected append-only preview and execute attempts, got %d: %+v", len(attempts), attempts)
	}
	if attempts[0]["mode"] != "preview" || attempts[0]["final_status"] != applyStatusPreviewReady {
		t.Fatalf("preview attempt provenance was lost: %+v", attempts[0])
	}
	if attempts[1]["mode"] != "execute" || attempts[1]["final_status"] != applyStatusSucceeded {
		t.Fatalf("execute attempt provenance was lost: %+v", attempts[1])
	}
	if attempts[1]["requested_stores"] != "store-a" || attempts[1]["stores_config_path"] == "" {
		t.Fatalf("execution scope/config provenance missing: %+v", attempts[1])
	}
	if attempts[1]["plan_sha256"] == "" || attempts[1]["duration_ms"] == nil {
		t.Fatalf("attempt identity or duration missing: %+v", attempts[1])
	}
	summary := readApplySummary(t, summaryPath)
	if summary.AttemptsPath != attemptsPath || summary.AttemptID == "" || summary.DurationMS < 0 || !containsString(summary.Artifacts, attemptsPath) {
		t.Fatalf("summary does not link to append-only attempts: %+v", summary)
	}
}

func TestApplyFailureStillAppendsTerminalAttempt(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{failStage: applyStageUpload, failRows: map[string]string{"upload:1": "boom"}}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err == nil {
		t.Fatal("failed apply must return non-zero")
	}
	attempts := readApplyAttemptLines(t, filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl"))
	if len(attempts) != 1 || attempts[0]["final_status"] != applyStatusPartialFailure || attempts[0]["failed_stage"] != applyStageUpload {
		t.Fatalf("failed terminal attempt was not preserved: %+v", attempts)
	}
}

func TestApplyAttemptRecordsAutoDiscoveredStoresConfigPath(t *testing.T) {
	callerDir := t.TempDir()
	configPath := filepath.Join(callerDir, "stores.config.json")
	if err := os.WriteFile(configPath, []byte(`{"stores":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEDIA_SYNC_CALLER_CWD", callerDir)
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	attempts := readApplyAttemptLines(t, filepath.Join(filepath.Dir(planPath), "apply-attempts.jsonl"))
	if len(attempts) != 1 || attempts[0]["stores_config_path"] != configPath {
		t.Fatalf("auto-discovered stores config provenance missing: %+v", attempts)
	}
}

func TestApplyExecuteRunsOnlyExplicitPlanScope(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	plan.Changes = append(plan.Changes, PlanChange{Store: "store-b", RowNo: "2", SourceFilename: "other.svg", TargetFilename: "other.svg", Actions: []string{"file_upload_same_filename"}, Resource: plan.Changes[0].Resource})
	plan.Summary.Stores = []string{"store-a", "store-b"}
	plan.Summary.Rows = 2
	plan.Summary.Changes = 2
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	previewArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, false), "--stores", "store-a")
	if err := run(t.Context(), previewArgs, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	args := append(runApplyArgs(planPath, evidencePath, summaryPath, true), "--stores", "store-a")
	if err := run(t.Context(), args, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Find("store-a", DesiredRow{RowNo: "1", TargetFilename: "same.svg"}) == nil {
		t.Fatal("selected plan row was not executed")
	}
	if evidence.Find("store-b", DesiredRow{RowNo: "2", TargetFilename: "other.svg"}) != nil {
		t.Fatal("apply expanded beyond explicitly selected plan scope")
	}
}

func TestApplyExecuteRejectsScopeDriftAfterPreview(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	second := plan.Changes[0]
	second.Store = "store-b"
	second.RowNo = "2"
	second.TargetFilename = "other.svg"
	plan.Changes = append(plan.Changes, second)
	plan.Summary.Stores = []string{"store-a", "store-b"}
	plan.Summary.Rows = 2
	plan.Summary.Changes = 2
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	previewArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, false), "--stores", "store-a")
	if err := run(t.Context(), previewArgs, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "preview") || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected preview scope drift rejection, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("scope drift must stop before remote stages: %v", runner.calls)
	}
}

func TestApplyExecuteRejectsStoresConfigDriftAfterPreview(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	dir := t.TempDir()
	previewedConfig := filepath.Join(dir, "previewed.json")
	driftedConfig := filepath.Join(dir, "drifted.json")
	mustWriteJSON(t, previewedConfig, StoresConfig{Stores: []Store{{ID: "store-a", ShopifyStore: "previewed-store", Enabled: true}}})
	mustWriteJSON(t, driftedConfig, StoresConfig{Stores: []Store{{ID: "store-a", ShopifyStore: "drifted-store", Enabled: true}}})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	previewArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, false), "--stores", "store-a", "--stores-config", previewedConfig)
	if err := run(t.Context(), previewArgs, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	executeArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, true), "--stores", "store-a", "--stores-config", driftedConfig)
	err := run(t.Context(), executeArgs, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "stores config") {
		t.Fatalf("expected stores config drift rejection, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("config drift must stop before remote stages: %v", runner.calls)
	}
}

func TestStoreResolverUsesFrozenApplyConfigSnapshot(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	previewed := StoresConfig{Stores: []Store{{
		ID: "store-a", ShopifyStore: "previewed-store", Enabled: true,
	}}}
	drifted := StoresConfig{Stores: []Store{{
		ID: "store-a", ShopifyStore: "drifted-store", Enabled: true,
	}}}
	previewedRaw, err := json.Marshal(previewed)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteJSON(t, configPath, drifted)
	resolve, _, err := storeResolver(commandOptions{
		storesConfig:         configPath,
		storesConfigSnapshot: previewedRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolve("store-a").ShopifyStore; got != "previewed-store" {
		t.Fatalf("store resolver re-read drifted config: %s", got)
	}
}

func TestApplyRoutesMP4ThroughVideoUploadAndReadback(t *testing.T) {
	setTestAdminToken(t, "store-us")
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "product-video.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource, err := inspectResource(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	resource.MimeType = "video/mp4"
	plan := Plan{
		RunID:   "run-apply-video",
		Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1, ByAction: map[string]int{"file_upload_same_filename": 1}},
		Changes: []PlanChange{{
			Store: "store-us", RowNo: "1", SourceFilename: "product-video.mp4", TargetFilename: "product-video.mp4",
			Actions: []string{"file_upload_same_filename"}, Status: "planned", Resource: &resource, NeedsExecute: true,
		}},
	}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(req *http.Request) (int, string) {
			if req.URL.Host == "upload.shopify.test" {
				return http.StatusCreated, "ok"
			}
			raw, readErr := io.ReadAll(req.Body)
			if readErr != nil {
				return http.StatusInternalServerError, `{}`
			}
			var body struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				return http.StatusBadRequest, `{}`
			}
			switch {
			case strings.Contains(body.Query, "files("):
				return http.StatusOK, `{"data":{"files":{"nodes":[]}}}`
			case strings.Contains(body.Query, "stagedUploadsCreate"):
				input := body.Variables["input"].([]any)[0].(map[string]any)
				if input["resource"] != "VIDEO" || input["fileSize"] != "9" {
					return http.StatusBadRequest, `{"errors":[{"message":"standard apply used non-video staged input"}]}`
				}
				return http.StatusOK, `{"data":{"stagedUploadsCreate":{"stagedTargets":[{"url":"https://upload.shopify.test/product-video.mp4","resourceUrl":"https://resource.shopify.test/product-video.mp4","parameters":[]}],"userErrors":[]}}}`
			case strings.Contains(body.Query, "fileCreate"):
				input := body.Variables["files"].([]any)[0].(map[string]any)
				if input["contentType"] != "VIDEO" {
					return http.StatusBadRequest, `{"errors":[{"message":"standard apply used non-video fileCreate input"}]}`
				}
				return http.StatusOK, `{"data":{"fileCreate":{"files":[{"id":"gid://shopify/Video/target","filename":"product-video.mp4","fileStatus":"UPLOADED","__typename":"Video","sources":[]}],"userErrors":[]}}}`
			case strings.Contains(body.Query, "node("):
				return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/Video/target","filename":"product-video.mp4","fileStatus":"READY","__typename":"Video","originalSource":{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"},"sources":[{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"}]}}}`
			default:
				return http.StatusBadRequest, `{"errors":[{"message":"unexpected standard apply video query"}]}`
			}
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })
	args := append(runApplyArgs(planPath, evidencePath, summaryPath, false), "--stores", "store-us", "--stores-config", filepath.Join("testdata", "stores.config.json"))
	if err := run(t.Context(), args, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	args = append(runApplyArgs(planPath, evidencePath, summaryPath, true), "--stores", "store-us", "--stores-config", filepath.Join("testdata", "stores.config.json"), "--max-attempts", "1", "--poll-interval", "1ns")
	if err := run(t.Context(), args, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := loaded.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "product-video.mp4"})
	if record == nil || record.Upload.Status != stageStatusSucceeded || record.Verify.Status != stageStatusSucceeded || record.MimeType != "video/mp4" || record.CDNURL == "" {
		t.Fatalf("unexpected standard apply video evidence: %+v", record)
	}
}

func TestVideoApplyPreflightRejectsUnprovenExactFilename(t *testing.T) {
	setTestAdminToken(t, "store-us")
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "product-video.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource, err := inspectResource(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	change := PlanChange{Store: "store-us", RowNo: "1", TargetFilename: "product-video.mp4", Actions: []string{"file_upload_same_filename"}, Resource: &resource}
	mutations := 0
	client := testShopifyClient(func(req *http.Request) (int, string) {
		raw, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			return http.StatusInternalServerError, `{}`
		}
		var body struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return http.StatusBadRequest, `{}`
		}
		switch {
		case strings.Contains(body.Query, "files("):
			return http.StatusOK, `{"data":{"files":{"nodes":[{"id":"gid://shopify/Video/existing","filename":"product-video.mp4","fileStatus":"READY","__typename":"Video","sources":[{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"}] }]}}}`
		default:
			mutations++
			return http.StatusBadRequest, `{"errors":[{"message":"unexpected mutation"}]}`
		}
	})
	_, err = executeFileChange(t.Context(), client, Store{ID: "store-us", ShopifyStore: "store-us"}, change, resource, Evidence{}, "REPLACE", commandOptions{maxAttempts: 1, pollInterval: time.Nanosecond})
	if err == nil || !strings.Contains(err.Error(), "缺少 SHA 匹配的成功 evidence") {
		t.Fatalf("expected unproven exact VIDEO to fail closed, got %v", err)
	}
	if mutations != 0 {
		t.Fatalf("exact VIDEO preflight created a duplicate: mutations=%d", mutations)
	}
}

func TestVideoReadbackRejectsFilenameDriftAcrossRecoveryPaths(t *testing.T) {
	setTestAdminToken(t, "store-us")
	client := testShopifyClient(func(*http.Request) (int, string) {
		return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/Video/drifted","filename":"product-video-uuid.mp4","fileStatus":"READY","__typename":"Video","sources":[{"url":"https://cdn.shopify.test/product-video-uuid.mp4","mimeType":"video/mp4"}]}}}`
	})
	change := PlanChange{Store: "store-us", RowNo: "1", TargetFilename: "product-video.mp4", Resource: &ResourceInfo{Filename: "product-video.mp4", MimeType: "video/mp4"}}
	_, err := pollChangeReady(t.Context(), client, Store{ID: "store-us", ShopifyStore: "store-us"}, change, "gid://shopify/Video/drifted", 1, time.Nanosecond)
	if err == nil || !strings.Contains(err.Error(), "filename 漂移") {
		t.Fatalf("expected VIDEO filename drift rejection, got %v", err)
	}
}

func TestApplyRejectsResourceDriftBeforeAnyStage(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	if err := os.WriteFile(plan.Changes[0].Resource.Path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "资源 SHA 漂移") {
		t.Fatalf("expected resource drift rejection, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("resource drift must stop before remote stage: %v", runner.calls)
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusNeedsAttention {
		t.Fatalf("unexpected drift summary: %+v", got)
	}
}

func TestApplyRejectsPlanContentDriftAfterPreview(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename", "alt_update")
	plan.Changes[0].Alt = "Approved alt"
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	plan.Changes[0].Alt = "Changed after preview"
	mustWriteJSON(t, planPath, plan)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "plan SHA 漂移") {
		t.Fatalf("expected approved plan digest mismatch, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("plan drift must stop before remote stages: %v", runner.calls)
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusNeedsAttention || got.PlanSHA256 == "" {
		t.Fatalf("unexpected plan drift summary: %+v", got)
	}
}

func TestApplyPreviewCanRebindReviewedPlanContent(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	plan.Changes[0].Reason = "reviewed plan revision"
	mustWriteJSON(t, planPath, plan)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, false), ioDiscard{}); err != nil {
		t.Fatalf("reviewed plan must be re-bindable by a new preview: %v", err)
	}
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyUploadSuccessAdvancesToAltAndVerify(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename", "alt_update")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.calls, ",") != "upload,alt,verify" {
		t.Fatalf("unexpected stage order: %v", runner.calls)
	}
	if runner.observeErr != nil || strings.Join(runner.statuses, ",") != "RUNNING:upload,RUNNING:alt,RUNNING:verify" {
		t.Fatalf("apply did not persist observable RUNNING progress: statuses=%v err=%v", runner.statuses, runner.observeErr)
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusSucceeded || got.Counts.Success != 1 {
		t.Fatalf("unexpected success summary: %+v", got)
	}
}

func TestApplyUploadFailureStopsBeforeAlt(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename", "alt_update")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{failStage: applyStageUpload, failRows: map[string]string{"upload:1": "upload failed"}}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if strings.Join(runner.calls, ",") != "upload" {
		t.Fatalf("alt must not run after upload failure: %v", runner.calls)
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusPartialFailure || got.Counts.Errors != 1 {
		t.Fatalf("unexpected failure summary: %+v", got)
	}
}

func TestApplyPreStageFailureDoesNotCountRowsAsSuccess(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	configPath := filepath.Join("testdata", "stores.config.json")
	previewArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, false), "--stores-config", configPath)
	if err := run(t.Context(), previewArgs, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	args := append(runApplyArgs(planPath, evidencePath, summaryPath, true), "--stores-config", configPath)
	err := run(t.Context(), args, ioDiscard{})
	if err == nil {
		t.Fatal("unknown store must fail before the stage")
	}
	summary := readApplySummary(t, summaryPath)
	if summary.FinalStatus != applyStatusPartialFailure || summary.Counts.Success != 0 || summary.Counts.Errors != 1 || len(summary.Errors) != 1 {
		t.Fatalf("pre-stage failure produced misleading counts: %+v", summary)
	}
}

func TestApplyPlanErrorCountsBlockedRowsAsSkipped(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	blocked := plan.Changes[0]
	blocked.RowNo = "2"
	blocked.TargetFilename = "blocked.svg"
	blocked.Status = "error"
	blocked.Reason = "planning failure"
	plan.Changes = append(plan.Changes, blocked)
	plan.Summary.Rows = 2
	plan.Summary.Changes = 2
	plan.Summary.Errors = 1
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil {
		t.Fatal("plan error must stop apply")
	}
	summary := readApplySummary(t, summaryPath)
	if summary.FinalStatus != applyStatusFailed || summary.Counts.Total != 2 || summary.Counts.Errors != 1 || summary.Counts.Skipped != 1 || summary.Counts.Success != 0 {
		t.Fatalf("globally blocked rows disappeared from counts: %+v", summary)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("plan error must stop before stages: %v", runner.calls)
	}
}

func TestApplyAltFailureDoesNotClaimSuccess(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename", "alt_update")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{failStage: applyStageAlt, failRows: map[string]string{"alt:1": "alt failed"}}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil {
		t.Fatal("expected alt failure")
	}
	if strings.Join(runner.calls, ",") != "upload,alt" {
		t.Fatalf("verify must not run after alt failure: %v", runner.calls)
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus == applyStatusSucceeded || got.FailedStage != applyStageAlt {
		t.Fatalf("alt failure was misreported: %+v", got)
	}
}

func TestApplyJSONActionWaitsForExternalProcessing(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_new_filename", "alt_update", "json_replace_needed")
	plan.Template = "product.example"
	plan.Changes[0].Template = plan.Template
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil {
		t.Fatal("waiting external must return non-zero")
	}
	if strings.Join(runner.calls, ",") != "upload,alt" {
		t.Fatalf("verify must wait for external JSON receipt: %v", runner.calls)
	}
	summary := readApplySummary(t, summaryPath)
	if summary.FinalStatus != applyStatusWaitingExternal || !summary.NeedsExternal || !strings.Contains(summary.NextSafeAction, "JSON") || summary.Counts.Success != 0 || summary.Counts.Skipped != 1 {
		t.Fatalf("unexpected waiting summary: %+v", summary)
	}
}

func TestApplyResumesVerifyAfterExternalJSONReceipt(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_new_filename", "alt_update", "json_replace_needed")
	plan.Template = "product.example"
	plan.TargetTheme = "review-theme"
	plan.Changes[0].Template = plan.Template
	plan.Changes[0].TargetTheme = plan.TargetTheme
	plan.Changes[0].SourceFilename = "old.svg"
	plan.Changes[0].TargetFilename = "new.svg"
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	receiptPath := filepath.Join(filepath.Dir(planPath), "receipt.json")
	t.Setenv("MEDIA_SYNC_CALLER_CWD", filepath.Dir(planPath))
	mustWriteJSON(t, receiptPath, jsonReplacementReceipt{
		Version: 2, Verified: true, Template: plan.Template, TargetTheme: plan.TargetTheme,
		Results: []struct {
			Store string `json:"store"`
			OK    bool   `json:"ok"`
		}{{Store: "store-a", OK: true}},
		MappingResults: []jsonReplacementMappingResult{{SourceFilename: "old.svg", TargetFilename: "new.svg", ReplacementCount: 1, Matched: true}},
	})
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	args := append(runApplyArgs(planPath, evidencePath, summaryPath, true), "--json-receipt", filepath.Base(receiptPath))
	if err := run(t.Context(), args, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.calls, ",") != "upload,alt,verify" {
		t.Fatalf("valid external receipt did not resume verify: %v", runner.calls)
	}
	summary := readApplySummary(t, summaryPath)
	if summary.FinalStatus != applyStatusSucceeded || !containsString(summary.Artifacts, receiptPath) {
		t.Fatalf("receipt-backed apply did not succeed with receipt artifact: %+v", summary)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestApplyRerunSkipsSucceededUploadWithMatchingSHA(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/1", MediaGID: "gid://shopify/MediaImage/1", SHA256: change.Resource.SHA256,
		Upload: StageEvidence{Status: stageStatusSucceeded, MutationAccepted: true},
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(runner.calls, ","), applyStageUpload) {
		t.Fatalf("completed upload was repeated: %v", runner.calls)
	}
}

func TestApplyRejectsSucceededUploadWithDifferentEvidenceSHA(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/stale", MediaGID: "gid://shopify/MediaImage/stale", SHA256: "different-sha",
		Upload: StageEvidence{Status: stageStatusSucceeded, MutationAccepted: true},
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "SHA") {
		t.Fatalf("expected evidence SHA mismatch to fail closed, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("SHA mismatch must not repeat a mutation: %v", runner.calls)
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusNeedsAttention {
		t.Fatalf("unexpected SHA mismatch summary: %+v", got)
	}
}

func TestApplyNormalizesLegacyReadyEvidenceAndSkipsUpload(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, SourceFilename: change.SourceFilename, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/legacy", MediaGID: "gid://shopify/MediaImage/legacy", SHA256: change.Resource.SHA256,
		Status: "READY",
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(runner.calls, ","), applyStageUpload) {
		t.Fatalf("legacy completed upload was repeated: %v", runner.calls)
	}
	loaded, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := loaded.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
	if record == nil || record.Upload.Status != stageStatusSucceeded {
		t.Fatalf("legacy READY evidence was not normalized explicitly: %+v", record)
	}
}

func TestApplyRerunResumesReadbackWithoutMutation(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/1", MediaGID: "gid://shopify/MediaImage/1", SHA256: change.Resource.SHA256,
		Upload: StageEvidence{Status: stageStatusAwaitingReadback, MutationAccepted: true, Retryable: true},
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.calls, ",") != "upload_readback,verify" {
		t.Fatalf("expected readback-only recovery, got %v", runner.calls)
	}
}

func TestApplyRerunResumesAltReadbackWithoutAltMutation(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename", "alt_update")
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/1", MediaGID: "gid://shopify/MediaImage/1", SHA256: change.Resource.SHA256,
		Upload: StageEvidence{Status: stageStatusSucceeded, MutationAccepted: true},
		Alt:    StageEvidence{Status: stageStatusAwaitingReadback, Checkpoint: "alt_file_readback", MutationAccepted: true, Retryable: true},
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.calls, ",") != "alt_readback,verify" {
		t.Fatalf("expected alt readback-only recovery, got %v", runner.calls)
	}
}

func TestApplyReadbackRecoveryNeverRepeatsMutation(t *testing.T) {
	setTestAdminToken(t, "store-us")
	plan := basicApplyPlan(t, "file_upload_same_filename")
	plan.Summary.Stores = []string{"store-us"}
	plan.Changes[0].Store = "store-us"
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, SourceFilename: change.SourceFilename, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/readback", MediaGID: "gid://shopify/MediaImage/readback", SHA256: change.Resource.SHA256,
		Status: "ERROR", LastError: "previous poll timeout",
		Upload: StageEvidence{Status: stageStatusAwaitingReadback, MutationAccepted: true, Retryable: true, Attempts: 1},
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	var queries []string
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(req *http.Request) (int, string) {
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			queries = append(queries, body.Query)
			if !strings.Contains(body.Query, "node") {
				t.Fatalf("readback recovery repeated a mutation: %s", body.Query)
			}
			return http.StatusOK, readyNodeResponse("gid://shopify/MediaImage/readback")
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	args := append(runApplyArgs(planPath, evidencePath, summaryPath, true),
		"--stores-config", filepath.Join("testdata", "stores.config.json"), "--stores", "store-us", "--max-attempts", "1", "--poll-interval", "1ns")
	previewArgs := append(runApplyArgs(planPath, evidencePath, summaryPath, false),
		"--stores-config", filepath.Join("testdata", "stores.config.json"), "--stores", "store-us")
	if err := run(t.Context(), previewArgs, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), args, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 {
		t.Fatalf("expected one upload readback and one verify readback, got %d", len(queries))
	}
	loaded, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := loaded.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
	if record == nil || record.Upload.Status != stageStatusSucceeded || record.Upload.Attempts != 2 || record.Verify.Status != stageStatusSucceeded {
		t.Fatalf("unexpected recovered evidence: %+v", record)
	}
}

func TestApplyRetriesFailureBeforeMutation(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename, SHA256: change.Resource.SHA256,
		Upload: StageEvidence{Status: stageStatusFailed, MutationAccepted: false, Retryable: true, LastError: "local failure"},
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	if err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) == 0 || runner.calls[0] != applyStageUpload {
		t.Fatalf("mutation-before failure was not retried: %v", runner.calls)
	}
}

func TestApplyLegacyFailureIsNotTreatedAsSuccess(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	change := plan.Changes[0]
	evidence := Evidence{RunID: plan.RunID, Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/ambiguous", SHA256: change.Resource.SHA256, Status: "ERROR", LastError: "timeout",
	}}}
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, evidence)
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil {
		t.Fatal("ambiguous legacy failure must need attention")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("ambiguous legacy failure must not mutate or read back automatically: %v", runner.calls)
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusNeedsAttention {
		t.Fatalf("legacy failure was misclassified: %+v", got)
	}
}

func TestApplySummaryPreservesAllRowErrors(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	second := plan.Changes[0]
	second.RowNo = "2"
	second.TargetFilename = "second.svg"
	plan.Changes = append(plan.Changes, second)
	plan.Summary.Rows = 2
	plan.Summary.Changes = 2
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{failStage: applyStageUpload, failRows: map[string]string{"upload:1": "first", "upload:2": "second"}}
	installApplyRunner(t, runner)
	_ = run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	summary := readApplySummary(t, summaryPath)
	if len(summary.Errors) != 2 || summary.Counts.Errors != 2 || len(summary.Artifacts) < 3 || summary.NextSafeAction == "" {
		t.Fatalf("summary lost aggregate details: %+v", summary)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Results) != 2 {
		t.Fatalf("row evidence was collapsed: %+v", evidence.Results)
	}
}

func TestApplyCancellationWritesCancelledSummary(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner := &scriptedApplyStageRunner{}
	installApplyRunner(t, runner)
	err := run(ctx, runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil {
		t.Fatal("cancelled apply must return non-zero")
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusCancelled {
		t.Fatalf("unexpected cancelled summary: %+v", got)
	}
}

func TestApplyCancellationDuringStageWritesCancelledSummary(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	runner := &scriptedApplyStageRunner{failStage: applyStageUpload, failErr: context.Canceled}
	installApplyRunner(t, runner)
	err := run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil {
		t.Fatal("cancelled stage must return non-zero")
	}
	if got := readApplySummary(t, summaryPath); got.FinalStatus != applyStatusCancelled {
		t.Fatalf("stage cancellation was misclassified: %+v", got)
	}
}

func TestApplyUsesEvidenceLease(t *testing.T) {
	plan := basicApplyPlan(t, "file_upload_same_filename")
	planPath, evidencePath, summaryPath := writeApplyFixture(t, plan, Evidence{RunID: plan.RunID})
	lease, err := acquireExecutionLease(commandOptions{command: "upload", planPath: planPath, evidencePath: evidencePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.release() })
	err = run(t.Context(), runApplyArgs(planPath, evidencePath, summaryPath, true), ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "execution lease") {
		t.Fatalf("expected apply lease rejection, got %v", err)
	}
}
