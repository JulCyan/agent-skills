package mediainput

import (
	"archive/zip"
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

func TestBuildResourceIndexRefusesToReplaceExistingExtractionDirectory(t *testing.T) {
	root := t.TempDir()
	zipPath := filepath.Join(root, "media.zip")
	archive, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	entry, err := writer.Create("asset.svg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	downloads := filepath.Join(root, "downloads")
	existing := filepath.Join(downloads, "zip")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existing, "consumer-owned.txt")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := BuildResourceIndex("", zipPath, downloads); err == nil {
		t.Fatal("expected existing extraction directory to be rejected")
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "preserve" {
		t.Fatalf("existing extraction directory was modified: body=%q err=%v", body, err)
	}
}

func TestInspectResourceRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.svg")
	link := filepath.Join(root, "link.svg")
	if err := os.WriteFile(target, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectResource(link); err == nil {
		t.Fatal("expected symlink resource to be rejected")
	}
}
