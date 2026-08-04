package mediasync

import (
	"strings"
	"testing"
)

func TestBuildPlanResolvesRenamedUploadResourceByTargetFilename(t *testing.T) {
	plan := BuildPlan(PlanInput{
		RunID:  "rename-target-resource",
		Stores: []Store{{ID: "store-us"}},
		Desired: DesiredManifest{Rows: []DesiredRow{{
			RowNo:           "1",
			SourceFilename:  "raw.png",
			TargetFilename:  "renamed.png",
			FilenameChanged: true,
		}}},
		Resources: ResourceIndex{Files: map[string]ResourceInfo{
			"renamed.png": {Filename: "renamed.png", Path: "/package/renamed.png", SHA256: "target-sha"},
		}},
		Template: "product.example",
	})

	change := singlePlanChange(t, plan)
	if change.Status != "planned" {
		t.Fatalf("renamed source resource should be planned, got %+v", change)
	}
	if change.Resource == nil || change.Resource.Filename != "renamed.png" {
		t.Fatalf("expected target resource renamed.png, got %+v", change.Resource)
	}
	if change.TargetFilename != "renamed.png" {
		t.Fatalf("Shopify target filename changed: %q", change.TargetFilename)
	}
	if got := strings.Join(change.Actions, ","); got != "file_upload_new_filename,json_replace_needed" {
		t.Fatalf("unexpected rename actions: %s", got)
	}
}

func TestBuildPlanRenamedUploadRejectsMissingTargetEvenWhenSourceExists(t *testing.T) {
	plan := BuildPlan(PlanInput{
		Stores: []Store{{ID: "store-us"}},
		Desired: DesiredManifest{Rows: []DesiredRow{{
			RowNo:           "1",
			SourceFilename:  "raw.png",
			TargetFilename:  "renamed.png",
			FilenameChanged: true,
		}}},
		Resources: ResourceIndex{Files: map[string]ResourceInfo{
			"raw.png": {Filename: "raw.png", Path: "/package/raw.png"},
		}},
	})

	change := singlePlanChange(t, plan)
	if change.Status != "error" || strings.Join(change.Actions, ",") != "error" {
		t.Fatalf("missing target must be a plan error, got %+v", change)
	}
	if !strings.Contains(change.Reason, "target 图片不存在") {
		t.Fatalf("unexpected missing-target reason: %q", change.Reason)
	}
}

func TestBuildPlanRenamedUploadChecksDuplicateTargetFilename(t *testing.T) {
	plan := BuildPlan(PlanInput{
		Stores: []Store{{ID: "store-us"}},
		Desired: DesiredManifest{Rows: []DesiredRow{{
			RowNo:           "1",
			SourceFilename:  "raw.png",
			TargetFilename:  "renamed.png",
			FilenameChanged: true,
		}}},
		Resources: ResourceIndex{
			Files: map[string]ResourceInfo{
				"raw.png": {Filename: "raw.png", Path: "/package/raw.png"},
			},
			Duplicates: map[string][]string{
				"renamed.png": {"/package/a/renamed.png", "/package/b/renamed.png"},
			},
		},
	})

	change := singlePlanChange(t, plan)
	if change.Status != "error" || strings.Join(change.Actions, ",") != "error" {
		t.Fatalf("duplicate target must be a plan error, got %+v", change)
	}
	if !strings.Contains(change.Reason, "renamed.png") {
		t.Fatalf("duplicate check used the wrong filename: %q", change.Reason)
	}
}

func TestBuildPlanSameFilenameStillResolvesPackageResource(t *testing.T) {
	plan := BuildPlan(PlanInput{
		Stores: []Store{{ID: "store-us"}},
		Desired: DesiredManifest{Rows: []DesiredRow{{
			RowNo:          "1",
			SourceFilename: "same.png",
			TargetFilename: "same.png",
		}}},
		Resources: ResourceIndex{Files: map[string]ResourceInfo{
			"same.png": {Filename: "same.png", Path: "/package/same.png"},
		}},
	})

	change := singlePlanChange(t, plan)
	if change.Status != "planned" || change.Resource == nil || change.Resource.Filename != "same.png" {
		t.Fatalf("same-name resource lookup regressed: %+v", change)
	}
	if got := strings.Join(change.Actions, ","); got != "file_upload_same_filename" {
		t.Fatalf("unexpected same-name actions: %s", got)
	}
}

func TestBuildPlanRejectsDuplicateStoreTargetFilename(t *testing.T) {
	plan := BuildPlan(PlanInput{
		Stores: []Store{{ID: "store-us"}},
		Desired: DesiredManifest{Rows: []DesiredRow{
			{RowNo: "1", SourceFilename: "first.png", TargetFilename: "same.png"},
			{RowNo: "2", SourceFilename: "second.png", TargetFilename: "same.png"},
		}},
		Resources: ResourceIndex{Files: map[string]ResourceInfo{
			"same.png": {Filename: "same.png", Path: "/package/same.png"},
		}},
	})

	if len(plan.Changes) != 2 {
		t.Fatalf("expected both input rows to remain auditable, got %d", len(plan.Changes))
	}
	if plan.Changes[0].Status != "planned" {
		t.Fatalf("first target occurrence should remain planned: %+v", plan.Changes[0])
	}
	duplicate := plan.Changes[1]
	if duplicate.Status != "error" || strings.Join(duplicate.Actions, ",") != "error" || duplicate.NeedsExecute {
		t.Fatalf("duplicate target must be a non-executable plan error: %+v", duplicate)
	}
	if !strings.Contains(duplicate.Reason, "target_filename") || !strings.Contains(duplicate.Reason, "row=1") {
		t.Fatalf("duplicate error must identify the target and first row: %q", duplicate.Reason)
	}
	if plan.Summary.Errors != 1 {
		t.Fatalf("duplicate target must block plan execution, summary=%+v", plan.Summary)
	}
}

func singlePlanChange(t *testing.T, plan Plan) PlanChange {
	t.Helper()
	if len(plan.Changes) != 1 {
		t.Fatalf("expected one plan change, got %d", len(plan.Changes))
	}
	return plan.Changes[0]
}
