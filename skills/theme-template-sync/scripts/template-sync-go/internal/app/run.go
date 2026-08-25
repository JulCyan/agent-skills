package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"theme-template-sync/internal/evidence"
)

func Run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	deps Dependencies,
) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if _, err := fmt.Fprint(stdout, helpText); err != nil {
			_, _ = fmt.Fprintf(stderr, "writing help: %v\n", err)
			return 1
		}
		return 0
	}

	switch args[0] {
	case "plan":
		if deps.Adapter == nil {
			return renderFailure(
				stdout,
				stderr,
				2,
				FailureNeedsSetup,
				"adapter is not configured",
			)
		}
		options, err := parsePlanOptions(args[1:], deps.CallerCWD)
		if err != nil {
			return renderFailure(stdout, stderr, 2, FailureUsage, err.Error())
		}
		result, err := CreatePlan(ctx, options, deps)
		return renderResult(stdout, stderr, result, err)
	case "apply":
		if deps.Adapter == nil {
			return renderFailure(
				stdout,
				stderr,
				2,
				FailureNeedsSetup,
				"adapter is not configured",
			)
		}
		options, err := parseApplyOptions(args[1:], deps.CallerCWD)
		if err != nil {
			return renderFailure(stdout, stderr, 2, FailureUsage, err.Error())
		}
		result, err := Apply(ctx, options, deps)
		return renderResult(stdout, stderr, result, err)
	default:
		return renderFailure(
			stdout,
			stderr,
			2,
			FailureUsage,
			fmt.Sprintf("unknown command %q", args[0]),
		)
	}
}

func renderFailure(
	stdout io.Writer,
	stderr io.Writer,
	code int,
	kind FailureKind,
	message string,
) int {
	failure := evidence.NewFailureRecord(string(kind), errors.New(message))
	result := Result{
		Status:      StatusFailed,
		FailureKind: kind,
		Message:     failure.Message,
		Targets:     []TargetResult{},
		Warnings:    []string{},
	}
	if rendered := renderResult(stdout, stderr, result, errors.New(failure.Message)); rendered == 1 {
		return rendered
	}
	return code
}

func renderResult(
	stdout io.Writer,
	stderr io.Writer,
	result Result,
	operationErr error,
) int {
	if result.Targets == nil {
		result.Targets = []TargetResult{}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	if operationErr != nil && result.Message == "" {
		failure := evidence.NewFailureRecord(string(result.FailureKind), operationErr)
		result.Message = failure.Message
	} else if result.Message != "" {
		result.Message = evidence.SanitizeMessage(result.Message)
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		_, _ = fmt.Fprintf(stderr, "encoding result: %v\n", err)
		return 1
	}
	if operationErr != nil && result.Message != "" {
		if _, err := fmt.Fprintln(stderr, result.Message); err != nil {
			return 1
		}
	}
	return exitCodeForResult(result)
}

func exitCodeForResult(result Result) int {
	switch result.Status {
	case StatusNoOp, StatusPlanned, StatusApplied:
		return 0
	case StatusPartial:
		return 4
	case StatusReadbackMismatch:
		return 3
	case StatusFailed:
		switch result.FailureKind {
		case FailureWrite, FailureReadback, FailureEvidence:
			return 3
		default:
			return 2
		}
	default:
		return 1
	}
}

const helpText = `theme-template-sync safely synchronizes Shopify JSON templates.

Usage:
  theme-template-sync plan [flags]
  theme-template-sync apply --plan <path> [--execute]
`
