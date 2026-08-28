package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/app"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
)

func TestRunMainReturnsBeforeProcessExit(t *testing.T) {
	var stdout bytes.Buffer
	if code := runMain(context.Background(), []string{"--help"}, app.Dependencies{Stdout: &stdout}); code != contract.ExitOK {
		t.Fatalf("code=%d", code)
	}
	if stdout.Len() == 0 {
		t.Fatal("help was not rendered")
	}
}
