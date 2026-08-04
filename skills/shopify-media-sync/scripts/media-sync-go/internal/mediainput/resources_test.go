package mediainput

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildResourceIndexFreezesPathBytesAndSHA(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "asset.svg")
	const body = `<svg xmlns="http://www.w3.org/2000/svg"/>`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := BuildResourceIndex(root, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resource := index.Files["asset.svg"]
	if resource.Path != path || resource.Bytes != int64(len(body)) || resource.SHA256 == "" || resource.MimeType != "image/svg+xml" {
		t.Fatalf("unexpected resource evidence: %#v", resource)
	}
}
