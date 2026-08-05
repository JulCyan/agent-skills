// Package engine installs and runs the repository-locked Lighthouse engine.
package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
)

const (
	lighthouseVersion = "13.4.1"
	lighthouseCLIPath = "node_modules/lighthouse/cli/index.js"
	installMarkerPath = ".webperf-engine.json"
	installPayloadDir = "payload"
	installClaimToken = ".webperf-claim"
	defaultGrace      = 2 * time.Second
)

type Status = contract.Status

const (
	Ready      Status = contract.OK
	NeedsSetup Status = contract.NeedsSetup
)

var (
	ErrNeedsSetup    = contract.ErrNeedsSetup
	ErrInvalidTarget = errors.New("invalid cached engine target")
	ErrCache         = errors.New("engine cache unavailable")
	ErrInstall       = errors.New("engine npm ci failed")
)

type commandRunner interface {
	Run(context.Context, string, []string, string, io.Writer, io.Writer) error
}

type nodeResolver interface {
	Resolve(context.Context) (string, error)
}

// Manager owns the locked Lighthouse cache lifecycle.
// Its zero value uses os.UserCacheDir and the production command runner.
type Manager struct {
	cacheRoot string
	commands  commandRunner
	node      nodeResolver
	rename    func(string, string) error
}

func (m Manager) Status(ctx context.Context) Status {
	if ctx.Err() != nil {
		return NeedsSetup
	}
	_, err := m.Runtime(ctx)
	if err != nil {
		return NeedsSetup
	}
	return Ready
}

func (m Manager) Setup(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return NeedsSetup, err
	}
	managedDirs, err := m.managerOwnedDirs()
	if err != nil {
		return NeedsSetup, err
	}
	target := managedDirs[len(managedDirs)-1]
	if err := os.MkdirAll(filepath.Dir(managedDirs[0]), 0o700); err != nil {
		return NeedsSetup, ErrCache
	}
	for _, dir := range managedDirs[:len(managedDirs)-1] {
		if err := ensureManagedDirectory(dir); err != nil {
			return NeedsSetup, ErrCache
		}
	}

	if _, statErr := os.Lstat(target); statErr == nil {
		if _, validateErr := m.Runtime(ctx); validateErr == nil {
			return Ready, nil
		}
		return NeedsSetup, fmt.Errorf("%w: remove the existing cached engine before setup", ErrInvalidTarget)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return NeedsSetup, ErrCache
	}

	parent := filepath.Dir(target)
	staging, err := os.MkdirTemp(parent, ".13.4.1.staging-")
	if err != nil {
		return NeedsSetup, ErrCache
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := writeAsset(filepath.Join(staging, "package.json"), npmPackageJSON); err != nil {
		return NeedsSetup, err
	}
	if err := writeAsset(filepath.Join(staging, "package-lock.json"), npmPackageLock); err != nil {
		return NeedsSetup, err
	}
	commands := m.commands
	if commands == nil {
		commands = execCommandRunner{terminationGrace: defaultGrace}
	}
	if err := commands.Run(
		ctx,
		"npm",
		[]string{"ci", "--ignore-scripts", "--no-audit", "--no-fund"},
		staging,
		io.Discard,
		io.Discard,
	); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return NeedsSetup, ctxErr
		}
		return NeedsSetup, ErrInstall
	}
	if err := writeAsset(filepath.Join(staging, installMarkerPath), installMarker()); err != nil {
		return NeedsSetup, err
	}
	if _, err := m.runtimeForPayload(ctx, staging); err != nil {
		return NeedsSetup, fmt.Errorf("installed engine failed verification: %w", ErrNeedsSetup)
	}
	claim, err := claimInstallTarget(target)
	if err != nil {
		return NeedsSetup, fmt.Errorf("atomic engine target claim failed: %w", ErrInvalidTarget)
	}
	rename := m.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(staging, filepath.Join(target, installPayloadDir)); err != nil {
		claim.rollback()
		return NeedsSetup, fmt.Errorf("atomic engine install failed: %w", ErrInvalidTarget)
	}
	claim.releaseToken()
	installed = true
	return Ready, nil
}

func (m Manager) Runtime(ctx context.Context) (Runtime, error) {
	if err := ctx.Err(); err != nil {
		return Runtime{}, err
	}
	managedDirs, err := m.managerOwnedDirs()
	if err != nil {
		return Runtime{}, err
	}
	if !managedDirectoriesValid(managedDirs) {
		return Runtime{}, ErrNeedsSetup
	}
	target := managedDirs[len(managedDirs)-1]
	return m.runtimeForPayload(ctx, filepath.Join(target, installPayloadDir))
}

func (m Manager) engineDir() (string, error) {
	managedDirs, err := m.managerOwnedDirs()
	if err != nil {
		return "", err
	}
	return managedDirs[len(managedDirs)-1], nil
}

func (m Manager) managerOwnedDirs() ([]string, error) {
	root := m.cacheRoot
	if root == "" {
		var err error
		root, err = os.UserCacheDir()
		if err != nil {
			return nil, ErrCache
		}
	}
	webperfRoot := filepath.Join(root, "webperf")
	enginesRoot := filepath.Join(webperfRoot, "engines")
	lighthouseRoot := filepath.Join(enginesRoot, "lighthouse")
	return []string{
		webperfRoot,
		enginesRoot,
		lighthouseRoot,
		filepath.Join(lighthouseRoot, lighthouseVersion),
	}, nil
}

func ensureManagedDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return ErrCache
	}
	return nil
}

func managedDirectoriesValid(paths []string) bool {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

type installClaim struct {
	target     string
	targetInfo os.FileInfo
	tokenPath  string
	tokenInfo  os.FileInfo
}

func claimInstallTarget(target string) (installClaim, error) {
	if err := os.Mkdir(target, 0o700); err != nil {
		return installClaim{}, err
	}
	targetInfo, err := os.Lstat(target)
	if err != nil || !targetInfo.IsDir() {
		return installClaim{}, ErrInvalidTarget
	}
	tokenPath := filepath.Join(target, installClaimToken)
	token, err := os.OpenFile(tokenPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		removeOwnedEmptyTarget(target, targetInfo)
		return installClaim{}, err
	}
	tokenInfo, statErr := token.Stat()
	closeErr := token.Close()
	if statErr != nil || closeErr != nil {
		if tokenInfo != nil {
			installClaim{
				target:     target,
				targetInfo: targetInfo,
				tokenPath:  tokenPath,
				tokenInfo:  tokenInfo,
			}.rollback()
		}
		return installClaim{}, ErrInvalidTarget
	}
	return installClaim{
		target:     target,
		targetInfo: targetInfo,
		tokenPath:  tokenPath,
		tokenInfo:  tokenInfo,
	}, nil
}

func (c installClaim) rollback() {
	if !sameEntry(c.target, c.targetInfo) || !sameEntry(c.tokenPath, c.tokenInfo) {
		return
	}
	if err := os.Remove(c.tokenPath); err != nil {
		return
	}
	if !sameEntry(c.target, c.targetInfo) {
		return
	}
	_ = os.Remove(c.target)
}

func (c installClaim) releaseToken() {
	if sameEntry(c.target, c.targetInfo) && sameEntry(c.tokenPath, c.tokenInfo) {
		_ = os.Remove(c.tokenPath)
	}
}

func sameEntry(path string, expected os.FileInfo) bool {
	current, err := os.Lstat(path)
	return err == nil && expected != nil && os.SameFile(expected, current)
}

func removeOwnedEmptyTarget(target string, targetInfo os.FileInfo) {
	if sameEntry(target, targetInfo) {
		_ = os.Remove(target)
	}
}

type Runtime struct {
	version  string
	cliPath  string
	nodePath string
}

func (r Runtime) Version() string { return r.version }

func (r Runtime) CLIPath() string { return r.cliPath }

func (r Runtime) NodePath() string { return r.nodePath }

type packageMetadata struct {
	Version string `json:"version"`
}

func validatePayload(root string) (Runtime, error) {
	managedPaths := []struct {
		path string
		dir  bool
	}{
		{path: root, dir: true},
		{path: filepath.Join(root, "package.json")},
		{path: filepath.Join(root, "package-lock.json")},
		{path: filepath.Join(root, installMarkerPath)},
		{path: filepath.Join(root, "node_modules"), dir: true},
		{path: filepath.Join(root, "node_modules", "lighthouse"), dir: true},
		{path: filepath.Join(root, "node_modules", "lighthouse", "package.json")},
		{path: filepath.Join(root, "node_modules", "lighthouse", "cli"), dir: true},
		{path: filepath.Join(root, lighthouseCLIPath)},
	}
	for _, managed := range managedPaths {
		info, err := os.Lstat(managed.path)
		if err != nil || managed.dir != info.IsDir() || (!managed.dir && !info.Mode().IsRegular()) {
			return Runtime{}, ErrNeedsSetup
		}
	}
	if !fileEquals(filepath.Join(root, "package.json"), npmPackageJSON) ||
		!fileEquals(filepath.Join(root, "package-lock.json"), npmPackageLock) ||
		!fileEquals(filepath.Join(root, installMarkerPath), installMarker()) {
		return Runtime{}, ErrNeedsSetup
	}
	metadataPath := filepath.Join(root, "node_modules", "lighthouse", "package.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return Runtime{}, ErrNeedsSetup
	}
	var metadata packageMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil || metadata.Version != lighthouseVersion {
		return Runtime{}, ErrNeedsSetup
	}
	cliPath := filepath.Join(root, lighthouseCLIPath)
	return Runtime{version: lighthouseVersion, cliPath: cliPath}, nil
}

func (m Manager) runtimeForPayload(ctx context.Context, root string) (Runtime, error) {
	runtime, err := validatePayload(root)
	if err != nil {
		return Runtime{}, err
	}
	resolver := m.node
	if resolver == nil {
		resolver = systemNodeResolver{commands: m.commands}
	}
	nodePath, err := resolver.Resolve(ctx)
	if err != nil {
		return Runtime{}, ErrNeedsSetup
	}
	runtime.nodePath = nodePath
	return runtime, nil
}

type systemNodeResolver struct {
	lookPath func(string) (string, error)
	commands commandRunner
}

func (r systemNodeResolver) Resolve(ctx context.Context) (string, error) {
	lookup := r.lookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	nodePath, err := lookup("node")
	if err != nil {
		return "", ErrNeedsSetup
	}
	nodePath, err = filepath.Abs(nodePath)
	if err != nil {
		return "", ErrNeedsSetup
	}
	commands := r.commands
	if commands == nil {
		commands = execCommandRunner{terminationGrace: defaultGrace}
	}
	var stdout bytes.Buffer
	if err := commands.Run(ctx, nodePath, []string{"--version"}, "", &stdout, io.Discard); err != nil {
		return "", ErrNeedsSetup
	}
	if !supportedNodeVersion(stdout.String()) {
		return "", ErrNeedsSetup
	}
	return nodePath, nil
}

func supportedNodeVersion(output string) bool {
	version := strings.TrimPrefix(strings.TrimSpace(output), "v")
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > 22 || (major == 22 && minor >= 19)
}

func installMarker() []byte {
	manifestHash := sha256.Sum256(npmPackageJSON)
	lockHash := sha256.Sum256(npmPackageLock)
	return []byte(fmt.Sprintf(
		"{\"schemaVersion\":1,\"lighthouseVersion\":%q,\"packageJSONSHA256\":%q,\"packageLockSHA256\":%q}\n",
		lighthouseVersion,
		fmt.Sprintf("%x", manifestHash),
		fmt.Sprintf("%x", lockHash),
	))
}

func fileEquals(path string, expected []byte) bool {
	contents, err := os.ReadFile(path)
	return err == nil && bytes.Equal(contents, expected)
}

func writeAsset(path string, contents []byte) error {
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return ErrCache
	}
	return nil
}

type Request struct {
	URL     string
	Profile profile.Profile
	Args    []string
	Stdout  io.Writer
	Stderr  io.Writer
}

type Result struct {
	ExitCode int
	Err      error
}

type Runner struct {
	runtime  Runtime
	commands commandRunner
}

func NewRunner(runtime Runtime) Runner {
	return newRunner(runtime, execCommandRunner{terminationGrace: defaultGrace})
}

func newRunner(runtime Runtime, commands commandRunner) Runner {
	return Runner{runtime: runtime, commands: commands}
}

func (r Runner) Run(ctx context.Context, request Request) Result {
	if r.runtime.version != lighthouseVersion || r.runtime.cliPath == "" || r.runtime.nodePath == "" {
		return Result{ExitCode: contract.ExitNeedsSetup, Err: ErrNeedsSetup}
	}
	args := make([]string, 0, 2+len(request.Profile.LighthouseArgs)+len(request.Args))
	args = append(args, r.runtime.cliPath, request.URL)
	args = append(args, request.Profile.LighthouseArgs...)
	args = append(args, request.Args...)
	commands := r.commands
	if commands == nil {
		commands = execCommandRunner{terminationGrace: defaultGrace}
	}
	err := commands.Run(ctx, r.runtime.nodePath, args, "", request.Stdout, request.Stderr)
	if err == nil {
		return Result{}
	}
	exitCode := 1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return Result{ExitCode: exitCode, Err: err}
}

type execCommandRunner struct {
	terminationGrace time.Duration
}

func (r execCommandRunner) Run(ctx context.Context, name string, args []string, dir string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	owned, err := startOwnedCommand(cmd)
	if err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- owned.wait() }()

	select {
	case err := <-wait:
		_ = owned.kill()
		owned.release()
		return err
	case <-ctx.Done():
		_ = owned.terminate()
	}

	grace := r.terminationGrace
	if grace <= 0 {
		grace = defaultGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	commandDone := false
	select {
	case <-wait:
		commandDone = true
		<-timer.C
	case <-timer.C:
	}
	_ = owned.kill()
	if !commandDone {
		<-wait
	}
	owned.release()
	return ctx.Err()
}
