package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const installMarkerSchemaVersion = 2

type integrityMarker struct {
	SchemaVersion     int    `json:"schemaVersion"`
	LighthouseVersion string `json:"lighthouseVersion"`
	PackageJSONSHA256 string `json:"packageJSONSHA256"`
	PackageLockSHA256 string `json:"packageLockSHA256"`
	PayloadSHA256     string `json:"payloadSHA256"`
}

type treeSnapshot struct {
	digest string
	root   os.FileInfo
}

type trustedFile struct {
	contents []byte
	info     os.FileInfo
}

type payloadValidation struct {
	runtime      Runtime
	digest       string
	root         os.FileInfo
	marker       os.FileInfo
	markerDigest [sha256.Size]byte
}

func validatePayload(rootPath string) (payloadValidation, error) {
	markerBefore, err := readTrustedFile(rootPath, installMarkerPath)
	if err != nil {
		return payloadValidation{}, ErrNeedsSetup
	}
	before, err := snapshotPayloadTree(rootPath)
	if err != nil {
		return payloadValidation{}, ErrNeedsSetup
	}
	manifest, err := readTrustedFile(rootPath, "package.json")
	if err != nil || !bytes.Equal(manifest.contents, npmPackageJSON) {
		return payloadValidation{}, ErrNeedsSetup
	}
	lock, err := readTrustedFile(rootPath, "package-lock.json")
	if err != nil || !bytes.Equal(lock.contents, npmPackageLock) {
		return payloadValidation{}, ErrNeedsSetup
	}
	metadataFile, err := readTrustedFile(rootPath, filepath.FromSlash("node_modules/lighthouse/package.json"))
	if err != nil {
		return payloadValidation{}, ErrNeedsSetup
	}
	var metadata packageMetadata
	if err := json.Unmarshal(metadataFile.contents, &metadata); err != nil || metadata.Version != lighthouseVersion {
		return payloadValidation{}, ErrNeedsSetup
	}
	if _, err := readTrustedFile(rootPath, filepath.FromSlash(lighthouseCLIPath)); err != nil {
		return payloadValidation{}, ErrNeedsSetup
	}
	after, err := snapshotPayloadTree(rootPath)
	if err != nil || !sameTreeSnapshot(before, after) {
		return payloadValidation{}, ErrNeedsSetup
	}
	markerAfter, err := readTrustedFile(rootPath, installMarkerPath)
	if err != nil || !sameStableEntry(markerBefore.info, markerAfter.info) || !bytes.Equal(markerBefore.contents, markerAfter.contents) {
		return payloadValidation{}, ErrNeedsSetup
	}
	marker, err := parseInstallMarker(markerAfter.contents)
	if err != nil || marker.PayloadSHA256 != after.digest {
		return payloadValidation{}, ErrNeedsSetup
	}
	markerDigest := sha256.Sum256(markerAfter.contents)
	return payloadValidation{
		runtime:      Runtime{version: lighthouseVersion, cliPath: filepath.Join(rootPath, filepath.FromSlash(lighthouseCLIPath))},
		digest:       after.digest,
		root:         after.root,
		marker:       markerAfter.info,
		markerDigest: markerDigest,
	}, nil
}

func samePayloadValidation(left, right payloadValidation) bool {
	return left.digest == right.digest && left.markerDigest == right.markerDigest &&
		sameStableEntry(left.root, right.root) && sameStableEntry(left.marker, right.marker)
}

func snapshotPayloadTree(rootPath string) (treeSnapshot, error) {
	pathInfo, err := os.Lstat(rootPath)
	if err != nil || !trustedDirectory(pathInfo) {
		return treeSnapshot{}, ErrNeedsSetup
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return treeSnapshot{}, ErrNeedsSetup
	}
	defer root.Close()
	handleInfo, err := root.Stat(".")
	if err != nil || !trustedDirectory(handleInfo) || !sameStableEntry(pathInfo, handleInfo) {
		return treeSnapshot{}, ErrNeedsSetup
	}
	hasher := sha256.New()
	if err := hashTreeEntry(root, ".", hasher); err != nil {
		return treeSnapshot{}, ErrNeedsSetup
	}
	handleAfter, handleErr := root.Stat(".")
	pathAfter, pathErr := os.Lstat(rootPath)
	if handleErr != nil || pathErr != nil || !trustedDirectory(handleAfter) || !trustedDirectory(pathAfter) ||
		!sameStableEntry(handleInfo, handleAfter) || !sameStableEntry(handleAfter, pathAfter) {
		return treeSnapshot{}, ErrNeedsSetup
	}
	return treeSnapshot{digest: hex.EncodeToString(hasher.Sum(nil)), root: handleAfter}, nil
}

func hashTreeEntry(root *os.Root, relativePath string, hasher hash.Hash) error {
	info, err := root.Lstat(relativePath)
	if err != nil || !trustedEntry(info) {
		return ErrNeedsSetup
	}
	entryType := byte('f')
	if info.IsDir() {
		entryType = 'd'
	} else if !info.Mode().IsRegular() {
		return ErrNeedsSetup
	}
	entry, err := root.Open(relativePath)
	if err != nil {
		return ErrNeedsSetup
	}
	defer entry.Close()
	handleInfo, err := entry.Stat()
	if err != nil || !sameStableEntry(info, handleInfo) {
		return ErrNeedsSetup
	}
	if (entryType == 'd') != handleInfo.IsDir() || (entryType == 'f' && !handleInfo.Mode().IsRegular()) {
		return ErrNeedsSetup
	}
	if err := hashEntryHeader(hasher, relativePath, entryType, handleInfo.Mode(), handleInfo.Size()); err != nil {
		return ErrNeedsSetup
	}
	if handleInfo.IsDir() {
		entries, err := entry.ReadDir(-1)
		if err != nil {
			return ErrNeedsSetup
		}
		sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
		for _, child := range entries {
			if relativePath == "." && child.Name() == installMarkerPath {
				continue
			}
			childPath := child.Name()
			if relativePath != "." {
				childPath = filepath.Join(relativePath, child.Name())
			}
			if err := hashTreeEntry(root, childPath, hasher); err != nil {
				return err
			}
		}
	} else {
		written, err := io.Copy(hasher, entry)
		if err != nil || written != handleInfo.Size() {
			return ErrNeedsSetup
		}
	}
	handleAfter, handleErr := entry.Stat()
	pathAfter, pathErr := root.Lstat(relativePath)
	if handleErr != nil || pathErr != nil || !trustedEntry(pathAfter) ||
		!sameStableEntry(handleInfo, handleAfter) || !sameStableEntry(handleAfter, pathAfter) {
		return ErrNeedsSetup
	}
	return nil
}

func hashEntryHeader(hasher hash.Hash, relativePath string, entryType byte, mode os.FileMode, size int64) error {
	pathBytes := []byte(filepath.ToSlash(relativePath))
	if err := binary.Write(hasher, binary.BigEndian, uint64(len(pathBytes))); err != nil {
		return err
	}
	if _, err := hasher.Write(pathBytes); err != nil {
		return err
	}
	if _, err := hasher.Write([]byte{entryType}); err != nil {
		return err
	}
	if err := binary.Write(hasher, binary.BigEndian, uint32(mode)); err != nil {
		return err
	}
	if entryType == 'f' {
		return binary.Write(hasher, binary.BigEndian, size)
	}
	return nil
}

func sameTreeSnapshot(left, right treeSnapshot) bool {
	return left.digest == right.digest && sameStableEntry(left.root, right.root)
}

func readTrustedFile(rootPath, relativePath string) (trustedFile, error) {
	pathRootInfo, err := os.Lstat(rootPath)
	if err != nil || !trustedDirectory(pathRootInfo) {
		return trustedFile{}, ErrNeedsSetup
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return trustedFile{}, ErrNeedsSetup
	}
	defer root.Close()
	handleRootInfo, err := root.Stat(".")
	if err != nil || !sameStableEntry(pathRootInfo, handleRootInfo) {
		return trustedFile{}, ErrNeedsSetup
	}
	pathInfo, err := root.Lstat(relativePath)
	if err != nil || !trustedRegularFile(pathInfo) {
		return trustedFile{}, ErrNeedsSetup
	}
	file, err := root.Open(relativePath)
	if err != nil {
		return trustedFile{}, ErrNeedsSetup
	}
	defer file.Close()
	handleInfo, err := file.Stat()
	if err != nil || !trustedRegularFile(handleInfo) || !sameStableEntry(pathInfo, handleInfo) {
		return trustedFile{}, ErrNeedsSetup
	}
	contents, err := io.ReadAll(file)
	if err != nil || int64(len(contents)) != handleInfo.Size() {
		return trustedFile{}, ErrNeedsSetup
	}
	handleAfter, handleErr := file.Stat()
	pathAfter, pathErr := root.Lstat(relativePath)
	rootAfter, rootHandleErr := root.Stat(".")
	pathRootAfter, rootPathErr := os.Lstat(rootPath)
	if handleErr != nil || pathErr != nil || rootHandleErr != nil || rootPathErr != nil ||
		!trustedRegularFile(pathAfter) || !sameStableEntry(handleInfo, handleAfter) || !sameStableEntry(handleAfter, pathAfter) ||
		!trustedDirectory(rootAfter) || !trustedDirectory(pathRootAfter) || !sameStableEntry(handleRootInfo, rootAfter) ||
		!sameStableEntry(rootAfter, pathRootAfter) {
		return trustedFile{}, ErrNeedsSetup
	}
	return trustedFile{contents: contents, info: handleAfter}, nil
}

func parseInstallMarker(contents []byte) (integrityMarker, error) {
	var marker integrityMarker
	if err := decodeSingleJSON(contents, &marker); err != nil {
		return integrityMarker{}, err
	}
	manifestHash := sha256.Sum256(npmPackageJSON)
	lockHash := sha256.Sum256(npmPackageLock)
	if marker.SchemaVersion != installMarkerSchemaVersion || marker.LighthouseVersion != lighthouseVersion ||
		marker.PackageJSONSHA256 != hex.EncodeToString(manifestHash[:]) ||
		marker.PackageLockSHA256 != hex.EncodeToString(lockHash[:]) ||
		len(marker.PayloadSHA256) != sha256.Size*2 {
		return integrityMarker{}, ErrNeedsSetup
	}
	if _, err := hex.DecodeString(marker.PayloadSHA256); err != nil {
		return integrityMarker{}, ErrNeedsSetup
	}
	return marker, nil
}

func installMarker(payloadDigest string) []byte {
	manifestHash := sha256.Sum256(npmPackageJSON)
	lockHash := sha256.Sum256(npmPackageLock)
	marker := integrityMarker{
		SchemaVersion:     installMarkerSchemaVersion,
		LighthouseVersion: lighthouseVersion,
		PackageJSONSHA256: hex.EncodeToString(manifestHash[:]),
		PackageLockSHA256: hex.EncodeToString(lockHash[:]),
		PayloadSHA256:     payloadDigest,
	}
	contents, _ := json.Marshal(marker)
	return append(contents, '\n')
}

func writeInstallMarker(rootPath, payloadDigest string) error {
	contents := installMarker(payloadDigest)
	temporary, err := os.CreateTemp(rootPath, ".webperf-engine-marker-")
	if err != nil {
		return ErrCache
	}
	temporaryPath := temporary.Name()
	var temporaryInfo os.FileInfo
	cleanup := func() {
		if temporaryInfo != nil && sameEntry(temporaryPath, temporaryInfo) {
			_ = os.Remove(temporaryPath)
		}
	}
	defer cleanup()
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return ErrCache
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrCache
	}
	temporaryInfo, err = temporary.Stat()
	if closeErr := temporary.Close(); err != nil || closeErr != nil || !trustedRegularFile(temporaryInfo) {
		return ErrCache
	}
	markerPath := filepath.Join(rootPath, installMarkerPath)
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		return ErrCache
	}
	if err := os.Link(temporaryPath, markerPath); err != nil {
		return ErrCache
	}
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !sameStableEntry(temporaryInfo, markerInfo) || !trustedRegularFile(markerInfo) {
		return ErrCache
	}
	cleanup()
	temporaryInfo = nil
	if err := syncDirectory(rootPath); err != nil {
		return ErrCache
	}
	published, err := readTrustedFile(rootPath, installMarkerPath)
	if err != nil || !bytes.Equal(published.contents, contents) {
		return ErrCache
	}
	return nil
}

func decodeSingleJSON(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrNeedsSetup
	}
	return nil
}
