package mediasync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const videoCopyEvidenceFilename = "video-copy-evidence.json"
const videoCopyReceiptFilename = "video-copy-receipt.json"

// VideoCopyEvidence is intentionally separate from image evidence. Video files are
// a JSON fallback prerequisite and are not part of the operations image Sheet.
type VideoCopyEvidence struct {
	Version   int               `json:"version"`
	RunID     string            `json:"run_id"`
	DryRun    bool              `json:"dry_run"`
	FromStore string            `json:"from_store"`
	Results   []VideoCopyRecord `json:"results"`
}

type VideoCopyRecord struct {
	FromStore       string `json:"from_store"`
	TargetStore     string `json:"target_store"`
	Filename        string `json:"filename"`
	Action          string `json:"action"`
	SourceFileID    string `json:"source_file_id,omitempty"`
	TargetFileID    string `json:"target_file_id,omitempty"`
	FinalFileStatus string `json:"final_file_status,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

type VideoCopyReceipt struct {
	Version   int               `json:"version"`
	Verified  bool              `json:"verified"`
	DryRun    bool              `json:"dry_run"`
	FromStore string            `json:"from_store"`
	Results   []VideoCopyRecord `json:"results"`
}

type sourceVideo struct {
	Node ShopifyFileNode
	URL  string
	Mime string
}

func runVideoCopy(ctx context.Context, stdout io.Writer, opts commandOptions) error {
	filenames, err := loadVideoManifest(opts.videoManifest)
	if err != nil {
		return err
	}
	fromStore, targetStores, err := resolveVideoCopyStores(opts)
	if err != nil {
		return err
	}
	if err := loadEnvForRemoteCommand(opts); err != nil {
		return err
	}
	runDir, runID, err := resolveRunDir(opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	evidencePath := filepath.Join(runDir, videoCopyEvidenceFilename)
	receiptPath := filepath.Join(runDir, videoCopyReceiptFilename)
	client := newShopifyClient(nil)

	fmt.Fprintf(stdout, "dry_run=%t\n", !opts.execute)
	storeByID := map[string]Store{fromStore.ID: fromStore}
	targetAPIStoreIDs := []string{fromStore.ID}
	for _, store := range targetStores {
		storeByID[store.ID] = store
		targetAPIStoreIDs = append(targetAPIStoreIDs, store.ID)
	}
	if err := writeTargetAPI(stdout, targetAPIStoreIDs, func(id string) Store { return storeByID[id] }); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "source_store=%s\n", fromStore.ID)
	fmt.Fprintf(stdout, "target_stores=%s\n", joinStoreIDs(targetStores))
	fmt.Fprintf(stdout, "data_scope=video_manifest:%s mp4_files:%d\n", opts.videoManifest, len(filenames))

	sources := make(map[string]sourceVideo, len(filenames))
	for _, filename := range filenames {
		node, findErr := client.FindVideoByFilename(ctx, fromStore, filename)
		if findErr != nil {
			return writeVideoCopyFailure(evidencePath, receiptPath, runID, opts, fromStore.ID, nil, fmt.Errorf("读取 source video %s 失败: %w", filename, findErr))
		}
		node, findErr = client.PollVideoReady(ctx, fromStore, node.ID, opts.maxAttempts, opts.pollInterval)
		if findErr != nil {
			return writeVideoCopyFailure(evidencePath, receiptPath, runID, opts, fromStore.ID, nil, fmt.Errorf("source video %s 未 READY: %w", filename, findErr))
		}
		url := videoSourceURL(node)
		if url == "" {
			return writeVideoCopyFailure(evidencePath, receiptPath, runID, opts, fromStore.ID, nil, fmt.Errorf("source video %s 缺少 MP4 source URL", filename))
		}
		sources[filename] = sourceVideo{Node: node, URL: url, Mime: "video/mp4"}
	}

	evidence := VideoCopyEvidence{Version: 1, RunID: runID, DryRun: !opts.execute, FromStore: fromStore.ID, Results: []VideoCopyRecord{}}
	downloaded := map[string]ResourceInfo{}
	var failures []error
	for _, target := range targetStores {
		for _, filename := range filenames {
			source := sources[filename]
			record := VideoCopyRecord{
				FromStore: fromStore.ID, TargetStore: target.ID, Filename: filename,
				SourceFileID: source.Node.ID, UpdatedAt: nowUTC(),
			}
			finalRecord, copyErr := executeVideoCopyRecord(ctx, client, opts, runDir, target, source, record, downloaded)
			evidence.Results = append(evidence.Results, finalRecord)
			if copyErr != nil {
				failures = append(failures, copyErr)
				fmt.Fprintf(stdout, "%s %s action=%s ERROR %s\n", target.ID, filename, finalRecord.Action, copyErr)
				continue
			}
			fmt.Fprintf(stdout, "%s %s action=%s status=%s\n", target.ID, filename, finalRecord.Action, finalRecord.FinalFileStatus)
		}
	}
	if err := writeJSON(evidencePath, evidence); err != nil {
		return err
	}
	receipt := VideoCopyReceipt{Version: 1, Verified: opts.execute && len(failures) == 0, DryRun: !opts.execute, FromStore: fromStore.ID, Results: evidence.Results}
	if err := writeJSON(receiptPath, receipt); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "evidence=%s\nreceipt=%s\n", evidencePath, receiptPath)
	if len(failures) > 0 {
		return fmt.Errorf("video-copy 完成但 %d/%d 项失败，详见 evidence=%s: %w", len(failures), len(evidence.Results), evidencePath, errors.Join(failures...))
	}
	if !opts.execute {
		fmt.Fprintln(stdout, "rerun_with=--execute")
	}
	return nil
}

func executeVideoCopyRecord(ctx context.Context, client *ShopifyClient, opts commandOptions, runDir string, target Store, source sourceVideo, record VideoCopyRecord, downloaded map[string]ResourceInfo) (VideoCopyRecord, error) {
	existing, err := client.FindVideoByFilename(ctx, target, record.Filename)
	if err == nil {
		record.TargetFileID = existing.ID
		record.FinalFileStatus = existing.FileStatus
		if videoNodeReady(existing) {
			record.Action = "reuse"
			return record, nil
		}
		record.Action = "await_ready"
		if !opts.execute {
			return record, nil
		}
		ready, pollErr := client.PollVideoReady(ctx, target, existing.ID, opts.maxAttempts, opts.pollInterval)
		if pollErr != nil {
			record.LastError = sanitizeVideoCopyError(pollErr)
			return record, pollErr
		}
		record.FinalFileStatus = ready.FileStatus
		return record, nil
	}
	if !errors.Is(err, errShopifyFileNotFound) {
		record.Action = "error"
		record.LastError = sanitizeVideoCopyError(err)
		return record, err
	}
	record.Action = "upload"
	if !opts.execute {
		record.FinalFileStatus = "MISSING"
		return record, nil
	}
	resource, ok := downloaded[record.Filename]
	if !ok {
		resource, err = downloadVideoResource(ctx, client.HTTPClient(), source, record.Filename, filepath.Join(runDir, "downloads", "videos"))
		if err != nil {
			record.LastError = sanitizeVideoCopyError(err)
			return record, err
		}
		downloaded[record.Filename] = resource
	}
	staged, err := client.CreateVideoStagedUpload(ctx, target, resource, record.Filename)
	if err != nil {
		record.LastError = sanitizeVideoCopyError(err)
		return record, err
	}
	if err := client.UploadToStagedTarget(ctx, staged, resource); err != nil {
		record.LastError = sanitizeVideoCopyError(err)
		return record, err
	}
	node, err := client.CreateVideoFile(ctx, target, staged.ResourceURL, record.Filename)
	if err != nil {
		record.LastError = sanitizeVideoCopyError(err)
		return record, err
	}
	record.TargetFileID = node.ID
	ready, err := client.PollVideoReady(ctx, target, node.ID, opts.maxAttempts, opts.pollInterval)
	if err != nil {
		record.LastError = sanitizeVideoCopyError(err)
		return record, err
	}
	if err = validateVideoReadbackFilename(target, record.Filename, ready); err != nil {
		record.LastError = sanitizeVideoCopyError(err)
		return record, err
	}
	record.FinalFileStatus = ready.FileStatus
	return record, nil
}

func validateVideoReadbackFilename(target Store, expected string, node ShopifyFileNode) error {
	if actual := shopifyFileNodeFilename(node); actual != expected {
		return fmt.Errorf("%s video readback filename 漂移: expected=%s actual=%s", target.ID, expected, actual)
	}
	return nil
}

var videoCopyURLPattern = regexp.MustCompile(`(?i)(https?:)?//[^\s"']+`)

func sanitizeVideoCopyError(err error) string {
	if err == nil {
		return ""
	}
	return videoCopyURLPattern.ReplaceAllString(err.Error(), "<redacted-url>")
}

func loadVideoManifest(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 video manifest 失败: %w", err)
	}
	var filenames []string
	if err := json.Unmarshal(raw, &filenames); err != nil {
		return nil, fmt.Errorf("解析 video manifest 失败（必须为 filename JSON 数组）: %w", err)
	}
	seen := map[string]bool{}
	for _, filename := range filenames {
		if filename != filepath.Base(filename) || !strings.EqualFold(filepath.Ext(filename), ".mp4") {
			return nil, fmt.Errorf("video manifest 仅支持安全的 .mp4 filename: %q", filename)
		}
		if seen[filename] {
			return nil, fmt.Errorf("video manifest 包含重复 filename: %s", filename)
		}
		seen[filename] = true
	}
	if len(filenames) == 0 {
		return nil, errors.New("video manifest 不能为空")
	}
	sort.Strings(filenames)
	return filenames, nil
}

func resolveVideoCopyStores(opts commandOptions) (Store, []Store, error) {
	cfg, err := loadStoresConfig(opts.storesConfig)
	if err != nil {
		return Store{}, nil, err
	}
	sources, err := selectStores(cfg, opts.fromStore)
	if err != nil || len(sources) != 1 {
		if err == nil {
			err = errors.New("--from-store 必须解析为一个 store")
		}
		return Store{}, nil, err
	}
	targets, err := selectStores(cfg, opts.stores)
	if err != nil {
		return Store{}, nil, err
	}
	filtered := make([]Store, 0, len(targets))
	for _, target := range targets {
		if target.ID != sources[0].ID {
			filtered = append(filtered, target)
		}
	}
	if len(filtered) == 0 {
		return Store{}, nil, errors.New("video-copy 没有目标 store（source store 会自动排除）")
	}
	return sources[0], filtered, nil
}

func joinStoreIDs(stores []Store) string {
	ids := make([]string, 0, len(stores))
	for _, store := range stores {
		ids = append(ids, store.ID)
	}
	return strings.Join(ids, ",")
}

func downloadVideoResource(ctx context.Context, httpClient *http.Client, source sourceVideo, filename, dir string) (ResourceInfo, error) {
	if !strings.EqualFold(source.Mime, "video/mp4") {
		return ResourceInfo{}, fmt.Errorf("source video %s MIME 不是 video/mp4: %s", filename, source.Mime)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ResourceInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return ResourceInfo{}, err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return ResourceInfo{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ResourceInfo{}, fmt.Errorf("下载 source video HTTP %d", res.StatusCode)
	}
	path := filepath.Join(dir, filename)
	temp, err := os.CreateTemp(dir, ".video-copy-*")
	if err != nil {
		return ResourceInfo{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	hash := sha256.New()
	bytes, copyErr := io.Copy(io.MultiWriter(temp, hash), res.Body)
	closeErr := temp.Close()
	if copyErr != nil {
		return ResourceInfo{}, copyErr
	}
	if closeErr != nil {
		return ResourceInfo{}, closeErr
	}
	if err := os.Rename(tempPath, path); err != nil {
		return ResourceInfo{}, err
	}
	return ResourceInfo{Filename: filename, Path: path, Bytes: bytes, SHA256: hex.EncodeToString(hash.Sum(nil)), MimeType: "video/mp4"}, nil
}

func writeVideoCopyFailure(evidencePath, receiptPath, runID string, opts commandOptions, fromStore string, results []VideoCopyRecord, cause error) error {
	evidence := VideoCopyEvidence{Version: 1, RunID: runID, DryRun: !opts.execute, FromStore: fromStore, Results: results}
	if err := writeJSON(evidencePath, evidence); err != nil {
		return fmt.Errorf("%w; 写 video-copy evidence 失败: %v", cause, err)
	}
	receipt := VideoCopyReceipt{Version: 1, Verified: false, DryRun: !opts.execute, FromStore: fromStore, Results: results}
	if err := writeJSON(receiptPath, receipt); err != nil {
		return fmt.Errorf("%w; 写 video-copy receipt 失败: %v", cause, err)
	}
	return cause
}
