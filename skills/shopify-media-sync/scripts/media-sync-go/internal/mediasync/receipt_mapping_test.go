package mediasync

import "testing"

func receiptForMappings(version int, verified bool, mappings []jsonReplacementMappingResult) *jsonReplacementReceipt {
	return &jsonReplacementReceipt{
		Version:     version,
		Verified:    verified,
		Template:    "product.example",
		TargetTheme: "staging-theme",
		Results: []struct {
			Store string `json:"store"`
			OK    bool   `json:"ok"`
		}{{Store: "store-us", OK: true}},
		MappingResults: mappings,
	}
}

func jsonReplacementChange(source, target string) PlanChange {
	return PlanChange{
		Store:          "store-us",
		Template:       "product.example",
		TargetTheme:    "staging-theme",
		SourceFilename: source,
		TargetFilename: target,
		Actions:        []string{"json_replace_needed"},
	}
}

func TestJSONReceiptConfirmsEveryMappingAndFailsClosed(t *testing.T) {
	receipt := receiptForMappings(2, true, []jsonReplacementMappingResult{
		{SourceFilename: "matched.jpg", TargetFilename: "matched.webp", ReplacementCount: 2, Matched: true},
		{SourceFilename: "unmatched.jpg", TargetFilename: "unmatched.webp", ReplacementCount: 0, Matched: false},
	})
	if jsonReceiptConfirmsChange(receipt, jsonReplacementChange("matched.jpg", "matched.webp")) {
		t.Fatal("receipt with an unmatched mapping must not confirm any JSON replacement")
	}
	if jsonReceiptConfirmsChange(receipt, jsonReplacementChange("unmatched.jpg", "unmatched.webp")) {
		t.Fatal("unmatched mapping must not be confirmed")
	}
	if jsonReceiptConfirmsChange(receiptForMappings(1, true, receipt.MappingResults), jsonReplacementChange("matched.jpg", "matched.webp")) {
		t.Fatal("version 1 receipt must fail closed for JSON replacement")
	}
	matchedOnly := receiptForMappings(2, true, []jsonReplacementMappingResult{
		{SourceFilename: "matched.jpg", TargetFilename: "matched.webp", ReplacementCount: 2, Matched: true},
	})
	if !jsonReceiptConfirmsChange(matchedOnly, jsonReplacementChange("matched.jpg", "matched.webp")) {
		t.Fatal("complete v2 receipt should confirm matching JSON replacement")
	}
}

func TestJSONReceiptDoesNotClearUploadEvidenceForOrdinaryFileChange(t *testing.T) {
	change := PlanChange{Actions: []string{"file_upload_new_filename"}}
	record := EvidenceRecord{Status: "ERROR", LastError: "缺少成功上传 evidence，不能验证文件写入结果"}
	if jsonReceiptConfirmsChange(receiptForMappings(2, true, nil), change) {
		clearResolvedJSONReplacementError(&record)
	}
	if record.Status != "ERROR" || record.LastError == "" {
		t.Fatalf("ordinary file change unexpectedly cleared upload evidence error: %#v", record)
	}
}
