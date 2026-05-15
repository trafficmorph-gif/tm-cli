# TrafficMorph CLI — GitHub Action

Install the `tm` CLI on a workflow runner and configure it with your
API key + base URL. Wraps the [install script](../install.sh) so the
binary becomes available to every subsequent `run:` step in the
same job.

## Minimal usage — gate a build on a regression verdict

```yaml
name: Load-test gate
on: [pull_request]

jobs:
  load-test:
    runs-on: ubuntu-latest
    steps:
      - uses: trafficmorph/tm-cli/cli/action@v0.1.0
        with:
          api-key: ${{ secrets.TRAFFICMORPH_API_KEY }}
          base-url: https://app.trafficmorph.example.com
      - name: Start traffic profile + wait + fail on regression
        run: tm runs start 42 --wait --fail-on-verdict FAIL,WARN
```

The action's `runs start --wait --fail-on-verdict FAIL,WARN` step
exits with a verdict-specific code (2 = FAIL, 3 = WARN, 4 =
NO_BASELINE). The job fails when the verdict matches.

## Inputs

| Input | Required | Default | Notes |
|---|---|---|---|
| `version` | no | `latest` | Pin to an exact tag (`v0.1.0`) for reproducible workflows. `latest` resolves via the GitHub Releases API on each run, which means newer releases roll in automatically — fine for trunk-targeted jobs, risky for release branches. |
| `api-key` | no | — | Pass via secret: `${{ secrets.TRAFFICMORPH_API_KEY }}`. Exported as `$TM_API_KEY` for downstream steps; also added to the action log mask so it can't accidentally leak. |
| `base-url` | no | — | TrafficMorph base URL. Exported as `$TM_BASE_URL` for downstream steps. Omit if your job sets it via repo / org env vars instead. |
| `install-dir` | no | `~/.local/bin` | Directory the `tm` binary lands in. Almost never needs overriding. |
| `github-repo` | no | `trafficmorph/tm-cli` | Override the source repo for forks / staging. Advanced use only. |

## Outputs

| Output | Notes |
|---|---|
| `version` | The exact version that got installed (the resolved tag when `version: latest`). Useful for log assertions or for pinning a downstream re-install to the same version. |

## Version pinning

**Always pin to an exact tag** (`@v0.1.0`) in production workflows.
`@v1` (the moving major-version convention some actions maintain)
is NOT published — Renovate/Dependabot will surface release updates
as PRs, which is the safe trade-off.

## Supported runner OSes

| Runner | Supported via this action? | Notes |
|---|---|---|
| `ubuntu-*` | ✅ | Primary target. amd64 + arm64 binaries available. |
| `macos-*` | ✅ | amd64 + arm64 binaries available. |
| `windows-*` | ❌ | The install script intentionally fails on `msys*` / `mingw*` / `cygwin*` because the `.tar.gz` extract path doesn't map to Windows native tooling. Windows users should download the `.zip` asset directly from [the releases page](https://github.com/trafficmorph/tm-cli/releases) in a separate workflow step. PRs adding Windows support to the install script — `.zip` extract via `tar` (Windows 10+ ships tar) or `Expand-Archive` — are welcome. |

## Composite action — why not Docker / JS?

| Action type | Why we DIDN'T pick it |
|---|---|
| **Docker** | Forces `runs-on: ubuntu-latest`; macOS users can't use the action at all. |
| **JS** | Requires bundling `node_modules/` into the action repo; adds a build / commit dance every release. |
| **Composite** ← | Pure YAML + shell. Runs on Linux + macOS runners (Windows excluded per the table above). Reuses the install script that human users curl directly, so there's only one install path to maintain. |

## What the action installs

- Binary: `tm` (Go 1.25, statically linked, ~9 MB stripped)
- Source: https://github.com/trafficmorph/tm-cli — `cli/` directory
- License: proprietary (see the parent project's terms)

## Action ref vs binary version — they're independent

**Important:** pinning the action ref (`@v0.2.0`) selects which
`action.yml` runs. It does **not** select which `tm` binary gets
installed — that's controlled by the `version` input, which defaults
to `latest`.

So the bare invocation:

```yaml
- uses: trafficmorph/tm-cli/cli/action@v0.2.0
```

…runs the v0.2.0 action.yml but installs **the latest** release binary
— which may drift past v0.2.0 over time. For strict pinning, set
both:

```yaml
- uses: trafficmorph/tm-cli/cli/action@v0.2.0
  with:
    version: v0.2.0
```

GitHub Actions does **not** support expression interpolation in
`uses:` — the `@<ref>` part must be a literal string. So a tempting
DRY shape like `uses: …@${{ env.TM_VERSION }}` will fail workflow
validation. Two valid ways to keep the two pins in sync:

**Option 1 — repeat the literal** (simplest):

```yaml
- uses: trafficmorph/tm-cli/cli/action@v0.2.0
  with:
    version: v0.2.0
```

Renovate / Dependabot can bump both lines automatically because they
match the same tag pattern. Two-line change per version bump; trivial
to review.

**Option 2 — centralize via a reusable workflow** (better for repos
running tm in many workflows):

```yaml
# .github/workflows/tm-load-test.yml — the reusable wrapper
on:
  workflow_call:
    inputs:
      profile-id: { required: true, type: string }
jobs:
  load-test:
    runs-on: ubuntu-latest
    steps:
      # Single pin lives here, shared across every consumer workflow.
      - uses: trafficmorph/tm-cli/cli/action@v0.2.0
        with:
          version: v0.2.0
          api-key: ${{ secrets.TRAFFICMORPH_API_KEY }}
      - run: tm runs start ${{ inputs.profile-id }} --wait --fail-on-verdict FAIL
```

Consumer workflows then `uses: ./.github/workflows/tm-load-test.yml`
and pass only the profile id — the version pin centralizes in one
place per repo.

## Releasing a new action version

Action and CLI binary versions are cut together as a single git tag
(the release workflow builds the binary; checking out the tag
provides the action.yml). After cutting `v0.2.0`, both
`@v0.2.0` (action) and `version: v0.2.0` (binary) become available
on the same release.

```
git tag -a v0.2.0 -m "tm v0.2.0"
git push origin v0.2.0
```

The `.github/workflows/release.yml` workflow handles binary builds
and GitHub Release upload.
