package app

import (
	"path/filepath"
	"reflect"
	"testing"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/syncengine"
)

func TestParsePlanOptions_PreservesExplicitTargetOrder(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	options, err := parsePlanOptions([]string{
		"--template", "product.lp4",
		"--source-store", "store-source",
		"--source-theme", "Source Theme",
		"--target-store", "store-one",
		"--target-theme", "101",
		"--target-store", "store-two.myshopify.com",
		"--target-theme", "Target Two",
		"--mode", "field",
		"--section", "spring_percent_four_product_Adefr6",
		"--field", "custom_css",
		"--allow-remove",
		"--runtime-root", "run-output",
	}, caller)
	if err != nil {
		t.Fatalf("parsePlanOptions(): %v", err)
	}
	wantTargets := []EndpointRef{
		{Store: adapter.StoreRef("store-one"), Theme: adapter.ThemeRef("101")},
		{Store: adapter.StoreRef("store-two.myshopify.com"), Theme: adapter.ThemeRef("Target Two")},
	}
	if !reflect.DeepEqual(options.Targets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", options.Targets, wantTargets)
	}
	if options.Template != "product.lp4" {
		t.Fatalf("template = %q, want product.lp4", options.Template)
	}
	wantScope := syncengine.Scope{
		Mode:       syncengine.ModeField,
		SectionKey: "spring_percent_four_product_Adefr6",
		Field:      "custom_css",
	}
	if options.Scope != wantScope {
		t.Fatalf("scope = %#v, want %#v", options.Scope, wantScope)
	}
	if !options.AllowRemove {
		t.Fatal("allow remove = false, want true")
	}
	if options.RuntimeRoot != filepath.Join(caller, "run-output") {
		t.Fatalf("runtime root = %q, want caller-relative path", options.RuntimeRoot)
	}
}

func TestParsePlanOptions_PageAlias(t *testing.T) {
	t.Parallel()

	options, err := parsePlanOptions([]string{
		"--page", "example",
		"--source-store", "store-source",
		"--source-theme", "101",
		"--target-store", "store-target",
		"--target-theme", "202",
	}, t.TempDir())
	if err != nil {
		t.Fatalf("parsePlanOptions(): %v", err)
	}
	if options.Template != "page.example" || len(options.Warnings) != 1 {
		t.Fatalf("options = %#v, want page alias and one warning", options)
	}
}

func TestParsePlanOptions_RejectsInvalidScopeAndEndpoints(t *testing.T) {
	t.Parallel()

	base := []string{
		"--template", "page.example",
		"--source-store", "store-source",
		"--source-theme", "101",
		"--target-store", "store-target",
		"--target-theme", "202",
	}
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing source store", args: []string{"--template", "page.example", "--source-theme", "101", "--target-store", "store-target", "--target-theme", "202"}},
		{name: "missing source theme", args: []string{"--template", "page.example", "--source-store", "store-source", "--target-store", "store-target", "--target-theme", "202"}},
		{name: "missing target", args: base[:6]},
		{name: "target count mismatch", args: append(append([]string{}, base...), "--target-store", "store-extra")},
		{name: "both template entrypoints", args: append(append([]string{}, base...), "--page", "example")},
		{name: "section without key", args: append(append([]string{}, base...), "--mode", "section")},
		{name: "field without section", args: append(append([]string{}, base...), "--mode", "field", "--field", "custom_css")},
		{name: "field not allowed", args: append(append([]string{}, base...), "--mode", "field", "--section", "hero", "--field", "settings")},
		{name: "template mode with section", args: append(append([]string{}, base...), "--section", "hero")},
		{name: "unknown mode", args: append(append([]string{}, base...), "--mode", "pointer")},
		{name: "positional argument", args: append(append([]string{}, base...), "extra")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parsePlanOptions(test.args, t.TempDir()); err == nil {
				t.Fatal("parsePlanOptions() error = nil")
			}
		})
	}
}

func TestParseApplyOptions(t *testing.T) {
	t.Parallel()

	caller := t.TempDir()
	options, err := parseApplyOptions(
		[]string{"--plan", "run/plan.json", "--execute", "--allow-remove"},
		caller,
	)
	if err != nil {
		t.Fatalf("parseApplyOptions(): %v", err)
	}
	if options.PlanPath != filepath.Join(caller, "run", "plan.json") {
		t.Fatalf("plan path = %q, want caller-relative absolute path", options.PlanPath)
	}
	if !options.Execute || !options.AllowRemove {
		t.Fatalf("options = %#v, want execute and allow-remove", options)
	}

	for _, args := range [][]string{
		{},
		{"--plan", ""},
		{"--plan", "run/plan.json", "extra"},
		{"--unknown"},
	} {
		if _, err := parseApplyOptions(args, caller); err == nil {
			t.Fatalf("parseApplyOptions(%v) error = nil", args)
		}
	}
}
