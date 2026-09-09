#!/usr/bin/env bash
# build-release.sh — produce release artifacts for all supported platforms.
#
# Output:
#   dist/privatedns_<VERSION>_<OS>_<ARCH>.tar.gz  (unix)
#   dist/privatedns_<VERSION>_<OS>_<ARCH>.zip     (windows)
#   dist/SHA256SUMS
#
# Each archive contains:
#   privatedns[.exe]        the binary
#   README.md
#   deploy/                 platform-appropriate install scripts + service units
#   LICENSE (if present at repo root)
#
# Pure Go, no CGo — cross-compiles cleanly from any host.

set -euo pipefail
cd "$(dirname "$0")"

VERSION="${VERSION:-$(grep -m1 'version.*=' main.go | sed -E 's/.*"([^"]+)".*/\1/')}"
: "${VERSION:?cannot determine version}"

log() { printf '[release] %s\n' "$*"; }

OUT=dist
rm -rf "$OUT"
mkdir -p "$OUT"

# Deploy assets by target OS.
have_zip() { command -v zip >/dev/null 2>&1; }
have_tar() { command -v tar >/dev/null 2>&1; }

# Repo root (for LICENSE).
REPO_ROOT="$(cd .. && pwd)"

# Target matrix.
TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "linux/arm"
  "windows/amd64"
  "windows/arm64"
  "darwin/amd64"
  "darwin/arm64"
)

build_one() {
  local target="$1"
  local os="${target%/*}"
  local arch="${target#*/}"
  local ext=""
  [ "$os" = "windows" ] && ext=".exe"

  local stage="$OUT/staging/privatedns_${VERSION}_${os}_${arch}"
  mkdir -p "$stage"

  log "building $target"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o "$stage/privatedns${ext}" .

  # Copy README + LICENSE.
  cp README.md "$stage/README.md"
  [ -f "$REPO_ROOT/LICENSE" ] && cp "$REPO_ROOT/LICENSE" "$stage/LICENSE"

  # Copy platform-appropriate deploy assets.
  case "$os" in
    linux)
      mkdir -p "$stage/deploy"
      cp -r deploy/linux "$stage/deploy/"
      ;;
    darwin)
      mkdir -p "$stage/deploy"
      cp -r deploy/macos "$stage/deploy/"
      ;;
    windows)
      # Windows uses the built-in `service` subcommand — no external assets.
      # Ship a minimal quickstart batch script.
      cat > "$stage/install-windows.ps1" <<'EOF'
# Run in an elevated PowerShell prompt.
$ErrorActionPreference = 'Stop'
$exe = (Resolve-Path .\privatedns.exe).Path
$env:PRIVATEDNS_DATA_DIR = "$env:ProgramData\privatedns"
New-Item -ItemType Directory -Force -Path $env:PRIVATEDNS_DATA_DIR | Out-Null
& $exe service install
& $exe service start
Write-Host "privatedns service installed. Use 'privatedns service status' to check."
EOF
      ;;
  esac

  # Archive. Use absolute output path so we can cd into the staging dir.
  local outAbs
  outAbs="$(cd "$OUT" && pwd)"
  local archive
  local base="privatedns_${VERSION}_${os}_${arch}"
  if [ "$os" = "windows" ] && have_zip; then
    archive="$outAbs/${base}.zip"
    (cd "$OUT/staging" && zip -qr "$archive" "$base")
  else
    archive="$outAbs/${base}.tar.gz"
    (cd "$OUT/staging" && tar czf "$archive" "$base")
  fi
  log "  -> $(basename "$archive") ($(du -h "$archive" | cut -f1))"
}

for t in "${TARGETS[@]}"; do
  build_one "$t"
done

# Cleanup staging.
rm -rf "$OUT/staging"

# Checksums.
log "generating SHA256SUMS"
(cd "$OUT" && sha256sum privatedns_* > SHA256SUMS)

log "done. Artifacts in $OUT/"
ls -lh "$OUT"
