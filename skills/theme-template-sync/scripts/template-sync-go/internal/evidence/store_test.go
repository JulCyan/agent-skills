package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"theme-template-sync/internal/syncengine"
)

func TestNewRunRoot_UsesCallerRuntimeDirectory(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	now := time.Date(2026, time.August, 25, 7, 8, 9, 0, time.UTC)
	root, runID, err := NewRunRoot(caller, now, bytes.NewReader([]byte{1, 2, 3, 4}))
	if err != nil {
		t.Fatalf("NewRunRoot(): %v", err)
	}
	if runID != "20260825T070809Z-01020304" {
		t.Fatalf("run ID = %q, want deterministic timestamp/random suffix", runID)
	}
	want := filepath.Join(caller, ".runtime", "theme-template-sync", runID)
	if root != want {
		t.Fatalf("root = %q, want %q", root, want)
	}
}

func TestStore_WriteReadJSONAtomically(t *testing.T) {
	t.Parallel()

	rootPath := filepath.Join(t.TempDir(), "run")
	store, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	value := map[string]any{
		"status":  "PLANNED",
		"targets": []any{},
	}
	if err := store.WriteJSON("targets/000/diff.json", value); err != nil {
		t.Fatalf("WriteJSON(): %v", err)
	}
	var got map[string]any
	if err := store.ReadJSON("targets/000/diff.json", &got); err != nil {
		t.Fatalf("ReadJSON(): %v", err)
	}
	if got["status"] != "PLANNED" {
		t.Fatalf("status = %v, want PLANNED", got["status"])
	}
	data, err := os.ReadFile(filepath.Join(rootPath, "targets", "000", "diff.json"))
	if err != nil {
		t.Fatalf("reading artifact: %v", err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) || !bytes.Contains(data, []byte("  \"status\"")) {
		t.Fatalf("artifact is not stable indented JSON: %q", data)
	}
	info, err := os.Stat(filepath.Join(rootPath, "targets", "000", "diff.json"))
	if err != nil {
		t.Fatalf("stating artifact: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("artifact permissions = %o, want owner-only", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Join(rootPath, "targets", "000"))
	if err != nil {
		t.Fatalf("reading artifact directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "diff.json" {
		t.Fatalf("artifact entries = %v, want only diff.json", entries)
	}
}

func TestStore_ReservationIsExclusive(t *testing.T) {
	t.Parallel()

	rootPath := filepath.Join(t.TempDir(), "run")
	first, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open(first): %v", err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("Close(first): %v", err)
		}
	})
	second, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open(second): %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close(second): %v", err)
		}
	})
	if err := first.Reserve(); err != nil {
		t.Fatalf("Reserve(first): %v", err)
	}
	if err := second.Reserve(); err == nil {
		t.Fatal("Reserve(second) error = nil")
	}
}

func TestStore_ApplyLeaseIsExclusiveReusableAndRetainable(t *testing.T) {
	t.Parallel()

	rootPath := filepath.Join(t.TempDir(), "run")
	first, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open(first): %v", err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("Close(first): %v", err)
		}
	})
	second, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open(second): %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close(second): %v", err)
		}
	})

	previewLease, err := first.AcquireApplyLease()
	if err != nil {
		t.Fatalf("AcquireApplyLease(first): %v", err)
	}
	if _, err := second.AcquireApplyLease(); err == nil {
		t.Fatal("AcquireApplyLease(second) error = nil while first lease is held")
	}
	if err := previewLease.Finish(); err != nil {
		t.Fatalf("Finish(preview lease): %v", err)
	}

	executeLease, err := second.AcquireApplyLease()
	if err != nil {
		t.Fatalf("AcquireApplyLease(after preview): %v", err)
	}
	executeLease.Retain()
	if err := executeLease.Finish(); err != nil {
		t.Fatalf("Finish(retained execute lease): %v", err)
	}
	if _, err := first.AcquireApplyLease(); err == nil {
		t.Fatal("AcquireApplyLease() error = nil after mutation lease was retained")
	}
	if _, err := os.Stat(filepath.Join(rootPath, ".apply-lease")); err != nil {
		t.Fatalf("retained apply lease is missing: %v", err)
	}
}

func TestStore_RejectsUnsafeRelativePaths(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	for _, path := range []string{"", ".", "../victim", "targets/../../victim", "/absolute", `targets\\victim`} {
		t.Run(path, func(t *testing.T) {
			if err := store.WriteBytes(path, []byte("unsafe")); err == nil {
				t.Fatalf("WriteBytes(%q) error = nil", path)
			}
		})
	}
}

func TestStore_RejectsSymlinkEscapeAndDestination(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	rootPath := filepath.Join(base, "run")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatalf("creating root: %v", err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("creating outside: %v", err)
	}
	victim := filepath.Join(outside, "victim.json")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("writing victim: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "targets")); err != nil {
		t.Fatalf("creating directory symlink: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(rootPath, "manifest.json")); err != nil {
		t.Fatalf("creating file symlink: %v", err)
	}

	store, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	if err := store.WriteBytes("targets/failure.json", []byte("escaped")); err == nil {
		t.Fatal("WriteBytes() through directory symlink error = nil")
	}
	if err := store.WriteBytes("manifest.json", []byte("replaced")); err == nil {
		t.Fatal("WriteBytes() to symlink error = nil")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("reading victim: %v", err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("victim = %q, want unchanged", data)
	}
}

func TestOpenUnder_RejectsSymlinkParentBeforeCreatingRun(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(caller, ".runtime")); err != nil {
		t.Fatalf("creating runtime symlink: %v", err)
	}
	store, err := OpenUnder(
		caller,
		filepath.Join(".runtime", "theme-template-sync", "synthetic-run"),
	)
	if err == nil {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("Close(): %v", closeErr)
		}
		t.Fatal("OpenUnder() error = nil")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "theme-template-sync")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside runtime was created, stat error = %v", statErr)
	}
}

func TestOpenExistingUnder_DoesNotCreateMissingPath(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	relative := filepath.Join("src", "typo", "run")
	store, err := OpenExistingUnder(caller, relative)
	if err == nil {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("Close(): %v", closeErr)
		}
		t.Fatal("OpenExistingUnder() error = nil")
	}
	if _, statErr := os.Lstat(filepath.Join(caller, "src")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing path was created, stat error = %v", statErr)
	}
}

func TestOpenExistingUnder_OpensOwnerOnlyRunRoot(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	relative := filepath.Join(".runtime", "theme-template-sync", "run")
	rootPath := filepath.Join(caller, relative)
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatalf("creating existing root: %v", err)
	}
	store, err := OpenExistingUnder(caller, relative)
	if err != nil {
		t.Fatalf("OpenExistingUnder(): %v", err)
	}
	if store.Path() != rootPath {
		t.Fatalf("store path = %q, want %q", store.Path(), rootPath)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func TestSHA256JSON_IsStable(t *testing.T) {
	t.Parallel()

	first := map[string]any{"z": 2, "a": []string{"one", "two"}}
	second := map[string]any{"a": []string{"one", "two"}, "z": 2}
	firstHash, err := SHA256JSON(first)
	if err != nil {
		t.Fatalf("SHA256JSON(first): %v", err)
	}
	secondHash, err := SHA256JSON(second)
	if err != nil {
		t.Fatalf("SHA256JSON(second): %v", err)
	}
	if firstHash != secondHash || len(firstHash) != 64 {
		t.Fatalf("hashes = %q/%q, want stable SHA-256", firstHash, secondHash)
	}
}

func TestMatchPreviewBinding_DetectsEveryAuthorityChange(t *testing.T) {
	t.Parallel()

	base := PreviewBinding{
		PlanSHA256:  strings.Repeat("a", 64),
		AllowRemove: true,
		Template:    "page.example",
		Scope: syncengine.Scope{
			Mode:       syncengine.ModeField,
			SectionKey: "hero",
			Field:      "custom_css",
		},
		Source:             Identity{Store: "store-source", ID: "101", Name: "Source", Role: "unpublished"},
		SourceBeforeSHA256: strings.Repeat("b", 64),
		Targets: []BoundTarget{{
			Identity:      Identity{Store: "store-target", ID: "202", Name: "Target", Role: "unpublished"},
			BeforeSHA256:  strings.Repeat("c", 64),
			PlannedSHA256: strings.Repeat("d", 64),
		}},
	}
	if err := MatchPreviewBinding(base, base); err != nil {
		t.Fatalf("MatchPreviewBinding(equal): %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*PreviewBinding)
	}{
		{name: "plan hash", mutate: func(binding *PreviewBinding) { binding.PlanSHA256 = strings.Repeat("e", 64) }},
		{name: "allow remove", mutate: func(binding *PreviewBinding) { binding.AllowRemove = false }},
		{name: "template", mutate: func(binding *PreviewBinding) { binding.Template = "product.example" }},
		{name: "scope", mutate: func(binding *PreviewBinding) { binding.Scope.Field = "other" }},
		{name: "source identity", mutate: func(binding *PreviewBinding) { binding.Source.ID = "999" }},
		{name: "source hash", mutate: func(binding *PreviewBinding) { binding.SourceBeforeSHA256 = strings.Repeat("f", 64) }},
		{name: "target identity", mutate: func(binding *PreviewBinding) { binding.Targets[0].Identity.Role = "live" }},
		{name: "target before hash", mutate: func(binding *PreviewBinding) { binding.Targets[0].BeforeSHA256 = strings.Repeat("1", 64) }},
		{name: "target planned hash", mutate: func(binding *PreviewBinding) { binding.Targets[0].PlannedSHA256 = strings.Repeat("2", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := cloneBinding(t, base)
			test.mutate(&changed)
			if err := MatchPreviewBinding(base, changed); err == nil {
				t.Fatal("MatchPreviewBinding() error = nil")
			}
		})
	}
}

func TestNewFailureRecord_RedactsCredentialValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		secrets []string
	}{
		{
			name:    "generic key value credentials",
			message: "request failed token=first-secret cookie=second-secret",
			secrets: []string{"first-secret", "second-secret"},
		},
		{
			name:    "Shopify and underscored token names",
			message: "SHOPIFY_ACCESS_TOKEN=shopify-secret access_token:api-secret",
			secrets: []string{"shopify-secret", "api-secret"},
		},
		{
			name:    "authorization bearer and standalone bearer",
			message: "Authorization: Bearer auth-secret; upstream said Bearer standalone-secret",
			secrets: []string{"auth-secret", "standalone-secret"},
		},
		{
			name:    "authorization basic scheme",
			message: "request failed Authorization: Basic synthetic-basic-secret",
			secrets: []string{"synthetic-basic-secret"},
		},
		{
			name: "authorization digest parameters",
			message: `request failed Authorization: Digest username="synthetic-digest-user", ` +
				`response="synthetic-digest-response", nonce="synthetic-digest-nonce"`,
			secrets: []string{
				"synthetic-digest-user",
				"synthetic-digest-response",
				"synthetic-digest-nonce",
			},
		},
		{
			name:    "multi-value cookie header",
			message: "request failed Cookie: first=synthetic-cookie-first; session=synthetic-cookie-second",
			secrets: []string{"synthetic-cookie-first", "synthetic-cookie-second"},
		},
		{
			name:    "JSON key and quoted value",
			message: `CLI said {"access_token":"json secret value","status":"denied"}`,
			secrets: []string{"json secret value"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := NewFailureRecord("WRITE_FAILED", errors.New(test.message))
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("marshaling record: %v", err)
			}
			for _, secret := range test.secrets {
				if bytes.Contains(encoded, []byte(secret)) {
					t.Fatalf("failure record leaked %q: %s", secret, encoded)
				}
			}
			if record.Kind != "WRITE_FAILED" || record.Message == "" {
				t.Fatalf("failure record = %#v, want categorized safe message", record)
			}
		})
	}
}

func cloneBinding(t *testing.T, value PreviewBinding) PreviewBinding {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshaling binding: %v", err)
	}
	var clone PreviewBinding
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatalf("unmarshaling binding: %v", err)
	}
	if !reflect.DeepEqual(value, clone) {
		t.Fatalf("binding clone changed value:\n%#v\n%#v", value, clone)
	}
	return clone
}
