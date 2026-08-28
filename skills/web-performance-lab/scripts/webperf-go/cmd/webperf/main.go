package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/app"
)

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:], app.Dependencies{}))
}

func runMain(parent context.Context, args []string, dependencies app.Dependencies) int {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, args, dependencies)
}
