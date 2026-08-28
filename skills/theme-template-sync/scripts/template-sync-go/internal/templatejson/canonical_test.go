package templatejson

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDocumentCanonical_IsStableAndPreservesNumbers(t *testing.T) {
	t.Parallel()

	document := Document{
		Header: []byte("/* ignored */\n"),
		Root: map[string]any{
			"unknown":  map[string]any{"z": json.Number("1.2300"), "a": true},
			"sections": map[string]any{"hero": map[string]any{}},
			"order":    []any{"hero"},
		},
	}
	want := []byte("{\"order\":[\"hero\"],\"sections\":{\"hero\":{}},\"unknown\":{\"a\":true,\"z\":1.2300}}\n")

	first, err := document.Canonical()
	if err != nil {
		t.Fatalf("Canonical(): %v", err)
	}
	second, err := document.Canonical()
	if err != nil {
		t.Fatalf("Canonical() again: %v", err)
	}
	if !bytes.Equal(first, want) {
		t.Fatalf("Canonical() = %s, want %s", first, want)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Canonical() unstable:\n%s\n%s", first, second)
	}

	firstHash, err := document.CanonicalSHA256()
	if err != nil {
		t.Fatalf("CanonicalSHA256(): %v", err)
	}
	secondHash, err := document.CanonicalSHA256()
	if err != nil {
		t.Fatalf("CanonicalSHA256() again: %v", err)
	}
	if firstHash != secondHash || len(firstHash) != 64 {
		t.Fatalf("hashes = %q, %q, want stable SHA-256", firstHash, secondHash)
	}
}

func TestDocumentCanonical_IgnoresHeaderFormattingAndObjectOrder(t *testing.T) {
	t.Parallel()

	first, err := Parse([]byte("/* first */\n{\"sections\":{\"hero\":{}},\"order\":[\"hero\"],\"x\":1}"))
	if err != nil {
		t.Fatalf("Parse(first): %v", err)
	}
	second, err := Parse([]byte("// second\n{ \"x\": 1, \"order\": [\"hero\"], \"sections\": {\"hero\": {}} }"))
	if err != nil {
		t.Fatalf("Parse(second): %v", err)
	}

	firstCanonical, err := first.Canonical()
	if err != nil {
		t.Fatalf("Canonical(first): %v", err)
	}
	secondCanonical, err := second.Canonical()
	if err != nil {
		t.Fatalf("Canonical(second): %v", err)
	}
	if !bytes.Equal(firstCanonical, secondCanonical) {
		t.Fatalf("canonical values differ:\n%s\n%s", firstCanonical, secondCanonical)
	}
}

func TestDocumentRender_PreservesHeaderWhileKeepingCanonicalBody(t *testing.T) {
	t.Parallel()

	document := Document{
		Header: []byte("/* target */\n"),
		Root: map[string]any{
			"sections": map[string]any{},
			"order":    []any{},
		},
	}
	canonical, err := document.Canonical()
	if err != nil {
		t.Fatalf("Canonical(): %v", err)
	}
	rendered, err := document.Render()
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	want := append([]byte("/* target */\n"), canonical...)
	if !bytes.Equal(rendered, want) {
		t.Fatalf("Render() = %q, want %q", rendered, want)
	}
	if bytes.Contains(canonical, []byte("target")) {
		t.Fatalf("Canonical() unexpectedly contains header: %q", canonical)
	}
}

func TestDocumentRender_WithoutHeaderMatchesCanonical(t *testing.T) {
	t.Parallel()

	document := Document{Root: map[string]any{
		"sections": map[string]any{},
		"order":    []any{},
	}}
	canonical, err := document.Canonical()
	if err != nil {
		t.Fatalf("Canonical(): %v", err)
	}
	rendered, err := document.Render()
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	if !bytes.Equal(rendered, canonical) {
		t.Fatalf("Render() = %q, want canonical %q", rendered, canonical)
	}
}

func TestDocumentClone_IsDeepAndPreservesHeader(t *testing.T) {
	t.Parallel()

	original := Document{
		Header: []byte("/* target */\n"),
		Root: map[string]any{
			"sections": map[string]any{
				"hero": map[string]any{"blocks": []any{map[string]any{"id": "one"}}},
			},
			"order": []any{"hero"},
		},
	}
	clone, err := original.Clone()
	if err != nil {
		t.Fatalf("Clone(): %v", err)
	}
	clone.Header[3] = 'X'
	clone.Root["sections"].(map[string]any)["hero"].(map[string]any)["blocks"].([]any)[0].(map[string]any)["id"] = "two"

	if string(original.Header) != "/* target */\n" {
		t.Fatalf("original Header mutated: %q", original.Header)
	}
	got := original.Root["sections"].(map[string]any)["hero"].(map[string]any)["blocks"].([]any)[0].(map[string]any)["id"]
	if got != "one" {
		t.Fatalf("original nested value = %v, want one", got)
	}
}
