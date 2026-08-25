package report

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julcyan/agent-skills/skills/web-performance-lab/scripts/webperf-go/internal/stats"
)

func TestRenderEscapesEvidenceTextAndUsesNoActiveContent(t *testing.T) {
	document := Document{
		Kind:           KindSingle,
		Title:          "Web Performance Evidence Report",
		ReportClass:    "VERIFIED LAB EVIDENCE",
		EvidenceStatus: "OK",
		TargetURL:      `https://example.test/?token=REDACTED\"><script>alert(1)</script>`,
		Runs: []Run{{
			ID:               "profile-mobile-lab-v1",
			EvidenceStatus:   "OK",
			RequestedURL:     "https://example.test/",
			FinalURL:         "https://example.test/landing",
			Profile:          "mobile-lab-v1",
			FormFactor:       "mobile",
			ThrottlingMethod: "simulate",
			RequestedRuns:    5,
			SuccessfulRuns:   5,
			Metrics: []Metric{{
				Key: "lcp", Label: "Largest Contentful Paint",
				Distribution: stats.Distribution{Count: 5, Median: 1500, MAD: 100, IQR: 200, Min: 1200, Max: 1800},
				Points:       []Point{{Attempt: 1, Value: 1500, Position: 50}}, MedianPosition: 50,
			}},
			Representative: Representative{Attempt: 1, LCP: 1500},
			Protocol:       Protocol{Fingerprint: strings.Repeat("a", 64)},
		}},
		CannotClaims: []string{"Field performance"},
	}

	contents, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "<script") || strings.Contains(string(contents), "alert(1)</script>") {
		t.Fatalf("rendered active evidence content: %s", contents)
	}
	for _, required := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "LCP element identity is withheld", "Content-Security-Policy"} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("rendered HTML missing %q", required)
		}
	}
}

func TestRenderSimplifiedChineseLocalizesHumanFacingSpeedIndexCopy(t *testing.T) {
	document := Document{
		Locale:         LocaleSimplifiedChinese,
		Kind:           KindSingle,
		Title:          "Web Performance Evidence Report",
		ReportClass:    "VERIFIED LAB EVIDENCE",
		EvidenceStatus: "OK",
		TargetURL:      "https://example.test/",
		Runs: []Run{{
			ID:             "profile-mobile-lab-v1",
			EvidenceStatus: "OK",
			RequestedURL:   "https://example.test/",
			FinalURL:       "https://example.test/landing",
			Profile:        "mobile-lab-v1",
			RequestedRuns:  1,
			SuccessfulRuns: 1,
			Warnings:       []string{"high dispersion for Speed Index"},
			Attempts:       []Attempt{{Number: 1, Status: "OK", Sample: &Sample{SpeedIndex: 1500}}},
			Representative: Representative{Attempt: 1, LCP: 1500},
			Protocol:       Protocol{Fingerprint: strings.Repeat("a", 64)},
		}},
	}

	contents, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"速度指数（Speed Index）的离散度较高",
		"<th>速度指数（Speed Index）</th>",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("simplified Chinese HTML missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"Speed Index 的离散度较高",
		"<th>Speed Index</th>",
	} {
		if strings.Contains(string(contents), forbidden) {
			t.Fatalf("simplified Chinese HTML contains untranslated copy %q", forbidden)
		}
	}
}

func TestLocalizedWarningTranslatesDispersionMetricLabels(t *testing.T) {
	tests := map[string]string{
		"Performance Score": "性能得分的离散度较高",
		"FCP":               "首次内容绘制（FCP）的离散度较高",
		"LCP":               "最大内容绘制（LCP）的离散度较高",
		"Speed Index":       "速度指数（Speed Index）的离散度较高",
		"TBT":               "总阻塞时间（TBT）的离散度较高",
		"CLS":               "累积布局偏移（CLS）的离散度较高",
	}
	for metric, expected := range tests {
		metric := metric
		expected := expected
		t.Run(metric, func(t *testing.T) {
			warning := localizedWarning("high dispersion for " + metric)
			if warning != expected {
				t.Fatalf("warning=%q want=%q", warning, expected)
			}
		})
	}
}

func TestLocalizeDocumentDoesNotMutateNestedInput(t *testing.T) {
	document := Document{
		Locale: LocaleSimplifiedChinese,
		Kind:   KindComparison,
		Runs: []Run{{
			Role:     "Baseline",
			Metrics:  []Metric{{Key: "lcp", Label: "Largest Contentful Paint"}},
			Warnings: []string{"high dispersion for LCP"},
			Representative: Representative{
				LCPBreakdown:  []LCPPhase{{ID: "timeToFirstByte", Label: "Time to First Byte"}},
				Opportunities: []Opportunity{{ID: "unused-css-rules", Label: "Reduce unused CSS"}},
				Signals:       []DiagnosticSignal{{ID: "bootup-time", Label: "JavaScript execution time"}},
			},
		}},
		Comparison: &Comparison{Metrics: []ComparisonMetric{{
			Key:   "speedIndex",
			Label: "Speed Index",
		}}},
	}

	localized, _, err := localizeDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	localized.Runs[0].Metrics[0].Label = "changed"
	localized.Runs[0].Warnings[0] = "changed"
	localized.Runs[0].Representative.LCPBreakdown[0].Label = "changed"
	localized.Runs[0].Representative.Opportunities[0].Label = "changed"
	localized.Runs[0].Representative.Signals[0].Label = "changed"
	localized.Comparison.Metrics[0].Label = "changed"

	if document.Runs[0].Metrics[0].Label != "Largest Contentful Paint" ||
		document.Runs[0].Warnings[0] != "high dispersion for LCP" ||
		document.Runs[0].Representative.LCPBreakdown[0].Label != "Time to First Byte" ||
		document.Runs[0].Representative.Opportunities[0].Label != "Reduce unused CSS" ||
		document.Runs[0].Representative.Signals[0].Label != "JavaScript execution time" ||
		document.Comparison.Metrics[0].Label != "Speed Index" {
		t.Fatalf("localization mutated the input document: %+v", document)
	}
}

func TestWriteNewIsPrivateDeterministicAndNoReplace(t *testing.T) {
	requireReportOutput(t)
	contents := []byte("<!doctype html><title>verified</title>")
	output := filepath.Join(t.TempDir(), "report.html")
	if err := WriteNew(output, contents, nil); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, contents) {
		t.Fatalf("contents=%q", actual)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%#o", info.Mode().Perm())
	}
	if err := WriteNew(output, []byte("replacement"), nil); !errors.Is(err, ErrOutputExists) {
		t.Fatalf("overwrite error=%v", err)
	}
	actual, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, contents) {
		t.Fatalf("existing contents changed: %q", actual)
	}
}

func TestWriteNewRequiresHTMLAndExistingParent(t *testing.T) {
	requireReportOutput(t)
	root := t.TempDir()
	for _, output := range []string{
		filepath.Join(root, "report.txt"),
		filepath.Join(root, "missing", "report.html"),
	} {
		if err := WriteNew(output, []byte("report"), nil); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("output=%q error=%v", output, err)
		}
	}
}

func TestWriteNewDoesNotFollowExistingSymlink(t *testing.T) {
	requireReportOutput(t)
	root := t.TempDir()
	target := filepath.Join(root, "target.html")
	output := filepath.Join(root, "report.html")
	if err := os.Symlink(target, output); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WriteNew(output, []byte("report"), nil); !errors.Is(err, ErrOutputExists) {
		t.Fatalf("symlink error=%v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}
}

func TestWriteNewRejectsDirectAndSymlinkedBundleLocations(t *testing.T) {
	requireReportOutput(t)
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{
		filepath.Join(bundle, "report.html"),
		filepath.Join(root, "bundle-link", "report.html"),
	} {
		if strings.Contains(output, "bundle-link") {
			if err := os.Symlink(bundle, filepath.Dir(output)); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}
		if err := WriteNew(output, []byte("report"), []string{bundle}); !errors.Is(err, ErrOutputInsideBundle) {
			t.Fatalf("output=%q error=%v", output, err)
		}
		if _, err := os.Lstat(filepath.Join(bundle, "report.html")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("bundle was modified: %v", err)
		}
	}
}

func TestWriteNewRejectsCaseAliasOfBundleOnCaseInsensitiveFilesystem(t *testing.T) {
	requireReportOutput(t)
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "BUNDLE")
	bundleInfo, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if err != nil || !os.SameFile(bundleInfo, aliasInfo) {
		t.Skip("filesystem is case-sensitive")
	}
	if err := WriteNew(filepath.Join(alias, "report.html"), []byte("report"), []string{bundle}); !errors.Is(err, ErrOutputInsideBundle) {
		t.Fatalf("case alias error=%v", err)
	}
}

func TestWriteNewRejectsParentReplacementBeforeAtomicCommit(t *testing.T) {
	requireReportOutput(t)
	root := t.TempDir()
	parent := filepath.Join(root, "reports")
	movedParent := filepath.Join(root, "reports-original")
	bundle := filepath.Join(root, "bundle")
	for _, directory := range []string{parent, bundle} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalHook := beforeReportCommit
	beforeReportCommit = func() {
		if err := os.Rename(parent, movedParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(bundle, parent); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeReportCommit = originalHook })

	output := filepath.Join(parent, "report.html")
	if err := WriteNew(output, []byte("complete report"), []string{bundle}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("parent replacement error=%v", err)
	}
	for _, candidate := range []string{
		filepath.Join(movedParent, "report.html"),
		filepath.Join(bundle, "report.html"),
	} {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected final output %q: %v", candidate, err)
		}
	}
	entries, err := os.ReadDir(movedParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary output was not cleaned up: %v", entries)
	}
}

func TestWriteNewRollsBackWhenBoundParentMovesIntoBundleAfterLink(t *testing.T) {
	requireReportOutput(t)
	root := t.TempDir()
	parent := filepath.Join(root, "reports")
	bundle := filepath.Join(root, "bundle")
	movedParent := filepath.Join(bundle, "moved-reports")
	for _, directory := range []string{parent, bundle} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalHook := afterReportLink
	afterReportLink = func() {
		if err := os.Rename(parent, movedParent); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterReportLink = originalHook })

	output := filepath.Join(parent, "report.html")
	if err := WriteNew(output, []byte("complete report"), []string{bundle}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("post-link parent move error=%v", err)
	}
	entries, err := os.ReadDir(movedParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("committed or temporary output remained in bundle: %v", entries)
	}
}

func TestWriteNewCleansTemporaryFileAfterWriteFailure(t *testing.T) {
	requireReportOutput(t)
	parent := t.TempDir()
	originalWriter := writeReportTemporary
	writeReportTemporary = func(file *os.File, _ []byte) error {
		if _, err := file.Write([]byte("partial")); err != nil {
			return err
		}
		return errors.New("injected write failure")
	}
	t.Cleanup(func() { writeReportTemporary = originalWriter })

	output := filepath.Join(parent, "report.html")
	if err := WriteNew(output, []byte("complete report"), nil); err == nil {
		t.Fatal("injected write failure was ignored")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial final output exists: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary output was not cleaned up: %v", entries)
	}
}

func TestWriteNewDoesNotReportSuccessWhenTemporaryCleanupFails(t *testing.T) {
	requireReportOutput(t)
	parent := t.TempDir()
	originalRemover := removeTemporaryReport
	removeTemporaryReport = func(root *os.Root, name string) error {
		if err := root.Remove(name); err != nil {
			return err
		}
		return errors.New("injected cleanup failure after removal")
	}
	t.Cleanup(func() { removeTemporaryReport = originalRemover })

	output := filepath.Join(parent, "report.html")
	if err := WriteNew(output, []byte("complete report"), nil); !errors.Is(err, ErrCleanupFailed) {
		t.Fatal("temporary cleanup failure was reported as success")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final output remained after cleanup failure: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cleanup failure left report artifacts: %v", entries)
	}
}

func TestWriteNewReportsCleanupFailureAfterPostLinkParentMove(t *testing.T) {
	requireReportOutput(t)
	root := t.TempDir()
	parent := filepath.Join(root, "reports")
	bundle := filepath.Join(root, "bundle")
	movedParent := filepath.Join(bundle, "moved-reports")
	for _, directory := range []string{parent, bundle} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalHook := afterReportLink
	originalRemover := removeTemporaryReport
	afterReportLink = func() {
		if err := os.Rename(parent, movedParent); err != nil {
			t.Fatal(err)
		}
	}
	removeTemporaryReport = func(root *os.Root, name string) error {
		if err := root.Remove(name); err != nil {
			return err
		}
		return errors.New("injected cleanup failure after removal")
	}
	t.Cleanup(func() {
		afterReportLink = originalHook
		removeTemporaryReport = originalRemover
	})

	output := filepath.Join(parent, "report.html")
	err := WriteNew(output, []byte("complete report"), []string{bundle})
	if !errors.Is(err, ErrInvalidOutput) || !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("post-link cleanup error=%v", err)
	}
	entries, readErr := os.ReadDir(movedParent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("post-link cleanup left bundle artifacts: %v", entries)
	}
}

func TestWriteNewDetectsFinalReplacementDuringTemporaryCleanup(t *testing.T) {
	requireReportOutput(t)
	parent := t.TempDir()
	output := filepath.Join(parent, "report.html")
	originalRemover := removeTemporaryReport
	removeTemporaryReport = func(root *os.Root, name string) error {
		if err := root.Remove(name); err != nil {
			return err
		}
		if err := root.Remove("report.html"); err != nil {
			return err
		}
		replacement, err := root.OpenFile("report.html", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := replacement.Write([]byte("foreign replacement")); err != nil {
			_ = replacement.Close()
			return err
		}
		return replacement.Close()
	}
	t.Cleanup(func() { removeTemporaryReport = originalRemover })

	err := WriteNew(output, []byte("complete report"), nil)
	if err == nil || !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("final replacement error=%v", err)
	}
	contents, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "foreign replacement" {
		t.Fatalf("foreign final replacement changed: %q", contents)
	}
}

func TestWriteNewDoesNotRetryRemovalAfterIdentityCanChange(t *testing.T) {
	requireReportOutput(t)
	parent := t.TempDir()
	originalRemover := removeTemporaryReport
	removeTemporaryReport = func(root *os.Root, name string) error {
		if err := root.Remove(name); err != nil {
			return err
		}
		replacement, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := replacement.Write([]byte("foreign temporary replacement")); err != nil {
			_ = replacement.Close()
			return err
		}
		if err := replacement.Close(); err != nil {
			return err
		}
		return errors.New("injected removal failure after replacement")
	}
	t.Cleanup(func() { removeTemporaryReport = originalRemover })

	output := filepath.Join(parent, "report.html")
	if err := WriteNew(output, []byte("complete report"), nil); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("replacement cleanup error=%v", err)
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final output remained after cleanup failure: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("foreign replacement count=%d entries=%v", len(entries), entries)
	}
	contents, err := os.ReadFile(filepath.Join(parent, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "foreign temporary replacement" {
		t.Fatalf("foreign temporary replacement changed: %q", contents)
	}
}

func TestWriteNewRollsBackWhenDirectorySyncFails(t *testing.T) {
	requireReportOutput(t)
	parent := t.TempDir()
	originalSync := syncReportParent
	syncCalls := 0
	syncReportParent = func(root *os.Root) error {
		syncCalls++
		if syncCalls == 1 {
			return errors.New("injected directory sync failure")
		}
		return syncReportDirectory(root)
	}
	t.Cleanup(func() { syncReportParent = originalSync })

	output := filepath.Join(parent, "report.html")
	if err := WriteNew(output, []byte("complete report"), nil); err == nil {
		t.Fatal("directory sync failure was reported as success")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory sync failure left report artifacts: %v", entries)
	}
}

func TestWriteNewReportsCleanupFailureWhenCleanupSyncFails(t *testing.T) {
	requireReportOutput(t)
	parent := t.TempDir()
	originalSync := syncReportParent
	syncCalls := 0
	syncReportParent = func(root *os.Root) error {
		syncCalls++
		if syncCalls == 2 {
			return errors.New("injected cleanup sync failure")
		}
		return syncReportDirectory(root)
	}
	t.Cleanup(func() { syncReportParent = originalSync })

	output := filepath.Join(parent, "report.html")
	if err := WriteNew(output, []byte("complete report"), nil); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("cleanup sync error=%v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cleanup sync failure left report artifacts: %v", entries)
	}
}

func requireReportOutput(t *testing.T) {
	t.Helper()
	if !OutputSupported() {
		t.Skip("private report output is unsupported on this platform")
	}
}

func TestInvalidEvidenceTimestampIsNotEchoed(t *testing.T) {
	if got := dateTime(LocaleEnglish, `<img src=x onerror=alert(1)>`); got != "Unavailable" {
		t.Fatalf("invalid timestamp=%q", got)
	}
}
