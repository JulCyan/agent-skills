package mediasync

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func BuildPlan(input PlanInput) Plan {
	plan := Plan{
		RunID:       input.RunID,
		GeneratedAt: input.GeneratedAt,
		Template:    input.Template,
		TargetTheme: input.TargetTheme,
		Desired:     input.Desired,
		Resources:   input.Resources,
		Summary: PlanSummary{
			ByAction: map[string]int{},
			Rows:     len(input.Desired.Rows),
		},
	}
	for _, store := range input.Stores {
		plan.Summary.Stores = append(plan.Summary.Stores, store.ID)
		firstRowByTarget := map[string]string{}
		for _, row := range input.Desired.Rows {
			row = desiredRowForStore(row, store)
			change := buildPlanChange(store, row, input)
			targetKey := strings.ToLower(strings.TrimSpace(uploadResourceFilename(row)))
			if firstRow, duplicate := firstRowByTarget[targetKey]; targetKey != "" && duplicate {
				change.Status = "error"
				change.Actions = []string{"error"}
				change.NeedsExecute = false
				change.Reason = fmt.Sprintf("输入中 store=%s target_filename=%q 重复；首次 row=%s", store.ID, uploadResourceFilename(row), firstRow)
			} else if targetKey != "" {
				firstRowByTarget[targetKey] = row.RowNo
			}
			plan.Changes = append(plan.Changes, change)
			for _, action := range change.Actions {
				plan.Summary.ByAction[action]++
			}
			if change.Status == "error" {
				plan.Summary.Errors++
			}
			if change.NeedsExecute {
				plan.Summary.NeedsExecute++
			}
			if change.FilenameChanged {
				plan.Summary.FilenameChanged++
			}
		}
	}
	plan.Summary.Changes = len(plan.Changes)
	return plan
}

func desiredRowForStore(row DesiredRow, store Store) DesiredRow {
	out := row
	if len(row.Translations) > 0 {
		out.Translations = make(map[string]string, len(row.Translations))
		for locale, value := range row.Translations {
			out.Translations[locale] = value
		}
	}
	primaryLocale := normalizeLocale(store.PrimaryLocale)
	if primaryLocale == "" {
		primaryLocale = "en"
	}
	if out.Alt == "" && primaryLocale != "" && len(out.Translations) > 0 {
		if value := strings.TrimSpace(out.Translations[primaryLocale]); value != "" {
			out.Alt = value
			delete(out.Translations, primaryLocale)
		}
	}
	if len(out.Translations) == 0 {
		out.Translations = nil
	}
	return out
}

func InitialEvidenceFromPlan(runID string, plan Plan) Evidence {
	evidence := Evidence{RunID: runID, Results: []EvidenceRecord{}}
	for _, change := range plan.Changes {
		if change.Evidence == nil {
			continue
		}
		record := *change.Evidence
		if record.Store == "" {
			record.Store = change.Store
		}
		if record.RowNo == "" {
			record.RowNo = change.RowNo
		}
		if record.SourceFilename == "" {
			record.SourceFilename = change.SourceFilename
		}
		if record.TargetFilename == "" {
			record.TargetFilename = change.TargetFilename
		}
		if record.Filename == "" {
			record.Filename = record.TargetFilename
		}
		evidence.Upsert(record)
	}
	return evidence
}

func buildPlanChange(store Store, row DesiredRow, input PlanInput) PlanChange {
	change := PlanChange{
		Store:           store.ID,
		RowNo:           row.RowNo,
		SourceFilename:  row.SourceFilename,
		TargetFilename:  row.TargetFilename,
		FilenameChanged: row.FilenameChanged,
		Alt:             row.Alt,
		Translations:    row.Translations,
		Status:          "planned",
		Template:        input.Template,
		TargetTheme:     input.TargetTheme,
	}
	resourceFilename := uploadResourceFilename(row)
	if duplicatePaths, ok := input.Resources.Duplicates[resourceFilename]; ok {
		change.Status = "error"
		change.Actions = []string{"error"}
		change.Reason = "资源包中存在重复上传图片名: " + strings.Join(duplicatePaths, ", ")
		return change
	}
	ev := input.Evidence.Find(store.ID, row)
	if ev != nil {
		change.Evidence = ev
	}
	if resource, ok := input.Resources.Files[resourceFilename]; ok {
		change.Resource = &resource
		if ev != nil && ev.SHA256 != "" && ev.SHA256 == resource.SHA256 {
			change.Actions = append(change.Actions, "verify")
			change.Reason = "sha256 unchanged; skip binary upload"
		} else if row.FilenameChanged {
			change.Actions = append(change.Actions, "file_upload_new_filename")
		} else if ev != nil && (ev.MediaGID != "" || ev.FileID != "") {
			change.Actions = append(change.Actions, "file_replace_same_filename")
		} else {
			change.Actions = append(change.Actions, "file_upload_same_filename")
		}
		appendAltActions(&change, row)
		appendJSONAction(&change, row, input)
		change.NeedsExecute = hasWriteAction(change.Actions)
		return change
	}
	if ev != nil && (ev.MediaGID != "" || ev.FileID != "" || ev.CDNURL != "") {
		appendAltActions(&change, row)
		if len(change.Actions) == 0 {
			change.Actions = append(change.Actions, "verify")
			change.Reason = "target missing; use previous evidence for verify"
		} else {
			change.Reason = "target missing; use previous evidence for alt-only update"
		}
		appendJSONAction(&change, row, input)
		change.NeedsExecute = hasWriteAction(change.Actions)
		return change
	}
	change.Status = "error"
	change.Actions = []string{"error"}
	change.Reason = "target 图片不存在，且没有可复用 evidence"
	return change
}

func uploadResourceFilename(row DesiredRow) string {
	// SourceFilename identifies the existing JSON/Files reference to replace.
	// The resource package supplies TargetFilename, which is also the name
	// requested from Shopify for the replacement file.
	if target := strings.TrimSpace(row.TargetFilename); target != "" {
		return target
	}
	return strings.TrimSpace(row.SourceFilename)
}

func appendAltActions(change *PlanChange, row DesiredRow) {
	if row.Alt != "" {
		change.Actions = append(change.Actions, "alt_update")
	}
	if len(row.Translations) > 0 {
		change.Actions = append(change.Actions, "translation_update")
	}
}

func appendJSONAction(change *PlanChange, row DesiredRow, input PlanInput) {
	if row.FilenameChanged && input.Template != "" {
		change.Actions = append(change.Actions, "json_replace_needed")
	}
}

func hasWriteAction(actions []string) bool {
	for _, action := range actions {
		switch action {
		case "file_upload_same_filename", "file_replace_same_filename", "file_upload_new_filename", "alt_update", "translation_update", "json_replace_needed":
			return true
		}
	}
	return false
}

func writePlanText(w io.Writer, runDir string, plan Plan) error {
	fmt.Fprintf(w, "run_id=%s\n", plan.RunID)
	fmt.Fprintf(w, "out_dir=%s\n", runDir)
	fmt.Fprintf(w, "stores=%s rows=%d changes=%d errors=%d needs_execute=%d\n",
		strings.Join(plan.Summary.Stores, ","), plan.Summary.Rows, plan.Summary.Changes, plan.Summary.Errors, plan.Summary.NeedsExecute)
	keys := make([]string, 0, len(plan.Summary.ByAction))
	for key := range plan.Summary.ByAction {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(w, "  %-26s %d\n", key, plan.Summary.ByAction[key])
	}
	for _, change := range plan.Changes {
		status := change.Status
		if status == "" {
			status = "planned"
		}
		fmt.Fprintf(w, "%-18s row=%-4s %-8s %s -> %s actions=%s",
			change.Store, change.RowNo, status, change.SourceFilename, change.TargetFilename, strings.Join(change.Actions, ","))
		if change.Reason != "" {
			fmt.Fprintf(w, " reason=%s", change.Reason)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func WritePlanReport(path string, plan Plan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"run_id", "store", "row_no", "source图片名", "target图片文件名", "actions", "status", "sha256", "last_error"}); err != nil {
		return err
	}
	for _, change := range plan.Changes {
		sha := ""
		if change.Resource != nil {
			sha = change.Resource.SHA256
		}
		if err := writer.Write([]string{plan.RunID, change.Store, change.RowNo, change.SourceFilename, change.TargetFilename, strings.Join(change.Actions, "|"), change.Status, sha, change.Reason}); err != nil {
			return err
		}
	}
	return writer.Error()
}
