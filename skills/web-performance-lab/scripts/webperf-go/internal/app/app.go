// Package app parses webperf commands and renders their stable contracts.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/bundle"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/collect"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/safeurl"
)

type Dependencies struct {
	Stdout io.Writer
	Stderr io.Writer
	Engine engineManager
}

type engineManager interface {
	Status(context.Context) engine.Status
	Setup(context.Context) (engine.Status, error)
	Runtime(context.Context) (engine.Runtime, error)
}

func Run(ctx context.Context, args []string, deps Dependencies) int {
	stdout, stderr := writers(deps)
	jsonOutput, args := splitGlobalFlags(args)

	if ctx.Err() != nil {
		return render(stdout, stderr, jsonOutput, failure("", contract.Interrupted, "interrupted", "command interrupted", "run the command again", contract.ErrInterrupted))
	}

	report, showHelp := dispatch(ctx, args, stderr, deps)
	if showHelp {
		if jsonOutput {
			return render(stdout, stderr, true, success("help", helpData{Commands: commands(), AvailableProfiles: profile.List()}))
		}
		_, _ = fmt.Fprint(stdout, helpText())
		return contract.ExitOK
	}
	return render(stdout, stderr, jsonOutput, report)
}

func dispatch(ctx context.Context, args []string, stderr io.Writer, deps Dependencies) (contract.Envelope, bool) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return contract.Envelope{}, true
	}

	switch args[0] {
	case "doctor":
		if len(args) != 1 {
			return invalid("doctor", "unexpected_argument", "doctor does not accept arguments"), false
		}
		return doctor(ctx, manager(deps)), false
	case "profiles":
		if len(args) == 2 && args[1] == "list" {
			return success("profiles list", profile.List()), false
		}
		return invalid("profiles", "invalid_profiles_command", "expected profiles list"), false
	case "engine":
		return engineCommand(ctx, args, manager(deps))
	case "collect":
		return collectCommand(ctx, args[1:], stderr, manager(deps)), false
	case "inspect":
		return inspect(args[1:], stderr), false
	case "compare":
		return compare(args[1:], stderr), false
	case "raw":
		return raw(args[1:])
	default:
		return invalid(args[0], "unknown_command", "unknown command"), false
	}
}

func manager(deps Dependencies) engineManager {
	if deps.Engine != nil {
		return deps.Engine
	}
	return engine.Manager{}
}

func doctor(ctx context.Context, manager engineManager) contract.Envelope {
	data := doctorData{GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	runtimeInfo, err := manager.Runtime(ctx)
	if ctx.Err() != nil {
		return interrupted("doctor")
	}
	if err != nil {
		data.EngineStatus = engine.NeedsSetup
		return needsSetupWithData("doctor", data)
	}
	data.EngineStatus = engine.Ready
	data.LighthouseVersion = runtimeInfo.Version()
	data.NodeVersion = runtimeInfo.NodeVersion()
	data.ChromeVersion = runtimeInfo.ChromeVersion()
	return success("doctor", data)
}

func engineCommand(ctx context.Context, args []string, manager engineManager) (contract.Envelope, bool) {
	if len(args) != 2 || (args[1] != "status" && args[1] != "setup") {
		return invalid("engine", "invalid_engine_command", "expected engine status or engine setup"), false
	}
	if args[1] == "status" {
		status := manager.Status(ctx)
		if ctx.Err() != nil {
			return interrupted("engine status"), false
		}
		if status != engine.Ready {
			return needsSetupWithData("engine status", engineStatusData{Status: status}), false
		}
		return success("engine status", engineStatusData{Status: status}), false
	}
	status, err := manager.Setup(ctx)
	if ctx.Err() != nil {
		return interrupted("engine setup"), false
	}
	if err != nil || status != engine.Ready {
		return needsSetupWithData("engine setup", engineStatusData{Status: status}), false
	}
	return success("engine setup", engineStatusData{Status: status}), false
}

func collectCommand(ctx context.Context, args []string, stderr io.Writer, manager engineManager) contract.Envelope {
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	urlValue := flags.String("url", "", "public http or https url")
	profileName := flags.String("profile", "", "versioned measurement profile")
	runs := flags.Int("runs", 5, "sequential samples")
	out := flags.String("out", "", "new evidence directory")
	if err := flags.Parse(args); err != nil {
		return invalid("collect", "invalid_flags", "collect flags are invalid")
	}
	if flags.NArg() != 0 {
		return invalid("collect", "unexpected_argument", "collect does not accept positional arguments")
	}
	if *profileName == "" {
		return profileRequired()
	}
	resolvedProfile, err := profile.Resolve(*profileName)
	if err != nil {
		return invalid("collect", "unknown_profile", "collect profile is not supported")
	}
	parsedURL, err := safeurl.Validate(*urlValue)
	if err != nil {
		return invalid("collect", "invalid_url", "collect requires a public http or https url")
	}
	if *out == "" {
		return invalid("collect", "out_required", "collect requires an output directory")
	}
	if *runs < 3 {
		return invalid("collect", "runs_too_low", "collect requires at least three runs")
	}
	runtimeInfo, err := manager.Runtime(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return failure("collect", contract.Interrupted, "interrupted", "command interrupted", "run the command again", contract.ErrInterrupted)
		}
		return unavailable("collect")
	}
	request := collect.Request{
		URL:        parsedURL.String(),
		DisplayURL: safeurl.Display(parsedURL),
		Profile:    resolvedProfile,
		Runs:       *runs,
		Out:        *out,
		Protocol:   protocolFor(resolvedProfile, runtimeInfo),
	}
	summary, err := (collect.Service{Runner: engine.NewRunner(runtimeInfo)}).Run(ctx, request)
	if err != nil {
		return collectionFailure(summary, err)
	}
	return contract.Envelope{
		SchemaVersion: contract.SchemaVersion,
		Command:       "collect",
		Status:        contract.OK,
		Data:          summary,
		Warnings:      summary.Warnings,
	}
}

func protocolFor(resolved profile.Profile, runtimeInfo engine.Runtime) bundle.Protocol {
	return bundle.CompleteProtocol(bundle.Protocol{
		SchemaVersion:     1,
		Profile:           resolved.Name,
		FormFactor:        resolved.FormFactor,
		ThrottlingMethod:  resolved.ThrottlingMethod,
		ResolvedFlags:     append([]string(nil), resolved.LighthouseArgs...),
		RuntimeFlags:      []string{"--only-categories=performance", "--output=json", "--quiet", "--no-enable-error-reporting", "--chrome-path=resolved-by-webperf", "fresh-user-data-dir-per-attempt"},
		LighthouseVersion: runtimeInfo.Version(),
		NodeVersion:       runtimeInfo.NodeVersion(),
		ChromeVersion:     runtimeInfo.ChromeVersion(),
		OS:                runtimeInfo.OS(),
		Arch:              runtimeInfo.Arch(),
	})
}

func collectionFailure(summary bundle.Summary, err error) contract.Envelope {
	status := contract.EngineFailed
	code := "collection_failed"
	message := "collection failed"
	remediation := "inspect the retained evidence and run a new collection"
	switch {
	case errors.Is(err, collect.ErrIncomplete):
		status = contract.Partial
		code = "incomplete_samples"
		message = "fewer than three samples succeeded"
	case errors.Is(err, collect.ErrPartial):
		status = contract.Partial
		code = "partial_attempts"
		message = "one or more attempts did not complete cleanly"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = contract.Interrupted
		code = "interrupted"
		message = "collection interrupted"
		remediation = "inspect the retained evidence and run a new collection"
	}
	return contract.Envelope{
		SchemaVersion: contract.SchemaVersion,
		Command:       "collect",
		Status:        status,
		Data:          summary,
		Warnings:      summary.Warnings,
		Error:         contract.NewCommandError(code, message, remediation, err),
	}
}

func inspect(args []string, stderr io.Writer) contract.Envelope {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	run := flags.String("run", "", "evidence directory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return invalid("inspect", "invalid_flags", "inspect requires only the --run flag")
	}
	if *run == "" {
		return invalid("inspect", "run_required", "inspect requires a run directory")
	}
	return unavailable("inspect")
}

func compare(args []string, stderr io.Writer) contract.Envelope {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseline := flags.String("baseline", "", "baseline evidence directory")
	candidate := flags.String("candidate", "", "candidate evidence directory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return invalid("compare", "invalid_flags", "compare requires only baseline and candidate flags")
	}
	if *baseline == "" || *candidate == "" {
		return invalid("compare", "bundle_required", "compare requires baseline and candidate directories")
	}
	return unavailable("compare")
}

func raw(args []string) (contract.Envelope, bool) {
	if len(args) == 0 || args[0] != "lighthouse" {
		return invalid("raw", "invalid_raw_command", "expected raw lighthouse"), false
	}
	return unavailable("raw lighthouse"), false
}

func success(command string, data any) contract.Envelope {
	return contract.Envelope{SchemaVersion: contract.SchemaVersion, Command: command, Status: contract.OK, Data: data}
}

func invalid(command, code, message string) contract.Envelope {
	return failure(command, contract.InvalidInput, code, message, "run webperf --help", contract.ErrInvalidInput)
}

func unavailable(command string) contract.Envelope {
	return failure(command, contract.NeedsSetup, "engine_needs_setup", "locked Lighthouse engine needs setup", "run webperf engine setup before using this command", contract.ErrNeedsSetup)
}

func needsSetupWithData(command string, data any) contract.Envelope {
	report := unavailable(command)
	report.Data = data
	return report
}

func interrupted(command string) contract.Envelope {
	return failure(command, contract.Interrupted, "interrupted", "command interrupted", "run the command again", contract.ErrInterrupted)
}

func profileRequired() contract.Envelope {
	report := invalid("collect", "profile_required", "collect requires an explicit versioned profile")
	report.Data = availableProfilesData{AvailableProfiles: profile.List()}
	return report
}

func failure(command string, status contract.Status, code, message, remediation string, cause error) contract.Envelope {
	return contract.Envelope{
		SchemaVersion: contract.SchemaVersion,
		Command:       command,
		Status:        status,
		Error:         contract.NewCommandError(code, message, remediation, cause),
	}
}

func render(stdout, stderr io.Writer, jsonOutput bool, report contract.Envelope) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(report)
	} else if report.Command == "collect" {
		if summary, ok := report.Data.(bundle.Summary); ok {
			renderHumanCollect(stdout, summary)
		} else if report.Error == nil {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", report.Command, report.Status)
		}
		if report.Error != nil {
			_, _ = fmt.Fprintf(stderr, "webperf: %s\n", report.Error.Message)
		}
	} else if report.Error == nil {
		if profiles, ok := report.Data.([]profile.Profile); ok && report.Command == "profiles list" {
			for _, item := range profiles {
				_, _ = fmt.Fprintf(stdout, "%s (%s, %s)\n", item.Name, item.FormFactor, item.ThrottlingMethod)
			}
		} else {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", report.Command, report.Status)
		}
	} else {
		_, _ = fmt.Fprintf(stderr, "webperf: %s\n", report.Error.Message)
	}
	if report.Error != nil && jsonOutput {
		_, _ = fmt.Fprintf(stderr, "webperf: %s\n", report.Error.Message)
	}
	return contract.ExitCode(report.Status)
}

func renderHumanCollect(stdout io.Writer, summary bundle.Summary) {
	if summary.SchemaVersion == 0 {
		return
	}
	if summary.Profile != "" {
		_, _ = fmt.Fprintf(stdout, "profile: %s\n", summary.Profile)
	}
	if summary.RequestedRuns > 0 || summary.SuccessfulRuns > 0 {
		_, _ = fmt.Fprintf(stdout, "samples: %d/%d\n", summary.SuccessfulRuns, summary.RequestedRuns)
	}
	if summary.FinalURL != "" {
		_, _ = fmt.Fprintf(stdout, "final URL: %s\n", summary.FinalURL)
	}
	if summary.Metrics.PerformanceScore.Count < 3 || summary.Metrics.LCP.Count < 3 {
		_, _ = fmt.Fprintln(stdout, "aggregate: unavailable")
		for _, warning := range summary.Warnings {
			_, _ = fmt.Fprintf(stdout, "warning: %s\n", warning)
		}
		return
	}
	_, _ = fmt.Fprintf(
		stdout,
		"official performance score: median=%g mad=%g iqr=%g\n",
		summary.Metrics.PerformanceScore.Median,
		summary.Metrics.PerformanceScore.MAD,
		summary.Metrics.PerformanceScore.IQR,
	)
	_, _ = fmt.Fprintf(
		stdout,
		"LCP: median=%g mad=%g iqr=%g\n",
		summary.Metrics.LCP.Median,
		summary.Metrics.LCP.MAD,
		summary.Metrics.LCP.IQR,
	)
	for _, warning := range summary.Warnings {
		_, _ = fmt.Fprintf(stdout, "warning: %s\n", warning)
	}
}

func splitGlobalFlags(args []string) (bool, []string) {
	jsonOutput := false
	for len(args) > 0 && args[0] == "--json" {
		jsonOutput = true
		args = args[1:]
	}
	return jsonOutput, args
}

func writers(deps Dependencies) (io.Writer, io.Writer) {
	stdout := deps.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := deps.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	return stdout, stderr
}

func commands() []string {
	return []string{"doctor", "profiles list", "engine status", "engine setup", "collect", "inspect", "compare", "raw lighthouse"}
}

type helpData struct {
	Commands          []string          `json:"commands"`
	AvailableProfiles []profile.Profile `json:"availableProfiles"`
}

type availableProfilesData struct {
	AvailableProfiles []profile.Profile `json:"availableProfiles"`
}

type doctorData struct {
	GoVersion         string        `json:"goVersion"`
	OS                string        `json:"os"`
	Arch              string        `json:"arch"`
	EngineStatus      engine.Status `json:"engineStatus,omitempty"`
	LighthouseVersion string        `json:"lighthouseVersion,omitempty"`
	NodeVersion       string        `json:"nodeVersion,omitempty"`
	ChromeVersion     string        `json:"chromeVersion,omitempty"`
}

type engineStatusData struct {
	Status engine.Status `json:"status"`
}

func helpText() string {
	return "webperf measures public web performance with explicit, repeatable protocols.\n\nUsage:\n  webperf [--json] <command>\n\nCommands:\n  doctor\n  profiles list\n  engine status\n  engine setup\n  collect --url <url> --profile <name> --runs 5 --out <dir>\n  inspect --run <dir>\n  compare --baseline <dir> --candidate <dir>\n  raw lighthouse -- <official flags>\n\nAvailable profiles:\n  " + strings.Join(profileNames(), "\n  ") + "\n"
}

func profileNames() []string {
	profiles := profile.List()
	names := make([]string, len(profiles))
	for i, item := range profiles {
		names[i] = item.Name
	}
	return names
}
