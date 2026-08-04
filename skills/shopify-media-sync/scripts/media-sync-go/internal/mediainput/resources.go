package mediainput

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ResourceIndex struct {
	Root       string                  `json:"root,omitempty"`
	ZipPath    string                  `json:"zip_path,omitempty"`
	Files      map[string]ResourceInfo `json:"files"`
	Duplicates map[string][]string     `json:"duplicates,omitempty"`
}

type ResourceInfo struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	MimeType string `json:"mime_type"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

func BuildResourceIndex(sourceRoot, zipPath, downloadsDir string) (ResourceIndex, error) {
	index := ResourceIndex{Files: map[string]ResourceInfo{}, Duplicates: map[string][]string{}}
	if zipPath != "" {
		extractRoot := filepath.Join(downloadsDir, "zip")
		if err := extractZip(zipPath, extractRoot); err != nil {
			return index, err
		}
		index.ZipPath = zipPath
		sourceRoot = extractRoot
	}
	if sourceRoot == "" {
		return index, nil
	}
	index.Root = sourceRoot
	err := filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := InspectResource(path)
		if err != nil {
			return err
		}
		if existing, ok := index.Files[info.Filename]; ok {
			index.Duplicates[info.Filename] = append(index.Duplicates[info.Filename], existing.Path, info.Path)
			return nil
		}
		index.Files[info.Filename] = info
		return nil
	})
	if err != nil {
		return index, err
	}
	for key, paths := range index.Duplicates {
		index.Duplicates[key] = uniqueSorted(paths)
		delete(index.Files, key)
	}
	if len(index.Duplicates) == 0 {
		index.Duplicates = nil
	}
	return index, nil
}

func extractZip(zipPath, destRoot string) error {
	if err := os.RemoveAll(destRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		cleanName := filepath.Clean(file.Name)
		if filepath.IsAbs(cleanName) || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) || cleanName == ".." {
			return fmt.Errorf("zip 包含不安全路径: %s", file.Name)
		}
		target := filepath.Join(destRoot, cleanName)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode())
		if err != nil {
			src.Close()
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		src.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func InspectResource(path string) (ResourceInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return ResourceInfo{}, err
	}
	defer file.Close()
	hash := sha256.New()
	bytes, err := io.Copy(hash, file)
	if err != nil {
		return ResourceInfo{}, err
	}
	info := ResourceInfo{
		Filename: filepath.Base(path),
		Path:     filepath.Clean(path),
		Bytes:    bytes,
		SHA256:   hex.EncodeToString(hash.Sum(nil)),
		MimeType: inferMimeType(path),
	}
	if width, height := imageDimensions(path); width > 0 && height > 0 {
		info.Width = width
		info.Height = height
	}
	return info, nil
}

func inferMimeType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".mp4":
		return "video/mp4"
	}
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(file, buf)
	if n > 0 {
		return http.DetectContentType(buf[:n])
	}
	return "application/octet-stream"
}

func imageDimensions(path string) (int, int) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
