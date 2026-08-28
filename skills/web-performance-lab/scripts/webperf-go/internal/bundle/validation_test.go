package bundle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

func TestValidateEvidenceAcceptsCompleteCollectionLedger(t *testing.T) {
	manifest, protocol, summary := validEvidence()
	if err := ValidateEvidence(manifest, protocol, &summary); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEvidenceRejectsInconsistentOrForgedAggregate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest, *Protocol, **Summary)
	}{
		{name: "OK manifest without aggregate", mutate: func(_ *Manifest, _ *Protocol, summary **Summary) { *summary = nil }},
		{name: "manifest partial summary ok", mutate: func(manifest *Manifest, _ *Protocol, _ **Summary) { manifest.Status = "PARTIAL" }},
		{name: "missing attempt", mutate: func(manifest *Manifest, _ *Protocol, _ **Summary) { manifest.Attempts = manifest.Attempts[:2] }},
		{name: "missing artifact ledger", mutate: func(manifest *Manifest, _ *Protocol, _ **Summary) { manifest.Artifacts = manifest.Artifacts[:2] }},
		{name: "parse failure lacks artifact", mutate: func(manifest *Manifest, _ *Protocol, _ **Summary) {
			manifest.Attempts[0] = Attempt{Number: 1, Status: "PARSE_FAILED", Error: "Lighthouse report parse failed"}
		}},
		{name: "summary URL does not match manifest", mutate: func(_ *Manifest, _ *Protocol, summary **Summary) { (*summary).FinalURL = "https://example.test/other" }},
		{name: "requested URL is not safe display", mutate: func(manifest *Manifest, _ *Protocol, summary **Summary) {
			manifest.RequestedURL = "https://example.test/?token=secret"
			(*summary).RequestedURL = manifest.RequestedURL
		}},
		{name: "forged distribution", mutate: func(_ *Manifest, _ *Protocol, summary **Summary) { (*summary).Metrics.LCP.Median = 1 }},
		{name: "forged benchmark distribution", mutate: func(_ *Manifest, _ *Protocol, summary **Summary) { (*summary).Metrics.BenchmarkIndex.Median = 2 }},
		{name: "unknown self fingerprinted profile", mutate: func(_ *Manifest, protocol *Protocol, _ **Summary) {
			protocol.Profile = "unknown-profile-v1"
			protocol.Fingerprint = ProtocolFingerprint(*protocol)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest, protocol, summary := validEvidence()
			summaryPointer := &summary
			tt.mutate(&manifest, &protocol, &summaryPointer)
			if err := ValidateEvidence(manifest, protocol, summaryPointer); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestValidateEvidenceDerivesAggregateStatusAndRequiredWarningsFromAttempts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest, *Summary)
		valid  bool
	}{
		{
			name: "OK cannot contain cleanup partial attempt",
			mutate: func(manifest *Manifest, _ *Summary) {
				manifest.Attempts[0].Status = "PARTIAL"
				manifest.Attempts[0].Error = "temporary browser cleanup failed"
				manifest.Attempts[0].CleanupFailed = true
			},
		},
		{
			name: "PARTIAL requires non OK attempt",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
			},
		},
		{
			name: "cleanup partial requires cleanup warning",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
				manifest.Attempts[0].Status = "PARTIAL"
				manifest.Attempts[0].Error = "temporary browser cleanup failed"
				manifest.Attempts[0].CleanupFailed = true
			},
		},
		{
			name: "engine failed attempt requires failure warning",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
				manifest.Attempts = append(manifest.Attempts, Attempt{Number: 4, Status: "ENGINE_FAILED", Error: "engine failed"})
				summary.RequestedRuns = 4
			},
		},
		{
			name: "partial evidence with derived warnings is valid",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
				manifest.Attempts[0].Status = "PARTIAL"
				manifest.Attempts[0].Error = "temporary browser cleanup failed"
				manifest.Attempts[0].CleanupFailed = true
				manifest.Attempts = append(manifest.Attempts, Attempt{Number: 4, Status: "ENGINE_FAILED", Error: "engine failed"})
				summary.RequestedRuns = 4
				summary.Warnings = []string{"one or more attempts failed", "temporary browser cleanup failed"}
				// The first sample remains tied to the cleanup-partial attempt.
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest, protocol, summary := validEvidence()
			tt.mutate(&manifest, &summary)
			err := ValidateEvidence(manifest, protocol, &summary)
			if tt.valid && err != nil {
				t.Fatalf("ValidateEvidence() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestValidateEvidenceEnforcesCollectorReachability(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest, **Summary)
		valid  bool
	}{
		{
			name: "partial aggregate cannot contain interrupted attempt",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "PARTIAL"
				(*summary).Status = "PARTIAL"
				manifest.Attempts = append(manifest.Attempts, Attempt{Number: 4, Status: "INTERRUPTED", Error: "interrupted"})
				(*summary).RequestedRuns = 4
				(*summary).Warnings = []string{"one or more attempts failed"}
			},
		},
		{
			name: "interrupted manifest cannot have aggregate",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "INTERRUPTED"
				(*summary).Status = "INTERRUPTED"
			},
		},
		{
			name: "interrupted attempt must be last",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "INTERRUPTED"
				*summary = nil
				manifest.Attempts = append(manifest.Attempts,
					Attempt{Number: 4, Status: "INTERRUPTED", Error: "interrupted"},
					Attempt{Number: 5, Status: "ENGINE_FAILED", Error: "engine failed"},
				)
			},
		},
		{
			name: "interrupted attempt must be unique",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "INTERRUPTED"
				*summary = nil
				manifest.Attempts = append(manifest.Attempts,
					Attempt{Number: 4, Status: "INTERRUPTED", Error: "interrupted"},
					Attempt{Number: 5, Status: "INTERRUPTED", Error: "interrupted"},
				)
			},
		},
		{
			name: "partial with three successful attempts requires aggregate",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "PARTIAL"
				*summary = nil
			},
		},
		{
			name: "running manifest cannot retain attempts",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "RUNNING"
				*summary = nil
			},
		},
		{
			name: "interrupted before first attempt is valid without aggregate",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "INTERRUPTED"
				manifest.Attempts = nil
				manifest.Artifacts = nil
				*summary = nil
			},
			valid: true,
		},
		{
			name: "interrupted final attempt is valid without aggregate",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "INTERRUPTED"
				*summary = nil
				manifest.Attempts = append(manifest.Attempts, Attempt{Number: 4, Status: "INTERRUPTED", Error: "interrupted"})
			},
			valid: true,
		},
		{
			name: "partial without aggregate requires at least three attempts",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "PARTIAL"
				manifest.Attempts = nil
				manifest.Artifacts = nil
				*summary = nil
			},
		},
		{
			name: "partial without aggregate rejects one attempt",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "PARTIAL"
				manifest.Attempts = manifest.Attempts[:1]
				manifest.Artifacts = manifest.Artifacts[:1]
				*summary = nil
			},
		},
		{
			name: "partial without aggregate rejects two attempts",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "PARTIAL"
				manifest.Attempts = manifest.Attempts[:2]
				manifest.Artifacts = manifest.Artifacts[:2]
				*summary = nil
			},
		},
		{
			name: "incomplete partial is valid without aggregate after three attempts",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "PARTIAL"
				manifest.Attempts = manifest.Attempts[:2]
				manifest.Artifacts = manifest.Artifacts[:2]
				manifest.Attempts = append(manifest.Attempts, Attempt{Number: 3, Status: "ENGINE_FAILED", Error: "engine failed"})
				*summary = nil
			},
			valid: true,
		},
		{
			name: "initial running manifest is valid without aggregate",
			mutate: func(manifest *Manifest, summary **Summary) {
				manifest.Status = "RUNNING"
				manifest.Attempts = nil
				manifest.Artifacts = nil
				*summary = nil
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest, protocol, summary := validEvidence()
			summaryPointer := &summary
			tt.mutate(&manifest, &summaryPointer)
			err := ValidateEvidence(manifest, protocol, summaryPointer)
			if tt.valid && err != nil {
				t.Fatalf("ValidateEvidence() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestValidateEvidenceEnforcesCollectorAttemptCombinations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest, *Summary)
		valid  bool
	}{
		{
			name: "successful attempt cannot have nonzero exit",
			mutate: func(manifest *Manifest, _ *Summary) {
				manifest.Attempts[0].ExitCode = 1
			},
		},
		{
			name: "partial sample requires recorded cleanup failure",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
				manifest.Attempts[0].Status = "PARTIAL"
				manifest.Attempts[0].Error = "temporary browser cleanup failed"
				summary.Warnings = []string{"temporary browser cleanup failed"}
			},
		},
		{
			name: "engine failure cannot retain an artifact",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
				manifest.Attempts[0].Status = "ENGINE_FAILED"
				manifest.Attempts[0].Error = "engine failed"
				summary.Warnings = []string{"one or more attempts failed"}
			},
		},
		{
			name: "evidence write failure has zero engine exit",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
				manifest.Attempts = append(manifest.Attempts, Attempt{Number: 4, Status: "ENGINE_FAILED", ExitCode: 1, Error: "evidence write failed"})
				summary.RequestedRuns = 4
				summary.Warnings = []string{"one or more attempts failed"}
			},
		},
		{
			name: "engine failure with zero exit is possible when execution reports an error",
			mutate: func(manifest *Manifest, summary *Summary) {
				manifest.Status = "PARTIAL"
				summary.Status = "PARTIAL"
				manifest.Attempts = append(manifest.Attempts, Attempt{Number: 4, Status: "ENGINE_FAILED", Error: "engine failed"})
				summary.RequestedRuns = 4
				summary.Warnings = []string{"one or more attempts failed"}
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest, protocol, summary := validEvidence()
			tt.mutate(&manifest, &summary)
			err := ValidateEvidence(manifest, protocol, &summary)
			if tt.valid && err != nil {
				t.Fatalf("ValidateEvidence() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestValidateEvidenceRequiresCanonicalAttemptArtifactPath(t *testing.T) {
	manifest, protocol, summary := validEvidence()
	manifest.Attempts[0].Artifact = "reports/run-1.lhr.json"
	manifest.Artifacts[0].Path = manifest.Attempts[0].Artifact
	if err := ValidateEvidence(manifest, protocol, &summary); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
	}
}

func TestValidAttemptAcceptsOnlyCollectorReachableShapes(t *testing.T) {
	tests := []struct {
		name    string
		attempt Attempt
		valid   bool
	}{
		{name: "OK", attempt: Attempt{Status: "OK", Artifact: "samples/run-1.lhr.json"}, valid: true},
		{name: "OK nonzero exit", attempt: Attempt{Status: "OK", ExitCode: 1, Artifact: "samples/run-1.lhr.json"}},
		{name: "PARTIAL cleanup", attempt: Attempt{Status: "PARTIAL", Artifact: "samples/run-1.lhr.json", Error: "temporary browser cleanup failed", CleanupFailed: true}, valid: true},
		{name: "PARTIAL without cleanup", attempt: Attempt{Status: "PARTIAL", Artifact: "samples/run-1.lhr.json", Error: "temporary browser cleanup failed"}},
		{name: "ENGINE_FAILED process exit", attempt: Attempt{Status: "ENGINE_FAILED", ExitCode: 1, Error: "engine failed"}, valid: true},
		{name: "ENGINE_FAILED execution error with zero exit", attempt: Attempt{Status: "ENGINE_FAILED", Error: "engine failed"}, valid: true},
		{name: "ENGINE_FAILED evidence write", attempt: Attempt{Status: "ENGINE_FAILED", Error: "evidence write failed"}, valid: true},
		{name: "ENGINE_FAILED evidence write nonzero exit", attempt: Attempt{Status: "ENGINE_FAILED", ExitCode: 1, Error: "evidence write failed"}},
		{name: "ENGINE_FAILED with artifact", attempt: Attempt{Status: "ENGINE_FAILED", Artifact: "samples/run-1.lhr.json", Error: "engine failed"}},
		{name: "PARSE_FAILED", attempt: Attempt{Status: "PARSE_FAILED", Artifact: "samples/run-1.lhr.json", Error: "Lighthouse report parse failed"}, valid: true},
		{name: "PARSE_FAILED nonzero exit", attempt: Attempt{Status: "PARSE_FAILED", ExitCode: 1, Artifact: "samples/run-1.lhr.json", Error: "Lighthouse report parse failed"}},
		{name: "INTERRUPTED before artifact", attempt: Attempt{Status: "INTERRUPTED", Error: "interrupted"}, valid: true},
		{name: "INTERRUPTED after artifact", attempt: Attempt{Status: "INTERRUPTED", Artifact: "samples/run-1.lhr.json", Error: "interrupted", CleanupFailed: true}, valid: true},
		{name: "INTERRUPTED nonzero exit", attempt: Attempt{Status: "INTERRUPTED", ExitCode: 1, Error: "interrupted"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validAttempt(tt.attempt); got != tt.valid {
				t.Fatalf("validAttempt(%+v) = %t, want %t", tt.attempt, got, tt.valid)
			}
		})
	}
}

func TestValidateProtocolUsesEngineVersionContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Protocol)
		valid  bool
	}{
		{
			name: "locked Lighthouse version",
			mutate: func(protocol *Protocol) {
				protocol.LighthouseVersion = "13.4.2"
			},
		},
		{
			name: "Node below engine minimum",
			mutate: func(protocol *Protocol) {
				protocol.NodeVersion = "22.18.9"
			},
		},
		{
			name: "newer supported Node major",
			mutate: func(protocol *Protocol) {
				protocol.NodeVersion = "24.0.0"
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, protocol, _ := validEvidence()
			tt.mutate(&protocol)
			protocol = CompleteProtocol(protocol)
			err := ValidateProtocol(protocol)
			if tt.valid && err != nil {
				t.Fatalf("ValidateProtocol() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateProtocol() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestValidateProtocolRejectsSelfFingerprintedNonCanonicalNodeVersions(t *testing.T) {
	for _, nodeVersion := range []string{"24.0", "24.0.foo", "24.0.0.0", "24.0.0-extra"} {
		t.Run(nodeVersion, func(t *testing.T) {
			_, protocol, _ := validEvidence()
			protocol.NodeVersion = nodeVersion
			protocol = CompleteProtocol(protocol)
			if err := ValidateProtocol(protocol); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateProtocol(%q) error = %v, want invalid evidence", nodeVersion, err)
			}
		})
	}
}

func TestStoreValidateEvidenceBindsArtifactHashesAndParsedSamples(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, directory string)
		valid  bool
	}{
		{
			name:  "unchanged descriptor-bound LHR evidence",
			valid: true,
		},
		{
			name: "replacement after manifest commit",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, "samples", "run-1.lhr.json"), []byte(`{"not":"an LHR"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink artifact",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				artifact := filepath.Join(directory, "samples", "run-1.lhr.json")
				if err := os.Remove(artifact); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(directory, "outside.lhr.json")
				if err := os.WriteFile(target, []byte(minimalLHR(1)), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, artifact); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := storedEvidence(t, nil)
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if tt.mutate != nil {
				tt.mutate(t, directory)
			}
			_, err = store.ValidateEvidence()
			if tt.valid && err != nil {
				t.Fatalf("ValidateEvidence() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestStoreValidateEvidenceRejectsLHRThatDoesNotMatchPersistedSample(t *testing.T) {
	directory := storedEvidence(t, func(summary *Summary) {
		summary.Samples[0].Sample.LCP++
		summary.Metrics = evidenceMetrics(summary.Samples)
	})
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateEvidence(); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
	}
}

func TestStoreValidateEvidenceRequiresExactCanonicalWarnings(t *testing.T) {
	tests := []struct {
		name          string
		raw           func(int) string
		mutateSummary func(*Summary)
	}{
		{
			name: "all OK evidence cannot add attempt failure warning",
			mutateSummary: func(summary *Summary) {
				summary.Warnings = []string{"one or more attempts failed"}
			},
		},
		{
			name: "all OK evidence cannot add cleanup warning",
			mutateSummary: func(summary *Summary) {
				summary.Warnings = []string{"temporary browser cleanup failed"}
			},
		},
		{
			name: "descriptor bound Lighthouse warning cannot be omitted",
			raw: func(run int) string {
				if run == 2 {
					return addLHRWarning(minimalLHR(run))
				}
				return minimalLHR(run)
			},
		},
		{
			name: "high dispersion warning cannot be omitted",
			raw: func(run int) string {
				return minimalLHRWithLCP(run, []int{1000, 1000, 3000}[run-1])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := storedEvidenceWithRaw(t, tt.raw, tt.mutateSummary)
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ValidateEvidence(); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestStoreValidateEvidenceDerivesFinalURLFromAttemptOrder(t *testing.T) {
	first := "https://example.test/first"
	second := "https://example.test/second"
	tests := []struct {
		name          string
		artifactOrder []int
		declaredFinal string
		valid         bool
	}{
		{
			name:          "ordered artifact ledger uses first successful attempt",
			artifactOrder: []int{1, 2, 3},
			declaredFinal: first,
			valid:         true,
		},
		{
			name:          "reversed ledger cannot select second attempt final URL",
			artifactOrder: []int{2, 1, 3},
			declaredFinal: second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := storedEvidenceWithArtifactOrder(t, tt.artifactOrder, tt.declaredFinal)
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.ValidateEvidence()
			if tt.valid && err != nil {
				t.Fatalf("ValidateEvidence() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestStoreValidateEvidenceRejectsPartialAggregateWithInterruptedAttempt(t *testing.T) {
	directory := storedPartialBundleWithInterruptedAttempt(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateEvidence(); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
	}
}

func TestStoreValidateEvidenceVerifiesNoSummaryArtifacts(t *testing.T) {
	const expectedFinalURL = "https://example.test/landing"
	tests := []struct {
		name            string
		status          string
		finalURL        string
		versions        []string
		interruptedLast bool
		valid           bool
	}{
		{
			name:     "partial no-summary rejects unsupported Lighthouse version",
			status:   "PARTIAL",
			finalURL: expectedFinalURL,
			versions: []string{"13.4.2"},
		},
		{
			name:     "partial no-summary rejects manifest final URL not from attempt order",
			status:   "PARTIAL",
			finalURL: "https://example.test/other",
			versions: []string{"13.4.1", "13.4.1"},
		},
		{
			name:            "interrupted no-summary rejects manifest final URL not from attempt order",
			status:          "INTERRUPTED",
			finalURL:        "https://example.test/other",
			versions:        []string{"13.4.1", "13.4.1"},
			interruptedLast: true,
		},
		{
			name:     "partial no-summary accepts supported descriptor evidence and attempt final URL",
			status:   "PARTIAL",
			finalURL: expectedFinalURL,
			versions: []string{"13.4.1", "13.4.1"},
			valid:    true,
		},
		{
			name:            "interrupted no-summary accepts supported descriptor evidence and attempt final URL",
			status:          "INTERRUPTED",
			finalURL:        expectedFinalURL,
			versions:        []string{"13.4.1", "13.4.1"},
			interruptedLast: true,
			valid:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := storedNoSummaryBundle(t, tt.status, tt.finalURL, tt.versions, tt.interruptedLast)
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.ValidateEvidence()
			if tt.valid && err != nil {
				t.Fatalf("ValidateEvidence() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestStoreValidateEvidenceRejectsCommitPointBypass(t *testing.T) {
	tests := []struct {
		name      string
		directory func(*testing.T) string
		prepare   func(*testing.T, string)
	}{
		{
			name: "pending partial manifest that is otherwise valid",
			directory: func(t *testing.T) string {
				return storedNoSummaryBundle(t, "PARTIAL", "https://example.test/landing", []string{"13.4.1", "13.4.1"}, false)
			},
			prepare: demoteFinalManifestToPending,
		},
		{
			name: "pending OK manifest that is otherwise valid",
			directory: func(t *testing.T) string {
				return storedEvidence(t, nil)
			},
			prepare: demoteFinalManifestToPending,
		},
		{
			name: "final manifest cannot remain running",
			directory: func(t *testing.T) string {
				directory := filepath.Join(t.TempDir(), "run")
				store, err := Create(directory, Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, completeProtocol())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.WriteArtifact(manifestFile, []byte(`{"schemaVersion":1,"status":"RUNNING","requestedUrl":"https://example.test/"}`)); err != nil {
					t.Fatal(err)
				}
				return directory
			},
		},
		{
			name: "pending manifest cannot retain stray summary",
			directory: func(t *testing.T) string {
				directory := filepath.Join(t.TempDir(), "run")
				store, err := Create(directory, Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, completeProtocol())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.WriteArtifact(summaryFile, []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
				return directory
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := tt.directory(t)
			if tt.prepare != nil {
				tt.prepare(t, directory)
			}
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ValidateEvidence(); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
			}
		})
	}
}

func TestStoreValidateEvidenceRejectsArtifactReplacedAfterLstat(t *testing.T) {
	directory := storedEvidence(t, nil)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	originalHook := afterArtifactLstat
	afterArtifactLstat = func(_ *os.Root, artifact Artifact) {
		if artifact.Path != "samples/run-1.lhr.json" {
			return
		}
		path := filepath.Join(directory, filepath.FromSlash(artifact.Path))
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(minimalLHR(99)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { afterArtifactLstat = originalHook }()

	if _, err := store.ValidateEvidence(); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
	}
}

func TestStoreValidateEvidenceRejectsParentReplacedWithRootInternalSymlink(t *testing.T) {
	directory := storedEvidence(t, nil)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	originalHook := afterArtifactParentCheck
	originalLstatHook := afterArtifactLstat
	afterArtifactParentCheck = func(_ *os.Root, artifact Artifact) {
		if artifact.Path != "samples/run-1.lhr.json" {
			return
		}
		samples := filepath.Join(directory, "samples")
		boundSamples := filepath.Join(directory, "bound-samples")
		if err := os.Rename(samples, boundSamples); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("bound-samples", samples); err != nil {
			t.Fatal(err)
		}
	}
	afterArtifactLstat = func(_ *os.Root, artifact Artifact) {
		if artifact.Path != "samples/run-1.lhr.json" {
			return
		}
		samples := filepath.Join(directory, "samples")
		boundSamples := filepath.Join(directory, "bound-samples")
		if err := os.Remove(samples); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(boundSamples, samples); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		afterArtifactParentCheck = originalHook
		afterArtifactLstat = originalLstatHook
	}()

	if _, err := store.ValidateEvidence(); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("ValidateEvidence() error = %v, want invalid evidence", err)
	}
}

func storedEvidence(t *testing.T, mutateSummary func(*Summary)) string {
	return storedEvidenceWithRaw(t, minimalLHR, mutateSummary)
}

func storedEvidenceWithRaw(t *testing.T, rawForAttempt func(int) string, mutateSummary func(*Summary)) string {
	t.Helper()
	if rawForAttempt == nil {
		rawForAttempt = minimalLHR
	}
	directory := filepath.Join(t.TempDir(), "run")
	protocol := completeProtocol()
	store, err := Create(directory, Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	attempts := make([]Attempt, 0, 3)
	artifacts := make([]Artifact, 0, 3)
	samples := make([]SuccessfulSample, 0, 3)
	for attempt := 1; attempt <= 3; attempt++ {
		raw := []byte(rawForAttempt(attempt))
		digest, err := store.WriteArtifact(fmt.Sprintf("samples/run-%d.lhr.json", attempt), raw)
		if err != nil {
			t.Fatal(err)
		}
		sample, err := lhr.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		sample.Warnings = nil
		path := fmt.Sprintf("samples/run-%d.lhr.json", attempt)
		attempts = append(attempts, Attempt{Number: attempt, Status: "OK", Artifact: path})
		artifacts = append(artifacts, Artifact{Kind: "lhr", Path: path, SHA256: digest})
		samples = append(samples, SuccessfulSample{Attempt: attempt, Sample: sample})
	}
	summary := Summary{
		SchemaVersion:  1,
		Status:         "OK",
		Profile:        protocol.Profile,
		RequestedURL:   "https://example.test/",
		FinalURL:       "https://example.test/landing",
		RequestedRuns:  3,
		SuccessfulRuns: 3,
		Samples:        samples,
		Metrics:        evidenceMetrics(samples),
	}
	if mutateSummary != nil {
		mutateSummary(&summary)
	}
	if err := store.Finalize(Manifest{SchemaVersion: 1, Status: "OK", RequestedURL: "https://example.test/", FinalURL: summary.FinalURL, Attempts: attempts, Artifacts: artifacts}, &summary); err != nil {
		t.Fatal(err)
	}
	return directory
}

func storedEvidenceWithArtifactOrder(t *testing.T, artifactOrder []int, declaredFinalURL string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "run")
	protocol := completeProtocol()
	store, err := Create(directory, Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	urls := []string{"https://example.test/first", "https://example.test/second", "https://example.test/second"}
	attempts := make([]Attempt, 0, len(urls))
	artifactsByAttempt := make(map[int]Artifact, len(urls))
	samples := make([]SuccessfulSample, 0, len(urls))
	for attempt, finalURL := range urls {
		number := attempt + 1
		raw := []byte(minimalLHRWithFinalURL(number, finalURL))
		path := fmt.Sprintf("samples/run-%d.lhr.json", number)
		digest, err := store.WriteArtifact(path, raw)
		if err != nil {
			t.Fatal(err)
		}
		sample, err := lhr.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		sample.Warnings = nil
		attempts = append(attempts, Attempt{Number: number, Status: "OK", Artifact: path})
		artifactsByAttempt[number] = Artifact{Kind: "lhr", Path: path, SHA256: digest}
		samples = append(samples, SuccessfulSample{Attempt: number, Sample: sample})
	}
	artifacts := make([]Artifact, 0, len(artifactOrder))
	for _, number := range artifactOrder {
		artifacts = append(artifacts, artifactsByAttempt[number])
	}
	summary := Summary{
		SchemaVersion:  1,
		Status:         "OK",
		Profile:        protocol.Profile,
		RequestedURL:   "https://example.test/",
		FinalURL:       declaredFinalURL,
		RequestedRuns:  len(attempts),
		SuccessfulRuns: len(samples),
		Samples:        samples,
		Metrics:        evidenceMetrics(samples),
		Warnings: CanonicalWarnings(attempts, samples, WarningSignals{
			RawFinalURLs: append([]string(nil), urls...),
		}),
	}
	if err := store.Finalize(Manifest{SchemaVersion: 1, Status: "OK", RequestedURL: summary.RequestedURL, FinalURL: declaredFinalURL, Attempts: attempts, Artifacts: artifacts}, &summary); err != nil {
		t.Fatal(err)
	}
	return directory
}

func storedPartialBundleWithInterruptedAttempt(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "run")
	protocol := completeProtocol()
	store, err := Create(directory, Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	attempts := make([]Attempt, 0, 4)
	artifacts := make([]Artifact, 0, 3)
	samples := make([]SuccessfulSample, 0, 3)
	for number := 1; number <= 3; number++ {
		raw := []byte(minimalLHR(number))
		path := fmt.Sprintf("samples/run-%d.lhr.json", number)
		digest, err := store.WriteArtifact(path, raw)
		if err != nil {
			t.Fatal(err)
		}
		sample, err := lhr.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		sample.Warnings = nil
		attempts = append(attempts, Attempt{Number: number, Status: "OK", Artifact: path})
		artifacts = append(artifacts, Artifact{Kind: "lhr", Path: path, SHA256: digest})
		samples = append(samples, SuccessfulSample{Attempt: number, Sample: sample})
	}
	attempts = append(attempts, Attempt{Number: 4, Status: "INTERRUPTED", Error: "interrupted"})
	summary := Summary{
		SchemaVersion:  1,
		Status:         "PARTIAL",
		Profile:        protocol.Profile,
		RequestedURL:   "https://example.test/",
		FinalURL:       "https://example.test/landing",
		RequestedRuns:  4,
		SuccessfulRuns: 3,
		Samples:        samples,
		Metrics:        evidenceMetrics(samples),
		Warnings:       []string{"one or more attempts failed"},
	}
	if err := store.Finalize(Manifest{SchemaVersion: 1, Status: "PARTIAL", RequestedURL: summary.RequestedURL, FinalURL: summary.FinalURL, Attempts: attempts, Artifacts: artifacts}, &summary); err != nil {
		t.Fatal(err)
	}
	return directory
}

func storedNoSummaryBundle(t *testing.T, status, finalURL string, versions []string, interruptedLast bool) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "run")
	protocol := completeProtocol()
	store, err := Create(directory, Manifest{SchemaVersion: 1, Status: "RUNNING", RequestedURL: "https://example.test/"}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	attempts := make([]Attempt, 0, 3)
	artifacts := make([]Artifact, 0, len(versions))
	for index, version := range versions {
		number := index + 1
		raw := []byte(minimalLHRWithVersion(number, version))
		path := fmt.Sprintf("samples/run-%d.lhr.json", number)
		digest, err := store.WriteArtifact(path, raw)
		if err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, Attempt{Number: number, Status: "OK", Artifact: path})
		artifacts = append(artifacts, Artifact{Kind: "lhr", Path: path, SHA256: digest})
	}
	for number := len(versions) + 1; number <= 3; number++ {
		if interruptedLast && number == 3 {
			attempts = append(attempts, Attempt{Number: number, Status: "INTERRUPTED", Error: "interrupted"})
			continue
		}
		attempts = append(attempts, Attempt{Number: number, Status: "ENGINE_FAILED", Error: "engine failed"})
	}
	if err := store.Finalize(Manifest{SchemaVersion: 1, Status: status, RequestedURL: "https://example.test/", FinalURL: finalURL, Attempts: attempts, Artifacts: artifacts}, nil); err != nil {
		t.Fatal(err)
	}
	return directory
}

func demoteFinalManifestToPending(t *testing.T, directory string) {
	t.Helper()
	finalPath := filepath.Join(directory, manifestFile)
	contents, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(finalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, pendingManifestFile), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func minimalLHRWithLCP(run, lcp int) string {
	return fmt.Sprintf(`{
  "lighthouseVersion": "13.4.1",
  "finalDisplayedUrl": "https://example.test/landing",
  "categories": {"performance": {"score": 0.8}},
  "environment": {"benchmarkIndex": 1000},
  "audits": {
    "first-contentful-paint": {"numericValue": 1000},
    "largest-contentful-paint": {"numericValue": %d},
    "speed-index": {"numericValue": 2000},
    "total-blocking-time": {"numericValue": 100},
    "cumulative-layout-shift": {"numericValue": 0.02},
    "lcp-breakdown-insight": {"details": {"items": [{"type": "node", "selector": "main > img"}]}}
  }
}`,
		lcp,
	)
}

func minimalLHRWithFinalURL(run int, finalURL string) string {
	return fmt.Sprintf(`{
  "lighthouseVersion": "13.4.1",
  "finalDisplayedUrl": %q,
  "categories": {"performance": {"score": 0.8}},
  "environment": {"benchmarkIndex": 1000},
  "audits": {
    "first-contentful-paint": {"numericValue": %d},
    "largest-contentful-paint": {"numericValue": %d},
    "speed-index": {"numericValue": %d},
    "total-blocking-time": {"numericValue": %d},
    "cumulative-layout-shift": {"numericValue": 0.02},
    "lcp-breakdown-insight": {"details": {"items": [{"type": "node", "selector": "main > img"}]}}
  }
}`,
		finalURL,
		1000+run,
		1500+run,
		2000+run,
		100+run,
	)
}

func minimalLHRWithVersion(run int, version string) string {
	return fmt.Sprintf(`{
  "lighthouseVersion": %q,
  "finalDisplayedUrl": "https://example.test/landing",
  "categories": {"performance": {"score": 0.8}},
  "environment": {"benchmarkIndex": 1000},
  "audits": {
    "first-contentful-paint": {"numericValue": %d},
    "largest-contentful-paint": {"numericValue": %d},
    "speed-index": {"numericValue": %d},
    "total-blocking-time": {"numericValue": %d},
    "cumulative-layout-shift": {"numericValue": 0.02},
    "lcp-breakdown-insight": {"details": {"items": [{"type": "node", "selector": "main > img"}]}}
  }
}`,
		version,
		1000+run,
		1500+run,
		2000+run,
		100+run,
	)
}

func addLHRWarning(raw string) string {
	return raw[:len(raw)-2] + `,
  "runWarnings": ["transient warning"]
}`
}

func minimalLHR(run int) string {
	return fmt.Sprintf(`{
  "lighthouseVersion": "13.4.1",
  "finalDisplayedUrl": "https://example.test/landing",
  "categories": {"performance": {"score": 0.8}},
  "environment": {"benchmarkIndex": 1000},
  "audits": {
    "first-contentful-paint": {"numericValue": %d},
    "largest-contentful-paint": {"numericValue": %d},
    "speed-index": {"numericValue": %d},
    "total-blocking-time": {"numericValue": %d},
    "cumulative-layout-shift": {"numericValue": 0.02},
    "lcp-breakdown-insight": {"details": {"items": [{"type": "node", "selector": "main > img"}]}}
  }
}`,
		1000+run,
		1500+run,
		2000+run,
		100+run,
	)
}

func validEvidence() (Manifest, Protocol, Summary) {
	protocol := CompleteProtocol(Protocol{
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
	attempts := []Attempt{
		{Number: 1, Status: "OK", Artifact: "samples/run-1.lhr.json"},
		{Number: 2, Status: "OK", Artifact: "samples/run-2.lhr.json"},
		{Number: 3, Status: "OK", Artifact: "samples/run-3.lhr.json"},
	}
	samples := []SuccessfulSample{
		{Attempt: 1, Sample: evidenceSample(protocol.LighthouseVersion)},
		{Attempt: 2, Sample: evidenceSample(protocol.LighthouseVersion)},
		{Attempt: 3, Sample: evidenceSample(protocol.LighthouseVersion)},
	}
	return Manifest{
			SchemaVersion: 1,
			Status:        "OK",
			RequestedURL:  "https://example.test/",
			Attempts:      attempts,
			Artifacts: []Artifact{
				{Kind: "lhr", Path: "samples/run-1.lhr.json", SHA256: testArtifactSHA256},
				{Kind: "lhr", Path: "samples/run-2.lhr.json", SHA256: testArtifactSHA256},
				{Kind: "lhr", Path: "samples/run-3.lhr.json", SHA256: testArtifactSHA256},
			},
		}, protocol, Summary{
			SchemaVersion:  1,
			Status:         "OK",
			Profile:        protocol.Profile,
			RequestedURL:   "https://example.test/",
			RequestedRuns:  3,
			SuccessfulRuns: 3,
			Samples:        samples,
			Metrics:        evidenceMetrics(samples),
		}
}

const testArtifactSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"

func evidenceSample(version string) lhr.Sample {
	return lhr.Sample{LighthouseVersion: version, PerformanceScore: 70, FCP: 1000, LCP: 1500, SpeedIndex: 1200, TBT: 20, CLS: 0.1, BenchmarkIndex: 1}
}

func evidenceMetrics(samples []SuccessfulSample) Metrics {
	scores, fcp, lcp, speedIndex, tbt, cls := make([]float64, len(samples)), make([]float64, len(samples)), make([]float64, len(samples)), make([]float64, len(samples)), make([]float64, len(samples)), make([]float64, len(samples))
	for index, item := range samples {
		scores[index] = item.Sample.PerformanceScore
		fcp[index] = item.Sample.FCP
		lcp[index] = item.Sample.LCP
		speedIndex[index] = item.Sample.SpeedIndex
		tbt[index] = item.Sample.TBT
		cls[index] = item.Sample.CLS
	}
	benchmarkIndex := make([]float64, len(samples))
	for index, item := range samples {
		benchmarkIndex[index] = item.Sample.BenchmarkIndex
	}
	return Metrics{PerformanceScore: stats.Summarize(scores), FCP: stats.Summarize(fcp), LCP: stats.Summarize(lcp), SpeedIndex: stats.Summarize(speedIndex), TBT: stats.Summarize(tbt), CLS: stats.Summarize(cls), BenchmarkIndex: stats.Summarize(benchmarkIndex)}
}
