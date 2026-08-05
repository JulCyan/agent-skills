package bundle

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	manifestFile  = "manifest.json"
	protocolFile  = "protocol.json"
	claimFile     = ".webperf-claim"
	directoryMode = 0o700
	jsonMode      = 0o600
)

var (
	mkdirDirectory = func(root *os.Root, name string, permission os.FileMode) error {
		return root.Mkdir(name, permission)
	}
	openRoot               = os.OpenRoot
	openBundleFile         = func(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
	openChildRoot          = func(parent *os.Root, name string) (*os.Root, error) { return parent.OpenRoot(name) }
	writeBundleJSON        = writeJSON
	afterInitialClaimLstat = func(parent *os.Root, name string) {}
	afterClaimIdentity     = func(parent *os.Root, name string) {}
	beforeCleanupRemove    = func(parent *os.Root, name string) {}
)

// Store is an evidence bundle rooted at Path. Creation never replaces an
// existing filesystem entry, including an existing symlink.
type Store struct {
	path     string
	manifest Manifest
	protocol Protocol
}

// Create atomically claims a new bundle directory. Existing targets fail closed
// and are never overwritten.
func Create(target string, manifest Manifest, protocol Protocol) (*Store, error) {
	if err := validateArtifacts(manifest.Artifacts); err != nil {
		return nil, err
	}
	parentRoot, base, err := openBoundParent(target)
	if err != nil {
		return nil, err
	}
	defer parentRoot.Close()

	claim, root, recovered, err := claimBundle(parentRoot, base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if recovered && bundleHasContent(root) {
		return nil, ErrExists
	}
	if err := root.Chmod(".", directoryMode); err != nil {
		cleanupClaim(parentRoot, base, claim, root)
		return nil, fmt.Errorf("set evidence bundle permissions: %w", err)
	}

	cleanup := true
	defer func() {
		if cleanup {
			cleanupClaim(parentRoot, base, claim, root)
		}
	}()
	if err := writeBundleJSON(root, manifestFile, manifest); err != nil {
		return nil, fmt.Errorf("write evidence manifest: %w", err)
	}
	if err := writeBundleJSON(root, protocolFile, protocol); err != nil {
		return nil, fmt.Errorf("write evidence protocol: %w", err)
	}
	_ = parentRoot.Remove(claimSidecar(base))
	cleanup = false
	return &Store{path: target, manifest: cloneManifest(manifest), protocol: protocol}, nil
}

// Open loads a complete bundle and rejects symlinks, non-regular JSON files,
// malformed JSON, and a second trailing JSON document.
func Open(target string) (*Store, error) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("open evidence bundle: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("open evidence bundle: not a directory")
	}
	root, err := openBoundRoot(target, info)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var manifest Manifest
	if err := readJSON(root, manifestFile, &manifest); err != nil {
		return nil, fmt.Errorf("read evidence manifest: %w", err)
	}
	if err := validateArtifacts(manifest.Artifacts); err != nil {
		return nil, err
	}
	var protocol Protocol
	if err := readJSON(root, protocolFile, &protocol); err != nil {
		return nil, fmt.Errorf("read evidence protocol: %w", err)
	}
	return &Store{path: target, manifest: manifest, protocol: protocol}, nil
}

// Path returns the root directory of this bundle.
func (s *Store) Path() string { return s.path }

// Manifest returns a copy of the run-level metadata.
func (s *Store) Manifest() Manifest { return cloneManifest(s.manifest) }

// Protocol returns the resolved measurement protocol.
func (s *Store) Protocol() Protocol { return s.protocol }

func validateArtifacts(artifacts []Artifact) error {
	for _, artifact := range artifacts {
		if !relativeArtifactPath(artifact.Path) {
			return fmt.Errorf("%w: %q", ErrInvalidArtifactPath, artifact.Path)
		}
	}
	return nil
}

func relativeArtifactPath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || filepath.IsAbs(value) {
		return false
	}
	clean := path.Clean(value)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && clean == value
}

func writeJSON(root *os.Root, filename string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	encoded = append(encoded, '\n')
	file, err := root.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, jsonMode)
	if err != nil {
		return fmt.Errorf("create JSON file: %w", err)
	}
	if err := file.Chmod(jsonMode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set JSON file permissions: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return fmt.Errorf("write JSON file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close JSON file: %w", err)
	}
	return nil
}

func readJSON(root *os.Root, filename string, target any) error {
	info, err := root.Lstat(filename)
	if err != nil {
		return fmt.Errorf("stat JSON file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("JSON file is not regular")
	}
	file, err := openBundleFile(root, filename)
	if err != nil {
		return fmt.Errorf("open JSON file: %w", err)
	}
	defer file.Close()
	bound, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat opened JSON file: %w", err)
	}
	if !bound.Mode().IsRegular() || !os.SameFile(info, bound) {
		return errors.New("JSON file changed before descriptor binding")
	}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("decode JSON: trailing JSON document")
		}
		return fmt.Errorf("decode JSON tail: %w", err)
	}
	return nil
}

func openBoundParent(target string) (*os.Root, string, error) {
	parent := filepath.Dir(target)
	base := filepath.Base(target)
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, "", fmt.Errorf("create evidence bundle parent: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, "", errors.New("create evidence bundle parent: not a directory")
	}
	root, err := openBoundRoot(parent, info)
	if err != nil {
		return nil, "", err
	}
	return root, base, nil
}

func openBoundRoot(target string, expected os.FileInfo) (*os.Root, error) {
	root, err := openRoot(target)
	if err != nil {
		return nil, fmt.Errorf("open evidence bundle root: %w", err)
	}
	actual, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("stat evidence bundle root: %w", err)
	}
	if !actual.IsDir() || !os.SameFile(expected, actual) {
		_ = root.Close()
		return nil, errors.New("evidence bundle root changed before descriptor binding")
	}
	return root, nil
}

type claimRecord struct {
	Token string `json:"token"`
}

func claimBundle(parent *os.Root, base string) (os.FileInfo, *os.Root, bool, error) {
	sidecar := claimSidecar(base)
	var record claimRecord
	if err := readJSON(parent, sidecar, &record); err == nil {
		claim, root, err := recoverClaim(parent, base, record)
		if err != nil {
			return nil, nil, false, ErrExists
		}
		return claim, root, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, fmt.Errorf("read evidence claim: %w", err)
	}

	token, err := randomToken()
	if err != nil {
		return nil, nil, false, fmt.Errorf("create evidence claim token: %w", err)
	}
	record.Token = token
	if err := writeJSON(parent, sidecar, record); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, nil, false, ErrExists
		}
		return nil, nil, false, fmt.Errorf("write evidence claim: %w", err)
	}
	if err := mkdirDirectory(parent, base, directoryMode); err != nil {
		_ = parent.Remove(sidecar)
		if errors.Is(err, os.ErrExist) {
			return nil, nil, false, ErrExists
		}
		return nil, nil, false, fmt.Errorf("claim evidence bundle directory: %w", err)
	}
	claim, err := parent.Lstat(base)
	if err != nil || claim.Mode()&os.ModeSymlink != 0 || !claim.IsDir() {
		return nil, nil, false, errors.New("evidence bundle claim changed before binding")
	}
	afterInitialClaimLstat(parent, base)
	root, err := openBoundChild(parent, base, claim)
	if err != nil {
		return nil, nil, false, err
	}
	if err := writeJSON(root, claimFile, record); err != nil {
		_ = root.Close()
		return nil, nil, false, fmt.Errorf("write evidence claim marker: %w", err)
	}
	afterClaimIdentity(parent, base)
	bound, err := parent.Lstat(base)
	if err != nil || !os.SameFile(claim, bound) {
		_ = root.Close()
		return nil, nil, false, errors.New("evidence bundle claim changed before binding")
	}
	if !claimMatches(root, record) {
		_ = root.Close()
		return nil, nil, false, errors.New("evidence bundle claim token mismatch")
	}
	return claim, root, false, nil
}

func recoverClaim(parent *os.Root, base string, record claimRecord) (os.FileInfo, *os.Root, error) {
	claim, err := parent.Lstat(base)
	if err != nil || claim.Mode()&os.ModeSymlink != 0 || !claim.IsDir() {
		return nil, nil, errors.New("evidence claim is unavailable")
	}
	root, err := openBoundChild(parent, base, claim)
	if err != nil {
		return nil, nil, err
	}
	if claimMatches(root, record) {
		return claim, root, nil
	}
	_ = root.Close()
	return nil, nil, errors.New("evidence claim token mismatch")
}

func claimMatches(root *os.Root, want claimRecord) bool {
	var got claimRecord
	return readJSON(root, claimFile, &got) == nil && got.Token != "" && got.Token == want.Token
}

func openBoundChild(parent *os.Root, base string, expected os.FileInfo) (*os.Root, error) {
	before, err := parent.Lstat(base)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() || !os.SameFile(expected, before) {
		return nil, errors.New("evidence bundle claim changed before descriptor binding")
	}
	root, err := openChildRoot(parent, base)
	if err != nil {
		return nil, fmt.Errorf("open evidence bundle claim: %w", err)
	}
	actual, err := root.Stat(".")
	after, afterErr := parent.Lstat(base)
	if err != nil || afterErr != nil || !actual.IsDir() || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(expected, actual) || !os.SameFile(expected, after) {
		_ = root.Close()
		return nil, errors.New("evidence bundle claim changed before descriptor binding")
	}
	return root, nil
}

func bundleHasContent(root *os.Root) bool {
	for _, filename := range []string{manifestFile, protocolFile} {
		if _, err := root.Lstat(filename); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func cleanupClaim(parent *os.Root, base string, claim os.FileInfo, root *os.Root) {
	current, err := parent.Lstat(base)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(claim, current) {
		return
	}
	beforeCleanupRemove(parent, base)
	current, err = parent.Lstat(base)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(claim, current) {
		return
	}
	_ = root.Remove(manifestFile)
	_ = root.Remove(protocolFile)
}

func claimSidecar(base string) string {
	sum := sha256.Sum256([]byte(base))
	return ".webperf-claim-" + hex.EncodeToString(sum[:]) + ".json"
}

func randomToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Artifacts = append([]Artifact(nil), manifest.Artifacts...)
	return manifest
}
