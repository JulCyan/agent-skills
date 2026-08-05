// Package bundle stores collection evidence in a private, append-only directory.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/lhr"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

var (
	ErrExists              = errors.New("evidence bundle already exists")
	ErrInvalidArtifactPath = errors.New("invalid evidence artifact path")
)

// Artifact identifies evidence relative to its containing bundle directory.
type Artifact struct {
	Kind string `json:"kind,omitempty"`
	Path string `json:"path"`
}

// Manifest records the run-level evidence identity and paths. Fields may be
// populated incrementally by collection while all artifact paths stay relative.
type Manifest struct {
	SchemaVersion int        `json:"schemaVersion,omitempty"`
	Status        string     `json:"status,omitempty"`
	RequestedURL  string     `json:"requestedUrl,omitempty"`
	FinalURL      string     `json:"finalUrl,omitempty"`
	StartedAt     string     `json:"startedAt,omitempty"`
	FinishedAt    string     `json:"finishedAt,omitempty"`
	SummarySHA256 string     `json:"summarySha256,omitempty"`
	Attempts      []Attempt  `json:"attempts,omitempty"`
	Artifacts     []Artifact `json:"artifacts,omitempty"`
}

// Attempt preserves every requested collection attempt. Error messages are
// intentionally short status labels so caller-supplied URLs and engine output
// never become persisted evidence metadata.
type Attempt struct {
	Number     int    `json:"number"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exitCode,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Artifact   string `json:"artifact,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Protocol records the resolved profile and runtime fingerprint for a bundle.
type Protocol struct {
	SchemaVersion     int      `json:"schemaVersion,omitempty"`
	Profile           string   `json:"profile,omitempty"`
	FormFactor        string   `json:"formFactor,omitempty"`
	ThrottlingMethod  string   `json:"throttlingMethod,omitempty"`
	ResolvedFlags     []string `json:"resolvedFlags,omitempty"`
	RuntimeFlags      []string `json:"runtimeFlags,omitempty"`
	LighthouseVersion string   `json:"lighthouseVersion,omitempty"`
	NodeVersion       string   `json:"nodeVersion,omitempty"`
	ChromeVersion     string   `json:"chromeVersion,omitempty"`
	OS                string   `json:"os,omitempty"`
	Arch              string   `json:"arch,omitempty"`
	Fingerprint       string   `json:"fingerprint,omitempty"`
}

// Summary contains only sanitized sample data and independent distributions.
// PerformanceScore is the distribution of Lighthouse-provided score samples,
// never a score reconstructed from metric medians.
type Summary struct {
	SchemaVersion  int                `json:"schemaVersion,omitempty"`
	Status         string             `json:"status,omitempty"`
	Profile        string             `json:"profile,omitempty"`
	RequestedURL   string             `json:"requestedUrl,omitempty"`
	FinalURL       string             `json:"finalUrl,omitempty"`
	RequestedRuns  int                `json:"requestedRuns,omitempty"`
	SuccessfulRuns int                `json:"successfulRuns,omitempty"`
	Samples        []SuccessfulSample `json:"samples,omitempty"`
	Metrics        Metrics            `json:"metrics,omitempty"`
	Warnings       []string           `json:"warnings,omitempty"`
}

type SuccessfulSample struct {
	Attempt int        `json:"attempt"`
	Sample  lhr.Sample `json:"sample"`
}

type Metrics struct {
	PerformanceScore stats.Distribution `json:"performanceScore"`
	FCP              stats.Distribution `json:"fcp"`
	LCP              stats.Distribution `json:"lcp"`
	SpeedIndex       stats.Distribution `json:"speedIndex"`
	TBT              stats.Distribution `json:"tbt"`
	CLS              stats.Distribution `json:"cls"`
	BenchmarkIndex   stats.Distribution `json:"benchmarkIndex"`
}

// CompleteProtocol fills portable environment details and computes the strict
// comparison fingerprint. Version discovery is performed by engine; this
// helper does not probe the host or persist executable paths.
func CompleteProtocol(protocol Protocol) Protocol {
	if protocol.SchemaVersion == 0 {
		protocol.SchemaVersion = 1
	}
	if protocol.OS == "" {
		protocol.OS = runtime.GOOS
	}
	if protocol.Arch == "" {
		protocol.Arch = runtime.GOARCH
	}
	protocol.ResolvedFlags = append([]string(nil), protocol.ResolvedFlags...)
	protocol.RuntimeFlags = append([]string(nil), protocol.RuntimeFlags...)
	protocol.Fingerprint = protocolFingerprint(protocol)
	return protocol
}

func protocolFingerprint(protocol Protocol) string {
	type fingerprintInput struct {
		SchemaVersion     int      `json:"schemaVersion"`
		Profile           string   `json:"profile"`
		FormFactor        string   `json:"formFactor"`
		ThrottlingMethod  string   `json:"throttlingMethod"`
		ResolvedFlags     []string `json:"resolvedFlags"`
		RuntimeFlags      []string `json:"runtimeFlags"`
		LighthouseVersion string   `json:"lighthouseVersion"`
		NodeVersion       string   `json:"nodeVersion"`
		ChromeVersion     string   `json:"chromeVersion"`
		OS                string   `json:"os"`
		Arch              string   `json:"arch"`
	}
	encoded, err := json.Marshal(fingerprintInput{
		SchemaVersion:     protocol.SchemaVersion,
		Profile:           protocol.Profile,
		FormFactor:        protocol.FormFactor,
		ThrottlingMethod:  protocol.ThrottlingMethod,
		ResolvedFlags:     protocol.ResolvedFlags,
		RuntimeFlags:      protocol.RuntimeFlags,
		LighthouseVersion: protocol.LighthouseVersion,
		NodeVersion:       protocol.NodeVersion,
		ChromeVersion:     protocol.ChromeVersion,
		OS:                protocol.OS,
		Arch:              protocol.Arch,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
