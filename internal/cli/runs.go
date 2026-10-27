package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trafficmorph-gif/tm-cli/internal/api"
)

// errHistoryRowNotReady is the sentinel returned by
// fetchLatestHistoryForProfile when the /history endpoint succeeds
// (HTTP 200) but its `content` array is empty — i.e. the run's
// history row hasn't been persisted yet. The retry loop in
// waitForHistoryWithVerdict checks for this specific error and
// retries only on it; every other error (network / 401 / 403 /
// 500) bubbles out immediately so persistent failures fail fast
// instead of accumulating misleading "no history row appeared"
// timeout messages.
var errHistoryRowNotReady = errors.New("history row not ready yet")

// historyAttemptOutcome encodes the per-iteration decision inside
// waitForHistoryWithVerdict. Extracted as a pure value so the
// classification logic (anchor stale row? wait for verdict? done?)
// can be unit-tested without mocking the HTTP client. The retry
// loop branches on the outcome to decide retry vs return.
type historyAttemptOutcome int

const (
	// outcomeWaitRow — the latest /history row still has the same
	// id as our pre-start snapshot, so the run we just started
	// hasn't been persisted yet. Keep retrying.
	outcomeWaitRow historyAttemptOutcome = iota
	// outcomeWaitVerdict — a new row IS present (id differs from
	// the pre-start snapshot, or there was no prior row at all),
	// but autoVerdict is empty because the auto-compare worker
	// hasn't filled it in. Keep retrying.
	outcomeWaitVerdict
	// outcomeDone — new row present AND verdict populated. The
	// wait loop returns this row.
	outcomeDone
)

// classifyHistoryAttempt decides what to do with one /history fetch
// result. Pure function so the (anchor / verdict / done) state
// machine is testable without standing up a mock HTTP server.
//
// Decision matrix:
//
//	row=nil                                        → outcomeWaitRow
//	priorTopID set + can't pick rowID              → outcomeWaitRow  (fail-closed)
//	priorTopID set + rowID equals priorTopID       → outcomeWaitRow
//	priorTopID nil OR rowID differs, no verdict    → outcomeWaitVerdict
//	priorTopID nil OR rowID differs, verdict set   → outcomeDone
//
// Fail-closed contract on the "can't pick rowID" branch:
// classifyHistoryAttempt MUST NOT treat a row whose id we can't
// extract as new. With priorTopID set, we know there's a previous
// run's row out there; an unparseable id could be that previous
// row's payload with a server-side shape change (renamed field,
// type drift). Treating it as new and falling through to the
// verdict check would let the gate fire on a stale verdict — the
// exact failure mode the priorTopID anchor exists to prevent.
//
// priorTopID being nil means "no prior history row" — the bug
// vector doesn't exist (no stale row is possible), so every row we
// see is the new one regardless of whether we can parse its id.
func classifyHistoryAttempt(row map[string]interface{}, priorTopID *int64) historyAttemptOutcome {
	if row == nil {
		return outcomeWaitRow
	}
	if priorTopID != nil {
		rowID := pickID(row)
		// Fail-closed branches: same row id, OR can't determine the
		// row id at all. Both might be the stale row.
		if rowID == nil || *rowID == *priorTopID {
			return outcomeWaitRow
		}
	}
	if pickVerdict(row) == "" {
		return outcomeWaitVerdict
	}
	return outcomeDone
}

// pickID extracts the id from a history summary row. Go's
// json.Unmarshal into map[string]interface{} parses JSON numbers
// as float64 by default, so we handle that path explicitly. Also
// handles json.Number (for callers that used a Decoder with
// UseNumber) and raw int64 (for tests that construct rows
// directly).
func pickID(row map[string]interface{}) *int64 {
	raw, ok := row["id"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case float64:
		id := int64(v)
		return &id
	case json.Number:
		id, err := v.Int64()
		if err == nil {
			return &id
		}
	case int64:
		id := v
		return &id
	case int:
		id := int64(v)
		return &id
	}
	return nil
}

// runsStartFlags holds command-local flag state for `tm runs start`.
// Lives at file scope so cobra's Run/RunE handlers can read it
// without per-invocation re-parsing.
type runsStartFlags struct {
	wait           bool
	failOnVerdict  string
	pollInterval   time.Duration
	waitTimeout    time.Duration
	verdictTimeout time.Duration
	region         string
	tags           []string
	noThresholds   bool
}

var runsStart runsStartFlags

func newRunsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Start, stop, pause, and resume traffic runs",
	}
	cmd.AddCommand(newRunsStartCmd())
	cmd.AddCommand(newRunsStopCmd())
	cmd.AddCommand(newRunsPauseCmd())
	cmd.AddCommand(newRunsResumeCmd())
	return cmd
}

// newRunsStartCmd is the flagship CI gating command.
//
// Behavior:
//
//   - Without --wait: POSTs /api/v1/profiles/{id}/start (or /runs when
//     --region / --tag are set) and prints the resulting status. Exits
//     0 on a 2xx; non-zero on any error.
//   - With --wait: polls /api/v1/profiles/{id} every --poll-interval
//     until the run leaves RUNNING/PAUSED, then fetches the latest
//     history entry for this profile to surface the auto-verdict.
//     If --fail-on-verdict is set, exits non-zero when the verdict
//     matches one of the listed values — the canonical use case is
//     a CI step that fails the build when a regression is detected.
//
// Verdict-to-exit-code mapping (when --fail-on-verdict triggers):
//
//	2 — FAIL  (one or more checks crossed the failure threshold)
//	3 — WARN  (one or more checks crossed the warn threshold)
//	4 — NO_BASELINE  (no baseline run designated)
//	1 — all other errors (HTTP, timeout, etc.)
//	0 — success / verdict not in --fail-on-verdict set
//
// Distinct codes per verdict so a CI script can branch — e.g. fail
// the build on FAIL but warn-only on WARN. Both share the catch-all
// `if $? -ne 0` shape for users who don't care.
//
// Thresholds are separate: absolute pass/fail limits stored on the
// profile (p95 < 200, errorRate <= 1, ...). With --wait, a run that
// breaks any of them exits 99 — the code k6 uses — whether or not
// --fail-on-verdict is set, and before the verdict gate is consulted.
// --no-thresholds reports them without failing.
func newRunsStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <profile-id>",
		Short: "Start a run for the given profile, optionally waiting for the verdict",
		Long: `Starts a traffic run for the given profile id. With --wait, polls until the run
finishes and surfaces the auto-comparison verdict — the canonical CI gating shape.

--tag stores tags on the run's history row, e.g. --tag "$GITHUB_SHA", so a
regression can be traced to a commit (filter with the history API's tag).
--region picks the dispatch region; by default the profile's own region is used.

With --wait, a run that breaks any of the profile's thresholds (e.g. p95 < 200)
exits 99. This happens without --fail-on-verdict and is checked first; pass
--no-thresholds to report them without failing.

Exit codes:
  99  thresholds failed (--wait; see above)
  2   FAIL          --fail-on-verdict: a check crossed the failure threshold
  3   WARN          --fail-on-verdict: a check crossed the warn threshold
  4   NO_BASELINE   --fail-on-verdict: no baseline run designated for the profile
  1   any other failure (HTTP error, timeout, ...)
  0   success`,
		Args:        cobra.ExactArgs(1),
		Annotations: authRequired(),
		RunE:        runStart,
	}
	cmd.Flags().BoolVar(&runsStart.wait, "wait", false,
		"Wait for the run to finish before returning")
	cmd.Flags().StringVar(&runsStart.failOnVerdict, "fail-on-verdict", "",
		"Comma-separated list of verdicts that should produce a non-zero exit (FAIL, WARN, NO_BASELINE). Requires --wait.")
	cmd.Flags().DurationVar(&runsStart.pollInterval, "poll-interval", 5*time.Second,
		"How often to poll for run status when --wait is set")
	cmd.Flags().DurationVar(&runsStart.waitTimeout, "wait-timeout", 30*time.Minute,
		"Maximum time to wait for a run to finish (--wait only)")
	cmd.Flags().DurationVar(&runsStart.verdictTimeout, "verdict-timeout", 60*time.Second,
		"Maximum time to wait for the history row + auto-verdict to be queryable after the run reaches a terminal status (--wait only). Increase on slow servers; lower to 0 to skip the verdict-presence wait.")
	cmd.Flags().StringVar(&runsStart.region, "region", "",
		`Dispatch region for this run (default: the profile's default region; "local" forces in-process dispatch)`)
	cmd.Flags().StringSliceVar(&runsStart.tags, "tag", nil,
		"Tag for the run's history row, e.g. a commit SHA; repeat or comma-separate for several (max 20)")
	cmd.Flags().BoolVar(&runsStart.noThresholds, "no-thresholds", false,
		"Report the profile's thresholds but don't exit 99 when they fail (--wait only)")
	return cmd
}

// startRun POSTs /start, or /runs when a region or tags are given, so a
// plain start keeps working against servers that predate /runs.
func startRun(ctx context.Context, c *api.ClientWithResponses, id int64, region string, tags []string) (int, []byte, error) {
	if region == "" && len(tags) == 0 {
		resp, err := c.StartWithResponse(ctx, id)
		if err != nil {
			return 0, nil, err
		}
		return resp.StatusCode(), resp.Body, nil
	}
	var body api.StartRunJSONRequestBody
	if region != "" {
		body.Region = &region
	}
	if len(tags) > 0 {
		body.Tags = &tags
	}
	resp, err := c.StartRunWithResponse(ctx, id, body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode(), resp.Body, nil
}

func runStart(cmd *cobra.Command, args []string) error {
	id, err := parseInt64(args[0])
	if err != nil {
		return fmt.Errorf("invalid profile id %q: %w", args[0], err)
	}
	if runsStart.failOnVerdict != "" {
		if !runsStart.wait {
			return fmt.Errorf("--fail-on-verdict requires --wait")
		}
		// Validate the WHOLE spec — covers both unknown tokens
		// (FAILL typo) AND delimiter-only inputs (`","`, `"   "`)
		// that normalize to an empty list. Either route would
		// otherwise produce a silent false-pass after the run
		// finishes. Catching here ahead of /start gives the user
		// immediate feedback AND avoids consuming the account's
		// request quota on a misconfigured invocation.
		if _, err := resolveFailOnVerdictSpec(runsStart.failOnVerdict); err != nil {
			return err
		}
	}

	c, err := api.NewClientWithResponses(config.BaseURL,
		api.WithRequestEditorFn(apiKeyRequestEditor(config.APIKey)))
	if err != nil {
		return err
	}

	// Phase 0 (--wait only): snapshot the top history id BEFORE
	// starting. The post-wait verdict fetch keys off this snapshot
	// to distinguish "my run's row landed" from "an older row's
	// already there." Without this, /history?size=1 immediately
	// after `/start` can return the PREVIOUS run's row — which
	// usually already has a verdict, so the gate would happily
	// (and wrongly) fire on stale data. nil priorTopID means "no
	// prior history" — every row we see post-start is, by
	// definition, the new run's row.
	var priorTopID *int64
	if runsStart.wait {
		priorTopID, err = fetchTopHistoryID(c, id)
		if err != nil {
			return fmt.Errorf("snapshot pre-start history id: %w", err)
		}
	}

	// Phase 1: POST /start. Always short-timeouted with shortCtx
	// because the start endpoint is non-blocking on the server side
	// — it sets up state then returns immediately.
	startCtx, cancel := shortCtx()
	defer cancel()
	status, body, err := startRun(startCtx, c, id, runsStart.region, runsStart.tags)
	if err != nil {
		return err
	}
	if status >= 400 {
		return errorFromResponse(status, body)
	}
	var startResp api.TrafficRunControlResponse
	if err := json.Unmarshal(body, &startResp); err != nil {
		return fmt.Errorf("decode start response: %w", err)
	}

	if !runsStart.wait {
		if config.JSON {
			return writeJSON(cmd.OutOrStdout(), startResp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Started run %s for profile %s (status=%s)\n",
			formatStrPtr(startResp.RunId), formatInt64Ptr(startResp.ProfileId), formatStrPtr(startResp.Status))
		return nil
	}

	// Phase 2: poll /profile/{id} until run status leaves
	// RUNNING/PAUSED. The status field of ApiProfileResponse is
	// the same one /start returned; using the profile-detail
	// endpoint (rather than a hypothetical /runs/{runId}) keeps
	// us on the documented v1 surface.
	if err := waitForRunFinish(cmd, c, id); err != nil {
		return err
	}

	// Phase 3: fetch the new history row's verdict. FOUR race
	// windows the wait closes:
	//   1. Terminal-status flip beats RunHistory row insert (row
	//      hasn't appeared yet);
	//   2. New row inserted but autoVerdict is empty because the
	//      auto-compare worker hasn't run;
	//   3. /history?size=1 returns the PREVIOUS run's row (the
	//      reason we snapshotted priorTopID in Phase 0). Without
	//      the anchor, a previous-run row with an already-populated
	//      verdict would be returned and the gate would fire on
	//      stale data — exact false-pass / false-fail bug.
	//   4. The newest row belongs to ANOTHER run of this profile: a
	//      schedule slot, a second pipeline or a UI click started it
	//      while ours was in flight, and the server replaces (cancels)
	//      the in-flight run, which then writes no row at all. The row
	//      is matched on the runId /start returned, so the gates never
	//      judge someone else's run as ours.
	startedRunID := ""
	if startResp.RunId != nil {
		startedRunID = *startResp.RunId
	}
	history, err := waitForHistoryWithVerdict(cmd, c, id, priorTopID, startedRunID)
	if err != nil {
		return fmt.Errorf("fetch run history: %w", err)
	}

	if config.JSON {
		if err := writeJSON(cmd.OutOrStdout(), history); err != nil {
			return err
		}
	} else {
		printHistorySummary(cmd, history)
	}

	// Thresholds before the verdict: they are the profile's own absolute
	// limits, so a run that breaks them fails the build regardless of how it
	// compares with the baseline.
	if !runsStart.noThresholds {
		if err := resolveThresholdExit(history); err != nil {
			return err
		}
	}
	return resolveGateExit(history, runsStart.failOnVerdict)
}

// exitCodeThresholdsFailed is the exit code for a run that broke any of its
// profile's thresholds — the same code k6 uses, so pipelines ported from k6
// keep their handling.
const exitCodeThresholdsFailed = 99

// resolveThresholdExit fails the build when the run broke a threshold. The
// server writes thresholdStatus in the same save as the history row, so,
// unlike the auto-verdict, there is no pending state to fail closed on:
// once the row exists, an absent status means the profile has no thresholds.
func resolveThresholdExit(history map[string]interface{}) error {
	status, _ := history["thresholdStatus"].(string)
	if !strings.EqualFold(status, "FAIL") {
		return nil
	}
	var failed []string
	for _, r := range thresholdResults(history) {
		if passed, _ := r["passed"].(bool); !passed {
			failed = append(failed, describeThreshold(r))
		}
	}
	msg := "thresholds failed"
	if len(failed) > 0 {
		msg += ": " + strings.Join(failed, "; ")
	}
	return &exitCodeError{code: exitCodeThresholdsFailed, msg: msg}
}

// thresholdResults extracts the per-threshold results from a history row,
// skipping anything that isn't an object.
func thresholdResults(history map[string]interface{}) []map[string]interface{} {
	raw, _ := history["thresholdResults"].([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// describeThreshold renders one result as e.g. "p95 < 200ms (actual 340.5ms)".
func describeThreshold(r map[string]interface{}) string {
	metric, _ := r["metric"].(string)
	op, _ := r["op"].(string)
	unit := thresholdUnit(metric)
	actual := "no data"
	if a, ok := r["actual"]; ok && a != nil {
		actual = "actual " + formatThresholdNumber(a) + unit
	}
	return fmt.Sprintf("%s %s %s%s (%s)", metric, op, formatThresholdNumber(r["value"]), unit, actual)
}

// formatThresholdNumber prints a history number in plain decimal. The server
// serialises doubles the way Java's Double.toString does — 200.0, 3.0E-4 —
// and printing those verbatim (they arrive as json.Number) would put
// "200.0ms" and "3.0E-4%" in CI logs.
func formatThresholdNumber(v interface{}) string {
	switch n := v.(type) {
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return n.String()
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func thresholdUnit(metric string) string {
	switch metric {
	case "p50", "p75", "p95", "p99":
		return "ms"
	case "errorRate":
		return "%"
	case "avgRps", "peakRps":
		return " rps"
	default:
		return ""
	}
}

// resolveGateExit translates a history row + --fail-on-verdict spec
// into an exit decision. Extracted from runStart for test
// reachability — the inline form was a hidden-state if-block that
// no unit test could exercise without mocking the full API client.
//
// Fail-closed contract: if --fail-on-verdict is set AND the history
// row has no autoVerdict yet, this returns a non-zero exit-code
// error rather than nil. The previous shape returned nil (exit 0)
// in that case — a slow async auto-compare worker would produce a
// false-pass on CI, exactly the failure mode --fail-on-verdict
// exists to PREVENT. With a gate set, indeterminate state must
// always surface as a failure, not silently pass.
func resolveGateExit(history map[string]interface{}, failOnVerdictSpec string) error {
	if failOnVerdictSpec == "" {
		// No gate configured — exit 0 regardless of verdict
		// presence. Used by `tm runs start --wait` without
		// --fail-on-verdict, where the verdict is informational
		// (printed to stdout) but doesn't gate the build.
		return nil
	}
	verdict := pickVerdict(history)
	if verdict == "" {
		// Gate is set but the run produced no verdict. This is
		// either:
		//   - --verdict-timeout was too short for the auto-compare
		//     worker to populate the field (increase the timeout),
		//   - auto-comparison isn't enabled for this profile (no
		//     baseline designated), OR
		//   - the verdict legitimately couldn't be computed.
		// All three are "couldn't evaluate the gate"; CI should
		// fail closed rather than emit a false-pass.
		return &exitCodeError{
			code: 1,
			msg: "could not determine auto-verdict within --verdict-timeout; refusing to exit 0 with --fail-on-verdict set " +
				"(check that auto-comparison is configured for the profile, or raise --verdict-timeout)",
		}
	}
	for _, want := range parseVerdictList(failOnVerdictSpec) {
		if verdict == want {
			return &exitCodeError{
				code: exitCodeForVerdict(verdict),
				msg:  fmt.Sprintf("verdict %q matched --fail-on-verdict", verdict),
			}
		}
	}
	return nil
}

func waitForRunFinish(cmd *cobra.Command, c *api.ClientWithResponses, profileID int64) error {
	deadline := time.Now().Add(runsStart.waitTimeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("run did not finish within %s", runsStart.waitTimeout)
		}

		ctx, cancel := shortCtx()
		resp, err := c.GetProfileWithResponse(ctx, profileID)
		cancel()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 400 {
			return errorFromResponse(resp.StatusCode(), resp.Body)
		}
		var p api.ApiProfileResponse
		if err := json.Unmarshal(resp.Body, &p); err != nil {
			return fmt.Errorf("decode profile during poll: %w", err)
		}
		status := formatStrPtr(p.Status)
		if status != "RUNNING" && status != "PAUSED" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Run finished (status=%s)\n", status)
			return nil
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Run %s, polling again in %s...\n",
			strings.ToLower(status), runsStart.pollInterval)

		// Respect outer context cancellation (Ctrl-C).
		select {
		case <-time.After(runsStart.pollInterval):
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		}
	}
}

// waitForHistoryWithVerdict polls the profile's newest history rows
// with bounded exponential backoff until either:
//
//   - the row for the run we just started exists — matched on
//     startedRunID, or for servers that don't record run ids, an
//     id distinct from priorTopID (i.e. not a leftover from an
//     earlier run) — AND has a non-empty autoVerdict, OR
//   - the --verdict-timeout budget is exhausted, in which case the
//     most-recent NEW row (verdict-less) is returned with a
//     warning, OR a clear "no new row appeared" error when even
//     row insert never happened.
//
// Four race windows this closes, all happening AFTER the run
// reaches a terminal in-memory status (the fourth, another run of
// the same profile landing first, is selectHistoryRow's job):
//
//  1. RunHistory row insert lags terminal-status by a few hundred
//     ms (the writer is on a different transactional boundary
//     than the in-memory state flip).
//  2. autoVerdict is populated by an async auto-compare worker
//     that runs only after row insert — empty until the worker
//     completes.
//  3. /history?size=1 returns the PREVIOUS run's row in the
//     pre-insert window. That older row very likely DOES have an
//     autoVerdict (the worker has had plenty of time), so naive
//     anchor-by-recency would fire the CI gate on stale data.
//     priorTopID lets us reject same-id rows as "not ours yet."
//
// Backoff starts at 500ms and doubles up to 5s per attempt.
// Setting --verdict-timeout=0 disables the wait and behaves like
// a single fetch (useful in tests).
func waitForHistoryWithVerdict(cmd *cobra.Command, c *api.ClientWithResponses, profileID int64, priorTopID *int64, startedRunID string) (map[string]interface{}, error) {
	var (
		deadline    = time.Now().Add(runsStart.verdictTimeout)
		backoff     = 500 * time.Millisecond
		maxBackoff  = 5 * time.Second
		lastNewRow  map[string]interface{} // most-recent row WHOSE ID DIFFERS from priorTopID
		otherRunIDs []string               // newer rows seen that belong to other runs of the profile
		attempts    int
	)
	for {
		attempts++
		rows, err := fetchHistoryPage(c, profileID, historyScanSize)
		switch {
		case err == nil:
			row, others := selectHistoryRow(rows, priorTopID, startedRunID)
			if len(others) > 0 {
				otherRunIDs = others
			}
			// Defer the row-vs-priorTopID + verdict-presence
			// decision to the pure classifier so the loop body
			// reads as a state machine and unit-tests can
			// exercise every outcome.
			switch classifyHistoryAttempt(row, priorTopID) {
			case outcomeDone:
				return row, nil
			case outcomeWaitVerdict:
				// New row, no verdict yet. Capture for the
				// timeout-but-incomplete branch below.
				lastNewRow = row
			case outcomeWaitRow:
				// Still the previous run's row. Don't update
				// lastNewRow — we explicitly want the
				// "no new row appeared" failure mode if the
				// timeout fires here.
			}
		case errors.Is(err, errHistoryRowNotReady):
			// Empty content[] — no row yet. Fall through to backoff.
		default:
			// Persistent failure (401, 403, 500, network, decode
			// error). Surface immediately. Retrying would just
			// delay the same failure behind a misleading
			// "no history row appeared within N" message.
			return nil, fmt.Errorf("history lookup failed: %w", err)
		}

		// If verdictTimeout=0 the caller opted out of waiting —
		// return whatever the single attempt produced. Useful in
		// tests; production CI always sets a non-zero timeout.
		if runsStart.verdictTimeout == 0 {
			if lastNewRow != nil {
				return lastNewRow, nil
			}
			return nil, errHistoryRowNotReady
		}

		if time.Now().After(deadline) {
			if lastNewRow != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"WARNING: verdict not populated within %s (%d attempts); returning history without verdict\n",
					runsStart.verdictTimeout, attempts)
				return lastNewRow, nil
			}
			if len(otherRunIDs) > 0 {
				return nil, fmt.Errorf("no history row for the run this command started (runId %s) appeared within %s (%d attempts); the newer rows for profile id %d are from other runs (%s). Starting a profile again replaces a run in flight, so ours was probably cancelled by a concurrent start. Refusing to gate on another run's result",
					startedRunID, runsStart.verdictTimeout, attempts, profileID, strings.Join(otherRunIDs, ", "))
			}
			return nil, fmt.Errorf("no NEW history row appeared within %s (%d attempts) for profile id %d — the run we just started may not have been persisted yet",
				runsStart.verdictTimeout, attempts, profileID)
		}

		select {
		case <-time.After(backoff):
		case <-cmd.Context().Done():
			return nil, cmd.Context().Err()
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// fetchTopHistoryID returns the id of the latest history row for
// this profile, or (nil, nil) when there's no prior history.
// Called from runStart BEFORE /start so the post-wait verdict
// loop can anchor against it.
//
// nil is a legitimate "no prior row" answer — first run of a
// freshly-created profile. A nil priorTopID makes
// classifyHistoryAttempt treat every row it sees as new (which
// is correct).
func fetchTopHistoryID(c *api.ClientWithResponses, profileID int64) (*int64, error) {
	row, err := fetchLatestHistoryForProfile(c, profileID)
	if errors.Is(err, errHistoryRowNotReady) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := pickID(row)
	if id == nil {
		return nil, fmt.Errorf("pre-start history row missing id field — server response shape changed?")
	}
	return id, nil
}

// historyScanSize is how many of the profile's newest history rows the
// post-run wait inspects for the row whose runId matches the run this
// command started. Rows come newest-first, so ours is near the top
// unless other runs of the profile finished after it.
const historyScanSize = 20

// pickRunID returns a history row's runId, or "" when the row has none
// (rows written before the server recorded run ids).
func pickRunID(row map[string]interface{}) string {
	runID, _ := row["runId"].(string)
	return runID
}

// selectHistoryRow picks, from a newest-first page of the profile's
// history, the row to judge the run this command started by, plus the
// runIds of newer rows that belong to other runs.
//
// With a startedRunID it returns the row whose runId matches, wherever it
// sits on the page. When none matches but the rows carry runIds, it returns
// nil: those rows are other runs of the same profile (starting a profile
// again replaces the run in flight, and the replaced run writes no row),
// and gating on one of them would report someone else's result as ours.
// Only rows without runIds (servers that predate them) fall back to the
// newest row, which classifyHistoryAttempt then checks against priorTopID.
func selectHistoryRow(rows []map[string]interface{}, priorTopID *int64, startedRunID string) (map[string]interface{}, []string) {
	if len(rows) == 0 {
		return nil, nil
	}
	if startedRunID == "" {
		return rows[0], nil
	}
	var others []string
	for _, row := range rows {
		if id := pickID(row); priorTopID != nil && id != nil && *id == *priorTopID {
			break // the pre-start snapshot: every row from here on is older
		}
		runID := pickRunID(row)
		if runID == startedRunID {
			return row, others
		}
		if runID != "" {
			others = append(others, runID)
		}
	}
	if pickRunID(rows[0]) != "" {
		return nil, others
	}
	return rows[0], others
}

// fetchLatestHistoryForProfile returns the profile's most recent run
// summary, or errHistoryRowNotReady when it has none yet, so the retry
// loop above can distinguish "wait longer" from "real error".
func fetchLatestHistoryForProfile(c *api.ClientWithResponses, profileID int64) (map[string]interface{}, error) {
	rows, err := fetchHistoryPage(c, profileID, 1)
	if err != nil {
		return nil, err
	}
	return rows[0], nil
}

// fetchHistoryPage hits /history?profileId=<id>&size=<size> and
// unmarshals the newest run summaries, newest first. Returns
// errHistoryRowNotReady when the profile has no rows at all.
func fetchHistoryPage(c *api.ClientWithResponses, profileID int64, size int32) ([]map[string]interface{}, error) {
	ctx, cancel := shortCtx()
	defer cancel()

	page := int32(0)
	params := &api.ListHistoryParams{
		ProfileId: &profileID,
		Size:      &size,
		Page:      &page,
	}
	resp, err := c.ListHistoryWithResponse(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() >= 400 {
		return nil, errorFromResponse(resp.StatusCode(), resp.Body)
	}
	// /history returns Map<String,Object> on the server; decode into
	// a Go map so we can pluck the verdict + numeric fields without
	// needing a brittle nested struct here. The summary contains
	// `autoVerdict`, `autoVerdictReasons`, totals, and timestamps.
	//
	// UseNumber() over plain json.Unmarshal: the default decoder
	// would coerce the row's `id` to float64, which silently loses
	// precision for ids >= 2^53. The classifier compares ids
	// exactly against priorTopID, so any precision loss could
	// false-positive the "row id == priorTopID" anchor check and
	// keep the wait loop spinning forever on a freshly-inserted
	// row whose float64-rounded id happened to collide. UseNumber
	// keeps numerics as json.Number (string-backed, lossless);
	// pickID has a json.Number case so the classifier sees the
	// real int64 value.
	dec := json.NewDecoder(bytes.NewReader(resp.Body))
	dec.UseNumber()
	var page0 struct {
		Content []map[string]interface{} `json:"content"`
	}
	if err := dec.Decode(&page0); err != nil {
		return nil, fmt.Errorf("decode history page: %w", err)
	}
	if len(page0.Content) == 0 {
		return nil, errHistoryRowNotReady
	}
	return page0.Content, nil
}

// pickVerdict pulls the autoVerdict string from a history summary,
// returning "" if absent so callers can treat that as "no verdict
// to gate on".
func pickVerdict(history map[string]interface{}) string {
	if v, ok := history["autoVerdict"].(string); ok {
		return v
	}
	return ""
}

func parseVerdictList(spec string) []string {
	parts := strings.Split(spec, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToUpper(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validVerdictTokens enumerates the verdicts that are meaningful in
// a --fail-on-verdict spec. PASS is deliberately NOT included —
// gating on PASS would fail every successful run, which is almost
// certainly a typo (intended FAIL?) rather than a real CI policy.
// Reject up front; if a genuine "fail on every outcome" use case
// emerges, the set is one line to extend.
var validVerdictTokens = map[string]struct{}{
	"FAIL":        {},
	"WARN":        {},
	"NO_BASELINE": {},
}

// resolveFailOnVerdictSpec is the full pre-flight check for the
// --fail-on-verdict input. Returns the canonical token list on
// success, or an error explaining what's wrong with the raw spec.
// Pairs three rejection modes that, combined, close every
// false-pass vector through this flag:
//
//  1. **Delimiter-only / whitespace-only spec** (e.g. `","` or
//     `"   "`): parseVerdictList drops empty tokens, so the spec
//     normalizes to []. validateVerdictList would happily accept
//     [] as "no unknown tokens" → resolveGateExit iterates zero
//     tokens → command exits 0 even on FAIL. Reject up front.
//     The unset-flag case can't reach here because runStart's
//     `if runsStart.failOnVerdict != ""` check gates the call.
//  2. **Unknown tokens** (e.g. FAILL typo): handled by
//     validateVerdictList below.
//  3. **PASS in the spec**: handled by validateVerdictList
//     (PASS isn't in the canonical set on purpose).
func resolveFailOnVerdictSpec(spec string) ([]string, error) {
	parsed := parseVerdictList(spec)
	if len(parsed) == 0 {
		return nil, fmt.Errorf("--fail-on-verdict was set to %q but parsed to no usable tokens — pass at least one of: FAIL, WARN, NO_BASELINE", spec)
	}
	if err := validateVerdictList(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// validateVerdictList rejects unknown tokens in --fail-on-verdict.
// Without this, a typo like `FAILL` parses cleanly into a
// one-element list, never matches a real verdict, and the gate
// silently exits 0 — exactly the false-pass shape --fail-on-verdict
// exists to prevent.
//
// Empty-list handling lives in resolveFailOnVerdictSpec, not here:
// this function treats an empty list as "no unknown tokens to
// report" (vacuously valid) so it can be reused for the unset-flag
// path. The combined caller path checks both.
func validateVerdictList(verdicts []string) error {
	var unknown []string
	for _, v := range verdicts {
		if _, ok := validVerdictTokens[v]; !ok {
			unknown = append(unknown, v)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	valid := make([]string, 0, len(validVerdictTokens))
	for v := range validVerdictTokens {
		valid = append(valid, v)
	}
	// Sort for stable error messages — map iteration is random and
	// the error message is asserted by tests.
	sortStrings(valid)
	return fmt.Errorf("unknown verdict(s) in --fail-on-verdict: %s. Valid values: %s",
		strings.Join(unknown, ", "), strings.Join(valid, ", "))
}

// sortStrings is a tiny shim around sort.Strings to avoid importing
// the sort package solely for one use site in error formatting.
// (Using a fixed-size insertion sort because we have ≤4 elements.)
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}

// exitCodeForVerdict maps a verdict to the CI gating exit code
// documented in the command's Long help.
func exitCodeForVerdict(verdict string) int {
	switch strings.ToUpper(verdict) {
	case "FAIL":
		return 2
	case "WARN":
		return 3
	case "NO_BASELINE":
		return 4
	default:
		return 1
	}
}

// exitCodeError carries an exit code through cobra's err return so
// main() can translate it to os.Exit. cobra prints the .msg field
// via SilenceErrors-disabled chain; we keep SilenceErrors=true at
// the root and let main() handle the message.
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }
func (e *exitCodeError) ExitCode() int { return e.code }

// printHistorySummary writes a few key fields from a history
// summary in human-readable form. Verdict, totals, success rate,
// and the verdict reasons (which explain WHY the gate triggered
// — critical for actionable CI logs).
func printHistorySummary(cmd *cobra.Command, h map[string]interface{}) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Run id:       %v\n", h["id"])
	fmt.Fprintf(out, "Verdict:      %v\n", h["autoVerdict"])
	fmt.Fprintf(out, "Total reqs:   %v\n", h["totalRequests"])
	fmt.Fprintf(out, "Total errors: %v\n", h["totalErrors"])
	fmt.Fprintf(out, "Avg RPS:      %v\n", h["avgRps"])
	fmt.Fprintf(out, "Peak RPS:     %v\n", h["peakRps"])
	if reasons, ok := h["autoVerdictReasons"].(string); ok && reasons != "" {
		fmt.Fprintf(out, "Reasons:      %s\n", reasons)
	}
	if status, ok := h["thresholdStatus"].(string); ok && status != "" {
		fmt.Fprintf(out, "Thresholds:   %s\n", status)
		for _, r := range thresholdResults(h) {
			mark := "FAIL"
			if passed, _ := r["passed"].(bool); passed {
				mark = "pass"
			}
			fmt.Fprintf(out, "  %s  %s\n", mark, describeThreshold(r))
		}
	}
}

func newRunsStopCmd() *cobra.Command {
	return runControlCmd("stop", "Stop the in-flight run for a profile (idempotent)",
		wrapControlCall("stop", func(ctx context.Context, c *api.ClientWithResponses, id int64) (int, []byte, error) {
			r, err := c.StopWithResponse(ctx, id)
			if err != nil {
				return 0, nil, err
			}
			return r.StatusCode(), r.Body, nil
		}))
}

func newRunsPauseCmd() *cobra.Command {
	return runControlCmd("pause", "Pause the in-flight run for a profile (idempotent)",
		wrapControlCall("pause", func(ctx context.Context, c *api.ClientWithResponses, id int64) (int, []byte, error) {
			r, err := c.PauseWithResponse(ctx, id)
			if err != nil {
				return 0, nil, err
			}
			return r.StatusCode(), r.Body, nil
		}))
}

func newRunsResumeCmd() *cobra.Command {
	return runControlCmd("resume", "Resume a paused run",
		wrapControlCall("resume", func(ctx context.Context, c *api.ClientWithResponses, id int64) (int, []byte, error) {
			r, err := c.ResumeWithResponse(ctx, id)
			if err != nil {
				return 0, nil, err
			}
			return r.StatusCode(), r.Body, nil
		}))
}

// wrapControlCall centralizes the parse-and-error-handle path
// shared by stop / pause / resume. Three nearly-identical inline
// closures used `_ = json.Unmarshal(...)` which silently swallowed
// decode failures — a 2xx response with a server-side schema drift
// would print empty fields and exit 0, hiding contract drift.
// Pulling the decode into one helper makes the "decode must
// succeed on 2xx, body is opaque on 4xx+" contract explicit and
// the per-verb callsites are 5-line "hit the endpoint, return
// status + body" stubs.
func wrapControlCall(verb string, doCall func(context.Context, *api.ClientWithResponses, int64) (int, []byte, error)) func(context.Context, *api.ClientWithResponses, int64) (*api.TrafficRunControlResponse, int, []byte, error) {
	return func(ctx context.Context, c *api.ClientWithResponses, id int64) (*api.TrafficRunControlResponse, int, []byte, error) {
		status, body, err := doCall(ctx, c, id)
		if err != nil {
			return nil, 0, nil, err
		}
		// 4xx/5xx: caller formats the error from status + body via
		// errorFromResponse. Body is NOT a TrafficRunControlResponse
		// shape on this branch (it's the {"error":"..."} JSON
		// envelope from GlobalExceptionHandler), so attempting to
		// decode as the control DTO would produce noise.
		if status >= 400 {
			return nil, status, body, nil
		}
		// 2xx: the body MUST parse as TrafficRunControlResponse.
		// A decode failure here means the server's contract changed
		// (renamed field, removed field) — fail loudly rather than
		// printing empty/default fields and exiting 0. CI scripts
		// that read the parsed status field would silently regress
		// otherwise.
		var parsed api.TrafficRunControlResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, status, body, fmt.Errorf("decode %s response (HTTP %d): %w", verb, status, err)
		}
		return &parsed, status, body, nil
	}
}

// runControlCmd builds one of stop/pause/resume — they share the
// exact same shape (POST /profiles/{id}/<verb>, decode
// TrafficRunControlResponse, print status). Pull the variant out
// into a callback to keep cobra registration concise.
func runControlCmd(verb, short string, call func(context.Context, *api.ClientWithResponses, int64) (*api.TrafficRunControlResponse, int, []byte, error)) *cobra.Command {
	return &cobra.Command{
		Use:         verb + " <profile-id>",
		Short:       short,
		Args:        cobra.ExactArgs(1),
		Annotations: authRequired(),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseInt64(args[0])
			if err != nil {
				return fmt.Errorf("invalid profile id %q: %w", args[0], err)
			}
			c, err := api.NewClientWithResponses(config.BaseURL,
				api.WithRequestEditorFn(apiKeyRequestEditor(config.APIKey)))
			if err != nil {
				return err
			}
			ctx, cancel := shortCtx()
			defer cancel()
			parsed, status, body, err := call(ctx, c, id)
			if err != nil {
				return err
			}
			if status >= 400 {
				return errorFromResponse(status, body)
			}
			if config.JSON {
				return writeJSON(cmd.OutOrStdout(), parsed)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s acknowledged for profile %s (status=%s)\n",
				strings.Title(verb), formatInt64Ptr(parsed.ProfileId), formatStrPtr(parsed.Status))
			return nil
		},
	}
}
