package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const DefaultShopifyAPIVersion = "2026-01"

var shopifyAPIVersionRE = regexp.MustCompile(`^[0-9]{4}-(01|04|07|10)$`)

type StoresConfig struct {
	Stores   []Store `json:"stores"`
	Archived []Store `json:"archived"`
}

type Store struct {
	ID                   string   `json:"id"`
	Label                string   `json:"label"`
	Aliases              []string `json:"aliases,omitempty"`
	ShopifyStore         string   `json:"shopifyStore"`
	APIVersion           string   `json:"apiVersion,omitempty"`
	PrimaryLocale        string   `json:"primaryLocale,omitempty"`
	DefaultLocale        string   `json:"defaultLocale,omitempty"`
	Enabled              bool     `json:"enabled"`
	Archived             bool     `json:"-"`
	ShopifyStoreFallback bool     `json:"-"`
}

func (s *Store) Normalize() {
	if s.ShopifyStore == "" {
		s.ShopifyStore = s.ID
		s.ShopifyStoreFallback = true
	}
	if s.Label == "" {
		s.Label = s.ID
	}
	if s.APIVersion == "" {
		s.APIVersion = DefaultShopifyAPIVersion
	}
	if s.PrimaryLocale == "" {
		s.PrimaryLocale = s.DefaultLocale
	}
	if locale := normalizeLocale(s.PrimaryLocale); locale != "" {
		s.PrimaryLocale = locale
	} else {
		s.PrimaryLocale = "en"
	}
	if s.DefaultLocale == "" {
		s.DefaultLocale = s.PrimaryLocale
	} else if locale := normalizeLocale(s.DefaultLocale); locale != "" {
		s.DefaultLocale = locale
	}
}

func LoadStoresConfig(configPath string) (StoresConfig, error) {
	if configPath == "" {
		found, err := FindUp("stores.config.json")
		if err != nil {
			return StoresConfig{}, err
		}
		configPath = found
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return StoresConfig{}, fmt.Errorf("读取 stores config 失败: %w", err)
	}
	var cfg StoresConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return StoresConfig{}, fmt.Errorf("解析 stores config 失败: %w", err)
	}
	for i := range cfg.Stores {
		cfg.Stores[i].Normalize()
		if err := ValidateShopifyAPIVersion(cfg.Stores[i].APIVersion); err != nil {
			return StoresConfig{}, fmt.Errorf("store %s apiVersion: %w", cfg.Stores[i].ID, err)
		}
	}
	for i := range cfg.Archived {
		cfg.Archived[i].Normalize()
		if err := ValidateShopifyAPIVersion(cfg.Archived[i].APIVersion); err != nil {
			return StoresConfig{}, fmt.Errorf("archived store %s apiVersion: %w", cfg.Archived[i].ID, err)
		}
		cfg.Archived[i].Archived = true
	}
	return cfg, nil
}

func ValidateShopifyAPIVersion(version string) error {
	if !shopifyAPIVersionRE.MatchString(strings.TrimSpace(version)) {
		return fmt.Errorf("Shopify Admin API version %q must use a quarterly YYYY-MM value (01, 04, 07, or 10)", version)
	}
	return nil
}

func ShopifyAPIVersion(store Store, lookup func(string) string) (string, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	suffix := strings.ToUpper(strings.ReplaceAll(store.ID, "-", "_"))
	candidates := []struct {
		source string
		value  string
	}{
		{source: "SHOPIFY_API_VERSION_" + suffix, value: lookup("SHOPIFY_API_VERSION_" + suffix)},
		{source: "SHOPIFY_API_VERSION", value: lookup("SHOPIFY_API_VERSION")},
		{source: "store apiVersion", value: store.APIVersion},
		{source: "default", value: DefaultShopifyAPIVersion},
	}
	for _, candidate := range candidates {
		version := strings.TrimSpace(candidate.value)
		if version == "" {
			continue
		}
		if err := ValidateShopifyAPIVersion(version); err != nil {
			return "", fmt.Errorf("%s: %w", candidate.source, err)
		}
		return version, nil
	}
	return "", errors.New("Shopify Admin API version could not be resolved")
}

func SelectStores(cfg StoresConfig, spec string) ([]Store, error) {
	activeByID := map[string]Store{}
	aliasByID := map[string]Store{}
	explicitByID := map[string]Store{}
	for _, store := range cfg.Stores {
		store.Normalize()
		explicitByID[store.ID] = store
		if !store.Enabled {
			continue
		}
		activeByID[store.ID] = store
		for _, alias := range storeAliases(store) {
			aliasByID[alias] = store
		}
	}
	for _, store := range cfg.Archived {
		store.Normalize()
		store.Archived = true
		explicitByID[store.ID] = store
		for _, alias := range storeAliases(store) {
			aliasByID[alias] = store
		}
	}

	var selected []Store
	seen := map[string]bool{}
	add := func(store Store) {
		if !seen[store.ID] {
			seen[store.ID] = true
			selected = append(selected, store)
		}
	}

	for _, item := range SplitCSV(spec) {
		item = strings.ToLower(item)
		if item == "all" {
			for _, store := range cfg.Stores {
				store.Normalize()
				if store.Enabled {
					add(store)
				}
			}
			continue
		}
		if store, ok := activeByID[item]; ok {
			add(store)
			continue
		}
		if store, ok := explicitByID[item]; ok {
			add(store)
			continue
		}
		if store, ok := aliasByID[item]; ok {
			add(store)
			continue
		}
		return nil, fmt.Errorf("未知 store %q", item)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("未选择任何 store")
	}
	return selected, nil
}

func storeAliases(store Store) []string {
	aliases := []string{strings.ToLower(store.ID), strings.ToLower(store.Label)}
	for _, alias := range store.Aliases {
		if normalized := strings.ToLower(strings.TrimSpace(alias)); normalized != "" {
			aliases = append(aliases, normalized)
		}
	}
	return aliases
}

func FindUp(name string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return FindUpFrom(dir, name)
}

func FindUpFrom(dir, name string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("向上查找未找到 %s", name)
}

func SplitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeLocale(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "jp":
		return "ja"
	case "en", "de", "fr", "ja":
		return value
	default:
		return ""
	}
}
