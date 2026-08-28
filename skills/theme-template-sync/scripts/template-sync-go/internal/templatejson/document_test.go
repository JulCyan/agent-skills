package templatejson

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParse_ShopifyHeaderAndUnknownFields(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/shopify-header.jsonc")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	data = append([]byte{0xef, 0xbb, 0xbf}, data...)
	document, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}
	if !strings.Contains(string(document.Header), "automatically generated") {
		t.Fatalf("Header = %q, want Shopify comment", document.Header)
	}
	metadata, ok := document.Root["synthetic_metadata"].(map[string]any)
	if !ok || metadata["keep"] != true {
		t.Fatalf("unknown metadata = %#v, want preserved object", document.Root["synthetic_metadata"])
	}
	if _, ok := document.Root["precise"].(json.Number); !ok {
		t.Fatalf("precise type = %T, want json.Number", document.Root["precise"])
	}
}

func TestParse_RejectsUnsupportedContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "non object", body: "[]"},
		{name: "second root", body: `{"sections":{},"order":[]} {}`},
		{name: "trailing comment", body: `{"sections":{},"order":[]} /* no */`},
		{name: "trailing garbage", body: `{"sections":{},"order":[]} nope`},
		{name: "body comment", body: `{"sections":{/* no */},"order":[]}`},
		{name: "unterminated header", body: `/* no`},
		{name: "invalid json", body: `{"sections":,}`},
		{name: "duplicate root key", body: `{"sections":{},"order":[],"order":[]}`},
		{name: "duplicate nested key", body: `{"sections":{"hero":{"type":"one","type":"two"}},"order":["hero"]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse([]byte(test.body)); err == nil {
				t.Fatalf("Parse(%q) error = nil", test.body)
			}
		})
	}
}

func TestDocumentValidate(t *testing.T) {
	t.Parallel()

	validOrder := make([]any, 25)
	sections := map[string]any{}
	for index := range 25 {
		key := string(rune('a' + index))
		validOrder[index] = key
		sections[key] = map[string]any{}
	}

	tests := []struct {
		name    string
		root    map[string]any
		wantErr bool
	}{
		{
			name: "valid maximum",
			root: map[string]any{"sections": sections, "order": validOrder},
		},
		{name: "missing sections", root: map[string]any{"order": []any{}}, wantErr: true},
		{name: "sections wrong type", root: map[string]any{"sections": []any{}, "order": []any{}}, wantErr: true},
		{name: "missing order", root: map[string]any{"sections": map[string]any{}}, wantErr: true},
		{name: "order wrong type", root: map[string]any{"sections": map[string]any{}, "order": "one"}, wantErr: true},
		{name: "order mixed type", root: map[string]any{"sections": map[string]any{}, "order": []any{"one", 2}}, wantErr: true},
		{name: "order duplicate", root: map[string]any{"sections": map[string]any{"one": map[string]any{}}, "order": []any{"one", "one"}}, wantErr: true},
		{name: "order over limit", root: map[string]any{"sections": sections, "order": append(validOrder, "overflow")}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := (Document{Root: test.root}).Validate()
			if test.wantErr && err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Validate(): %v", err)
			}
		})
	}
}
