package syncengine

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestCompare_ReportsStableStructuralAndOrderChanges(t *testing.T) {
	t.Parallel()

	before := mustDocument(t, `{
  "sections":{
    "a":{"value":1},
    "b":{"value":1,"gone":true},
    "d":{}
  },
  "order":["a","b","d"],
  "top_removed":true,
  "top_changed":"before"
}`)
	planned := mustDocument(t, `{
  "sections":{
    "a":{"value":2},
    "c":{"new":true},
    "d":{}
  },
  "order":["c","a","d"],
  "top_added":true,
  "top_changed":"after"
}`)

	first, err := Compare(before, planned)
	if err != nil {
		t.Fatalf("Compare(): %v", err)
	}
	second, err := Compare(before, planned)
	if err != nil {
		t.Fatalf("Compare() again: %v", err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshaling first diff: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshaling second diff: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("diff is unstable:\n%s\n%s", firstJSON, secondJSON)
	}

	if got, want := changePaths(first.Added), []string{"sections.c", "top_added"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("added paths = %v, want %v", got, want)
	}
	if got, want := changePaths(first.Removed), []string{"sections.b", "top_removed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed paths = %v, want %v", got, want)
	}
	if got, want := changePaths(first.Changed), []string{"sections.a.value", "top_changed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %v, want %v", got, want)
	}
	if !first.Order.Changed {
		t.Fatal("order changed = false, want true")
	}
	if got, want := first.Order.Added, []string{"c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order added = %v, want %v", got, want)
	}
	if got, want := first.Order.Removed, []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order removed = %v, want %v", got, want)
	}
	if got, want := first.Order.Moved, []OrderMove{{SectionKey: "a", From: 0, To: 1}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order moved = %v, want %v", got, want)
	}
	wantSections := SectionSummary{
		Added:   []string{"c"},
		Removed: []string{"b"},
		Changed: []string{"a"},
	}
	if !reflect.DeepEqual(first.Sections, wantSections) {
		t.Fatalf("sections = %#v, want %#v", first.Sections, wantSections)
	}
	if !first.HasRemoval() {
		t.Fatal("HasRemoval() = false, want true")
	}
}

func TestCompare_NoOpIgnoresHeaderAndFormatting(t *testing.T) {
	t.Parallel()

	before := mustDocument(t, `/* first */
{"sections":{"hero":{"value":1}},"order":["hero"],"unknown":true}`)
	planned := mustDocument(t, `// second
{ "unknown": true, "order": ["hero"], "sections": {"hero":{"value":1}} }`)

	diff, err := Compare(before, planned)
	if err != nil {
		t.Fatalf("Compare(): %v", err)
	}
	if !diff.Empty() {
		encoded, _ := json.Marshal(diff)
		t.Fatalf("Empty() = false, diff = %s", encoded)
	}
	if diff.HasRemoval() {
		t.Fatal("HasRemoval() = true, want false")
	}
}

func TestCompare_CustomCSSShorteningIsRemoval(t *testing.T) {
	t.Parallel()

	before := mustDocument(t, `{
  "sections":{"hero":{"custom_css":["one","two"]}},
  "order":["hero"]
}`)
	planned := mustDocument(t, `{
  "sections":{"hero":{"custom_css":["one"]}},
  "order":["hero"]
}`)

	diff, err := Compare(before, planned)
	if err != nil {
		t.Fatalf("Compare(): %v", err)
	}
	wantPath := "sections.hero.custom_css[1]"
	if got := changePaths(diff.Removed); !reflect.DeepEqual(got, []string{wantPath}) {
		t.Fatalf("removed paths = %v, want [%s]", got, wantPath)
	}
	if !diff.HasRemoval() {
		t.Fatal("HasRemoval() = false, want true")
	}
}

func TestCompare_ContainerTypeReplacementReportsRemovedContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		before      string
		after       string
		removedPath string
	}{
		{
			name:        "object to null",
			before:      `{"sections":{"hero":{"settings":{"legacy":1}}},"order":["hero"]}`,
			after:       `{"sections":{"hero":{"settings":null}},"order":["hero"]}`,
			removedPath: "sections.hero.settings.legacy",
		},
		{
			name:        "array to scalar",
			before:      `{"sections":{"hero":{"blocks":[{"id":"one"}]}},"order":["hero"]}`,
			after:       `{"sections":{"hero":{"blocks":"replaced"}},"order":["hero"]}`,
			removedPath: "sections.hero.blocks[0].id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := mustDocument(t, test.before)
			after := mustDocument(t, test.after)
			diff, err := Compare(before, after)
			if err != nil {
				t.Fatalf("Compare(): %v", err)
			}
			if !diff.HasRemoval() {
				t.Fatal("HasRemoval() = false, want true")
			}
			paths := make([]string, 0, len(diff.Removed))
			for _, change := range diff.Removed {
				paths = append(paths, change.Path)
			}
			if !slices.Contains(paths, test.removedPath) {
				t.Fatalf("removed paths = %v, want %q", paths, test.removedPath)
			}
		})
	}
}

func TestCompare_RejectsInvalidDocument(t *testing.T) {
	t.Parallel()

	valid := mustDocument(t, `{"sections":{},"order":[]}`)
	invalid := valid
	invalid.Root["order"] = "bad"
	if _, err := Compare(invalid, valid); err == nil {
		t.Fatal("Compare() error = nil")
	}
}

func changePaths(changes []Change) []string {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	return paths
}
