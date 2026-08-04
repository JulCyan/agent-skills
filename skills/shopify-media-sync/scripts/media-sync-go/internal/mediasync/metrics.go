package mediasync

import (
	"path/filepath"
	"time"
)

type RunMetrics struct {
	RunID               string         `json:"run_id,omitempty"`
	Command             string         `json:"command"`
	StartedAt           string         `json:"started_at"`
	FinishedAt          string         `json:"finished_at"`
	DurationMS          int64          `json:"duration_ms"`
	Stores              []string       `json:"stores,omitempty"`
	Rows                int            `json:"rows"`
	Actions             map[string]int `json:"actions,omitempty"`
	Statuses            map[string]int `json:"statuses,omitempty"`
	Success             int            `json:"success"`
	Errors              int            `json:"errors"`
	Skipped             int            `json:"skipped"`
	SkippedTranslations int            `json:"skipped_translations"`
	// Concurrency is retained for existing consumers and always matches the
	// effective value. RequestedConcurrency captures the user flag verbatim.
	Concurrency          int  `json:"concurrency"`
	RequestedConcurrency int  `json:"requested_concurrency"`
	EffectiveConcurrency int  `json:"effective_concurrency"`
	DryRun               bool `json:"dry_run,omitempty"`
}

func newRunMetrics(command, runID string, started time.Time, stores []string, rows int, changes []PlanChange, concurrency int, dryRun bool) RunMetrics {
	effectiveConcurrency := effectiveConcurrency(command, concurrency, len(changes))
	return RunMetrics{
		RunID:                runID,
		Command:              command,
		StartedAt:            started.Format(time.RFC3339),
		Stores:               append([]string(nil), stores...),
		Rows:                 rows,
		Actions:              metricActionCounts(command, changes),
		Statuses:             map[string]int{},
		Concurrency:          effectiveConcurrency,
		RequestedConcurrency: concurrency,
		EffectiveConcurrency: effectiveConcurrency,
		DryRun:               dryRun,
	}
}

func writeMetricsFile(path string, metrics *RunMetrics) error {
	finished := time.Now().UTC()
	metrics.FinishedAt = finished.Format(time.RFC3339)
	if started, err := time.Parse(time.RFC3339, metrics.StartedAt); err == nil {
		metrics.DurationMS = finished.Sub(started).Milliseconds()
	}
	if err := writeJSON(path, metrics); err != nil {
		return err
	}
	return writeJSON(filepath.Join(filepath.Dir(path), "metrics."+metrics.Command+".json"), metrics)
}

func effectiveConcurrency(command string, requested, changes int) int {
	if command != "upload" {
		return 1
	}
	if changes < requested {
		return changes
	}
	return requested
}

func resolveMetricsPath(opts commandOptions, evidencePath string) string {
	if opts.outDir != "" {
		return filepath.Join(filepath.Clean(opts.outDir), "metrics.json")
	}
	return filepath.Join(filepath.Dir(evidencePath), "metrics.json")
}

func actionCounts(changes []PlanChange) map[string]int {
	return actionCountsFiltered(changes, nil)
}

func metricActionCounts(command string, changes []PlanChange) map[string]int {
	switch command {
	case "upload":
		return actionCountsFiltered(changes, map[string]bool{
			"file_upload_same_filename":  true,
			"file_replace_same_filename": true,
			"file_upload_new_filename":   true,
		})
	case "alt":
		return actionCountsFiltered(changes, map[string]bool{
			"alt_update":         true,
			"translation_update": true,
		})
	default:
		return actionCounts(changes)
	}
}

func actionCountsFiltered(changes []PlanChange, allowed map[string]bool) map[string]int {
	counts := map[string]int{}
	for _, change := range changes {
		for _, action := range change.Actions {
			if allowed != nil && !allowed[action] {
				continue
			}
			counts[action]++
		}
	}
	return counts
}

func updateMetricsFromChanges(metrics *RunMetrics, changes []PlanChange) {
	statuses := map[string]int{}
	for _, change := range changes {
		status := change.Status
		if status == "" {
			status = "planned"
		}
		statuses[status]++
	}
	metrics.Statuses = statuses
	metrics.Success = statuses["READY"]
	metrics.Errors = statuses["ERROR"] + statuses["error"]
	metrics.Skipped = statuses["SKIPPED"] + statuses["skipped"]
}

func updateMetricsFromEvidence(metrics *RunMetrics, evidence Evidence, changes []PlanChange) {
	statuses := map[string]int{}
	skippedTranslations := 0
	for _, change := range changes {
		record := evidence.Find(change.Store, DesiredRow{RowNo: change.RowNo, TargetFilename: change.TargetFilename})
		status := "MISSING"
		if record != nil {
			status = record.Status
			if status == "" {
				status = "UNKNOWN"
			}
			for _, readback := range record.TranslationReadback {
				if readback.Status == "SKIPPED" {
					skippedTranslations++
				}
			}
		}
		statuses[status]++
	}
	metrics.Statuses = statuses
	metrics.Success = statuses["READY"]
	metrics.Errors = statuses["ERROR"] + statuses["MISSING"]
	metrics.Skipped = statuses["SKIPPED"]
	metrics.SkippedTranslations = skippedTranslations
}
