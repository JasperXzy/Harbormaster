#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
release_dir=${1:-deploy/bin/agent-releases}
mkdir -p "$release_dir"
for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -o "$release_dir/hm-agent-linux-$arch" ./cmd/hm-agent
done
cp deploy/agent/install.sh deploy/agent/hm-agent.service "$release_dir/"
(
  cd "$release_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum hm-agent-linux-amd64 hm-agent-linux-arm64 hm-agent.service install.sh > SHA256SUMS
  else
    shasum -a 256 hm-agent-linux-amd64 hm-agent-linux-arm64 hm-agent.service install.sh > SHA256SUMS
  fi
)
