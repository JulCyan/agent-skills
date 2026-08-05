// Package lhr parses the stable subset of Lighthouse JSON reports used by webperf.
package lhr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"
)

var ErrParse = errors.New("parse lighthouse report")

// Sample contains only the LHR fields used as collection evidence. PerformanceScore
// is the Lighthouse category score expressed as a percentage, not a derived score.
type Sample struct {
	LighthouseVersion string   `json:"lighthouseVersion"`
	FinalURL          string   `json:"finalUrl"`
	PerformanceScore  float64  `json:"performanceScore"`
	FCP               float64  `json:"fcp"`
	LCP               float64  `json:"lcp"`
	SpeedIndex        float64  `json:"speedIndex"`
	TBT               float64  `json:"tbt"`
	CLS               float64  `json:"cls"`
	BenchmarkIndex    float64  `json:"benchmarkIndex"`
	Warnings          []string `json:"warnings,omitempty"`
	LCPSelector       string   `json:"lcpSelector,omitempty"`
}

// Parse extracts locked measurement fields from a single Lighthouse JSON report.
func Parse(data []byte) (Sample, error) {
	var report report
	if err := decode(data, &report); err != nil {
		return Sample{}, fmt.Errorf("%w: decode report: %v", ErrParse, err)
	}

	finalURL := report.FinalDisplayedURL
	if !isHTTPURL(finalURL) {
		finalURL = report.FinalURL
	}
	if !isHTTPURL(finalURL) {
		return Sample{}, parseField("finalDisplayedUrl or finalUrl")
	}
	if report.LighthouseVersion == "" {
		return Sample{}, parseField("lighthouseVersion")
	}
	if report.Categories.Performance.Score == nil || !finite(*report.Categories.Performance.Score) || *report.Categories.Performance.Score < 0 || *report.Categories.Performance.Score > 1 {
		return Sample{}, parseField("categories.performance.score")
	}
	if report.Environment.BenchmarkIndex == nil || !finite(*report.Environment.BenchmarkIndex) {
		return Sample{}, parseField("environment.benchmarkIndex")
	}

	fcp, err := numericAudit(report.Audits, "first-contentful-paint")
	if err != nil {
		return Sample{}, err
	}
	lcp, err := numericAudit(report.Audits, "largest-contentful-paint")
	if err != nil {
		return Sample{}, err
	}
	speedIndex, err := numericAudit(report.Audits, "speed-index")
	if err != nil {
		return Sample{}, err
	}
	tbt, err := numericAudit(report.Audits, "total-blocking-time")
	if err != nil {
		return Sample{}, err
	}
	cls, err := numericAudit(report.Audits, "cumulative-layout-shift")
	if err != nil {
		return Sample{}, err
	}

	return Sample{
		LighthouseVersion: report.LighthouseVersion,
		FinalURL:          finalURL,
		PerformanceScore:  *report.Categories.Performance.Score * 100,
		FCP:               fcp,
		LCP:               lcp,
		SpeedIndex:        speedIndex,
		TBT:               tbt,
		CLS:               cls,
		BenchmarkIndex:    *report.Environment.BenchmarkIndex,
		Warnings:          append([]string(nil), report.RunWarnings...),
		LCPSelector:       report.Audits["lcp-breakdown-insight"].selector(),
	}, nil
}

type report struct {
	LighthouseVersion string `json:"lighthouseVersion"`
	FinalDisplayedURL string `json:"finalDisplayedUrl"`
	FinalURL          string `json:"finalUrl"`
	Categories        struct {
		Performance struct {
			Score *float64 `json:"score"`
		} `json:"performance"`
	} `json:"categories"`
	Audits      map[string]audit `json:"audits"`
	Environment struct {
		BenchmarkIndex *float64 `json:"benchmarkIndex"`
	} `json:"environment"`
	RunWarnings []string `json:"runWarnings"`
}

type audit struct {
	NumericValue *float64 `json:"numericValue"`
	Details      struct {
		Items json.RawMessage `json:"items"`
	} `json:"details"`
}

type insightItem struct {
	Type     string `json:"type"`
	Selector string `json:"selector"`
}

func (a audit) selector() string {
	var items []insightItem
	if len(a.Details.Items) == 0 || json.Unmarshal(a.Details.Items, &items) != nil {
		return ""
	}
	for _, item := range items {
		if item.Type == "node" && item.Selector != "" {
			return item.Selector
		}
	}
	return ""
}

func numericAudit(audits map[string]audit, name string) (float64, error) {
	audit, ok := audits[name]
	if !ok || audit.NumericValue == nil || !finite(*audit.NumericValue) {
		return 0, parseField("audits." + name + ".numericValue")
	}
	return *audit.NumericValue, nil
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON document")
		}
		return err
	}
	return nil
}

func parseField(name string) error {
	return fmt.Errorf("%w: invalid or missing %s", ErrParse, name)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func isHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && strings.TrimSpace(value) == value
}
