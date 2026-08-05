package bundle

import (
	"bytes"
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
	manifestFile        = "manifest.json"
	pendingManifestFile = ".manifest.pending.json"
	protocolFile        = "protocol.json"
	summaryFile         = "summary.json"
	claimFile           = ".webperf-claim"
	directoryMode       = 0o700
	jsonMode            = 0o600
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
	writeTempJSON          = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	syncTempJSON           = func(file *os.File) error { return file.Sync() }
	syncCommittedJSON      = func(file *os.File) error { return file.Sync() }
	linkTempJSON           = func(root *os.Root, oldName, newName string) error { return root.Link(oldName, newName) }
	syncBundleDirectory    = syncRootDirectory
	beforeJSONLink         = func(root *os.Root, destination string) {}
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
	protocol = CompleteProtocol(protocol)
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
	if err := writeBundleJSON(root, pendingManifestFile, manifest); err != nil {
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

	manifestName, finalManifest, err := selectManifest(root)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := readJSON(root, manifestName, &manifest); err != nil {
		return nil, fmt.Errorf("read evidence manifest: %w", err)
	}
	if err := validateArtifacts(manifest.Artifacts); err != nil {
		return nil, err
	}
	var protocol Protocol
	if err := readJSON(root, protocolFile, &protocol); err != nil {
		return nil, fmt.Errorf("read evidence protocol: %w", err)
	}
	if finalManifest && manifest.SummarySHA256 != "" {
		if err := verifySummaryHash(root, manifest.SummarySHA256); err != nil {
			return nil, fmt.Errorf("verify evidence summary: %w", err)
		}
	} else if finalManifest {
		if err := ensureNoSummary(root); err != nil {
			return nil, fmt.Errorf("verify absent evidence summary: %w", err)
		}
	}
	return &Store{path: target, manifest: manifest, protocol: protocol}, nil
}

// Path returns the root directory of this bundle.
func (s *Store) Path() string { return s.path }

// Manifest returns a copy of the run-level metadata.
func (s *Store) Manifest() Manifest { return cloneManifest(s.manifest) }

// Protocol returns the resolved measurement protocol.
func (s *Store) Protocol() Protocol { return s.protocol }

// WriteArtifact creates one private, relative evidence artifact. It never
// replaces a prior artifact, including a pre-existing path introduced by
// another process.
func (s *Store) WriteArtifact(relativePath string, data []byte) error {
	if !relativeArtifactPath(relativePath) {
		return fmt.Errorf("%w: %q", ErrInvalidArtifactPath, relativePath)
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := makeArtifactParent(root, path.Dir(relativePath)); err != nil {
		return err
	}
	return writeBundleBytes(root, relativePath, data)
}

// Finalize records the final manifest and, for complete runs, writes one
// aggregate summary. A nil summary intentionally leaves an incomplete run
// inspectable without claiming stable aggregate conclusions.
func (s *Store) Finalize(manifest Manifest, summary *Summary) error {
	if err := validateArtifacts(manifest.Artifacts); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	manifest.SummarySHA256 = ""
	var summaryHash string
	if summary != nil {
		summary.SuccessfulRuns = len(summary.Samples)
		summaryValue := *summary
		encodedSummary, err := encodeJSON(summaryValue)
		if err != nil {
			return fmt.Errorf("encode evidence summary: %w", err)
		}
		if err := commitImmutableJSON(root, summaryFile, encodedSummary, nil); err != nil {
			return fmt.Errorf("commit evidence summary: %w", err)
		}
		summaryHash = hashBytes(encodedSummary)
		manifest.SummarySHA256 = summaryHash
	} else if err := ensureNoSummary(root); err != nil {
		return fmt.Errorf("verify absent evidence summary: %w", err)
	}
	encodedManifest, err := encodeJSON(manifest)
	if err != nil {
		return fmt.Errorf("encode evidence manifest: %w", err)
	}
	verifySummary := func() error {
		if summaryHash == "" {
			return ensureNoSummary(root)
		}
		return verifySummaryHash(root, summaryHash)
	}
	if err := verifySummary(); err != nil {
		return fmt.Errorf("verify evidence summary before manifest commit: %w", err)
	}
	if err := commitImmutableJSON(root, manifestFile, encodedManifest, verifySummary); err != nil {
		return fmt.Errorf("commit evidence manifest: %w", err)
	}
	if err := verifySummary(); err != nil {
		return fmt.Errorf("verify evidence summary after manifest commit: %w", err)
	}
	if err := removePendingManifest(root); err != nil {
		return fmt.Errorf("remove pending evidence manifest: %w", err)
	}
	s.manifest = cloneManifest(manifest)
	return nil
}

func (s *Store) openRoot() (*os.Root, error) {
	info, err := os.Lstat(s.path)
	if err != nil {
		return nil, fmt.Errorf("open evidence bundle: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("open evidence bundle: not a directory")
	}
	return openBoundRoot(s.path, info)
}

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
	encoded, err := encodeJSON(value)
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	file, err := root.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, jsonMode)
	if err != nil {
		return fmt.Errorf("create JSON file: %w", err)
	}
	if err := file.Chmod(jsonMode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set JSON file permissions: %w", err)
	}
	written, err := file.Write(encoded)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write JSON file: %w", err)
	}
	if written != len(encoded) {
		_ = file.Close()
		return fmt.Errorf("write JSON file: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync JSON file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close JSON file: %w", err)
	}
	return nil
}

func encodeJSON(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// commitImmutableJSON writes, fsyncs, and closes a private temporary file
// before linking it into the bundle. Link is a no-replace operation, so a
// destination can only be recovered when its descriptor-bound bytes are exact.
func commitImmutableJSON(root *os.Root, filename string, encoded []byte, beforeLink func() error) error {
	matched, err := destinationMatches(root, filename, encoded)
	if err != nil {
		return err
	}
	if matched {
		return recoverImmutableJSON(root, filename, encoded)
	}
	temporary, err := temporaryJSONName(filename)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, jsonMode)
	if err != nil {
		return fmt.Errorf("create JSON temporary file: %w", err)
	}
	temporaryExists := true
	defer func() {
		if temporaryExists {
			_ = root.Remove(temporary)
			_ = syncBundleDirectory(root)
		}
	}()
	if err := file.Chmod(jsonMode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set JSON temporary permissions: %w", err)
	}
	written, err := writeTempJSON(file, encoded)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write JSON temporary file: %w", err)
	}
	if written != len(encoded) {
		_ = file.Close()
		return fmt.Errorf("write JSON temporary file: %w", io.ErrShortWrite)
	}
	if err := syncTempJSON(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync JSON temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close JSON temporary file: %w", err)
	}
	matched, err = destinationMatches(root, filename, encoded)
	if err != nil {
		return err
	}
	if matched {
		return recoverImmutableJSON(root, filename, encoded)
	}
	beforeJSONLink(root, filename)
	if beforeLink != nil {
		if err := beforeLink(); err != nil {
			return err
		}
	}
	if err := linkTempJSON(root, temporary, filename); err != nil {
		matched, recoveryErr := destinationMatches(root, filename, encoded)
		if recoveryErr == nil && matched {
			return recoverImmutableJSON(root, filename, encoded)
		}
		if recoveryErr != nil {
			return recoveryErr
		}
		return fmt.Errorf("link JSON temporary file: %w", err)
	}
	if err := syncBundleDirectory(root); err != nil {
		return fmt.Errorf("sync bundle directory after JSON link: %w", err)
	}
	if err := root.Remove(temporary); err != nil {
		return fmt.Errorf("remove JSON temporary file: %w", err)
	}
	temporaryExists = false
	if err := syncBundleDirectory(root); err != nil {
		return fmt.Errorf("sync bundle directory after temporary cleanup: %w", err)
	}
	return nil
}

func destinationMatches(root *os.Root, filename string, encoded []byte) (bool, error) {
	info, err := root.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat JSON destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("JSON destination is not regular")
	}
	existing, err := readBoundBytes(root, filename, info)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(existing, encoded) {
		return false, errors.New("existing JSON destination differs from recovery target")
	}
	return true, nil
}

// recoverImmutableJSON makes a matching no-replace destination durable before
// the caller can continue to a later commit point.
func recoverImmutableJSON(root *os.Root, filename string, encoded []byte) error {
	info, err := root.Lstat(filename)
	if err != nil {
		return fmt.Errorf("stat recovered JSON destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("recovered JSON destination is not regular")
	}
	file, err := root.Open(filename)
	if err != nil {
		return fmt.Errorf("open recovered JSON destination: %w", err)
	}
	defer file.Close()
	bound, err := file.Stat()
	if err != nil || bound.Mode()&os.ModeSymlink != 0 || !bound.Mode().IsRegular() || !os.SameFile(info, bound) {
		return errors.New("recovered JSON destination changed before descriptor binding")
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read recovered JSON destination: %w", err)
	}
	if !bytes.Equal(contents, encoded) {
		return errors.New("recovered JSON destination differs from recovery target")
	}
	if err := syncCommittedJSON(file); err != nil {
		return fmt.Errorf("sync recovered JSON destination: %w", err)
	}
	if err := syncBundleDirectory(root); err != nil {
		return fmt.Errorf("sync bundle directory after recovered JSON destination: %w", err)
	}
	return nil
}

func selectManifest(root *os.Root) (string, bool, error) {
	if _, err := root.Lstat(manifestFile); err == nil {
		return manifestFile, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("stat final evidence manifest: %w", err)
	}
	if _, err := root.Lstat(pendingManifestFile); err != nil {
		return "", false, fmt.Errorf("stat pending evidence manifest: %w", err)
	}
	return pendingManifestFile, false, nil
}

func verifySummaryHash(root *os.Root, want string) error {
	if len(want) != sha256.Size*2 {
		return errors.New("final evidence manifest has invalid summary hash")
	}
	info, err := root.Lstat(summaryFile)
	if err != nil {
		return fmt.Errorf("stat evidence summary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("evidence summary is not regular")
	}
	contents, err := readBoundBytes(root, summaryFile, info)
	if err != nil {
		return err
	}
	if hashBytes(contents) != want {
		return errors.New("evidence summary hash does not match final manifest")
	}
	return nil
}

func ensureNoSummary(root *os.Root) error {
	if _, err := root.Lstat(summaryFile); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat evidence summary: %w", err)
	}
	return errors.New("evidence summary exists without aggregate finalization")
}

func hashBytes(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func removePendingManifest(root *os.Root) error {
	if err := root.Remove(pendingManifestFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncBundleDirectory(root)
}

func syncRootDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func temporaryJSONName(filename string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("create JSON temporary token: %w", err)
	}
	return "." + filename + "." + token + ".tmp", nil
}

func readBoundBytes(root *os.Root, filename string, expected os.FileInfo) ([]byte, error) {
	file, err := root.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open JSON file: %w", err)
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || actual.Mode()&os.ModeSymlink != 0 || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return nil, errors.New("JSON file changed before descriptor binding")
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read JSON file: %w", err)
	}
	return contents, nil
}

func makeArtifactParent(root *os.Root, dirname string) error {
	if dirname == "." {
		return nil
	}
	if err := root.MkdirAll(dirname, directoryMode); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	current := ""
	for _, component := range strings.Split(dirname, "/") {
		if component == "" || component == "." {
			continue
		}
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		info, err := root.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("artifact directory is not a directory")
		}
	}
	return nil
}

func writeBundleBytes(root *os.Root, filename string, data []byte) error {
	file, err := root.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, jsonMode)
	if err != nil {
		return fmt.Errorf("create evidence artifact: %w", err)
	}
	if err := file.Chmod(jsonMode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set evidence artifact permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write evidence artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close evidence artifact: %w", err)
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
	for _, filename := range []string{pendingManifestFile, manifestFile, protocolFile, summaryFile} {
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
	_ = root.Remove(pendingManifestFile)
	_ = root.Remove(manifestFile)
	_ = root.Remove(protocolFile)
	_ = root.Remove(summaryFile)
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
	manifest.Attempts = append([]Attempt(nil), manifest.Attempts...)
	return manifest
}
