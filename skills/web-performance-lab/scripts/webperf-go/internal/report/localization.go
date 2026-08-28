package report

import (
	"errors"
	"fmt"
	"strings"
)

var ErrUnsupportedLocale = errors.New("unsupported report locale")

type Locale string

const (
	LocaleEnglish           Locale = "en"
	LocaleSimplifiedChinese Locale = "zh-CN"
)

// Messages contains human-facing template copy. Protocol identifiers, metric
// keys, evidence statuses, and report classes remain canonical in every locale.
type Messages struct {
	HeroEyebrow                string
	Target                     string
	EvidenceStatus             string
	MeasurementStart           string
	MeasurementFinish          string
	ReportSections             string
	StrictAnalysis             string
	ComparisonVerdict          string
	PossibleClassifications    string
	Metric                     string
	BaselineMedian             string
	CandidateMedian            string
	Delta                      string
	MaterialityFloor           string
	Classification             string
	RequestedURL               string
	FinalURL                   string
	Started                    string
	Finished                   string
	Median                     string
	Run                        string
	Range                      string
	LCPDiagnostic              string
	RepresentativeRun          string
	ClosestMedianLCP           string
	SelectorWithheld           string
	NoLCPBreakdown             string
	TopOpportunities           string
	NoOpportunities            string
	WorkloadSignals            string
	Warnings                   string
	NoWarnings                 string
	EvidenceInterpretation     string
	EvidenceInterpretationBody string
	Status                     string
	Score                      string
	SpeedIndex                 string
	Benchmark                  string
	NoSuccessfulMetric         string
	ProtocolAndRuntime         string
	ProtocolFingerprint        string
	Platform                   string
	ResolvedProfileFlags       string
	ExecutorRuntimeFlags       string
	ScopeBoundary              string
	CannotClaim                string
	Footer                     string
	Unavailable                string
	EstimatedTimeSavings       string
	EstimatedTransferSavings   string
}

func ParseLocale(value string) (Locale, error) {
	locale := Locale(value)
	switch locale {
	case LocaleEnglish, LocaleSimplifiedChinese:
		return locale, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedLocale, value)
	}
}

func normalizeLocale(locale Locale) (Locale, error) {
	if locale == "" {
		return LocaleEnglish, nil
	}
	return ParseLocale(string(locale))
}

func messagesFor(locale Locale) Messages {
	if locale == LocaleSimplifiedChinese {
		return Messages{
			HeroEyebrow:             "webperf · 已验证证据",
			Target:                  "目标",
			EvidenceStatus:          "证据状态",
			MeasurementStart:        "测量开始",
			MeasurementFinish:       "测量结束",
			ReportSections:          "报告章节",
			StrictAnalysis:          "严格同协议分析",
			ComparisonVerdict:       "对比结论",
			PossibleClassifications: "可能的分类",
			Metric:                  "指标",
			BaselineMedian:          "基线中位数",
			CandidateMedian:         "候选中位数",
			Delta:                   "变化量",
			MaterialityFloor:        "实质性阈值",
			Classification:          "分类",
			RequestedURL:            "请求 URL",
			FinalURL:                "最终 URL",
			Started:                 "开始时间",
			Finished:                "结束时间",
			Median:                  "中位数",
			Run:                     "测量轮次",
			Range:                   "范围",
			LCPDiagnostic:           "LCP 诊断",
			RepresentativeRun:       "代表性测量",
			ClosestMedianLCP:        "最接近 LCP 中位数的测量",
			SelectorWithheld: "可分享报告不会披露 LCP 元素标识；" +
				"仅包含 allowlist 内的数值诊断。",
			NoLCPBreakdown:         "该代表性 LHR 中没有可用的稳定 LCP 阶段拆分。",
			TopOpportunities:       "量化机会项",
			NoOpportunities:        "该代表性 LHR 中没有 allowlist 内的量化节省项。",
			WorkloadSignals:        "负载信号",
			Warnings:               "警告",
			NoWarnings:             "没有规范警告。",
			EvidenceInterpretation: "证据解读",
			EvidenceInterpretationBody: "性能得分是 Lighthouse 官方得分样本的中位数，" +
				"不会根据各指标中位数重新计算。报告保留离散度，" +
				"避免以单次结果作为验收结论。",
			Status:               "状态",
			Score:                "得分",
			SpeedIndex:           "速度指数（Speed Index）",
			Benchmark:            "基准指数",
			NoSuccessfulMetric:   "无成功指标样本",
			ProtocolAndRuntime:   "协议与运行环境",
			ProtocolFingerprint:  "协议指纹",
			Platform:             "平台",
			ResolvedProfileFlags: "解析后的 profile flags",
			ExecutorRuntimeFlags: "执行器 runtime flags",
			ScopeBoundary:        "适用边界",
			CannotClaim:          "不可据此声明",
			Footer: "本报告由经过哈希校验的本地 evidence 确定性生成。" +
				"该 standalone 文件不会加载脚本、字体、样式表、tracker 或任何网络资源。",
			Unavailable:              "不可用",
			EstimatedTimeSavings:     "预计可节省时间",
			EstimatedTransferSavings: "预计可减少传输",
		}
	}
	return Messages{
		HeroEyebrow:             "webperf · verified evidence",
		Target:                  "Target",
		EvidenceStatus:          "Evidence status",
		MeasurementStart:        "Measurement start",
		MeasurementFinish:       "Measurement finish",
		ReportSections:          "Report sections",
		StrictAnalysis:          "Strict compatible-protocol analysis",
		ComparisonVerdict:       "Comparison verdict",
		PossibleClassifications: "Possible classifications",
		Metric:                  "Metric",
		BaselineMedian:          "Baseline median",
		CandidateMedian:         "Candidate median",
		Delta:                   "Delta",
		MaterialityFloor:        "Materiality floor",
		Classification:          "Classification",
		RequestedURL:            "Requested URL",
		FinalURL:                "Final URL",
		Started:                 "Started",
		Finished:                "Finished",
		Median:                  "Median",
		Run:                     "Run",
		Range:                   "Range",
		LCPDiagnostic:           "LCP diagnostic",
		RepresentativeRun:       "representative run",
		ClosestMedianLCP:        "Run closest to median LCP",
		SelectorWithheld: "LCP element identity is withheld from the shareable report; " +
			"only allowlisted numeric diagnostics are included.",
		NoLCPBreakdown:         "No stable LCP phase breakdown was available in this representative LHR.",
		TopOpportunities:       "Top quantified opportunities",
		NoOpportunities:        "No allowlisted quantified savings were available in this representative LHR.",
		WorkloadSignals:        "Workload signals",
		Warnings:               "Warnings",
		NoWarnings:             "No canonical warnings.",
		EvidenceInterpretation: "Evidence interpretation",
		EvidenceInterpretationBody: "Performance score is the median of official Lighthouse score samples. " +
			"It is never reconstructed from median metrics. " +
			"Dispersion remains visible so one run cannot become the acceptance result.",
		Status:               "Status",
		Score:                "Score",
		SpeedIndex:           "Speed Index",
		Benchmark:            "Benchmark",
		NoSuccessfulMetric:   "No successful metric sample",
		ProtocolAndRuntime:   "Protocol and runtime",
		ProtocolFingerprint:  "Protocol fingerprint",
		Platform:             "Platform",
		ResolvedProfileFlags: "Resolved profile flags",
		ExecutorRuntimeFlags: "Executor-owned runtime flags",
		ScopeBoundary:        "Scope boundary",
		CannotClaim:          "Cannot Claim",
		Footer: "Deterministically rendered from hash-verified local evidence. " +
			"This standalone file loads no scripts, fonts, stylesheets, trackers, or network resources.",
		Unavailable:              "Unavailable",
		EstimatedTimeSavings:     "estimated time savings",
		EstimatedTransferSavings: "estimated transfer savings",
	}
}

func localizeDocument(document Document) (Document, Locale, error) {
	locale, err := normalizeLocale(document.Locale)
	if err != nil {
		return Document{}, "", err
	}
	document.Locale = locale
	document.Messages = messagesFor(locale)
	if locale == LocaleEnglish {
		return document, locale, nil
	}

	document.Title = localizedTitle(document.Kind)
	if document.EvidenceStatus == "PARTIAL" {
		document.Notice = "该 PARTIAL evidence 已通过校验，可用于诊断，但不能作为发布验收依据。"
	}
	document.CannotClaims = localizedClaims(
		document.Kind,
		document.EvidenceStatus == "PARTIAL",
	)
	document.Runs = localizeRuns(document.Runs)
	if document.Comparison != nil {
		comparison := *document.Comparison
		comparison.Metrics = append([]ComparisonMetric(nil), document.Comparison.Metrics...)
		for index := range comparison.Metrics {
			comparison.Metrics[index].Label = localizedMetricLabel(comparison.Metrics[index].Key)
		}
		document.Comparison = &comparison
	}
	return document, locale, nil
}

func localizedTitle(kind string) string {
	if kind == KindComparison {
		return "网页性能对比报告"
	}
	return "网页性能证据报告"
}

func localizedClaims(kind string, partial bool) []string {
	claims := []string{
		"CrUX、RUM 或 PageSpeed Insights 的 field performance",
		"真实用户 INP 或交互质量",
	}
	if kind == KindComparison {
		return append(claims,
			"生产部署状态，或证明某项代码改动导致了该变化",
			"超出双方完全相同 protocol fingerprint 的对比",
		)
	}
	claims = append(claims,
		"生产部署状态或因果归因",
		"不同测量 profile 之间的等价性",
	)
	if partial {
		claims = append([]string{"发布验收或完整成功采集"}, claims...)
	}
	return claims
}

func localizeRuns(runs []Run) []Run {
	localized := make([]Run, len(runs))
	for index, run := range runs {
		localized[index] = run
		localized[index].Role = localizedRole(run.Role)
		localized[index].Metrics = append([]Metric(nil), run.Metrics...)
		for metricIndex := range localized[index].Metrics {
			metric := &localized[index].Metrics[metricIndex]
			metric.Label = localizedMetricLabel(metric.Key)
		}
		localized[index].Warnings = make([]string, len(run.Warnings))
		for warningIndex, warning := range run.Warnings {
			localized[index].Warnings[warningIndex] = localizedWarning(warning)
		}
		localized[index].Representative = localizeRepresentative(run.Representative)
	}
	return localized
}

func localizedRole(role string) string {
	switch role {
	case "Baseline":
		return "基线"
	case "Candidate":
		return "候选"
	default:
		return role
	}
}

func localizedMetricLabel(key string) string {
	switch key {
	case "performanceScore":
		return "性能得分"
	case "fcp":
		return "首次内容绘制（FCP）"
	case "lcp":
		return "最大内容绘制（LCP）"
	case "speedIndex":
		return "速度指数（Speed Index）"
	case "tbt":
		return "总阻塞时间（TBT）"
	case "cls":
		return "累积布局偏移（CLS）"
	default:
		return key
	}
}

func localizeRepresentative(representative Representative) Representative {
	localized := representative
	localized.LCPBreakdown = append([]LCPPhase(nil), representative.LCPBreakdown...)
	for index := range localized.LCPBreakdown {
		phase := &localized.LCPBreakdown[index]
		phase.Label = localizedLCPPhaseLabel(phase.ID)
	}
	localized.Opportunities = append([]Opportunity(nil), representative.Opportunities...)
	for index := range localized.Opportunities {
		opportunity := &localized.Opportunities[index]
		opportunity.Label = localizedOpportunityLabel(opportunity.ID)
	}
	localized.Signals = append([]DiagnosticSignal(nil), representative.Signals...)
	for index := range localized.Signals {
		signal := &localized.Signals[index]
		signal.Label = localizedSignalLabel(signal.ID)
	}
	return localized
}

func localizedLCPPhaseLabel(id string) string {
	switch id {
	case "timeToFirstByte":
		return "首字节时间（TTFB）"
	case "resourceLoadDelay":
		return "资源加载延迟"
	case "resourceLoadDuration":
		return "资源加载耗时"
	case "elementRenderDelay":
		return "元素渲染延迟"
	default:
		return id
	}
}

func localizedOpportunityLabel(id string) string {
	labels := map[string]string{
		"server-response-time":  "缩短初始服务器响应时间",
		"unminified-css":        "压缩 CSS",
		"unminified-javascript": "压缩 JavaScript",
		"unused-css-rules":      "减少未使用的 CSS",
		"unused-javascript":     "减少未使用的 JavaScript",
	}
	if label, ok := labels[id]; ok {
		return label
	}
	return id
}

func localizedSignalLabel(id string) string {
	switch id {
	case "mainthread-work-breakdown":
		return "主线程工作"
	case "bootup-time":
		return "JavaScript 执行"
	case "total-byte-weight":
		return "总网络传输量"
	case "dom-size-insight":
		return "DOM 元素数量"
	default:
		return id
	}
}

func localizedWarning(warning string) string {
	switch warning {
	case "temporary browser cleanup failed":
		return "临时浏览器清理失败"
	case "one or more attempts failed":
		return "一个或多个测量轮次失败"
	case "Lighthouse reported an invalid final URL":
		return "Lighthouse 报告了无效的最终 URL"
	case "final URL changed across successful samples":
		return "最终 URL 在成功样本之间发生变化"
	case "LCP selector changed across successful samples":
		return "LCP selector 在成功样本之间发生变化"
	}
	const lighthousePrefix = "Lighthouse reported warnings on attempt "
	if strings.HasPrefix(warning, lighthousePrefix) {
		return "Lighthouse 在第 " + strings.TrimPrefix(warning, lighthousePrefix) + " 次测量中报告了警告"
	}
	const dispersionPrefix = "high dispersion for "
	if strings.HasPrefix(warning, dispersionPrefix) {
		metric := strings.TrimPrefix(warning, dispersionPrefix)
		return localizedDispersionMetricLabel(metric) + "的离散度较高"
	}
	return warning
}

func localizedDispersionMetricLabel(label string) string {
	switch label {
	case "Performance Score":
		return localizedMetricLabel("performanceScore")
	case "FCP":
		return localizedMetricLabel("fcp")
	case "LCP":
		return localizedMetricLabel("lcp")
	case "Speed Index":
		return localizedMetricLabel("speedIndex")
	case "TBT":
		return localizedMetricLabel("tbt")
	case "CLS":
		return localizedMetricLabel("cls")
	default:
		return label
	}
}
