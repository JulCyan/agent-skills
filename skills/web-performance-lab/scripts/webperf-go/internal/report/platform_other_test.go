//go:build !(aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris)

package report

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteNewRejectsPlatformsWithoutPrivateOutputContract(t *testing.T) {
	if OutputSupported() {
		t.Fatal("unsupported-platform build reported private output support")
	}
	output := filepath.Join(t.TempDir(), "report.html")
	if err := WriteNew(output, []byte("report"), nil); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported platform created output: %v", err)
	}
}
