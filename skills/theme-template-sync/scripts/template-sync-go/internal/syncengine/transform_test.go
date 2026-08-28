package syncengine

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"theme-template-sync/internal/templatejson"
)

func TestTransform_TemplatePreservesCompleteSource(t *testing.T) {
	t.Parallel()

	source := mustDocument(t, `/* source */
{
  "sections":{"hero":{"type":"source","settings":{"count":2}}},
  "order":["hero"],
  "unknown_top":{"keep":"source"}
}`)
	target := mustDocument(t, `/* target */
{"sections":{"old":{"type":"old"}},"order":["old"],"target_only":true}`)

	planned, err := Transform(source, target, Scope{Mode: ModeTemplate})
	if err != nil {
		t.Fatalf("Transform(): %v", err)
	}
	assertCanonicalEqual(t, planned, source)
	if !bytes.Equal(planned.Header, source.Header) {
		t.Fatalf("planned header = %q, want source header %q", planned.Header, source.Header)
	}
}

func TestTransform_SectionReplacesOnlyTargetSection(t *testing.T) {
	t.Parallel()

	source := mustDocument(t, `{
  "sections":{"hero":{"type":"source","settings":{"title":"new"}}},
  "order":["hero"],
  "source_only":true
}`)
	target := mustDocument(t, `/* target header */
{
  "sections":{
    "hero":{"type":"target","settings":{"title":"old"}},
    "keep":{"type":"keep","blocks":[{"id":"one"}]}
  },
  "order":["keep","hero"],
  "target_only":{"nested":true}
}`)
	want := mustDocument(t, `{
  "sections":{
    "hero":{"type":"source","settings":{"title":"new"}},
    "keep":{"type":"keep","blocks":[{"id":"one"}]}
  },
  "order":["keep","hero"],
  "target_only":{"nested":true}
}`)

	planned, err := Transform(source, target, Scope{Mode: ModeSection, SectionKey: "hero"})
	if err != nil {
		t.Fatalf("Transform(): %v", err)
	}
	assertCanonicalEqual(t, planned, want)
	if !bytes.Equal(planned.Header, target.Header) {
		t.Fatalf("planned header = %q, want target header %q", planned.Header, target.Header)
	}
}

func TestTransform_SectionInsertionUsesNearestSourcePredecessor(t *testing.T) {
	t.Parallel()

	source := mustDocument(t, `{
  "sections":{"first":{},"middle":{},"wanted":{"type":"new"},"after":{}},
  "order":["first","middle","wanted","after"]
}`)
	target := mustDocument(t, `{
  "sections":{"first":{},"after":{}},
  "order":["first","after"]
}`)

	planned, err := Transform(source, target, Scope{Mode: ModeSection, SectionKey: "wanted"})
	if err != nil {
		t.Fatalf("Transform(): %v", err)
	}
	want := []string{"first", "wanted", "after"}
	if got := orderStrings(t, planned); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestTransform_SectionInsertionAppendsWithoutAvailablePredecessor(t *testing.T) {
	t.Parallel()

	source := mustDocument(t, `{
  "sections":{"wanted":{"type":"new"},"source_only":{}},
  "order":["wanted","source_only"]
}`)
	target := mustDocument(t, `{"sections":{"target_only":{}},"order":["target_only"]}`)

	planned, err := Transform(source, target, Scope{Mode: ModeSection, SectionKey: "wanted"})
	if err != nil {
		t.Fatalf("Transform(): %v", err)
	}
	want := []string{"target_only", "wanted"}
	if got := orderStrings(t, planned); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestTransform_ExistingSectionMissingFromOrderIsInserted(t *testing.T) {
	t.Parallel()

	source := mustDocument(t, `{
  "sections":{"first":{},"wanted":{"type":"new"}},
  "order":["first","wanted"]
}`)
	target := mustDocument(t, `{
  "sections":{"first":{},"wanted":{"type":"old"},"after":{}},
  "order":["first","after"]
}`)

	planned, err := Transform(source, target, Scope{Mode: ModeSection, SectionKey: "wanted"})
	if err != nil {
		t.Fatalf("Transform(): %v", err)
	}
	want := []string{"first", "wanted", "after"}
	if got := orderStrings(t, planned); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestTransform_SectionFailures(t *testing.T) {
	t.Parallel()

	baseTarget := mustDocument(t, `{"sections":{"target":{}},"order":["target"]}`)
	fullOrder := make([]string, 25)
	fullSections := make([]string, 25)
	for index := range 25 {
		fullOrder[index] = fmt.Sprintf("\"s%02d\"", index)
		fullSections[index] = fmt.Sprintf("\"s%02d\":{}", index)
	}
	overLimitTarget := mustDocument(
		t,
		fmt.Sprintf(
			`{"sections":{%s},"order":[%s]}`,
			strings.Join(fullSections, ","),
			strings.Join(fullOrder, ","),
		),
	)

	tests := []struct {
		name    string
		source  templatejson.Document
		target  templatejson.Document
		section string
	}{
		{
			name:    "missing source section",
			source:  mustDocument(t, `{"sections":{"other":{}},"order":["other"]}`),
			target:  baseTarget,
			section: "wanted",
		},
		{
			name:    "order exceeds limit",
			source:  mustDocument(t, `{"sections":{"wanted":{}},"order":["wanted"]}`),
			target:  overLimitTarget,
			section: "wanted",
		},
		{
			name:    "missing section key",
			source:  mustDocument(t, `{"sections":{"wanted":{}},"order":["wanted"]}`),
			target:  baseTarget,
			section: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Transform(
				test.source,
				test.target,
				Scope{Mode: ModeSection, SectionKey: test.section},
			); err == nil {
				t.Fatal("Transform() error = nil")
			}
		})
	}
}

func TestTransform_CustomCSSChangesOnlyAllowedField(t *testing.T) {
	t.Parallel()

	source := mustDocument(t, `{
  "sections":{"hero":{"custom_css":[".hero { color: red; }"],"settings":{"source":true}}},
  "order":["hero"],
  "source_only":true
}`)
	target := mustDocument(t, `/* keep target */
{
  "sections":{
    "hero":{
      "custom_css":[".hero { color: blue; }",".keep { display: block; }"],
      "settings":{"product":"synthetic-product"},
      "blocks":[{"type":"synthetic","settings":{"keep":true}}],
      "other":"keep"
    },
    "untouched":{"settings":{"keep":true}}
  },
  "order":["hero","untouched"],
  "target_unknown":{"keep":true}
}`)
	want := mustDocument(t, `{
  "sections":{
    "hero":{
      "custom_css":[".hero { color: red; }"],
      "settings":{"product":"synthetic-product"},
      "blocks":[{"type":"synthetic","settings":{"keep":true}}],
      "other":"keep"
    },
    "untouched":{"settings":{"keep":true}}
  },
  "order":["hero","untouched"],
  "target_unknown":{"keep":true}
}`)

	planned, err := Transform(
		source,
		target,
		Scope{Mode: ModeField, SectionKey: "hero", Field: "custom_css"},
	)
	if err != nil {
		t.Fatalf("Transform(): %v", err)
	}
	assertCanonicalEqual(t, planned, want)
	if !bytes.Equal(planned.Header, target.Header) {
		t.Fatalf("planned header = %q, want target header %q", planned.Header, target.Header)
	}
}

func TestTransform_CustomCSSRejectsMissingAndWrongTypes(t *testing.T) {
	t.Parallel()

	valid := `{"sections":{"hero":{"custom_css":["ok"]}},"order":["hero"]}`
	tests := []struct {
		name   string
		source string
		target string
		field  string
	}{
		{name: "field not allowed", source: valid, target: valid, field: "settings"},
		{name: "source section missing", source: `{"sections":{},"order":[]}`, target: valid, field: "custom_css"},
		{name: "target section missing", source: valid, target: `{"sections":{},"order":[]}`, field: "custom_css"},
		{name: "source field missing", source: `{"sections":{"hero":{}},"order":["hero"]}`, target: valid, field: "custom_css"},
		{name: "target field missing", source: valid, target: `{"sections":{"hero":{}},"order":["hero"]}`, field: "custom_css"},
		{name: "source null", source: `{"sections":{"hero":{"custom_css":null}},"order":["hero"]}`, target: valid, field: "custom_css"},
		{name: "target string", source: valid, target: `{"sections":{"hero":{"custom_css":"bad"}},"order":["hero"]}`, field: "custom_css"},
		{name: "source object", source: `{"sections":{"hero":{"custom_css":{}}},"order":["hero"]}`, target: valid, field: "custom_css"},
		{name: "source number", source: `{"sections":{"hero":{"custom_css":2}},"order":["hero"]}`, target: valid, field: "custom_css"},
		{name: "source mixed array", source: `{"sections":{"hero":{"custom_css":["ok",2]}},"order":["hero"]}`, target: valid, field: "custom_css"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Transform(
				mustDocument(t, test.source),
				mustDocument(t, test.target),
				Scope{Mode: ModeField, SectionKey: "hero", Field: test.field},
			)
			if err == nil {
				t.Fatal("Transform() error = nil")
			}
			if test.field == "custom_css" && !strings.Contains(err.Error(), "sections.hero.custom_css") && !strings.Contains(err.Error(), "sections.hero") {
				t.Fatalf("Transform() error = %q, want target path", err)
			}
		})
	}
}

func mustDocument(t *testing.T, body string) templatejson.Document {
	t.Helper()

	document, err := templatejson.Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	return document
}

func assertCanonicalEqual(
	t *testing.T,
	got templatejson.Document,
	want templatejson.Document,
) {
	t.Helper()

	gotCanonical, err := got.Canonical()
	if err != nil {
		t.Fatalf("Canonical(got): %v", err)
	}
	wantCanonical, err := want.Canonical()
	if err != nil {
		t.Fatalf("Canonical(want): %v", err)
	}
	if !bytes.Equal(gotCanonical, wantCanonical) {
		t.Fatalf("canonical mismatch:\n got: %s\nwant: %s", gotCanonical, wantCanonical)
	}
}

func orderStrings(t *testing.T, document templatejson.Document) []string {
	t.Helper()

	values := document.Root["order"].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.(string))
	}
	return result
}
