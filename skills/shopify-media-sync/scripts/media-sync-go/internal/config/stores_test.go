package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreNormalizeAndSelectPortableAlias(t *testing.T) {
	cfg := StoresConfig{Stores: []Store{{
		ID:            "store-jp",
		Label:         "Japan",
		Aliases:       []string{"jp"},
		PrimaryLocale: "jp",
		Enabled:       true,
	}}}
	stores, err := SelectStores(cfg, "jp")
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || stores[0].PrimaryLocale != "ja" || stores[0].ShopifyStore != "store-jp" {
		t.Fatalf("unexpected normalized store: %#v", stores)
	}
}

func TestShopifyAPIVersionUsesExplicitPrecedenceAndSafeDefault(t *testing.T) {
	store := Store{ID: "store-test", APIVersion: "2026-04"}
	values := map[string]string{
		"SHOPIFY_API_VERSION":            "2026-07",
		"SHOPIFY_API_VERSION_STORE_TEST": "2026-10",
	}
	lookup := func(key string) string { return values[key] }

	version, err := ShopifyAPIVersion(store, lookup)
	if err != nil || version != "2026-10" {
		t.Fatalf("expected per-store environment override, got version=%q err=%v", version, err)
	}
	delete(values, "SHOPIFY_API_VERSION_STORE_TEST")
	version, err = ShopifyAPIVersion(store, lookup)
	if err != nil || version != "2026-07" {
		t.Fatalf("expected global environment override, got version=%q err=%v", version, err)
	}
	delete(values, "SHOPIFY_API_VERSION")
	version, err = ShopifyAPIVersion(store, lookup)
	if err != nil || version != "2026-04" {
		t.Fatalf("expected store config version, got version=%q err=%v", version, err)
	}
	version, err = ShopifyAPIVersion(Store{ID: "store-default"}, lookup)
	if err != nil || version != DefaultShopifyAPIVersion {
		t.Fatalf("expected safe default version, got version=%q err=%v", version, err)
	}
}

func TestLoadStoresConfigRejectsInvalidShopifyAPIVersion(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "stores.config.json")
	raw, err := json.Marshal(StoresConfig{Stores: []Store{{
		ID: "store-test", ShopifyStore: "example-test", APIVersion: "2026-02", Enabled: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStoresConfig(configPath); err == nil {
		t.Fatal("expected non-quarterly Shopify API version to be rejected")
	}
}

func TestLoadStoresConfigRejectsUnsafeShopifyStoreHandle(t *testing.T) {
	for _, handle := range []string{
		"evil.example/collect?x=",
		"../other-store",
		"-leading-hyphen",
		"trailing-hyphen-",
		"Uppercase",
	} {
		t.Run(handle, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "stores.config.json")
			raw, err := json.Marshal(StoresConfig{Stores: []Store{{
				ID: "store-test", ShopifyStore: handle, Enabled: true,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadStoresConfig(configPath); err == nil {
				t.Fatalf("expected unsafe shopifyStore %q to be rejected", handle)
			}
		})
	}
}

func TestReadDotEnvDoesNotMutateProcess(t *testing.T) {
	const key = "MEDIA_SYNC_CONFIG_TEST_SECRET"
	t.Setenv(key, "process")
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(key+"=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := ReadDotEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	if values[key] != "file" || os.Getenv(key) != "process" {
		t.Fatalf("dotenv read changed process or lost value: values=%q process=%q", values[key], os.Getenv(key))
	}
}

func TestLoadDotEnvExportsOnlySupportedShopifyKeys(t *testing.T) {
	for _, key := range []string{"SHOPIFY_ADMIN_TOKEN", "SSL_CERT_FILE"} {
		previous, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	t.Setenv("HTTPS_PROXY", "process-proxy")
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(
		"SHOPIFY_ADMIN_TOKEN=fixture-token\nHTTPS_PROXY=https://untrusted.example\nSSL_CERT_FILE=/tmp/untrusted.pem\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SHOPIFY_ADMIN_TOKEN") != "fixture-token" {
		t.Fatal("supported Shopify credential was not loaded")
	}
	if os.Getenv("HTTPS_PROXY") != "process-proxy" {
		t.Fatal("dotenv changed transport proxy configuration")
	}
	if _, exists := os.LookupEnv("SSL_CERT_FILE"); exists {
		t.Fatal("dotenv exported unsupported TLS configuration")
	}
}
