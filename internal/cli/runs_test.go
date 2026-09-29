package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trafficmorph-gif/tm-cli/internal/api"
)

func TestParseVerdictList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"FAIL", []string{"FAIL"}},
		{"fail,warn", []string{"FAIL", "WARN"}},
		{"FAIL, WARN , NO_BASELINE", []string{"FAIL", "WARN", "NO_BASELINE"}},
		{"   ", nil},
		{",,FAIL,,", []string{"FAIL"}},
	}
	for _, c := range cases {
		got := parseVerdictList(c.in)
		if !stringSliceEq(got, c.want) {
			t.Errorf("parseVerdictList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestExitCodeForVerdict(t *testing.T) {
	cases := map[string]int{
		"FAIL":        2,
		"fail":        2,
		"WARN":        3,
		"NO_BASELINE": 4,
		"PASS":        1, // PASS shouldn't trigger gating, but if it does we use the catch-all
		"":            1,
		"garbage":     1,
	}
	for v, want := range cases {
		if got := exitCodeForVerdict(v); got != want {
			t.Errorf("exitCodeForVerdict(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestPickVerdict(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want string
	}{
		{"present", map[string]interface{}{"autoVerdict": "FAIL"}, "FAIL"},
		{"absent", map[string]interface{}{}, ""},
		{"wrong type", map[string]interface{}{"autoVerdict": 42}, ""},
		{"null", map[string]interface{}{"autoVerdict": nil}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickVerdict(c.in); got != c.want {
				t.Errorf("pickVerdict(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResolveGateExit_NoGate locks in that --fail-on-verdict
// being unset disables the CI gate entirely — runs without
// regression checks must produce exit 0 even on FAIL verdicts.
func TestResolveGateExit_NoGate(t *testing.T) {
	cases := []map[string]interface{}{
		{"autoVerdict": "PASS"},
		{"autoVerdict": "FAIL"},
		{"autoVerdict": "WARN"},
		{"autoVerdict": "NO_BASELINE"},
		{}, // missing verdict
	}
	for _, h := range cases {
		if err := resolveGateExit(h, ""); err != nil {
			t.Errorf("expected nil with no gate for %v, got: %v", h, err)
		}
	}
}

// TestResolveGateExit_MatchingVerdict — the happy gating path:
// matched verdict produces the verdict-specific exit code.
func TestResolveGateExit_MatchingVerdict(t *testing.T) {
	cases := []struct {
		verdict      string
		gate         string
		wantExitCode int
	}{
		{"FAIL", "FAIL", 2},
		{"WARN", "WARN", 3},
		{"NO_BASELINE", "NO_BASELINE", 4},
		{"FAIL", "FAIL,WARN", 2},
		{"WARN", "FAIL,WARN", 3},
		{"FAIL", "fail", 2}, // gate normalization is case-insensitive
	}
	for _, c := range cases {
		t.Run(c.verdict+"_in_"+c.gate, func(t *testing.T) {
			err := resolveGateExit(map[string]interface{}{"autoVerdict": c.verdict}, c.gate)
			if err == nil {
				t.Fatalf("expected exit-code error for verdict=%q gate=%q", c.verdict, c.gate)
			}
			ec, ok := err.(interface{ ExitCode() int })
			if !ok {
				t.Fatalf("error did not carry ExitCode(): %v", err)
			}
			if ec.ExitCode() != c.wantExitCode {
				t.Errorf("verdict=%q gate=%q: got exit %d, want %d",
					c.verdict, c.gate, ec.ExitCode(), c.wantExitCode)
			}
		})
	}
}

// TestResolveGateExit_NonMatchingVerdict — a verdict NOT in the
// gate list passes (exit 0). The CI use case: `--fail-on-verdict FAIL`
// should NOT fail on PASS or WARN, only on FAIL.
func TestResolveGateExit_NonMatchingVerdict(t *testing.T) {
	cases := []struct {
		verdict string
		gate    string
	}{
		{"PASS", "FAIL"},
		{"PASS", "FAIL,WARN"},
		{"WARN", "FAIL"},
		{"NO_BASELINE", "FAIL"},
	}
	for _, c := range cases {
		t.Run(c.verdict+"_not_in_"+c.gate, func(t *testing.T) {
			err := resolveGateExit(map[string]interface{}{"autoVerdict": c.verdict}, c.gate)
			if err != nil {
				t.Errorf("verdict=%q gate=%q: expected nil, got: %v", c.verdict, c.gate, err)
			}
		})
	}
}

// TestResolveGateExit_FailsClosedOnMissingVerdict pins the P1 fix:
// when --fail-on-verdict is set but the history row has no
// autoVerdict (either because the auto-compare worker hasn't run
// yet, or because the profile has no baseline), the gate must fail
// CLOSED with a non-zero exit. The previous implementation returned
// nil here, producing a silent false-pass — exactly the regression
// shape --fail-on-verdict exists to catch.
func TestResolveGateExit_FailsClosedOnMissingVerdict(t *testing.T) {
	cases := []struct {
		name    string
		history map[string]interface{}
	}{
		{"empty map", map[string]interface{}{}},
		{"verdict is null", map[string]interface{}{"autoVerdict": nil}},
		{"verdict is empty string", map[string]interface{}{"autoVerdict": ""}},
		{"verdict is wrong type", map[string]interface{}{"autoVerdict": 42}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := resolveGateExit(c.history, "FAIL,WARN")
			if err == nil {
				t.Fatal("expected non-nil error — gate must fail closed on missing verdict")
			}
			ec, ok := err.(interface{ ExitCode() int })
			if !ok {
				t.Fatalf("expected exit-code error, got: %T %v", err, err)
			}
			if ec.ExitCode() == 0 {
				t.Errorf("expected non-zero exit code, got 0 (= false-pass)")
			}
		})
	}
}

// TestPickID covers every JSON-decode shape an id field might take.
// json.Unmarshal into map[string]interface{} produces float64 for
// numbers by default; json.Number is the alternative path a caller
// gets when using a Decoder with UseNumber(). Test-constructed
// rows often use raw int64 or int.
func TestPickID(t *testing.T) {
	int64Ptr := func(v int64) *int64 { return &v }
	cases := []struct {
		name string
		in   map[string]interface{}
		want *int64
	}{
		{"float64 (default json.Unmarshal path)", map[string]interface{}{"id": float64(42)}, int64Ptr(42)},
		{"json.Number small (production decoder uses UseNumber)", map[string]interface{}{"id": json.Number("42")}, int64Ptr(42)},
		{"json.Number large (beyond float64 mantissa)", map[string]interface{}{"id": json.Number("9007199254740993")}, int64Ptr(9007199254740993)},
		{"json.Number invalid → nil", map[string]interface{}{"id": json.Number("not a number")}, nil},
		{"int64 (test fixture path)", map[string]interface{}{"id": int64(42)}, int64Ptr(42)},
		{"int", map[string]interface{}{"id": 42}, int64Ptr(42)},
		{"missing field", map[string]interface{}{}, nil},
		{"null", map[string]interface{}{"id": nil}, nil},
		{"wrong type — string", map[string]interface{}{"id": "42"}, nil},
		{"wrong type — bool", map[string]interface{}{"id": true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickID(c.in)
			switch {
			case got == nil && c.want == nil:
				return
			case got == nil || c.want == nil:
				t.Fatalf("got %v, want %v", got, c.want)
			case *got != *c.want:
				t.Errorf("got %d, want %d", *got, *c.want)
			}
		})
	}
}

// TestClassifyHistoryAttempt locks in the P1 stale-run fix: the
// retry loop's per-iteration decision must distinguish
// "previous run's row" from "our run's row" and from
// "row not yet inserted." Each outcome drives a different control-
// flow path in waitForHistoryWithVerdict, so getting any branch
// wrong would either fail-fast on a transient state or hang
// waiting for a row that's already there.
func TestClassifyHistoryAttempt(t *testing.T) {
	int64Ptr := func(v int64) *int64 { return &v }
	cases := []struct {
		name        string
		row         map[string]interface{}
		priorTopID  *int64
		wantOutcome historyAttemptOutcome
	}{
		{
			name:        "nil row → wait for row",
			row:         nil,
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitRow,
		},
		{
			name:        "row id == priorTopID → wait for new row (the bug this fix exists for)",
			row:         map[string]interface{}{"id": int64(100), "autoVerdict": "PASS"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitRow,
		},
		{
			name:        "row id == priorTopID with verdict — STILL wait for row",
			row:         map[string]interface{}{"id": int64(100), "autoVerdict": "FAIL"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitRow,
		},
		{
			name:        "new row but no verdict → wait for verdict",
			row:         map[string]interface{}{"id": int64(101), "autoVerdict": ""},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitVerdict,
		},
		{
			name:        "new row, verdict missing field → wait for verdict",
			row:         map[string]interface{}{"id": int64(101)},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitVerdict,
		},
		{
			name:        "new row with verdict → done",
			row:         map[string]interface{}{"id": int64(101), "autoVerdict": "PASS"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeDone,
		},
		{
			name:        "no prior history + any row with verdict → done",
			row:         map[string]interface{}{"id": int64(1), "autoVerdict": "PASS"},
			priorTopID:  nil,
			wantOutcome: outcomeDone,
		},
		{
			name:        "no prior history + row without verdict → wait for verdict",
			row:         map[string]interface{}{"id": int64(1)},
			priorTopID:  nil,
			wantOutcome: outcomeWaitVerdict,
		},
		// Fail-closed branches when we have a priorTopID but can't
		// extract a row id to compare against. Without these, a
		// server-side shape change (renamed field, type drift) or
		// a malformed response could route a stale row's
		// verdict straight into the CI gate.
		{
			name:        "priorTopID set + row missing id field → wait for row (fail closed)",
			row:         map[string]interface{}{"autoVerdict": "PASS"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitRow,
		},
		{
			name:        "priorTopID set + row id wrong type → wait for row (fail closed)",
			row:         map[string]interface{}{"id": "not a number", "autoVerdict": "FAIL"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitRow,
		},
		{
			name:        "priorTopID set + row id null → wait for row (fail closed)",
			row:         map[string]interface{}{"id": nil, "autoVerdict": "PASS"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitRow,
		},
		// Symmetric: WITHOUT a priorTopID there's no stale-row
		// vector, so an unparseable id falls through to the verdict
		// check (no fail-closed needed — there's no anchor to fail
		// against).
		{
			name:        "no priorTopID + row missing id field → done (no anchor to fail against)",
			row:         map[string]interface{}{"autoVerdict": "PASS"},
			priorTopID:  nil,
			wantOutcome: outcomeDone,
		},
		// json.Number path (the UseNumber decoder produces this).
		// Verifies the classifier correctly threads large / lossless
		// ids through pickID's json.Number branch.
		{
			name:        "priorTopID set + row id is json.Number matching priorTopID → wait for row",
			row:         map[string]interface{}{"id": json.Number("100"), "autoVerdict": "PASS"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeWaitRow,
		},
		{
			name:        "priorTopID set + row id is json.Number differing → done",
			row:         map[string]interface{}{"id": json.Number("101"), "autoVerdict": "PASS"},
			priorTopID:  int64Ptr(100),
			wantOutcome: outcomeDone,
		},
		// Beyond-float64 precision: ids >= 2^53 round to even
		// nearest float64. Verify json.Number preserves them.
		{
			name:        "priorTopID set + row id is large json.Number matching priorTopID → wait for row",
			row:         map[string]interface{}{"id": json.Number("9007199254740993"), "autoVerdict": "FAIL"},
			priorTopID:  int64Ptr(9007199254740993),
			wantOutcome: outcomeWaitRow,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyHistoryAttempt(c.row, c.priorTopID)
			if got != c.wantOutcome {
				t.Errorf("got outcome %d, want %d", got, c.wantOutcome)
			}
		})
	}
}

// TestValidateVerdictList covers the up-front --fail-on-verdict
// guard. The CI false-pass shape this prevents:
//
//	tm runs start 1 --wait --fail-on-verdict FAILL  ← typo
//
// parseVerdictList alone would accept that, run the load test,
// and report "verdict didn't match anything in the gate set" →
// exit 0 → broken CI thinks the build is green. The validator
// catches typos before any HTTP work begins.
func TestValidateVerdictList(t *testing.T) {
	t.Run("accepts canonical tokens", func(t *testing.T) {
		// Every token the server can emit as autoVerdict, minus
		// PASS (deliberately rejected — gating on PASS is almost
		// certainly a typo). Single + comma-combinations.
		cases := []string{
			"FAIL",
			"WARN",
			"NO_BASELINE",
			"FAIL,WARN",
			"FAIL,WARN,NO_BASELINE",
			"NO_BASELINE,FAIL",
		}
		for _, spec := range cases {
			t.Run(spec, func(t *testing.T) {
				if err := validateVerdictList(parseVerdictList(spec)); err != nil {
					t.Errorf("expected %q to validate, got: %v", spec, err)
				}
			})
		}
	})
	t.Run("rejects unknown tokens", func(t *testing.T) {
		// Typos + invented values. Each case must produce a non-
		// nil error AND mention the offending token in the message
		// so the user can self-correct without re-reading docs.
		cases := []struct {
			spec      string
			mustMatch string
		}{
			{"FAILL", "FAILL"},
			{"fail,WARM", "WARM"},
			{"WARN,FOOO", "FOOO"},
			{"PASS", "PASS"}, // PASS is intentionally rejected
			{"PASS,FAIL", "PASS"},
		}
		for _, c := range cases {
			t.Run(c.spec, func(t *testing.T) {
				err := validateVerdictList(parseVerdictList(c.spec))
				if err == nil {
					t.Fatalf("expected error for %q", c.spec)
				}
				if !strings.Contains(err.Error(), c.mustMatch) {
					t.Errorf("error %q must mention %q", err.Error(), c.mustMatch)
				}
				// The error must also list the valid set so the
				// user knows the allowed values.
				for _, valid := range []string{"FAIL", "WARN", "NO_BASELINE"} {
					if !strings.Contains(err.Error(), valid) {
						t.Errorf("error %q must list valid token %q", err.Error(), valid)
					}
				}
			})
		}
	})
	t.Run("empty list is fine — used when --fail-on-verdict is unset", func(t *testing.T) {
		if err := validateVerdictList(parseVerdictList("")); err != nil {
			t.Errorf("expected empty list to validate, got: %v", err)
		}
	})
}

// TestResolveFailOnVerdictSpec_RejectsDelimiterOnlyInputs pins the
// false-pass fix: a user typing `--fail-on-verdict ","` or
// `--fail-on-verdict "   "` clearly INTENDED to set a gate but
// produced an empty token list after parsing. Without this guard,
// validateVerdictList would treat the empty list as "no unknown
// tokens" (vacuously valid) and resolveGateExit would iterate zero
// tokens — exiting 0 even on FAIL verdicts. The CI gate would be
// silently disabled and the user wouldn't know until a regression
// shipped.
func TestResolveFailOnVerdictSpec_RejectsDelimiterOnlyInputs(t *testing.T) {
	cases := []string{
		",",
		",,,",
		"   ",
		" , , , ",
		",  ,",
		"\t",
		"\n",
	}
	for _, spec := range cases {
		t.Run("spec="+spec, func(t *testing.T) {
			_, err := resolveFailOnVerdictSpec(spec)
			if err == nil {
				t.Fatalf("expected error for delimiter-only spec %q — gate would otherwise be silently disabled", spec)
			}
			// Error must surface the user's actual input so they
			// can see what got rejected. And it must list the
			// valid set so the user can fix it.
			if !strings.Contains(err.Error(), spec) {
				// Whitespace specs render oddly; check via "no usable tokens" instead.
				if !strings.Contains(err.Error(), "no usable tokens") {
					t.Errorf("error %q should reference the empty-parse outcome", err.Error())
				}
			}
			for _, valid := range []string{"FAIL", "WARN", "NO_BASELINE"} {
				if !strings.Contains(err.Error(), valid) {
					t.Errorf("error %q should list valid token %q", err.Error(), valid)
				}
			}
		})
	}
}

// TestResolveFailOnVerdictSpec_HappyPath confirms the full
// pipeline accepts well-formed specs and returns the canonical
// list. This is the regression guard against over-tightening:
// the empty-list rejection above must NOT break valid specs.
func TestResolveFailOnVerdictSpec_HappyPath(t *testing.T) {
	cases := []struct {
		spec string
		want []string
	}{
		{"FAIL", []string{"FAIL"}},
		{"fail", []string{"FAIL"}},
		{"FAIL,WARN", []string{"FAIL", "WARN"}},
		{"  FAIL  ,  WARN  ", []string{"FAIL", "WARN"}},
		{"FAIL,,WARN", []string{"FAIL", "WARN"}}, // single empty token in the middle is OK
		{"FAIL,WARN,NO_BASELINE", []string{"FAIL", "WARN", "NO_BASELINE"}},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			got, err := resolveFailOnVerdictSpec(c.spec)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", c.spec, err)
			}
			if !stringSliceEq(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestResolveFailOnVerdictSpec_ForwardsUnknownTokenErrors confirms
// that resolveFailOnVerdictSpec still surfaces the unknown-token
// error from validateVerdictList — the empty-list check above must
// not shadow the more-specific validation below it.
func TestResolveFailOnVerdictSpec_ForwardsUnknownTokenErrors(t *testing.T) {
	_, err := resolveFailOnVerdictSpec("FAILL")
	if err == nil {
		t.Fatal("expected unknown-token error for FAILL")
	}
	if !strings.Contains(err.Error(), "FAILL") {
		t.Errorf("error should name the offending token; got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should classify itself as unknown-token; got: %v", err)
	}
}

// TestWrapControlCall_DecodeFailureOnSuccessStatus pins the P2 fix:
// a 2xx response with malformed JSON must surface as an error, not
// be silently swallowed (which would print empty fields and exit
// 0, masking server contract drift).
func TestWrapControlCall_DecodeFailureOnSuccessStatus(t *testing.T) {
	// Faked "endpoint call" — returns whatever status + body we want.
	wrapped := wrapControlCall("stop", func(_ context.Context, _ *api.ClientWithResponses, _ int64) (int, []byte, error) {
		return 200, []byte("not valid json"), nil
	})
	_, _, _, err := wrapped(context.Background(), nil, 0)
	if err == nil {
		t.Fatal("expected decode error to be returned on 2xx with malformed body")
	}
	if !strings.Contains(err.Error(), "decode stop response") {
		t.Errorf("error should name the verb being decoded; got: %v", err)
	}
	if !strings.Contains(err.Error(), "200") {
		t.Errorf("error should carry the HTTP status for debugging; got: %v", err)
	}
}

// TestWrapControlCall_4xxBypassesDecode confirms that error
// responses are NOT decoded as TrafficRunControlResponse — the
// body shape is the {"error":"..."} envelope on those branches,
// and trying to fit it into the control DTO would emit a
// misleading "decode failed" noise. The 4xx path must return
// (nil, status, body, nil) so the caller formats the real error.
func TestWrapControlCall_4xxBypassesDecode(t *testing.T) {
	wrapped := wrapControlCall("pause", func(_ context.Context, _ *api.ClientWithResponses, _ int64) (int, []byte, error) {
		return 400, []byte(`{"error":"Profile not found"}`), nil
	})
	parsed, status, body, err := wrapped(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("4xx path must NOT return decode error, got: %v", err)
	}
	if parsed != nil {
		t.Errorf("4xx path must return nil parsed; the caller formats from body")
	}
	if status != 400 {
		t.Errorf("expected status 400, got %d", status)
	}
	if string(body) != `{"error":"Profile not found"}` {
		t.Errorf("body must be returned unchanged for the caller's error formatter; got %q", body)
	}
}

// TestWrapControlCall_HappyPath confirms valid 2xx JSON decodes
// into the typed struct.
func TestWrapControlCall_HappyPath(t *testing.T) {
	wrapped := wrapControlCall("resume", func(_ context.Context, _ *api.ClientWithResponses, _ int64) (int, []byte, error) {
		return 200, []byte(`{"profileId":42,"runId":"abc","status":"PAUSED"}`), nil
	})
	parsed, status, _, err := wrapped(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("happy path produced unexpected error: %v", err)
	}
	if status != 200 {
		t.Errorf("expected status 200, got %d", status)
	}
	if parsed == nil {
		t.Fatal("expected non-nil parsed body on 2xx")
	}
	if parsed.Status == nil || *parsed.Status != "PAUSED" {
		t.Errorf("status field not decoded correctly: %+v", parsed.Status)
	}
}

// TestErrHistoryRowNotReady_IsSentinel — the sentinel error is
// what the retry loop in waitForHistoryWithVerdict keys off to
// decide "retry" vs "fail fast". Locking the identity in keeps
// future refactors of fetchLatestHistoryForProfile from accidentally
// downgrading the sentinel to a generic error message (which the
// retry loop would then treat as a real failure and exit early)
// or upgrading some real failure to use the sentinel (which would
// mask 401/403/500 behind a misleading timeout).
func TestErrHistoryRowNotReady_IsSentinel(t *testing.T) {
	if !errors.Is(errHistoryRowNotReady, errHistoryRowNotReady) {
		t.Fatal("errors.Is should match the sentinel against itself")
	}
	wrapped := errors.New("wrapping: " + errHistoryRowNotReady.Error())
	if errors.Is(wrapped, errHistoryRowNotReady) {
		t.Fatal("a string-equal but unrelated error must NOT match the sentinel — " +
			"retry-vs-fail-fast classification depends on identity equality")
	}
}

func TestStartRun_PlainStartUsesStartEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runId":"r1","status":"RUNNING","profileId":42}`))
	}))
	defer srv.Close()
	c, err := api.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	status, body, err := startRun(context.Background(), c, 42, "", nil)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if gotPath != "/api/v1/profiles/42/start" {
		t.Fatalf("path = %q; plain starts must keep using /start for older servers", gotPath)
	}
	if !strings.Contains(string(body), `"r1"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestStartRun_RegionAndTagsUseRunsEndpoint(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runId":"r2","status":"RUNNING","profileId":42}`))
	}))
	defer srv.Close()
	c, err := api.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := startRun(context.Background(), c, 42, "eu-west-1", []string{"abc123", "release-7"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/profiles/42/runs" {
		t.Fatalf("path = %q, want /api/v1/profiles/42/runs", gotPath)
	}
	if gotBody["region"] != "eu-west-1" {
		t.Fatalf("region = %v", gotBody["region"])
	}
	tags, _ := gotBody["tags"].([]any)
	if len(tags) != 2 || tags[0] != "abc123" || tags[1] != "release-7" {
		t.Fatalf("tags = %v", gotBody["tags"])
	}
}

func TestStartRun_TagsOnlyOmitsRegion(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, _ := api.NewClientWithResponses(srv.URL)

	if _, _, err := startRun(context.Background(), c, 42, "", []string{"abc123"}); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["region"]; present {
		t.Fatalf("region must be omitted so the server applies the profile default; body = %v", gotBody)
	}
}
