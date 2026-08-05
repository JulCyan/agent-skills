// Package app parses webperf commands and renders their stable contracts.
package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/safeurl"
)

type Dependencies struct {
	Stdout io.Writer
	Stderr io.Writer
}

func Run(ctx context.Context, args []string, deps Dependencies) int {
	stdout, stderr := writers(deps)
	jsonOutput, args := splitGlobalFlags(args)

	if ctx.Err() != nil {
		return render(stdout, stderr, jsonOutput, failure("", contract.Interrupted, "interrupted", "command interrupted", "run the command again", contract.ErrInterrupted))
	}

	report, showHelp := dispatch(args, stderr)
	if showHelp {
		if jsonOutput {
			return render(stdout, stderr, true, success("help", helpData{Commands: commands(), AvailableProfiles: profile.List()}))
		}
		_, _ = fmt.Fprint(stdout, helpText())
		return contract.ExitOK
	}
	return render(stdout, stderr, jsonOutput, report)
}

func dispatch(args []string, stderr io.Writer) (contract.Envelope, bool) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return contract.Envelope{}, true
	}

	switch args[0] {
	case "doctor":
		if len(args) != 1 {
			return invalid("doctor", "unexpected_argument", "doctor does not accept arguments"), false
		}
		return unavailable("doctor"), false
	case "profiles":
		if len(args) == 2 && args[1] == "list" {
			return success("profiles list", profile.List()), false
		}
		return invalid("profiles", "invalid_profiles_command", "expected profiles list"), false
	case "engine":
		return engine(args)
	case "collect":
		return collect(args[1:], stderr), false
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

func engine(args []string) (contract.Envelope, bool) {
	if len(args) != 2 || (args[1] != "status" && args[1] != "setup") {
		return invalid("engine", "invalid_engine_command", "expected engine status or engine setup"), false
	}
	return unavailable("engine " + args[1]), false
}

func collect(args []string, stderr io.Writer) contract.Envelope {
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
	if _, err := profile.Resolve(*profileName); err != nil {
		return invalid("collect", "unknown_profile", "collect profile is not supported")
	}
	if _, err := safeurl.Validate(*urlValue); err != nil {
		return invalid("collect", "invalid_url", "collect requires a public http or https url")
	}
	if *out == "" {
		return invalid("collect", "out_required", "collect requires an output directory")
	}
	if *runs < 3 {
		return invalid("collect", "runs_too_low", "collect requires at least three runs")
	}
	return unavailable("collect")
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
	return failure(command, contract.NeedsSetup, "command_unavailable", "command is not available yet", "complete engine setup before using this command", contract.ErrNeedsSetup)
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
