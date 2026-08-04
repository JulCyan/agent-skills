package mediasync

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvFixture(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearGlobalShopifyAuth(t *testing.T) {
	t.Helper()
	t.Setenv("SHOPIFY_CLIENT_ID", "")
	t.Setenv("SHOPIFY_CLIENT_SECRET", "")
	t.Setenv("SHOPIFY_ADMIN_TOKEN", "")
}

func TestReadDotEnvDoesNotMutateProcess(t *testing.T) {
	path := writeEnvFixture(t, "SHOPIFY_CLIENT_ID=id-from-file\nSHOPIFY_CLIENT_SECRET=secret-from-file\n", 0o600)
	t.Setenv("SHOPIFY_CLIENT_ID", "process-id")

	values, err := readDotEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["SHOPIFY_CLIENT_ID"] != "id-from-file" {
		t.Fatalf("unexpected parsed client id: %q", values["SHOPIFY_CLIENT_ID"])
	}
	if os.Getenv("SHOPIFY_CLIENT_ID") != "process-id" {
		t.Fatalf("readDotEnv mutated process environment")
	}
}

func TestLoadDotEnvPreservesProcessPrecedence(t *testing.T) {
	path := writeEnvFixture(t, "SHOPIFY_CLIENT_ID=id-from-file\nSHOPIFY_CLIENT_SECRET=secret-from-file\n", 0o600)
	t.Setenv("SHOPIFY_CLIENT_ID", "process-id")
	t.Setenv("SHOPIFY_CLIENT_SECRET", "process-secret")

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SHOPIFY_CLIENT_ID") != "process-id" || os.Getenv("SHOPIFY_CLIENT_SECRET") != "process-secret" {
		t.Fatalf("dotenv overrode process credentials")
	}
}

func TestRemoteEnvAutoDiscoveryStartsFromCallerPathBase(t *testing.T) {
	projectRoot := t.TempDir()
	callerDir := filepath.Join(projectRoot, "nested", "project")
	if err := os.MkdirAll(callerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const key = "MEDIA_SYNC_AUTODISCOVERY_TEST"
	const value = "from-caller-project"
	if err := os.WriteFile(filepath.Join(projectRoot, ".env.local"), []byte(key+"="+value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
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

	if err := loadEnvForRemoteCommand(commandOptions{pathBase: callerDir}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != value {
		t.Fatalf("auto-discovered env value=%q, want caller project value", got)
	}
}

func TestDoctorExplicitEnvFileDoesNotExportSecrets(t *testing.T) {
	clearGlobalShopifyAuth(t)
	path := writeEnvFixture(t, "SHOPIFY_CLIENT_ID=id-from-file\nSHOPIFY_CLIENT_SECRET=secret-from-file\n", 0o600)
	var stdout bytes.Buffer

	err := run(t.Context(), []string{"doctor", "--env-file", path, "--format", "json"}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SHOPIFY_CLIENT_ID") != "" || os.Getenv("SHOPIFY_CLIENT_SECRET") != "" {
		t.Fatalf("doctor exported file credentials into process environment")
	}
	if strings.Contains(stdout.String(), "id-from-file") || strings.Contains(stdout.String(), "secret-from-file") {
		t.Fatalf("doctor leaked credential values: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"available": true`) || !strings.Contains(stdout.String(), `"source": "client_credentials_env_file"`) {
		t.Fatalf("doctor did not diagnose explicit env file: %s", stdout.String())
	}
}

func TestDoctorRejectsConflictingEnvFileFlags(t *testing.T) {
	path := writeEnvFixture(t, "SHOPIFY_CLIENT_ID=id-from-file\nSHOPIFY_CLIENT_SECRET=secret-from-file\n", 0o600)
	err := run(t.Context(), []string{"doctor", "--env-file", path, "--no-env-file", "--format", "json"}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "--env-file") || !strings.Contains(err.Error(), "--no-env-file") {
		t.Fatalf("expected conflicting env flag error, got %v", err)
	}
}

func writeEnvExample(t *testing.T, root string) {
	t.Helper()
	content := "# Shopify Admin API credentials\nSHOPIFY_CLIENT_ID=\nSHOPIFY_CLIENT_SECRET=\n"
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeJSONReport(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("invalid JSON report %q: %v", raw, err)
	}
	return report
}

func TestEnvInitDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	writeEnvExample(t, root)
	var stdout bytes.Buffer

	if err := run(t.Context(), []string{"env-init", "--root", root, "--dry-run", "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".env.local")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("env-init dry-run wrote .env.local")
	}
	report := decodeJSONReport(t, stdout.Bytes())
	if report["status"] != "PREVIEW_READY" || report["would_write"] != true || report["created"] != false {
		t.Fatalf("unexpected dry-run report: %#v", report)
	}
}

func TestEnvInitCreates0600AndRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	writeEnvExample(t, root)

	if err := run(t.Context(), []string{"env-init", "--root", root, "--format", "json"}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(root, ".env.local")
	info, err := os.Stat(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".env.local mode=%o, want 600", info.Mode().Perm())
	}
	before, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"env-init", "--root", root}, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("expected overwrite refusal, got %v", err)
	}
	after, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("overwrite refusal changed .env.local")
	}
}

func TestEnvInitSourceCopiesOnlyShopifyWhitelist(t *testing.T) {
	root := t.TempDir()
	writeEnvExample(t, root)
	source := writeEnvFixture(t, "SHOPIFY_CLIENT_ID=id-value\nSHOPIFY_CLIENT_SECRET=secret-value\nSHOPIFY_ADMIN_TOKEN_STORE_DE=token-value\nUNRELATED_SECRET=do-not-copy\n", 0o600)
	var stdout bytes.Buffer

	if err := run(t.Context(), []string{"env-init", "--root", root, "--source", source, "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".env.local"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"SHOPIFY_CLIENT_ID=id-value", "SHOPIFY_CLIENT_SECRET=secret-value", "SHOPIFY_ADMIN_TOKEN_STORE_DE=token-value"} {
		if !bytes.Contains(raw, []byte(expected)) {
			t.Fatalf("missing imported key %s in %q", expected, raw)
		}
	}
	if bytes.Contains(raw, []byte("UNRELATED_SECRET")) {
		t.Fatalf("copied non-Shopify secret: %q", raw)
	}
	for _, secret := range []string{"id-value", "secret-value", "token-value", "do-not-copy"} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("env-init leaked secret %q: %s", secret, stdout.String())
		}
	}
}

func TestEnvCheckRejectsPartialCredentialsAndLoosePermissions(t *testing.T) {
	root := t.TempDir()
	writeEnvExample(t, root)
	localPath := filepath.Join(root, ".env.local")
	if err := os.WriteFile(localPath, []byte("SHOPIFY_CLIENT_ID=id-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer

	err := run(t.Context(), []string{"env-check", "--root", root, "--format", "json"}, &stdout)
	if err == nil {
		t.Fatal("expected invalid env runtime")
	}
	if !strings.Contains(stdout.String(), "permissions") || !strings.Contains(stdout.String(), "must be configured together") {
		t.Fatalf("unexpected env-check report: %s", stdout.String())
	}
}

func TestEnvCheckRejectsOwnerExecutePermission(t *testing.T) {
	root := t.TempDir()
	writeEnvExample(t, root)
	localPath := filepath.Join(root, ".env.local")
	if err := os.WriteFile(localPath, []byte("SHOPIFY_CLIENT_ID=id-value\nSHOPIFY_CLIENT_SECRET=secret-value\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer

	err := run(t.Context(), []string{"env-check", "--root", root, "--format", "json"}, &stdout)
	if err == nil {
		t.Fatal("expected owner execute permission to be rejected")
	}
	if !strings.Contains(stdout.String(), "permissions") {
		t.Fatalf("unexpected env-check report: %s", stdout.String())
	}
}

func TestEnvCheckAcceptsSecureCompleteCredentials(t *testing.T) {
	root := t.TempDir()
	writeEnvExample(t, root)
	localPath := filepath.Join(root, ".env.local")
	if err := os.WriteFile(localPath, []byte("SHOPIFY_CLIENT_ID=id-value\nSHOPIFY_CLIENT_SECRET=secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer

	if err := run(t.Context(), []string{"env-check", "--root", root, "--format", "json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	report := decodeJSONReport(t, stdout.Bytes())
	if report["status"] != "READY" || report["mode"] != "0600" {
		t.Fatalf("unexpected env-check report: %#v", report)
	}
	if strings.Contains(stdout.String(), "id-value") || strings.Contains(stdout.String(), "secret-value") {
		t.Fatalf("env-check leaked credential values: %s", stdout.String())
	}
}

func TestEnvCheckDoesNotRequireHostExampleWhenLocalIsReady(t *testing.T) {
	root := t.TempDir()
	localPath := filepath.Join(root, ".env.local")
	if err := os.WriteFile(localPath, []byte("SHOPIFY_CLIENT_ID=id-value\nSHOPIFY_CLIENT_SECRET=secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer

	if err := run(t.Context(), []string{"env-check", "--root", root, "--format", "json"}, &stdout); err != nil {
		t.Fatalf("portable env-check must use the built-in contract: %v; report=%s", err, stdout.String())
	}
	report := decodeJSONReport(t, stdout.Bytes())
	if report["status"] != "READY" {
		t.Fatalf("unexpected portable env-check report: %#v", report)
	}
}
