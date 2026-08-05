package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
)

func TestRuntimeNeverUsesGlobalLighthouse(t *testing.T) {
	commands := &recordingCommandRunner{
		run: func(context.Context, string, []string, string, []string, io.Writer, io.Writer) error {
			t.Fatal("runtime queried an external command")
			return nil
		},
	}
	manager := testManager(t, commands)

	_, err := manager.Runtime(context.Background())
	if !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("err=%v", err)
	}
	if got := manager.Status(context.Background()); got != NeedsSetup {
		t.Fatalf("status=%q want=%q", got, NeedsSetup)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("external command calls=%v", commands.calls)
	}
}

func TestSetupInstallsLockedEngineThroughNPMCI(t *testing.T) {
	ctxKey := struct{}{}
	ctx := context.WithValue(context.Background(), ctxKey, "setup-context")
	commands := &recordingCommandRunner{}
	manager := testManager(t, commands)
	target := manager.targetDir(t)
	commands.run = func(gotCtx context.Context, name string, args []string, dir string, environment []string, stdout, stderr io.Writer) error {
		if gotCtx.Value(ctxKey) != "setup-context" {
			t.Fatal("setup context was not propagated")
		}
		if name != "npm" {
			t.Fatalf("command=%q want=npm", name)
		}
		wantArgs := []string{"ci", "--ignore-scripts", "--no-bin-links", "--no-audit", "--no-fund"}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("args=%v want=%v", args, wantArgs)
		}
		if environment == nil || hasEnvironmentKey(environment, "NODE_OPTIONS") || hasEnvironmentKey(environment, "NODE_PATH") || hasEnvironmentKey(environment, "CHROME_PATH") {
			t.Fatalf("npm environment is not executor-owned: %v", environment)
		}
		if filepath.Dir(dir) != filepath.Dir(target) || !strings.HasPrefix(filepath.Base(dir), ".13.4.1.staging-") {
			t.Fatalf("staging dir is not a unique target sibling: %q", dir)
		}
		assertFileEquals(t, filepath.Join(dir, "package.json"), npmPackageJSON)
		assertFileEquals(t, filepath.Join(dir, "package-lock.json"), npmPackageLock)
		installUnmanagedFixture(t, dir, lighthouseVersion)
		return nil
	}

	status, err := manager.Setup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status != Ready {
		t.Fatalf("status=%q want=%q", status, Ready)
	}
	if len(commands.calls) != 1 {
		t.Fatalf("command calls=%d want=1", len(commands.calls))
	}
	runtime, err := manager.Runtime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Version() != lighthouseVersion {
		t.Fatalf("version=%q want=%q", runtime.Version(), lighthouseVersion)
	}
	if runtime.CLIPath() != filepath.Join(target, installPayloadDir, lighthouseCLIPath) {
		t.Fatalf("CLI path=%q", runtime.CLIPath())
	}
	payloadInfo, err := os.Lstat(filepath.Join(target, installPayloadDir))
	if err != nil || !payloadInfo.IsDir() {
		t.Fatalf("atomic payload is missing or invalid: info=%v err=%v", payloadInfo, err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".staging-") {
			t.Fatalf("staging directory was not cleaned: %q", entry.Name())
		}
	}
	managedDirs, err := manager.managerOwnedDirs()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range managedDirs {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("new manager directory %s mode=%#o want=0700", filepath.Base(path), info.Mode().Perm())
		}
	}
	markerBytes, err := os.ReadFile(filepath.Join(target, installPayloadDir, installMarkerPath))
	if err != nil {
		t.Fatal(err)
	}
	var marker map[string]any
	if err := json.Unmarshal(markerBytes, &marker); err != nil {
		t.Fatal(err)
	}
	digest, _ := marker["payloadSHA256"].(string)
	if len(digest) != sha256.Size*2 {
		t.Fatalf("payload digest length=%d want=%d", len(digest), sha256.Size*2)
	}
	payloadEntries, err := os.ReadDir(filepath.Join(target, installPayloadDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range payloadEntries {
		if strings.HasPrefix(entry.Name(), ".webperf-engine-marker-") {
			t.Fatalf("temporary marker survived publication: %q", entry.Name())
		}
	}
}

func TestRealEngineSetupSmoke(t *testing.T) {
	cacheRoot := os.Getenv("WEBPERF_ENGINE_SMOKE_ROOT")
	if cacheRoot == "" {
		t.Skip("set WEBPERF_ENGINE_SMOKE_ROOT to run the networked locked-engine smoke test")
	}
	if !filepath.IsAbs(cacheRoot) {
		t.Fatal("WEBPERF_ENGINE_SMOKE_ROOT must be an absolute path")
	}

	manager := Manager{cacheRoot: cacheRoot}
	status, err := manager.Setup(context.Background())
	if err != nil || status != Ready {
		t.Fatalf("setup status=%s err=%v", status, err)
	}
	runtimeInfo, err := manager.Runtime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtimeInfo.Version() != LighthouseVersion || runtimeInfo.NodeVersion() == "" || runtimeInfo.ChromeVersion() == "" {
		t.Fatalf("runtime=%+v", runtimeInfo)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	result := NewRunner(runtimeInfo).RunRaw(context.Background(), []string{"--version"}, &stdout, &stderr)
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("raw version result=%+v stderr=%q", result, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != LighthouseVersion {
		t.Fatalf("raw version=%q", stdout.String())
	}
}

func TestSetupUsesNewCacheGenerationWithoutModifyingLegacyTarget(t *testing.T) {
	commands := &recordingCommandRunner{}
	manager := testManager(t, commands)
	commands.run = func(_ context.Context, _ string, _ []string, dir string, _ []string, _ io.Writer, _ io.Writer) error {
		installUnmanagedFixture(t, dir, lighthouseVersion)
		return nil
	}
	legacyTarget := filepath.Join(manager.cacheRoot, "webperf", "engines", "lighthouse", lighthouseVersion)
	if err := os.MkdirAll(legacyTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	legacySentinel := filepath.Join(legacyTarget, "legacy-sentinel")
	legacyContents := []byte("legacy cache remains untouched\n")
	if err := os.WriteFile(legacySentinel, legacyContents, 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := manager.Setup(context.Background())
	if err != nil || status != Ready {
		t.Fatalf("setup status=%s err=%v", status, err)
	}
	if manager.targetDir(t) == legacyTarget {
		t.Fatal("integrity-bound engine reused the legacy cache target")
	}
	got, err := os.ReadFile(legacySentinel)
	if err != nil || !bytes.Equal(got, legacyContents) {
		t.Fatalf("legacy cache changed: contents=%q err=%v", got, err)
	}
}

func TestSetupRejectsUnsafeCacheRootWithoutRunningNPM(t *testing.T) {
	if !isUnix(runtime.GOOS) {
		t.Skip("Unix permission bits are required")
	}
	commands := &recordingCommandRunner{}
	manager := testManager(t, commands)
	if err := os.Chmod(manager.cacheRoot, 0o777); err != nil {
		t.Fatal(err)
	}

	status, err := manager.Setup(context.Background())
	if !errors.Is(err, ErrCache) {
		t.Fatalf("err=%v", err)
	}
	if status != NeedsSetup {
		t.Fatalf("status=%q want=%q", status, NeedsSetup)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("npm calls=%d want=0", len(commands.calls))
	}
}

func TestSystemNodeResolverAndManagerRuntimeDoNotExecuteInheritedNodePreload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a Node shebang")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	preload := filepath.Join(directory, "preload.cjs")
	sentinel := filepath.Join(directory, "preload-executed")
	if err := os.WriteFile(preload, []byte(`require("node:fs").writeFileSync(process.env.WEBPERF_PRELOAD_SENTINEL, "executed")`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_OPTIONS", "--require="+preload)
	t.Setenv("NODE_PATH", directory)
	t.Setenv("WEBPERF_PRELOAD_SENTINEL", sentinel)

	fakeNode := filepath.Join(directory, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/usr/bin/env node\nprocess.stdout.write(\"v24.16.0\\n\")\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(nodePath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	resolver := systemNodeResolver{lookPath: func(string) (string, error) { return fakeNode, nil }}
	if _, err := resolver.Version(context.Background(), fakeNode); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		cacheRoot: t.TempDir(),
		node:      resolver,
		chrome:    staticChromeResolver{path: "/test/bin/chrome", version: "150.0.0.0"},
	}
	installFixture(t, manager.targetDir(t), lighthouseVersion)
	if _, err := manager.Runtime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inherited NODE_OPTIONS preload executed: %v", err)
	}
}

func TestSupportsNodeVersionRequiresCanonicalMajorMinorPatch(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "22.19.0", want: true},
		{version: "24.0.0", want: true},
		{version: "24.0", want: false},
		{version: "24.0.foo", want: false},
		{version: "24.0.0.0", want: false},
		{version: "24.0.0-extra", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if got := SupportsNodeVersion(tt.version); got != tt.want {
				t.Fatalf("SupportsNodeVersion(%q)=%t want=%t", tt.version, got, tt.want)
			}
		})
	}
}

func TestSetupDoesNotReplaceInvalidExistingTarget(t *testing.T) {
	commands := &recordingCommandRunner{}
	manager := testManager(t, commands)
	target := manager.targetDir(t)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep-me")
	if err := os.WriteFile(marker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := manager.Setup(context.Background())
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("err=%v", err)
	}
	if status != NeedsSetup {
		t.Fatalf("status=%q want=%q", status, NeedsSetup)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("npm calls=%d want=0", len(commands.calls))
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil || string(contents) != "original" {
		t.Fatalf("existing target was changed: contents=%q err=%v", contents, readErr)
	}
	if strings.Contains(err.Error(), manager.cacheRoot) {
		t.Fatalf("error exposed absolute cache root: %v", err)
	}
}

func TestSetupDoesNotReplaceTargetCreatedDuringNPM(t *testing.T) {
	tests := []struct {
		name   string
		create func(*testing.T, string)
		intact func(*testing.T, string)
	}{
		{
			name: "empty directory",
			create: func(t *testing.T, target string) {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			intact: func(t *testing.T, target string) {
				entries, err := os.ReadDir(target)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("concurrent empty directory was replaced: entries=%d", len(entries))
				}
			},
		},
		{
			name: "symlink",
			create: func(t *testing.T, target string) {
				destination := t.TempDir()
				if err := os.Symlink(destination, target); err != nil {
					t.Fatal(err)
				}
			},
			intact: func(t *testing.T, target string) {
				info, err := os.Lstat(target)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("concurrent symlink was replaced")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands := &recordingCommandRunner{}
			manager := testManager(t, commands)
			target := manager.targetDir(t)
			commands.run = func(context.Context, string, []string, string, []string, io.Writer, io.Writer) error {
				installUnmanagedFixture(t, commands.calls[len(commands.calls)-1].dir, lighthouseVersion)
				tt.create(t, target)
				return nil
			}

			status, err := manager.Setup(context.Background())
			if !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("err=%v", err)
			}
			if status != NeedsSetup {
				t.Fatalf("status=%q want=%q", status, NeedsSetup)
			}
			tt.intact(t, target)
		})
	}
}

func TestSetupRollsBackOnlyEmptyClaimAfterRenameFailure(t *testing.T) {
	tests := []struct {
		name        string
		replacement string
		wantTarget  bool
	}{
		{name: "empty claim is removed"},
		{name: "non-empty foreign replacement is preserved", replacement: "non-empty", wantTarget: true},
		{name: "empty foreign replacement is preserved", replacement: "empty", wantTarget: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands := &recordingCommandRunner{}
			manager := testManager(t, commands)
			target := manager.targetDir(t)
			commands.run = func(_ context.Context, _ string, _ []string, dir string, _ []string, _ io.Writer, _ io.Writer) error {
				installUnmanagedFixture(t, dir, lighthouseVersion)
				return nil
			}
			manager.rename = func(_, destination string) error {
				claim := filepath.Dir(destination)
				if tt.replacement != "" {
					if err := os.RemoveAll(claim); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(claim, 0o700); err != nil {
						t.Fatal(err)
					}
					if tt.replacement == "non-empty" {
						if err := os.WriteFile(filepath.Join(claim, "foreign"), []byte("keep"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				return errors.New("injected rename failure")
			}

			status, err := manager.Setup(context.Background())
			if !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("err=%v", err)
			}
			if status != NeedsSetup {
				t.Fatalf("status=%q want=%q", status, NeedsSetup)
			}
			_, statErr := os.Lstat(target)
			if got := statErr == nil; got != tt.wantTarget {
				t.Fatalf("target exists=%t want=%t statErr=%v", got, tt.wantTarget, statErr)
			}
			if tt.replacement == "non-empty" {
				contents, readErr := os.ReadFile(filepath.Join(target, "foreign"))
				if readErr != nil || string(contents) != "keep" {
					t.Fatalf("foreign claim contents=%q err=%v", contents, readErr)
				}
			} else if tt.replacement == "empty" {
				entries, readErr := os.ReadDir(target)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("foreign empty claim entries=%d err=%v", len(entries), readErr)
				}
			}
		})
	}
}

func TestSetupFailureCleansStagingDirectory(t *testing.T) {
	commands := &recordingCommandRunner{}
	manager := testManager(t, commands)
	commands.run = func(context.Context, string, []string, string, []string, io.Writer, io.Writer) error {
		return errors.New("npm failed in " + manager.cacheRoot)
	}

	status, err := manager.Setup(context.Background())
	if err == nil {
		t.Fatal("expected setup error")
	}
	if status != NeedsSetup {
		t.Fatalf("status=%q want=%q", status, NeedsSetup)
	}
	parent := filepath.Dir(manager.targetDir(t))
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".staging-") {
			t.Fatalf("staging directory was not cleaned: %q", entry.Name())
		}
	}
	if strings.Contains(err.Error(), manager.cacheRoot) {
		t.Fatalf("error exposed absolute cache root: %v", err)
	}
}

func TestRuntimeRejectsWrongVersionAndMissingCLI(t *testing.T) {
	tests := []struct {
		name    string
		version string
		withCLI bool
	}{
		{name: "wrong version", version: "13.4.0", withCLI: true},
		{name: "missing CLI", version: lighthouseVersion, withCLI: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := testManager(t, &recordingCommandRunner{})
			installFixtureVersionOnly(t, manager.targetDir(t), tt.version)
			if tt.withCLI {
				writeCLI(t, manager.targetDir(t))
			}

			_, err := manager.Runtime(context.Background())
			if !errors.Is(err, ErrNeedsSetup) {
				t.Fatalf("err=%v", err)
			}
			if got := manager.Status(context.Background()); got != NeedsSetup {
				t.Fatalf("status=%q want=%q", got, NeedsSetup)
			}
		})
	}
}

func TestRuntimeRejectsUnmanagedCache(t *testing.T) {
	manager := testManager(t, &recordingCommandRunner{})
	installUnmanagedFixture(t, filepath.Join(manager.targetDir(t), installPayloadDir), lighthouseVersion)

	_, err := manager.Runtime(context.Background())
	if !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeRejectsTamperedInstallCredentials(t *testing.T) {
	tests := []string{"package.json", "package-lock.json", installMarkerPath}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			manager := testManager(t, &recordingCommandRunner{})
			target := manager.targetDir(t)
			installFixture(t, target, lighthouseVersion)
			path := filepath.Join(target, installPayloadDir, name)
			if err := os.WriteFile(path, []byte("tampered\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := manager.Runtime(context.Background())
			if !errors.Is(err, ErrNeedsSetup) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRuntimeRejectsTamperedPayloadDigestMarker(t *testing.T) {
	manager := testManager(t, &recordingCommandRunner{})
	target := manager.targetDir(t)
	installFixture(t, target, lighthouseVersion)
	markerPath := filepath.Join(target, installPayloadDir, installMarkerPath)
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker integrityMarker
	if err := json.Unmarshal(markerBytes, &marker); err != nil {
		t.Fatal(err)
	}
	marker.PayloadSHA256 = strings.Repeat("0", sha256.Size*2)
	markerBytes, err = json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, append(markerBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = manager.Runtime(context.Background())
	if !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeRejectsTamperedInstalledPayload(t *testing.T) {
	tests := []string{
		lighthouseCLIPath,
		"node_modules/dependency/index.js",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			manager := testManager(t, &recordingCommandRunner{})
			installFixture(t, manager.targetDir(t), lighthouseVersion)
			path := filepath.Join(manager.targetDir(t), installPayloadDir, filepath.FromSlash(name))
			if err := os.WriteFile(path, []byte("tampered\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := manager.Runtime(context.Background())
			if !errors.Is(err, ErrNeedsSetup) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRuntimeRejectsGroupWritableCacheHierarchy(t *testing.T) {
	if !isUnix(runtime.GOOS) {
		t.Skip("Unix permission bits are required")
	}
	tests := []struct {
		name string
		path func(Manager) string
	}{
		{name: "cache root", path: func(manager Manager) string { return manager.cacheRoot }},
		{name: "manager root", path: func(manager Manager) string { return filepath.Join(manager.cacheRoot, "webperf") }},
		{name: "engine target", path: func(manager Manager) string { return manager.targetDir(t) }},
		{name: "payload root", path: func(manager Manager) string { return filepath.Join(manager.targetDir(t), installPayloadDir) }},
		{name: "node modules", path: func(manager Manager) string {
			return filepath.Join(manager.targetDir(t), installPayloadDir, "node_modules")
		}},
		{name: "payload file", path: func(manager Manager) string {
			return filepath.Join(manager.targetDir(t), installPayloadDir, "node_modules", "dependency", "index.js")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := testManager(t, &recordingCommandRunner{})
			installFixture(t, manager.targetDir(t), lighthouseVersion)
			if err := os.Chmod(tt.path(manager), 0o770); err != nil {
				t.Fatal(err)
			}

			_, err := manager.Runtime(context.Background())
			if !errors.Is(err, ErrNeedsSetup) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRuntimeAcceptsSafeReadOnlySharedCacheRoot(t *testing.T) {
	if !isUnix(runtime.GOOS) {
		t.Skip("Unix permission bits are required")
	}
	manager := testManager(t, &recordingCommandRunner{})
	installFixture(t, manager.targetDir(t), lighthouseVersion)
	if err := os.Chmod(manager.cacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Runtime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRejectsSymlinkAnywhereInPayload(t *testing.T) {
	manager := testManager(t, &recordingCommandRunner{})
	target := manager.targetDir(t)
	installFixture(t, target, lighthouseVersion)
	dependency := filepath.Join(target, installPayloadDir, "node_modules", "dependency", "index.js")
	backup := dependency + ".original"
	if err := os.Rename(dependency, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, dependency); err != nil {
		t.Fatal(err)
	}

	_, err := manager.Runtime(context.Background())
	if !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeRejectsPayloadReplacementDuringAssembly(t *testing.T) {
	manager := testManager(t, &recordingCommandRunner{})
	target := manager.targetDir(t)
	installFixture(t, target, lighthouseVersion)
	replacementTarget := filepath.Join(t.TempDir(), "replacement")
	installFixture(t, replacementTarget, lighthouseVersion)
	payload := filepath.Join(target, installPayloadDir)
	replacementPayload := filepath.Join(replacementTarget, installPayloadDir)
	backup := filepath.Join(filepath.Dir(payload), "old-payload")
	manager.node = hookNodeResolver{
		path:    "/test/bin/node-22.19",
		version: "22.19.0",
		hook: func() {
			if err := os.Rename(payload, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacementPayload, payload); err != nil {
				t.Fatal(err)
			}
		},
	}

	_, err := manager.Runtime(context.Background())
	if !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeRejectsManagedPathSymlinks(t *testing.T) {
	t.Run("target", func(t *testing.T) {
		manager := testManager(t, &recordingCommandRunner{})
		actual := t.TempDir()
		installFixture(t, actual, lighthouseVersion)
		target := manager.targetDir(t)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(actual, target); err != nil {
			t.Fatal(err)
		}

		_, err := manager.Runtime(context.Background())
		if !errors.Is(err, ErrNeedsSetup) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("node_modules", func(t *testing.T) {
		manager := testManager(t, &recordingCommandRunner{})
		actual := t.TempDir()
		installFixture(t, actual, lighthouseVersion)
		target := manager.targetDir(t)
		installFixture(t, target, lighthouseVersion)
		targetNodeModules := filepath.Join(target, installPayloadDir, "node_modules")
		if err := os.RemoveAll(targetNodeModules); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(actual, installPayloadDir, "node_modules"), targetNodeModules); err != nil {
			t.Fatal(err)
		}

		_, err := manager.Runtime(context.Background())
		if !errors.Is(err, ErrNeedsSetup) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRuntimeRejectsManagerOwnedAncestorSymlink(t *testing.T) {
	tests := []struct {
		name              string
		linkParts         []string
		externalRemainder []string
	}{
		{name: "webperf", linkParts: []string{"webperf"}, externalRemainder: []string{"engines", "lighthouse", lighthouseVersion}},
		{name: "engines", linkParts: []string{"webperf", "engines"}, externalRemainder: []string{"lighthouse", lighthouseVersion}},
		{name: "lighthouse", linkParts: []string{"webperf", "engines", "lighthouse"}, externalRemainder: []string{lighthouseVersion}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := testManager(t, &recordingCommandRunner{})
			external := t.TempDir()
			externalTargetParts := append([]string{external}, tt.externalRemainder...)
			installFixture(t, filepath.Join(externalTargetParts...), lighthouseVersion)
			linkParts := append([]string{manager.cacheRoot}, tt.linkParts...)
			linkPath := filepath.Join(linkParts...)
			if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, linkPath); err != nil {
				t.Fatal(err)
			}

			_, err := manager.Runtime(context.Background())
			if !errors.Is(err, ErrNeedsSetup) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRuntimeRequiresNodeAtLeast2219(t *testing.T) {
	tests := []struct {
		name        string
		lookup      func(string) (string, error)
		version     string
		wantErr     bool
		wantCommand bool
	}{
		{
			name: "missing",
			lookup: func(string) (string, error) {
				return "", errors.New("node missing")
			},
			wantErr: true,
		},
		{
			name:        "too old",
			lookup:      func(string) (string, error) { return "/test/bin/node-22.18", nil },
			version:     "v22.18.0\n",
			wantErr:     true,
			wantCommand: true,
		},
		{
			name:        "minimum supported",
			lookup:      func(string) (string, error) { return "/test/bin/node-22.19", nil },
			version:     "v22.19.0\n",
			wantCommand: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands := &recordingCommandRunner{}
			commands.run = func(_ context.Context, name string, args []string, _ string, _ []string, stdout, _ io.Writer) error {
				if name == "node" {
					t.Fatal("runtime used unresolved PATH node")
				}
				if !reflect.DeepEqual(args, []string{"--version"}) {
					t.Fatalf("args=%v", args)
				}
				_, err := io.WriteString(stdout, tt.version)
				return err
			}
			manager := Manager{
				cacheRoot: t.TempDir(),
				node: systemNodeResolver{
					lookPath: tt.lookup,
					commands: commands,
				},
				chrome: staticChromeResolver{path: "/test/bin/chrome", version: "150.0.0.0"},
			}
			installFixture(t, manager.targetDir(t), lighthouseVersion)

			runtime, err := manager.Runtime(context.Background())
			if tt.wantErr {
				if !errors.Is(err, ErrNeedsSetup) {
					t.Fatalf("err=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if runtime.NodePath() != "/test/bin/node-22.19" {
					t.Fatalf("node path=%q", runtime.NodePath())
				}
			}
			if got := len(commands.calls) > 0; got != tt.wantCommand {
				t.Fatalf("node command called=%t want=%t", got, tt.wantCommand)
			}
		})
	}
}

func TestRuntimeReportsResolvedNodeAndChromeVersions(t *testing.T) {
	manager := testManager(t, &recordingCommandRunner{})
	manager.node = staticNodeResolver{path: "/test/bin/node", version: "22.20.0"}
	manager.chrome = staticChromeResolver{path: "/test/bin/chrome", version: "150.0.7871.187"}
	installFixture(t, manager.targetDir(t), lighthouseVersion)

	runtime, err := manager.Runtime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.NodeVersion() != "22.20.0" || runtime.ChromeVersion() != "150.0.7871.187" {
		t.Fatalf("node=%q chrome=%q", runtime.NodeVersion(), runtime.ChromeVersion())
	}
	if runtime.ChromePath() != "/test/bin/chrome" {
		t.Fatalf("chrome path=%q", runtime.ChromePath())
	}
}

func TestRunnerUsesLockedCLIAndProfileArgumentsWithoutShell(t *testing.T) {
	commands := &recordingCommandRunner{}
	manager := testManager(t, commands)
	installFixture(t, manager.targetDir(t), lighthouseVersion)
	runtime, err := manager.Runtime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctxKey := struct{}{}
	ctx := context.WithValue(context.Background(), ctxKey, "runner-context")
	commands.run = func(gotCtx context.Context, name string, args []string, dir string, environment []string, stdout, stderr io.Writer) error {
		if gotCtx.Value(ctxKey) != "runner-context" {
			t.Fatal("runner context was not propagated")
		}
		if name != runtime.NodePath() {
			t.Fatalf("command=%q want=%q", name, runtime.NodePath())
		}
		wantArgs := []string{
			runtime.CLIPath(),
			"https://example.com/product?a=1&b=2",
			"--form-factor=desktop",
			"--throttling-method=provided",
			"--output=json",
		}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("args=%v want=%v", args, wantArgs)
		}
		if got := chromePathValue(environment); got != "/test/bin/chrome" {
			t.Fatalf("CHROME_PATH=%q env=%v", got, environment)
		}
		return nil
	}
	runner := newRunner(runtime, commands)

	result := runner.Run(ctx, Request{
		URL: "https://example.com/product?a=1&b=2",
		Profile: profile.Profile{
			LighthouseArgs: []string{"--form-factor=desktop", "--throttling-method=provided"},
		},
		Args: []string{"--output=json"},
	})
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(commands.calls) != 1 {
		t.Fatalf("command calls=%d want=1", len(commands.calls))
	}
}

func TestRunnerRejectsUnverifiedRuntime(t *testing.T) {
	commands := &recordingCommandRunner{}
	result := newRunner(Runtime{}, commands).Run(context.Background(), Request{URL: "https://example.com"})
	if !errors.Is(result.Err, ErrNeedsSetup) {
		t.Fatalf("err=%v", result.Err)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("external command calls=%d want=0", len(commands.calls))
	}
}

func TestRunnerRunRawUsesOnlyLockedNodeCLIAndResolvedChrome(t *testing.T) {
	commands := &recordingCommandRunner{}
	manager := testManager(t, commands)
	installFixture(t, manager.targetDir(t), lighthouseVersion)
	runtime, err := manager.Runtime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := newRunner(runtime, commands).RunRaw(context.Background(), []string{"--output=json", "https://example.test"}, io.Discard, io.Discard)
	if result.Err != nil || result.ExitCode != 0 || len(commands.calls) != 1 {
		t.Fatalf("result=%+v calls=%+v", result, commands.calls)
	}
	call := commands.calls[0]
	if call.name != runtime.NodePath() {
		t.Fatalf("command=%q", call.name)
	}
	want := []string{runtime.CLIPath(), "--output=json", "https://example.test"}
	if !reflect.DeepEqual(call.args, want) {
		t.Fatalf("args=%v want=%v", call.args, want)
	}
	if got := chromePathValue(call.env); got != "/test/bin/chrome" {
		t.Fatalf("CHROME_PATH=%q env=%v", got, call.env)
	}
}

func TestRunnerRunRawRejectsUserChromePath(t *testing.T) {
	result := newRunner(Runtime{version: lighthouseVersion, cliPath: "cli", nodePath: "node", nodeVersion: "24.16.0", chromePath: "chrome", chromeVersion: "150.0.0.0"}, &recordingCommandRunner{}).RunRaw(context.Background(), []string{"--chrome-path=other"}, io.Discard, io.Discard)
	if !errors.Is(result.Err, ErrRawChromePath) {
		t.Fatalf("result=%+v", result)
	}
}

func TestChromeEnvironmentReplacesInheritedChromePathCaseInsensitively(t *testing.T) {
	got := chromeEnvironment([]string{"KEEP=value", "CHROME_PATH=/attacker/chrome", "chrome_path=C:\\attacker\\chrome", "Chrome_Path=other"}, "/verified/chrome")
	want := []string{"KEEP=value", "CHROME_PATH=/verified/chrome"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env=%v want=%v", got, want)
	}
}

func TestChromeEnvironmentPreventsInheritedNodePreloadExecution(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	preload := filepath.Join(directory, "preload.cjs")
	sentinel := filepath.Join(directory, "preload-executed")
	if err := os.WriteFile(preload, []byte(`require("node:fs").writeFileSync(process.env.WEBPERF_PRELOAD_SENTINEL, "executed")`), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := chromeEnvironment(append(os.Environ(), "NODE_OPTIONS=--require="+preload, "NODE_PATH="+directory, "WEBPERF_PRELOAD_SENTINEL="+sentinel), "/verified/chrome")
	var stdout bytes.Buffer
	err = (execCommandRunner{terminationGrace: defaultGrace}).Run(context.Background(), nodePath, []string{"-e", `process.stdout.write("ok")`}, "", environment, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "ok" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if _, err := os.Lstat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inherited NODE_OPTIONS preload executed: %v", err)
	}
	for _, blocked := range []string{"NODE_OPTIONS", "NODE_PATH"} {
		for _, item := range environment {
			name, value, found := strings.Cut(item, "=")
			if found && strings.EqualFold(name, blocked) {
				t.Fatalf("%s survived sanitization: %q", blocked, value)
			}
		}
	}
}

func TestOwnedCommandStopsAfterContextCancellation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = (execCommandRunner{terminationGrace: 100 * time.Millisecond}).Run(
		ctx,
		executable,
		[]string{"-test.run=^TestEngineHelperProcess$"},
		"",
		nil,
		io.Discard,
		io.Discard,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestOwnedCommandKillsDescendantsThatIgnoreTermination(t *testing.T) {
	if !isUnix(runtime.GOOS) {
		t.Skip("process-group behavior is Unix-specific")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- (execCommandRunner{terminationGrace: 100 * time.Millisecond}).Run(
			ctx,
			executable,
			[]string{"-test.run=^TestEngineProcessTreeHelper$", "-test.outputdir=" + helperDir},
			"",
			nil,
			io.Discard,
			io.Discard,
		)
	}()

	pidPath := filepath.Join(helperDir, "grandchild.pid")
	waitForFile(t, pidPath)
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grandchild.Kill() })

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owned command did not stop")
	}
	if processStillExists(grandchild, time.Second) {
		t.Fatalf("descendant process %d survived process-group cancellation", pid)
	}
}

func TestOwnedCommandRetainsDistinctGroupLeaderUntilCleanup(t *testing.T) {
	if !isUnix(runtime.GOOS) {
		t.Skip("process-group behavior is Unix-specific")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- (execCommandRunner{terminationGrace: 50 * time.Millisecond}).Run(
			ctx,
			executable,
			[]string{"-test.run=^TestEngineOwnedTargetHelper$", "-test.outputdir=" + helperDir},
			"",
			nil,
			io.Discard,
			io.Discard,
		)
	}()

	pidPath := filepath.Join(helperDir, "owned-target.pid")
	waitForFile(t, pidPath)
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	pgidBytes, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(pgidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if pgid == pid {
		t.Fatalf("target pid %d is also the group leader; delayed escalation can outlive ownership", pid)
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owned command did not stop")
	}
}

func TestGuardianCleansOwnedGroupWhenOwnerDiesAbruptly(t *testing.T) {
	if !isUnix(runtime.GOOS) {
		t.Skip("process-group behavior is Unix-specific")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperDir := t.TempDir()
	owner := exec.Command(executable, "-test.run=^TestEngineAbruptOwnerHelper$", "-test.outputdir="+helperDir)
	owner.Stdout = io.Discard
	owner.Stderr = io.Discard
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill() })

	pidPath := filepath.Join(helperDir, "owned-target.pid")
	waitForFile(t, pidPath)
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	targetPID, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	pgidBytes, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(targetPID)).Output()
	if err != nil {
		t.Fatal(err)
	}
	guardianPID, err := strconv.Atoi(strings.TrimSpace(string(pgidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.FindProcess(targetPID)
	if err != nil {
		t.Fatal(err)
	}
	guardian, err := os.FindProcess(guardianPID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = target.Kill()
		_ = guardian.Kill()
	})

	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("abrupt owner unexpectedly exited successfully")
	}
	if processStillExists(guardian, time.Second) {
		t.Fatalf("guardian process %d survived owner death", guardianPID)
	}
	if processStillExists(target, time.Second) {
		t.Fatalf("owned descendant process %d survived owner death", targetPID)
	}
}

func TestEngineHelperProcess(t *testing.T) {
	if !containsArgument(os.Args, "-test.run=^TestEngineHelperProcess$") {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestEngineProcessTreeHelper(t *testing.T) {
	if !containsArgument(os.Args, "-test.run=^TestEngineProcessTreeHelper$") {
		return
	}
	outputDir := argumentValue(os.Args, "-test.outputdir=")
	child := exec.Command(os.Args[0], "-test.run=^TestEngineGrandchildHelper$", "-test.outputdir="+outputDir)
	child.Stdout = io.Discard
	child.Stderr = io.Discard
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, filepath.Join(outputDir, "grandchild.ready"))
	if err := os.WriteFile(filepath.Join(outputDir, "grandchild.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestEngineGrandchildHelper(t *testing.T) {
	if !containsArgument(os.Args, "-test.run=^TestEngineGrandchildHelper$") {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	outputDir := argumentValue(os.Args, "-test.outputdir=")
	if err := os.WriteFile(filepath.Join(outputDir, "grandchild.ready"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestEngineOwnedTargetHelper(t *testing.T) {
	if !containsArgument(os.Args, "-test.run=^TestEngineOwnedTargetHelper$") {
		return
	}
	outputDir := argumentValue(os.Args, "-test.outputdir=")
	if err := os.WriteFile(filepath.Join(outputDir, "owned-target.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestEngineAbruptOwnerHelper(t *testing.T) {
	if !containsArgument(os.Args, "-test.run=^TestEngineAbruptOwnerHelper$") {
		return
	}
	outputDir := argumentValue(os.Args, "-test.outputdir=")
	err := (execCommandRunner{terminationGrace: 50 * time.Millisecond}).Run(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=^TestEngineOwnedTargetHelper$", "-test.outputdir=" + outputDir},
		"",
		nil,
		io.Discard,
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func argumentValue(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}

func processStillExists(process *os.Process, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

func isUnix(goos string) bool {
	switch goos {
	case "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "linux", "netbsd", "openbsd", "solaris":
		return true
	default:
		return false
	}
}

type commandCall struct {
	name string
	args []string
	dir  string
	env  []string
}

type recordingCommandRunner struct {
	calls []commandCall
	run   func(context.Context, string, []string, string, []string, io.Writer, io.Writer) error
}

func (r *recordingCommandRunner) Run(ctx context.Context, name string, args []string, dir string, environment []string, stdout, stderr io.Writer) error {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...), dir: dir, env: append([]string(nil), environment...)})
	if r.run != nil {
		return r.run(ctx, name, args, dir, environment, stdout, stderr)
	}
	return nil
}

func chromePathValue(environment []string) string {
	for _, item := range environment {
		name, value, found := strings.Cut(item, "=")
		if found && strings.EqualFold(name, "CHROME_PATH") {
			return value
		}
	}
	return ""
}

func hasEnvironmentKey(environment []string, want string) bool {
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if found && strings.EqualFold(name, want) {
			return true
		}
	}
	return false
}

func testManager(t *testing.T, commands commandRunner) Manager {
	t.Helper()
	return Manager{
		cacheRoot: t.TempDir(),
		commands:  commands,
		node:      staticNodeResolver{path: "/test/bin/node-22.19"},
		chrome:    staticChromeResolver{path: "/test/bin/chrome", version: "150.0.0.0"},
	}
}

type staticNodeResolver struct {
	path    string
	version string
	err     error
}

type hookNodeResolver struct {
	path    string
	version string
	hook    func()
}

func (r hookNodeResolver) Resolve(context.Context) (string, error) {
	if r.hook != nil {
		r.hook()
	}
	return r.path, nil
}

func (r hookNodeResolver) Version(context.Context, string) (string, error) {
	return r.version, nil
}

func (r staticNodeResolver) Resolve(context.Context) (string, error) {
	return r.path, r.err
}

func (r staticNodeResolver) Version(context.Context, string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if r.version == "" {
		return "22.19.0", nil
	}
	return r.version, nil
}

type staticChromeResolver struct {
	path    string
	version string
	err     error
}

func (r staticChromeResolver) Resolve(context.Context) (chromeRuntime, error) {
	if r.err != nil {
		return chromeRuntime{}, r.err
	}
	return chromeRuntime{path: r.path, version: r.version}, nil
}

func (m Manager) targetDir(t *testing.T) string {
	t.Helper()
	target, err := m.engineDir()
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func installFixture(t *testing.T, root, version string) {
	t.Helper()
	payload := filepath.Join(root, installPayloadDir)
	writeManagedInstallFiles(t, payload)
	installUnmanagedFixture(t, payload, version)
	writeFixtureMarker(t, payload)
}

func installFixtureVersionOnly(t *testing.T, root, version string) {
	t.Helper()
	payload := filepath.Join(root, installPayloadDir)
	writeManagedInstallFiles(t, payload)
	installUnmanagedFixtureVersionOnly(t, payload, version)
	writeFixtureMarker(t, payload)
}

func installUnmanagedFixture(t *testing.T, root, version string) {
	t.Helper()
	installUnmanagedFixtureVersionOnly(t, root, version)
	writeCLI(t, root)
	writeDependency(t, root)
}

func writeDependency(t *testing.T, root string) {
	t.Helper()
	dependency := filepath.Join(root, "node_modules", "dependency", "index.js")
	if err := os.MkdirAll(filepath.Dir(dependency), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, []byte("export const value = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func installUnmanagedFixtureVersionOnly(t *testing.T, root, version string) {
	t.Helper()
	packageDir := filepath.Join(root, "node_modules", "lighthouse")
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := []byte(`{"name":"lighthouse","version":"` + version + `"}`)
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeManagedInstallFiles(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"package.json":      npmPackageJSON,
		"package-lock.json": npmPackageLock,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFixtureMarker(t *testing.T, root string) {
	t.Helper()
	snapshot, err := snapshotPayloadTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeInstallMarker(root, snapshot.digest); err != nil {
		t.Fatal(err)
	}
}

func writeCLI(t *testing.T, root string) {
	t.Helper()
	cli := filepath.Join(root, lighthouseCLIPath)
	if err := os.MkdirAll(filepath.Dir(cli), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/usr/bin/env node\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contents of %s do not match embedded source", filepath.Base(path))
	}
}
