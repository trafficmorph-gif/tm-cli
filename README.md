# tm — TrafficMorph CLI

`tm` is a single static-binary CLI for the TrafficMorph `/api/v1`
surface. Designed for CI integration: trigger a run, wait for the
regression verdict, and gate the build on the result.

```
tm <command> [flags]
```

## Install

### One-line install (recommended)

```
curl -sSL https://raw.githubusercontent.com/trafficmorph/tm-cli/main/cli/install.sh | sh
```

Detects your OS + architecture, downloads the matching binary from
GitHub Releases, verifies its SHA-256, and installs to
`~/.local/bin/tm`. Linux amd64/arm64 + macOS amd64/arm64 supported.

**Pin a version (recommended for CI):**

```
curl -sSL https://raw.githubusercontent.com/trafficmorph/tm-cli/main/cli/install.sh \
  | TM_VERSION=v0.1.0 sh
```

**Override the install dir** (e.g. inside Docker):

```
curl -sSL https://raw.githubusercontent.com/trafficmorph/tm-cli/main/cli/install.sh \
  | TM_INSTALL_DIR=/usr/local/bin sh
```

### GitHub Actions

```yaml
- uses: trafficmorph/tm-cli/cli/action@v0.1.0
  with:
    api-key: ${{ secrets.TRAFFICMORPH_API_KEY }}
    base-url: https://app.trafficmorph.example.com
- run: tm runs start 42 --wait --fail-on-verdict FAIL,WARN
```

See [`action/README.md`](./action/README.md) for inputs and the
composite-action design.

### Manual download

Grab the right tarball from
[the releases page](https://github.com/trafficmorph/tm-cli/releases),
extract, and move `tm` somewhere on your PATH. Use this when you
can't run the install script (corporate proxies, etc.).

### From source

```
make -C cli build           # → cli/bin/tm
sudo cp cli/bin/tm /usr/local/bin/
```

### Verify

```
$ tm version
tm v0.1.0 (spec v1)
```

## Authentication

Commands that talk to the server — `tm profiles list|get`,
`tm runs start|stop|pause|resume`, `tm history get` — require an
API key. Offline / local commands (`tm version`, `tm help …`,
`tm completion …`, any `--help` flow, plus the bare parent groups
like `tm profiles`) work without one, so first-run setup like
`tm completion bash >> ~/.bashrc` succeeds before you've provisioned
a key.

Provision a key from the in-app **Settings → API keys** page and
expose it via either:

```
export TM_API_KEY=tm_xxxxxxxxxxxxxxxx
export TM_BASE_URL=https://your-trafficmorph-instance.example.com
```

…or pass them per invocation with `--api-key` / `--base-url`.

On the cloud build, API access is a TEAM+ feature. On self-hosted
installs (`app.deployment-mode=SELF_HOSTED`) every authenticated user
gets full access regardless of stored plan tier.

## Common workflows

### CI gating — fail the build on a regression

```
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

### List profiles

```
$ tm profiles list
ID  NAME                         CREATED
42  smoke-test-api               2026-04-12T09:14:01Z
43  load-test-checkout           2026-04-14T17:22:55Z
```

JSON for scripting:

```
tm profiles list --json | jq -r '.[] | "\(.id) \(.name)"'
```

### Inspect a past run

```
tm history get 1234 > run-1234.json
```

Returns the full metric set: response-code distribution, latency
quantiles, RPS / latency time series, secondary stats from response
scripts, the auto-comparison snapshot, and verdict reasoning.

### Run lifecycle control

```
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
| Built-in defaults | Lowest |

| Flag | Env var | Default | Notes |
|------|---------|---------|-------|
| `--api-key` | `TM_API_KEY` | _(none — required)_ | Full `tm_…` key value. |
| `--base-url` | `TM_BASE_URL` | `https://app.trafficmorph.example.com` | Override per environment. |
| `--timeout` | — | `30s` | Per-HTTP-call timeout. |
| `--json` | — | `false` | Emit machine-readable JSON. |

## Architecture

- **`cmd/tm/main.go`** — entry point, exit-code threading.
- **`internal/cli/`** — cobra command tree, config loading, auth.
- **`internal/api/`** — generated typed HTTP client (regenerate via
  `make regen-client` after a spec update).
- **`openapi/v1.json` / `openapi/v1.yaml`** — versioned snapshot of
  the server's `/api/v1` spec. Both formats committed: JSON for the
  Go client generator, YAML for downstream SDK generators that prefer
  it. Regenerate via `make regen-spec`.

## Regenerating after a server-side API change

```
make regen-spec      # boot Spring, fetch /v3/api-docs/v1, write openapi/v1.{json,yaml}
make regen-client    # run oapi-codegen against openapi/v1.json
make build           # rebuild the binary
make test            # smoke-check
```

Commit the snapshot files alongside the source change so the CLI's
client stays in lockstep with the server.
