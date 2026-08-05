//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package engine

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRuntimeRejectsSpecialFileAnywhereInPayload(t *testing.T) {
	manager := testManager(t, &recordingCommandRunner{})
	target := manager.targetDir(t)
	installFixture(t, target, lighthouseVersion)
	fifo := filepath.Join(target, installPayloadDir, "node_modules", "dependency", "untrusted.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := manager.Runtime(context.Background())
	if !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("err=%v", err)
	}
}
