package shopifyadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"shopify-media-sync/internal/config"
	"shopify-media-sync/internal/mediainput"
	"shopify-media-sync/internal/runstate"
)

type Store = config.Store
type ResourceInfo = mediainput.ResourceInfo
type TranslationReadback = runstate.TranslationReadback

const APIVersion = config.DefaultShopifyAPIVersion

var newShopifyClient = NewShopifyClient

type ShopifyClient struct {
	httpClient      *http.Client
	cache           map[string]oauthToken
	localeCache     map[string][]ShopLocale
	oauthEndpoint   func(Store) string
	graphqlEndpoint func(Store) string
	backoff         func(context.Context, int) error
}

type oauthToken struct {
	token     string
	expiresAt time.Time
}

type ShopifyUserError struct {
	Field   []string `json:"field"`
	Message string   `json:"message"`
	Code    string   `json:"code"`
}

type StagedTarget struct {
	URL         string `json:"url"`
	ResourceURL string `json:"resourceUrl"`
	Parameters  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"parameters"`
}

type ShopifyFileNode struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	Type       string `json:"__typename"`
	FileStatus string `json:"fileStatus"`
	Alt        string `json:"alt"`
	CreatedAt  string `json:"createdAt"`
	Image      *struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"image"`
	OriginalVideoSource *VideoSource  `json:"originalSource"`
	VideoSources        []VideoSource `json:"sources"`
}

type VideoSource struct {
	URL      string `json:"url"`
	MimeType string `json:"mimeType"`
}

var ErrShopifyFileNotFound = errors.New("Shopify File not found")

type ShopLocale struct {
	Locale    string `json:"locale"`
	Primary   bool   `json:"primary"`
	Published bool   `json:"published"`
}

type ClientOption func(*ShopifyClient)

func WithEndpoints(oauth func(Store) string, graphql func(Store) string) ClientOption {
	return func(client *ShopifyClient) {
		client.oauthEndpoint = oauth
		client.graphqlEndpoint = graphql
	}
}

func WithBackoff(backoff func(context.Context, int) error) ClientOption {
	return func(client *ShopifyClient) { client.backoff = backoff }
}

func NewShopifyClient(httpClient *http.Client, options ...ClientOption) *ShopifyClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	client := &ShopifyClient{
		httpClient:  httpClient,
		cache:       map[string]oauthToken{},
		localeCache: map[string][]ShopLocale{},
		oauthEndpoint: func(store Store) string {
			return fmt.Sprintf("https://%s.myshopify.com/admin/oauth/access_token", store.ShopifyStore)
		},
		graphqlEndpoint: func(store Store) string {
			version, _ := APIVersionForStore(store)
			return fmt.Sprintf("https://%s.myshopify.com/admin/api/%s/graphql.json", store.ShopifyStore, version)
		},
		backoff: sleepBackoff,
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func APIVersionForStore(store Store) (string, error) {
	return config.ShopifyAPIVersion(store, os.Getenv)
}

func EnvSuffix(storeID string) string {
	return strings.ToUpper(strings.ReplaceAll(storeID, "-", "_"))
}

func (c *ShopifyClient) AdminToken(ctx context.Context, store Store) (string, error) {
	suffix := EnvSuffix(store.ID)
	psClientID := os.Getenv("SHOPIFY_CLIENT_ID_" + suffix)
	psClientSecret := os.Getenv("SHOPIFY_CLIENT_SECRET_" + suffix)
	globalClientID := os.Getenv("SHOPIFY_CLIENT_ID")
	globalClientSecret := os.Getenv("SHOPIFY_CLIENT_SECRET")
	storeToken := os.Getenv("SHOPIFY_ADMIN_TOKEN_" + suffix)
	globalToken := os.Getenv("SHOPIFY_ADMIN_TOKEN")

	if (psClientID == "") != (psClientSecret == "") {
		return "", fmt.Errorf("%s Admin API 鉴权半配置(per-store): SHOPIFY_CLIENT_ID_%s 与 SHOPIFY_CLIENT_SECRET_%s 必须成对", store.ID, suffix, suffix)
	}
	if (globalClientID == "") != (globalClientSecret == "") {
		return "", fmt.Errorf("Admin API 鉴权半配置(全局): SHOPIFY_CLIENT_ID 与 SHOPIFY_CLIENT_SECRET 必须成对")
	}
	if psClientID != "" {
		return c.oauthToken(ctx, store, psClientID, psClientSecret)
	}
	if globalClientID != "" {
		return c.oauthToken(ctx, store, globalClientID, globalClientSecret)
	}
	if storeToken != "" {
		return storeToken, nil
	}
	if globalToken != "" {
		return globalToken, nil
	}
	return "", fmt.Errorf("未找到 %s 的 Admin API 鉴权；请配置以下任一项：SHOPIFY_CLIENT_ID_%s/SHOPIFY_CLIENT_SECRET_%s、SHOPIFY_CLIENT_ID/SHOPIFY_CLIENT_SECRET、SHOPIFY_ADMIN_TOKEN_%s、SHOPIFY_ADMIN_TOKEN；token 不要写入 git", store.ID, suffix, suffix, suffix)
}

func (c *ShopifyClient) oauthToken(ctx context.Context, store Store, clientID, clientSecret string) (string, error) {
	if cached, ok := c.cache[store.ID]; ok && time.Now().Before(cached.expiresAt) {
		return cached.token, nil
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	endpoint := c.oauthEndpoint(store)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("%s OAuth POST 失败 HTTP %d: %s", store.ID, res.StatusCode, string(raw))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("%s OAuth 响应缺 access_token", store.ID)
	}
	expiresIn := parsed.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	c.cache[store.ID] = oauthToken{
		token:     parsed.AccessToken,
		expiresAt: time.Now().Add(time.Duration(expiresIn)*time.Second - time.Minute),
	}
	return parsed.AccessToken, nil
}

func (c *ShopifyClient) GraphQL(ctx context.Context, store Store, query string, variables any) (json.RawMessage, error) {
	if _, err := APIVersionForStore(store); err != nil {
		return nil, err
	}
	token, err := c.AdminToken(ctx, store)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	endpoint := c.graphqlEndpoint(store)
	isMutation := strings.HasPrefix(strings.TrimSpace(query), "mutation")
	var lastBody string
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Shopify-Access-Token", token)
		res, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		lastBody = string(raw)
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			if isMutation {
				return nil, fmt.Errorf("%s Admin GraphQL mutation HTTP %d，远端结果不确定且不会自动重试: %s", store.ID, res.StatusCode, lastBody)
			}
			if err := c.backoff(ctx, attempt); err != nil {
				return nil, err
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, fmt.Errorf("%s Admin GraphQL HTTP %d: %s", store.ID, res.StatusCode, lastBody)
		}
		var envelope struct {
			Data   json.RawMessage `json:"data"`
			Errors json.RawMessage `json:"errors"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, err
		}
		if len(envelope.Errors) > 0 && string(envelope.Errors) != "null" {
			if strings.Contains(string(envelope.Errors), "THROTTLED") {
				if isMutation {
					return nil, fmt.Errorf("%s Admin GraphQL mutation throttled，远端结果不确定且不会自动重试: %s", store.ID, string(envelope.Errors))
				}
				if err := c.backoff(ctx, attempt); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("%s Admin GraphQL errors: %s", store.ID, string(envelope.Errors))
		}
		return envelope.Data, nil
	}
	return nil, fmt.Errorf("%s Admin GraphQL 重试后仍失败: %s", store.ID, lastBody)
}

func sleepBackoff(ctx context.Context, attempt int) error {
	delay := time.Duration(500*(1<<attempt)) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *ShopifyClient) CreateStagedUpload(ctx context.Context, store Store, resource ResourceInfo, filename string) (StagedTarget, error) {
	return c.createStagedUploadForType(ctx, store, resource, filename, "IMAGE")
}

func (c *ShopifyClient) CreateVideoStagedUpload(ctx context.Context, store Store, resource ResourceInfo, filename string) (StagedTarget, error) {
	return c.createStagedUploadForType(ctx, store, resource, filename, "VIDEO")
}

func (c *ShopifyClient) createStagedUploadForType(ctx context.Context, store Store, resource ResourceInfo, filename, resourceType string) (StagedTarget, error) {
	input := map[string]any{
		"filename":   filename,
		"mimeType":   resource.MimeType,
		"resource":   resourceType,
		"httpMethod": "POST",
	}
	if resourceType == "VIDEO" {
		if resource.Bytes <= 0 {
			return StagedTarget{}, fmt.Errorf("video staged upload 缺少有效 fileSize: %s", filename)
		}
		input["fileSize"] = strconv.FormatInt(resource.Bytes, 10)
	}
	data, err := c.GraphQL(ctx, store, mutationStagedUploadsCreate, map[string]any{
		"input": []map[string]any{input},
	})
	if err != nil {
		return StagedTarget{}, err
	}
	var parsed struct {
		StagedUploadsCreate struct {
			StagedTargets []StagedTarget     `json:"stagedTargets"`
			UserErrors    []ShopifyUserError `json:"userErrors"`
		} `json:"stagedUploadsCreate"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return StagedTarget{}, err
	}
	if err := ensureNoUserErrors("stagedUploadsCreate", parsed.StagedUploadsCreate.UserErrors); err != nil {
		return StagedTarget{}, err
	}
	if len(parsed.StagedUploadsCreate.StagedTargets) == 0 {
		return StagedTarget{}, fmt.Errorf("stagedUploadsCreate 未返回 upload target: %s", filename)
	}
	target := parsed.StagedUploadsCreate.StagedTargets[0]
	if target.URL == "" || target.ResourceURL == "" {
		return StagedTarget{}, fmt.Errorf("stagedUploadsCreate target 缺 url/resourceUrl: %s", filename)
	}
	return target, nil
}

func (c *ShopifyClient) UploadToStagedTarget(ctx context.Context, target StagedTarget, resource ResourceInfo) error {
	file, err := os.Open(resource.Path)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	contentLength, err := stagedMultipartContentLength(target, filepath.Base(resource.Path), writer.Boundary(), info.Size())
	if err != nil {
		pipeReader.Close()
		pipeWriter.Close()
		file.Close()
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, pipeReader)
	if err != nil {
		pipeReader.Close()
		pipeWriter.Close()
		file.Close()
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.ContentLength = contentLength
	go streamStagedMultipart(pipeWriter, writer, file, target, filepath.Base(resource.Path))
	res, err := c.httpClient.Do(req)
	if err != nil {
		pipeReader.CloseWithError(err)
		return err
	}
	defer pipeReader.Close()
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("staged upload HTTP %d: %s", res.StatusCode, string(raw))
	}
	return nil
}

type countingWriter struct {
	bytes int64
}

func (writer *countingWriter) Write(value []byte) (int, error) {
	writer.bytes += int64(len(value))
	return len(value), nil
}

func stagedMultipartContentLength(target StagedTarget, filename, boundary string, fileSize int64) (int64, error) {
	counter := &countingWriter{}
	writer := multipart.NewWriter(counter)
	if err := writer.SetBoundary(boundary); err != nil {
		return 0, err
	}
	for _, parameter := range target.Parameters {
		if err := writer.WriteField(parameter.Name, parameter.Value); err != nil {
			return 0, err
		}
	}
	if _, err := writer.CreateFormFile("file", filename); err != nil {
		return 0, err
	}
	if err := writer.Close(); err != nil {
		return 0, err
	}
	return counter.bytes + fileSize, nil
}

func streamStagedMultipart(pipeWriter *io.PipeWriter, writer *multipart.Writer, file *os.File, target StagedTarget, filename string) {
	var streamErr error
	for _, parameter := range target.Parameters {
		if err := writer.WriteField(parameter.Name, parameter.Value); err != nil {
			streamErr = err
			break
		}
	}
	if streamErr == nil {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			streamErr = err
		} else if _, err := io.Copy(part, file); err != nil {
			streamErr = err
		}
	}
	if err := writer.Close(); streamErr == nil && err != nil {
		streamErr = err
	}
	if err := file.Close(); streamErr == nil && err != nil {
		streamErr = err
	}
	_ = pipeWriter.CloseWithError(streamErr)
}

func (c *ShopifyClient) CreateFile(ctx context.Context, store Store, resourceURL, filename, duplicatePolicy string) (ShopifyFileNode, error) {
	return c.createFileForType(ctx, store, resourceURL, filename, duplicatePolicy, "IMAGE")
}

func (c *ShopifyClient) CreateVideoFile(ctx context.Context, store Store, resourceURL, filename string) (ShopifyFileNode, error) {
	return c.createFileForType(ctx, store, resourceURL, filename, "RAISE_ERROR", "VIDEO")
}

func (c *ShopifyClient) createFileForType(ctx context.Context, store Store, resourceURL, filename, duplicatePolicy, contentType string) (ShopifyFileNode, error) {
	input := map[string]any{
		"originalSource": resourceURL,
		"contentType":    contentType,
	}
	if contentType != "VIDEO" {
		input["filename"] = filename
		input["duplicateResolutionMode"] = duplicatePolicy
	}
	data, err := c.GraphQL(ctx, store, mutationFileCreate, map[string]any{
		"files": []map[string]any{input},
	})
	if err != nil {
		return ShopifyFileNode{}, err
	}
	var parsed struct {
		FileCreate struct {
			Files      []ShopifyFileNode  `json:"files"`
			UserErrors []ShopifyUserError `json:"userErrors"`
		} `json:"fileCreate"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ShopifyFileNode{}, err
	}
	if err := ensureNoUserErrors("fileCreate", parsed.FileCreate.UserErrors); err != nil {
		return ShopifyFileNode{}, err
	}
	if len(parsed.FileCreate.Files) == 0 || parsed.FileCreate.Files[0].ID == "" {
		return ShopifyFileNode{}, fmt.Errorf("fileCreate 未返回 file id: %s", filename)
	}
	return parsed.FileCreate.Files[0], nil
}

func (c *ShopifyClient) UpdateFileOriginalSource(ctx context.Context, store Store, fileID, resourceURL string) (ShopifyFileNode, error) {
	data, err := c.GraphQL(ctx, store, mutationFileUpdate, map[string]any{
		"files": []map[string]any{{
			"id":             fileID,
			"originalSource": resourceURL,
		}},
	})
	if err != nil {
		return ShopifyFileNode{}, err
	}
	return parseFileUpdate(data)
}

func (c *ShopifyClient) UpdateFileAlt(ctx context.Context, store Store, fileID, alt string) (ShopifyFileNode, error) {
	data, err := c.GraphQL(ctx, store, mutationFileUpdate, map[string]any{
		"files": []map[string]any{{
			"id":  fileID,
			"alt": alt,
		}},
	})
	if err != nil {
		return ShopifyFileNode{}, newMutationAttemptError(err)
	}
	node, err := parseFileUpdate(data)
	if err != nil {
		return ShopifyFileNode{}, newMutationAttemptError(err)
	}
	return node, nil
}

func parseFileUpdate(data json.RawMessage) (ShopifyFileNode, error) {
	var parsed struct {
		FileUpdate struct {
			Files      []ShopifyFileNode  `json:"files"`
			UserErrors []ShopifyUserError `json:"userErrors"`
		} `json:"fileUpdate"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ShopifyFileNode{}, err
	}
	if err := ensureNoUserErrors("fileUpdate", parsed.FileUpdate.UserErrors); err != nil {
		return ShopifyFileNode{}, err
	}
	if len(parsed.FileUpdate.Files) == 0 || parsed.FileUpdate.Files[0].ID == "" {
		return ShopifyFileNode{}, fmt.Errorf("fileUpdate 未返回 file id")
	}
	return parsed.FileUpdate.Files[0], nil
}

func (c *ShopifyClient) PollFileReady(ctx context.Context, store Store, fileID string, attempts int, interval time.Duration) (ShopifyFileNode, error) {
	if attempts <= 0 {
		attempts = 20
	}
	if interval <= 0 {
		interval = 1500 * time.Millisecond
	}
	var last ShopifyFileNode
	for attempt := 1; attempt <= attempts; attempt++ {
		node, err := c.FileNode(ctx, store, fileID)
		if err != nil {
			return ShopifyFileNode{}, err
		}
		last = node
		if node.FileStatus == "FAILED" {
			return ShopifyFileNode{}, fmt.Errorf("%s file processing failed: %s", store.ID, fileID)
		}
		if FileNodeReadyWithImageMetadata(node) {
			return node, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ShopifyFileNode{}, ctx.Err()
		case <-timer.C:
		}
	}
	return ShopifyFileNode{}, fmt.Errorf("%s file readback 未就绪: %s status=%s image_metadata=%t", store.ID, fileID, last.FileStatus, FileNodeReadyWithImageMetadata(last))
}

func (c *ShopifyClient) PollVideoReady(ctx context.Context, store Store, fileID string, attempts int, interval time.Duration) (ShopifyFileNode, error) {
	if attempts <= 0 {
		attempts = 20
	}
	if interval <= 0 {
		interval = 1500 * time.Millisecond
	}
	var last ShopifyFileNode
	for attempt := 1; attempt <= attempts; attempt++ {
		node, err := c.FileNode(ctx, store, fileID)
		if err != nil {
			return ShopifyFileNode{}, err
		}
		last = node
		if node.FileStatus == "FAILED" {
			return ShopifyFileNode{}, fmt.Errorf("%s video processing failed: %s", store.ID, fileID)
		}
		if VideoNodeReady(node) {
			return node, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ShopifyFileNode{}, ctx.Err()
		case <-timer.C:
		}
	}
	return ShopifyFileNode{}, fmt.Errorf("%s video readback 未就绪: %s status=%s video_source=%t", store.ID, fileID, last.FileStatus, VideoSourceURL(last) != "")
}

func VideoNodeReady(node ShopifyFileNode) bool {
	return node.FileStatus == "READY" && IsVideoNode(node) && VideoSourceURL(node) != ""
}

func IsVideoNode(node ShopifyFileNode) bool {
	return node.Type == "Video"
}

func VideoSourceURL(node ShopifyFileNode) string {
	if node.OriginalVideoSource != nil && node.OriginalVideoSource.URL != "" && strings.EqualFold(node.OriginalVideoSource.MimeType, "video/mp4") {
		return node.OriginalVideoSource.URL
	}
	for _, source := range node.VideoSources {
		if source.URL != "" && strings.EqualFold(source.MimeType, "video/mp4") {
			return source.URL
		}
	}
	return ""
}

func FileNodeReadyWithImageMetadata(node ShopifyFileNode) bool {
	return node.FileStatus == "READY" &&
		node.Image != nil &&
		node.Image.URL != "" &&
		node.Image.Width > 0 &&
		node.Image.Height > 0
}

func (c *ShopifyClient) FileNode(ctx context.Context, store Store, fileID string) (ShopifyFileNode, error) {
	data, err := c.GraphQL(ctx, store, queryFileNode, map[string]any{"id": fileID})
	if err != nil {
		return ShopifyFileNode{}, err
	}
	var parsed struct {
		Node *ShopifyFileNode `json:"node"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ShopifyFileNode{}, err
	}
	if parsed.Node == nil || parsed.Node.ID == "" {
		return ShopifyFileNode{}, fmt.Errorf("%s file node not found: %s", store.ID, fileID)
	}
	return *parsed.Node, nil
}

func (c *ShopifyClient) FindFileByFilename(ctx context.Context, store Store, filename string) (ShopifyFileNode, error) {
	filename = strings.TrimSpace(filename)
	query := ShopifyFilenameSearchQuery(filename)
	data, err := c.GraphQL(ctx, store, queryFilesByFilename, map[string]any{"query": query})
	if err != nil {
		return ShopifyFileNode{}, err
	}
	var parsed struct {
		Files struct {
			Nodes []ShopifyFileNode `json:"nodes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ShopifyFileNode{}, err
	}
	for _, node := range parsed.Files.Nodes {
		if ShopifyFileNodeFilename(node) == filename {
			return node, nil
		}
	}
	if len(parsed.Files.Nodes) > 0 {
		return ShopifyFileNode{}, fmt.Errorf("%s Shopify Files filename search returned %d result(s), but none matched exact filename: %s", store.ID, len(parsed.Files.Nodes), filename)
	}
	return ShopifyFileNode{}, fmt.Errorf("%w: %s 未找到 Shopify Files: %s", ErrShopifyFileNotFound, store.ID, filename)
}

func (c *ShopifyClient) FindVideoByFilename(ctx context.Context, store Store, filename string) (ShopifyFileNode, error) {
	nodes, err := c.findExactFilesByFilename(ctx, store, filename)
	if err != nil {
		return ShopifyFileNode{}, err
	}
	if len(nodes) == 0 {
		return ShopifyFileNode{}, fmt.Errorf("%w: %s 未找到 Shopify Files: %s", ErrShopifyFileNotFound, store.ID, filename)
	}
	if len(nodes) > 1 {
		return ShopifyFileNode{}, fmt.Errorf("%s 同名 Shopify Files 候选不唯一，拒绝选择 first: %s", store.ID, describeFileCandidates(nodes))
	}
	node := nodes[0]
	if !IsVideoNode(node) {
		return ShopifyFileNode{}, fmt.Errorf("%s 同名 Shopify File 不是 VIDEO: %s", store.ID, describeFileCandidates(nodes))
	}
	return node, nil
}

func (c *ShopifyClient) findExactFilesByFilename(ctx context.Context, store Store, filename string) ([]ShopifyFileNode, error) {
	filename = strings.TrimSpace(filename)
	query := ShopifyFilenameSearchQuery(filename)
	data, err := c.GraphQL(ctx, store, queryFilesByFilename, map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Files struct {
			Nodes []ShopifyFileNode `json:"nodes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	var exact []ShopifyFileNode
	for _, node := range parsed.Files.Nodes {
		if ShopifyFileNodeFilename(node) == filename {
			exact = append(exact, node)
		}
	}
	return exact, nil
}

func describeFileCandidates(nodes []ShopifyFileNode) string {
	parts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		parts = append(parts, fmt.Sprintf("id=%s type=%s status=%s", node.ID, node.Type, node.FileStatus))
	}
	return strings.Join(parts, "; ")
}

func ShopifyFilenameSearchQuery(filename string) string {
	return fmt.Sprintf("filename:%q", strings.TrimSpace(filename))
}

func ShopifyFileNodeFilename(node ShopifyFileNode) string {
	if node.Filename != "" {
		return node.Filename
	}
	if node.Image == nil || node.Image.URL == "" {
		return ""
	}
	parsed, err := url.Parse(node.Image.URL)
	if err != nil {
		return ""
	}
	filename := path.Base(parsed.Path)
	if unescaped, err := url.PathUnescape(filename); err == nil {
		return unescaped
	}
	return filename
}

func (c *ShopifyClient) RegisterAltTranslations(ctx context.Context, store Store, resourceID string, translations map[string]string) (map[string]TranslationReadback, error) {
	_, err := c.RegisterAltTranslationsMutation(ctx, store, resourceID, translations)
	if err != nil {
		return nil, err
	}
	return c.VerifyAltTranslations(ctx, store, resourceID, translations)
}

func (c *ShopifyClient) RegisterAltTranslationsMutation(ctx context.Context, store Store, resourceID string, translations map[string]string) (bool, error) {
	if len(translations) == 0 {
		return false, nil
	}
	locales, err := c.shopLocales(ctx, store)
	if err != nil {
		return false, err
	}
	registerable, _ := FilterRegisterableTranslations(translations, locales)
	if len(registerable) == 0 {
		return false, nil
	}
	digest, err := c.altDigest(ctx, store, resourceID, firstLocale(translations))
	if err != nil {
		return false, err
	}
	inputs := make([]map[string]any, 0, len(registerable))
	for _, locale := range sortedStringKeys(registerable) {
		value := registerable[locale]
		inputs = append(inputs, map[string]any{
			"key":                       "alt",
			"value":                     value,
			"locale":                    locale,
			"translatableContentDigest": digest,
		})
	}
	data, err := c.GraphQL(ctx, store, mutationTranslationsRegister, map[string]any{
		"resourceId":   resourceID,
		"translations": inputs,
	})
	if err != nil {
		return false, newMutationAttemptError(err)
	}
	var parsed struct {
		TranslationsRegister struct {
			Translations []TranslationReadback `json:"translations"`
			UserErrors   []ShopifyUserError    `json:"userErrors"`
		} `json:"translationsRegister"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false, newMutationAttemptError(err)
	}
	if err := ensureNoUserErrors("translationsRegister", parsed.TranslationsRegister.UserErrors); err != nil {
		return false, newMutationAttemptError(err)
	}
	return true, nil
}

func (c *ShopifyClient) VerifyAltTranslations(ctx context.Context, store Store, resourceID string, translations map[string]string) (map[string]TranslationReadback, error) {
	if len(translations) == 0 {
		return nil, nil
	}
	locales, err := c.shopLocales(ctx, store)
	if err != nil {
		return nil, err
	}
	registerable, readbacks := FilterRegisterableTranslations(translations, locales)
	for locale := range registerable {
		if err := c.addVerifiedAltTranslationReadback(ctx, store, resourceID, locale, registerable[locale], readbacks); err != nil {
			return nil, err
		}
	}
	return readbacks, nil
}

func (c *ShopifyClient) addVerifiedAltTranslationReadback(ctx context.Context, store Store, resourceID, locale, expected string, readbacks map[string]TranslationReadback) error {
	readback, err := c.verifiedAltTranslationReadback(ctx, store, resourceID, locale, expected)
	if err != nil {
		return err
	}
	readbacks[locale] = readback
	return nil
}

func (c *ShopifyClient) verifiedAltTranslationReadback(ctx context.Context, store Store, resourceID, locale, expected string) (TranslationReadback, error) {
	readback, err := c.altTranslationReadback(ctx, store, resourceID, locale)
	if err != nil {
		return TranslationReadback{}, err
	}
	if err := ValidateTranslationReadback(store, resourceID, locale, expected, readback); err != nil {
		return TranslationReadback{}, err
	}
	return readback, nil
}

func ValidateTranslationReadback(store Store, resourceID, locale, expected string, readback TranslationReadback) error {
	if readback.Value != expected {
		return fmt.Errorf("%s translations(%s) alt readback mismatch: expected %q got %q: %s", store.ID, locale, expected, readback.Value, resourceID)
	}
	if readback.Outdated {
		return fmt.Errorf("%s translations(%s) alt readback is outdated: %s", store.ID, locale, resourceID)
	}
	return nil
}

func (c *ShopifyClient) shopLocales(ctx context.Context, store Store) ([]ShopLocale, error) {
	if c.localeCache == nil {
		c.localeCache = map[string][]ShopLocale{}
	}
	if locales, ok := c.localeCache[store.ID]; ok {
		return locales, nil
	}
	data, err := c.GraphQL(ctx, store, queryShopLocales, nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		ShopLocales []ShopLocale `json:"shopLocales"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	c.localeCache[store.ID] = parsed.ShopLocales
	return parsed.ShopLocales, nil
}

func FilterRegisterableTranslations(translations map[string]string, locales []ShopLocale) (map[string]string, map[string]TranslationReadback) {
	byLocale := map[string]ShopLocale{}
	for _, locale := range locales {
		byLocale[strings.ToLower(locale.Locale)] = locale
	}
	registerable := map[string]string{}
	readbacks := map[string]TranslationReadback{}
	for _, requestedLocale := range sortedStringKeys(translations) {
		normalized := strings.ToLower(requestedLocale)
		locale, ok := byLocale[normalized]
		if !ok {
			readbacks[requestedLocale] = skippedTranslation(requestedLocale, "locale is not enabled for the shop")
			continue
		}
		if locale.Primary {
			readbacks[requestedLocale] = skippedTranslation(requestedLocale, "locale is the shop primary locale; source alt covers it")
			continue
		}
		if !locale.Published {
			readbacks[requestedLocale] = skippedTranslation(requestedLocale, "locale is not published for the shop")
			continue
		}
		registerable[locale.Locale] = translations[requestedLocale]
	}
	return registerable, readbacks
}

func skippedTranslation(locale, reason string) TranslationReadback {
	return TranslationReadback{
		Key:      "alt",
		Locale:   locale,
		Status:   "SKIPPED",
		Reason:   reason,
		Outdated: false,
	}
}

func (c *ShopifyClient) altDigest(ctx context.Context, store Store, resourceID, locale string) (string, error) {
	data, err := c.GraphQL(ctx, store, queryTranslatableResource, map[string]any{"resourceId": resourceID, "locale": locale})
	if err != nil {
		return "", err
	}
	resource, err := parseTranslatableResource(data)
	if err != nil {
		return "", err
	}
	for _, item := range resource.TranslatableContent {
		if item.Key == "alt" && item.Digest != "" {
			return item.Digest, nil
		}
	}
	return "", fmt.Errorf("%s translatableResource 缺 alt digest: %s", store.ID, resourceID)
}

func (c *ShopifyClient) altTranslationReadback(ctx context.Context, store Store, resourceID, locale string) (TranslationReadback, error) {
	data, err := c.GraphQL(ctx, store, queryTranslatableResource, map[string]any{"resourceId": resourceID, "locale": locale})
	if err != nil {
		return TranslationReadback{}, err
	}
	resource, err := parseTranslatableResource(data)
	if err != nil {
		return TranslationReadback{}, err
	}
	for _, item := range resource.Translations {
		if item.Key == "alt" {
			return item, nil
		}
	}
	return TranslationReadback{}, fmt.Errorf("%s translations(%s) 缺 alt readback: %s", store.ID, locale, resourceID)
}

func parseTranslatableResource(data json.RawMessage) (struct {
	TranslatableContent []struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Digest string `json:"digest"`
		Locale string `json:"locale"`
	} `json:"translatableContent"`
	Translations []TranslationReadback `json:"translations"`
}, error) {
	var parsed struct {
		TranslatableResource struct {
			TranslatableContent []struct {
				Key    string `json:"key"`
				Value  string `json:"value"`
				Digest string `json:"digest"`
				Locale string `json:"locale"`
			} `json:"translatableContent"`
			Translations []TranslationReadback `json:"translations"`
		} `json:"translatableResource"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return parsed.TranslatableResource, err
	}
	return parsed.TranslatableResource, nil
}

func firstLocale(values map[string]string) string {
	keys := sortedStringKeys(values)
	if len(keys) > 0 {
		return keys[0]
	}
	return "en"
}

func sortedStringKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ensureNoUserErrors(label string, errors []ShopifyUserError) error {
	if len(errors) == 0 {
		return nil
	}
	var parts []string
	for _, item := range errors {
		text := item.Message
		if item.Code != "" {
			text += " (" + item.Code + ")"
		}
		if len(item.Field) > 0 {
			text += " field=" + strings.Join(item.Field, ".")
		}
		parts = append(parts, text)
	}
	return shopifyUserErrorsError{label: label, message: strings.Join(parts, "; ")}
}

type shopifyUserErrorsError struct {
	label   string
	message string
}

type MutationAttemptError struct {
	err       error
	safeRetry bool
}

func newMutationAttemptError(err error) MutationAttemptError {
	var userErrors shopifyUserErrorsError
	return NewMutationAttemptError(err, errors.As(err, &userErrors))
}

func NewMutationAttemptError(err error, safeRetry bool) MutationAttemptError {
	return MutationAttemptError{err: err, safeRetry: safeRetry}
}

func WrapMutationAttemptError(err error) MutationAttemptError {
	return newMutationAttemptError(err)
}

func (c *ShopifyClient) HTTPClient() *http.Client { return c.httpClient }

func (e MutationAttemptError) Error() string   { return e.err.Error() }
func (e MutationAttemptError) Unwrap() error   { return e.err }
func (e MutationAttemptError) SafeRetry() bool { return e.safeRetry }

func (e shopifyUserErrorsError) Error() string {
	return fmt.Sprintf("%s userErrors: %s", e.label, e.message)
}

const mutationStagedUploadsCreate = `
mutation MediaSyncStagedUploadsCreate($input: [StagedUploadInput!]!) {
  stagedUploadsCreate(input: $input) {
    stagedTargets {
      url
      resourceUrl
      parameters { name value }
    }
    userErrors { field message }
  }
}`

const mutationFileCreate = `
mutation MediaSyncFileCreate($files: [FileCreateInput!]!) {
  fileCreate(files: $files) {
    files {
      id
	  __typename
      fileStatus
      alt
      createdAt
      ... on MediaImage { image { url width height } }
	  ... on Video { filename originalSource { url mimeType } sources { url mimeType } }
    }
    userErrors { field message code }
  }
}`

const mutationFileUpdate = `
mutation MediaSyncFileUpdate($files: [FileUpdateInput!]!) {
  fileUpdate(files: $files) {
    files {
      id
	  __typename
      fileStatus
      alt
      createdAt
      ... on MediaImage { image { url width height } }
	  ... on Video { filename originalSource { url mimeType } sources { url mimeType } }
    }
    userErrors { field message code }
  }
}`

const queryFileNode = `
query MediaSyncFileNode($id: ID!) {
  node(id: $id) {
    ... on File {
      id
	  __typename
      fileStatus
      alt
      createdAt
      ... on MediaImage { image { url width height } }
	  ... on Video { filename originalSource { url mimeType } sources { url mimeType } }
    }
  }
}`

const queryFilesByFilename = `
query MediaSyncFilesByFilename($query: String!) {
  files(first: 10, query: $query) {
    nodes {
      id
	  __typename
      fileStatus
      alt
      createdAt
      ... on MediaImage { image { url width height } }
	  ... on Video { filename originalSource { url mimeType } sources { url mimeType } }
    }
  }
}`

const queryTranslatableResource = `
query MediaSyncTranslatableResource($resourceId: ID!, $locale: String!) {
  translatableResource(resourceId: $resourceId) {
    resourceId
    translatableContent { key value digest locale }
    translations(locale: $locale) { key value locale outdated }
  }
}`

const queryShopLocales = `
query MediaSyncShopLocales {
  shopLocales {
    locale
    primary
    published
  }
}`

const mutationTranslationsRegister = `
mutation MediaSyncTranslationsRegister($resourceId: ID!, $translations: [TranslationInput!]!) {
  translationsRegister(resourceId: $resourceId, translations: $translations) {
    translations { key value locale outdated }
    userErrors { field message code }
  }
}`
