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
	webcompare "github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/compare"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/contract"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/engine"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/profile"
	webreport "github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/report"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/safeurl"
	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

type Dependencies struct {
	Stdout    io.Writer
	Stderr    io.Writer
	Engine    engineManager
	RawRunner rawLighthouseRunner
}

type engineManager interface {
	Status(context.Context) engine.Status
	Setup(context.Context) (engine.Status, error)
	Runtime(context.Context) (engine.Runtime, error)
}

type rawLighthouseRunner interface {
	Run(context.Context, engine.Runtime, []string, io.Writer, io.Writer) engine.Result
}

type lockedRawRunner struct{}

func (lockedRawRunner) Run(ctx context.Context, runtime engine.Runtime, args []string, stdout, stderr io.Writer) engine.Result {
	return engine.NewRunner(runtime).RunRaw(ctx, args, stdout, stderr)
}

func Run(ctx context.Context, args []string, deps Dependencies) int {
	stdout, stderr := writers(deps)
	jsonOutput, args := splitGlobalFlags(args)

	if ctx.Err() != nil {
		return render(stdout, stderr, jsonOutput, failure("", contract.Interrupted, "interrupted", "command interrupted", "run the command again", contract.ErrInterrupted))
	}
	if len(args) > 0 && args[0] == "raw" {
		if jsonOutput {
			return render(stdout, stderr, true, invalid("raw lighthouse", "raw_json_unsupported", "raw lighthouse does not support global --json"))
		}
		return rawLighthouse(ctx, args, stdout, stderr, deps)
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
	case "report":
		return reportCommand(args[1:], stderr), false
	case "raw":
		return invalid("raw", "invalid_raw_command", "expected raw lighthouse -- <official flags>"), false
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
	if errors.Is(err, engine.ErrInvalidTarget) {
		return contract.Envelope{
			SchemaVersion: contract.SchemaVersion,
			Command:       "engine setup",
			Status:        contract.NeedsSetup,
			Data:          engineStatusData{Status: status},
			Error: contract.NewCommandError(
				"engine_target_invalid",
				"existing locked Lighthouse engine target is invalid",
				"stop and inspect the existing engine cache; do not delete, replace, or rerun setup automatically",
				err,
			),
		}, false
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
		RuntimeFlags:      bundle.ExpectedRuntimeFlags(),
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
	store, err := bundle.Open(*run)
	if err != nil {
		return invalidEvidence("inspect", "invalid_bundle", "inspect could not verify the evidence bundle", err)
	}
	summary, err := store.ValidateEvidence()
	if err != nil {
		return invalidEvidence("inspect", "invalid_bundle", "inspect could not verify the evidence bundle", err)
	}
	manifest := store.Manifest()
	protocol := store.Protocol()
	data := makeInspectData(manifest, protocol, summary)
	if !data.AggregateAvailable {
		report := failure("inspect", contract.Inconclusive, "aggregate_unavailable", "aggregate is unavailable for this evidence bundle", "collect at least three successful samples and inspect the retained evidence", nil)
		report.Data = data
		report.Warnings = []string{"aggregate unavailable"}
		return report
	}
	if summary.Status != string(contract.OK) || data.Status != string(contract.OK) {
		report := failure("inspect", contract.Inconclusive, "aggregate_not_final", "aggregate status is not final", "use a fully successful collection before comparison", nil)
		report.Data = data
		report.Warnings = append([]string(nil), data.Warnings...)
		return report
	}
	return contract.Envelope{SchemaVersion: contract.SchemaVersion, Command: "inspect", Status: contract.OK, Data: data, Warnings: append([]string(nil), data.Warnings...)}
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
	baselineStore, err := bundle.Open(*baseline)
	if err != nil {
		return invalidEvidence("compare", "invalid_baseline_bundle", "compare could not verify the baseline evidence bundle", err)
	}
	candidateStore, err := bundle.Open(*candidate)
	if err != nil {
		return invalidEvidence("compare", "invalid_candidate_bundle", "compare could not verify the candidate evidence bundle", err)
	}
	baselineSummary, err := baselineStore.ValidateEvidence()
	if err != nil {
		return invalidEvidence("compare", "invalid_baseline_bundle", "compare could not verify baseline evidence", err)
	}
	candidateSummary, err := candidateStore.ValidateEvidence()
	if err != nil {
		return invalidEvidence("compare", "invalid_candidate_bundle", "compare could not verify candidate evidence", err)
	}
	if baselineSummary == nil || candidateSummary == nil {
		return failure("compare", contract.Inconclusive, "aggregate_unavailable", "comparison requires finalized aggregate evidence", "collect at least three successful samples for each bundle", nil)
	}
	baselineManifest := baselineStore.Manifest()
	candidateManifest := candidateStore.Manifest()
	if baselineManifest.Status != string(contract.OK) || candidateManifest.Status != string(contract.OK) {
		return failure("compare", contract.Inconclusive, "incomplete_bundle", "comparison requires fully successful evidence bundles", "collect a fully successful bundle for each side", nil)
	}
	report, err := webcompare.Bundles(*baselineSummary, *candidateSummary, baselineStore.Protocol(), candidateStore.Protocol())
	if err != nil {
		var incompatible *webcompare.IncompatibleError
		if errors.As(err, &incompatible) {
			result := failure("compare", contract.IncompatibleProtocol, "incompatible_protocol", "measurement protocols are incompatible", "collect both bundles under one exact profile and runtime", err)
			result.Data = incompatibleFieldsData{Fields: append([]string(nil), incompatible.Fields...)}
			return result
		}
		if errors.Is(err, webcompare.ErrIncompleteBundle) {
			return failure("compare", contract.Inconclusive, "incomplete_bundle", "comparison requires complete successful aggregate evidence", "collect at least three successful samples for each bundle", err)
		}
		return failure("compare", contract.Inconclusive, "invalid_bundle", "comparison could not verify evidence integrity", "inspect both evidence bundles and collect new samples", err)
	}
	return contract.Envelope{SchemaVersion: contract.SchemaVersion, Command: "compare", Status: contract.OK, Data: report}
}

func reportCommand(args []string, stderr io.Writer) contract.Envelope {
	if len(args) > 0 && args[0] == "compare" {
		return comparisonReport(args[1:], stderr)
	}
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var runs stringListFlag
	flags.Var(&runs, "run", "verified evidence directory; repeat for another profile of the same target")
	out := flags.String("out", "", "new standalone HTML output")
	localeName := flags.String("locale", string(webreport.LocaleEnglish), "report language: en or zh-CN")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return contract.Envelope{SchemaVersion: contract.SchemaVersion, Command: "report", Status: contract.OK}
		}
		return invalid("report", "invalid_flags", "report accepts only repeatable --run, --out, and --locale flags")
	}
	if flags.NArg() != 0 {
		return invalid("report", "invalid_flags", "report accepts only repeatable --run, --out, and --locale flags")
	}
	if len(runs) == 0 || hasEmptyString(runs) {
		return invalid("report", "run_required", "report requires at least one run directory")
	}
	if *out == "" {
		return invalid("report", "out_required", "report requires a new HTML output path")
	}
	locale, err := webreport.ParseLocale(*localeName)
	if err != nil {
		return invalid("report", "unsupported_locale", "report locale must be en or zh-CN")
	}
	document, err := webreport.BuildSingle([]string(runs))
	if err != nil {
		return reportBuildFailure("report", err)
	}
	document.Locale = locale
	return writeReport("report", *out, document, []string(runs))
}

func comparisonReport(args []string, stderr io.Writer) contract.Envelope {
	flags := flag.NewFlagSet("report compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseline := flags.String("baseline", "", "baseline evidence directory")
	candidate := flags.String("candidate", "", "candidate evidence directory")
	out := flags.String("out", "", "new standalone HTML output")
	localeName := flags.String("locale", string(webreport.LocaleEnglish), "report language: en or zh-CN")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return contract.Envelope{SchemaVersion: contract.SchemaVersion, Command: "report compare", Status: contract.OK}
		}
		return invalid(
			"report compare",
			"invalid_flags",
			"report compare accepts only baseline, candidate, out, and locale flags",
		)
	}
	if flags.NArg() != 0 {
		return invalid(
			"report compare",
			"invalid_flags",
			"report compare accepts only baseline, candidate, out, and locale flags",
		)
	}
	if *baseline == "" || *candidate == "" {
		return invalid("report compare", "bundle_required", "report compare requires baseline and candidate directories")
	}
	if *out == "" {
		return invalid("report compare", "out_required", "report compare requires a new HTML output path")
	}
	locale, err := webreport.ParseLocale(*localeName)
	if err != nil {
		return invalid("report compare", "unsupported_locale", "report locale must be en or zh-CN")
	}
	document, err := webreport.BuildComparison(*baseline, *candidate)
	if err != nil {
		return reportBuildFailure("report compare", err)
	}
	document.Locale = locale
	return writeReport("report compare", *out, document, []string{*baseline, *candidate})
}

func writeReport(command, output string, document webreport.Document, bundlePaths []string) contract.Envelope {
	contents, err := webreport.Render(document)
	if err != nil {
		return failure(command, contract.ReportFailed, "report_render_failed", "HTML report rendering failed", "retain the verified evidence and retry with this webperf version", err)
	}
	if err := webreport.WriteNew(output, contents, bundlePaths); err != nil {
		return reportWriteFailure(command, err)
	}
	profiles := make([]string, len(document.Runs))
	for index, run := range document.Runs {
		profiles[index] = run.Profile
	}
	data := reportData{
		Kind:           document.Kind,
		ReportClass:    document.ReportClass,
		EvidenceStatus: document.EvidenceStatus,
		OutputCreated:  true,
		Profiles:       profiles,
	}
	warnings := []string(nil)
	if document.Notice != "" {
		warnings = []string{document.Notice}
	}
	return contract.Envelope{SchemaVersion: contract.SchemaVersion, Command: command, Status: contract.OK, Data: data, Warnings: warnings}
}

func reportWriteFailure(command string, err error) contract.Envelope {
	switch {
	case errors.Is(err, webreport.ErrUnsupportedPlatform):
		return failure(command, contract.ReportFailed, "report_platform_unsupported", "private HTML report output is unsupported on this platform", "generate the report on a supported Unix platform while retaining the verified evidence", err)
	case errors.Is(err, webreport.ErrCleanupFailed):
		return failure(command, contract.ReportFailed, "report_cleanup_failed", "HTML report output could not be cleaned up safely", "retain the verified evidence and inspect the caller-selected output directory before retrying", err)
	case errors.Is(err, webreport.ErrOutputInsideBundle):
		return invalid(command, "out_inside_bundle", "report output must be outside every input evidence bundle")
	case errors.Is(err, webreport.ErrOutputExists):
		return invalid(command, "out_exists", "report output already exists")
	case errors.Is(err, webreport.ErrInvalidOutput):
		return invalid(command, "invalid_out", "report output must be a new .html file in an existing directory")
	default:
		return failure(command, contract.ReportFailed, "report_write_failed", "HTML report could not be written", "retain the verified evidence and choose a writable new output path", err)
	}
}

func reportBuildFailure(command string, err error) contract.Envelope {
	switch {
	case errors.Is(err, webreport.ErrAggregateUnavailable):
		return failure(command, contract.Inconclusive, "aggregate_unavailable", "report requires a verified aggregate", "collect at least three successful samples and inspect the evidence", err)
	case errors.Is(err, webreport.ErrIncompleteBundle), errors.Is(err, webcompare.ErrIncompleteBundle):
		return failure(command, contract.Inconclusive, "incomplete_bundle", "comparison report requires fully successful evidence bundles", "use a diagnostic single report for verified PARTIAL evidence or collect complete bundles", err)
	case errors.Is(err, webreport.ErrInvalidEvidence):
		return invalidEvidence(command, "invalid_bundle", "report could not verify the evidence bundle", err)
	case errors.Is(err, webreport.ErrDuplicateProfile):
		return invalid(command, "duplicate_profile", "single report accepts only one bundle per profile")
	case errors.Is(err, webreport.ErrDifferentTarget):
		return invalid(command, "different_target", "single report requires every profile to use one requested URL")
	case errors.Is(err, webreport.ErrAmbiguousTarget):
		return invalid(command, "ambiguous_target", "multiple-profile reports require a requested URL without query parameters")
	}
	var incompatible *webcompare.IncompatibleError
	if errors.As(err, &incompatible) {
		result := failure(command, contract.IncompatibleProtocol, "incompatible_protocol", "measurement protocols are incompatible", "if a comparison is still needed, collect both bundles under one exact profile and runtime", err)
		result.Data = incompatibleFieldsData{Fields: append([]string(nil), incompatible.Fields...)}
		return result
	}
	return failure(command, contract.ReportFailed, "report_build_failed", "HTML report could not be built", "inspect the evidence bundles and retry with this webperf version", err)
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }

func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func hasEmptyString(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func rawLighthouse(ctx context.Context, args []string, stdout, stderr io.Writer, deps Dependencies) int {
	if len(args) < 3 || args[0] != "raw" || args[1] != "lighthouse" || args[2] != "--" {
		return render(stdout, stderr, false, invalid("raw lighthouse", "invalid_raw_command", "expected raw lighthouse -- <official flags>"))
	}
	runtimeInfo, err := manager(deps).Runtime(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return render(stdout, stderr, false, interrupted("raw lighthouse"))
		}
		return render(stdout, stderr, false, unavailable("raw lighthouse"))
	}
	runner := deps.RawRunner
	if runner == nil {
		runner = lockedRawRunner{}
	}
	_, _ = fmt.Fprintln(stderr, "webperf: raw lighthouse does not use profiles, aggregation, redaction, or comparison guarantees")
	result := runner.Run(ctx, runtimeInfo, append([]string(nil), args[3:]...), stdout, stderr)
	if ctx.Err() != nil {
		return contract.ExitInterrupted
	}
	if errors.Is(result.Err, engine.ErrRawChromePath) {
		_, _ = fmt.Fprintln(stderr, "webperf: raw lighthouse cannot override the resolved Chrome runtime")
	}
	if result.ExitCode != 0 {
		return result.ExitCode
	}
	if result.Err != nil {
		return contract.ExitGeneralError
	}
	return contract.ExitOK
}

func invalidEvidence(command, code, message string, cause error) contract.Envelope {
	return failure(command, contract.Inconclusive, code, message, "inspect the retained evidence and collect a new bundle", cause)
}

func aggregateAvailable(summary *bundle.Summary) bool {
	if summary == nil || summary.SuccessfulRuns < 3 || summary.SuccessfulRuns != len(summary.Samples) {
		return false
	}
	for _, item := range []stats.Distribution{
		summary.Metrics.PerformanceScore,
		summary.Metrics.FCP,
		summary.Metrics.LCP,
		summary.Metrics.SpeedIndex,
		summary.Metrics.TBT,
		summary.Metrics.CLS,
	} {
		if item.Count != summary.SuccessfulRuns {
			return false
		}
	}
	return true
}

func makeInspectData(manifest bundle.Manifest, protocol bundle.Protocol, summary *bundle.Summary) inspectData {
	data := inspectData{
		Status:        manifest.Status,
		RequestedURL:  inspectURL(manifest.RequestedURL),
		FinalURL:      inspectURL(manifest.FinalURL),
		Profile:       protocol.Profile,
		AttemptCounts: countAttempts(manifest.Attempts),
		Protocol: inspectProtocolData{
			FormFactor:        protocol.FormFactor,
			ThrottlingMethod:  protocol.ThrottlingMethod,
			ResolvedFlags:     append([]string(nil), protocol.ResolvedFlags...),
			RuntimeFlags:      append([]string(nil), protocol.RuntimeFlags...),
			LighthouseVersion: protocol.LighthouseVersion,
			NodeVersion:       protocol.NodeVersion,
			ChromeVersion:     protocol.ChromeVersion,
			OS:                protocol.OS,
			Arch:              protocol.Arch,
			Fingerprint:       protocol.Fingerprint,
		},
	}
	if summary == nil {
		return data
	}
	data.FinalURL = inspectURL(summary.FinalURL)
	if data.RequestedURL == "" {
		data.RequestedURL = inspectURL(summary.RequestedURL)
	}
	data.RequestedRuns = summary.RequestedRuns
	data.SuccessfulRuns = summary.SuccessfulRuns
	data.Metrics = &summary.Metrics
	data.Warnings = append([]string(nil), summary.Warnings...)
	data.AggregateAvailable = aggregateAvailable(summary)
	return data
}

func countAttempts(attempts []bundle.Attempt) map[string]int {
	counts := make(map[string]int)
	for _, attempt := range attempts {
		counts[attempt.Status]++
	}
	return counts
}

func inspectURL(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := safeurl.Validate(value)
	if err != nil {
		return ""
	}
	return safeurl.Display(parsed)
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
	} else if report.Command == "inspect" {
		if data, ok := report.Data.(inspectData); ok {
			renderHumanInspect(stdout, data)
		}
		if report.Error != nil {
			_, _ = fmt.Fprintf(stderr, "webperf: %s\n", report.Error.Message)
		}
	} else if report.Command == "compare" && report.Error == nil {
		if comparison, ok := report.Data.(webcompare.Report); ok {
			renderHumanCompare(stdout, comparison)
		}
	} else if (report.Command == "report" || report.Command == "report compare") && report.Error == nil {
		if data, ok := report.Data.(reportData); ok {
			renderHumanReport(stdout, data)
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

func renderHumanInspect(stdout io.Writer, data inspectData) {
	_, _ = fmt.Fprintf(stdout, "status: %s\n", data.Status)
	if data.Profile != "" {
		_, _ = fmt.Fprintf(stdout, "profile: %s\n", data.Profile)
	}
	if !data.AggregateAvailable {
		_, _ = fmt.Fprintln(stdout, "aggregate: unavailable")
		return
	}
	_, _ = fmt.Fprintln(stdout, "aggregate: available")
	_, _ = fmt.Fprintf(stdout, "official performance score: median=%g mad=%g\n", data.Metrics.PerformanceScore.Median, data.Metrics.PerformanceScore.MAD)
	_, _ = fmt.Fprintf(stdout, "LCP: median=%g mad=%g\n", data.Metrics.LCP.Median, data.Metrics.LCP.MAD)
	for _, warning := range data.Warnings {
		_, _ = fmt.Fprintf(stdout, "warning: %s\n", warning)
	}
}

func renderHumanCompare(stdout io.Writer, report webcompare.Report) {
	_, _ = fmt.Fprintf(stdout, "overall: %s\n", report.Overall)
	for _, metric := range report.Metrics {
		_, _ = fmt.Fprintf(stdout, "%s: %s (delta=%g floor=%g)\n", metric.Name, metric.Classification, metric.Delta, metric.MaterialityFloor)
	}
}

func renderHumanReport(stdout io.Writer, report reportData) {
	_, _ = fmt.Fprintln(stdout, "report: OK")
	_, _ = fmt.Fprintf(stdout, "kind: %s\n", report.Kind)
	_, _ = fmt.Fprintf(stdout, "class: %s\n", report.ReportClass)
	_, _ = fmt.Fprintln(stdout, "output: created at caller-selected path")
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
	return []string{"doctor", "profiles list", "engine status", "engine setup", "collect", "inspect", "compare", "report", "report compare", "raw lighthouse"}
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

type inspectData struct {
	Status             string              `json:"status"`
	RequestedURL       string              `json:"requestedUrl,omitempty"`
	FinalURL           string              `json:"finalUrl,omitempty"`
	Profile            string              `json:"profile"`
	RequestedRuns      int                 `json:"requestedRuns,omitempty"`
	SuccessfulRuns     int                 `json:"successfulRuns,omitempty"`
	AttemptCounts      map[string]int      `json:"attemptCounts,omitempty"`
	Protocol           inspectProtocolData `json:"protocol"`
	Metrics            *bundle.Metrics     `json:"metrics,omitempty"`
	Warnings           []string            `json:"warnings,omitempty"`
	AggregateAvailable bool                `json:"aggregateAvailable"`
}

type inspectProtocolData struct {
	FormFactor        string   `json:"formFactor"`
	ThrottlingMethod  string   `json:"throttlingMethod"`
	ResolvedFlags     []string `json:"resolvedFlags"`
	RuntimeFlags      []string `json:"runtimeFlags"`
	LighthouseVersion string   `json:"lighthouseVersion"`
	NodeVersion       string   `json:"nodeVersion"`
	ChromeVersion     string   `json:"chromeVersion"`
	OS                string   `json:"os"`
	Arch              string   `json:"arch"`
	Fingerprint       string   `json:"fingerprint"`
}

type incompatibleFieldsData struct {
	Fields []string `json:"incompatibleFields"`
}

type reportData struct {
	Kind           string   `json:"kind"`
	ReportClass    string   `json:"reportClass"`
	EvidenceStatus string   `json:"evidenceStatus"`
	OutputCreated  bool     `json:"outputCreated"`
	Profiles       []string `json:"profiles"`
}

func helpText() string {
	lines := []string{
		"webperf measures public web performance with explicit, repeatable protocols.",
		"",
		"Usage:",
		"  webperf [--json] <command>",
		"",
		"Commands:",
		"  doctor",
		"  profiles list",
		"  engine status",
		"  engine setup",
		"  collect --url <url> --profile <name> --runs 5 --out <dir>",
		"  inspect --run <dir>",
		"  compare --baseline <dir> --candidate <dir>",
		"  report --run <dir> [--run <dir> ...] --out <new.html> [--locale en|zh-CN]",
		"  report compare --baseline <dir> --candidate <dir> --out <new.html> [--locale en|zh-CN]",
		"  raw lighthouse -- <official flags>",
		"",
		"Available profiles:",
		"  " + strings.Join(profileNames(), "\n  "),
		"",
	}
	return strings.Join(lines, "\n")
}

func profileNames() []string {
	profiles := profile.List()
	names := make([]string, len(profiles))
	for i, item := range profiles {
		names[i] = item.Name
	}
	return names
}
