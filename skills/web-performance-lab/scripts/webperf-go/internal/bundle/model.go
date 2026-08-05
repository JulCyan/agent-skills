// Package bundle stores collection evidence in a private, append-only directory.
package bundle

import "errors"

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
	Artifacts     []Artifact `json:"artifacts,omitempty"`
}

// Protocol records the resolved profile and runtime fingerprint for a bundle.
type Protocol struct {
	SchemaVersion     int    `json:"schemaVersion,omitempty"`
	Profile           string `json:"profile,omitempty"`
	LighthouseVersion string `json:"lighthouseVersion,omitempty"`
}
