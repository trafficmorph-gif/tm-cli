package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// decodeHistory decodes a history row the way fetchLatestHistoryForProfile
// does — UseNumber — so numbers reach the formatter as json.Number.
func decodeHistory(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var h map[string]interface{}
	if err := dec.Decode(&h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return h
}

const failedRow = `{
  "id": 41,
  "thresholdStatus": "FAIL",
  "thresholdResults": [
    {"metric": "p95", "op": "<", "value": 200.0, "actual": 340.5, "passed": false},
    {"metric": "errorRate", "op": "<=", "value": 1.0, "actual": 0.25, "passed": true},
    {"metric": "p99", "op": "<", "value": 500.0, "actual": null, "passed": false}
  ]
}`

func TestResolveThresholdExit_FailExits99NamingOnlyTheBrokenOnes(t *testing.T) {
	err := resolveThresholdExit(decodeHistory(t, failedRow))
	if err == nil {
		t.Fatal("expected an exit-code error for a FAIL threshold status")
	}
	ec, ok := err.(interface{ ExitCode() int })
	if !ok {
		t.Fatalf("error did not carry ExitCode(): %v", err)
	}
	if ec.ExitCode() != 99 {
		t.Errorf("got exit %d, want 99", ec.ExitCode())
	}
	msg := err.Error()
	for _, want := range []string{"p95 < 200ms (actual 340.5ms)", "p99 < 500ms (no data)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "errorRate") {
		t.Errorf("message %q lists a threshold that passed", msg)
	}
}

func TestResolveThresholdExit_CaseInsensitiveAndWithoutResults(t *testing.T) {
	err := resolveThresholdExit(map[string]interface{}{"thresholdStatus": "fail"})
	ec, ok := err.(interface{ ExitCode() int })
	if !ok || ec.ExitCode() != 99 {
		t.Fatalf("lowercase fail with no results: got %v, want exit 99", err)
	}
	if err.Error() != "thresholds failed" {
		t.Errorf("got %q", err.Error())
	}
}

func TestResolveThresholdExit_PassOrNoThresholdsExitsZero(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"pass":              {"thresholdStatus": "PASS"},
		"absent":            {},
		"null":              {"thresholdStatus": nil},
		"empty":             {"thresholdStatus": ""},
		"wrong type":        {"thresholdStatus": 7},
		"verdict FAIL only": {"autoVerdict": "FAIL"}, // the verdict gate's job, not this one
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			if err := resolveThresholdExit(h); err != nil {
				t.Errorf("expected nil, got %v", err)
			}
		})
	}
}

func TestPrintHistorySummary_ListsEachThreshold(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printHistorySummary(cmd, decodeHistory(t, failedRow))

	out := buf.String()
	for _, want := range []string{
		"Thresholds:   FAIL",
		"  FAIL  p95 < 200ms (actual 340.5ms)",
		"  pass  errorRate <= 1% (actual 0.25%)",
		"  FAIL  p99 < 500ms (no data)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestPrintHistorySummary_OmitsThresholdsWhenNone(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printHistorySummary(cmd, map[string]interface{}{"id": json.Number("1")})

	if strings.Contains(buf.String(), "Thresholds") {
		t.Errorf("summary mentions thresholds for a profile without any:\n%s", buf.String())
	}
}

func TestDescribeThreshold_PrintsPlainDecimals(t *testing.T) {
	// Jackson writes doubles in Double.toString form: 200.0, 5.0E-4.
	h := decodeHistory(t, `{"thresholdResults": [
	  {"metric": "errorRate", "op": "<=", "value": 5.0E-4, "actual": 3.0E-4, "passed": true},
	  {"metric": "peakRps", "op": ">=", "value": 1.0E7, "actual": 98.33, "passed": false}
	]}`)
	results := thresholdResults(h)
	for i, want := range []string{
		"errorRate <= 0.0005% (actual 0.0003%)",
		"peakRps >= 10000000 rps (actual 98.33 rps)",
	} {
		if got := describeThreshold(results[i]); got != want {
			t.Errorf("result %d: got %q, want %q", i, got, want)
		}
	}
}
