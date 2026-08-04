package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRunMainCancelsContextOnSIGTERM(t *testing.T) {
	if os.Getenv("MEDIA_SYNC_SIGNAL_HELPER") == "1" {
		marker := os.Getenv("MEDIA_SYNC_SIGNAL_MARKER")
		runCLI = func(ctx context.Context, _ []string, _ io.Writer) error {
			if err := os.WriteFile(marker+".ready", []byte("ready"), 0o600); err != nil {
				return err
			}
			<-ctx.Done()
			if err := os.WriteFile(marker, []byte("cancelled"), 0o600); err != nil {
				return err
			}
			return ctx.Err()
		}
		os.Exit(runMain(nil, io.Discard, io.Discard))
	}

	marker := filepath.Join(t.TempDir(), "cancelled")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunMainCancelsContextOnSIGTERM$")
	cmd.Env = append(os.Environ(),
		"MEDIA_SYNC_SIGNAL_HELPER=1",
		"MEDIA_SYNC_SIGNAL_MARKER="+marker,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("helper CLI did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled CLI must return a non-zero exit status")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "cancelled" {
		t.Fatalf("SIGTERM did not cancel CLI context: marker=%q err=%v", got, err)
	}
}
