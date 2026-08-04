package runstate

import (
	"encoding/json"
	"testing"
)

func TestEvidenceRecordReadsLegacyCamelCaseWithoutPromotingFailure(t *testing.T) {
	var record EvidenceRecord
	if err := json.Unmarshal([]byte(`{"fileId":"gid://shopify/MediaImage/1","mediaGid":"gid://shopify/MediaImage/1","requestedFilename":"source.png","filename":"target.png","status":"ERROR","last_error":"failed"}`), &record); err != nil {
		t.Fatal(err)
	}
	if record.FileID == "" || record.TargetFilename != "target.png" || record.Status != "ERROR" || record.Upload.Status == "SUCCEEDED" {
		t.Fatalf("legacy evidence was misread: %#v", record)
	}
}

func TestEvidenceUpsertKeepsRowsWithSameFilenameIndependent(t *testing.T) {
	evidence := Evidence{}
	evidence.Upsert(EvidenceRecord{Store: "store-a", RowNo: "1", TargetFilename: "same.png", Status: "READY"})
	evidence.Upsert(EvidenceRecord{Store: "store-a", RowNo: "2", TargetFilename: "same.png", Status: "ERROR"})
	if len(evidence.Results) != 2 {
		t.Fatalf("expected two row records, got %#v", evidence.Results)
	}
}
