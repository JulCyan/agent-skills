package report

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Diagnostics is the optional, allowlisted diagnostic subset extracted from a
// hash-verified representative Lighthouse report. Labels are owned by webperf;
// arbitrary LHR prose, URLs, snippets, and resource rows are never copied.
type Diagnostics struct {
	LCPBreakdown  []LCPPhase
	Opportunities []Opportunity
	Signals       []DiagnosticSignal
}

type LCPPhase struct {
	ID       string
	Label    string
	Duration float64
}

type Opportunity struct {
	ID           string
	Label        string
	SavingsMS    float64
	SavingsBytes float64
}

type DiagnosticSignal struct {
	ID    string
	Label string
	Value float64
	Unit  string
}

type diagnosticReport struct {
	Audits map[string]diagnosticAudit `json:"audits"`
}

type diagnosticAudit struct {
	NumericValue *float64 `json:"numericValue"`
	Details      struct {
		OverallSavingsMS    *float64        `json:"overallSavingsMs"`
		OverallSavingsBytes *float64        `json:"overallSavingsBytes"`
		Items               json.RawMessage `json:"items"`
	} `json:"details"`
}

type diagnosticListItem struct {
	Type  string `json:"type"`
	Items []struct {
		Subpart  string   `json:"subpart"`
		Duration *float64 `json:"duration"`
	} `json:"items"`
}

var opportunityLabels = map[string]string{
	"efficient-animated-content": "Use efficient animated content",
	"modern-image-formats":       "Serve images in modern formats",
	"offscreen-images":           "Defer offscreen images",
	"render-blocking-insight":    "Eliminate render-blocking resources",
	"render-blocking-resources":  "Eliminate render-blocking resources",
	"server-response-time":       "Reduce initial server response time",
	"unminified-css":             "Minify CSS",
	"unminified-javascript":      "Minify JavaScript",
	"unused-css-rules":           "Reduce unused CSS",
	"unused-javascript":          "Reduce unused JavaScript",
	"uses-optimized-images":      "Efficiently encode images",
	"uses-responsive-images":     "Properly size images",
	"uses-text-compression":      "Enable text compression",
}

var signalDefinitions = []struct {
	id    string
	label string
	unit  string
}{
	{id: "mainthread-work-breakdown", label: "Main-thread work", unit: "ms"},
	{id: "bootup-time", label: "JavaScript execution", unit: "ms"},
	{id: "total-byte-weight", label: "Total network payload", unit: "bytes"},
	{id: "dom-size-insight", label: "DOM elements", unit: "count"},
}

var lcpPhaseDefinitions = []struct {
	id    string
	label string
}{
	{id: "timeToFirstByte", label: "Time to first byte"},
	{id: "resourceLoadDelay", label: "Resource load delay"},
	{id: "resourceLoadDuration", label: "Resource load duration"},
	{id: "elementRenderDelay", label: "Element render delay"},
}

func parseDiagnostics(data []byte) (Diagnostics, error) {
	var parsed diagnosticReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Diagnostics{}, fmt.Errorf("decode report diagnostics: %w", err)
	}

	result := Diagnostics{}
	result.LCPBreakdown = parseLCPBreakdown(parsed.Audits["lcp-breakdown-insight"])
	for id, label := range opportunityLabels {
		audit, ok := parsed.Audits[id]
		if !ok {
			continue
		}
		savingsMS := positiveValue(audit.Details.OverallSavingsMS)
		savingsBytes := positiveValue(audit.Details.OverallSavingsBytes)
		if savingsMS == 0 && savingsBytes == 0 {
			continue
		}
		result.Opportunities = append(result.Opportunities, Opportunity{
			ID:           id,
			Label:        label,
			SavingsMS:    savingsMS,
			SavingsBytes: savingsBytes,
		})
	}
	sort.Slice(result.Opportunities, func(left, right int) bool {
		l, r := result.Opportunities[left], result.Opportunities[right]
		if (l.SavingsMS > 0) != (r.SavingsMS > 0) {
			return l.SavingsMS > 0
		}
		if l.SavingsMS != r.SavingsMS {
			return l.SavingsMS > r.SavingsMS
		}
		if l.SavingsBytes != r.SavingsBytes {
			return l.SavingsBytes > r.SavingsBytes
		}
		return l.ID < r.ID
	})
	if len(result.Opportunities) > 5 {
		result.Opportunities = result.Opportunities[:5]
	}

	for _, definition := range signalDefinitions {
		audit, ok := parsed.Audits[definition.id]
		if !ok {
			continue
		}
		value := positiveValue(audit.NumericValue)
		if value == 0 {
			continue
		}
		result.Signals = append(result.Signals, DiagnosticSignal{
			ID:    definition.id,
			Label: definition.label,
			Value: value,
			Unit:  definition.unit,
		})
	}
	return result, nil
}

func parseLCPBreakdown(audit diagnosticAudit) []LCPPhase {
	if len(audit.Details.Items) == 0 {
		return nil
	}
	var items []diagnosticListItem
	if err := json.Unmarshal(audit.Details.Items, &items); err != nil {
		return nil
	}
	durations := make(map[string]float64)
	for _, item := range items {
		if item.Type == "table" {
			for _, phase := range item.Items {
				value := positiveValue(phase.Duration)
				if value > 0 {
					durations[phase.Subpart] = value
				}
			}
		}
	}
	phases := make([]LCPPhase, 0, len(lcpPhaseDefinitions))
	for _, definition := range lcpPhaseDefinitions {
		if duration := durations[definition.id]; duration > 0 {
			phases = append(phases, LCPPhase{ID: definition.id, Label: definition.label, Duration: duration})
		}
	}
	return phases
}

func positiveValue(value *float64) float64 {
	if value == nil || *value <= 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return 0
	}
	return *value
}
