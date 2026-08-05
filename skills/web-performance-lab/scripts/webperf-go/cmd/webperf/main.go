package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(app.Run(ctx, os.Args[1:], app.Dependencies{}))
}
