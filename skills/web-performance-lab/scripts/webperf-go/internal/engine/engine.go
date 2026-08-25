// Package engine installs and runs the repository-locked Lighthouse engine.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
)

const (
	// LighthouseVersion is the only Lighthouse release accepted by the engine
	// and persisted evidence validator.
	LighthouseVersion = "13.4.1"
	lighthouseVersion = LighthouseVersion
	requiredNodeMajor = 22
	requiredNodeMinor = 19
	cacheGeneration   = "v2"
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
	ErrRawChromePath = errors.New("raw lighthouse cannot override the resolved Chrome path")
)

var canonicalNodeVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type commandRunner interface {
	Run(context.Context, string, []string, string, []string, io.Writer, io.Writer) error
}

type nodeResolver interface {
	Resolve(context.Context) (string, error)
	Version(context.Context, string) (string, error)
}

type chromeResolver interface {
	Resolve(context.Context) (chromeRuntime, error)
}

type chromeRuntime struct {
	path    string
	version string
}

// Manager owns the locked Lighthouse cache lifecycle.
// Its zero value uses os.UserCacheDir and the production command runner.
type Manager struct {
	cacheRoot string
	commands  commandRunner
	node      nodeResolver
	chrome    chromeResolver
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
	cacheRoot := filepath.Dir(managedDirs[0])
	target := managedDirs[len(managedDirs)-1]
	if err := ensureCacheRoot(cacheRoot); err != nil {
		return NeedsSetup, err
	}
	for _, dir := range managedDirs[:len(managedDirs)-1] {
		if err := ensureManagedDirectory(dir); err != nil {
			return NeedsSetup, err
		}
	}
	if _, err := inspectManagedHierarchy(cacheRoot, managedDirs[:len(managedDirs)-1]); err != nil {
		return NeedsSetup, ErrCache
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
		[]string{"ci", "--ignore-scripts", "--no-bin-links", "--no-audit", "--no-fund"},
		staging,
		executorEnvironment(os.Environ(), ""),
		io.Discard,
		io.Discard,
	); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return NeedsSetup, ctxErr
		}
		return NeedsSetup, ErrInstall
	}
	payloadSnapshot, err := snapshotPayloadTree(staging)
	if err != nil {
		return NeedsSetup, ErrInstall
	}
	if err := writeInstallMarker(staging, payloadSnapshot.digest); err != nil {
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
	installed = true
	claim.releaseToken()
	if _, err := m.Runtime(ctx); err != nil {
		return NeedsSetup, fmt.Errorf("installed engine failed final verification: %w", ErrNeedsSetup)
	}
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
	cacheRoot := filepath.Dir(managedDirs[0])
	hierarchyBefore, err := inspectManagedHierarchy(cacheRoot, managedDirs)
	if err != nil {
		return Runtime{}, ErrNeedsSetup
	}
	target := managedDirs[len(managedDirs)-1]
	runtime, err := m.runtimeForPayload(ctx, filepath.Join(target, installPayloadDir))
	if err != nil {
		return Runtime{}, err
	}
	hierarchyAfter, err := inspectManagedHierarchy(cacheRoot, managedDirs)
	if err != nil || !sameManagedHierarchy(hierarchyBefore, hierarchyAfter) {
		return Runtime{}, ErrNeedsSetup
	}
	return runtime, nil
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
		filepath.Join(lighthouseRoot, lighthouseVersion+"-"+cacheGeneration),
	}, nil
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
	if err != nil || !trustedDirectory(targetInfo) {
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
	if statErr != nil || closeErr != nil || !trustedRegularFile(tokenInfo) {
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
	version       string
	cliPath       string
	nodePath      string
	nodeVersion   string
	chromePath    string
	chromeVersion string
}

func (r Runtime) Version() string { return r.version }

func (r Runtime) NodeVersion() string { return r.nodeVersion }

func (r Runtime) ChromeVersion() string { return r.chromeVersion }

func (r Runtime) OS() string { return runtime.GOOS }

func (r Runtime) Arch() string { return runtime.GOARCH }

type packageMetadata struct {
	Version string `json:"version"`
}

func (m Manager) runtimeForPayload(ctx context.Context, root string) (Runtime, error) {
	before, err := validatePayload(root)
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
	nodeVersion, err := resolver.Version(ctx, nodePath)
	if err != nil || !SupportsNodeVersion(nodeVersion) {
		return Runtime{}, ErrNeedsSetup
	}
	chrome := m.chrome
	if chrome == nil {
		chrome = systemChromeResolver{commands: m.commands}
	}
	chromeRuntime, err := chrome.Resolve(ctx)
	if err != nil || chromeRuntime.path == "" || chromeRuntime.version == "" {
		return Runtime{}, ErrNeedsSetup
	}
	after, err := validatePayload(root)
	if err != nil || !samePayloadValidation(before, after) {
		return Runtime{}, ErrNeedsSetup
	}
	runtime := after.runtime
	runtime.nodePath = nodePath
	runtime.nodeVersion = normalizedNodeVersion(nodeVersion)
	runtime.chromePath = chromeRuntime.path
	runtime.chromeVersion = chromeRuntime.version
	return runtime, nil
}

type systemNodeResolver struct {
	lookPath func(string) (string, error)
	commands commandRunner
}

func (r systemNodeResolver) Resolve(context.Context) (string, error) {
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
	return nodePath, nil
}

func (r systemNodeResolver) Version(ctx context.Context, nodePath string) (string, error) {
	commands := r.commands
	if commands == nil {
		commands = execCommandRunner{terminationGrace: defaultGrace}
	}
	var stdout bytes.Buffer
	if err := commands.Run(ctx, nodePath, []string{"--version"}, "", executorEnvironment(os.Environ(), ""), &stdout, io.Discard); err != nil {
		return "", ErrNeedsSetup
	}
	version := normalizedNodeVersion(stdout.String())
	if !SupportsNodeVersion(version) {
		return "", ErrNeedsSetup
	}
	return version, nil
}

// SupportsNodeVersion reports whether a canonical MAJOR.MINOR.PATCH Node
// version meets the same minimum the executor requires.
func SupportsNodeVersion(version string) bool {
	if !canonicalNodeVersionPattern.MatchString(version) {
		return false
	}
	parts := strings.Split(version, ".")
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > requiredNodeMajor || (major == requiredNodeMajor && minor >= requiredNodeMinor)
}

func normalizedNodeVersion(output string) string {
	return strings.TrimPrefix(strings.TrimSpace(output), "v")
}

type systemChromeResolver struct {
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	commands commandRunner
	goos     string
}

var chromeVersionPattern = regexp.MustCompile(`\d+(?:\.\d+){2,}`)

func (r systemChromeResolver) Resolve(ctx context.Context) (chromeRuntime, error) {
	lookup := r.lookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	stat := r.stat
	if stat == nil {
		stat = os.Stat
	}
	goos := r.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	candidates := chromeCandidates(goos)
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		resolved := candidate
		if !filepath.IsAbs(candidate) {
			path, err := lookup(candidate)
			if err != nil {
				continue
			}
			resolved = path
		} else if info, err := stat(candidate); err != nil || info.IsDir() {
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		version, err := r.version(ctx, resolved)
		if err == nil {
			return chromeRuntime{path: resolved, version: version}, nil
		}
	}
	return chromeRuntime{}, ErrNeedsSetup
}

func (r systemChromeResolver) version(ctx context.Context, chromePath string) (string, error) {
	commands := r.commands
	if commands == nil {
		commands = execCommandRunner{terminationGrace: defaultGrace}
	}
	var stdout bytes.Buffer
	if err := commands.Run(ctx, chromePath, []string{"--version"}, "", nil, &stdout, io.Discard); err != nil {
		return "", ErrNeedsSetup
	}
	version := chromeVersionPattern.FindString(stdout.String())
	if version == "" {
		return "", ErrNeedsSetup
	}
	return version, nil
}

func chromeCandidates(goos string) []string {
	candidates := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "Google Chrome"}
	switch goos {
	case "darwin":
		return append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	case "windows":
		return append(candidates, "chrome.exe")
	default:
		return candidates
	}
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
	if !r.validRuntime() {
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
	err := commands.Run(ctx, r.runtime.nodePath, args, "", chromeEnvironment(os.Environ(), r.runtime.chromePath), request.Stdout, request.Stderr)
	if err == nil {
		return Result{}
	}
	return commandResult(err)
}

// RunRaw forwards expert arguments only through the verified Runtime Node and
// fixed Lighthouse CLI. It injects the Runtime Chrome path and rejects a
// caller override so raw execution remains tied to its reported browser.
func (r Runner) RunRaw(ctx context.Context, args []string, stdout, stderr io.Writer) Result {
	if !r.validRuntime() {
		return Result{ExitCode: contract.ExitNeedsSetup, Err: ErrNeedsSetup}
	}
	for _, arg := range args {
		if arg == "--chrome-path" || strings.HasPrefix(arg, "--chrome-path=") {
			return Result{ExitCode: contract.ExitInvalidInput, Err: ErrRawChromePath}
		}
	}
	commandArgs := make([]string, 0, 1+len(args))
	commandArgs = append(commandArgs, r.runtime.cliPath)
	commandArgs = append(commandArgs, args...)
	commands := r.commands
	if commands == nil {
		commands = execCommandRunner{terminationGrace: defaultGrace}
	}
	err := commands.Run(ctx, r.runtime.nodePath, commandArgs, "", chromeEnvironment(os.Environ(), r.runtime.chromePath), stdout, stderr)
	if err == nil {
		return Result{}
	}
	return commandResult(err)
}

func (r Runner) validRuntime() bool {
	return r.runtime.version == lighthouseVersion && r.runtime.cliPath != "" && r.runtime.nodePath != "" && r.runtime.nodeVersion != "" && r.runtime.chromePath != "" && r.runtime.chromeVersion != ""
}

func commandResult(err error) Result {
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

func (r execCommandRunner) Run(ctx context.Context, name string, args []string, dir string, environment []string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if environment != nil {
		cmd.Env = append([]string(nil), environment...)
	}
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

// chromeEnvironment removes inherited browser and Node module-loading
// overrides before adding the runtime-verified Chrome path. NODE_OPTIONS can
// preload code (for example with --require or --import), while NODE_PATH
// changes module resolution; neither may influence the locked Lighthouse
// process. Windows environment keys are case-insensitive, so filtering is
// intentionally case-insensitive everywhere.
func chromeEnvironment(base []string, chromePath string) []string {
	return executorEnvironment(base, chromePath)
}

func executorEnvironment(base []string, chromePath string) []string {
	environment := make([]string, 0, len(base)+1)
	for _, item := range base {
		name, _, found := strings.Cut(item, "=")
		if found && blockedRuntimeEnvironment(name) {
			continue
		}
		environment = append(environment, item)
	}
	if chromePath != "" {
		environment = append(environment, "CHROME_PATH="+chromePath)
	}
	return environment
}

func blockedRuntimeEnvironment(name string) bool {
	return strings.EqualFold(name, "CHROME_PATH") || strings.EqualFold(name, "NODE_OPTIONS") || strings.EqualFold(name, "NODE_PATH")
}
