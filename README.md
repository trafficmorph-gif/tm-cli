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
  | TM_VERSION=v0.3.0 sh
```

**Override the install dir** (e.g. inside Docker):

```bash
curl -sSL https://raw.githubusercontent.com/trafficmorph-gif/tm-cli/main/install.sh \
  | TM_INSTALL_DIR=/usr/local/bin sh
```

### GitHub Actions

```yaml
- uses: trafficmorph-gif/tm-cli/cli/action@v0.3.0
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
tm v0.3.0 (spec v1)
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
| 0 | Verdict was PASS, or not in `--fail-on-verdict` set |
| 1 | HTTP / network / configuration error |
| 2 | FAIL — one or more checks crossed the failure threshold |
| 3 | WARN — one or more checks crossed the warn threshold |
| 4 | NO_BASELINE — no baseline run designated for the profile |

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
