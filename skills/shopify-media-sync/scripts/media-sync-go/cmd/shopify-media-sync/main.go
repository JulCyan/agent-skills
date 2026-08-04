package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"shopify-media-sync/internal/mediasync"
)

var runCLI = mediasync.Run

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runCLI(ctx, args, stdout); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	return 0
}
