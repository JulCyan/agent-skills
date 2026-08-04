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
