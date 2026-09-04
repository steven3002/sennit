#!/usr/bin/env bash
#
# Build the release artifacts for every platform we support.
#
#   ./scripts/release.sh            build from the current commit
#   VERSION=0.2.0 ./scripts/release.sh
#
# Output lands in dist/. Nothing here touches the network or your vault.
#
# The binaries are pure Go (CGO_ENABLED=0), so every target cross-compiles from
# any host and the result has no shared-library dependencies. That is the whole
# reason a stranger can be handed one file and have it work.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

version="${VERSION:-$(grep -o 'Version = "[^"]*"' build/version.go | cut -d'"' -f2)}"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
date="$(date -u +%Y-%m-%d)"
dirty=""
if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
  dirty="-dirty"
  printf '\033[1mWARNING: the working tree is dirty. Stamping the commit as %s%s.\033[0m\n' "$commit" "$dirty"
fi

dist="$root/dist"
rm -rf "$dist"; mkdir -p "$dist"

ldflags="-s -w"
ldflags="$ldflags -X github.com/steven3002/sennit/build.Commit=${commit}${dirty}"
ldflags="$ldflags -X github.com/steven3002/sennit/build.Date=${date}"

printf '\nsennit %s (%s%s, %s)\n\n' "$version" "$commit" "$dirty" "$date"

targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"
for target in $targets; do
  os="${target%/*}"; arch="${target#*/}"
  name="sennit_${version}_${os}_${arch}"
  stage="$dist/$name"
  mkdir -p "$stage"

  ext=""
  [ "$os" = "windows" ] && ext=".exe"

  for cmd in sennit sennit-mcp; do
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "$ldflags" -o "$stage/${cmd}${ext}" "./cmd/$cmd"
  done

  # Ship the things a person needs in order to trust and use the binary.
  cp README.md LICENSE "$stage/"
  cp docs/cost.md docs/host-checks.md "$stage/" 2>/dev/null || true

  if [ "$os" = "windows" ]; then
    # Windows users expect a zip. `zip` is not installed everywhere, and Python
    # is, so this does not make the release depend on a package nobody has.
    if command -v zip >/dev/null 2>&1; then
      ( cd "$dist" && zip -qr "$name.zip" "$name" )
    else
      ( cd "$dist" && python3 -c 'import shutil,sys; shutil.make_archive(sys.argv[1], "zip", ".", sys.argv[1])' "$name" )
    fi
    archive="$name.zip"
  else
    ( cd "$dist" && tar czf "$name.tar.gz" "$name" )
    archive="$name.tar.gz"
  fi
  rm -rf "$stage"
  printf '  %-40s %s\n' "$archive" "$(du -h "$dist/$archive" | cut -f1)"
done

# One checksum file over every archive. A release without checksums asks people
# to run an unverified binary that holds the key to all their memories.
( cd "$dist" && sha256sum ./*.tar.gz ./*.zip > SHA256SUMS 2>/dev/null || sha256sum ./* > SHA256SUMS )
printf '\n  %-40s\n' "SHA256SUMS"
printf '\ndist/ is ready.\n'
