package report

import (
	"strings"
	"testing"
)

func TestParseDiagnosticsExtractsOnlyAllowlistedQuantifiedSignals(t *testing.T) {
	raw := []byte(`{
  "audits": {
    "lcp-breakdown-insight": {
      "details": {
        "items": [
          {"type":"table","items":[
            {"subpart":"timeToFirstByte","label":"untrusted label","duration":700},
            {"subpart":"resourceLoadDelay","duration":300},
            {"subpart":"resourceLoadDuration","duration":500},
            {"subpart":"elementRenderDelay","duration":900}
          ]},
          {"type":"node","selector":"main > img[data-secret='escaped']","snippet":"do not copy"}
        ]
      }
    },
    "unused-javascript": {
      "title":"do not copy this title",
      "displayValue":"https://secret.example/token",
      "details":{"overallSavingsMs":2300,"overallSavingsBytes":452008}
    },
    "server-response-time": {"details":{"overallSavingsMs":570}},
    "unused-css-rules": {"details":{"overallSavingsBytes":36968}},
    "unknown-audit": {"details":{"overallSavingsMs":999999,"overallSavingsBytes":999999}}
  }
}`)

	diagnostics, err := parseDiagnostics(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.LCPBreakdown) != 4 {
		t.Fatalf("breakdown=%+v", diagnostics.LCPBreakdown)
	}
	if diagnostics.LCPBreakdown[0].Label != "Time to first byte" || diagnostics.LCPBreakdown[3].Label != "Element render delay" {
		t.Fatalf("labels=%+v", diagnostics.LCPBreakdown)
	}
	if len(diagnostics.Opportunities) != 3 {
		t.Fatalf("opportunities=%+v", diagnostics.Opportunities)
	}
	if diagnostics.Opportunities[0].ID != "unused-javascript" || diagnostics.Opportunities[1].ID != "server-response-time" || diagnostics.Opportunities[2].ID != "unused-css-rules" {
		t.Fatalf("opportunity order=%+v", diagnostics.Opportunities)
	}
	encoded := diagnosticsText(diagnostics)
	for _, forbidden := range []string{"untrusted label", "do not copy", "secret.example", "unknown-audit", "data-secret", "escaped"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("diagnostics copied untrusted field %q: %s", forbidden, encoded)
		}
	}
}

func TestParseDiagnosticsTreatsOptionalShapeChangesAsUnavailable(t *testing.T) {
	diagnostics, err := parseDiagnostics([]byte(`{"audits":{"lcp-breakdown-insight":{"details":{"items":{"changed":true}}},"unused-javascript":{"details":{"overallSavingsMs":-1}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.LCPBreakdown) != 0 || len(diagnostics.Opportunities) != 0 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}

func TestParseDiagnosticsRejectsInvalidJSON(t *testing.T) {
	if _, err := parseDiagnostics([]byte(`{"audits":`)); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestEvidenceWindowIgnoresInvalidTimestamps(t *testing.T) {
	startedAt, finishedAt := evidenceWindow([]Run{
		{StartedAt: "not-a-time", FinishedAt: "also-not-a-time"},
		{StartedAt: "2026-08-24T03:05:35Z", FinishedAt: "2026-08-24T03:07:29Z"},
	})
	if startedAt != "2026-08-24T03:05:35Z" || finishedAt != "2026-08-24T03:07:29Z" {
		t.Fatalf("window=%q..%q", startedAt, finishedAt)
	}
}

func diagnosticsText(value Diagnostics) string {
	var result strings.Builder
	for _, phase := range value.LCPBreakdown {
		result.WriteString(phase.ID)
		result.WriteString(phase.Label)
	}
	for _, opportunity := range value.Opportunities {
		result.WriteString(opportunity.ID)
		result.WriteString(opportunity.Label)
	}
	return result.String()
}
