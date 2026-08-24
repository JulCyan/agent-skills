package report

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrOutputExists        = errors.New("report output already exists")
	ErrInvalidOutput       = errors.New("invalid report output path")
	ErrOutputInsideBundle  = errors.New("report output is inside an input evidence bundle")
	ErrUnsupportedPlatform = errors.New("private report output is unsupported on this platform")
	ErrCleanupFailed       = errors.New("report output cleanup failed")

	beforeReportCommit    = func() {}
	afterReportLink       = func() {}
	removeTemporaryReport = func(root *os.Root, name string) error {
		return root.Remove(name)
	}
	syncReportParent     = syncReportDirectory
	writeReportTemporary = func(file *os.File, contents []byte) error {
		written, err := io.Copy(file, bytes.NewReader(contents))
		if err != nil {
			return err
		}
		if written != int64(len(contents)) {
			return io.ErrShortWrite
		}
		return file.Sync()
	}
)

//go:embed template.html
var reportTemplate string

func Render(document Document) ([]byte, error) {
	if (document.Kind != KindSingle && document.Kind != KindComparison) || len(document.Runs) == 0 {
		return nil, errors.New("invalid report document")
	}
	localized, locale, err := localizeDocument(document)
	if err != nil {
		return nil, err
	}
	functions := template.FuncMap{
		"classLabel": func(value string) string {
			return classLabel(locale, value)
		},
		"classToken": classToken,
		"dateTime": func(value string) string {
			return dateTime(locale, value)
		},
		"distributionLabel": func(label, key string, minimum, maximum float64) string {
			return distributionLabel(locale, label, key, minimum, maximum)
		},
		"metricDelta": metricDelta,
		"metricValue": metricValue,
		"opportunityValue": func(opportunity Opportunity) string {
			return opportunityValue(locale, opportunity)
		},
		"signalValue": signalValue,
		"statusToken": statusToken,
	}
	parsed, err := template.New("webperf-report").Funcs(functions).Parse(reportTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse report template: %w", err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, localized); err != nil {
		return nil, fmt.Errorf("render report template: %w", err)
	}
	return output.Bytes(), nil
}

// WriteNew atomically commits one private HTML file outside every input bundle.
// The bound parent descriptor is revalidated immediately before the no-replace
// link, so aliases and path replacement cannot redirect the final write.
func WriteNew(outputPath string, contents []byte, bundlePaths []string) error {
	if !privateReportOutputSupported {
		return ErrUnsupportedPlatform
	}
	if outputPath == "" || strings.ToLower(filepath.Ext(outputPath)) != ".html" {
		return ErrInvalidOutput
	}
	clean, err := filepath.Abs(filepath.Clean(outputPath))
	if err != nil {
		return ErrInvalidOutput
	}
	if clean == "." || filepath.Base(clean) == "." || filepath.Base(clean) == string(filepath.Separator) {
		return ErrInvalidOutput
	}
	parent := filepath.Dir(clean)
	root, parentInfo, err := bindOutputParent(parent, bundlePaths)
	if err != nil {
		return err
	}
	defer root.Close()
	base := filepath.Base(clean)
	if _, err := root.Lstat(base); err == nil {
		return ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: output target is unavailable", ErrInvalidOutput)
	}

	temporaryName, file, err := createTemporaryReport(root)
	if err != nil {
		return err
	}
	temporaryInfo, statErr := file.Stat()
	if statErr != nil {
		closeErr := file.Close()
		return errors.Join(
			fmt.Errorf("stat created temporary report: %w", statErr),
			closeErr,
			cleanupUnverifiedTemporary(root, temporaryName),
		)
	}
	failBeforeCommit := func(primary error) error {
		return errors.Join(primary, cleanupReportArtifacts(root, "", nil, temporaryName, temporaryInfo))
	}
	if err := file.Chmod(0o600); err != nil {
		return failBeforeCommit(errors.Join(fmt.Errorf("set temporary report permissions: %w", err), file.Close()))
	}
	writeErr := writeReportTemporary(file, contents)
	writtenInfo, writtenStatErr := file.Stat()
	closeErr := file.Close()
	if writeErr != nil {
		return failBeforeCommit(fmt.Errorf("write temporary report: %w", writeErr))
	}
	if writtenStatErr != nil {
		return failBeforeCommit(fmt.Errorf("stat temporary report: %w", writtenStatErr))
	}
	if closeErr != nil {
		return failBeforeCommit(fmt.Errorf("close temporary report: %w", closeErr))
	}
	if !os.SameFile(temporaryInfo, writtenInfo) || !validPrivateReportFile(writtenInfo) {
		return failBeforeCommit(errors.New("temporary report identity is invalid"))
	}
	boundTemporaryInfo, err := root.Lstat(temporaryName)
	if err != nil || !os.SameFile(temporaryInfo, boundTemporaryInfo) {
		return failBeforeCommit(errors.New("temporary report changed before commit"))
	}

	beforeReportCommit()
	if err := verifyOutputParent(parent, root, parentInfo, bundlePaths); err != nil {
		return failBeforeCommit(err)
	}
	if err := root.Link(temporaryName, base); err != nil {
		if errors.Is(err, os.ErrExist) {
			return failBeforeCommit(ErrOutputExists)
		}
		return failBeforeCommit(fmt.Errorf("commit report output: %w", err))
	}
	failAfterCommit := func(primary error) error {
		return errors.Join(primary, cleanupReportArtifacts(root, base, temporaryInfo, temporaryName, temporaryInfo))
	}
	afterReportLink()
	if err := verifyOutputParent(parent, root, parentInfo, bundlePaths); err != nil {
		return failAfterCommit(err)
	}
	committedInfo, err := root.Lstat(base)
	if err != nil || !os.SameFile(temporaryInfo, committedInfo) {
		return failAfterCommit(errors.New("report output changed during commit"))
	}
	if err := syncReportParent(root); err != nil {
		return failAfterCommit(fmt.Errorf("sync committed report: %w", err))
	}
	if err := cleanupReportArtifacts(root, "", nil, temporaryName, temporaryInfo); err != nil {
		return errors.Join(err, cleanupReportArtifacts(root, base, temporaryInfo, "", nil))
	}
	if err := verifyOutputParent(parent, root, parentInfo, bundlePaths); err != nil {
		return errors.Join(err, cleanupReportArtifacts(root, base, temporaryInfo, "", nil))
	}
	finalInfo, err := root.Lstat(base)
	if err != nil || !os.SameFile(temporaryInfo, finalInfo) || !validPrivateReportFile(finalInfo) {
		return errors.Join(errors.New("report output changed after commit"), cleanupReportArtifacts(root, base, temporaryInfo, "", nil))
	}
	return nil
}

// OutputSupported reports whether the current platform can enforce the private
// report-output contract, including Unix owner-only file modes.
func OutputSupported() bool { return privateReportOutputSupported }

func bindOutputParent(parent string, bundlePaths []string) (*os.Root, os.FileInfo, error) {
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("%w: output parent is unavailable", ErrInvalidOutput)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: output parent is unavailable", ErrInvalidOutput)
	}
	if err := verifyOutputParent(parent, root, info, bundlePaths); err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	return root, info, nil
}

func verifyOutputParent(parent string, root *os.Root, expected os.FileInfo, bundlePaths []string) error {
	bound, err := root.Stat(".")
	if err != nil || !bound.IsDir() || !os.SameFile(expected, bound) {
		return fmt.Errorf("%w: output parent changed", ErrInvalidOutput)
	}
	current, err := os.Stat(parent)
	if err != nil || !current.IsDir() || !os.SameFile(expected, current) {
		return fmt.Errorf("%w: output parent changed", ErrInvalidOutput)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("%w: output parent is unavailable", ErrInvalidOutput)
	}
	return ensureOutsideBundles(resolvedParent, bundlePaths)
}

func ensureOutsideBundles(resolvedParent string, bundlePaths []string) error {
	for _, bundlePath := range bundlePaths {
		absoluteBundle, err := filepath.Abs(bundlePath)
		if err != nil {
			return ErrInvalidOutput
		}
		resolvedBundle, err := filepath.EvalSymlinks(absoluteBundle)
		if err != nil {
			return ErrInvalidOutput
		}
		bundleInfo, err := os.Stat(resolvedBundle)
		if err != nil || !bundleInfo.IsDir() {
			return ErrInvalidOutput
		}
		for ancestor := resolvedParent; ; ancestor = filepath.Dir(ancestor) {
			ancestorInfo, err := os.Stat(ancestor)
			if err != nil {
				return ErrInvalidOutput
			}
			if os.SameFile(bundleInfo, ancestorInfo) {
				return ErrOutputInsideBundle
			}
			if parent := filepath.Dir(ancestor); parent == ancestor {
				break
			}
		}
	}
	return nil
}

func createTemporaryReport(root *os.Root) (string, *os.File, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", nil, fmt.Errorf("create temporary report token: %w", err)
		}
		name := ".webperf-report-" + hex.EncodeToString(token[:]) + ".tmp"
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("create temporary report: %w", err)
		}
	}
	return "", nil, errors.New("create temporary report: collision limit reached")
}

func cleanupReportArtifacts(root *os.Root, finalName string, finalInfo os.FileInfo, temporaryName string, temporaryInfo os.FileInfo) error {
	var cleanupErrors []error
	if finalName != "" {
		if err := removeBoundReport(root, finalName, finalInfo, func(root *os.Root, name string) error {
			return root.Remove(name)
		}); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove committed report: %w", err))
		}
	}
	if temporaryName != "" {
		if err := removeBoundReport(root, temporaryName, temporaryInfo, removeTemporaryReport); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove temporary report: %w", err))
		}
	}
	if err := syncReportParent(root); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("sync report cleanup: %w", err))
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		return errors.Join(ErrCleanupFailed, err)
	}
	return nil
}

func removeBoundReport(root *os.Root, name string, expected os.FileInfo, remove func(*os.Root, string) error) error {
	current, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat report artifact: %w", err)
	}
	if expected == nil || !os.SameFile(expected, current) {
		return errors.New("report artifact identity changed")
	}
	return remove(root, name)
}

func cleanupUnverifiedTemporary(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return errors.Join(ErrCleanupFailed, fmt.Errorf("stat unverified temporary report: %w", err))
	}
	if !info.Mode().IsRegular() {
		return errors.Join(ErrCleanupFailed, errors.New("unverified temporary report is not regular"))
	}
	return cleanupReportArtifacts(root, "", nil, name, info)
}

func metricValue(key string, value float64) string {
	switch key {
	case "performanceScore":
		return strconv.FormatFloat(value, 'f', 0, 64)
	case "cls":
		return strconv.FormatFloat(value, 'f', 3, 64)
	default:
		return milliseconds(value)
	}
}

func metricDelta(key string, value float64) string {
	prefix := ""
	if value > 0 {
		prefix = "+"
	}
	switch key {
	case "performanceScore":
		return prefix + strconv.FormatFloat(value, 'f', 1, 64) + " pts"
	case "cls":
		return prefix + strconv.FormatFloat(value, 'f', 3, 64)
	default:
		return prefix + milliseconds(value)
	}
}

func milliseconds(value float64) string {
	abs := value
	if abs < 0 {
		abs = -abs
	}
	if abs >= 1000 {
		return strconv.FormatFloat(value/1000, 'f', 2, 64) + " s"
	}
	return strconv.FormatFloat(value, 'f', 0, 64) + " ms"
}

func opportunityValue(locale Locale, opportunity Opportunity) string {
	messages := messagesFor(locale)
	parts := make([]string, 0, 2)
	if opportunity.SavingsMS > 0 {
		parts = append(parts, milliseconds(opportunity.SavingsMS)+" "+messages.EstimatedTimeSavings)
	}
	if opportunity.SavingsBytes > 0 {
		parts = append(parts, bytesValue(opportunity.SavingsBytes)+" "+messages.EstimatedTransferSavings)
	}
	return strings.Join(parts, " · ")
}

func signalValue(signal DiagnosticSignal) string {
	switch signal.Unit {
	case "ms":
		return milliseconds(signal.Value)
	case "bytes":
		return bytesValue(signal.Value)
	case "count":
		return strconv.FormatFloat(signal.Value, 'f', 0, 64)
	default:
		return strconv.FormatFloat(signal.Value, 'f', 2, 64)
	}
}

func bytesValue(value float64) string {
	const (
		kib = 1024
		mib = 1024 * kib
	)
	switch {
	case value >= mib:
		return strconv.FormatFloat(value/mib, 'f', 2, 64) + " MiB"
	case value >= kib:
		return strconv.FormatFloat(value/kib, 'f', 1, 64) + " KiB"
	default:
		return strconv.FormatFloat(value, 'f', 0, 64) + " B"
	}
}

func classLabel(locale Locale, value string) string {
	if locale == LocaleSimplifiedChinese {
		switch value {
		case "improvement":
			return "改善 · IMPROVEMENT"
		case "regression":
			return "回退 · REGRESSION"
		case "mixed":
			return "混合 · MIXED"
		case "no_material_change":
			return "无实质变化 · NO MATERIAL CHANGE"
		}
	}
	return strings.ToUpper(strings.ReplaceAll(value, "_", " "))
}

func distributionLabel(locale Locale, label, key string, minimum, maximum float64) string {
	minimumValue := metricValue(key, minimum)
	maximumValue := metricValue(key, maximum)
	if locale == LocaleSimplifiedChinese {
		return label + " 各次测量分布，从 " + minimumValue + " 到 " + maximumValue
	}
	return label + " run distribution from " + minimumValue + " to " + maximumValue
}

func classToken(value string) string {
	switch value {
	case "improvement":
		return "good"
	case "regression":
		return "bad"
	case "mixed":
		return "warn"
	case "no_material_change":
		return "neutral"
	default:
		return "neutral"
	}
}

func statusToken(value string) string {
	switch value {
	case "VERIFIED LAB EVIDENCE", "VERIFIED COMPARISON", "OK":
		return "good"
	case "DIAGNOSTIC ONLY", "PARTIAL":
		return "warn"
	default:
		return "neutral"
	}
}

func dateTime(locale Locale, value string) string {
	if value == "" {
		return messagesFor(locale).Unavailable
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return messagesFor(locale).Unavailable
	}
	return parsed.UTC().Format("2006-01-02 15:04:05 UTC")
}
