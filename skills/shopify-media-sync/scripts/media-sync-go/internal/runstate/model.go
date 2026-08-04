package runstate

import (
	"shopify-media-sync/internal/config"
	"shopify-media-sync/internal/mediainput"
)

type DesiredManifest struct {
	RunID       string       `json:"run_id,omitempty"`
	GeneratedAt string       `json:"generated_at,omitempty"`
	Source      string       `json:"source"`
	Rows        []DesiredRow `json:"rows"`
}

type DesiredRow struct {
	RowNo           string            `json:"row_no"`
	SourceFilename  string            `json:"source_filename"`
	TargetFilename  string            `json:"target_filename"`
	FilenameChanged bool              `json:"filename_changed"`
	Alt             string            `json:"alt,omitempty"`
	Translations    map[string]string `json:"translations,omitempty"`
}

type TranslationReadback struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Locale   string `json:"locale"`
	Outdated bool   `json:"outdated"`
	Status   string `json:"status,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type PlanInput struct {
	RunID       string
	GeneratedAt string
	Stores      []config.Store
	Desired     DesiredManifest
	Resources   mediainput.ResourceIndex
	Evidence    Evidence
	Template    string
	TargetTheme string
}

type Plan struct {
	RunID       string                   `json:"run_id"`
	GeneratedAt string                   `json:"generated_at"`
	Template    string                   `json:"template,omitempty"`
	TargetTheme string                   `json:"target_theme,omitempty"`
	Summary     PlanSummary              `json:"summary"`
	Desired     DesiredManifest          `json:"desired"`
	Resources   mediainput.ResourceIndex `json:"resources"`
	Changes     []PlanChange             `json:"changes"`
}

type PlanSummary struct {
	Stores          []string       `json:"stores"`
	Rows            int            `json:"rows"`
	Changes         int            `json:"changes"`
	ByAction        map[string]int `json:"by_action"`
	Errors          int            `json:"errors"`
	NeedsExecute    int            `json:"needs_execute"`
	FilenameChanged int            `json:"filename_changed"`
}

type PlanChange struct {
	Store           string                   `json:"store"`
	RowNo           string                   `json:"row_no"`
	SourceFilename  string                   `json:"source_filename"`
	TargetFilename  string                   `json:"target_filename"`
	FilenameChanged bool                     `json:"filename_changed"`
	Actions         []string                 `json:"actions"`
	Status          string                   `json:"status"`
	Reason          string                   `json:"reason,omitempty"`
	Alt             string                   `json:"alt,omitempty"`
	Translations    map[string]string        `json:"translations,omitempty"`
	Resource        *mediainput.ResourceInfo `json:"resource,omitempty"`
	Evidence        *EvidenceRecord          `json:"evidence,omitempty"`
	Template        string                   `json:"template,omitempty"`
	TargetTheme     string                   `json:"target_theme,omitempty"`
	NeedsExecute    bool                     `json:"needs_execute"`
}

type Evidence struct {
	RunID      string               `json:"run_id,omitempty"`
	PlanSHA256 string               `json:"plan_sha256,omitempty"`
	Preview    *ApplyPreviewBinding `json:"preview_binding,omitempty"`
	Store      string               `json:"store,omitempty"`
	Results    []EvidenceRecord     `json:"results,omitempty"`
	Rows       []EvidenceRecord     `json:"rows,omitempty"`
}

type ApplyPreviewBinding struct {
	Version            int      `json:"version"`
	PlanSHA256         string   `json:"plan_sha256"`
	SelectedStores     []string `json:"selected_stores"`
	StoresConfigPath   string   `json:"stores_config_path,omitempty"`
	StoresConfigSHA256 string   `json:"stores_config_sha256,omitempty"`
	PreviewedAt        string   `json:"previewed_at"`
}

type EvidenceRecord struct {
	Store               string                         `json:"store,omitempty"`
	RowNo               string                         `json:"row_no,omitempty"`
	SourceFilename      string                         `json:"source_filename,omitempty"`
	TargetFilename      string                         `json:"target_filename,omitempty"`
	Filename            string                         `json:"filename,omitempty"`
	FileID              string                         `json:"file_id,omitempty"`
	MediaGID            string                         `json:"media_gid,omitempty"`
	CDNURL              string                         `json:"cdn_url,omitempty"`
	Width               int                            `json:"width,omitempty"`
	Height              int                            `json:"height,omitempty"`
	Bytes               int64                          `json:"bytes,omitempty"`
	SHA256              string                         `json:"sha256,omitempty"`
	MimeType            string                         `json:"mime_type,omitempty"`
	FileStatus          string                         `json:"file_status,omitempty"`
	AltReadback         string                         `json:"alt_readback,omitempty"`
	TranslationReadback map[string]TranslationReadback `json:"translation_readback,omitempty"`
	CurrentStage        string                         `json:"current_stage,omitempty"`
	Upload              StageEvidence                  `json:"upload,omitempty"`
	Alt                 StageEvidence                  `json:"alt,omitempty"`
	Verify              StageEvidence                  `json:"verify,omitempty"`
	Status              string                         `json:"status,omitempty"`
	LastError           string                         `json:"last_error,omitempty"`
	UpdatedAt           string                         `json:"updated_at,omitempty"`
}

type StageEvidence struct {
	Status           string `json:"status,omitempty"`
	Checkpoint       string `json:"checkpoint,omitempty"`
	Attempts         int    `json:"attempts,omitempty"`
	MutationAccepted bool   `json:"mutation_accepted,omitempty"`
	Retryable        bool   `json:"retryable,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

type ApplyCounts struct {
	Total   int `json:"total"`
	Success int `json:"success"`
	Errors  int `json:"errors"`
	Skipped int `json:"skipped"`
}

type ApplyRowError struct {
	Store          string `json:"store,omitempty"`
	RowNo          string `json:"row_no,omitempty"`
	SourceFilename string `json:"source_filename,omitempty"`
	TargetFilename string `json:"target_filename,omitempty"`
	Stage          string `json:"stage"`
	Message        string `json:"message"`
}

type ApplySummary struct {
	Version        int             `json:"version"`
	AttemptID      string          `json:"attempt_id"`
	RunID          string          `json:"run_id"`
	PlanPath       string          `json:"plan_path"`
	PlanSHA256     string          `json:"plan_sha256"`
	EvidencePath   string          `json:"evidence_path"`
	SummaryPath    string          `json:"summary_path"`
	AttemptsPath   string          `json:"attempts_path"`
	CurrentStage   string          `json:"current_stage,omitempty"`
	FailedStage    string          `json:"failed_stage,omitempty"`
	FinalStatus    string          `json:"final_status"`
	DryRun         bool            `json:"dry_run"`
	Counts         ApplyCounts     `json:"counts"`
	NeedsAttention bool            `json:"needs_attention"`
	NeedsExternal  bool            `json:"needs_external"`
	NextSafeAction string          `json:"next_safe_action"`
	Artifacts      []string        `json:"artifacts"`
	Errors         []ApplyRowError `json:"errors"`
	StartedAt      string          `json:"started_at"`
	UpdatedAt      string          `json:"updated_at"`
	DurationMS     int64           `json:"duration_ms"`
}

type ApplyAttempt struct {
	Version          int             `json:"version"`
	AttemptID        string          `json:"attempt_id"`
	RunID            string          `json:"run_id"`
	Mode             string          `json:"mode"`
	PlanPath         string          `json:"plan_path"`
	PlanSHA256       string          `json:"plan_sha256"`
	EvidencePath     string          `json:"evidence_path"`
	SummaryPath      string          `json:"summary_path"`
	AttemptsPath     string          `json:"attempts_path"`
	RequestedStores  string          `json:"requested_stores"`
	SelectedStores   []string        `json:"selected_stores"`
	StoresConfigPath string          `json:"stores_config_path,omitempty"`
	FinalStatus      string          `json:"final_status"`
	CurrentStage     string          `json:"current_stage,omitempty"`
	FailedStage      string          `json:"failed_stage,omitempty"`
	Counts           ApplyCounts     `json:"counts"`
	NeedsAttention   bool            `json:"needs_attention"`
	NeedsExternal    bool            `json:"needs_external"`
	NextSafeAction   string          `json:"next_safe_action"`
	Errors           []ApplyRowError `json:"errors"`
	StartedAt        string          `json:"started_at"`
	FinishedAt       string          `json:"finished_at"`
	DurationMS       int64           `json:"duration_ms"`
}
