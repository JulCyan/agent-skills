package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"theme-template-sync/internal/adapter"
	"theme-template-sync/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	callerCWD := os.Getenv("THEME_TEMPLATE_SYNC_CALLER_CWD")
	if callerCWD == "" {
		var err error
		callerCWD, err = os.Getwd()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stdout, `{"status":"NEEDS_SETUP","reason":"caller-working-directory-unavailable"}`)
			_, _ = fmt.Fprintln(os.Stderr, "determining caller working directory failed")
			os.Exit(2)
		}
	}
	code := app.Run(
		ctx,
		os.Args[1:],
		os.Stdout,
		os.Stderr,
		app.Dependencies{
			Adapter:   adapter.NewShopifyCLIAdapter(nil, "shopify"),
			CallerCWD: callerCWD,
		},
	)
	os.Exit(code)
}
