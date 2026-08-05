package bundle

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/safeurl"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

var ErrInvalidEvidence = errors.New("invalid evidence bundle")

var (
	versionPattern     = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,3}$`)
	platformPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	digestPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	warningAttempt     = regexp.MustCompile(`^Lighthouse reported warnings on attempt [1-9][0-9]*$`)
)

// ValidateEvidence verifies this Store's immutable documents and every
// artifact from a root-bound descriptor. Callers rendering or comparing a
// bundle must use this method rather than validating copied metadata alone.
func (s *Store) ValidateEvidence() (*Summary, error) {
	summary, err := s.Summary()
	if err != nil {
		return nil, invalidEvidence("summary")
	}
	if err := s.validateCommitPoint(); err != nil {
		return nil, err
	}
	manifest := s.Manifest()
	protocol := s.Protocol()
	if err := ValidateEvidence(manifest, protocol, summary); err != nil {
		return nil, err
	}
	root, err := s.openRoot()
	if err != nil {
		return nil, invalidEvidence("store root")
	}
	defer root.Close()
	warningSignals, err := validateStoredArtifacts(root, manifest, protocol, summary)
	if err != nil {
		return nil, err
	}
	if summary != nil && !sameStrings(summary.Warnings, CanonicalWarnings(manifest.Attempts, summary.Samples, warningSignals)) {
		return nil, invalidEvidence("warnings")
	}
	return summary, nil
}

// ValidateEvidence validates the three linked evidence documents before any
// summary is rendered or compared. A summary is optional for a pending or
// interrupted bundle; when present it must be a final OK or PARTIAL aggregate.
func ValidateEvidence(manifest Manifest, protocol Protocol, summary *Summary) error {
	if err := ValidateProtocol(protocol); err != nil {
		return invalidEvidence("protocol")
	}
	if manifest.SchemaVersion != 1 || !validManifestStatus(manifest.Status) {
		return invalidEvidence("manifest")
	}
	if !validDisplayedURL(manifest.RequestedURL, false) || !validDisplayedURL(manifest.FinalURL, true) {
		return invalidEvidence("manifest URL")
	}
	if err := validateLedger(manifest); err != nil {
		return err
	}
	if err := validateReachability(manifest, summary); err != nil {
		return err
	}
	if summary == nil {
		return nil
	}
	if summary.SchemaVersion != 1 || summary.Status != manifest.Status || (summary.Status != "OK" && summary.Status != "PARTIAL") {
		return invalidEvidence("aggregate status")
	}
	if summary.RequestedURL != manifest.RequestedURL || summary.FinalURL != manifest.FinalURL || !validDisplayedURL(summary.RequestedURL, false) || !validDisplayedURL(summary.FinalURL, true) {
		return invalidEvidence("aggregate URL")
	}
	if summary.Profile != protocol.Profile || summary.RequestedRuns != len(manifest.Attempts) || summary.SuccessfulRuns != len(summary.Samples) || summary.SuccessfulRuns < 3 {
		return invalidEvidence("aggregate counts")
	}
	if err := validateAggregateState(manifest, *summary); err != nil {
		return err
	}
	if err := validateSamples(manifest, protocol, *summary); err != nil {
		return err
	}
	if !validWarnings(summary.Warnings, len(manifest.Attempts)) {
		return invalidEvidence("warnings")
	}
	return nil
}

// validateReachability accepts only states emitted by the sequential
// collector. It prevents a hand-authored ledger from claiming a completed
// aggregate after cancellation or from treating an incomplete collection as a
// resumable pending bundle.
func validateReachability(manifest Manifest, summary *Summary) error {
	interrupted := 0
	interruptedIndex := -1
	successful := 0
	for index, attempt := range manifest.Attempts {
		switch attempt.Status {
		case "OK", "PARTIAL":
			successful++
		case "INTERRUPTED":
			interrupted++
			interruptedIndex = index
		}
	}
	if interrupted > 0 {
		if manifest.Status != "INTERRUPTED" || summary != nil || interrupted != 1 || interruptedIndex != len(manifest.Attempts)-1 {
			return invalidEvidence("interrupted reachability")
		}
	}
	switch manifest.Status {
	case "OK":
		if summary == nil {
			return invalidEvidence("missing final aggregate")
		}
	case "PARTIAL":
		if summary == nil && (len(manifest.Attempts) < 3 || successful >= 3) {
			return invalidEvidence("missing partial aggregate")
		}
	case "INTERRUPTED":
		if summary != nil {
			return invalidEvidence("interrupted aggregate")
		}
	case "RUNNING":
		if summary != nil || len(manifest.Attempts) != 0 || len(manifest.Artifacts) != 0 || manifest.FinalURL != "" || manifest.FinishedAt != "" || manifest.SummarySHA256 != "" {
			return invalidEvidence("running reachability")
		}
	}
	return nil
}

func validateAggregateState(manifest Manifest, summary Summary) error {
	allOK := true
	hasNonOK := false
	hasCleanupFailure := false
	hasFailedAttempt := false
	for _, attempt := range manifest.Attempts {
		if attempt.CleanupFailed {
			hasCleanupFailure = true
		}
		switch attempt.Status {
		case "OK":
		case "PARTIAL":
			allOK = false
			hasNonOK = true
		case "ENGINE_FAILED", "PARSE_FAILED", "INTERRUPTED":
			allOK = false
			hasNonOK = true
			hasFailedAttempt = true
		default:
			return invalidEvidence("attempt status")
		}
	}
	switch summary.Status {
	case "OK":
		if !allOK || summary.SuccessfulRuns != summary.RequestedRuns {
			return invalidEvidence("aggregate state")
		}
	case "PARTIAL":
		if !hasNonOK {
			return invalidEvidence("aggregate state")
		}
	default:
		return invalidEvidence("aggregate state")
	}
	if hasCleanupFailure && !containsWarning(summary.Warnings, "temporary browser cleanup failed") {
		return invalidEvidence("missing cleanup warning")
	}
	if hasFailedAttempt && !containsWarning(summary.Warnings, "one or more attempts failed") {
		return invalidEvidence("missing attempt failure warning")
	}
	return nil
}

func containsWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}

func validDisplayedURL(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	parsed, err := safeurl.Validate(value)
	return err == nil && safeurl.Display(parsed) == value
}

// ValidateProtocol accepts only the compiled profile contracts produced by the
// executor, never a self-fingerprinted arbitrary profile or runtime flag set.
func ValidateProtocol(protocol Protocol) error {
	compiled, err := profile.Resolve(protocol.Profile)
	if err != nil || protocol.SchemaVersion != 1 || protocol.FormFactor != compiled.FormFactor || protocol.ThrottlingMethod != compiled.ThrottlingMethod || !sameStrings(protocol.ResolvedFlags, compiled.LighthouseArgs) || !sameStrings(protocol.RuntimeFlags, ExpectedRuntimeFlags()) || protocol.LighthouseVersion != engine.LighthouseVersion || !engine.SupportsNodeVersion(protocol.NodeVersion) || !versionPattern.MatchString(protocol.ChromeVersion) || !platformPattern.MatchString(protocol.OS) || !platformPattern.MatchString(protocol.Arch) || !fingerprintPattern.MatchString(protocol.Fingerprint) || protocol.Fingerprint != ProtocolFingerprint(protocol) {
		return invalidEvidence("protocol")
	}
	return nil
}

// ExpectedRuntimeFlags is the exact persisted record of the executor-owned
// arguments and Chrome environment binding. It intentionally has stable safe
// tokens rather than executable paths.
func ExpectedRuntimeFlags() []string {
	return []string{"--only-categories=performance", "--output=json", "--quiet", "--no-enable-error-reporting", "CHROME_PATH=resolved-by-webperf", "fresh-user-data-dir-per-attempt"}
}

func validateLedger(manifest Manifest) error {
	paths := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if artifact.Kind != "lhr" || !relativeArtifactPath(artifact.Path) || !digestPattern.MatchString(artifact.SHA256) {
			return invalidEvidence("artifact")
		}
		if _, exists := paths[artifact.Path]; exists {
			return invalidEvidence("duplicate artifact")
		}
		paths[artifact.Path] = struct{}{}
	}
	if len(manifest.Attempts) == 0 && len(manifest.Artifacts) != 0 {
		return invalidEvidence("artifact ledger")
	}
	referenced := make(map[string]struct{}, len(manifest.Attempts))
	expectedArtifacts := make([]string, 0, len(manifest.Attempts))
	for index, attempt := range manifest.Attempts {
		if attempt.Number != index+1 || !validAttempt(attempt) {
			return invalidEvidence("attempt")
		}
		if attempt.Artifact != "" {
			expectedPath := fmt.Sprintf("samples/run-%d.lhr.json", attempt.Number)
			if !relativeArtifactPath(attempt.Artifact) || attempt.Artifact != expectedPath {
				return invalidEvidence("attempt artifact")
			}
			if _, exists := paths[attempt.Artifact]; !exists {
				return invalidEvidence("attempt artifact ledger")
			}
			if _, exists := referenced[attempt.Artifact]; exists {
				return invalidEvidence("duplicate attempt artifact")
			}
			referenced[attempt.Artifact] = struct{}{}
			expectedArtifacts = append(expectedArtifacts, attempt.Artifact)
		}
	}
	if len(referenced) != len(paths) {
		return invalidEvidence("unreferenced artifact")
	}
	if len(manifest.Artifacts) != len(expectedArtifacts) {
		return invalidEvidence("artifact order")
	}
	for index, expectedPath := range expectedArtifacts {
		if manifest.Artifacts[index].Path != expectedPath {
			return invalidEvidence("artifact order")
		}
	}
	return nil
}

func validateStoredArtifacts(root *os.Root, manifest Manifest, protocol Protocol, summary *Summary) (WarningSignals, error) {
	warningSignals := WarningSignals{}
	artifacts := make(map[string]Artifact, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifacts[artifact.Path] = artifact
	}
	samples := make(map[int]lhr.Sample)
	if summary != nil {
		for _, item := range summary.Samples {
			samples[item.Attempt] = item.Sample
		}
	}
	expectedFinalURL := ""
	for _, attempt := range manifest.Attempts {
		if attempt.Artifact == "" {
			continue
		}
		artifact := artifacts[attempt.Artifact]
		raw, err := readVerifiedArtifact(root, artifact)
		if err != nil {
			return WarningSignals{}, invalidEvidence("artifact content")
		}
		switch attempt.Status {
		case "OK", "PARTIAL":
			sample, err := lhr.Parse(raw)
			if err != nil {
				return WarningSignals{}, invalidEvidence("LHR artifact")
			}
			if sample.LighthouseVersion != protocol.LighthouseVersion {
				return WarningSignals{}, invalidEvidence("LHR version")
			}
			warningSignals.RawFinalURLs = append(warningSignals.RawFinalURLs, sample.FinalURL)
			var invalidFinalURL bool
			sample, invalidFinalURL = sanitizeArtifactSample(sample)
			if invalidFinalURL {
				warningSignals.InvalidFinalURL = true
			}
			if len(sample.Warnings) > 0 {
				warningSignals.LighthouseWarningAttempts = append(warningSignals.LighthouseWarningAttempts, attempt.Number)
			}
			sample.Warnings = nil
			if summary != nil && !sameCollectedSample(samples[attempt.Number], sample) {
				return WarningSignals{}, invalidEvidence("LHR sample mismatch")
			}
			if expectedFinalURL == "" && sample.FinalURL != "" {
				expectedFinalURL = sample.FinalURL
			}
		case "PARSE_FAILED":
			sample, parseErr := lhr.Parse(raw)
			if attempt.Error == "Lighthouse report parse failed" && parseErr == nil {
				return WarningSignals{}, invalidEvidence("parse failure artifact")
			}
			if attempt.Error == "Lighthouse version mismatch" && (parseErr != nil || sample.LighthouseVersion == protocol.LighthouseVersion) {
				return WarningSignals{}, invalidEvidence("version mismatch artifact")
			}
		}
	}
	if manifest.FinalURL != expectedFinalURL {
		return WarningSignals{}, invalidEvidence("final URL evidence")
	}
	if summary != nil && summary.FinalURL != expectedFinalURL {
		return WarningSignals{}, invalidEvidence("final URL evidence")
	}
	return warningSignals, nil
}

func sanitizeArtifactSample(sample lhr.Sample) (lhr.Sample, bool) {
	parsed, err := safeurl.Validate(sample.FinalURL)
	if err != nil {
		sample.FinalURL = ""
		return sample, true
	}
	sample.FinalURL = safeurl.Display(parsed)
	return sample, false
}

func sameCollectedSample(left, right lhr.Sample) bool {
	return len(left.Warnings) == 0 && left.LighthouseVersion == right.LighthouseVersion && left.FinalURL == right.FinalURL && left.PerformanceScore == right.PerformanceScore && left.FCP == right.FCP && left.LCP == right.LCP && left.SpeedIndex == right.SpeedIndex && left.TBT == right.TBT && left.CLS == right.CLS && left.BenchmarkIndex == right.BenchmarkIndex && left.LCPSelector == right.LCPSelector
}

func validManifestStatus(status string) bool {
	switch status {
	case "RUNNING", "OK", "PARTIAL", "INTERRUPTED":
		return true
	default:
		return false
	}
}

func validAttempt(attempt Attempt) bool {
	switch attempt.Status {
	case "OK":
		return attempt.Artifact != "" && attempt.ExitCode == 0 && attempt.Error == "" && !attempt.CleanupFailed
	case "PARTIAL":
		return attempt.Artifact != "" && attempt.ExitCode == 0 && attempt.Error == "temporary browser cleanup failed" && attempt.CleanupFailed
	case "ENGINE_FAILED":
		return attempt.Artifact == "" && (attempt.Error == "engine failed" || (attempt.Error == "evidence write failed" && attempt.ExitCode == 0))
	case "PARSE_FAILED":
		return attempt.Artifact != "" && attempt.ExitCode == 0 && (attempt.Error == "Lighthouse report parse failed" || attempt.Error == "Lighthouse version mismatch")
	case "INTERRUPTED":
		return attempt.ExitCode == 0 && attempt.Error == "interrupted"
	default:
		return false
	}
}

func validateSamples(manifest Manifest, protocol Protocol, summary Summary) error {
	attempts := make(map[int]Attempt, len(manifest.Attempts))
	successfulAttempts := 0
	for _, attempt := range manifest.Attempts {
		attempts[attempt.Number] = attempt
		if attempt.Status == "OK" || attempt.Status == "PARTIAL" {
			successfulAttempts++
		}
	}
	if successfulAttempts != len(summary.Samples) {
		return invalidEvidence("successful samples")
	}
	seen := make(map[int]struct{}, len(summary.Samples))
	for _, item := range summary.Samples {
		attempt, exists := attempts[item.Attempt]
		if !exists || attempt.Artifact == "" || (attempt.Status != "OK" && attempt.Status != "PARTIAL") {
			return invalidEvidence("sample attempt")
		}
		if _, duplicate := seen[item.Attempt]; duplicate || item.Sample.LighthouseVersion != protocol.LighthouseVersion || !validSample(item) {
			return invalidEvidence("sample")
		}
		seen[item.Attempt] = struct{}{}
	}
	if len(seen) != successfulAttempts {
		return invalidEvidence("sample attempts")
	}
	values := summaryValues(summary.Samples)
	if !sameDistribution(summary.Metrics.PerformanceScore, stats.Summarize(values.score)) ||
		!sameDistribution(summary.Metrics.FCP, stats.Summarize(values.fcp)) ||
		!sameDistribution(summary.Metrics.LCP, stats.Summarize(values.lcp)) ||
		!sameDistribution(summary.Metrics.SpeedIndex, stats.Summarize(values.speedIndex)) ||
		!sameDistribution(summary.Metrics.TBT, stats.Summarize(values.tbt)) ||
		!sameDistribution(summary.Metrics.CLS, stats.Summarize(values.cls)) ||
		!sameDistribution(summary.Metrics.BenchmarkIndex, stats.Summarize(values.benchmarkIndex)) {
		return invalidEvidence("metric distributions")
	}
	return nil
}

func validSample(item SuccessfulSample) bool {
	sample := item.Sample
	return finiteInRange(sample.PerformanceScore, 0, 100) && finiteNonNegative(sample.FCP) && finiteNonNegative(sample.LCP) && finiteNonNegative(sample.SpeedIndex) && finiteNonNegative(sample.TBT) && finiteNonNegative(sample.CLS) && finiteNonNegative(sample.BenchmarkIndex)
}

func finiteInRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

type metricValues struct {
	score, fcp, lcp, speedIndex, tbt, cls, benchmarkIndex []float64
}

func summaryValues(samples []SuccessfulSample) metricValues {
	values := metricValues{score: make([]float64, len(samples)), fcp: make([]float64, len(samples)), lcp: make([]float64, len(samples)), speedIndex: make([]float64, len(samples)), tbt: make([]float64, len(samples)), cls: make([]float64, len(samples)), benchmarkIndex: make([]float64, len(samples))}
	for index, item := range samples {
		values.score[index] = item.Sample.PerformanceScore
		values.fcp[index] = item.Sample.FCP
		values.lcp[index] = item.Sample.LCP
		values.speedIndex[index] = item.Sample.SpeedIndex
		values.tbt[index] = item.Sample.TBT
		values.cls[index] = item.Sample.CLS
		values.benchmarkIndex[index] = item.Sample.BenchmarkIndex
	}
	return values
}

func sameDistribution(left, right stats.Distribution) bool {
	return left.Count == right.Count && left.Min == right.Min && left.Max == right.Max && left.Median == right.Median && left.MAD == right.MAD && left.IQR == right.IQR
}

func validWarnings(warnings []string, attempts int) bool {
	allowed := map[string]struct{}{
		"temporary browser cleanup failed":               {},
		"one or more attempts failed":                    {},
		"Lighthouse reported an invalid final URL":       {},
		"final URL changed across successful samples":    {},
		"LCP selector changed across successful samples": {},
		"high dispersion for FCP":                        {},
		"high dispersion for LCP":                        {},
		"high dispersion for Speed Index":                {},
		"high dispersion for TBT":                        {},
		"high dispersion for Performance Score":          {},
		"high dispersion for CLS":                        {},
	}
	seen := make(map[string]struct{}, len(warnings))
	for _, warning := range warnings {
		if _, duplicate := seen[warning]; duplicate {
			return false
		}
		seen[warning] = struct{}{}
		if _, ok := allowed[warning]; ok {
			continue
		}
		if !warningAttempt.MatchString(warning) {
			return false
		}
		var attempt int
		if _, err := fmt.Sscanf(warning, "Lighthouse reported warnings on attempt %d", &attempt); err != nil || attempt < 1 || attempt > attempts {
			return false
		}
	}
	return true
}

func invalidEvidence(part string) error { return fmt.Errorf("%w: %s", ErrInvalidEvidence, part) }

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
