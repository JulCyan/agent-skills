//go:build android || darwin || dragonfly || freebsd || linux || netbsd || openbsd

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
