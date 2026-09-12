#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

native_goos=$(env -u GOOS -u GOARCH go env GOOS)
native_goarch=$(env -u GOOS -u GOARCH go env GOARCH)
target_goos=${GOOS:-$native_goos}
target_goarch=${GOARCH:-$native_goarch}

case "$target_goos/$target_goarch" in
  darwin/arm64) extension=dylib ;;
  linux/amd64|linux/arm64) extension=so ;;
  *)
    echo "unsupported target: $target_goos/$target_goarch (supported: darwin/arm64, linux/amd64, linux/arm64)" >&2
    exit 2
    ;;
esac

export CGO_ENABLED=${CGO_ENABLED:-1}
export GOOS="$target_goos"
export GOARCH="$target_goarch"

if [[ "$target_goos" == "$native_goos" && "$target_goarch" == "$native_goarch" ]]; then
  go test ./...
  go vet ./...
fi

if [[ -n "${OUTPUT:-}" ]]; then
  artifact=$OUTPUT
elif [[ "$target_goos" == "$native_goos" && "$target_goarch" == "$native_goarch" ]]; then
  artifact="web-accounts.$extension"
else
  # Cross-builds never replace a binary for another platform/architecture.
  artifact="dist/$target_goos/$target_goarch/web-accounts.$extension"
fi

mkdir -p "$(dirname "$artifact")"
go build -trimpath -buildmode=c-shared -o "$artifact" .
printf 'built %s/%s -> %s\n' "$target_goos" "$target_goarch" "$artifact"
