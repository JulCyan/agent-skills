package mediasync

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// runDirectStageForTest keeps lower-level mutation algorithms covered while
// the public CLI deliberately exposes remote writes only through apply.
func runDirectStageForTest(ctx context.Context, args []string, stdout io.Writer) error {
	opts, err := parseCommand(args)
	if err != nil {
		return err
	}
	execute := opts.execute
	opts.execute = false
	if err := validateCommandOptions(opts); err != nil {
		return err
	}
	opts.execute = execute
	opts = resolveCommandPaths(opts)
	switch opts.command {
	case "upload":
		return runUpload(ctx, stdout, opts)
	case "alt":
		return runAlt(ctx, stdout, opts)
	case "video-copy":
		return runVideoCopy(ctx, stdout, opts)
	case "verify":
		return runVerify(ctx, stdout, opts)
	case "sync-status":
		return runSyncStatus(ctx, stdout, opts)
	default:
		return fmt.Errorf("unsupported direct test stage: %s", opts.command)
	}
}

func TestDesiredFromCSVNormalizesMinimalOpsSheet(t *testing.T) {
	raw := []byte("序号,source图片名,target图片文件名,en,de,jp,fr\n1,old.png,new.png,English alt,Deutsch alt,日本語 alt,French alt\n2,same.png,,Same English alt,,,\n")
	rows, err := parseCSVRows(raw)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := desiredFromTable(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(manifest.Rows))
	}
	first := manifest.Rows[0]
	if !first.FilenameChanged || first.TargetFilename != "new.png" {
		t.Fatalf("filename change not detected: %+v", first)
	}
	if first.Alt != "" {
		t.Fatalf("default alt should be derived in plan stage, got %q", first.Alt)
	}
	if first.Translations["ja"] != "日本語 alt" {
		t.Fatalf("jp should normalize to ja: %+v", first.Translations)
	}
	second := manifest.Rows[1]
	if second.TargetFilename != "same.png" || second.FilenameChanged {
		t.Fatalf("empty target should equal source: %+v", second)
	}
}

func TestParseCommandUsesBatchSafeDefaultTimeout(t *testing.T) {
	opts, err := parseCommand([]string{"upload", "--plan", "plan.json"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.timeout != 30*time.Minute {
		t.Fatalf("expected 30m default timeout for batch upload, got %s", opts.timeout)
	}
}

func TestDoctorJSONReportsMissingSetupWithoutReadingSecrets(t *testing.T) {
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "")
	t.Setenv("SHOPIFY_CLIENT_ID", "")
	t.Setenv("SHOPIFY_CLIENT_SECRET", "")

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"--json", "doctor"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var report struct {
		SchemaVersion    int    `json:"schema_version"`
		Command          string `json:"command"`
		Status           string `json:"status"`
		RemoteApplyReady bool   `json:"remote_apply_ready"`
		Auth             struct {
			Available bool   `json:"available"`
			Source    string `json:"source"`
		} `json:"auth"`
		Problems   []string `json:"problems"`
		NextAction string   `json:"next_action"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor stdout must be stable JSON, got %q: %v", stdout.String(), err)
	}
	if report.SchemaVersion != 1 || report.Command != "doctor" || report.Status != "NEEDS_SETUP" {
		t.Fatalf("unexpected doctor identity: %+v", report)
	}
	if report.Auth.Available || report.Auth.Source != "missing" || report.RemoteApplyReady {
		t.Fatalf("missing auth must be reported without failing: %+v", report)
	}
	if len(report.Problems) == 0 || report.NextAction == "" {
		t.Fatalf("doctor must explain missing setup: %+v", report)
	}
}

func TestDoctorNeverPrintsCredentialValues(t *testing.T) {
	const secret = "test-admin-token-do-not-print-this-value"
	t.Setenv("SHOPIFY_ADMIN_TOKEN", secret)
	t.Setenv("SHOPIFY_CLIENT_ID", "")
	t.Setenv("SHOPIFY_CLIENT_SECRET", "")

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), secret) {
		t.Fatalf("doctor leaked an auth value: %s", stdout.String())
	}
	var report struct {
		Auth struct {
			Available bool   `json:"available"`
			Source    string `json:"source"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Auth.Available || report.Auth.Source != "admin_token_env" {
		t.Fatalf("expected redacted environment auth diagnosis, got %+v", report.Auth)
	}
}

func TestDoctorRejectsExecute(t *testing.T) {
	err := run(t.Context(), []string{"doctor", "--execute"}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "doctor 不接受 --execute") {
		t.Fatalf("doctor must reject execute, got %v", err)
	}
}

func TestDoctorRecognizesPerStoreAuthWithoutPrintingValues(t *testing.T) {
	const secret = "per-store-token-must-stay-redacted"
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "")
	t.Setenv("SHOPIFY_CLIENT_ID", "")
	t.Setenv("SHOPIFY_CLIENT_SECRET", "")
	t.Setenv("SHOPIFY_ADMIN_TOKEN_STORE_TEST", secret)
	t.Setenv("SHOPIFY_CLIENT_ID_STORE_TEST", "")
	t.Setenv("SHOPIFY_CLIENT_SECRET_STORE_TEST", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	mustWriteJSON(t, configPath, StoresConfig{Stores: []Store{{
		ID: "store-test", ShopifyStore: "example-test", PrimaryLocale: "en", Enabled: true,
	}}})

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--stores-config", configPath, "--stores", "store-test", "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), secret) {
		t.Fatalf("doctor leaked a per-store auth value: %s", stdout.String())
	}
	var report struct {
		Status           string `json:"status"`
		RemoteApplyReady bool   `json:"remote_apply_ready"`
		Config           struct {
			Valid              bool `json:"valid"`
			SelectedStoreCount int  `json:"selected_store_count"`
		} `json:"config"`
		Auth struct {
			Available bool   `json:"available"`
			Source    string `json:"source"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "READY" || !report.RemoteApplyReady || !report.Config.Valid || report.Config.SelectedStoreCount != 1 {
		t.Fatalf("per-store setup should be ready: %+v", report)
	}
	if !report.Auth.Available || report.Auth.Source != "per_store_admin_token_env" {
		t.Fatalf("unexpected per-store auth diagnosis: %+v", report.Auth)
	}
}

func TestDoctorReportsOptionalSheetCapabilityWithoutBlockingLocalInputs(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "test-token")
	t.Setenv("SHOPIFY_CLIENT_ID", "")
	t.Setenv("SHOPIFY_CLIENT_SECRET", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	mustWriteJSON(t, configPath, StoresConfig{Stores: []Store{{
		ID: "store-test", ShopifyStore: "example-test", PrimaryLocale: "en", Enabled: true,
	}}})

	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--stores-config", configPath, "--stores", "store-test", "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Status           string `json:"status"`
		RemoteApplyReady bool   `json:"remote_apply_ready"`
		Capabilities     struct {
			LocalInput struct {
				Available bool `json:"available"`
			} `json:"local_input"`
			SheetInput struct {
				Available  bool   `json:"available"`
				Dependency string `json:"dependency"`
				NextAction string `json:"next_action"`
			} `json:"sheet_input"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "READY" || !report.RemoteApplyReady || !report.Capabilities.LocalInput.Available {
		t.Fatalf("missing optional CLI must not block local-input readiness: %+v", report)
	}
	if report.Capabilities.SheetInput.Available || report.Capabilities.SheetInput.Dependency != "lark-cli" || report.Capabilities.SheetInput.NextAction == "" {
		t.Fatalf("doctor must explain the optional Sheet dependency: %+v", report.Capabilities.SheetInput)
	}
}

func TestSheetInputFailsPreflightWhenOptionalCLIIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := loadDesired(t.Context(), commandOptions{
		sheetURL:   "https://example.invalid/sheets/example-sheet",
		sheetName:  "Media",
		sheetRange: "A:Z",
	})
	if err == nil {
		t.Fatal("expected missing optional Sheet dependency to fail before planning")
	}
	if !strings.Contains(err.Error(), "NEEDS_SETUP") || !strings.Contains(err.Error(), "lark-cli") || !strings.Contains(err.Error(), "local CSV/XLSX/JSON inputs remain available") {
		t.Fatalf("unexpected Sheet preflight error: %v", err)
	}
}

func TestTargetAPIResultEchoesResolvedVersionPerStore(t *testing.T) {
	t.Setenv("SHOPIFY_API_VERSION", "")
	t.Setenv("SHOPIFY_API_VERSION_STORE_A", "2026-07")
	stores := map[string]Store{
		"store-a": {ID: "store-a", ShopifyStore: "example-a", APIVersion: "2026-04"},
		"store-b": {ID: "store-b", ShopifyStore: "example-b", APIVersion: "2026-10"},
	}
	var stdout bytes.Buffer
	err := writeTargetAPI(&stdout, []string{"store-a", "store-b"}, func(id string) Store { return stores[id] })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "target_api_versions=store-a:2026-07,store-b:2026-10") {
		t.Fatalf("machine output must echo actual versions, got %q", stdout.String())
	}
}

func TestVideoCopyDryRunWritesAuditablePlannedActions(t *testing.T) {
	setTestAdminToken(t, "store-us", "store-de")
	oldFactory := newShopifyClient
	fileSearches := 0
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(req *http.Request) (int, string) {
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(body.Query, "node(") {
				return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/Video/source","filename":"product-video.mp4","fileStatus":"READY","__typename":"Video","originalSource":{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"},"sources":[{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"}]}}}`
			}
			if !strings.Contains(body.Query, "files") {
				t.Fatalf("dry-run must only read Files, got %s", body.Query)
			}
			fileSearches++
			if fileSearches == 1 {
				return http.StatusOK, `{"data":{"files":{"nodes":[{"id":"gid://shopify/Video/source","filename":"product-video.mp4","fileStatus":"READY","__typename":"Video","originalSource":{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"},"sources":[{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"}]}]}}}`
			}
			return http.StatusOK, `{"data":{"files":{"nodes":[]}}}`
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "videos.json")
	mustWriteJSON(t, manifestPath, []string{"product-video.mp4"})
	var stdout bytes.Buffer
	err := run(t.Context(), []string{
		"video-copy",
		"--from-store", "us",
		"--stores", "de",
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--video-manifest", manifestPath,
		"--out-dir", filepath.Join(dir, "run"),
		"--no-env-file",
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "action=upload") || !strings.Contains(stdout.String(), "dry_run=true") {
		t.Fatalf("expected dry-run upload action, got:\n%s", stdout.String())
	}
	var evidence VideoCopyEvidence
	readJSONFixture(t, filepath.Join(dir, "run", "video-copy-evidence.json"), &evidence)
	if evidence.DryRun != true || len(evidence.Results) != 1 || evidence.Results[0].Action != "upload" {
		t.Fatalf("unexpected dry-run evidence: %#v", evidence)
	}
	for _, name := range []string{"video-copy-evidence.json", "video-copy-receipt.json"} {
		raw, readErr := os.ReadFile(filepath.Join(dir, "run", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(raw), "cdn.shopify.test") {
			t.Fatalf("%s must not persist source CDN URL: %s", name, raw)
		}
	}
}

func TestVideoCopyRejectsDuplicateExactFilenameCandidatesWithoutURL(t *testing.T) {
	setTestAdminToken(t, "store-us")
	client := testShopifyClient(func(req *http.Request) (int, string) {
		return http.StatusOK, `{"data":{"files":{"nodes":[{"id":"gid://shopify/Video/ready","filename":"product-video.mp4","fileStatus":"READY","__typename":"Video","sources":[{"url":"https://cdn.shopify.test/ready.mp4","mimeType":"video/mp4"}]},{"id":"gid://shopify/Video/uploaded","filename":"product-video.mp4","fileStatus":"UPLOADED","__typename":"Video","sources":[]}]}}}`
	})
	_, err := client.FindVideoByFilename(t.Context(), Store{ID: "store-us", ShopifyStore: "store-us"}, "product-video.mp4")
	if err == nil || !strings.Contains(err.Error(), "gid://shopify/Video/ready") || !strings.Contains(err.Error(), "gid://shopify/Video/uploaded") {
		t.Fatalf("expected fail-safe candidate report, got %v", err)
	}
	if strings.Contains(err.Error(), "https://") {
		t.Fatalf("candidate report must not expose URL: %v", err)
	}
}

func TestVideoCopyExecuteIsReservedBehindApplyBinding(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "videos.json")
	mustWriteJSON(t, manifestPath, []string{"product-video.mp4"})
	err := run(t.Context(), []string{
		"video-copy", "--from-store", "us", "--video-manifest", manifestPath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"), "--execute", "--no-env-file",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "不接受 --execute") {
		t.Fatalf("expected direct execute rejection, got %v", err)
	}
}

func TestVideoCopySanitizesURLFromPersistedLastError(t *testing.T) {
	record := VideoCopyRecord{LastError: sanitizeVideoCopyError(errors.New("download failed: https://cdn.shopify.test/private.mp4?token=secret"))}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cdn.shopify.test") || !strings.Contains(record.LastError, "<redacted-url>") {
		t.Fatalf("persisted last_error must redact URL, got %s", raw)
	}
}

func TestVideoCopyExecuteUploadsMissingVideoAndWritesVerifiedReceipt(t *testing.T) {
	setTestAdminToken(t, "store-us", "store-de")
	oldFactory := newShopifyClient
	fileSearches := 0
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(req *http.Request) (int, string) {
			if req.URL.Host == "cdn.shopify.test" {
				return http.StatusOK, "mp4-bytes"
			}
			if req.URL.Host == "upload.shopify.test" {
				return http.StatusCreated, "ok"
			}
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.Contains(body.Query, "node("):
				return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/Video/target","filename":"product-video.mp4","fileStatus":"READY","__typename":"Video","originalSource":{"url":"https://cdn.shopify.test/target.mp4","mimeType":"video/mp4"},"sources":[{"url":"https://cdn.shopify.test/target.mp4","mimeType":"video/mp4"}]}}}`
			case strings.Contains(body.Query, "stagedUploadsCreate"):
				input, _ := body.Variables["input"].([]any)
				first, _ := input[0].(map[string]any)
				if first["resource"] != "VIDEO" || first["mimeType"] != "video/mp4" || first["fileSize"] != "9" {
					t.Fatalf("unexpected video staged upload input: %#v", first)
				}
				return http.StatusOK, `{"data":{"stagedUploadsCreate":{"stagedTargets":[{"url":"https://upload.shopify.test/product-video.mp4","resourceUrl":"https://resource.shopify.test/product-video.mp4","parameters":[]}],"userErrors":[]}}}`
			case strings.Contains(body.Query, "fileCreate"):
				files, _ := body.Variables["files"].([]any)
				first, _ := files[0].(map[string]any)
				_, hasFilename := first["filename"]
				_, hasDuplicateMode := first["duplicateResolutionMode"]
				if first["contentType"] != "VIDEO" || hasFilename || hasDuplicateMode {
					t.Fatalf("unexpected video fileCreate input: %#v", first)
				}
				return http.StatusOK, `{"data":{"fileCreate":{"files":[{"id":"gid://shopify/Video/target","filename":"product-video.mp4","fileStatus":"UPLOADED","__typename":"Video","sources":[]}],"userErrors":[]}}}`
			case strings.Contains(body.Query, "files"):
				fileSearches++
				if fileSearches == 1 {
					return http.StatusOK, `{"data":{"files":{"nodes":[{"id":"gid://shopify/Video/source","filename":"product-video.mp4","fileStatus":"READY","__typename":"Video","originalSource":{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"},"sources":[{"url":"https://cdn.shopify.test/product-video.mp4","mimeType":"video/mp4"}]}]}}}`
				}
				return http.StatusOK, `{"data":{"files":{"nodes":[]}}}`
			default:
				t.Fatalf("unexpected graphql query: %s", body.Query)
				return http.StatusInternalServerError, `{}`
			}
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "videos.json")
	mustWriteJSON(t, manifestPath, []string{"product-video.mp4"})
	err := runDirectStageForTest(t.Context(), []string{
		"video-copy", "--from-store", "us", "--stores", "de",
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--video-manifest", manifestPath, "--out-dir", filepath.Join(dir, "run"),
		"--execute", "--no-env-file", "--max-attempts", "1", "--poll-interval", "1ns",
	}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	var receipt VideoCopyReceipt
	readJSONFixture(t, filepath.Join(dir, "run", "video-copy-receipt.json"), &receipt)
	if !receipt.Verified || len(receipt.Results) != 1 || receipt.Results[0].Action != "upload" || receipt.Results[0].FinalFileStatus != "READY" {
		t.Fatalf("unexpected execute receipt: %#v", receipt)
	}
}

func TestVideoStagedUploadRejectsMissingFileSizeBeforeGraphQL(t *testing.T) {
	_, err := (&ShopifyClient{}).CreateVideoStagedUpload(t.Context(), Store{ID: "store-de"}, ResourceInfo{MimeType: "video/mp4"}, "empty.mp4")
	if err == nil || !strings.Contains(err.Error(), "fileSize") {
		t.Fatalf("expected local fileSize validation, got %v", err)
	}
}

func TestVideoReadbackRejectsFilenameDrift(t *testing.T) {
	err := validateVideoReadbackFilename(
		Store{ID: "store-de"},
		"product-video.mp4",
		ShopifyFileNode{Filename: "product-video-uuid.mp4"},
	)
	if err == nil || !strings.Contains(err.Error(), "filename 漂移") {
		t.Fatalf("expected filename drift error, got %v", err)
	}
}

func TestDesiredFromCSVHandlesMissingOptionalColumns(t *testing.T) {
	rows, err := parseCSVRows([]byte("序号,source图片名\n1,same.png\n"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := desiredFromTable(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(manifest.Rows))
	}
	row := manifest.Rows[0]
	if row.TargetFilename != "same.png" || row.FilenameChanged {
		t.Fatalf("missing target column should default to source, got %+v", row)
	}
	if row.Alt != "" {
		t.Fatalf("missing alt column should stay empty, got %q", row.Alt)
	}
	if len(row.Translations) != 0 {
		t.Fatalf("missing locale columns should not create translations: %+v", row.Translations)
	}
}

func TestDesiredFromCSVStripsBOMFromFirstHeader(t *testing.T) {
	rows, err := parseCSVRows([]byte("\ufeffsource图片名\nsame.png\n"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := desiredFromTable(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(manifest.Rows))
	}
	row := manifest.Rows[0]
	if row.SourceFilename != "same.png" || row.TargetFilename != "same.png" || row.FilenameChanged {
		t.Fatalf("BOM-prefixed source header should parse as required column, got %+v", row)
	}
}

func TestLoadDesiredFiltersLocaleOnlyTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.csv")
	raw := []byte("序号,source图片名,target图片文件名,alt,en,de,jp,fr\n1,same.png,,Legacy Alt,EN,DE,JP,FR\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadDesired(t.Context(), commandOptions{input: path})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Rows[0].Alt != "" {
		t.Fatalf("legacy alt column should be ignored even without --locales, got %q", manifest.Rows[0].Alt)
	}
	wantAll := map[string]string{"en": "EN", "de": "DE", "ja": "JP", "fr": "FR"}
	if !reflect.DeepEqual(manifest.Rows[0].Translations, wantAll) {
		t.Fatalf("unexpected unfiltered translations\nwant=%#v\ngot=%#v", wantAll, manifest.Rows[0].Translations)
	}

	manifest, err = loadDesired(t.Context(), commandOptions{input: path, locales: "en,fr"})
	if err != nil {
		t.Fatal(err)
	}
	row := manifest.Rows[0]
	if row.Alt != "" {
		t.Fatalf("locale-only template should not set source alt during desired load, got %q", row.Alt)
	}
	want := map[string]string{"en": "EN", "fr": "FR"}
	if !reflect.DeepEqual(row.Translations, want) {
		t.Fatalf("unexpected filtered translations\nwant=%#v\ngot=%#v", want, row.Translations)
	}

	manifest, err = loadDesired(t.Context(), commandOptions{input: path, locales: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Rows[0].Alt != "" {
		t.Fatalf("--locales none should ignore legacy alt column, got %q", manifest.Rows[0].Alt)
	}
	if len(manifest.Rows[0].Translations) != 0 {
		t.Fatalf("--locales none should ignore locale columns, got %#v", manifest.Rows[0].Translations)
	}
}

func TestLoadDesiredExplicitLocalesClearLegacyAlt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.csv")
	raw := []byte("序号,source图片名,target图片文件名,alt,en,fr\n1,same.png,,Default alt,EN,FR\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadDesired(t.Context(), commandOptions{input: path})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Rows[0].Alt != "" {
		t.Fatalf("legacy alt column should be ignored without --locales, got %+v", manifest.Rows[0])
	}
	if !reflect.DeepEqual(manifest.Rows[0].Translations, map[string]string{"en": "EN", "fr": "FR"}) {
		t.Fatalf("unexpected translations without --locales: %+v", manifest.Rows[0].Translations)
	}

	manifest, err = loadDesired(t.Context(), commandOptions{input: path, locales: "en,fr"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Rows[0].Alt != "" {
		t.Fatalf("--locales en,fr should ignore legacy alt column, got %+v", manifest.Rows[0])
	}
	if !reflect.DeepEqual(manifest.Rows[0].Translations, map[string]string{"en": "EN", "fr": "FR"}) {
		t.Fatalf("unexpected translations: %+v", manifest.Rows[0].Translations)
	}

	manifest, err = loadDesired(t.Context(), commandOptions{input: path, locales: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Rows[0].Alt != "" || len(manifest.Rows[0].Translations) != 0 {
		t.Fatalf("--locales none should suppress all alt writes, got %+v", manifest.Rows[0])
	}
}

func TestLoadDesiredJSONPreservesExplicitAlt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desired.json")
	mustWriteJSON(t, path, DesiredManifest{Rows: []DesiredRow{{
		RowNo:          "1",
		SourceFilename: "same.png",
		TargetFilename: "same.png",
		Alt:            "JSON alt",
		Translations:   map[string]string{"de": "DE alt", "fr": "FR alt"},
	}}})

	manifest, err := loadDesired(t.Context(), commandOptions{input: path})
	if err != nil {
		t.Fatal(err)
	}
	row := manifest.Rows[0]
	if row.Alt != "JSON alt" {
		t.Fatalf("desired JSON should preserve explicit alt, got %+v", row)
	}
	if !reflect.DeepEqual(row.Translations, map[string]string{"de": "DE alt", "fr": "FR alt"}) {
		t.Fatalf("unexpected translations: %+v", row.Translations)
	}

	manifest, err = loadDesired(t.Context(), commandOptions{input: path, locales: "fr"})
	if err != nil {
		t.Fatal(err)
	}
	row = manifest.Rows[0]
	if row.Alt != "JSON alt" {
		t.Fatalf("--locales should not drop explicit JSON alt unless none is requested, got %+v", row)
	}
	if !reflect.DeepEqual(row.Translations, map[string]string{"fr": "FR alt"}) {
		t.Fatalf("unexpected filtered translations: %+v", row.Translations)
	}

	manifest, err = loadDesired(t.Context(), commandOptions{input: path, locales: "none"})
	if err != nil {
		t.Fatal(err)
	}
	row = manifest.Rows[0]
	if row.Alt != "" || len(row.Translations) != 0 {
		t.Fatalf("--locales none should suppress JSON alt and translations, got %+v", row)
	}
}

func TestExtractFeishuAnnotatedCSVFromJSONEnvelope(t *testing.T) {
	raw := []byte(`{
  "ok": true,
  "data": {
    "annotated_csv": " 序号,source图片名,target图片文件名,en,de,jp,fr\n 1,same.png,target.png,EN,DE,JP,FR"
  },
  "_notice": {
    "update": {
      "current": "1.0.52",
      "latest": "1.0.63"
    }
  }
}`)
	csvRaw, err := extractFeishuAnnotatedCSV(raw)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseCSVRows(csvRaw)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := desiredFromTable(rows)
	if err != nil {
		t.Fatal(err)
	}
	got := manifest.Rows[0]
	if got.SourceFilename != "same.png" || got.TargetFilename != "target.png" || got.Alt != "" || got.Translations["ja"] != "JP" {
		t.Fatalf("unexpected row from Feishu envelope: %+v", got)
	}
}

func TestExtractFeishuAnnotatedCSVRedactsEnvelopeErrors(t *testing.T) {
	sheetURL := "https://example.feishu.cn/sheets/sht-secret-token?sheet=abc"
	raw := []byte(fmt.Sprintf(`{
  "ok": false,
  "error": {
    "message": "failed request %s token=sht-secret-token",
    "type": "auth_error_sht-secret-token"
  }
}`, sheetURL))
	_, err := extractFeishuAnnotatedCSV(raw, sheetURL, "sht-secret-token")
	if err == nil {
		t.Fatal("expected envelope error")
	}
	got := err.Error()
	for _, forbidden := range []string{sheetURL, "sht-secret-token"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected envelope error to redact %q, got %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected redaction marker, got %q", got)
	}
}

func TestGoldenDesiredFromCSV(t *testing.T) {
	manifest, err := loadDesired(t.Context(), commandOptions{input: filepath.Join("testdata", "media.csv")})
	if err != nil {
		t.Fatal(err)
	}
	var expected DesiredManifest
	readJSONFixture(t, filepath.Join("testdata", "expected-desired.json"), &expected)
	if !reflect.DeepEqual(manifest, expected) {
		t.Fatalf("desired golden mismatch\nwant:\n%s\ngot:\n%s", prettyJSON(t, expected), prettyJSON(t, manifest))
	}
}

func TestGoldenPlanUsesResourceEvidenceAndJSONScope(t *testing.T) {
	desired, err := loadDesired(t.Context(), commandOptions{input: filepath.Join("testdata", "media.csv")})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := BuildResourceIndex(filepath.Join("testdata", "images"), "", filepath.Join(t.TempDir(), "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := LoadEvidence(filepath.Join("testdata", "legacy-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadStoresConfig(filepath.Join("testdata", "stores.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	stores, err := selectStores(cfg, "us")
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(PlanInput{
		RunID:       "run-golden",
		GeneratedAt: "2026-07-01T00:00:00Z",
		Stores:      stores,
		Desired:     desired,
		Resources:   resources,
		Evidence:    evidence,
		Template:    "product.example",
	})
	var expected Plan
	readJSONFixture(t, filepath.Join("testdata", "expected-plan.json"), &expected)
	if !reflect.DeepEqual(plan, expected) {
		t.Fatalf("plan golden mismatch\nwant:\n%s\ngot:\n%s", prettyJSON(t, expected), prettyJSON(t, plan))
	}
}

func TestBuildPlanUsesResourceEvidenceAndJSONScope(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "target.png")
	if err := os.WriteFile(imagePath, tinyPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	resources, err := BuildResourceIndex(dir, "", filepath.Join(dir, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredManifest{Rows: []DesiredRow{{
		RowNo:           "1",
		SourceFilename:  "new.png",
		TargetFilename:  "target.png",
		FilenameChanged: true,
		Alt:             "Alt",
		Translations:    map[string]string{"de": "Deutsch"},
	}}}
	plan := BuildPlan(PlanInput{
		RunID:       "run-test",
		Stores:      []Store{{ID: "store-us"}},
		Desired:     desired,
		Resources:   resources,
		Template:    "product.example",
		Evidence:    Evidence{},
		TargetTheme: "开发专用模板",
	})
	if len(plan.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(plan.Changes))
	}
	actions := strings.Join(plan.Changes[0].Actions, ",")
	for _, want := range []string{"file_upload_new_filename", "alt_update", "translation_update", "json_replace_needed"} {
		if !strings.Contains(actions, want) {
			t.Fatalf("missing action %s in %s", want, actions)
		}
	}
	if got := plan.Changes[0].Resource.Filename; got != "target.png" {
		t.Fatalf("expected target resource, got %q", got)
	}
	if !plan.Changes[0].NeedsExecute {
		t.Fatal("expected write actions to require execute")
	}
}

func TestBuildPlanUsesTargetResourceFromZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "media.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	addZipFile(t, writer, "target.webp", string(tinyPNG()))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	resources, err := BuildResourceIndex("", zipPath, filepath.Join(dir, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(PlanInput{
		RunID:  "run-test",
		Stores: []Store{{ID: "store-us"}},
		Desired: DesiredManifest{Rows: []DesiredRow{{
			RowNo:           "1",
			SourceFilename:  "old.png",
			TargetFilename:  "target.webp",
			FilenameChanged: true,
		}}},
		Resources: resources,
		Template:  "product.example",
	})
	change := plan.Changes[0]
	if change.Status != "planned" || strings.Join(change.Actions, ",") != "file_upload_new_filename,json_replace_needed" {
		t.Fatalf("target-only zip resource must plan upload and JSON replacement, got %+v", change)
	}
	if change.Resource == nil || change.Resource.Filename != "target.webp" {
		t.Fatalf("expected target zip resource, got %+v", change.Resource)
	}
}

func TestBuildPlanUsesStorePrimaryLocaleColumnAsAlt(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "same.png")
	if err := os.WriteFile(imagePath, tinyPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	resources, err := BuildResourceIndex(dir, "", filepath.Join(dir, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredManifest{Rows: []DesiredRow{{
		RowNo:          "1",
		SourceFilename: "same.png",
		TargetFilename: "same.png",
		Translations: map[string]string{
			"en": "EN alt",
			"de": "DE alt",
			"ja": "JA alt",
			"fr": "FR alt",
		},
	}}}
	plan := BuildPlan(PlanInput{
		RunID: "run-test",
		Stores: []Store{
			{ID: "store-us", PrimaryLocale: "en"},
			{ID: "store-de", PrimaryLocale: "de"},
			{ID: "store-jp", PrimaryLocale: "ja"},
		},
		Desired:   desired,
		Resources: resources,
	})
	got := map[string]PlanChange{}
	for _, change := range plan.Changes {
		got[change.Store] = change
	}
	cases := []struct {
		store         string
		wantAlt       string
		absentLocale  string
		presentLocale string
	}{
		{store: "store-us", wantAlt: "EN alt", absentLocale: "en", presentLocale: "fr"},
		{store: "store-de", wantAlt: "DE alt", absentLocale: "de", presentLocale: "en"},
		{store: "store-jp", wantAlt: "JA alt", absentLocale: "ja", presentLocale: "en"},
	}
	for _, tc := range cases {
		change := got[tc.store]
		if change.Alt != tc.wantAlt {
			t.Fatalf("%s expected primary locale alt %q, got %+v", tc.store, tc.wantAlt, change)
		}
		if _, ok := change.Translations[tc.absentLocale]; ok {
			t.Fatalf("%s should remove primary locale %q from translations: %+v", tc.store, tc.absentLocale, change.Translations)
		}
		if change.Translations[tc.presentLocale] == "" {
			t.Fatalf("%s should keep non-primary locale %q in translations: %+v", tc.store, tc.presentLocale, change.Translations)
		}
	}
}

func TestBuildPlanSkipsBinaryWhenSHAUnchanged(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "same.png")
	if err := os.WriteFile(imagePath, tinyPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	resources, err := BuildResourceIndex(dir, "", filepath.Join(dir, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	sha := resources.Files["same.png"].SHA256
	desired := DesiredManifest{Rows: []DesiredRow{{RowNo: "1", SourceFilename: "same.png", TargetFilename: "same.png"}}}
	plan := BuildPlan(PlanInput{
		RunID:     "run-test",
		Stores:    []Store{{ID: "store-us"}},
		Desired:   desired,
		Resources: resources,
		Evidence: Evidence{Results: []EvidenceRecord{{
			Store:          "store-us",
			RowNo:          "1",
			TargetFilename: "same.png",
			MediaGID:       "gid://shopify/MediaImage/1",
			SHA256:         sha,
		}}},
	})
	if got := strings.Join(plan.Changes[0].Actions, ","); got != "verify" {
		t.Fatalf("expected verify only, got %s", got)
	}
}

func TestBuildPlanSkipsRenamedBinaryWhenTargetEvidenceSHAUnchanged(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "target.png")
	if err := os.WriteFile(imagePath, tinyPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	resources, err := BuildResourceIndex(dir, "", filepath.Join(dir, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	sha := resources.Files["target.png"].SHA256
	desired := DesiredManifest{Rows: []DesiredRow{{
		RowNo:           "1",
		SourceFilename:  "source.png",
		TargetFilename:  "target.png",
		FilenameChanged: true,
	}}}
	plan := BuildPlan(PlanInput{
		RunID:     "run-test",
		Stores:    []Store{{ID: "store-us"}},
		Desired:   desired,
		Resources: resources,
		Evidence: Evidence{Results: []EvidenceRecord{{
			Store:          "store-us",
			RowNo:          "1",
			TargetFilename: "target.png",
			MediaGID:       "gid://shopify/MediaImage/1",
			SHA256:         sha,
		}}},
	})
	change := plan.Changes[0]
	if got := strings.Join(change.Actions, ","); got != "verify" {
		t.Fatalf("expected renamed unchanged binary to verify only, got %s", got)
	}
	if change.NeedsExecute {
		t.Fatalf("unchanged renamed binary should not need upload execute: %+v", change)
	}
}

func TestBuildPlanDoesNotReuseStaleRowNumberEvidence(t *testing.T) {
	desired := DesiredManifest{Rows: []DesiredRow{{
		RowNo:          "1",
		SourceFilename: "new.png",
		TargetFilename: "new.png",
		Alt:            "New alt",
	}}}
	plan := BuildPlan(PlanInput{
		RunID:   "run-test",
		Stores:  []Store{{ID: "store-us"}},
		Desired: desired,
		Evidence: Evidence{Results: []EvidenceRecord{{
			Store:          "store-us",
			RowNo:          "1",
			TargetFilename: "old.png",
			FileID:         "gid://shopify/MediaImage/old",
			MediaGID:       "gid://shopify/MediaImage/old",
		}}},
	})
	change := plan.Changes[0]
	if change.Status != "error" || !reflect.DeepEqual(change.Actions, []string{"error"}) {
		t.Fatalf("expected stale row-number evidence to be ignored, got %+v", change)
	}
	if change.Evidence != nil {
		t.Fatalf("stale evidence should not be attached: %+v", change.Evidence)
	}
}

func TestLoadEvidenceAcceptsLegacyCamelCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	raw := []byte(`{
  "store": "store-us",
  "results": [{
    "row_no": "1",
    "filename": "same.png",
    "requestedFilename": "source.png",
    "fileId": "gid://shopify/File/1",
    "mediaGid": "gid://shopify/MediaImage/1",
    "cdnUrl": "https://cdn.shopify.com/s/files/1/same.png",
    "sha256": "abc",
    "fileStatus": "READY"
  }]
}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	evidence, err := LoadEvidence(path)
	if err != nil {
		t.Fatal(err)
	}
	row := DesiredRow{RowNo: "1", SourceFilename: "source.png", TargetFilename: "same.png"}
	record := evidence.Find("store-us", row)
	if record == nil {
		t.Fatal("expected legacy evidence to be reusable")
	}
	if record.MediaGID == "" || record.FileID == "" || record.CDNURL == "" || record.FileStatus != "READY" {
		t.Fatalf("legacy fields not normalized: %+v", record)
	}
}

func TestEvidenceFindDoesNotMergeDifferentRowsWithSameFilename(t *testing.T) {
	evidence := Evidence{Results: []EvidenceRecord{
		{
			Store:          "store-us",
			RowNo:          "1",
			TargetFilename: "old.png",
			FileID:         "gid://shopify/MediaImage/old",
		},
		{
			Store:          "store-us",
			RowNo:          "9",
			TargetFilename: "new.png",
			FileID:         "gid://shopify/MediaImage/new",
		},
	}}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", SourceFilename: "new.png", TargetFilename: "new.png"})
	if record != nil {
		t.Fatalf("different rows must not share filename-only evidence, got %+v", record)
	}
}

func TestEvidenceUpsertKeepsSameFilenameRowsIndependent(t *testing.T) {
	evidence := Evidence{}
	evidence.Upsert(EvidenceRecord{Store: "store-a", RowNo: "1", TargetFilename: "same.png", Status: "READY"})
	evidence.Upsert(EvidenceRecord{Store: "store-a", RowNo: "2", TargetFilename: "same.png", Status: "ERROR", LastError: "row two"})
	if len(evidence.Results) != 2 {
		t.Fatalf("same filename rows collapsed: %+v", evidence.Results)
	}
	first := evidence.Find("store-a", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	second := evidence.Find("store-a", DesiredRow{RowNo: "2", TargetFilename: "same.png"})
	if first == nil || second == nil || first.Status != "READY" || second.Status != "ERROR" {
		t.Fatalf("row evidence lost independence: first=%+v second=%+v", first, second)
	}
}

func TestEvidenceUpsertRequiresFilenameWhenRowNumberReused(t *testing.T) {
	evidence := Evidence{Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "old.png",
		FileID:         "gid://shopify/MediaImage/old",
	}}}
	evidence.Upsert(EvidenceRecord{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "new.png",
		Status:         "ERROR",
		LastError:      "forced failure",
	})
	if len(evidence.Results) != 2 {
		t.Fatalf("expected stale row number to append a separate record, got %+v", evidence.Results)
	}
	oldRecord := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "old.png"})
	if oldRecord == nil || oldRecord.FileID != "gid://shopify/MediaImage/old" || oldRecord.Status == "ERROR" {
		t.Fatalf("old record should not be overwritten: %+v", oldRecord)
	}
	newRecord := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "new.png"})
	if newRecord == nil || newRecord.FileID != "" || newRecord.Status != "ERROR" || newRecord.LastError == "" {
		t.Fatalf("new record should not inherit old file id: %+v", newRecord)
	}
}

func TestEvidenceUpsertClearsLastErrorOnReadyRetry(t *testing.T) {
	evidence := Evidence{Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		Status:         "ERROR",
		LastError:      "forced failure",
	}}}
	evidence.Upsert(EvidenceRecord{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		Status:         "READY",
		LastError:      "",
		CDNURL:         "https://cdn.shopify.com/s/files/1/same.png",
	})
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil || record.Status != "READY" || record.LastError != "" {
		t.Fatalf("expected successful retry to clear LastError, got %+v", record)
	}
}

func TestRunPlanWritesArtifactsWithoutRemoteWrites(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	inputPath := filepath.Join(dir, "media.csv")
	sourceRoot := filepath.Join(dir, "images")
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteJSON(t, configPath, StoresConfig{Stores: []Store{{ID: "store-us", ShopifyStore: "store-us", Enabled: true, Label: "US"}}})
	if err := os.WriteFile(inputPath, []byte("序号,source图片名,target图片文件名,en,de,jp,fr\n1,same.png,,EN,,JP,\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "same.png"), tinyPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	err := run(t.Context(), []string{
		"plan",
		"--input", inputPath,
		"--source-root", sourceRoot,
		"--stores-config", configPath,
		"--stores", "us",
		"--out-dir", outDir,
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"desired.json", "plan.json", "evidence.json", "report.csv"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("missing artifact %s: %v", name, err)
		}
	}
	if !strings.Contains(stdout.String(), "file_upload_same_filename") {
		t.Fatalf("unexpected plan output:\n%s", stdout.String())
	}
}

func TestRunPlanResolvesRelativePathsAgainstCallerCWD(t *testing.T) {
	callerDir := t.TempDir()
	t.Setenv("MEDIA_SYNC_CALLER_CWD", callerDir)
	sourceRoot := filepath.Join(callerDir, "images")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteJSON(t, filepath.Join(callerDir, "stores.config.json"), StoresConfig{Stores: []Store{{ID: "store-us", ShopifyStore: "store-us", Enabled: true, Label: "US"}}})
	if err := os.WriteFile(filepath.Join(callerDir, "media.csv"), []byte("序号,source图片名,target图片文件名,en\n1,same.png,,EN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "same.png"), tinyPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	err := run(t.Context(), []string{
		"plan",
		"--input", "media.csv",
		"--source-root", "images",
		"--stores-config", "stores.config.json",
		"--stores", "us",
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(callerDir, ".runtime", "media-sync", "_plan")
	entries, err := os.ReadDir(runRoot)
	if err != nil {
		t.Fatalf("expected run output under caller cwd, got err=%v stdout=%s", err, stdout.String())
	}
	if len(entries) != 1 {
		t.Fatalf("expected one run directory under caller cwd, got %d", len(entries))
	}
	if _, err := os.Stat(filepath.Join(runRoot, entries[0].Name(), "plan.json")); err != nil {
		t.Fatalf("expected caller-cwd plan artifact: %v", err)
	}
}

func TestResolveRunDirUsesCollisionResistantRunID(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 3, 2, 59, 1, 123, time.UTC)
	opts := commandOptions{pathBase: dir}
	runDir, runID, err := resolveRunDirAt(opts, now)
	if err != nil {
		t.Fatal(err)
	}
	if runID != "run-20260703T025901.000000123Z" {
		t.Fatalf("unexpected run id: %s", runID)
	}
	if runDir != filepath.Join(dir, ".runtime", "media-sync", "_plan", runID) {
		t.Fatalf("unexpected run dir: %s", runDir)
	}
	nextRunID := generateRunID(now.Add(time.Nanosecond))
	if nextRunID == runID {
		t.Fatalf("run id should differ within the same second: %s", runID)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveRunDirAt(opts, now); err == nil || !strings.Contains(err.Error(), "run dir already exists") {
		t.Fatalf("expected existing default run dir to fail, got %v", err)
	}
}

func TestResolveRunDirAllowsExplicitOutDir(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "fixed")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runDir, runID, err := resolveRunDirAt(commandOptions{outDir: outDir}, time.Date(2026, 7, 3, 2, 59, 1, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if runDir != outDir || runID != "run-20260703T025901.000000000Z" {
		t.Fatalf("unexpected explicit out-dir result: runDir=%s runID=%s", runDir, runID)
	}
}

func TestRunPlanCarriesReusableStateEvidence(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	err := run(t.Context(), []string{
		"plan",
		"--input", filepath.Join("testdata", "media.csv"),
		"--source-root", filepath.Join("testdata", "images"),
		"--state", filepath.Join("testdata", "legacy-evidence.json"),
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--out-dir", outDir,
		"--template", "product.example",
	}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := LoadEvidence(filepath.Join(outDir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.svg"})
	if record == nil {
		t.Fatal("expected plan --state evidence to be carried into new evidence.json")
	}
	if record.MediaGID == "" || record.SHA256 == "" {
		t.Fatalf("carried evidence lost reusable fields: %+v", record)
	}
}

func TestReadXLSX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.xlsx")
	createMinimalXLSX(t, path)
	rows, err := ReadXLSX(path, "Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := desiredFromTable(rows)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Rows[0].SourceFilename != "same.png" || manifest.Rows[0].Translations["ja"] != "JP" {
		t.Fatalf("unexpected manifest: %+v", manifest.Rows[0])
	}
}

func TestParseCommandDefaultsConcurrencyToTwo(t *testing.T) {
	opts, err := parseCommand([]string{"upload"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.concurrency != 2 {
		t.Fatalf("expected default concurrency 2, got %d", opts.concurrency)
	}
}

func TestRunMetricsRecordsRequestedAndEffectiveConcurrency(t *testing.T) {
	changes := []PlanChange{{}, {}, {}}
	upload := newRunMetrics("upload", "run", time.Now(), nil, len(changes), changes, 2, false)
	if upload.RequestedConcurrency != 2 || upload.EffectiveConcurrency != 2 || upload.Concurrency != 2 {
		t.Fatalf("unexpected upload concurrency metrics: %+v", upload)
	}
	uploadSingle := newRunMetrics("upload", "run", time.Now(), nil, 1, changes[:1], 2, false)
	if uploadSingle.RequestedConcurrency != 2 || uploadSingle.EffectiveConcurrency != 1 || uploadSingle.Concurrency != 1 {
		t.Fatalf("expected one-change upload to use one worker: %+v", uploadSingle)
	}
	alt := newRunMetrics("alt", "run", time.Now(), nil, len(changes), changes, 3, false)
	verify := newRunMetrics("verify", "run", time.Now(), nil, len(changes), changes, 3, false)
	if alt.RequestedConcurrency != 3 || alt.EffectiveConcurrency != 1 || verify.EffectiveConcurrency != 1 {
		t.Fatalf("alt/verify must be serial: alt=%+v verify=%+v", alt, verify)
	}
}

func TestWriteMetricsPreservesPerCommandReceipts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")
	upload := newRunMetrics("upload", "run", time.Now(), nil, 1, []PlanChange{{}}, 2, false)
	if err := writeMetricsFile(path, &upload); err != nil {
		t.Fatal(err)
	}
	alt := newRunMetrics("alt", "run", time.Now(), nil, 1, []PlanChange{{}}, 2, false)
	if err := writeMetricsFile(path, &alt); err != nil {
		t.Fatal(err)
	}
	var legacy, uploadReceipt, altReceipt RunMetrics
	readJSONFixture(t, path, &legacy)
	readJSONFixture(t, filepath.Join(dir, "metrics.upload.json"), &uploadReceipt)
	readJSONFixture(t, filepath.Join(dir, "metrics.alt.json"), &altReceipt)
	if legacy.Command != "alt" || uploadReceipt.Command != "upload" || altReceipt.Command != "alt" {
		t.Fatalf("expected compatibility and per-command metrics: legacy=%+v upload=%+v alt=%+v", legacy, uploadReceipt, altReceipt)
	}
}

func TestExecutionLeaseRejectsConcurrentCheckpointBeforeReadingPlan(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "missing-plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	lease, err := acquireExecutionLease(commandOptions{command: "alt", planPath: planPath, evidencePath: evidencePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if releaseErr := lease.release(); releaseErr != nil {
			t.Error(releaseErr)
		}
	})
	err = run(t.Context(), []string{"upload", "--plan", planPath, "--evidence", evidencePath, "--no-env-file"}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "正被其他 upload/alt/verify 命令使用") || !strings.Contains(err.Error(), "等待当前命令完成") {
		t.Fatalf("expected checkpoint lease rejection, got %v", err)
	}
	for _, path := range []string{evidencePath, filepath.Join(dir, "metrics.json"), filepath.Join(dir, "metrics.upload.json")} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("lease rejection must not write %s, stat err=%v", path, statErr)
		}
	}
}

func TestExecutionLeaseRejectsEvidenceSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	realEvidencePath := filepath.Join(dir, "evidence.json")
	aliasEvidencePath := filepath.Join(dir, "evidence-alias.json")
	if err := os.WriteFile(realEvidencePath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realEvidencePath, aliasEvidencePath); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireExecutionLease(commandOptions{command: "upload", planPath: planPath, evidencePath: realEvidencePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if releaseErr := lease.release(); releaseErr != nil {
			t.Error(releaseErr)
		}
	})
	_, err = acquireExecutionLease(commandOptions{command: "verify", planPath: planPath, evidencePath: aliasEvidencePath})
	if err == nil || !strings.Contains(err.Error(), "正被其他 upload/alt/verify 命令使用") || !strings.Contains(err.Error(), "等待当前命令完成") {
		t.Fatalf("expected symlink checkpoint lease rejection, got %v", err)
	}
}

func TestExecutionLeaseRejectsSamePlanWithDifferentEvidence(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	mustWriteJSON(t, planPath, Plan{RunID: "run-test"})
	first, err := acquireExecutionLease(commandOptions{command: "apply", planPath: planPath, evidencePath: filepath.Join(dir, "evidence-a.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.release() })
	_, err = acquireExecutionLease(commandOptions{command: "apply", planPath: planPath, evidencePath: filepath.Join(dir, "evidence-b.json")})
	if err == nil || !strings.Contains(err.Error(), "execution lease") {
		t.Fatalf("same plan bypassed lease through a different evidence path: %v", err)
	}
}

func TestWriteJSONAtomicallyReplacesAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")
	if err := os.WriteFile(path, []byte("{\"old\":true}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, map[string]any{"new": true}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]bool
	readJSONFixture(t, path, &decoded)
	if !decoded["new"] || len(decoded) != 1 {
		t.Fatalf("unexpected atomically written JSON: %#v", decoded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("atomic replacement should preserve existing permissions, got %o", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".evidence.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary JSON files must be cleaned up: %v", matches)
	}
}

func TestValidateUploadConcurrencyBounds(t *testing.T) {
	opts, err := parseCommand([]string{"upload", "--concurrency", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err == nil || !strings.Contains(err.Error(), "只支持 1-3") {
		t.Fatalf("expected upload concurrency upper bound error, got %v", err)
	}
	opts, err = parseCommand([]string{"apply", "--plan", "plan.json", "--concurrency", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err == nil || !strings.Contains(err.Error(), "只支持 1-3") {
		t.Fatalf("expected apply concurrency upper bound error, got %v", err)
	}
	opts, err = parseCommand([]string{"alt", "--concurrency", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err != nil {
		t.Fatalf("non-upload commands should accept inert concurrency flag, got %v", err)
	}
	opts, err = parseCommand([]string{"upload", "--concurrency", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err == nil || !strings.Contains(err.Error(), ">= 1") {
		t.Fatalf("expected upload concurrency lower bound error, got %v", err)
	}
}

func TestDirectCommandsCannotExecuteRemoteWrites(t *testing.T) {
	cases := [][]string{
		{"upload", "--execute"},
		{"alt", "--execute"},
		{"video-copy", "--from-store", "store-us", "--video-manifest", "videos.json", "--stores", "store-de", "--execute"},
	}
	for _, args := range cases {
		opts, err := parseCommand(args)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateCommandOptions(opts); err == nil || !strings.Contains(err.Error(), "apply") {
			t.Fatalf("direct command %q accepted remote execution: %v", args[0], err)
		}
	}
}

func TestAllCommandsRejectImplicitResumeLast(t *testing.T) {
	opts, err := parseCommand([]string{"upload", "--resume", "last"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("implicit resume was accepted: %v", err)
	}
}

func TestUploadCheckpointFailureStartsNoWorkers(t *testing.T) {
	setTestAdminToken(t, "store-us")
	dir := t.TempDir()
	resourcePath := filepath.Join(dir, "same.svg")
	if err := os.WriteFile(resourcePath, []byte("<svg></svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource, err := inspectResource(resourcePath)
	if err != nil {
		t.Fatal(err)
	}
	changes := []PlanChange{
		{Store: "store-us", RowNo: "1", SourceFilename: "same.svg", TargetFilename: "one.svg", Actions: []string{"file_upload_same_filename"}, Resource: &resource},
		{Store: "store-us", RowNo: "2", SourceFilename: "same.svg", TargetFilename: "two.svg", Actions: []string{"file_upload_same_filename"}, Resource: &resource},
	}
	oldPersist := persistUploadEvidence
	persistUploadEvidence = func(path string, value any) error {
		evidence, ok := value.(*Evidence)
		if ok && len(evidence.Results) == len(changes) {
			return errors.New("forced checkpoint failure")
		}
		return writeJSON(path, value)
	}
	t.Cleanup(func() { persistUploadEvidence = oldPersist })
	remoteCall := make(chan struct{}, 1)
	client := testShopifyClient(func(*http.Request) (int, string) {
		select {
		case remoteCall <- struct{}{}:
		default:
		}
		return http.StatusInternalServerError, `{}`
	})
	evidence := Evidence{RunID: "run-checkpoint"}
	err = executeUploadChanges(
		t.Context(),
		ioDiscard{},
		commandOptions{concurrency: 1},
		changes,
		func(string) Store { return Store{ID: "store-us", ShopifyStore: "store-us"} },
		&evidence,
		filepath.Join(dir, "evidence.json"),
		"REPLACE",
		client,
	)
	if err == nil || !strings.Contains(err.Error(), "forced checkpoint failure") {
		t.Fatalf("expected checkpoint failure, got %v", err)
	}
	select {
	case <-remoteCall:
		t.Fatal("remote worker started before all upload checkpoints were durable")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestUploadResultCheckpointFailureWaitsForWorkers(t *testing.T) {
	setTestAdminToken(t, "store-us")
	dir := t.TempDir()
	resourcePath := filepath.Join(dir, "same.svg")
	if err := os.WriteFile(resourcePath, []byte("<svg></svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource, err := inspectResource(resourcePath)
	if err != nil {
		t.Fatal(err)
	}
	changes := []PlanChange{
		{Store: "store-us", RowNo: "1", TargetFilename: "missing.svg", Actions: []string{"file_upload_same_filename"}},
		{Store: "store-us", RowNo: "2", TargetFilename: "same.svg", Actions: []string{"file_upload_same_filename"}, Resource: &resource},
	}
	remoteStarted := make(chan struct{})
	remoteStopped := make(chan struct{})
	var startedOnce sync.Once
	var stoppedOnce sync.Once
	client := testShopifyClient(func(req *http.Request) (int, string) {
		startedOnce.Do(func() { close(remoteStarted) })
		<-req.Context().Done()
		stoppedOnce.Do(func() { close(remoteStopped) })
		return 499, `{}`
	})
	oldClientFactory := newShopifyClient
	newShopifyClient = func(*http.Client) *ShopifyClient { return client }
	t.Cleanup(func() { newShopifyClient = oldClientFactory })
	persistCalls := 0
	oldPersist := persistUploadEvidence
	persistUploadEvidence = func(path string, value any) error {
		persistCalls++
		if persistCalls == 1 {
			return writeJSON(path, value)
		}
		<-remoteStarted
		return errors.New("forced result checkpoint failure")
	}
	t.Cleanup(func() { persistUploadEvidence = oldPersist })
	evidence := Evidence{RunID: "run-result-checkpoint"}
	err = executeUploadChanges(
		t.Context(),
		ioDiscard{},
		commandOptions{concurrency: 2},
		changes,
		func(string) Store { return Store{ID: "store-us", ShopifyStore: "store-us"} },
		&evidence,
		filepath.Join(dir, "evidence.json"),
		"REPLACE",
		client,
	)
	if err == nil || !strings.Contains(err.Error(), "forced result checkpoint failure") {
		t.Fatalf("expected result checkpoint failure, got %v", err)
	}
	select {
	case <-remoteStopped:
	default:
		t.Fatal("executeUploadChanges returned before the in-flight worker stopped")
	}
}

func TestUploadResultCheckpointFailureRetriesFinalEvidenceSnapshot(t *testing.T) {
	dir := t.TempDir()
	change := PlanChange{
		Store:          "store-us",
		RowNo:          "1",
		SourceFilename: "missing.svg",
		TargetFilename: "missing.svg",
		Actions:        []string{"file_upload_same_filename"},
	}
	evidencePath := filepath.Join(dir, "evidence.json")
	persistCalls := 0
	oldPersist := persistUploadEvidence
	persistUploadEvidence = func(path string, value any) error {
		persistCalls++
		if persistCalls == 2 {
			return errors.New("transient result checkpoint failure")
		}
		return writeJSON(path, value)
	}
	t.Cleanup(func() { persistUploadEvidence = oldPersist })
	evidence := Evidence{RunID: "run-final-evidence-retry"}
	err := executeUploadChanges(
		t.Context(),
		ioDiscard{},
		commandOptions{concurrency: 1},
		[]PlanChange{change},
		func(string) Store { return Store{ID: "store-us", ShopifyStore: "store-us"} },
		&evidence,
		evidencePath,
		"REPLACE",
		testShopifyClient(func(*http.Request) (int, string) {
			t.Error("resource validation failure must not call Shopify")
			return http.StatusInternalServerError, `{}`
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "transient result checkpoint failure") {
		t.Fatalf("expected transient checkpoint failure, got %v", err)
	}
	if persistCalls != 3 {
		t.Fatalf("expected initial, failed result, and final snapshot writes; got %d", persistCalls)
	}
	persisted, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := persisted.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "missing.svg"})
	if record == nil || record.Upload.Status != stageStatusFailed || record.Upload.LastError == "" {
		t.Fatalf("final evidence snapshot did not preserve the drained result: %+v", record)
	}
}

func TestUploadCancellationPersistsAcceptedMutationBeforeReturning(t *testing.T) {
	setTestAdminToken(t, "store-us")
	dir := t.TempDir()
	resourcePath := filepath.Join(dir, "same.svg")
	if err := os.WriteFile(resourcePath, []byte("<svg></svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource, err := inspectResource(resourcePath)
	if err != nil {
		t.Fatal(err)
	}
	changes := []PlanChange{
		{Store: "store-us", RowNo: "1", SourceFilename: "same.svg", TargetFilename: "one.svg", Actions: []string{"file_upload_same_filename"}, Resource: &resource},
		{Store: "store-us", RowNo: "2", SourceFilename: "same.svg", TargetFilename: "two.svg", Actions: []string{"file_upload_same_filename"}, Resource: &resource},
	}
	pollStarted := make(chan struct{})
	var pollOnce sync.Once
	client := testShopifyClient(func(req *http.Request) (int, string) {
		if req.URL.Host == "upload.shopify.test" {
			if _, err := io.Copy(io.Discard, req.Body); err != nil {
				t.Errorf("read staged upload: %v", err)
			}
			return http.StatusCreated, "ok"
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read GraphQL request: %v", err)
			return http.StatusInternalServerError, `{"errors":[{"message":"request read failed"}]}`
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("parse GraphQL request: %v", err)
			return http.StatusInternalServerError, `{"errors":[{"message":"request parse failed"}]}`
		}
		switch {
		case strings.Contains(body.Query, "stagedUploadsCreate"):
			filename := filenameFromGraphQLVariables(body.Variables, "input")
			return http.StatusOK, fmt.Sprintf(`{"data":{"stagedUploadsCreate":{"stagedTargets":[{"url":"https://upload.shopify.test/%s","resourceUrl":"https://resource.shopify.test/%s","parameters":[]}],"userErrors":[]}}}`, filename, filename)
		case strings.Contains(body.Query, "fileCreate"):
			return http.StatusOK, `{"data":{"fileCreate":{"files":[{"id":"gid://shopify/MediaImage/one","fileStatus":"UPLOADED","image":null}],"userErrors":[]}}}`
		case strings.Contains(body.Query, "node"):
			pollOnce.Do(func() { close(pollStarted) })
			<-req.Context().Done()
			return 499, `{"errors":[{"message":"cancelled"}]}`
		default:
			return http.StatusInternalServerError, `{"errors":[{"message":"unexpected query"}]}`
		}
	})
	evidencePath := filepath.Join(dir, "evidence.json")
	evidence := Evidence{RunID: "run-cancel-drain"}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- executeUploadChanges(
			ctx,
			ioDiscard{},
			commandOptions{concurrency: 1, maxAttempts: 2, pollInterval: time.Nanosecond},
			changes,
			func(string) Store { return Store{ID: "store-us", ShopifyStore: "store-us"} },
			&evidence,
			evidencePath,
			"REPLACE",
			client,
		)
	}()
	select {
	case <-pollStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first upload did not reach readback")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executeUploadChanges did not return after cancellation")
	}

	persisted, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := persisted.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "one.svg"})
	if record == nil {
		t.Fatal("accepted mutation result was not persisted")
	}
	if record.FileID != "gid://shopify/MediaImage/one" || record.Upload.Status != stageStatusAwaitingReadback || !record.Upload.MutationAccepted {
		t.Fatalf("accepted mutation checkpoint was lost: %+v", record)
	}
}

func TestValidatePlanRejectsMixedResourceSources(t *testing.T) {
	cases := [][]string{
		{"--zip", "old.zip", "--source-root", "images"},
		{"--zip", "old.zip", "--drive-file-token", "drive-file"},
		{"--source-root", "images", "--drive-folder-token", "drive-folder"},
		{"--drive-file-token", "drive-file", "--drive-folder-token", "drive-folder"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmdArgs := append([]string{"plan", "--input", "media.csv"}, args...)
			opts, err := parseCommand(cmdArgs)
			if err != nil {
				t.Fatal(err)
			}
			err = validateCommandOptions(opts)
			if err == nil || !strings.Contains(err.Error(), "资源包来源只能指定一种") {
				t.Fatalf("expected mixed resource source error, got %v", err)
			}
		})
	}

	opts, err := parseCommand([]string{"plan", "--input", "media.csv"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err != nil {
		t.Fatalf("plan without resource package should stay valid for alt/state-only flows, got %v", err)
	}
}

func TestValidateLocalesFlag(t *testing.T) {
	opts, err := parseCommand([]string{"plan", "--input", "media.csv", "--locales", "en,jp,fr"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err != nil {
		t.Fatalf("expected en,jp,fr to be valid, got %v", err)
	}
	filter, err := parseLocaleFilter(opts.locales)
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"en", "ja", "fr"} {
		if !filter.allowed[locale] {
			t.Fatalf("expected locale %s to be allowed: %+v", locale, filter.allowed)
		}
	}
	opts, err = parseCommand([]string{"plan", "--input", "media.csv", "--locales", "en,none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err == nil || !strings.Contains(err.Error(), "none") {
		t.Fatalf("expected none mixing error, got %v", err)
	}
	opts, err = parseCommand([]string{"plan", "--input", "media.csv", "--locales", "es"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandOptions(opts); err == nil || !strings.Contains(err.Error(), "不支持的 locale") {
		t.Fatalf("expected unsupported locale error, got %v", err)
	}
}

func TestUploadDryRunDoesNotExecuteRemoteWrites(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us"},
			Rows:     1,
			Changes:  1,
			ByAction: map[string]int{"file_upload_same_filename": 1},
		},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"file_upload_same_filename"},
			Resource:       &ResourceInfo{Filename: "same.png", SHA256: "abc"},
			NeedsExecute:   true,
		}},
	})
	var stdout bytes.Buffer
	calledClientFactory := false
	oldFactory := newShopifyClient
	newShopifyClient = func(httpClient *http.Client) *ShopifyClient {
		calledClientFactory = true
		return oldFactory(httpClient)
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })
	if err := run(t.Context(), []string{"upload", "--plan", planPath, "--evidence", evidencePath, "--no-env-file"}, &stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, want := range []string{"dry_run=true", "matching_changes=1", "rerun_with=--execute"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in output:\n%s", want, output)
		}
	}
	if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write evidence, stat err=%v", err)
	}
	if calledClientFactory {
		t.Fatal("dry-run should not create Shopify client")
	}
}

func TestUploadDryRunWritesMetricsButNotEvidence(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us"},
			Rows:     1,
			Changes:  1,
			ByAction: map[string]int{"file_upload_same_filename": 1},
		},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"file_upload_same_filename", "alt_update", "translation_update"},
			Status:         "planned",
			Resource:       &ResourceInfo{Filename: "same.png", SHA256: "abc"},
			NeedsExecute:   true,
		}},
	})
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"upload", "--plan", planPath, "--evidence", evidencePath, "--no-env-file"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write evidence, stat err=%v", err)
	}
	metricsPath := filepath.Join(dir, "metrics.json")
	var metrics RunMetrics
	readJSONFixture(t, metricsPath, &metrics)
	if metrics.Command != "upload" || !metrics.DryRun || metrics.Concurrency != 1 || metrics.RequestedConcurrency != 2 || metrics.EffectiveConcurrency != 1 || metrics.Rows != 1 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
	var stageMetrics RunMetrics
	readJSONFixture(t, filepath.Join(dir, "metrics.upload.json"), &stageMetrics)
	if !reflect.DeepEqual(stageMetrics, metrics) {
		t.Fatalf("stage metrics should match compatibility metrics: stage=%+v legacy=%+v", stageMetrics, metrics)
	}
	if metrics.Actions["file_upload_same_filename"] != 1 || metrics.Statuses["planned"] != 1 {
		t.Fatalf("unexpected metrics counts: %+v", metrics)
	}
	if metrics.Actions["alt_update"] != 0 || metrics.Actions["translation_update"] != 0 {
		t.Fatalf("upload metrics should count upload-stage actions only: %+v", metrics.Actions)
	}
	if !strings.Contains(stdout.String(), "metrics="+metricsPath) {
		t.Fatalf("expected metrics path in stdout, got:\n%s", stdout.String())
	}
}

func TestArchivedDevelopmentStoreDefaultsShopifyStoreToID(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	mustWriteJSON(t, configPath, StoresConfig{Archived: []Store{{
		ID:       "store-development",
		Label:    "Development",
		Archived: true,
	}}})
	cfg, err := loadStoresConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := selectStores(cfg, "development")
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 {
		t.Fatalf("expected one store, got %d", len(stores))
	}
	if stores[0].ShopifyStore != "store-development" || !stores[0].Archived {
		t.Fatalf("expected archived development store to fallback shopifyStore to id, got %+v", stores[0])
	}
}

func TestUploadDryRunAllowsArchivedDevelopmentStoreWithoutShopifyStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	missingEnvPath := filepath.Join(dir, "missing.env.local")
	mustWriteJSON(t, configPath, StoresConfig{Archived: []Store{{
		ID:    "store-development",
		Label: "Development",
	}}})
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-development"},
			Rows:     1,
			Changes:  1,
			ByAction: map[string]int{"file_upload_same_filename": 1},
		},
		Changes: []PlanChange{{
			Store:          "store-development",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"file_upload_same_filename"},
			Resource:       &ResourceInfo{Filename: "same.png"},
			NeedsExecute:   true,
		}},
	})
	var stdout bytes.Buffer
	err := run(t.Context(), []string{
		"upload",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", configPath,
		"--stores", "development",
		"--env-file", missingEnvPath,
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "dry_run=true") || !strings.Contains(output, "target_stores=store-development") {
		t.Fatalf("expected development dry-run target, got:\n%s", output)
	}
	if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write evidence, stat err=%v", err)
	}
}

func TestRemoteCommandsRejectArchivedDevelopmentStoreWithoutExplicitShopifyStore(t *testing.T) {
	cases := []struct {
		name    string
		command string
		execute bool
		actions []string
	}{
		{name: "upload", command: "upload", execute: true, actions: []string{"file_upload_same_filename"}},
		{name: "alt", command: "alt", execute: true, actions: []string{"alt_update"}},
		{name: "verify", command: "verify", actions: []string{"verify"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "stores.config.json")
			planPath := filepath.Join(dir, "plan.json")
			evidencePath := filepath.Join(dir, "evidence.json")
			missingEnvPath := filepath.Join(dir, "missing.env.local")
			mustWriteJSON(t, configPath, StoresConfig{Archived: []Store{{
				ID:    "store-development",
				Label: "Development",
			}}})
			mustWriteJSON(t, planPath, Plan{
				RunID: "run-test",
				Summary: PlanSummary{
					Stores:   []string{"store-development"},
					Rows:     1,
					Changes:  1,
					ByAction: map[string]int{tc.actions[0]: 1},
				},
				Changes: []PlanChange{{
					Store:          "store-development",
					RowNo:          "1",
					SourceFilename: "same.png",
					TargetFilename: "same.png",
					Actions:        tc.actions,
					Status:         "planned",
					Alt:            "Alt",
					Resource:       &ResourceInfo{Filename: "same.png"},
					NeedsExecute:   tc.command != "verify",
				}},
			})
			mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
				Store:          "store-development",
				RowNo:          "1",
				TargetFilename: "same.png",
				FileID:         "gid://shopify/MediaImage/1",
			}}})

			args := []string{
				tc.command,
				"--plan", planPath,
				"--evidence", evidencePath,
				"--stores-config", configPath,
				"--stores", "development",
				"--env-file", missingEnvPath,
			}
			if tc.execute {
				args = append(args, "--execute")
			}
			err := runDirectStageForTest(t.Context(), args, ioDiscard{})
			if err == nil {
				t.Fatal("expected archived store target guard error")
			}
			got := err.Error()
			if !strings.Contains(got, "缺少显式 shopifyStore") || !strings.Contains(got, "store-development") {
				t.Fatalf("expected archived store target guard, got %v", err)
			}
			if strings.Contains(got, "missing.env.local") {
				t.Fatalf("target guard should run before env loading, got %v", err)
			}
		})
	}
}

func TestRemoteCommandsRejectActiveDevelopmentStoreWithoutExplicitShopifyStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	missingEnvPath := filepath.Join(dir, "missing.env.local")
	mustWriteJSON(t, configPath, StoresConfig{Stores: []Store{{
		ID:      "store-development",
		Label:   "Development",
		Enabled: true,
	}}})
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-development"},
			Rows:     1,
			Changes:  1,
			ByAction: map[string]int{"verify": 1},
		},
		Changes: []PlanChange{{
			Store:          "store-development",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"verify"},
			Status:         "planned",
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-development",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/1",
	}}})

	err := run(t.Context(), []string{
		"verify",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", configPath,
		"--stores", "development",
		"--env-file", missingEnvPath,
	}, ioDiscard{})
	if err == nil {
		t.Fatal("expected active development store target guard error")
	}
	got := err.Error()
	if !strings.Contains(got, "缺少显式 shopifyStore") || !strings.Contains(got, "store-development") {
		t.Fatalf("expected development store target guard, got %v", err)
	}
	if strings.Contains(got, "missing.env.local") {
		t.Fatalf("target guard should run before env loading, got %v", err)
	}
}

func TestRemoteCommandsRejectUnknownDevelopmentPlanStore(t *testing.T) {
	cases := []struct {
		command string
		actions []string
		execute bool
	}{
		{command: "upload", actions: []string{"file_upload_same_filename"}, execute: true},
		{command: "alt", actions: []string{"alt_update"}, execute: true},
		{command: "verify", actions: []string{"verify"}},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			dir := t.TempDir()
			planPath := filepath.Join(dir, "plan.json")
			evidencePath := filepath.Join(dir, "evidence.json")
			missingEnvPath := filepath.Join(dir, "missing.env.local")
			mustWriteJSON(t, planPath, Plan{
				RunID: "run-test",
				Summary: PlanSummary{
					Stores:   []string{"development-shadow"},
					Rows:     1,
					Changes:  1,
					ByAction: map[string]int{tc.actions[0]: 1},
				},
				Changes: []PlanChange{{
					Store:          "development-shadow",
					RowNo:          "1",
					SourceFilename: "same.png",
					TargetFilename: "same.png",
					Actions:        tc.actions,
					Status:         "planned",
					Alt:            "Alt",
					Resource:       &ResourceInfo{Filename: "same.png", SHA256: "new-sha"},
					NeedsExecute:   tc.command == "upload",
				}},
			})
			mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
				Store:          "development-shadow",
				RowNo:          "1",
				TargetFilename: "same.png",
				FileID:         "gid://shopify/MediaImage/1",
			}}})

			args := []string{
				tc.command,
				"--plan", planPath,
				"--evidence", evidencePath,
				"--stores-config", filepath.Join("testdata", "stores.config.json"),
				"--stores", "development-shadow",
				"--env-file", missingEnvPath,
			}
			if tc.execute {
				args = append(args, "--execute")
			}
			err := runDirectStageForTest(t.Context(), args, ioDiscard{})
			if err == nil {
				t.Fatal("expected unknown development store target guard error")
			}
			got := err.Error()
			if !strings.Contains(got, "缺少显式 shopifyStore") || !strings.Contains(got, "development-shadow") {
				t.Fatalf("expected unknown development store target guard, got %v", err)
			}
			if strings.Contains(got, "missing.env.local") {
				t.Fatalf("target guard should run before env loading, got %v", err)
			}
		})
	}
}

func TestRemoteCommandsRejectUnknownNonDevelopmentPlanStoreBeforeEnv(t *testing.T) {
	cases := []struct {
		command string
		actions []string
		execute bool
	}{
		{command: "upload", actions: []string{"file_upload_same_filename"}, execute: true},
		{command: "alt", actions: []string{"alt_update"}, execute: true},
		{command: "verify", actions: []string{"verify"}},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			dir := t.TempDir()
			planPath := filepath.Join(dir, "plan.json")
			evidencePath := filepath.Join(dir, "evidence.json")
			missingEnvPath := filepath.Join(dir, "missing.env.local")
			mustWriteJSON(t, planPath, Plan{
				RunID: "run-test",
				Summary: PlanSummary{
					Stores:   []string{"store-us1"},
					Rows:     1,
					Changes:  1,
					ByAction: map[string]int{tc.actions[0]: 1},
				},
				Changes: []PlanChange{{
					Store:          "store-us1",
					RowNo:          "1",
					SourceFilename: "same.png",
					TargetFilename: "same.png",
					Actions:        tc.actions,
					Status:         "planned",
					Alt:            "Alt",
					Resource:       &ResourceInfo{Filename: "same.png", SHA256: "new-sha"},
					NeedsExecute:   tc.command == "upload",
				}},
			})
			mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
				Store:          "store-us1",
				RowNo:          "1",
				TargetFilename: "same.png",
				FileID:         "gid://shopify/MediaImage/1",
			}}})

			calledClientFactory := false
			oldFactory := newShopifyClient
			newShopifyClient = func(httpClient *http.Client) *ShopifyClient {
				calledClientFactory = true
				return oldFactory(httpClient)
			}
			t.Cleanup(func() { newShopifyClient = oldFactory })

			args := []string{
				tc.command,
				"--plan", planPath,
				"--evidence", evidencePath,
				"--stores-config", filepath.Join("testdata", "stores.config.json"),
				"--stores", "store-us1",
				"--env-file", missingEnvPath,
			}
			if tc.execute {
				args = append(args, "--execute")
			}
			err := runDirectStageForTest(t.Context(), args, ioDiscard{})
			if err == nil {
				t.Fatal("expected unknown store target guard error")
			}
			got := err.Error()
			if !strings.Contains(got, "未知 store ID") || !strings.Contains(got, "store-us1") {
				t.Fatalf("expected unknown store target guard, got %v", err)
			}
			if strings.Contains(got, "missing.env.local") {
				t.Fatalf("target guard should run before env loading, got %v", err)
			}
			if calledClientFactory {
				t.Fatal("target guard should run before Shopify client creation")
			}
		})
	}
}

func TestValidateRemoteStoreTargetsAllowsArchivedStoreWithExplicitShopifyStore(t *testing.T) {
	resolve := func(id string) Store {
		return Store{
			ID:           id,
			ShopifyStore: "store-development",
			Archived:     true,
		}
	}
	knownStoreIDs := map[string]bool{"store-development": true}
	if err := validateRemoteStoreTargets([]string{"store-development"}, resolve, knownStoreIDs); err != nil {
		t.Fatalf("explicit archived shopifyStore should be allowed, got %v", err)
	}
}

func TestValidateRemoteStoreTargetsRejectsDisabledSourceStore(t *testing.T) {
	resolve := func(id string) Store {
		return Store{
			ID:           id,
			ShopifyStore: id,
			Enabled:      false,
		}
	}
	knownStoreIDs := map[string]bool{"source-us": true}
	err := validateRemoteStoreTargets([]string{"source-us"}, resolve, knownStoreIDs)
	if err == nil || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "source-us") {
		t.Fatalf("disabled source store must be rejected before remote execution, got %v", err)
	}
}

func TestRedactSensitiveOutputHidesDriveTokens(t *testing.T) {
	raw := "failed request --file-token fld-secret-token and path downloads/fld-secret-token"
	got := redactSensitiveOutput(raw, "fld-secret-token")
	if strings.Contains(got, "fld-secret-token") {
		t.Fatalf("expected token to be redacted, got %q", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected redaction marker, got %q", got)
	}
}

func TestReadFeishuCSVRedactsSheetSecretsFromLarkErrors(t *testing.T) {
	cases := []struct {
		name      string
		opts      commandOptions
		stderr    string
		forbidden []string
	}{
		{
			name: "sheet url",
			opts: commandOptions{
				sheetURL:   "https://example.feishu.cn/sheets/sht-secret-token?sheet=abc",
				sheetName:  "图片",
				sheetRange: "A:Z",
			},
			stderr: "failed request https://example.feishu.cn/sheets/sht-secret-token?sheet=abc token=sht-secret-token",
			forbidden: []string{
				"https://example.feishu.cn/sheets/sht-secret-token?sheet=abc",
				"sht-secret-token",
			},
		},
		{
			name: "spreadsheet token",
			opts: commandOptions{
				spreadsheetToken: "spread-secret-token",
				sheetID:          "sheet-id",
				sheetRange:       "A:Z",
			},
			stderr:    "failed request --spreadsheet-token spread-secret-token",
			forbidden: []string{"spread-secret-token"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFakeLarkCLI(t, tc.stderr)
			_, err := readFeishuCSV(t.Context(), tc.opts)
			if err == nil {
				t.Fatal("expected lark-cli failure")
			}
			got := err.Error()
			for _, value := range tc.forbidden {
				if strings.Contains(got, value) {
					t.Fatalf("expected %q to be redacted from %q", value, got)
				}
			}
			if !strings.Contains(got, "<redacted>") {
				t.Fatalf("expected redaction marker, got %q", got)
			}
		})
	}
}

func TestActiveStoreKeepsDefaultShopifyStoreFallback(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	mustWriteJSON(t, configPath, StoresConfig{Stores: []Store{{
		ID:      "store-us",
		Label:   "US",
		Enabled: true,
	}}})
	cfg, err := loadStoresConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := selectStores(cfg, "us")
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 {
		t.Fatalf("expected one store, got %d", len(stores))
	}
	if stores[0].ShopifyStore != "store-us" {
		t.Fatalf("expected active store to default shopifyStore to id, got %+v", stores[0])
	}
}

func TestDisabledSourceStoreIsExplicitlySelectableButExcludedFromAll(t *testing.T) {
	cfg := StoresConfig{Stores: []Store{
		{ID: "source-us", ShopifyStore: "source-us", Enabled: false},
		{ID: "target-uk", ShopifyStore: "target-uk", Enabled: true},
	}}
	explicit, err := selectStores(cfg, "source-us")
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit) != 1 || explicit[0].ID != "source-us" {
		t.Fatalf("disabled source must remain explicitly selectable: %+v", explicit)
	}
	all, err := selectStores(cfg, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "target-uk" {
		t.Fatalf("all must exclude disabled source stores: %+v", all)
	}
}

func TestStoreNormalizeUsesExplicitPrimaryLocalesWithoutImplicitInference(t *testing.T) {
	stores := []Store{
		{ID: "store-us"},
		{ID: "store-de"},
		{ID: "store-jp"},
		{ID: "store-development"},
		{ID: "custom-store", DefaultLocale: "jp"},
		{ID: "explicit-de", PrimaryLocale: "de"},
	}
	got := map[string]string{}
	for i := range stores {
		stores[i].Normalize()
		got[stores[i].ID] = stores[i].PrimaryLocale
	}
	want := map[string]string{
		"store-us":          "en",
		"store-de":          "en",
		"store-jp":          "en",
		"store-development": "en",
		"custom-store":      "ja",
		"explicit-de":       "de",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected primary locales\nwant=%#v\ngot=%#v", want, got)
	}
}

func TestStoreAliasesComeFromPortableConfig(t *testing.T) {
	cfg := StoresConfig{Stores: []Store{{
		ID:            "store-europe",
		Label:         "Europe Store",
		Aliases:       []string{"eu", "europe"},
		PrimaryLocale: "fr",
		Enabled:       true,
	}}}
	stores, err := selectStores(cfg, "eu")
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || stores[0].ID != "store-europe" || stores[0].PrimaryLocale != "fr" {
		t.Fatalf("unexpected portable store config: %+v", stores)
	}
}

func TestExecutionStoresFilterNarrowsChanges(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us", "store-de"},
			Rows:     1,
			Changes:  2,
			ByAction: map[string]int{"file_upload_same_filename": 2},
		},
		Changes: []PlanChange{
			{Store: "store-us", RowNo: "1", SourceFilename: "same.png", TargetFilename: "same.png", Actions: []string{"file_upload_same_filename"}, Resource: &ResourceInfo{Filename: "same.png"}, NeedsExecute: true},
			{Store: "store-de", RowNo: "1", SourceFilename: "same.png", TargetFilename: "same.png", Actions: []string{"file_upload_same_filename"}, Resource: &ResourceInfo{Filename: "same.png"}, NeedsExecute: true},
		},
	})
	var stdout bytes.Buffer
	err := runDirectStageForTest(t.Context(), []string{
		"upload",
		"--plan", planPath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--no-env-file",
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "target_stores=store-us") || !strings.Contains(output, "matching_changes=1") {
		t.Fatalf("expected narrowed dry-run output, got:\n%s", output)
	}
	if strings.Contains(output, "store-de row=") {
		t.Fatalf("dry-run should not include unselected store:\n%s", output)
	}

	err = run(t.Context(), []string{
		"upload",
		"--plan", planPath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "jp",
		"--no-env-file",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "所选 store 不在 plan 中") {
		t.Fatalf("expected selected store missing error, got %v", err)
	}
}

func TestRunSyncStatusHonorsStoreFilter(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us", "store-de"},
			Rows:     1,
			Changes:  2,
			ByAction: map[string]int{"file_upload_same_filename": 2},
		},
		Changes: []PlanChange{
			{Store: "store-us", RowNo: "1", SourceFilename: "us.png", TargetFilename: "us.png", Actions: []string{"file_upload_same_filename"}},
			{Store: "store-de", RowNo: "1", SourceFilename: "de.png", TargetFilename: "de.png", Actions: []string{"file_upload_same_filename"}},
		},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		SourceFilename: "us.png",
		TargetFilename: "us.png",
		Status:         "READY",
		FileID:         "gid://shopify/MediaImage/us",
		MediaGID:       "gid://shopify/MediaImage/us",
	}}})

	var stdout bytes.Buffer
	err := runDirectStageForTest(t.Context(), []string{
		"sync-status",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	reportRaw, err := os.ReadFile(filepath.Join(dir, "status-report.csv"))
	if err != nil {
		t.Fatal(err)
	}
	report := string(reportRaw)
	if !strings.Contains(report, "store-us") || !strings.Contains(report, "gid://shopify/MediaImage/us") {
		t.Fatalf("expected selected store in status report, got:\n%s", report)
	}
	if strings.Contains(report, "store-de") || strings.Contains(report, "de.png") || strings.Contains(report, "MISSING") {
		t.Fatalf("status report should not include unselected store as missing, got:\n%s", report)
	}
	if !strings.Contains(stdout.String(), "status-report.csv") {
		t.Fatalf("expected report path in stdout, got:\n%s", stdout.String())
	}
}

func TestExecuteReplaceRequiresEvidenceBeforeStagedUpload(t *testing.T) {
	setTestAdminToken(t, "test-store")
	hits := 0
	client := testShopifyClient(func(*http.Request) (int, string) {
		hits++
		return http.StatusInternalServerError, ""
	})
	_, err := executeFileChange(t.Context(), client, Store{ID: "test-store", ShopifyStore: "test-store"}, PlanChange{
		Store:          "test-store",
		RowNo:          "1",
		TargetFilename: "same.png",
		Actions:        []string{"file_replace_same_filename"},
	}, ResourceInfo{Filename: "same.png", Path: filepath.Join("testdata", "images", "same.svg")}, Evidence{}, "REPLACE", commandOptions{})
	if err == nil || !strings.Contains(err.Error(), "缺少同名替换所需 evidence") {
		t.Fatalf("expected missing evidence error before staged upload, got %v", err)
	}
	if hits != 0 {
		t.Fatalf("replace without evidence should not call Shopify, got %d hits", hits)
	}
}

func TestRunUploadConcurrentWritesEvidenceAndMetricsForFailures(t *testing.T) {
	setTestAdminToken(t, "store-us")
	var mu sync.Mutex
	inFlightFileCreates := 0
	maxInFlightFileCreates := 0
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(req *http.Request) (int, string) {
			if req.URL.Host == "upload.shopify.test" {
				return http.StatusCreated, "ok"
			}
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.Contains(body.Query, "stagedUploadsCreate"):
				filename := filenameFromGraphQLVariables(body.Variables, "input")
				return http.StatusOK, fmt.Sprintf(`{"data":{"stagedUploadsCreate":{"stagedTargets":[{"url":"https://upload.shopify.test/%s","resourceUrl":"https://resource.shopify.test/%s","parameters":[]}],"userErrors":[]}}}`, filename, filename)
			case strings.Contains(body.Query, "fileCreate"):
				filename := filenameFromGraphQLVariables(body.Variables, "files")
				mu.Lock()
				inFlightFileCreates++
				if inFlightFileCreates > maxInFlightFileCreates {
					maxInFlightFileCreates = inFlightFileCreates
				}
				mu.Unlock()
				time.Sleep(25 * time.Millisecond)
				mu.Lock()
				inFlightFileCreates--
				mu.Unlock()
				if filename == "fail.svg" {
					return http.StatusOK, `{"data":{"fileCreate":{"files":[],"userErrors":[{"message":"forced failure","field":["files"]}]}}}`
				}
				return http.StatusOK, fmt.Sprintf(`{"data":{"fileCreate":{"files":[{"id":"gid://shopify/MediaImage/%s","fileStatus":"UPLOADED","image":null}],"userErrors":[]}}}`, filename)
			case strings.Contains(body.Query, "node"):
				id, _ := body.Variables["id"].(string)
				return http.StatusOK, readyNodeResponse(id)
			default:
				t.Fatalf("unexpected graphql query: %s", body.Query)
				return http.StatusInternalServerError, `{}`
			}
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	imagePath := filepath.Join("testdata", "images", "same.svg")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us"},
			Rows:     3,
			Changes:  3,
			ByAction: map[string]int{"file_upload_same_filename": 3},
		},
		Changes: []PlanChange{
			{Store: "store-us", RowNo: "1", SourceFilename: "same.svg", TargetFilename: "ok-1.svg", Actions: []string{"file_upload_same_filename"}, Status: "planned", Resource: &ResourceInfo{Filename: "same.svg", Path: imagePath, MimeType: "image/svg+xml"}, NeedsExecute: true},
			{Store: "store-us", RowNo: "2", SourceFilename: "same.svg", TargetFilename: "ok-2.svg", Actions: []string{"file_upload_same_filename"}, Status: "planned", Resource: &ResourceInfo{Filename: "same.svg", Path: imagePath, MimeType: "image/svg+xml"}, NeedsExecute: true},
			{Store: "store-us", RowNo: "3", SourceFilename: "same.svg", TargetFilename: "fail.svg", Actions: []string{"file_upload_same_filename"}, Status: "planned", Resource: &ResourceInfo{Filename: "same.svg", Path: imagePath, MimeType: "image/svg+xml"}, NeedsExecute: true},
		},
	})
	var stdout bytes.Buffer
	err := runDirectStageForTest(t.Context(), []string{
		"upload",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--execute",
		"--no-env-file",
		"--concurrency", "2",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, &stdout)
	if err == nil || !strings.Contains(err.Error(), "1/3 行失败") {
		t.Fatalf("expected partial upload failure, got %v\nstdout:\n%s", err, stdout.String())
	}
	if maxInFlightFileCreates < 2 {
		t.Fatalf("expected bounded workers to overlap fileCreate calls, max in-flight=%d", maxInFlightFileCreates)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, rowNo := range []string{"1", "2"} {
		record := evidence.Find("store-us", DesiredRow{RowNo: rowNo, TargetFilename: "ok-" + rowNo + ".svg"})
		if record == nil || record.Status != "READY" || record.LastError != "" || record.Upload.Status != stageStatusSucceeded {
			t.Fatalf("expected row %s READY without LastError, got %+v", rowNo, record)
		}
	}
	failed := evidence.Find("store-us", DesiredRow{RowNo: "3", TargetFilename: "fail.svg"})
	if failed == nil || failed.Status != "ERROR" || !strings.Contains(failed.LastError, "forced failure") || failed.Upload.Status != stageStatusFailed || failed.Upload.MutationAccepted || !failed.Upload.Retryable {
		t.Fatalf("expected failed row to keep LastError, got %+v", failed)
	}
	var metrics RunMetrics
	readJSONFixture(t, filepath.Join(dir, "metrics.json"), &metrics)
	if metrics.Concurrency != 2 || metrics.RequestedConcurrency != 2 || metrics.EffectiveConcurrency != 2 || metrics.Success != 2 || metrics.Errors != 1 || metrics.Statuses["ERROR"] != 1 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
	if !strings.Contains(stdout.String(), "metrics="+filepath.Join(dir, "metrics.json")) {
		t.Fatalf("expected metrics path in stdout, got:\n%s", stdout.String())
	}
}

func TestRunUploadContinuesAnotherMutationWhileWorkerWaitsForReadback(t *testing.T) {
	setTestAdminToken(t, "store-us")
	firstReadbackStarted := make(chan struct{})
	secondMutationReached := make(chan struct{})
	var firstReadbackOnce sync.Once
	var secondMutationOnce sync.Once
	overlapped := false

	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(req *http.Request) (int, string) {
			if req.URL.Host == "upload.shopify.test" {
				return http.StatusCreated, "ok"
			}
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				return http.StatusInternalServerError, err.Error()
			}
			var body struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				return http.StatusInternalServerError, err.Error()
			}
			switch {
			case strings.Contains(body.Query, "stagedUploadsCreate"):
				filename := filenameFromGraphQLVariables(body.Variables, "input")
				return http.StatusOK, fmt.Sprintf(`{"data":{"stagedUploadsCreate":{"stagedTargets":[{"url":"https://upload.shopify.test/%s","resourceUrl":"https://resource.shopify.test/%s","parameters":[]}],"userErrors":[]}}}`, filename, filename)
			case strings.Contains(body.Query, "fileCreate"):
				filename := filenameFromGraphQLVariables(body.Variables, "files")
				if filename == "second.svg" {
					select {
					case <-firstReadbackStarted:
						overlapped = true
						secondMutationOnce.Do(func() { close(secondMutationReached) })
					case <-req.Context().Done():
						return 499, `{"errors":[{"message":"cancelled waiting for concurrent readback"}]}`
					}
				}
				return http.StatusOK, fmt.Sprintf(`{"data":{"fileCreate":{"files":[{"id":"gid://shopify/MediaImage/%s","fileStatus":"UPLOADED","image":null}],"userErrors":[]}}}`, filename)
			case strings.Contains(body.Query, "node"):
				id, _ := body.Variables["id"].(string)
				if strings.HasSuffix(id, "first.svg") {
					firstReadbackOnce.Do(func() { close(firstReadbackStarted) })
					select {
					case <-secondMutationReached:
					case <-req.Context().Done():
						return 499, `{"errors":[{"message":"cancelled waiting for second mutation"}]}`
					}
				}
				return http.StatusOK, readyNodeResponse(id)
			default:
				return http.StatusInternalServerError, `{"errors":[{"message":"unexpected query"}]}`
			}
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	imagePath := filepath.Join("testdata", "images", "same.svg")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-overlap",
		Summary: PlanSummary{
			Stores:   []string{"store-us"},
			Rows:     2,
			Changes:  2,
			ByAction: map[string]int{"file_upload_same_filename": 2},
		},
		Changes: []PlanChange{
			{Store: "store-us", RowNo: "1", SourceFilename: "same.svg", TargetFilename: "first.svg", Actions: []string{"file_upload_same_filename"}, Status: "planned", Resource: &ResourceInfo{Filename: "same.svg", Path: imagePath, MimeType: "image/svg+xml"}, NeedsExecute: true},
			{Store: "store-us", RowNo: "2", SourceFilename: "same.svg", TargetFilename: "second.svg", Actions: []string{"file_upload_same_filename"}, Status: "planned", Resource: &ResourceInfo{Filename: "same.svg", Path: imagePath, MimeType: "image/svg+xml"}, NeedsExecute: true},
		},
	})

	err := runDirectStageForTest(t.Context(), []string{
		"upload",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--execute",
		"--no-env-file",
		"--concurrency", "2",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !overlapped {
		t.Fatal("expected one worker to reach another mutation while the first worker waited for readback")
	}
}

func TestRunUploadPreservesMutationFileIDWhenPollFails(t *testing.T) {
	setTestAdminToken(t, "store-us")
	fixture, err := inspectResource(filepath.Join("testdata", "images", "same.svg"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		action      string
		mutation    string
		nodeID      string
		initial     []EvidenceRecord
		target      string
		wantSHA     string
		wantByError string
	}{
		{
			name:     "create",
			action:   "file_upload_same_filename",
			mutation: "fileCreate",
			nodeID:   "gid://shopify/MediaImage/pending-create",
			target:   "pending-create.svg",
			wantSHA:  fixture.SHA256,
		},
		{
			name:     "replace",
			action:   "file_replace_same_filename",
			mutation: "fileUpdate",
			nodeID:   "gid://shopify/MediaImage/pending-replace",
			initial: []EvidenceRecord{{
				Store:          "store-us",
				RowNo:          "1",
				SourceFilename: "same.svg",
				TargetFilename: "pending-replace.svg",
				FileID:         "gid://shopify/MediaImage/existing",
				MediaGID:       "gid://shopify/MediaImage/existing",
				Status:         "READY",
			}},
			target:  "pending-replace.svg",
			wantSHA: fixture.SHA256,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldFactory := newShopifyClient
			newShopifyClient = func(_ *http.Client) *ShopifyClient {
				return testShopifyClient(func(req *http.Request) (int, string) {
					if req.URL.Host == "upload.shopify.test" {
						return http.StatusCreated, "ok"
					}
					raw, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					var body struct {
						Query     string         `json:"query"`
						Variables map[string]any `json:"variables"`
					}
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Fatal(err)
					}
					switch {
					case strings.Contains(body.Query, "stagedUploadsCreate"):
						filename := filenameFromGraphQLVariables(body.Variables, "input")
						return http.StatusOK, fmt.Sprintf(`{"data":{"stagedUploadsCreate":{"stagedTargets":[{"url":"https://upload.shopify.test/%s","resourceUrl":"https://resource.shopify.test/%s","parameters":[]}],"userErrors":[]}}}`, filename, filename)
					case strings.Contains(body.Query, tt.mutation):
						if tt.mutation == "fileCreate" {
							return http.StatusOK, fmt.Sprintf(`{"data":{"fileCreate":{"files":[{"id":%q,"fileStatus":"UPLOADED","image":null}],"userErrors":[]}}}`, tt.nodeID)
						}
						return http.StatusOK, fmt.Sprintf(`{"data":{"fileUpdate":{"files":[{"id":%q,"fileStatus":"UPLOADED","image":null}],"userErrors":[]}}}`, tt.nodeID)
					case strings.Contains(body.Query, "node"):
						return http.StatusOK, fmt.Sprintf(`{"data":{"node":{"id":%q,"fileStatus":"UPLOADED","image":null}}}`, tt.nodeID)
					default:
						t.Fatalf("unexpected graphql query: %s", body.Query)
						return http.StatusInternalServerError, `{}`
					}
				})
			}
			t.Cleanup(func() { newShopifyClient = oldFactory })

			dir := t.TempDir()
			planPath := filepath.Join(dir, "plan.json")
			evidencePath := filepath.Join(dir, "evidence.json")
			imagePath := filepath.Join("testdata", "images", "same.svg")
			mustWriteJSON(t, planPath, Plan{
				RunID:   "run-test",
				Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1, ByAction: map[string]int{tt.action: 1}},
				Changes: []PlanChange{{
					Store:          "store-us",
					RowNo:          "1",
					SourceFilename: "same.svg",
					TargetFilename: tt.target,
					Actions:        []string{tt.action},
					Status:         "planned",
					Resource:       &ResourceInfo{Filename: "same.svg", Path: imagePath, MimeType: "image/svg+xml", SHA256: tt.wantSHA},
					NeedsExecute:   true,
				}},
			})
			if len(tt.initial) > 0 {
				mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: tt.initial})
			}
			err := runDirectStageForTest(t.Context(), []string{
				"upload",
				"--plan", planPath,
				"--evidence", evidencePath,
				"--stores-config", filepath.Join("testdata", "stores.config.json"),
				"--stores", "us",
				"--execute",
				"--no-env-file",
				"--max-attempts", "1",
				"--poll-interval", "1ns",
			}, ioDiscard{})
			if err == nil || !strings.Contains(err.Error(), "1/1 行失败") {
				t.Fatalf("expected upload failure after polling timeout, got %v", err)
			}
			evidence, err := LoadEvidence(evidencePath)
			if err != nil {
				t.Fatal(err)
			}
			record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: tt.target})
			if record == nil {
				t.Fatal("expected failed upload evidence record")
			}
			if record.Status != "ERROR" || !strings.Contains(record.LastError, "未就绪") {
				t.Fatalf("expected ERROR evidence with poll failure, got %+v", record)
			}
			if record.FileID != tt.nodeID || record.MediaGID != tt.nodeID {
				t.Fatalf("expected failed polling evidence to preserve mutation id, got %+v", record)
			}
			if record.SHA256 != tt.wantSHA {
				t.Fatalf("expected resource sha to remain in failed evidence, got %+v", record)
			}
			if record.Upload.Status != stageStatusAwaitingReadback || !record.Upload.MutationAccepted || !record.Upload.Retryable {
				t.Fatalf("expected accepted mutation to wait for readback without becoming retryable upload, got %+v", record.Upload)
			}
		})
	}
}

func TestUploadMutationTransportAmbiguityRequiresAttention(t *testing.T) {
	setTestAdminToken(t, "store-us")
	client := testShopifyClient(func(req *http.Request) (int, string) {
		if req.URL.Host == "upload.shopify.test" {
			return http.StatusCreated, "ok"
		}
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
		switch {
		case strings.Contains(body.Query, "stagedUploadsCreate"):
			return http.StatusOK, `{"data":{"stagedUploadsCreate":{"stagedTargets":[{"url":"https://upload.shopify.test/same.svg","resourceUrl":"https://resource.shopify.test/same.svg","parameters":[]}],"userErrors":[]}}}`
		case strings.Contains(body.Query, "fileCreate"):
			return http.StatusOK, `{invalid-json`
		default:
			t.Fatalf("unexpected graphql query: %s", body.Query)
			return http.StatusInternalServerError, `{}`
		}
	})
	resource, err := inspectResource(filepath.Join("testdata", "images", "same.svg"))
	if err != nil {
		t.Fatal(err)
	}
	change := PlanChange{
		Store: "store-us", RowNo: "1", SourceFilename: "same.svg", TargetFilename: "same.svg",
		Actions: []string{"file_upload_same_filename"}, Resource: &resource,
	}
	result := executeUploadJob(t.Context(), uploadJob{change: change}, func(string) Store {
		return Store{ID: "store-us", ShopifyStore: "store-us"}
	}, Evidence{}, "REPLACE", commandOptions{maxAttempts: 1, pollInterval: time.Nanosecond}, client)
	if result.err == nil {
		t.Fatal("expected ambiguous mutation response failure")
	}
	if result.record.Upload.Status != stageStatusFailed || result.record.Upload.Retryable || result.record.Upload.MutationAccepted {
		t.Fatalf("ambiguous mutation outcome must fail closed for manual attention: %+v", result.record.Upload)
	}
}

func TestReplaceFailureBeforeMutationNeverReadsBackOldMediaID(t *testing.T) {
	setTestAdminToken(t, "store-us")
	change := PlanChange{
		Store: "store-us", RowNo: "1", SourceFilename: "same.svg", TargetFilename: "same.svg",
		Actions: []string{"file_replace_same_filename"}, Resource: &ResourceInfo{Path: filepath.Join("testdata", "images", "same.svg"), Filename: "same.svg", SHA256: "new-sha"},
	}
	initial := Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename,
		FileID: "gid://shopify/MediaImage/old", MediaGID: "gid://shopify/MediaImage/old", SHA256: "old-sha", Status: "READY",
	}}}
	client := testShopifyClient(func(*http.Request) (int, string) {
		return http.StatusOK, `{"data":{"stagedUploadsCreate":{"stagedTargets":[],"userErrors":[{"message":"staging rejected"}]}}}`
	})
	result := executeUploadJob(t.Context(), uploadJob{change: change}, func(string) Store {
		return Store{ID: "store-us", ShopifyStore: "store-us"}
	}, initial, "REPLACE", commandOptions{}, client)
	if result.err == nil {
		t.Fatal("expected pre-mutation staging failure")
	}
	if result.record.Upload.Status != stageStatusFailed || result.record.Upload.MutationAccepted || !result.record.Upload.Retryable {
		t.Fatalf("pre-mutation failure was misclassified: %+v", result.record.Upload)
	}
	evidence := Evidence{RunID: "run-test", Results: []EvidenceRecord{result.record}}
	plan := Plan{RunID: "run-test", Changes: []PlanChange{change}}
	normalizeLegacyEvidence(plan, plan.Changes, &evidence)
	if got := filterUploadReadbackChanges(plan.Changes, evidence); len(got) != 0 {
		t.Fatalf("old media id was incorrectly scheduled for readback: %+v", got)
	}
	if got := filterPendingUploadChanges(plan.Changes, evidence); len(got) != 1 {
		t.Fatalf("safe pre-mutation retry was not preserved: %+v", got)
	}
}

func TestShopifyGraphQLRetries429AndThrottled(t *testing.T) {
	tests := []struct {
		name          string
		firstResponse func() (int, string)
	}{
		{
			name: "http 429",
			firstResponse: func() (int, string) {
				return http.StatusTooManyRequests, "busy"
			},
		},
		{
			name: "graphql throttled",
			firstResponse: func() (int, string) {
				return http.StatusOK, `{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}}]}`
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestAdminToken(t, "test-store")
			hits := 0
			client := testShopifyClient(func(*http.Request) (int, string) {
				hits++
				if hits == 1 {
					return tt.firstResponse()
				}
				return http.StatusOK, readyNodeResponse("gid://shopify/MediaImage/1")
			})
			node, err := client.FileNode(t.Context(), Store{ID: "test-store", ShopifyStore: "test-store"}, "gid://shopify/MediaImage/1")
			if err != nil {
				t.Fatal(err)
			}
			if node.ID == "" || hits != 2 {
				t.Fatalf("expected retry success after 2 hits, node=%+v hits=%d", node, hits)
			}
		})
	}
}

func TestShopifyGraphQLNeverAutomaticallyRetriesMutation(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http 500", status: http.StatusInternalServerError, body: "ambiguous"},
		{name: "graphql throttled", status: http.StatusOK, body: `{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestAdminToken(t, "test-store")
			hits := 0
			client := testShopifyClient(func(*http.Request) (int, string) {
				hits++
				return tt.status, tt.body
			})
			_, err := client.GraphQL(t.Context(), Store{ID: "test-store", ShopifyStore: "test-store"}, `mutation TestMutation { test }`, map[string]any{})
			if err == nil || !strings.Contains(err.Error(), "不会自动重试") {
				t.Fatalf("expected fail-closed mutation error, got %v", err)
			}
			if hits != 1 {
				t.Fatalf("mutation was automatically retried %d times", hits)
			}
		})
	}
}

func TestFindFileByFilenameQuotesSearchAndMatchesExactBasename(t *testing.T) {
	setTestAdminToken(t, "store-us")
	var gotQuery string
	client := testShopifyClient(func(req *http.Request) (int, string) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.Query, "files(first: 10") {
			t.Fatalf("unexpected graphql query: %s", body.Query)
		}
		gotQuery, _ = body.Variables["query"].(string)
		return http.StatusOK, `{"data":{"files":{"nodes":[
			{"id":"gid://shopify/MediaImage/wrong","fileStatus":"READY","alt":"Wrong","image":{"url":"https://cdn.shopify.com/s/files/1/files/other.png","width":120,"height":80}},
			{"id":"gid://shopify/MediaImage/right","fileStatus":"READY","alt":"Right","image":{"url":"https://cdn.shopify.com/s/files/1/files/synthetic%20sample%20(v2)%3Ahero.png?v=1","width":120,"height":80}}
		]}}}`
	})
	node, err := client.FindFileByFilename(t.Context(), Store{ID: "store-us", ShopifyStore: "store-us"}, "synthetic sample (v2):hero.png")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != `filename:"synthetic sample (v2):hero.png"` {
		t.Fatalf("filename search should be quoted, got %q", gotQuery)
	}
	if node.ID != "gid://shopify/MediaImage/right" {
		t.Fatalf("expected exact basename match, got %+v", node)
	}
}

func TestFindFileByFilenameRejectsSearchResultsWithoutExactBasename(t *testing.T) {
	setTestAdminToken(t, "store-us")
	client := testShopifyClient(func(*http.Request) (int, string) {
		return http.StatusOK, `{"data":{"files":{"nodes":[
			{"id":"gid://shopify/MediaImage/wrong","fileStatus":"READY","alt":"Wrong","image":{"url":"https://cdn.shopify.com/s/files/1/files/other.png","width":120,"height":80}}
		]}}}`
	})
	_, err := client.FindFileByFilename(t.Context(), Store{ID: "store-us", ShopifyStore: "store-us"}, "target (1).png")
	if err == nil || !strings.Contains(err.Error(), "none matched exact filename") {
		t.Fatalf("expected exact filename mismatch error, got %v", err)
	}
}

func TestAdminTokenErrorListsAllSupportedSources(t *testing.T) {
	suffix := envSuffix("store-us")
	for _, key := range []string{
		"SHOPIFY_CLIENT_ID_" + suffix,
		"SHOPIFY_CLIENT_SECRET_" + suffix,
		"SHOPIFY_CLIENT_ID",
		"SHOPIFY_CLIENT_SECRET",
		"SHOPIFY_ADMIN_TOKEN_" + suffix,
		"SHOPIFY_ADMIN_TOKEN",
	} {
		t.Setenv(key, "")
	}
	_, err := NewShopifyClient(nil).AdminToken(t.Context(), Store{ID: "store-us", ShopifyStore: "store-us"})
	if err == nil {
		t.Fatal("expected missing credential error")
	}
	message := err.Error()
	for _, want := range []string{
		"SHOPIFY_CLIENT_ID_" + suffix + "/SHOPIFY_CLIENT_SECRET_" + suffix,
		"SHOPIFY_CLIENT_ID/SHOPIFY_CLIENT_SECRET",
		"SHOPIFY_ADMIN_TOKEN_" + suffix,
		"SHOPIFY_ADMIN_TOKEN",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("missing %q in error: %s", want, message)
		}
	}
}

func TestPollFileReadyFailsOnFailedStatus(t *testing.T) {
	setTestAdminToken(t, "test-store")
	client := testShopifyClient(func(*http.Request) (int, string) {
		return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/failed","fileStatus":"FAILED","image":null}}}`
	})
	_, err := client.PollFileReady(t.Context(), Store{ID: "test-store", ShopifyStore: "test-store"}, "gid://shopify/MediaImage/failed", 1, time.Nanosecond)
	if err == nil || !strings.Contains(err.Error(), "file processing failed") {
		t.Fatalf("expected failed status error, got %v", err)
	}
}

func TestPollFileReadyWaitsForImageMetadata(t *testing.T) {
	setTestAdminToken(t, "test-store")
	hits := 0
	client := testShopifyClient(func(*http.Request) (int, string) {
		hits++
		if hits == 1 {
			return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/1","fileStatus":"READY","image":null}}}`
		}
		return http.StatusOK, readyNodeResponse("gid://shopify/MediaImage/1")
	})
	node, err := client.PollFileReady(t.Context(), Store{ID: "test-store", ShopifyStore: "test-store"}, "gid://shopify/MediaImage/1", 2, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if node.Image == nil || node.Image.Width == 0 || node.Image.Height == 0 || hits != 2 {
		t.Fatalf("expected delayed metadata readback, node=%+v hits=%d", node, hits)
	}
}

func TestRunVerifyDoesNotWriteSuccessWithoutImageMetadata(t *testing.T) {
	setTestAdminToken(t, "store-us")
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(*http.Request) (int, string) {
			return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/1","fileStatus":"READY","image":{"url":"https://cdn.shopify.com/s/files/1/same.png","width":0,"height":80}}}}`
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID:   "run-test",
		Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1, ByAction: map[string]int{"verify": 1}},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"verify"},
			Status:         "planned",
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/1",
	}}})
	err := runDirectStageForTest(t.Context(), []string{
		"verify",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--no-env-file",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "verify failed") {
		t.Fatalf("expected verify failure, got %v", err)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil {
		t.Fatal("expected verify evidence record")
	}
	if record.Status == "READY" || record.LastError == "" {
		t.Fatalf("verify should not write READY without image metadata: %+v", record)
	}
	if record.Verify.Status != stageStatusFailed || !record.Verify.Retryable {
		t.Fatalf("verify failure should be explicit and retryable, got %+v", record.Verify)
	}
}

func TestExecuteVerifyChangesPersistsAndPropagatesReadbackCancellation(t *testing.T) {
	setTestAdminToken(t, "store-us")
	dir := t.TempDir()
	evidencePath := filepath.Join(dir, "evidence.json")
	change := PlanChange{
		Store:          "store-us",
		RowNo:          "1",
		SourceFilename: "same.png",
		TargetFilename: "same.png",
		Actions:        []string{"verify"},
		Status:         "planned",
	}
	evidence := Evidence{RunID: "run-verify-cancel", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/1",
	}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := executeVerifyChanges(
		ctx,
		ioDiscard{},
		commandOptions{maxAttempts: 1, pollInterval: time.Nanosecond},
		[]PlanChange{change},
		func(string) Store { return Store{ID: "store-us", ShopifyStore: "store-us", Enabled: true} },
		&evidence,
		evidencePath,
		nil,
		testShopifyClient(func(req *http.Request) (int, string) {
			<-req.Context().Done()
			return 499, `{"errors":[{"message":"cancelled"}]}`
		}),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verify cancellation must propagate to apply, got %v", err)
	}
	persisted, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := persisted.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil || record.Verify.Status != stageStatusFailed || record.Verify.LastError == "" {
		t.Fatalf("verify cancellation must persist row evidence before returning: %+v", record)
	}
}

func TestRunVerifyDoesNotWriteSuccessWhenAltReadbackMismatches(t *testing.T) {
	setTestAdminToken(t, "store-us")
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(*http.Request) (int, string) {
			return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/1","fileStatus":"READY","alt":"Old alt","image":{"url":"https://cdn.shopify.com/s/files/1/same.png","width":120,"height":80}}}}`
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID:   "run-test",
		Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1, ByAction: map[string]int{"alt_update": 1}},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"alt_update"},
			Status:         "planned",
			Alt:            "Expected alt",
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/1",
	}}})
	err := runDirectStageForTest(t.Context(), []string{
		"verify",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--no-env-file",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "verify failed") {
		t.Fatalf("expected verify failure, got %v", err)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil {
		t.Fatal("expected verify evidence record")
	}
	if record.Status != "ERROR" || !strings.Contains(record.LastError, "alt readback mismatch") {
		t.Fatalf("verify should reject mismatched alt readback: %+v", record)
	}
}

func TestRunVerifyDoesNotMarkFailedUploadReadyFromStaleEvidence(t *testing.T) {
	setTestAdminToken(t, "store-us")
	oldFactory := newShopifyClient
	graphQLHits := 0
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(*http.Request) (int, string) {
			graphQLHits++
			return http.StatusOK, readyNodeResponse("gid://shopify/MediaImage/old")
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID:   "run-test",
		Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1, ByAction: map[string]int{"file_replace_same_filename": 1}},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"file_replace_same_filename"},
			Status:         "planned",
			Resource:       &ResourceInfo{Filename: "same.png", SHA256: "new-sha"},
			NeedsExecute:   true,
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/old",
		MediaGID:       "gid://shopify/MediaImage/old",
		SHA256:         "old-sha",
		Status:         "ERROR",
		LastError:      "forced upload failure",
	}}})
	err := run(t.Context(), []string{
		"verify",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--no-env-file",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "verify failed") {
		t.Fatalf("expected verify failure, got %v", err)
	}
	if graphQLHits != 0 {
		t.Fatalf("verify should not poll stale file evidence after failed upload, got %d GraphQL calls", graphQLHits)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil {
		t.Fatal("expected verify evidence record")
	}
	if record.Status != "ERROR" || !strings.Contains(record.LastError, "缺少成功上传 evidence") {
		t.Fatalf("verify should keep failed upload evidence as ERROR, got %+v", record)
	}
}

func TestRunVerifyKeepsPendingJSONReplacementNonReady(t *testing.T) {
	setTestAdminToken(t, "store-us")
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(*http.Request) (int, string) {
			return http.StatusOK, readyNodeResponse("gid://shopify/MediaImage/1")
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us"},
			Rows:     1,
			Changes:  1,
			ByAction: map[string]int{"file_upload_new_filename": 1, "json_replace_needed": 1},
		},
		Changes: []PlanChange{{
			Store:           "store-us",
			RowNo:           "1",
			SourceFilename:  "old.png",
			TargetFilename:  "new.png",
			FilenameChanged: true,
			Actions:         []string{"file_upload_new_filename", "json_replace_needed"},
			Status:          "planned",
			Template:        "product.example.json",
			NeedsExecute:    true,
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		SourceFilename: "old.png",
		TargetFilename: "new.png",
		FileID:         "gid://shopify/MediaImage/1",
		MediaGID:       "gid://shopify/MediaImage/1",
		Status:         "READY",
	}}})

	err := run(t.Context(), []string{
		"verify",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--no-env-file",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "verify failed") {
		t.Fatalf("expected verify failure, got %v", err)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "new.png"})
	if record == nil {
		t.Fatal("expected verify evidence record")
	}
	if record.Status == "READY" || !strings.Contains(record.LastError, "json_replace_needed") {
		t.Fatalf("verify should keep pending json replacement non-ready, got %+v", record)
	}
}

func TestRunVerifyAcceptsMatchingVerifiedJSONReplacementReceipt(t *testing.T) {
	setTestAdminToken(t, "store-us")
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(*http.Request) (int, string) {
			return http.StatusOK, readyNodeResponse("gid://shopify/MediaImage/1")
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{RunID: "run-test", Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1}, Changes: []PlanChange{{Store: "store-us", RowNo: "1", SourceFilename: "old.png", TargetFilename: "new.png", FilenameChanged: true, Actions: []string{"file_upload_new_filename", "json_replace_needed"}, Status: "planned", Template: "product.example", TargetTheme: "staging-theme", NeedsExecute: true}}})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{Store: "store-us", RowNo: "1", SourceFilename: "old.png", TargetFilename: "new.png", FileID: "gid://shopify/MediaImage/1", MediaGID: "gid://shopify/MediaImage/1", Status: "ERROR", LastError: "store-us row=1 缺少成功上传 evidence，不能验证文件写入结果"}}})
	mustWriteJSON(t, filepath.Join(dir, "json-replacement-receipt.json"), map[string]any{
		"version":      2,
		"verified":     true,
		"template":     "product.example",
		"target_theme": "staging-theme",
		"results":      []map[string]any{{"store": "store-us", "ok": true}},
		"mapping_results": []map[string]any{{
			"source_filename":   "old.png",
			"target_filename":   "new.png",
			"replacement_count": 1,
			"matched":           true,
		}},
	})

	if err := run(t.Context(), []string{"verify", "--plan", planPath, "--evidence", evidencePath, "--stores-config", filepath.Join("testdata", "stores.config.json"), "--stores", "us", "--no-env-file", "--max-attempts", "1", "--poll-interval", "1ns"}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "new.png"})
	if record == nil || record.Status != "READY" || record.LastError != "" {
		t.Fatalf("matching JSON receipt should allow READY, got %+v", record)
	}
}

func TestJSONReceiptAllowsPlanWithoutExplicitTargetTheme(t *testing.T) {
	receipt := &jsonReplacementReceipt{Version: 2, Verified: true, Template: "product.example", TargetTheme: "staging-theme", Results: []struct {
		Store string `json:"store"`
		OK    bool   `json:"ok"`
	}{{Store: "store-us", OK: true}}, MappingResults: []jsonReplacementMappingResult{{SourceFilename: "old.png", TargetFilename: "new.png", ReplacementCount: 1, Matched: true}}}
	change := PlanChange{Store: "store-us", Template: "product.example", SourceFilename: "old.png", TargetFilename: "new.png", Actions: []string{"json_replace_needed"}}
	if !jsonReceiptConfirmsChange(receipt, change) {
		t.Fatal("receipt should cover a plan that leaves target_theme unspecified")
	}
}

func TestSyncStatusReportsPendingJSONReplacementAsNonReady(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us"},
			Rows:     1,
			Changes:  1,
			ByAction: map[string]int{"file_upload_new_filename": 1, "json_replace_needed": 1},
		},
		Changes: []PlanChange{{
			Store:           "store-us",
			RowNo:           "1",
			SourceFilename:  "old.png",
			TargetFilename:  "new.png",
			FilenameChanged: true,
			Actions:         []string{"file_upload_new_filename", "json_replace_needed"},
			Status:          "planned",
			Template:        "product.example.json",
			NeedsExecute:    true,
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		SourceFilename: "old.png",
		TargetFilename: "new.png",
		FileID:         "gid://shopify/MediaImage/1",
		MediaGID:       "gid://shopify/MediaImage/1",
		CDNURL:         "https://cdn.shopify.com/s/files/1/new.png",
		Status:         "READY",
	}}})

	if err := run(t.Context(), []string{
		"sync-status",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
	}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	reportRaw, err := os.ReadFile(filepath.Join(dir, "status-report.csv"))
	if err != nil {
		t.Fatal(err)
	}
	report := string(reportRaw)
	if strings.Contains(report, ",READY,") || !strings.Contains(report, ",PENDING_JSON_REPLACE,") || !strings.Contains(report, "json_replace_needed") {
		t.Fatalf("expected sync-status report to show pending json replacement, got:\n%s", report)
	}
}

func TestRunVerifyFailsOnPlanErrorsBeforeUsingStaleEvidence(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	missingEnvPath := filepath.Join(dir, "missing.env.local")
	mustWriteJSON(t, planPath, Plan{
		RunID: "run-test",
		Summary: PlanSummary{
			Stores:   []string{"store-us"},
			Rows:     1,
			Changes:  1,
			ByAction: map[string]int{"verify": 1},
		},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "missing.png",
			TargetFilename: "same.png",
			Actions:        []string{"verify"},
			Status:         "error",
			Reason:         "missing source",
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/1",
		MediaGID:       "gid://shopify/MediaImage/1",
		Status:         "READY",
	}}})

	err := run(t.Context(), []string{
		"verify",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--env-file", missingEnvPath,
	}, ioDiscard{})
	if err == nil {
		t.Fatal("expected verify to fail on scoped plan errors")
	}
	got := err.Error()
	if !strings.Contains(got, "预检错误") || !strings.Contains(got, "verify 未执行") {
		t.Fatalf("expected plan error guard, got %v", err)
	}
	if strings.Contains(got, "missing.env.local") {
		t.Fatalf("plan error guard should run before env loading, got %v", err)
	}
	var metrics RunMetrics
	readJSONFixture(t, filepath.Join(dir, "metrics.json"), &metrics)
	if metrics.Errors != 1 || metrics.Statuses["error"] != 1 {
		t.Fatalf("expected plan error metrics, got %+v", metrics)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil {
		t.Fatal("expected plan error evidence record")
	}
	if record.Status != "ERROR" || !strings.Contains(record.LastError, "missing source") {
		t.Fatalf("expected stale READY evidence to be overwritten as plan ERROR, got %+v", record)
	}
	if record.FileID == "" || record.MediaGID == "" {
		t.Fatalf("plan error evidence should preserve prior ids for audit, got %+v", record)
	}
	if err := run(t.Context(), []string{
		"sync-status",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
	}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	reportRaw, err := os.ReadFile(filepath.Join(dir, "status-report.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reportRaw), ",ERROR,") || !strings.Contains(string(reportRaw), "missing source") {
		t.Fatalf("expected sync-status report to use plan ERROR evidence, got:\n%s", string(reportRaw))
	}
}

func TestRunVerifySkipsUnavailableTranslationLocales(t *testing.T) {
	setTestAdminToken(t, "store-us")
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
			switch {
			case strings.Contains(body.Query, "node"):
				return http.StatusOK, readyNodeResponse("gid://shopify/MediaImage/1")
			case strings.Contains(body.Query, "shopLocales"):
				return http.StatusOK, `{"data":{"shopLocales":[{"locale":"en","primary":true,"published":true}]}}`
			default:
				t.Fatalf("unexpected graphql query: %s", body.Query)
				return http.StatusInternalServerError, `{}`
			}
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID:   "run-test",
		Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1, ByAction: map[string]int{"translation_update": 1}},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"translation_update"},
			Status:         "planned",
			Translations:   map[string]string{"en": "EN alt", "de": "DE alt"},
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/1",
	}}})
	err := run(t.Context(), []string{
		"verify",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--no-env-file",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil {
		t.Fatal("expected verify evidence record")
	}
	if record.Status != "READY" || record.LastError != "" {
		t.Fatalf("verify should treat skipped locales as non-error: %+v", record)
	}
	for _, locale := range []string{"en", "de"} {
		if record.TranslationReadback[locale].Status != "SKIPPED" {
			t.Fatalf("expected skipped locale %s, got %+v", locale, record.TranslationReadback[locale])
		}
	}
}

func TestRunAltRequiresReadyImageMetadataBeforeSuccess(t *testing.T) {
	setTestAdminToken(t, "store-us")
	oldFactory := newShopifyClient
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		hits := 0
		return testShopifyClient(func(*http.Request) (int, string) {
			hits++
			if hits == 1 {
				return http.StatusOK, `{"data":{"fileUpdate":{"files":[{"id":"gid://shopify/MediaImage/1","fileStatus":"READY","alt":"Alt","image":{"url":"https://cdn.shopify.com/s/files/1/same.png","width":120,"height":80}}],"userErrors":[]}}}`
			}
			return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/1","fileStatus":"READY","alt":"Alt","image":{"url":"https://cdn.shopify.com/s/files/1/same.png","width":0,"height":80}}}}`
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID:   "run-test",
		Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 1, Changes: 1, ByAction: map[string]int{"alt_update": 1}},
		Changes: []PlanChange{{
			Store:          "store-us",
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
			Actions:        []string{"alt_update"},
			Status:         "planned",
			Alt:            "Alt",
			NeedsExecute:   true,
		}},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store:          "store-us",
		RowNo:          "1",
		TargetFilename: "same.png",
		FileID:         "gid://shopify/MediaImage/1",
	}}})
	err := runDirectStageForTest(t.Context(), []string{
		"alt",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--execute",
		"--no-env-file",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, ioDiscard{})
	if err == nil {
		t.Fatal("expected alt readback to fail without image metadata")
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	record := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "same.png"})
	if record == nil {
		t.Fatal("expected alt evidence record")
	}
	if record.Status == "READY" || record.LastError == "" {
		t.Fatalf("alt should not write READY without image metadata: %+v", record)
	}
}

func TestRunAltContinuesAfterRowFailure(t *testing.T) {
	setTestAdminToken(t, "store-us")
	oldFactory := newShopifyClient
	var sawSecondAlt bool
	newShopifyClient = func(_ *http.Client) *ShopifyClient {
		return testShopifyClient(func(req *http.Request) (int, string) {
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.Contains(body.Query, "fileUpdate"):
				files, _ := body.Variables["files"].([]any)
				file, _ := files[0].(map[string]any)
				alt, _ := file["alt"].(string)
				if alt == "Bad alt" {
					return http.StatusOK, `{"data":{"fileUpdate":{"files":[],"userErrors":[{"field":["alt"],"message":"forced alt failure"}]}}}`
				}
				if alt == "Good alt" {
					sawSecondAlt = true
					return http.StatusOK, `{"data":{"fileUpdate":{"files":[{"id":"gid://shopify/MediaImage/2","fileStatus":"READY","alt":"Good alt","image":{"url":"https://cdn.shopify.com/s/files/1/good.png","width":120,"height":80}}],"userErrors":[]}}}`
				}
				t.Fatalf("unexpected alt update: %q", alt)
				return http.StatusInternalServerError, `{}`
			case strings.Contains(body.Query, "node"):
				return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/2","fileStatus":"READY","alt":"Good alt","image":{"url":"https://cdn.shopify.com/s/files/1/good.png","width":120,"height":80}}}}`
			default:
				t.Fatalf("unexpected graphql query: %s", body.Query)
				return http.StatusInternalServerError, `{}`
			}
		})
	}
	t.Cleanup(func() { newShopifyClient = oldFactory })

	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	evidencePath := filepath.Join(dir, "evidence.json")
	mustWriteJSON(t, planPath, Plan{
		RunID:   "run-test",
		Summary: PlanSummary{Stores: []string{"store-us"}, Rows: 2, Changes: 2, ByAction: map[string]int{"alt_update": 2}},
		Changes: []PlanChange{
			{
				Store:          "store-us",
				RowNo:          "1",
				SourceFilename: "bad.png",
				TargetFilename: "bad.png",
				Actions:        []string{"alt_update"},
				Status:         "planned",
				Alt:            "Bad alt",
				NeedsExecute:   true,
			},
			{
				Store:          "store-us",
				RowNo:          "2",
				SourceFilename: "good.png",
				TargetFilename: "good.png",
				Actions:        []string{"alt_update"},
				Status:         "planned",
				Alt:            "Good alt",
				NeedsExecute:   true,
			},
		},
	})
	mustWriteJSON(t, evidencePath, Evidence{RunID: "run-test", Results: []EvidenceRecord{
		{
			Store:          "store-us",
			RowNo:          "1",
			TargetFilename: "bad.png",
			FileID:         "gid://shopify/MediaImage/1",
		},
		{
			Store:          "store-us",
			RowNo:          "2",
			TargetFilename: "good.png",
			FileID:         "gid://shopify/MediaImage/2",
		},
	}})
	err := runDirectStageForTest(t.Context(), []string{
		"alt",
		"--plan", planPath,
		"--evidence", evidencePath,
		"--stores-config", filepath.Join("testdata", "stores.config.json"),
		"--stores", "us",
		"--execute",
		"--no-env-file",
		"--max-attempts", "1",
		"--poll-interval", "1ns",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "alt finished with 1 error") {
		t.Fatalf("expected aggregated alt failure, got %v", err)
	}
	if !sawSecondAlt {
		t.Fatal("expected alt command to continue to the second row")
	}
	evidence, err := LoadEvidence(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	failed := evidence.Find("store-us", DesiredRow{RowNo: "1", TargetFilename: "bad.png"})
	if failed == nil || failed.Status != "ERROR" || !strings.Contains(failed.LastError, "forced alt failure") || failed.Alt.Status != stageStatusFailed || !failed.Alt.Retryable {
		t.Fatalf("expected first row error evidence, got %+v", failed)
	}
	ready := evidence.Find("store-us", DesiredRow{RowNo: "2", TargetFilename: "good.png"})
	if ready == nil || ready.Status != "READY" || ready.LastError != "" || ready.AltReadback != "Good alt" || ready.Alt.Status != stageStatusSucceeded {
		t.Fatalf("expected second row ready evidence, got %+v", ready)
	}
}

func TestAltReadbackRecoveryDoesNotRepeatFileUpdate(t *testing.T) {
	setTestAdminToken(t, "store-us")
	change := PlanChange{Store: "store-us", RowNo: "1", SourceFilename: "same.png", TargetFilename: "same.png", Actions: []string{"alt_update"}, Alt: "Alt"}
	evidence := Evidence{RunID: "run-test", Results: []EvidenceRecord{{Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename, FileID: "gid://shopify/MediaImage/1", MediaGID: "gid://shopify/MediaImage/1", Status: "READY"}}}
	evidencePath := filepath.Join(t.TempDir(), "evidence.json")
	mustWriteJSON(t, evidencePath, evidence)
	fileUpdates := 0
	firstClient := testShopifyClient(func(req *http.Request) (int, string) {
		raw, _ := io.ReadAll(req.Body)
		var body struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &body)
		if strings.Contains(body.Query, "fileUpdate") {
			fileUpdates++
			return http.StatusOK, `{"data":{"fileUpdate":{"files":[{"id":"gid://shopify/MediaImage/1","fileStatus":"UPLOADED","alt":"Alt","image":null}],"userErrors":[]}}}`
		}
		if strings.Contains(body.Query, "node") {
			return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/1","fileStatus":"UPLOADED","alt":"Alt","image":null}}}`
		}
		t.Fatalf("unexpected query: %s", body.Query)
		return http.StatusInternalServerError, `{}`
	})
	resolve := func(string) Store { return Store{ID: "store-us", ShopifyStore: "store-us"} }
	err := executeAltChanges(t.Context(), ioDiscard{}, commandOptions{maxAttempts: 1, pollInterval: time.Nanosecond}, []PlanChange{change}, resolve, &evidence, evidencePath, firstClient)
	if err == nil {
		t.Fatal("expected first alt readback to time out")
	}
	record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
	if record == nil || record.Alt.Status != stageStatusAwaitingReadback || record.Alt.Checkpoint != "alt_file_readback" || !record.Alt.MutationAccepted {
		t.Fatalf("missing alt readback checkpoint: %+v", record)
	}
	secondClient := testShopifyClient(func(req *http.Request) (int, string) {
		raw, _ := io.ReadAll(req.Body)
		var body struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &body)
		if strings.Contains(body.Query, "fileUpdate") {
			t.Fatal("alt recovery repeated fileUpdate")
		}
		return http.StatusOK, `{"data":{"node":{"id":"gid://shopify/MediaImage/1","fileStatus":"READY","alt":"Alt","image":{"url":"https://cdn.shopify.com/s/files/1/same.png","width":120,"height":80}}}}`
	})
	if err := executeAltChanges(t.Context(), ioDiscard{}, commandOptions{maxAttempts: 1, pollInterval: time.Nanosecond}, []PlanChange{change}, resolve, &evidence, evidencePath, secondClient); err != nil {
		t.Fatal(err)
	}
	if fileUpdates != 1 || record.Alt.Status != stageStatusSucceeded {
		t.Fatalf("unexpected alt recovery: updates=%d record=%+v", fileUpdates, record)
	}
}

func TestTranslationReadbackRecoveryDoesNotRepeatMutation(t *testing.T) {
	setTestAdminToken(t, "store-us")
	change := PlanChange{Store: "store-us", RowNo: "1", TargetFilename: "same.png", Actions: []string{"translation_update"}, Translations: map[string]string{"de": "DE alt"}}
	evidence := Evidence{RunID: "run-test", Results: []EvidenceRecord{{
		Store: change.Store, RowNo: change.RowNo, TargetFilename: change.TargetFilename, FileID: "gid://shopify/MediaImage/1", MediaGID: "gid://shopify/MediaImage/1", Status: "ERROR",
		Alt: StageEvidence{Status: stageStatusAwaitingReadback, Checkpoint: "translation_readback", MutationAccepted: true, Retryable: true},
	}}}
	evidencePath := filepath.Join(t.TempDir(), "evidence.json")
	mustWriteJSON(t, evidencePath, evidence)
	client := testShopifyClient(func(req *http.Request) (int, string) {
		raw, _ := io.ReadAll(req.Body)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &body)
		if strings.Contains(body.Query, "translationsRegister") || strings.Contains(body.Query, "fileUpdate") {
			t.Fatal("translation readback recovery repeated a mutation")
		}
		if strings.Contains(body.Query, "shopLocales") {
			return http.StatusOK, `{"data":{"shopLocales":[{"locale":"en","primary":true,"published":true},{"locale":"de","primary":false,"published":true}]}}`
		}
		if strings.Contains(body.Query, "translatableResource") {
			return http.StatusOK, `{"data":{"translatableResource":{"resourceId":"gid://shopify/MediaImage/1","translatableContent":[{"key":"alt","value":"Alt","digest":"digest-123","locale":"en"}],"translations":[{"key":"alt","value":"DE alt","locale":"de","outdated":false}]}}}`
		}
		t.Fatalf("unexpected query: %s", body.Query)
		return http.StatusInternalServerError, `{}`
	})
	resolve := func(string) Store { return Store{ID: "store-us", ShopifyStore: "store-us"} }
	if err := executeAltChanges(t.Context(), ioDiscard{}, commandOptions{}, []PlanChange{change}, resolve, &evidence, evidencePath, client); err != nil {
		t.Fatal(err)
	}
	record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
	if record == nil || record.Alt.Status != stageStatusSucceeded || record.TranslationReadback["de"].Value != "DE alt" {
		t.Fatalf("translation readback did not recover: %+v", record)
	}
}

func TestRegisterAltTranslationsUsesTranslatableContentDigest(t *testing.T) {
	setTestAdminToken(t, "store-us")
	var sawRegister bool
	client := testShopifyClient(func(req *http.Request) (int, string) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(body.Query, "shopLocales"):
			return http.StatusOK, `{"data":{"shopLocales":[{"locale":"en","primary":true,"published":true},{"locale":"de","primary":false,"published":true}]}}`
		case strings.Contains(body.Query, "translatableResource"):
			locale, _ := body.Variables["locale"].(string)
			return http.StatusOK, fmt.Sprintf(`{"data":{"translatableResource":{"resourceId":"gid://shopify/MediaImage/1","translatableContent":[{"key":"alt","value":"Alt","digest":"digest-123","locale":"en"}],"translations":[{"key":"alt","value":"DE alt","locale":%q,"outdated":false}]}}}`, locale)
		case strings.Contains(body.Query, "translationsRegister"):
			sawRegister = true
			translations, ok := body.Variables["translations"].([]any)
			if !ok || len(translations) != 1 {
				t.Fatalf("unexpected translations variable: %#v", body.Variables["translations"])
			}
			first, ok := translations[0].(map[string]any)
			if !ok {
				t.Fatalf("unexpected translation input: %#v", translations[0])
			}
			if _, ok := first["digest"]; ok {
				t.Fatalf("TranslationInput must not use legacy digest field: %#v", first)
			}
			if first["translatableContentDigest"] != "digest-123" {
				t.Fatalf("missing translatableContentDigest: %#v", first)
			}
			return http.StatusOK, `{"data":{"translationsRegister":{"translations":[{"key":"alt","value":"DE alt","locale":"de","outdated":false}],"userErrors":[]}}}`
		default:
			t.Fatalf("unexpected graphql query: %s", body.Query)
			return http.StatusInternalServerError, `{}`
		}
	})
	readbacks, err := client.RegisterAltTranslations(t.Context(), Store{ID: "store-us", ShopifyStore: "store-us"}, "gid://shopify/MediaImage/1", map[string]string{"de": "DE alt"})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRegister {
		t.Fatal("expected translationsRegister call")
	}
	if readbacks["de"].Value == "" {
		t.Fatalf("expected translation readback, got %#v", readbacks)
	}
}

func TestVerifyAltTranslationsRejectsMismatchedValueAndOutdatedReadback(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		outdated bool
		want     string
	}{
		{name: "wrong value", value: "Old alt", want: "alt readback mismatch"},
		{name: "outdated", value: "New alt", outdated: true, want: "outdated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestAdminToken(t, "store-de")
			client := testShopifyClient(func(req *http.Request) (int, string) {
				raw, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				var body struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				switch {
				case strings.Contains(body.Query, "shopLocales"):
					return http.StatusOK, `{"data":{"shopLocales":[{"locale":"en","primary":true,"published":true},{"locale":"de","primary":false,"published":true}]}}`
				case strings.Contains(body.Query, "translatableResource"):
					locale, _ := body.Variables["locale"].(string)
					return http.StatusOK, fmt.Sprintf(`{"data":{"translatableResource":{"resourceId":"gid://shopify/MediaImage/1","translatableContent":[{"key":"alt","value":"Alt","digest":"digest-123","locale":"en"}],"translations":[{"key":"alt","value":%q,"locale":%q,"outdated":%t}]}}}`, tt.value, locale, tt.outdated)
				default:
					t.Fatalf("unexpected graphql query: %s", body.Query)
					return http.StatusInternalServerError, `{}`
				}
			})
			_, err := client.VerifyAltTranslations(t.Context(), Store{ID: "store-de", ShopifyStore: "store-de"}, "gid://shopify/MediaImage/1", map[string]string{"de": "New alt"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestShopLocalesCachesPerStore(t *testing.T) {
	setTestAdminToken(t, "store-us", "store-de")
	localeQueries := map[string]int{}
	translationQueries := map[string]int{}
	client := testShopifyClient(func(req *http.Request) (int, string) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		storeID := strings.TrimSuffix(req.URL.Host, ".shopify.test")
		switch {
		case strings.Contains(body.Query, "shopLocales"):
			localeQueries[storeID]++
			if storeID == "store-de" {
				return http.StatusOK, `{"data":{"shopLocales":[{"locale":"en","primary":true,"published":true},{"locale":"de","primary":false,"published":true}]}}`
			}
			return http.StatusOK, `{"data":{"shopLocales":[{"locale":"en","primary":true,"published":true},{"locale":"de","primary":false,"published":false}]}}`
		case strings.Contains(body.Query, "translatableResource"):
			locale, _ := body.Variables["locale"].(string)
			if storeID != "store-de" || locale != "de" {
				t.Fatalf("unexpected translation readback store=%s locale=%s query=%s", storeID, locale, body.Query)
			}
			translationQueries[storeID]++
			return http.StatusOK, `{"data":{"translatableResource":{"resourceId":"gid://shopify/MediaImage/1","translatableContent":[{"key":"alt","value":"Alt","digest":"digest-123","locale":"en"}],"translations":[{"key":"alt","value":"DE alt","locale":"de","outdated":false}]}}}`
		default:
			t.Fatalf("unexpected graphql query: %s", body.Query)
			return http.StatusInternalServerError, `{}`
		}
	})
	client = NewShopifyClient(
		client.HTTPClient(),
		withShopifyEndpoints(
			func(store Store) string {
				return fmt.Sprintf("https://%s.shopify.test/admin/oauth/access_token", store.ID)
			},
			func(store Store) string { return fmt.Sprintf("https://%s.shopify.test/admin/graphql.json", store.ID) },
		),
		withShopifyBackoff(func(context.Context, int) error { return nil }),
	)
	stores := []Store{
		{ID: "store-us", ShopifyStore: "store-us"},
		{ID: "store-us", ShopifyStore: "store-us"},
		{ID: "store-de", ShopifyStore: "store-de"},
		{ID: "store-us", ShopifyStore: "store-us"},
	}
	for _, store := range stores {
		readbacks, err := client.VerifyAltTranslations(t.Context(), store, "gid://shopify/MediaImage/1", map[string]string{
			"en": "EN alt",
			"de": "DE alt",
			"ja": "JA alt",
		})
		if err != nil {
			t.Fatal(err)
		}
		if store.ID == "store-de" {
			if readbacks["de"].Value != "DE alt" {
				t.Fatalf("expected de readback for %s, got %#v", store.ID, readbacks["de"])
			}
			for _, locale := range []string{"en", "ja"} {
				if readbacks[locale].Status != "SKIPPED" {
					t.Fatalf("expected %s to be skipped for %s, got %#v", locale, store.ID, readbacks[locale])
				}
			}
		} else {
			for _, locale := range []string{"en", "de", "ja"} {
				if readbacks[locale].Status != "SKIPPED" {
					t.Fatalf("expected %s to be skipped for %s, got %#v", locale, store.ID, readbacks[locale])
				}
			}
		}
	}
	if localeQueries["store-us"] != 1 || localeQueries["store-de"] != 1 {
		t.Fatalf("expected one shopLocales query per store, got %#v", localeQueries)
	}
	if translationQueries["store-us"] != 0 || translationQueries["store-de"] != 1 {
		t.Fatalf("unexpected translation readback queries: %#v", translationQueries)
	}
}

func TestFilterRegisterableTranslationsSkipsPrimaryAndUnavailableLocales(t *testing.T) {
	registerable, readbacks := filterRegisterableTranslations(
		map[string]string{
			"en": "EN alt",
			"de": "DE alt",
			"ja": "JA alt",
			"fr": "FR alt",
		},
		[]ShopLocale{
			{Locale: "en", Primary: true, Published: true},
			{Locale: "de", Published: true},
			{Locale: "fr", Published: false},
		},
	)
	if !reflect.DeepEqual(registerable, map[string]string{"de": "DE alt"}) {
		t.Fatalf("unexpected registerable translations: %#v", registerable)
	}
	for _, locale := range []string{"en", "ja", "fr"} {
		if readbacks[locale].Status != "SKIPPED" || readbacks[locale].Reason == "" {
			t.Fatalf("expected skipped readback for %s, got %#v", locale, readbacks[locale])
		}
	}
}

func TestJsonReplaceStillReserved(t *testing.T) {
	err := run(t.Context(), []string{"json-replace", "--execute"}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "尚未实现") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func writeFakeLarkCLI(t *testing.T, stderr string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "lark-cli")
	script := fmt.Sprintf("#!/bin/sh\ncat >&2 <<'EOF'\n%s\nEOF\nexit 7\n", stderr)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func prettyJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func setTestAdminToken(t *testing.T, storeIDs ...string) {
	t.Helper()
	t.Setenv("SHOPIFY_CLIENT_ID", "")
	t.Setenv("SHOPIFY_CLIENT_SECRET", "")
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "test-token")
	for _, id := range storeIDs {
		suffix := envSuffix(id)
		t.Setenv("SHOPIFY_CLIENT_ID_"+suffix, "")
		t.Setenv("SHOPIFY_CLIENT_SECRET_"+suffix, "")
		t.Setenv("SHOPIFY_ADMIN_TOKEN_"+suffix, "")
	}
}

func testShopifyClient(handler func(*http.Request) (int, string)) *ShopifyClient {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status, body := handler(req)
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	return NewShopifyClient(
		httpClient,
		withShopifyEndpoints(
			func(Store) string { return "https://shopify.test/admin/oauth/access_token" },
			func(Store) string { return "https://shopify.test/admin/graphql.json" },
		),
		withShopifyBackoff(func(context.Context, int) error { return nil }),
	)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func readyNodeResponse(id string) string {
	return fmt.Sprintf(`{"data":{"node":{"id":%q,"fileStatus":"READY","alt":"Alt","image":{"url":"https://cdn.shopify.com/s/files/1/same.png","width":120,"height":80}}}}`, id)
}

func filenameFromGraphQLVariables(variables map[string]any, key string) string {
	items, _ := variables[key].([]any)
	if len(items) == 0 {
		return ""
	}
	first, _ := items[0].(map[string]any)
	filename, _ := first["filename"].(string)
	return filename
}

func createMinimalXLSX(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	addZipFile(t, zw, "xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets>
</workbook>`)
	addZipFile(t, zw, "xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Target="worksheets/sheet1.xml"/>
</Relationships>`)
	addZipFile(t, zw, "xl/sharedStrings.xml", `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>序号</t></si><si><t>source图片名</t></si><si><t>target图片文件名</t></si><si><t>en</t></si><si><t>jp</t></si>
  <si><t>1</t></si><si><t>same.png</t></si><si><t>EN</t></si><si><t>JP</t></si>
</sst>`)
	addZipFile(t, zw, "xl/worksheets/sheet1.xml", `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" t="s"><v>3</v></c><c r="E1" t="s"><v>4</v></c></row>
    <row r="2"><c r="A2" t="s"><v>5</v></c><c r="B2" t="s"><v>6</v></c><c r="D2" t="s"><v>7</v></c><c r="E2" t="s"><v>8</v></c></row>
  </sheetData>
</worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func addZipFile(t *testing.T, zw *zip.Writer, name, content string) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}

func tinyPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x62, 0x60, 0x60, 0x60, 0x00,
		0x00, 0x00, 0x04, 0x00, 0x01, 0xf6, 0x17, 0x38, 0x55, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
