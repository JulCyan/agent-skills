package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
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
	if _, err := os.Lstat(filepath.Join(target, pendingManifestFile)); !errors.Is(err, os.ErrNotExist) {
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
	for _, filename := range []string{claimFile, pendingManifestFile, manifestFile, protocolFile} {
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
	for _, filename := range []string{claimFile, pendingManifestFile, manifestFile, protocolFile} {
		if _, err := os.Lstat(filepath.Join(target, filename)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement contains %s: err=%v", filename, err)
		}
	}
}

func TestCreateWritesPendingManifestAndOpenReadsIt(t *testing.T) {
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
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Create wrote final manifest: %v", err)
	}
	assertMode(t, filepath.Join(target, pendingManifestFile), 0o600)
	assertMode(t, filepath.Join(target, protocolFile), 0o600)

	opened, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Manifest().Artifacts[0].Path != "attempts/001/lhr.json" || opened.Protocol().Profile != "desktop@v1" {
		t.Fatalf("opened bundle=%+v protocol=%+v", opened.Manifest(), opened.Protocol())
	}

	writeFile(t, filepath.Join(target, pendingManifestFile), []byte(`{} {}`), 0o600)
	if _, err := Open(target); err == nil {
		t.Fatal("Open accepted trailing JSON document")
	}
}

func TestStoreWritesPrivateArtifactsAndFinalizesManifest(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{
		SchemaVersion:     1,
		Profile:           "desktop-lab-v1",
		LighthouseVersion: "13.4.1",
		NodeVersion:       "22.19.0",
		ChromeVersion:     "150.0.0.0",
		OS:                "darwin",
		Arch:              "arm64",
		ResolvedFlags:     []string{"--preset=desktop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteArtifact("samples/run-1.lhr.json", []byte(`{"fixture":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(
		Manifest{Status: "OK", Attempts: []Attempt{{Number: 1, Status: "OK", Artifact: "samples/run-1.lhr.json"}}},
		&Summary{Status: "OK", Metrics: Metrics{LCP: stats.Distribution{Count: 1, Median: 1500}}},
	); err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(target, "samples", "run-1.lhr.json"), 0o600)
	assertMode(t, filepath.Join(target, "summary.json"), 0o600)
	opened, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Manifest().Status != "OK" || len(opened.Manifest().Attempts) != 1 {
		t.Fatalf("manifest=%+v", opened.Manifest())
	}
	if opened.Protocol().Fingerprint == "" {
		t.Fatal("protocol fingerprint was not persisted")
	}
}

func TestOpenReadsHashVerifiedSummaryAndClonesMutableSlices(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, completeProtocol())
	if err != nil {
		t.Fatal(err)
	}
	summary := Summary{
		SchemaVersion:  1,
		Status:         "OK",
		Profile:        "desktop-lab-v1",
		RequestedRuns:  3,
		SuccessfulRuns: 3,
		Samples:        []SuccessfulSample{{Attempt: 1}, {Attempt: 2}, {Attempt: 3}},
		Warnings:       []string{"synthetic warning"},
	}
	if err := store.Finalize(Manifest{Status: "OK"}, &summary); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := opened.Summary()
	if err != nil || got == nil || got.Status != "OK" {
		t.Fatalf("summary=%+v err=%v", got, err)
	}
	got.Warnings[0] = "mutated"
	again, err := opened.Summary()
	if err != nil || again.Warnings[0] != "synthetic warning" {
		t.Fatalf("summary=%+v err=%v", again, err)
	}
	protocol := opened.Protocol()
	protocol.ResolvedFlags[0] = "mutated"
	if opened.Protocol().ResolvedFlags[0] == "mutated" {
		t.Fatal("Protocol returned an aliased flag slice")
	}
}

func TestValidateProtocolRejectsMissingOrStaleFingerprint(t *testing.T) {
	if err := ValidateProtocol(Protocol{}); err == nil {
		t.Fatal("ValidateProtocol accepted missing fields")
	}
	broken := completeProtocol()
	broken.Fingerprint = "stale"
	if err := ValidateProtocol(broken); err == nil {
		t.Fatal("ValidateProtocol accepted a stale fingerprint")
	}
}

func TestSummaryDoesNotReturnReplacementAfterHashValidation(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, completeProtocol())
	if err != nil {
		t.Fatal(err)
	}
	summary := Summary{SchemaVersion: 1, Status: "OK", Profile: "desktop-lab-v1", RequestedRuns: 3, Samples: []SuccessfulSample{{Attempt: 1}, {Attempt: 2}, {Attempt: 3}}}
	if err := store.Finalize(Manifest{Status: "OK"}, &summary); err != nil {
		t.Fatal(err)
	}
	originalHook := afterSummaryHashVerified
	afterSummaryHashVerified = func(_ *os.Root) {
		if err := os.Remove(filepath.Join(target, summaryFile)); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(target, summaryFile), []byte(`{"status":"replacement"}`), 0o600)
	}
	t.Cleanup(func() { afterSummaryHashVerified = originalHook })

	opened, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := opened.Summary()
	if err != nil || got == nil || got.Status != "OK" {
		t.Fatalf("summary=%+v err=%v", got, err)
	}
}

func TestFinalizeDoesNotReplaceForeignManifestInsertedAfterDestinationCheck(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalHook := beforeJSONLink
	beforeJSONLink = func(_ *os.Root, destination string) {
		if destination != manifestFile {
			return
		}
		path := filepath.Join(target, manifestFile)
		writeFile(t, path, []byte("foreign manifest"), 0o600)
	}
	t.Cleanup(func() { beforeJSONLink = originalHook })

	if err := store.Finalize(Manifest{Status: "OK"}, nil); err == nil {
		t.Fatal("Finalize accepted a replaced manifest")
	}
	contents, readErr := os.ReadFile(filepath.Join(target, manifestFile))
	if readErr != nil || string(contents) != "foreign manifest" {
		t.Fatalf("foreign manifest was modified: contents=%q err=%v", contents, readErr)
	}
}

func TestFinalizeFailuresKeepPendingManifestComplete(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalWrite := writeTempJSON
	writeTempJSON = func(*os.File, []byte) (int, error) {
		return 0, errors.New("injected temp write failure")
	}
	t.Cleanup(func() { writeTempJSON = originalWrite })

	err = store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"})
	if err == nil {
		t.Fatal("Finalize succeeded")
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists after failed finalize: %v", err)
	}
}

func TestFinalizeRejectsShortTempWriteWithoutPublishingSummary(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalWrite := writeTempJSON
	writeTempJSON = func(_ *os.File, data []byte) (int, error) { return len(data) - 1, nil }
	t.Cleanup(func() { writeTempJSON = originalWrite })

	if err := store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"}); err == nil {
		t.Fatal("Finalize accepted a short temporary write")
	}
	if _, err := os.Lstat(filepath.Join(target, summaryFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("summary exists after short write: %v", err)
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
}

func TestDirectorySyncFailurePreventsLaterManifestCommit(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalSync := syncBundleDirectory
	syncBundleDirectory = func(*os.Root) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { syncBundleDirectory = originalSync })

	err = store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"})
	if err == nil {
		t.Fatal("Finalize succeeded")
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
	assertCompleteJSON(t, filepath.Join(target, summaryFile))
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists despite summary directory sync failure: %v", err)
	}
}

func TestFinalizeRetryDurablyRecoversSummaryBeforeManifestLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalDirectorySync := syncBundleDirectory
	originalCommittedSync := syncCommittedJSON
	originalLink := linkTempJSON
	firstDirectorySync := true
	recoveredFileSynced := false
	recoveredDirectorySynced := false
	syncCommittedJSON = func(file *os.File) error {
		recoveredFileSynced = true
		return originalCommittedSync(file)
	}
	syncBundleDirectory = func(root *os.Root) error {
		if firstDirectorySync {
			firstDirectorySync = false
			return errors.New("injected summary directory sync failure")
		}
		if recoveredFileSynced {
			recoveredDirectorySynced = true
		}
		return originalDirectorySync(root)
	}
	linkTempJSON = func(root *os.Root, oldName, newName string) error {
		if newName == manifestFile && (!recoveredFileSynced || !recoveredDirectorySynced) {
			return errors.New("manifest link ran before summary recovery durability barrier")
		}
		return originalLink(root, oldName, newName)
	}
	t.Cleanup(func() {
		syncBundleDirectory = originalDirectorySync
		syncCommittedJSON = originalCommittedSync
		linkTempJSON = originalLink
	})

	manifest := Manifest{Status: "OK"}
	summary := &Summary{Status: "OK"}
	if err := store.Finalize(manifest, summary); err == nil {
		t.Fatal("first Finalize succeeded despite summary directory sync failure")
	}
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists after failed summary durability sync: %v", err)
	}
	if err := store.Finalize(manifest, summary); err != nil {
		t.Fatalf("retry did not durably recover summary before manifest link: %v", err)
	}
	if !recoveredFileSynced || !recoveredDirectorySynced {
		t.Fatalf("summary recovery durability not observed: file=%t directory=%t", recoveredFileSynced, recoveredDirectorySynced)
	}
}

func TestFinalizeRecoveryBarrierFailurePreventsManifestLink(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(t *testing.T, enable *bool)
	}{
		{
			name: "file sync",
			inject: func(t *testing.T, enable *bool) {
				original := syncCommittedJSON
				syncCommittedJSON = func(file *os.File) error {
					if *enable {
						return errors.New("injected recovered summary file sync failure")
					}
					return original(file)
				}
				t.Cleanup(func() { syncCommittedJSON = original })
			},
		},
		{
			name: "directory sync",
			inject: func(t *testing.T, enable *bool) {
				original := syncBundleDirectory
				syncBundleDirectory = func(root *os.Root) error {
					if *enable {
						return errors.New("injected recovered summary directory sync failure")
					}
					return original(root)
				}
				t.Cleanup(func() { syncBundleDirectory = original })
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "run")
			store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
			if err != nil {
				t.Fatal(err)
			}
			originalLink := linkTempJSON
			failManifestOnce := true
			linkTempJSON = func(root *os.Root, oldName, newName string) error {
				if newName == manifestFile && failManifestOnce {
					failManifestOnce = false
					return errors.New("injected initial manifest link failure")
				}
				return originalLink(root, oldName, newName)
			}
			t.Cleanup(func() { linkTempJSON = originalLink })

			manifest := Manifest{Status: "OK"}
			summary := &Summary{Status: "OK"}
			if err := store.Finalize(manifest, summary); err == nil {
				t.Fatal("first Finalize succeeded")
			}
			enableFailure := false
			tc.inject(t, &enableFailure)
			enableFailure = true
			if err := store.Finalize(manifest, summary); err == nil {
				t.Fatal("Finalize continued after recovery durability failure")
			}
			if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final manifest exists after recovery durability failure: %v", err)
			}
		})
	}
}

func TestFinalizeDoesNotReplaceForeignSummaryInsertedAfterDestinationCheck(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalHook := beforeJSONLink
	beforeJSONLink = func(_ *os.Root, destination string) {
		if destination == summaryFile {
			writeFile(t, filepath.Join(target, summaryFile), []byte(`{"status":"foreign"}`), 0o600)
		}
	}
	t.Cleanup(func() { beforeJSONLink = originalHook })

	err = store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"})
	if err == nil {
		t.Fatal("Finalize succeeded")
	}
	contents, readErr := os.ReadFile(filepath.Join(target, summaryFile))
	if readErr != nil || string(contents) != `{"status":"foreign"}` {
		t.Fatalf("foreign summary was modified: contents=%q err=%v", contents, readErr)
	}
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists after foreign summary: %v", err)
	}
}

func TestFinalizeRecoversMatchingSummaryAfterManifestLinkFailure(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalLink := linkTempJSON
	failManifestOnce := true
	linkTempJSON = func(root *os.Root, oldName, newName string) error {
		if newName == manifestFile && failManifestOnce {
			failManifestOnce = false
			return errors.New("injected manifest link failure")
		}
		return originalLink(root, oldName, newName)
	}
	t.Cleanup(func() { linkTempJSON = originalLink })
	manifest := Manifest{Status: "OK"}
	summary := &Summary{Status: "OK"}

	if err := store.Finalize(manifest, summary); err == nil {
		t.Fatal("first Finalize succeeded")
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
	assertCompleteJSON(t, filepath.Join(target, summaryFile))
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists after injected failure: %v", err)
	}
	if err := store.Finalize(manifest, summary); err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	assertCompleteJSON(t, filepath.Join(target, manifestFile))
	assertCompleteJSON(t, filepath.Join(target, summaryFile))
	opened, err := Open(target)
	if err != nil || opened.Manifest().Status != "OK" {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
}

func TestOpenRejectsSummaryHashMismatch(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, summaryFile), []byte(`{"status":"replaced"}`), 0o600)
	if _, err := Open(target); err == nil {
		t.Fatal("Open accepted a summary that no longer matches the final manifest hash")
	}
}

func TestFinalizeFailsClosedWhenSummaryChangesBeforeManifestCommit(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalHook := beforeJSONLink
	beforeJSONLink = func(_ *os.Root, destination string) {
		if destination == manifestFile {
			writeFile(t, filepath.Join(target, summaryFile), []byte(`{"status":"replaced"}`), 0o600)
		}
	}
	t.Cleanup(func() { beforeJSONLink = originalHook })

	if err := store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"}); err == nil {
		t.Fatal("Finalize accepted a summary replaced between commit points")
	}
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists after summary replacement: %v", err)
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
}

func TestFinalManifestRecordsSummaryHashOnlyWithAggregate(t *testing.T) {
	target := filepath.Join(t.TempDir(), "with-summary")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"}); err != nil {
		t.Fatal(err)
	}
	var finalized Manifest
	contents, err := os.ReadFile(filepath.Join(target, manifestFile))
	if err != nil || json.Unmarshal(contents, &finalized) != nil {
		t.Fatalf("read final manifest err=%v contents=%q", err, contents)
	}
	if len(finalized.SummarySHA256) != 64 {
		t.Fatalf("summary hash=%q", finalized.SummarySHA256)
	}

	target = filepath.Join(t.TempDir(), "without-summary")
	store, err = Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(Manifest{Status: "PARTIAL"}, nil); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(filepath.Join(target, manifestFile))
	finalized = Manifest{}
	if err != nil || json.Unmarshal(contents, &finalized) != nil {
		t.Fatalf("read incomplete final manifest err=%v contents=%q", err, contents)
	}
	if finalized.SummarySHA256 != "" {
		t.Fatalf("incomplete final manifest carries summary hash %q", finalized.SummarySHA256)
	}
}

func TestFinalizeSetsSuccessfulRunsFromSamples(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	summary := &Summary{
		Status:         "OK",
		SuccessfulRuns: 99,
		Samples: []SuccessfulSample{
			{Attempt: 1, Sample: lhr.Sample{FinalURL: "https://example.test"}},
			{Attempt: 2, Sample: lhr.Sample{FinalURL: "https://example.test"}},
		},
	}
	if err := store.Finalize(Manifest{Status: "OK"}, summary); err != nil {
		t.Fatal(err)
	}
	var persisted Summary
	contents, err := os.ReadFile(filepath.Join(target, summaryFile))
	if err != nil || json.Unmarshal(contents, &persisted) != nil {
		t.Fatalf("read summary err=%v contents=%q", err, contents)
	}
	if persisted.SuccessfulRuns != len(persisted.Samples) {
		t.Fatalf("successfulRuns=%d samples=%d", persisted.SuccessfulRuns, len(persisted.Samples))
	}
}

func TestFinalizeRejectsDifferingExistingSummary(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteArtifact(summaryFile, []byte(`{"status":"foreign"}`)); err != nil {
		t.Fatal(err)
	}

	if err := store.Finalize(Manifest{Status: "OK"}, &Summary{Status: "OK"}); err == nil {
		t.Fatal("Finalize accepted a differing summary")
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
	contents, readErr := os.ReadFile(filepath.Join(target, summaryFile))
	if readErr != nil || string(contents) != `{"status":"foreign"}` {
		t.Fatalf("foreign summary changed: %q err=%v", contents, readErr)
	}
}

func TestReadArtifactReturnsOnlyHashVerifiedLedgerEntries(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "run")
	store, err := Create(directory, Manifest{SchemaVersion: 1, Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := "samples/run-1.lhr.json"
	payload := []byte(`{"verified":true}`)
	digest, err := store.WriteArtifact(artifactPath, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(Manifest{
		SchemaVersion: 1,
		Status:        "PARTIAL",
		Attempts: []Attempt{{
			Number:   1,
			Status:   "PARSE_FAILED",
			Artifact: artifactPath,
			Error:    "Lighthouse report parse failed",
		}},
		Artifacts: []Artifact{{Kind: "lhr", Path: artifactPath, SHA256: digest}},
	}, nil); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := reopened.ReadArtifact(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, payload) {
		t.Fatalf("contents=%q", contents)
	}
	if _, err := reopened.ReadArtifact("manifest.json"); !errors.Is(err, ErrInvalidArtifactPath) {
		t.Fatalf("non-ledger read error=%v", err)
	}

	writeFile(t, filepath.Join(directory, artifactPath), []byte(`{"verified":false}`), 0o600)
	if _, err := reopened.ReadArtifact(artifactPath); err == nil {
		t.Fatal("hash-mismatched artifact was accepted")
	}
}

func TestFinalizeWithoutAggregateRejectsExistingSummary(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteArtifact(summaryFile, []byte(`{"status":"unanchored"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(Manifest{Status: "PARTIAL"}, nil); err == nil {
		t.Fatal("Finalize created an unanchored final manifest despite an existing summary")
	}
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists: %v", err)
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
}

func TestFinalizeWithoutAggregateFailsWhenSummaryAppearsBeforeManifestLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	originalHook := beforeJSONLink
	beforeJSONLink = func(_ *os.Root, destination string) {
		if destination == manifestFile {
			writeFile(t, filepath.Join(target, summaryFile), []byte(`{"status":"appeared"}`), 0o600)
		}
	}
	t.Cleanup(func() { beforeJSONLink = originalHook })

	if err := store.Finalize(Manifest{Status: "PARTIAL"}, nil); err == nil {
		t.Fatal("Finalize committed manifest after an unanchored summary appeared")
	}
	if _, err := os.Lstat(filepath.Join(target, manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final manifest exists after summary insertion: %v", err)
	}
	assertCompleteJSON(t, filepath.Join(target, pendingManifestFile))
}

func TestOpenRejectsFinalManifestWithoutSummaryHashWhenSummaryExists(t *testing.T) {
	target := filepath.Join(t.TempDir(), "run")
	store, err := Create(target, Manifest{Status: "RUNNING"}, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteArtifact(manifestFile, []byte(`{"status":"PARTIAL"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteArtifact(summaryFile, []byte(`{"status":"unanchored"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(target); err == nil {
		t.Fatal("Open accepted a final manifest without summary hash alongside a summary")
	}
}

func TestCreateRejectsAbsoluteAndEscapingArtifactPaths(t *testing.T) {
	cases := []string{"/tmp/lhr.json", "../lhr.json", "", "C:/evidence/lhr.json", "C:\\evidence\\lhr.json", "//server/share/lhr.json", "samples/run:1.lhr.json"}
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
		if err := os.Remove(filepath.Join(target, pendingManifestFile)); err != nil {
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
		if name == pendingManifestFile {
			if err := os.Remove(filepath.Join(target, pendingManifestFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(protocolFile, filepath.Join(target, pendingManifestFile)); err != nil {
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

func assertCompleteJSON(t *testing.T, path string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatalf("%s is not complete JSON: %v; contents=%q", filepath.Base(path), err, contents)
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

func completeProtocol() Protocol {
	return CompleteProtocol(Protocol{
		SchemaVersion:     1,
		Profile:           "desktop-lab-v1",
		FormFactor:        "desktop",
		ThrottlingMethod:  "simulate",
		ResolvedFlags:     []string{"--preset=desktop", "--throttling-method=simulate"},
		RuntimeFlags:      ExpectedRuntimeFlags(),
		LighthouseVersion: "13.4.1",
		NodeVersion:       "24.16.0",
		ChromeVersion:     "150.0.0.0",
		OS:                "darwin",
		Arch:              "arm64",
	})
}
