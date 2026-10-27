# TrafficMorph CLI

```
████████╗██████╗  █████╗ ███████╗███████╗██╗ ██████╗
╚══██╔══╝██╔══██╗██╔══██╗██╔════╝██╔════╝██║██╔════╝
   ██║   ██████╔╝███████║█████╗  █████╗  ██║██║
   ██║   ██╔══██╗██╔══██║██╔══╝  ██╔══╝  ██║██║
   ██║   ██║  ██║██║  ██║██║     ██║     ██║╚██████╗
   ╚═╝   ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝     ╚═╝     ╚═╝ ╚═════╝
```

[![Latest release](https://img.shields.io/github/v/release/trafficmorph-gif/tm-cli?sort=semver&label=latest)](https://github.com/trafficmorph-gif/tm-cli/releases)
[![License](https://img.shields.io/github/license/trafficmorph-gif/tm-cli)](LICENSE)

`tm` is a single static-binary CLI for the TrafficMorph `/api/v1`
API. Designed for CI integration: trigger a run, wait for the
regression verdict, and gate the build on the result.

```
tm <command> [flags]
```

## Install

### One-line install (recommended)

```bash
curl -sSL https://raw.githubusercontent.com/trafficmorph-gif/tm-cli/main/install.sh | sh
```

Detects your OS + architecture, downloads the matching binary from
GitHub Releases, verifies its SHA-256, and installs to
`~/.local/bin/tm`. Linux amd64/arm64 + macOS amd64/arm64 supported.

**Pin a version (recommended for CI):**

```bash
curl -sSL https://raw.githubusercontent.com/trafficmorph-gif/tm-cli/main/install.sh \
  | TM_VERSION=v0.4.0 sh
```

**Override the install dir** (e.g. inside Docker):

```bash
curl -sSL https://raw.githubusercontent.com/trafficmorph-gif/tm-cli/main/install.sh \
  | TM_INSTALL_DIR=/usr/local/bin sh
```

### GitHub Actions

```yaml
- uses: trafficmorph-gif/tm-cli/cli/action@v0.4.0
  with:
    api-key: ${{ secrets.TRAFFICMORPH_API_KEY }}
    base-url: https://app.your-trafficmorph-host.com
- run: tm runs start 42 --wait --fail-on-verdict FAIL,WARN
```

See [`action/README.md`](./action/README.md) for inputs and the
composite-action design.

### Manual download

Grab the right tarball from
[the releases page](https://github.com/trafficmorph-gif/tm-cli/releases),
extract, and move `tm` somewhere on your `PATH`. Use this when you
can't run the install script (corporate proxies, etc.).

### From source

```bash
git clone https://github.com/trafficmorph-gif/tm-cli.git
cd tm-cli
make build           # → bin/tm
sudo cp bin/tm /usr/local/bin/
```

### Verify

```bash
$ tm version
tm v0.4.0 (spec v1)
```

## Prerequisites

- **A TrafficMorph API key** in the form `tm_…`. Provision one from the in-app **Settings → API keys** page.
- **A reachable TrafficMorph install.** Examples below assume `http://localhost:8092` for local development; swap for your hosted URL. There is no built-in default — the CLI requires the base URL to be set explicitly.

## Authentication

Commands that talk to the server — `tm profiles list|get`,
`tm runs start|stop|pause|resume`, `tm history get` — require both
an API key and a base URL. Offline / local commands (`tm version`,
`tm help …`, `tm completion …`, any `--help` flow, plus the bare
parent groups like `tm profiles`) work without configuration, so
first-run setup like `tm completion bash >> ~/.bashrc` succeeds
before you've provisioned a key.

Set both values:

```bash
export TM_API_KEY=tm_xxxxxxxxxxxxxxxx
export TM_BASE_URL=https://app.your-trafficmorph-host.com
```

…or pass them per invocation with `--api-key` / `--base-url`.

Malformed inputs (missing scheme, wrong scheme, query strings,
fragments in the base URL; control bytes in the API key) are
rejected at startup rather than failing late on the first call.

## Common workflows

### CI gating — fail the build on a regression

```bash
tm runs start 42 --wait --fail-on-verdict FAIL,WARN
```

Starts a run for profile 42, polls until it finishes, fetches the
auto-comparison verdict, and exits with a verdict-specific code so
your CI script can branch:

| Exit code | Meaning |
|-----------|---------|
| 0 | Passed: thresholds held, and the verdict was PASS or not in `--fail-on-verdict` |
| 1 | HTTP / network / configuration error |
| 2 | FAIL — one or more checks crossed the failure threshold |
| 3 | WARN — one or more checks crossed the warn threshold |
| 4 | NO_BASELINE — no baseline run designated for the profile |
| 99 | Thresholds failed — see below |

### CI gating — fail the build on absolute limits (thresholds)

The verdict compares a run with an earlier one. Thresholds are absolute
limits stored on the profile — "p95 under 200 ms, under 1% errors" —
and need no baseline:

```bash
curl --fail-with-body -X PUT "$TM_BASE_URL/api/v1/profiles/42/thresholds" -H "X-Api-Key: $TM_API_KEY" \
  -H 'Content-Type: application/json' -d '{
    "thresholds": [
      {"metric": "p95",       "op": "<",  "value": 200},
      {"metric": "errorRate", "op": "<=", "value": 1},
      {"metric": "avgRps",    "op": ">=", "value": 95}
    ]}'
```

That replaces only the thresholds (`[]` removes them), so you can keep
them in your repository and apply them in a step before the run. Keep
`--fail-with-body` (or `-f`): without it curl exits 0 when the server
rejects the body with a 400, and the run is then gated on whatever
thresholds the profile had before, possibly none. A body without a
`thresholds` key is rejected too, so a mistyped key can't remove them.
They can also be set in the app, in the **Alerts** tab of the profile
sidebar. Then:

```bash
tm runs start 42 --wait
```

exits **99** if any threshold failed — the same code k6 uses — and lists
which ones and by how much:

```
Thresholds:   FAIL
  FAIL  p95 < 200ms (actual 340.5ms)
  pass  errorRate <= 1% (actual 0.25%)
  pass  avgRps >= 95 rps (actual 98.3 rps)
```

No flag is needed: with `--wait`, a profile's thresholds always gate,
and they are checked before `--fail-on-verdict`. Use both to fail on an
absolute limit *or* a regression. `--no-thresholds` reports them without
failing.

| Metric | Unit | Meaning |
|---|---|---|
| `p50` `p75` `p95` `p99` | ms | Response time percentiles, as shown on the run page* |
| `errorRate` | % (0–100) | Requests with a non-2xx status or no response |
| `avgRps` `peakRps` | requests/s | Requests sent, counted when dispatched whether or not a response came back. They drop when a plan, profile or account limit throttles the run, not when the target fails, so pair them with `errorRate` |

Operators are `<`, `<=`, `>`, `>=`; up to 20 thresholds per profile. A
threshold whose metric has no data — a latency limit on a run where
nothing got a response — **fails** rather than passing on missing data.

\*They are a sample-weighted mean of 5-second bucket percentiles, not
percentiles pooled across the whole run. On a spiky run they can read
far from the run-wide figure: a single 5-second stall moves the run-wide
p95 a lot but barely moves the mean of bucket p95s, while the mean p50
rises above the run-wide median. Requests that got no response (timeouts,
refused connections) add no latency sample at all, so a latency limit
alone won't catch them; that is what `errorRate` is for. On runs longer
than about 7 hours only the most recent ~7 hours of buckets count
towards the latency and `peakRps` figures. Thresholds use the same
numbers the run page shows, so the gate and the UI always agree.

Tag the run with the commit so a regression can be traced back to it,
and pick a dispatch region if the profile's default isn't the one you
want (`local` forces in-process dispatch):

```bash
tm runs start 42 --wait --fail-on-verdict FAIL --tag "$GITHUB_SHA" --region eu-west-1
```

`--tag` can be repeated or comma-separated (up to 20 tags); tags are
stored lowercase on the run's history row.

### List profiles

```
$ tm profiles list
ID  NAME                         CREATED
42  smoke-test-api               2026-04-12T09:14:01Z
43  load-test-checkout           2026-04-14T17:22:55Z
```

JSON for scripting:

```bash
tm profiles list --json | jq -r '.[] | "\(.id) \(.name)"'
```

### Inspect a past run

```bash
tm history get 1234 > run-1234.json
```

Returns the full metric set: response-code distribution, latency
quantiles, RPS / latency time series, secondary stats from response
scripts, the auto-comparison snapshot, and verdict reasoning.

### Run lifecycle control

```bash
tm runs start 42                      # one-shot start, don't wait
tm runs stop 42                       # idempotent — succeeds even if no run is in flight
tm runs pause 42                      # idempotent
tm runs resume 42                     # 400 if no paused run to resume
```

## Configuration

| Source | Precedence |
|--------|------------|
| Command-line flags | Highest |
| Environment variables (`TM_*`) | Middle |

| Flag | Env var | Default | Notes |
|------|---------|---------|-------|
| `--api-key` | `TM_API_KEY` | _(required)_ | Full `tm_…` value. Rejected at startup if it contains control bytes (CR/LF/NUL/DEL). |
| `--base-url` | `TM_BASE_URL` | _(required)_ | URL of your TrafficMorph install. Rejected at startup if it's missing the scheme, wrong scheme, has a query string, or has a fragment. Path-prefixed deployments behind reverse proxies keep their prefix during URL resolution. |
| `--timeout` | — | `30s` | Per-HTTP-call timeout. |
| `--json` | — | `false` | Emit machine-readable JSON. |

## License

Apache 2.0 — see [LICENSE](LICENSE).
