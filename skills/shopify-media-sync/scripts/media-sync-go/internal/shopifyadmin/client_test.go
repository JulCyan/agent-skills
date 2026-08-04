package shopifyadmin

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestGraphQLMutationDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "test-token")
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"errors":[{"message":"temporary"}]}`)),
			Request:    request,
		}, nil
	})}
	client := NewShopifyClient(
		httpClient,
		WithEndpoints(
			func(Store) string { return "https://shopify.test/oauth" },
			func(Store) string { return "https://shopify.test/graphql" },
		),
		WithBackoff(func(context.Context, int) error { return nil }),
	)
	_, err := client.GraphQL(t.Context(), Store{ID: "store-a", ShopifyStore: "store-a"}, `mutation Test { test }`, map[string]any{})
	if err == nil {
		t.Fatal("expected mutation failure")
	}
	if requests != 1 {
		t.Fatalf("mutation transport ambiguity retried %d times", requests)
	}
}

func TestGraphQLRejectsUnsafeShopifyStoreBeforeRequest(t *testing.T) {
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "test-token")
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})}
	client := NewShopifyClient(httpClient)
	_, err := client.GraphQL(
		t.Context(),
		Store{ID: "store-a", ShopifyStore: "evil.example/collect?x="},
		`query Test { shop { id } }`,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "shopifyStore") {
		t.Fatalf("expected unsafe shopifyStore rejection, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("unsafe shopifyStore reached HTTP transport: %d requests", requests)
	}
}

func TestGraphQLDoesNotFollowCrossOriginRedirectWithToken(t *testing.T) {
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "test-token")
	redirectedRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		redirectedRequests++
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)

	client := NewShopifyClient(
		nil,
		WithEndpoints(
			func(Store) string { return source.URL },
			func(Store) string { return source.URL },
		),
	)
	_, err := client.GraphQL(
		t.Context(),
		Store{ID: "store-a", ShopifyStore: "store-a"},
		`query Test { shop { id } }`,
		nil,
	)
	if err == nil {
		t.Fatal("expected cross-origin redirect to fail closed")
	}
	if redirectedRequests != 0 {
		t.Fatalf("cross-origin redirect received Shopify token: %d requests", redirectedRequests)
	}
}

func TestUploadToStagedTargetStreamsMultipartBody(t *testing.T) {
	content := strings.Repeat("streamed-media-content", 128)
	resourcePath := filepath.Join(t.TempDir(), "scene.mp4")
	if err := os.WriteFile(resourcePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if _, ok := request.Body.(*io.PipeReader); !ok {
			t.Fatalf("multipart body must stream through io.Pipe, got %T", request.Body)
		}
		if request.ContentLength <= int64(len(content)) {
			t.Fatalf("multipart content length must include framing: got %d", request.ContentLength)
		}
		_, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(request.Body, params["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() != "file" || part.FileName() != "scene.mp4" {
			t.Fatalf("unexpected multipart file part: name=%q filename=%q", part.FormName(), part.FileName())
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != content {
			t.Fatal("streamed multipart payload changed")
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	client := NewShopifyClient(httpClient)
	err := client.UploadToStagedTarget(t.Context(), StagedTarget{URL: "https://upload.shopify.test"}, ResourceInfo{
		Path:     resourcePath,
		Filename: "scene.mp4",
		MimeType: "video/mp4",
		Bytes:    int64(len(content)),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUploadToStagedTargetRejectsResourceHashDriftBeforeHTTP(t *testing.T) {
	resourcePath := filepath.Join(t.TempDir(), "asset.svg")
	if err := os.WriteFile(resourcePath, []byte("changed-after-plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := NewShopifyClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})})
	err := client.UploadToStagedTarget(t.Context(), StagedTarget{URL: "https://upload.shopify.test"}, ResourceInfo{
		Path:     resourcePath,
		Filename: "asset.svg",
		SHA256:   strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA") {
		t.Fatalf("expected resource SHA drift rejection, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("drifted resource reached staged upload: %d requests", requests)
	}
}

func TestGraphQLUsesResolvedAPIVersionAndRejectsInvalidBeforeRequest(t *testing.T) {
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "test-token")
	t.Setenv("SHOPIFY_API_VERSION", "")
	t.Setenv("SHOPIFY_API_VERSION_STORE_A", "2026-07")
	requests := 0
	var requestPath string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		requestPath = request.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":{"shop":{"id":"gid://shopify/Shop/1"}}}`)),
			Request:    request,
		}, nil
	})}
	client := NewShopifyClient(httpClient)
	store := Store{ID: "store-a", ShopifyStore: "example-test", APIVersion: "2026-04"}
	if _, err := client.GraphQL(t.Context(), store, `query Test { shop { id } }`, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !strings.Contains(requestPath, "/admin/api/2026-07/graphql.json") {
		t.Fatalf("request did not echo the resolved version in its endpoint: requests=%d path=%q", requests, requestPath)
	}

	t.Setenv("SHOPIFY_API_VERSION_STORE_A", "2026-02")
	if _, err := client.GraphQL(t.Context(), store, `query Test { shop { id } }`, nil); err == nil || !strings.Contains(err.Error(), "SHOPIFY_API_VERSION_STORE_A") {
		t.Fatalf("expected invalid API version to fail closed, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("invalid API version must fail before an HTTP request, got %d requests", requests)
	}
}
