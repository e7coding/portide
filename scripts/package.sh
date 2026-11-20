#!/usr/bin/env bash
# 交叉编译 portide 并打包 Windows / Linux / macOS 版本到 dist/；中继 portide-relay 另外打 Linux 包（含 Dockerfile）。
# 版本号取自 core/version/VERSION（见 scripts/version.sh）。
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/version.sh
rm -rf dist/portide-"$version"-* dist/portide-relay-"$version"-*
for target in linux/amd64 linux/arm64 windows/amd64 darwin/arm64 darwin/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  out="dist/portide-$version-$os-$arch"
  ext=""; [ "$os" = windows ] && ext=".exe"
  mkdir -p "$out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "$out/portide$ext" ./cmd/portide
  cp docs/en/cli.md "$out/README.md"
  if [ "$os" = windows ]; then
    (cd dist && python3 -m zipfile -c "portide-$version-$os-$arch.zip" "portide-$version-$os-$arch")
  else
    tar -C dist -czf "dist/portide-$version-$os-$arch.tar.gz" "portide-$version-$os-$arch"
  fi
  echo "built $out"
done
for arch in amd64 arm64; do
  out="dist/portide-relay-$version-linux-$arch"
  mkdir -p "$out"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "$out/portide-relay" ./cmd/portide-relay
  cp deploy/relay/Dockerfile docs/中继部署.md "$out/" && cp docs/en/relay.md "$out/README.md"
  tar -C dist -czf "dist/portide-relay-$version-linux-$arch.tar.gz" "portide-relay-$version-linux-$arch"
  echo "built $out"
done
