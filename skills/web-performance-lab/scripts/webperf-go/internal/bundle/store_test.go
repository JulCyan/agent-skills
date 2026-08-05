package bundle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateDoesNotOverwrite(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	if _, err := Create(target, Manifest{}, Protocol{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target, Manifest{}, Protocol{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateRejectsExistingTargets(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "file", setup: func(t *testing.T, path string) { writeFile(t, path, []byte("existing"), 0o600) }},
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "run")
			tc.setup(t, target)
			if _, err := Create(target, Manifest{}, Protocol{}); !errors.Is(err, ErrExists) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCreatePreservesTargetClaimedDuringCreate(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	originalMkdir := mkdirDirectory
	mkdirDirectory = func(root *os.Root, name string, permission os.FileMode) error {
		if err := root.Mkdir(name, 0o700); err != nil {
			return err
		}
		return originalMkdir(root, name, permission)
	}
	t.Cleanup(func() { mkdirDirectory = originalMkdir })

	if _, err := Create(target, Manifest{}, Protocol{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err=%v", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target replaced: mode=%v", info.Mode())
	}
}

func TestCreateRejectsTargetReplacedAfterClaimIdentity(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	originalAfterClaimIdentity := afterClaimIdentity
	afterClaimIdentity = func(parent *os.Root, name string) {
		if err := os.Remove(filepath.Join(target, claimFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterClaimIdentity = originalAfterClaimIdentity })

	if _, err := Create(target, Manifest{}, Protocol{}); err == nil {
		t.Fatal("Create accepted a replaced target")
	}
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement was initialized: err=%v", err)
	}
}

func TestCreateDoesNotWriteMarkerToTargetReplacedBeforeChildBinding(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	originalAfterInitialClaimLstat := afterInitialClaimLstat
	afterInitialClaimLstat = func(parent *os.Root, name string) {
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterInitialClaimLstat = originalAfterInitialClaimLstat })

	if _, err := Create(target, Manifest{}, Protocol{}); err == nil {
		t.Fatal("Create accepted a replaced target")
	}
	for _, filename := range []string{claimFile, manifestFile, protocolFile} {
		if _, err := os.Lstat(filepath.Join(target, filename)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement contains %s: err=%v", filename, err)
		}
	}
}

func TestCleanupDoesNotRemoveReplacementAfterIdentityCheck(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	originalWriteJSON := writeBundleJSON
	writeBundleJSON = func(root *os.Root, name string, value any) error {
		return errors.New("injected write failure")
	}
	originalBeforeCleanup := beforeCleanupRemove
	beforeCleanupRemove = func(parent *os.Root, name string) {
		if err := os.Remove(filepath.Join(target, claimFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		writeBundleJSON = originalWriteJSON
		beforeCleanupRemove = originalBeforeCleanup
	})

	if _, err := Create(target, Manifest{}, Protocol{}); err == nil {
		t.Fatal("Create succeeded")
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("replacement was removed: mode=%v", info.Mode())
	}
}

func TestCreateDoesNotRecoverForeignEmptyReplacementAfterBindFailure(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	originalOpenChildRoot := openChildRoot
	failOnce := true
	openChildRoot = func(parent *os.Root, name string) (*os.Root, error) {
		if failOnce {
			failOnce = false
			return nil, errors.New("injected root open failure")
		}
		return originalOpenChildRoot(parent, name)
	}
	t.Cleanup(func() { openChildRoot = originalOpenChildRoot })

	if _, err := Create(target, Manifest{}, Protocol{}); err == nil {
		t.Fatal("first Create succeeded")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target, Manifest{}, Protocol{}); !errors.Is(err, ErrExists) {
		t.Fatalf("second Create err=%v", err)
	}
	for _, filename := range []string{claimFile, manifestFile, protocolFile} {
		if _, err := os.Lstat(filepath.Join(target, filename)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement contains %s: err=%v", filename, err)
		}
	}
}

func TestCreateWritesPrivateJSONAndOpenRejectsTrailingDocument(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	manifest := Manifest{Artifacts: []Artifact{{Path: "attempts/001/lhr.json", Kind: "lhr"}}}
	store, err := Create(target, manifest, Protocol{Profile: "desktop@v1"})
	if err != nil {
		t.Fatal(err)
	}
	if store.Path() != target {
		t.Fatalf("path=%q", store.Path())
	}
	assertMode(t, target, 0o700)
	assertMode(t, filepath.Join(target, manifestFile), 0o600)
	assertMode(t, filepath.Join(target, protocolFile), 0o600)

	opened, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Manifest().Artifacts[0].Path != "attempts/001/lhr.json" || opened.Protocol().Profile != "desktop@v1" {
		t.Fatalf("opened bundle=%+v protocol=%+v", opened.Manifest(), opened.Protocol())
	}

	writeFile(t, filepath.Join(target, manifestFile), []byte(`{} {}`), 0o600)
	if _, err := Open(target); err == nil {
		t.Fatal("Open accepted trailing JSON document")
	}
}

func TestCreateRejectsAbsoluteAndEscapingArtifactPaths(t *testing.T) {
	cases := []string{"/tmp/lhr.json", "../lhr.json", ""}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			_, err := Create(filepath.Join(t.TempDir(), "run"), Manifest{Artifacts: []Artifact{{Path: path}}}, Protocol{})
			if !errors.Is(err, ErrInvalidArtifactPath) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestOpenRejectsRootReplacedBeforeDescriptorBinding(t *testing.T) {
	target := createBundle(t)
	external := t.TempDir()
	originalOpenRoot := openRoot
	openRoot = func(path string) (*os.Root, error) {
		if err := os.Remove(filepath.Join(target, manifestFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(target, protocolFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(target, claimFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, target); err != nil {
			t.Fatal(err)
		}
		return originalOpenRoot(path)
	}
	t.Cleanup(func() { openRoot = originalOpenRoot })

	if _, err := Open(target); err == nil {
		t.Fatal("Open accepted a root replaced with a symlink")
	}
}

func TestOpenRejectsJSONReplacedWithSymlinkBeforeDescriptorBinding(t *testing.T) {
	target := createBundle(t)
	originalOpenFile := openBundleFile
	openBundleFile = func(root *os.Root, name string) (*os.File, error) {
		if name == manifestFile {
			if err := os.Remove(filepath.Join(target, manifestFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(protocolFile, filepath.Join(target, manifestFile)); err != nil {
				t.Fatal(err)
			}
		}
		return originalOpenFile(root, name)
	}
	t.Cleanup(func() { openBundleFile = originalOpenFile })

	if _, err := Open(target); err == nil {
		t.Fatal("Open accepted a JSON file replaced with a symlink")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode=%#o want=%#o", path, got, want)
	}
}

func writeFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func createBundle(t *testing.T) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "run")
	if _, err := Create(target, Manifest{}, Protocol{}); err != nil {
		t.Fatal(err)
	}
	return target
}
