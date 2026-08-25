package templatejson

import "testing"

func TestParseTemplateKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		template  string
		page      string
		want      TemplateKey
		wantAsset string
		warn      bool
		wantErr   bool
	}{
		{
			name:      "page template",
			template:  "page.example",
			want:      "page.example",
			wantAsset: "templates/page.example.json",
		},
		{
			name:      "product template",
			template:  "product.lp4",
			want:      "product.lp4",
			wantAsset: "templates/product.lp4.json",
		},
		{
			name:      "product template with underscore",
			template:  "product.lx2_42w",
			want:      "product.lx2_42w",
			wantAsset: "templates/product.lx2_42w.json",
		},
		{
			name:      "collection template",
			template:  "collection.example",
			want:      "collection.example",
			wantAsset: "templates/collection.example.json",
		},
		{
			name:      "root template",
			template:  "index",
			want:      "index",
			wantAsset: "templates/index.json",
		},
		{
			name:      "nested customer template",
			template:  "customers/account",
			want:      "customers/account",
			wantAsset: "templates/customers/account.json",
		},
		{
			name:      "nested metaobject template",
			template:  "metaobject/synthetic_type",
			want:      "metaobject/synthetic_type",
			wantAsset: "templates/metaobject/synthetic_type.json",
		},
		{
			name:      "deprecated page alias",
			page:      "example",
			want:      "page.example",
			wantAsset: "templates/page.example.json",
			warn:      true,
		},
		{name: "missing key", wantErr: true},
		{name: "both entrypoints", template: "page.one", page: "two", wantErr: true},
		{name: "slash traversal", template: "../page.example", wantErr: true},
		{name: "nested traversal", template: "customers/../account", wantErr: true},
		{name: "too deeply nested", template: "one/two/three", wantErr: true},
		{name: "empty nested segment", template: "customers//account", wantErr: true},
		{name: "backslash", template: `page\\example`, wantErr: true},
		{name: "absolute", template: "/page.example", wantErr: true},
		{name: "dot dot", template: "page..example", wantErr: true},
		{name: "json suffix", template: "page.example.json", wantErr: true},
		{name: "asset prefix", template: "templates/page.example", wantErr: true},
		{name: "uppercase", template: "Page.Example", wantErr: true},
		{name: "page alias path", page: "nested/example", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, warnings, err := ParseTemplateKey(test.template, test.page)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseTemplateKey(%q, %q) error = nil", test.template, test.page)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTemplateKey(%q, %q): %v", test.template, test.page, err)
			}
			if got != test.want {
				t.Fatalf("key = %q, want %q", got, test.want)
			}
			if got.AssetPath() != test.wantAsset {
				t.Fatalf("AssetPath() = %q, want %q", got.AssetPath(), test.wantAsset)
			}
			if (len(warnings) > 0) != test.warn {
				t.Fatalf("warnings = %v, want warning = %t", warnings, test.warn)
			}
		})
	}
}
