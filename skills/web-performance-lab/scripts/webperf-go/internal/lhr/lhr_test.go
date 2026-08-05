package lhr

import (
	"fmt"
	"os"
	"testing"
)

func TestParseReadsLockedFields(t *testing.T) {
	payload, err := os.ReadFile("testdata/sample.json")
	if err != nil {
		t.Fatal(err)
	}

	got, err := Parse(payload)
	if err != nil {
		t.Fatal(err)
	}

	if got.LighthouseVersion != "13.4.1" || got.FinalURL != "https://example.test/page" {
		t.Fatalf("identity=%+v", got)
	}
	if got.PerformanceScore != 91 || got.FCP != 1200 || got.LCP != 2400 || got.SpeedIndex != 1800 || got.TBT != 90 || got.CLS != 0.03 {
		t.Fatalf("metrics=%+v", got)
	}
	if got.BenchmarkIndex != 1234 || got.LCPSelector != "main > img.hero" {
		t.Fatalf("environment=%+v", got)
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "synthetic warning" {
		t.Fatalf("warnings=%q", got.Warnings)
	}
}

func TestParseUsesFinalURLWhenDisplayedURLIsMissing(t *testing.T) {
	payload := []byte(`{
        "lighthouseVersion":"13.4.1",
        "finalUrl":"https://example.test/page",
        "categories":{"performance":{"score":0.5}},
        "audits":{
          "first-contentful-paint":{"numericValue":1},
          "largest-contentful-paint":{"numericValue":2},
          "speed-index":{"numericValue":3},
          "total-blocking-time":{"numericValue":4},
          "cumulative-layout-shift":{"numericValue":0.01}
        },
        "environment":{"benchmarkIndex":5}
    }`)

	got, err := Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.FinalURL != "https://example.test/page" {
		t.Fatalf("final URL=%q", got.FinalURL)
	}
}

func TestParseUsesSafeFinalURLWhenDisplayedURLIsInvalid(t *testing.T) {
	payload := []byte(`{
        "lighthouseVersion":"13.4.1",
        "finalDisplayedUrl":"javascript:alert(1)",
        "finalUrl":"https://example.test/page",
        "categories":{"performance":{"score":0.5}},
        "audits":{
          "first-contentful-paint":{"numericValue":1},
          "largest-contentful-paint":{"numericValue":2},
          "speed-index":{"numericValue":3},
          "total-blocking-time":{"numericValue":4},
          "cumulative-layout-shift":{"numericValue":0.01}
        },
        "environment":{"benchmarkIndex":5}
    }`)

	got, err := Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.FinalURL != "https://example.test/page" {
		t.Fatalf("final URL=%q", got.FinalURL)
	}
}

func TestParseRejectsInvalidReports(t *testing.T) {
	valid := `{
        "lighthouseVersion":"13.4.1", "finalDisplayedUrl":"https://example.test/page",
        "categories":{"performance":{"score":0.5}},
        "audits":{
          "first-contentful-paint":{"numericValue":1}, "largest-contentful-paint":{"numericValue":2},
          "speed-index":{"numericValue":3}, "total-blocking-time":{"numericValue":4},
          "cumulative-layout-shift":{"numericValue":0.01}
        }, "environment":{"benchmarkIndex":5}
    }`

	cases := []struct {
		name    string
		payload string
	}{
		{name: "malformed", payload: `{"lighthouseVersion":`},
		{name: "trailing document", payload: valid + ` {}`},
		{name: "missing metric", payload: `{"lighthouseVersion":"13.4.1","finalDisplayedUrl":"https://example.test/page","categories":{"performance":{"score":0.5}},"audits":{},"environment":{"benchmarkIndex":5}}`},
		{name: "non-finite score", payload: `{"lighthouseVersion":"13.4.1","finalDisplayedUrl":"https://example.test/page","categories":{"performance":{"score":1e999}},"audits":{},"environment":{"benchmarkIndex":5}}`},
		{name: "negative score", payload: validScore(-0.1)},
		{name: "score greater than one", payload: validScore(1.1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.payload)); err == nil {
				t.Fatal("Parse succeeded")
			}
		})
	}

}

func validScore(score float64) string {
	return fmt.Sprintf(`{
        "lighthouseVersion":"13.4.1", "finalDisplayedUrl":"https://example.test/page",
        "categories":{"performance":{"score":%v}},
        "audits":{
          "first-contentful-paint":{"numericValue":1}, "largest-contentful-paint":{"numericValue":2},
          "speed-index":{"numericValue":3}, "total-blocking-time":{"numericValue":4},
          "cumulative-layout-shift":{"numericValue":0.01}
        }, "environment":{"benchmarkIndex":5}
    }`, score)
}
