package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store struct {
	root *os.Root
	path string
}

type ApplyLease struct {
	store    *Store
	retained bool
	finished bool
}

func NewRunRoot(
	callerCWD string,
	now time.Time,
	random io.Reader,
) (string, string, error) {
	if strings.TrimSpace(callerCWD) == "" {
		return "", "", errors.New("caller working directory is required")
	}
	caller, err := filepath.Abs(callerCWD)
	if err != nil {
		return "", "", fmt.Errorf("resolving caller working directory: %w", err)
	}
	info, err := os.Stat(caller)
	if err != nil {
		return "", "", fmt.Errorf("stating caller working directory: %w", err)
	}
	if !info.IsDir() {
		return "", "", errors.New("caller working directory is not a directory")
	}

	suffix := make([]byte, 4)
	if _, err := io.ReadFull(random, suffix); err != nil {
		return "", "", fmt.Errorf("generating run ID: %w", err)
	}
	runID := now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix)
	root := filepath.Join(caller, ".runtime", "theme-template-sync", runID)
	return root, runID, nil
}

func Open(rootPath string) (*Store, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, errors.New("evidence root is required")
	}
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolving evidence root: %w", err)
	}
	if info, err := os.Lstat(absolute); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("evidence root must not be a symlink")
		}
		if !info.IsDir() {
			return nil, errors.New("evidence root is not a directory")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("existing evidence root permissions must be owner-only")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("checking evidence root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("creating evidence root: %w", err)
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("opening evidence root: %w", err)
	}
	return &Store{root: root, path: absolute}, nil
}

func OpenUnder(basePath string, relative string) (*Store, error) {
	return openUnder(basePath, relative, true)
}

func OpenExistingUnder(basePath string, relative string) (*Store, error) {
	return openUnder(basePath, relative, false)
}

func openUnder(basePath string, relative string, create bool) (*Store, error) {
	if err := validateRelativePath(relative); err != nil {
		return nil, err
	}
	base, err := filepath.Abs(basePath)
	if err != nil {
		return nil, fmt.Errorf("resolving evidence base: %w", err)
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		return nil, fmt.Errorf("opening evidence base: %w", err)
	}
	closeBase := func(operationErr error) error {
		if closeErr := baseRoot.Close(); closeErr != nil {
			return errors.Join(operationErr, fmt.Errorf("closing evidence base: %w", closeErr))
		}
		return operationErr
	}

	current := ""
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, statErr := baseRoot.Lstat(current)
		switch {
		case statErr == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, closeBase(fmt.Errorf("evidence path component %q must not be a symlink", current))
			}
			if !info.IsDir() {
				return nil, closeBase(fmt.Errorf("evidence path component %q is not a directory", current))
			}
			if current == relative && info.Mode().Perm()&0o077 != 0 {
				return nil, closeBase(errors.New("existing evidence root permissions must be owner-only"))
			}
		case errors.Is(statErr, os.ErrNotExist):
			if !create {
				return nil, closeBase(fmt.Errorf("evidence path component %q does not exist", current))
			}
			if err := baseRoot.Mkdir(current, 0o700); err != nil {
				return nil, closeBase(fmt.Errorf("creating evidence path component %q: %w", current, err))
			}
		default:
			return nil, closeBase(fmt.Errorf("checking evidence path component %q: %w", current, statErr))
		}
	}

	root, err := baseRoot.OpenRoot(relative)
	if err != nil {
		return nil, closeBase(fmt.Errorf("opening scoped evidence root: %w", err))
	}
	if err := closeBase(nil); err != nil {
		closeErr := root.Close()
		return nil, errors.Join(err, closeErr)
	}
	return &Store{
		root: root,
		path: filepath.Join(base, relative),
	}, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) RequireEmpty() error {
	directory, err := s.root.Open(".")
	if err != nil {
		return fmt.Errorf("opening evidence root for inspection: %w", err)
	}
	entries, readErr := directory.ReadDir(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(fmt.Errorf("inspecting evidence root: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing evidence root inspection: %w", closeErr)
	}
	if len(entries) != 0 {
		return errors.New("evidence root is not empty")
	}
	return nil
}

func (s *Store) Reserve() error {
	file, err := s.root.OpenFile(
		".run-reservation",
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("reserving evidence root: %w", err)
	}
	if err := file.Sync(); err != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("syncing evidence reservation: %w", err), closeErr)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing evidence reservation: %w", err)
	}
	return nil
}

func (s *Store) AcquireApplyLease() (*ApplyLease, error) {
	file, err := s.root.OpenFile(
		".apply-lease",
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("acquiring apply lease: %w", err)
	}
	if err := file.Sync(); err != nil {
		closeErr := file.Close()
		return nil, errors.Join(fmt.Errorf("syncing apply lease: %w", err), closeErr)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("closing apply lease: %w", err)
	}
	return &ApplyLease{store: s}, nil
}

func (l *ApplyLease) Retain() {
	if !l.finished {
		l.retained = true
	}
}

func (l *ApplyLease) Finish() error {
	if l.finished {
		return nil
	}
	if l.retained {
		l.finished = true
		return nil
	}
	if err := l.store.root.Remove(".apply-lease"); err != nil {
		return fmt.Errorf("releasing apply lease: %w", err)
	}
	l.finished = true
	return nil
}

func (s *Store) WriteJSON(relative string, value any) error {
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encoding evidence JSON: %w", err)
	}
	return s.WriteBytes(relative, []byte(output.String()))
}

func (s *Store) WriteBytes(relative string, data []byte) (returnErr error) {
	if err := validateRelativePath(relative); err != nil {
		return err
	}
	parent := filepath.Dir(relative)
	if parent != "." {
		if err := s.root.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("creating evidence directory: %w", err)
		}
	}
	if info, err := s.root.Lstat(relative); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("evidence destination must not be a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking evidence destination: %w", err)
	}

	temporary, err := temporaryName(parent, filepath.Base(relative))
	if err != nil {
		return err
	}
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating temporary evidence file: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			if err := s.root.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("removing temporary evidence file: %w", err),
				)
			}
		}
	}()

	if _, err := file.Write(data); err != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("writing evidence file: %w", err), closeErr)
	}
	if err := file.Sync(); err != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("syncing evidence file: %w", err), closeErr)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing evidence file: %w", err)
	}
	if err := s.root.Rename(temporary, relative); err != nil {
		return fmt.Errorf("committing evidence file: %w", err)
	}
	renamed = true
	return nil
}

func (s *Store) ReadJSON(relative string, value any) error {
	data, err := s.ReadBytes(relative)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("decoding evidence JSON: %w", err)
	}
	return nil
}

func (s *Store) ReadBytes(relative string) ([]byte, error) {
	if err := validateRelativePath(relative); err != nil {
		return nil, err
	}
	data, err := s.root.ReadFile(relative)
	if err != nil {
		return nil, fmt.Errorf("reading evidence file: %w", err)
	}
	return data, nil
}

func (s *Store) Close() error {
	if err := s.root.Close(); err != nil {
		return fmt.Errorf("closing evidence root: %w", err)
	}
	return nil
}

func validateRelativePath(relative string) error {
	containsBackslash := strings.Contains(relative, `\`)
	invalidLocalPath := relative == "" || relative == "." || !filepath.IsLocal(relative)
	notCanonical := filepath.Clean(relative) != relative
	if containsBackslash || invalidLocalPath || notCanonical {
		return fmt.Errorf("invalid evidence relative path %q", relative)
	}
	return nil
}

func temporaryName(parent string, destination string) (string, error) {
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generating temporary evidence name: %w", err)
	}
	name := "." + destination + ".tmp-" + hex.EncodeToString(randomBytes)
	if parent == "." {
		return name, nil
	}
	return filepath.Join(parent, name), nil
}
