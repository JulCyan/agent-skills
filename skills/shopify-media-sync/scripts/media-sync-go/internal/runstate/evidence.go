package runstate

import (
	"encoding/json"
	"os"
	"strings"
)

func (r *EvidenceRecord) UnmarshalJSON(raw []byte) error {
	type Alias EvidenceRecord
	var aux struct {
		Alias
		FileIDCamel     string `json:"fileId"`
		MediaGIDCamel   string `json:"mediaGid"`
		CDNURLCamel     string `json:"cdnUrl"`
		SHA256Camel     string `json:"sha256"`
		FileStatusCamel string `json:"fileStatus"`
		RequestedName   string `json:"requestedFilename"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return err
	}
	*r = EvidenceRecord(aux.Alias)
	if r.FileID == "" {
		r.FileID = aux.FileIDCamel
	}
	if r.MediaGID == "" {
		r.MediaGID = aux.MediaGIDCamel
	}
	if r.CDNURL == "" {
		r.CDNURL = aux.CDNURLCamel
	}
	if r.SHA256 == "" {
		r.SHA256 = aux.SHA256Camel
	}
	if r.FileStatus == "" {
		r.FileStatus = aux.FileStatusCamel
	}
	if r.TargetFilename == "" && r.Filename != "" {
		r.TargetFilename = r.Filename
	}
	if r.SourceFilename == "" && aux.RequestedName != "" {
		r.SourceFilename = aux.RequestedName
	}
	return nil
}

func LoadEvidence(path string) (Evidence, error) {
	if path == "" {
		return Evidence{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Evidence{}, err
	}
	var evidence Evidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return Evidence{}, err
	}
	if len(evidence.Results) == 0 && len(evidence.Rows) > 0 {
		evidence.Results = evidence.Rows
	}
	for i := range evidence.Results {
		if evidence.Results[i].Store == "" {
			evidence.Results[i].Store = evidence.Store
		}
	}
	return evidence, nil
}

func (e Evidence) Find(storeID string, row DesiredRow) *EvidenceRecord {
	for i := range e.Results {
		record := e.Results[i]
		if record.Store != "" && record.Store != storeID {
			continue
		}
		if record.RowNo != "" && record.RowNo == row.RowNo && evidenceRecordMatchesRowFilename(record, row) {
			return &e.Results[i]
		}
	}
	for i := range e.Results {
		record := e.Results[i]
		if record.Store != "" && record.Store != storeID {
			continue
		}
		if (record.RowNo == "" || row.RowNo == "") && evidenceRecordMatchesRowFilename(record, row) {
			return &e.Results[i]
		}
	}
	return nil
}

func (e *Evidence) Upsert(record EvidenceRecord) {
	for i := range e.Results {
		existing := e.Results[i]
		if existing.Store == record.Store && existing.RowNo != "" && existing.RowNo == record.RowNo && evidenceRecordsShareFilename(existing, record) {
			e.Results[i] = MergeEvidenceRecord(existing, record)
			return
		}
	}
	for i := range e.Results {
		existing := e.Results[i]
		if existing.Store == record.Store && (existing.RowNo == "" || record.RowNo == "") && evidenceRecordsShareFilename(existing, record) {
			e.Results[i] = MergeEvidenceRecord(existing, record)
			return
		}
	}
	e.Results = append(e.Results, record)
}

func evidenceRecordMatchesRowFilename(record EvidenceRecord, row DesiredRow) bool {
	return evidenceRecordHasFilename(record, desiredEvidenceFilename(row))
}
func evidenceRecordsShareFilename(existing, next EvidenceRecord) bool {
	return evidenceRecordHasFilename(existing, evidenceRecordFilename(next))
}
func evidenceRecordHasFilename(record EvidenceRecord, filename string) bool {
	filename = strings.TrimSpace(filename)
	return filename != "" && (strings.TrimSpace(record.TargetFilename) == filename || strings.TrimSpace(record.Filename) == filename)
}
func desiredEvidenceFilename(row DesiredRow) string {
	if target := strings.TrimSpace(row.TargetFilename); target != "" {
		return target
	}
	return strings.TrimSpace(row.SourceFilename)
}
func evidenceRecordFilename(record EvidenceRecord) string {
	if target := strings.TrimSpace(record.TargetFilename); target != "" {
		return target
	}
	return strings.TrimSpace(record.Filename)
}
func MergeEvidenceRecord(existing, next EvidenceRecord) EvidenceRecord {
	rawExisting, _ := json.Marshal(existing)
	rawNext, _ := json.Marshal(next)
	var merged map[string]any
	_ = json.Unmarshal(rawExisting, &merged)
	var updates map[string]any
	_ = json.Unmarshal(rawNext, &updates)
	for key, value := range updates {
		if !shouldSkipEvidenceMergeValue(key, value, next) {
			merged[key] = value
		}
	}
	if evidenceRecordClearsLastError(next) {
		merged["last_error"] = ""
	}
	rawMerged, _ := json.Marshal(merged)
	var out EvidenceRecord
	_ = json.Unmarshal(rawMerged, &out)
	return out
}
func shouldSkipEvidenceMergeValue(key string, value any, record EvidenceRecord) bool {
	if key == "last_error" && evidenceRecordClearsLastError(record) {
		return false
	}
	return isZeroJSONValue(value)
}
func evidenceRecordClearsLastError(record EvidenceRecord) bool {
	return strings.EqualFold(record.Status, "READY") && record.LastError == ""
}
func isZeroJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case float64:
		return typed == 0
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}
