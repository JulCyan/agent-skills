package mediasync

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func loadDesired(ctx context.Context, opts commandOptions) (DesiredManifest, error) {
	if opts.sheetURL != "" || opts.spreadsheetToken != "" {
		raw, err := readFeishuCSV(ctx, opts)
		if err != nil {
			return DesiredManifest{}, err
		}
		rows, err := parseCSVRows(raw)
		if err != nil {
			return DesiredManifest{}, err
		}
		manifest, err := desiredFromTable(rows)
		if err != nil {
			return DesiredManifest{}, err
		}
		return finalizeDesiredManifest(manifest, "feishu", opts, true)
	}

	ext := strings.ToLower(filepath.Ext(opts.input))
	switch ext {
	case ".json":
		manifest, err := loadDesiredJSON(opts.input)
		if err != nil {
			return DesiredManifest{}, err
		}
		if manifest.Source == "" {
			manifest.Source = opts.input
		}
		return finalizeDesiredManifest(manifest, manifest.Source, opts, false)
	case ".csv":
		raw, err := os.ReadFile(opts.input)
		if err != nil {
			return DesiredManifest{}, err
		}
		rows, err := parseCSVRows(raw)
		if err != nil {
			return DesiredManifest{}, err
		}
		manifest, err := desiredFromTable(rows)
		if err != nil {
			return DesiredManifest{}, err
		}
		return finalizeDesiredManifest(manifest, opts.input, opts, true)
	case ".xlsx":
		rows, err := ReadXLSX(opts.input, opts.sheetName)
		if err != nil {
			return DesiredManifest{}, err
		}
		manifest, err := desiredFromTable(rows)
		if err != nil {
			return DesiredManifest{}, err
		}
		return finalizeDesiredManifest(manifest, opts.input, opts, true)
	default:
		return DesiredManifest{}, fmt.Errorf("不支持的输入格式 %q；请使用 .csv/.xlsx/.json", ext)
	}
}

func finalizeDesiredManifest(manifest DesiredManifest, source string, opts commandOptions, stripLegacyAlt bool) (DesiredManifest, error) {
	if manifest.Source == "" {
		manifest.Source = source
	}
	if stripLegacyAlt {
		dropInputAlt(&manifest)
	}
	if err := applyLocaleFilter(&manifest, opts.locales); err != nil {
		return DesiredManifest{}, err
	}
	return manifest, nil
}

func dropInputAlt(manifest *DesiredManifest) {
	for i := range manifest.Rows {
		manifest.Rows[i].Alt = ""
	}
}

type localeFilter struct {
	active  bool
	allowed map[string]bool
}

func parseLocaleFilter(spec string) (localeFilter, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return localeFilter{}, nil
	}
	filter := localeFilter{active: true, allowed: map[string]bool{}}
	for _, item := range splitCSV(spec) {
		if strings.EqualFold(item, "none") {
			if len(splitCSV(spec)) != 1 {
				return localeFilter{}, fmt.Errorf("--locales none 不能与其它 locale 混用")
			}
			return filter, nil
		}
		locale := normalizeLocale(item)
		if locale == "" {
			return localeFilter{}, fmt.Errorf("不支持的 locale %q；当前支持 en/de/jp/ja/fr 或 none", item)
		}
		filter.allowed[locale] = true
	}
	if len(filter.allowed) == 0 {
		return localeFilter{}, fmt.Errorf("--locales 不能为空")
	}
	return filter, nil
}

func applyLocaleFilter(manifest *DesiredManifest, spec string) error {
	filter, err := parseLocaleFilter(spec)
	if err != nil {
		return err
	}
	if !filter.active {
		return nil
	}
	for i := range manifest.Rows {
		if len(filter.allowed) == 0 {
			manifest.Rows[i].Alt = ""
		}
		manifest.Rows[i].Translations = filterTranslations(manifest.Rows[i].Translations, filter)
	}
	return nil
}

func filterTranslations(translations map[string]string, filter localeFilter) map[string]string {
	if len(translations) == 0 {
		return nil
	}
	out := map[string]string{}
	for locale, value := range translations {
		locale = normalizeLocale(locale)
		if locale != "" && filter.allowed[locale] && strings.TrimSpace(value) != "" {
			out[locale] = strings.TrimSpace(value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func readFeishuCSV(ctx context.Context, opts commandOptions) ([]byte, error) {
	if _, err := exec.LookPath("lark-cli"); err != nil {
		return nil, fmt.Errorf("NEEDS_SETUP: Sheet input requires the optional lark-cli dependency; install and authenticate lark-cli, or use a local input; local CSV/XLSX/JSON inputs remain available")
	}
	args := []string{"sheets", "+csv-get", "--format", "json", "--include-row-prefix=false", "--range", opts.sheetRange}
	if opts.sheetURL != "" {
		args = append(args, "--url", opts.sheetURL)
	} else {
		args = append(args, "--spreadsheet-token", opts.spreadsheetToken)
	}
	if opts.sheetName != "" {
		args = append(args, "--sheet-name", opts.sheetName)
	} else {
		args = append(args, "--sheet-id", opts.sheetID)
	}
	cmd := exec.CommandContext(ctx, "lark-cli", args...)
	raw, err := cmd.Output()
	if err != nil {
		var stderr bytes.Buffer
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr.Write(exitErr.Stderr)
		}
		return nil, fmt.Errorf("lark-cli sheets +csv-get failed: %w\n%s", err, redactSensitiveOutput(stderr.String(), feishuSheetRedactionSecrets(opts)...))
	}
	return extractFeishuAnnotatedCSV(raw, feishuSheetRedactionSecrets(opts)...)
}

func feishuSheetRedactionSecrets(opts commandOptions) []string {
	secrets := []string{opts.sheetURL, opts.spreadsheetToken}
	if opts.sheetURL != "" {
		if match := feishuSheetTokenRE.FindStringSubmatch(opts.sheetURL); len(match) == 2 {
			secrets = append(secrets, match[1])
		}
	}
	return secrets
}

func extractFeishuAnnotatedCSV(raw []byte, redactionSecrets ...string) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("lark-cli sheets +csv-get returned empty output")
	}
	if trimmed[0] != '{' {
		return raw, nil
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			AnnotatedCSV string `json:"annotated_csv"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, fmt.Errorf("解析 lark-cli sheets +csv-get JSON 输出失败: %w", err)
	}
	if !envelope.OK {
		if envelope.Error.Message != "" {
			return nil, fmt.Errorf("lark-cli sheets +csv-get failed: %s", redactSensitiveOutput(envelope.Error.Message, redactionSecrets...))
		}
		return nil, fmt.Errorf("lark-cli sheets +csv-get failed: %s", redactSensitiveOutput(envelope.Error.Type, redactionSecrets...))
	}
	if envelope.Data.AnnotatedCSV == "" {
		return nil, fmt.Errorf("lark-cli sheets +csv-get missing data.annotated_csv")
	}
	return []byte(envelope.Data.AnnotatedCSV), nil
}

func loadDesiredJSON(path string) (DesiredManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return DesiredManifest{}, err
	}
	var manifest DesiredManifest
	if err := json.Unmarshal(raw, &manifest); err == nil && len(manifest.Rows) > 0 {
		for i := range manifest.Rows {
			normalizeDesiredRow(&manifest.Rows[i])
		}
		return manifest, nil
	}
	var rows []DesiredRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return DesiredManifest{}, fmt.Errorf("解析 desired JSON 失败: %w", err)
	}
	for i := range rows {
		normalizeDesiredRow(&rows[i])
	}
	return DesiredManifest{Source: path, Rows: rows}, nil
}

func parseCSVRows(raw []byte) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i] = stripLarkRowPrefix(rows[i])
	}
	return rows, nil
}

var (
	larkRowPrefixRE    = regexp.MustCompile(`^\[row=(\d+)\]$`)
	feishuSheetTokenRE = regexp.MustCompile(`/sheets/([^/?#]+)`)
)

func stripLarkRowPrefix(row []string) []string {
	if len(row) == 0 {
		return row
	}
	if larkRowPrefixRE.MatchString(strings.TrimSpace(row[0])) {
		return row[1:]
	}
	return row
}

func desiredFromTable(rows [][]string) (DesiredManifest, error) {
	headerIdx := -1
	var header []string
	for i, row := range rows {
		if len(row) == 0 || isEmptyRow(row) {
			continue
		}
		header = row
		headerIdx = i
		break
	}
	if headerIdx < 0 {
		return DesiredManifest{}, fmt.Errorf("输入表格为空")
	}
	cols := map[string]int{}
	for i, cell := range header {
		if name := canonicalHeader(cell); name != "" {
			cols[name] = i
		}
	}
	if _, ok := cols["source"]; !ok {
		return DesiredManifest{}, fmt.Errorf("缺少必填列 source图片名")
	}

	var out []DesiredRow
	for i := headerIdx + 1; i < len(rows); i++ {
		row := rows[i]
		if isEmptyRow(row) {
			continue
		}
		source := cell(row, cols["source"])
		if source == "" {
			continue
		}
		rowNo := cellByColumn(row, cols, "row_no")
		if rowNo == "" {
			rowNo = fmt.Sprintf("%d", i+1)
		}
		target := cellByColumn(row, cols, "target")
		desired := DesiredRow{
			RowNo:          rowNo,
			SourceFilename: source,
			TargetFilename: target,
			Alt:            cellByColumn(row, cols, "alt"),
			Translations:   map[string]string{},
		}
		for name, idx := range cols {
			if strings.HasPrefix(name, "locale:") {
				locale := strings.TrimPrefix(name, "locale:")
				if value := cell(row, idx); value != "" {
					desired.Translations[locale] = value
				}
			}
		}
		normalizeDesiredRow(&desired)
		out = append(out, desired)
	}
	if len(out) == 0 {
		return DesiredManifest{}, fmt.Errorf("没有可处理的数据行")
	}
	return DesiredManifest{Rows: out}, nil
}

func normalizeDesiredRow(row *DesiredRow) {
	row.RowNo = strings.TrimSpace(row.RowNo)
	row.SourceFilename = strings.TrimSpace(row.SourceFilename)
	row.TargetFilename = strings.TrimSpace(row.TargetFilename)
	row.Alt = strings.TrimSpace(row.Alt)
	if row.TargetFilename == "" {
		row.TargetFilename = row.SourceFilename
	}
	row.FilenameChanged = row.SourceFilename != row.TargetFilename
	if len(row.Translations) == 0 {
		row.Translations = nil
		return
	}
	normalized := map[string]string{}
	for locale, value := range row.Translations {
		locale = normalizeLocale(locale)
		value = strings.TrimSpace(value)
		if locale != "" && value != "" {
			normalized[locale] = value
		}
	}
	if len(normalized) == 0 {
		row.Translations = nil
	} else {
		row.Translations = normalized
	}
}

func canonicalHeader(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "\ufeff")
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, "_", "")
	switch value {
	case "序号", "row", "rowno", "no", "编号":
		return "row_no"
	case "source图片名", "source", "sourcefilename", "sourcefile", "源图片名":
		return "source"
	case "target图片文件名", "target", "targetfilename", "targetfile", "目标图片名":
		return "target"
	}
	if locale := normalizeLocale(value); locale != "" {
		return "locale:" + locale
	}
	return ""
}

func normalizeLocale(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "jp":
		return "ja"
	case "en", "de", "fr", "ja":
		return strings.TrimSpace(strings.ToLower(value))
	default:
		return ""
	}
}

func cell(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func cellByColumn(row []string, cols map[string]int, name string) string {
	idx, ok := cols[name]
	if !ok {
		return ""
	}
	return cell(row, idx)
}

func isEmptyRow(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}
