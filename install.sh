#!/bin/sh
# POSIX install script for the `tm` CLI.
#
# Usage:
#
#   curl -sSL https://raw.githubusercontent.com/trafficmorph/tm-cli/main/cli/install.sh | sh
#
# Pin a specific version (recommended for CI):
#
#   curl -sSL https://raw.githubusercontent.com/trafficmorph/tm-cli/main/cli/install.sh | TM_VERSION=v0.1.0 sh
#
# Override the install location (default $HOME/.local/bin, falls
# back to /usr/local/bin if $HOME/.local/bin isn't writable):
#
#   curl ... | TM_INSTALL_DIR=/opt/tm/bin sh
#
# Detects OS/arch via `uname`, downloads the matching tarball from
# the GitHub Release, verifies the SHA-256 against the published
# checksums file, extracts the binary, and writes it to the chosen
# directory.
#
# Safe for piping into `sh` because every command runs through the
# `set -e` failure-fast contract — partial-download failures abort
# before anything lands on disk.

set -eu

# Configuration knobs that callers can override via env.
TM_VERSION="${TM_VERSION:-latest}"
TM_INSTALL_DIR="${TM_INSTALL_DIR:-}"
TM_GITHUB_REPO="${TM_GITHUB_REPO:-trafficmorph/tm-cli}"

# ── Logging helpers ─────────────────────────────────────────────────
# Use stderr for status output so a caller redirecting stdout still
# sees install progress, and so anything machine-readable (currently
# nothing) doesn't get mingled with the status stream.
log()  { printf '%s\n' "$*" >&2; }
fail() { printf 'tm-install: error: %s\n' "$*" >&2; exit 1; }

# ── Required tools ──────────────────────────────────────────────────
# curl and tar are universally available on macOS / modern Linux.
# `command -v` works in any POSIX shell.
for cmd in curl tar uname; do
  command -v "$cmd" >/dev/null 2>&1 || fail "required command '$cmd' not found in PATH"
done

# SHA-256 verifier — try shasum (macOS) then sha256sum (Linux).
# If neither is present we'd rather skip the check loudly than
# silently install an unverified binary.
sha_verifier=""
if command -v shasum >/dev/null 2>&1; then
  sha_verifier="shasum -a 256"
elif command -v sha256sum >/dev/null 2>&1; then
  sha_verifier="sha256sum"
fi

# ── OS / arch detection ─────────────────────────────────────────────
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux|darwin) ;;
  msys*|mingw*|cygwin*)
    fail "Windows is not supported via this script — download the .zip asset from https://github.com/$TM_GITHUB_REPO/releases manually" ;;
  *) fail "unsupported OS: $os" ;;
esac

arch_raw="$(uname -m)"
case "$arch_raw" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) fail "unsupported architecture: $arch_raw" ;;
esac

# ── Resolve target version ──────────────────────────────────────────
if [ "$TM_VERSION" = "latest" ]; then
  log "Resolving latest release..."
  # GitHub's latest-release API redirects to the canonical tag.
  # `-Ls` follows redirects silently; `-o /dev/null -w '%{url_effective}'`
  # captures the final URL after redirect without downloading the page.
  latest_url="$(curl -Ls -o /dev/null -w '%{url_effective}' "https://github.com/$TM_GITHUB_REPO/releases/latest")"
  TM_VERSION="${latest_url##*/}"
  if [ -z "$TM_VERSION" ] || [ "$TM_VERSION" = "latest" ]; then
    fail "could not resolve latest version — try pinning with TM_VERSION=v0.1.0"
  fi
fi
log "Installing tm $TM_VERSION for $os/$arch"

# ── Pick install dir ────────────────────────────────────────────────
if [ -z "$TM_INSTALL_DIR" ]; then
  # Prefer ~/.local/bin so we don't need sudo. Fall back to
  # /usr/local/bin only if ~/.local/bin can't be created.
  if mkdir -p "$HOME/.local/bin" 2>/dev/null && [ -w "$HOME/.local/bin" ]; then
    TM_INSTALL_DIR="$HOME/.local/bin"
  elif [ -w /usr/local/bin ]; then
    TM_INSTALL_DIR="/usr/local/bin"
  else
    fail "no writable install dir — set TM_INSTALL_DIR or run with sudo"
  fi
fi
log "Install dir: $TM_INSTALL_DIR"

# ── Download tarball + checksums ────────────────────────────────────
# Strip the leading `v` for the version inside the asset filename;
# the tag has it but the goreleaser archive template does too, so
# we end up with `tm_v0.1.0_linux_amd64.tar.gz` either way. Keep
# this naming in sync with cli/.goreleaser.yml's name_template.
version_no_v="${TM_VERSION#v}"
asset="tm_v${version_no_v}_${os}_${arch}.tar.gz"
checksums="tm_v${version_no_v}_SHA256SUMS"
base_url="https://github.com/$TM_GITHUB_REPO/releases/download/$TM_VERSION"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

log "Downloading $asset"
curl -fL --proto '=https' --tlsv1.2 -o "$tmp_dir/$asset" "$base_url/$asset" \
  || fail "download failed: $base_url/$asset"

if [ -n "$sha_verifier" ]; then
  log "Verifying SHA-256"
  curl -fL --proto '=https' --tlsv1.2 -o "$tmp_dir/$checksums" "$base_url/$checksums" \
    || fail "checksums file download failed: $base_url/$checksums"

  expected="$(grep "  $asset\$" "$tmp_dir/$checksums" | awk '{print $1}')"
  [ -n "$expected" ] || fail "no checksum for $asset in $checksums"

  actual="$(cd "$tmp_dir" && $sha_verifier "$asset" | awk '{print $1}')"
  [ "$expected" = "$actual" ] || \
    fail "checksum mismatch for $asset (expected $expected, got $actual)"
else
  log "WARNING: no SHA-256 tool found (shasum / sha256sum); skipping checksum verification"
fi

# ── Extract + install ───────────────────────────────────────────────
log "Extracting"
tar -xzf "$tmp_dir/$asset" -C "$tmp_dir" tm \
  || fail "tar extraction failed"

log "Installing to $TM_INSTALL_DIR/tm"
mv "$tmp_dir/tm" "$TM_INSTALL_DIR/tm" \
  || fail "could not install to $TM_INSTALL_DIR/tm — set TM_INSTALL_DIR or run with sudo"
chmod +x "$TM_INSTALL_DIR/tm"

# ── PATH hint ───────────────────────────────────────────────────────
case ":$PATH:" in
  *":$TM_INSTALL_DIR:"*) ;;
  *) log "NOTE: $TM_INSTALL_DIR is not in your PATH. Add to your shell rc:"
     log "    export PATH=\"$TM_INSTALL_DIR:\$PATH\"" ;;
esac

log "Installed: $("$TM_INSTALL_DIR/tm" version 2>/dev/null || echo 'tm (run failed — check PATH)')"
log "Next: 'export TM_API_KEY=tm_...' and 'tm profiles list' to verify."
