package mediasync

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const usageText = `shopify-media-sync plans Shopify Files image upload/replacement and MEDIA_IMAGE alt updates.

Usage:
  shopify-media-sync --json doctor
  shopify-media-sync plan --input media.csv --source-root ./images --stores all
  shopify-media-sync plan --input media.xlsx --zip media.zip --stores store-us --template product.example
  shopify-media-sync plan --sheet-url <url> --sheet-name 图片 --drive-folder-token <token> --stores all
  shopify-media-sync plan --sheet-url <url> --sheet-name 图片 --stores us,uk --locales en,fr

Commands:
  doctor        Diagnose runtime, config, and auth readiness. Reads dotenv only with explicit --env-file; never calls Shopify.
  env-init      Initialize an ignored 0600 .env.local from .env.example or an explicit source. Never overwrites.
  env-check     Validate local env presence, permissions, and Shopify auth completeness without calling Shopify.
  plan          Build desired.json and plan.json. Does not write Shopify.
  inspect       Read one explicit plan/evidence/summary and print a stable recovery digest. Never writes Shopify or local artifacts.
  apply         Consume one explicit plan and run upload -> alt -> verify. Writes only with --execute.
  upload        Execute Shopify Files upload/replacement from plan.json. Writes only with --execute. Defaults to --concurrency 2; supports 1-3.
  alt           Execute MEDIA_IMAGE alt and translation updates from plan.json. Writes only with --execute.
  video-copy    Copy named MP4 Shopify Files videos between stores. Writes only with --execute.
  json-replace  Reserved for single template JSON replacement. Requires --execute when implemented.
  verify        Read back Shopify Files / translations and update local evidence.
  sync-status   Write a local status report from plan/evidence. Feishu writeback is still reserved.
`

type commandOptions struct {
	command  string
	pathBase string

	input            string
	planPath         string
	evidencePath     string
	summaryPath      string
	sheetURL         string
	spreadsheetToken string
	sheetName        string
	sheetID          string
	sheetRange       string

	driveFileToken   string
	driveFolderToken string

	sourceRoot    string
	zipPath       string
	outDir        string
	statePath     string
	resume        string
	jsonReceipt   string
	videoManifest string
	fromStore     string
	rootPath      string
	sourceEnv     string

	stores          string
	storesExplicit  bool
	locales         string
	storesConfig    string
	envFile         string
	noEnvFile       bool
	template        string
	targetTheme     string
	duplicatePolicy string
	format          string
	execute         bool
	dryRun          bool
	concurrency     int
	timeout         time.Duration
	pollInterval    time.Duration
	maxAttempts     int
}

// Run executes the CLI contract against the supplied process boundary.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return run(ctx, args, stdout)
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usageText)
		return nil
	}
	args = normalizeCommandArgs(args)

	opts, err := parseCommand(args)
	if err != nil {
		return err
	}
	if err := validateCommandOptions(opts); err != nil {
		return err
	}
	opts = resolveCommandPaths(opts)

	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	switch opts.command {
	case "doctor":
		return runDoctor(stdout, opts)
	case "env-init":
		return runEnvInit(stdout, opts)
	case "env-check":
		return runEnvCheck(stdout, opts)
	case "plan":
		return runPlan(ctx, stdout, opts)
	case "inspect":
		return runInspect(stdout, opts)
	case "apply":
		return runApply(ctx, stdout, opts)
	case "upload":
		return runUpload(ctx, stdout, opts)
	case "alt":
		return runAlt(ctx, stdout, opts)
	case "verify":
		return runVerify(ctx, stdout, opts)
	case "sync-status":
		return runSyncStatus(ctx, stdout, opts)
	case "video-copy":
		return runVideoCopy(ctx, stdout, opts)
	case "json-replace":
		return reservedCommand(opts)
	default:
		return fmt.Errorf("未知命令: %s\n%s", opts.command, usageText)
	}
}

func normalizeCommandArgs(args []string) []string {
	if len(args) < 2 || args[0] != "--json" {
		return args
	}
	normalized := append([]string{args[1]}, args[2:]...)
	return append(normalized, "--format", "json")
}

func parseCommand(args []string) (commandOptions, error) {
	opts := commandOptions{
		command:         args[0],
		pathBase:        callerPathBase(),
		stores:          "all",
		sheetRange:      "A:Z",
		duplicatePolicy: "REPLACE",
		format:          "text",
		concurrency:     2,
		timeout:         30 * time.Minute,
		pollInterval:    1500 * time.Millisecond,
		maxAttempts:     20,
		rootPath:        ".",
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.input, "input", "", "local CSV, XLSX, or desired JSON")
	fs.StringVar(&opts.planPath, "plan", "", "plan.json for inspect/apply/upload/alt/verify/sync-status")
	fs.StringVar(&opts.evidencePath, "evidence", "", "evidence.json to read/update")
	fs.StringVar(&opts.summaryPath, "summary", "", "apply summary JSON path")
	fs.StringVar(&opts.sheetURL, "sheet-url", "", "Feishu spreadsheet URL")
	fs.StringVar(&opts.sheetURL, "url", "", "alias of --sheet-url")
	fs.StringVar(&opts.spreadsheetToken, "spreadsheet-token", "", "Feishu spreadsheet token")
	fs.StringVar(&opts.sheetName, "sheet-name", "", "Feishu sheet name")
	fs.StringVar(&opts.sheetID, "sheet-id", "", "Feishu sheet id")
	fs.StringVar(&opts.sheetRange, "range", opts.sheetRange, "sheet range for Feishu CSV read")
	fs.StringVar(&opts.driveFileToken, "drive-file-token", "", "Feishu Drive file token for resource package")
	fs.StringVar(&opts.driveFolderToken, "drive-folder-token", "", "Feishu Drive folder token for resources")
	fs.StringVar(&opts.sourceRoot, "source-root", "", "local image source root")
	fs.StringVar(&opts.zipPath, "zip", "", "local zip resource package")
	fs.StringVar(&opts.outDir, "out-dir", "", "run output directory")
	fs.StringVar(&opts.statePath, "state", "", "previous evidence.json")
	fs.StringVar(&opts.resume, "resume", "", "resume mode; currently supports last")
	fs.StringVar(&opts.jsonReceipt, "json-receipt", "", "verified mapping-level receipt from an external theme JSON workflow")
	fs.StringVar(&opts.videoManifest, "video-manifest", "", "JSON array of MP4 filenames for video-copy")
	fs.StringVar(&opts.fromStore, "from-store", "", "source store id for video-copy")
	fs.StringVar(&opts.rootPath, "root", opts.rootPath, "project root for env-init/env-check")
	fs.StringVar(&opts.sourceEnv, "source", "", "explicit dotenv source for env-init whitelist import")
	fs.StringVar(&opts.stores, "stores", opts.stores, "store ids, aliases, or all")
	fs.StringVar(&opts.locales, "locales", "", "comma-separated locale columns to include in this plan; use none to ignore locale columns")
	fs.StringVar(&opts.storesConfig, "stores-config", "", "path to stores.config.json")
	fs.StringVar(&opts.envFile, "env-file", "", "optional .env.local path for Shopify remote commands")
	fs.BoolVar(&opts.noEnvFile, "no-env-file", false, "do not load .env.local")
	fs.StringVar(&opts.template, "template", "", "single template scope, for example product.example")
	fs.StringVar(&opts.targetTheme, "target-theme", "", "target theme for future json-replace")
	fs.StringVar(&opts.targetTheme, "to-theme", "", "alias of --target-theme")
	fs.StringVar(&opts.duplicatePolicy, "duplicate-policy", opts.duplicatePolicy, "REPLACE, APPEND_UUID, or ERROR")
	fs.StringVar(&opts.format, "format", opts.format, "text or json")
	fs.BoolVar(&opts.execute, "execute", false, "required for future remote writes")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "preview local initialization without writing")
	fs.IntVar(&opts.concurrency, "concurrency", opts.concurrency, "upload worker concurrency; only upload uses this flag")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "operation timeout")
	fs.DurationVar(&opts.pollInterval, "poll-interval", opts.pollInterval, "Shopify file readback poll interval")
	fs.IntVar(&opts.maxAttempts, "max-attempts", opts.maxAttempts, "Shopify file readback max attempts")
	if err := fs.Parse(args[1:]); err != nil {
		return opts, err
	}
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == "stores" {
			opts.storesExplicit = true
		}
	})
	return opts, nil
}

func callerPathBase() string {
	return strings.TrimSpace(os.Getenv("MEDIA_SYNC_CALLER_CWD"))
}

func resolveCommandPaths(opts commandOptions) commandOptions {
	opts.input = resolveUserPath(opts, opts.input)
	opts.planPath = resolveUserPath(opts, opts.planPath)
	opts.evidencePath = resolveUserPath(opts, opts.evidencePath)
	opts.summaryPath = resolveUserPath(opts, opts.summaryPath)
	opts.sourceRoot = resolveUserPath(opts, opts.sourceRoot)
	opts.zipPath = resolveUserPath(opts, opts.zipPath)
	opts.outDir = resolveUserPath(opts, opts.outDir)
	opts.statePath = resolveUserPath(opts, opts.statePath)
	opts.jsonReceipt = resolveUserPath(opts, opts.jsonReceipt)
	opts.rootPath = resolveUserPath(opts, opts.rootPath)
	opts.sourceEnv = resolveUserPath(opts, opts.sourceEnv)
	opts.storesConfig = resolveUserPath(opts, opts.storesConfig)
	if opts.storesConfig == "" {
		start := opts.pathBase
		if start == "" {
			start, _ = os.Getwd()
		}
		if found, err := findUpFrom(start, "stores.config.json"); err == nil {
			opts.storesConfig = found
		}
	}
	opts.envFile = resolveUserPath(opts, opts.envFile)
	opts.videoManifest = resolveUserPath(opts, opts.videoManifest)
	return opts
}

func resolveUserPath(opts commandOptions, path string) string {
	if path == "" || filepath.IsAbs(path) || opts.pathBase == "" {
		return path
	}
	return filepath.Join(opts.pathBase, path)
}

func defaultRunsRoot(opts commandOptions) string {
	return resolveUserPath(opts, filepath.Join(".runtime", "media-sync", "_plan"))
}

func validateCommandOptions(opts commandOptions) error {
	switch opts.command {
	case "doctor", "env-init", "env-check", "plan", "inspect", "apply", "upload", "alt", "json-replace", "verify", "sync-status", "video-copy":
	default:
		return fmt.Errorf("未知命令: %s\n%s", opts.command, usageText)
	}
	if opts.format != "text" && opts.format != "json" {
		return fmt.Errorf("--format 只支持 text 或 json")
	}
	if opts.command == "doctor" && opts.execute {
		return errors.New("doctor 不接受 --execute；doctor 永远只执行本地只读诊断")
	}
	if (opts.command == "env-init" || opts.command == "env-check") && opts.execute {
		return fmt.Errorf("%s 不接受 --execute", opts.command)
	}
	if opts.envFile != "" && opts.noEnvFile {
		return errors.New("--env-file 与 --no-env-file 互斥")
	}
	if opts.command != "env-init" && opts.sourceEnv != "" {
		return errors.New("--source 只用于 env-init")
	}
	if opts.command != "env-init" && opts.dryRun {
		return errors.New("--dry-run 只用于 env-init")
	}
	if opts.resume != "" && opts.resume != "last" {
		return fmt.Errorf("--resume 当前只支持 last")
	}
	if _, err := parseLocaleFilter(opts.locales); err != nil {
		return err
	}
	if _, err := normalizeDuplicatePolicy(opts.duplicatePolicy); err != nil {
		return err
	}
	if opts.concurrency < 1 {
		return fmt.Errorf("--concurrency 必须 >= 1")
	}
	if (opts.command == "upload" || opts.command == "apply") && opts.concurrency > 3 {
		return fmt.Errorf("%s --concurrency 只支持 1-3", opts.command)
	}
	if opts.command == "video-copy" {
		if strings.TrimSpace(opts.fromStore) == "" {
			return errors.New("video-copy 必须指定 --from-store")
		}
		if opts.videoManifest == "" {
			return errors.New("video-copy 必须指定 --video-manifest <filenames.json>")
		}
		if opts.execute && (!opts.storesExplicit || storeSpecMeansAll(opts.stores)) {
			return errors.New("video-copy --execute 必须显式指定非 all 的 --stores")
		}
	}
	if opts.command == "plan" {
		if opts.execute {
			return errors.New("plan 不接受 --execute；plan 永远不执行 Shopify 远端写入")
		}
		hasLocalInput := opts.input != ""
		hasSheetInput := opts.sheetURL != "" || opts.spreadsheetToken != ""
		if hasLocalInput == hasSheetInput {
			return errors.New("plan 必须且只能指定一种输入：--input 或 --sheet-url/--spreadsheet-token")
		}
		if hasSheetInput {
			if (opts.sheetURL == "") == (opts.spreadsheetToken == "") {
				return errors.New("Feishu 输入必须在 --sheet-url 与 --spreadsheet-token 中二选一")
			}
			if (opts.sheetName == "") == (opts.sheetID == "") {
				return errors.New("Feishu 输入必须在 --sheet-name 与 --sheet-id 中二选一")
			}
		}
		if countResourceSources(opts) > 1 {
			return errors.New("资源包来源只能指定一种：--zip、--source-root、--drive-file-token、--drive-folder-token")
		}
	}
	if opts.command == "apply" {
		if opts.resume != "" {
			return errors.New("apply 不得使用 --resume 或自动选择最近 plan")
		}
		if strings.TrimSpace(opts.planPath) == "" {
			return errors.New("apply 必须显式指定 --plan <plan.json>")
		}
		if opts.input != "" || opts.outDir != "" {
			return errors.New("apply 只接受显式 --plan；不得通过 --input/--out-dir 推断或重新生成 plan")
		}
	}
	if opts.command == "inspect" {
		if opts.execute {
			return errors.New("inspect 不接受 --execute；inspect 永远只读")
		}
		if strings.TrimSpace(opts.planPath) == "" {
			return errors.New("inspect 必须显式指定 --plan <plan.json>")
		}
		if opts.input != "" || opts.outDir != "" || opts.resume != "" {
			return errors.New("inspect 只接受显式 --plan；不得推断最近一次 plan")
		}
	}
	return nil
}

func appendJSONLine(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func countResourceSources(opts commandOptions) int {
	count := 0
	for _, value := range []string{opts.zipPath, opts.sourceRoot, opts.driveFileToken, opts.driveFolderToken} {
		if value != "" {
			count++
		}
	}
	return count
}

func runPlan(ctx context.Context, stdout io.Writer, opts commandOptions) error {
	started := time.Now().UTC()
	runDir, runID, err := resolveRunDir(opts)
	if err != nil {
		return err
	}
	downloadsDir := filepath.Join(runDir, "downloads")
	if err := os.MkdirAll(downloadsDir, 0o755); err != nil {
		return err
	}

	if opts.driveFileToken != "" {
		downloaded, err := downloadDriveFile(ctx, opts.driveFileToken, downloadsDir)
		if err != nil {
			return err
		}
		opts.zipPath = downloaded
	}
	if opts.driveFolderToken != "" {
		if err := pullDriveFolder(ctx, opts.driveFolderToken, downloadsDir); err != nil {
			return err
		}
		opts.sourceRoot = downloadsDir
	}

	cfg, err := loadStoresConfig(opts.storesConfig)
	if err != nil {
		return err
	}
	stores, err := selectStores(cfg, opts.stores)
	if err != nil {
		return err
	}

	desired, err := loadDesired(ctx, opts)
	if err != nil {
		return err
	}
	desired.RunID = runID
	desired.GeneratedAt = time.Now().UTC().Format(time.RFC3339)

	resources, err := BuildResourceIndex(opts.sourceRoot, opts.zipPath, downloadsDir)
	if err != nil {
		return err
	}
	statePath, err := resolveStatePath(opts)
	if err != nil {
		return err
	}
	evidence, err := LoadEvidence(statePath)
	if err != nil {
		return err
	}
	plan := BuildPlan(PlanInput{
		RunID:       runID,
		GeneratedAt: desired.GeneratedAt,
		Stores:      stores,
		Desired:     desired,
		Resources:   resources,
		Evidence:    evidence,
		Template:    normalizeTemplateStem(opts.template),
		TargetTheme: opts.targetTheme,
	})

	if err := writeJSON(filepath.Join(runDir, "desired.json"), desired); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(runDir, "plan.json"), plan); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(runDir, "evidence.json"), InitialEvidenceFromPlan(runID, plan)); err != nil {
		return err
	}
	if err := WritePlanReport(filepath.Join(runDir, "report.csv"), plan); err != nil {
		return err
	}
	metricsPath := resolveMetricsPath(opts, filepath.Join(runDir, "evidence.json"))
	metrics := newRunMetrics("plan", runID, started, plan.Summary.Stores, plan.Summary.Rows, plan.Changes, 1, false)
	updateMetricsFromChanges(&metrics, plan.Changes)
	if err := writeMetricsFile(metricsPath, &metrics); err != nil {
		return err
	}

	if opts.format == "json" {
		return json.NewEncoder(stdout).Encode(plan)
	}
	if err := writePlanText(stdout, runDir, plan); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "metrics=%s\n", metricsPath)
	return nil
}

func reservedCommand(opts commandOptions) error {
	if opts.execute {
		return fmt.Errorf("%s 尚未实现；本阶段不会执行任何远端写入", opts.command)
	}
	return fmt.Errorf("%s 尚未实现；先运行 plan 生成 desired.json / plan.json", opts.command)
}

func resolveRunDir(opts commandOptions) (string, string, error) {
	return resolveRunDirAt(opts, time.Now().UTC())
}

func resolveRunDirAt(opts commandOptions, now time.Time) (string, string, error) {
	runID := generateRunID(now)
	if opts.outDir != "" {
		return filepath.Clean(opts.outDir), runID, nil
	}
	runDir := filepath.Join(defaultRunsRoot(opts), runID)
	if _, err := os.Stat(runDir); err == nil {
		return "", "", fmt.Errorf("run dir already exists: %s", runDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return runDir, runID, nil
}

func generateRunID(now time.Time) string {
	return "run-" + now.UTC().Format("20060102T150405.000000000Z")
}

func resolveStatePath(opts commandOptions) (string, error) {
	if opts.statePath != "" {
		return opts.statePath, nil
	}
	if opts.resume != "last" {
		return "", nil
	}
	root := defaultRunsRoot(opts)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
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
		path := filepath.Join(root, entry.Name(), "evidence.json")
		info, err := os.Stat(path)
		if err == nil {
			candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })
	return candidates[0].path, nil
}

func downloadDriveFile(ctx context.Context, token, downloadsDir string) (string, error) {
	output := filepath.Join(downloadsDir, "drive-resource-package.zip")
	args := []string{"drive", "+download", "--file-token", token, "--output", output, "--overwrite"}
	cmd := exec.CommandContext(ctx, "lark-cli", args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("lark-cli drive +download failed: %w\n%s", err, redactSensitiveOutput(string(raw), token))
	}
	return output, nil
}

func pullDriveFolder(ctx context.Context, token, downloadsDir string) error {
	args := []string{"drive", "+pull", "--folder-token", token, "--local-dir", downloadsDir, "--if-exists", "smart"}
	cmd := exec.CommandContext(ctx, "lark-cli", args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lark-cli drive +pull failed: %w\n%s", err, redactSensitiveOutput(string(raw), token))
	}
	return nil
}

func redactSensitiveOutput(output string, secrets ...string) string {
	redacted := output
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, secret, "<redacted>")
	}
	return redacted
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	// Sync the directory so a completed rename is durable across a crash.
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func normalizeTemplateStem(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".json")
	value = strings.TrimPrefix(value, "templates/")
	return value
}
