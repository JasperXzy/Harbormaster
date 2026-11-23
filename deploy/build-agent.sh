#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
release_dir=${1:-deploy/bin/agent-releases}
release_version=$(go run ./internal/agentrelease/prepare "${2:-dev}")
mkdir -p "$release_dir"
for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/JasperXzy/harbormaster/internal/buildinfo.Version=$release_version" -o "$release_dir/hm-agent-linux-$arch" ./cmd/hm-agent
done
cp deploy/agent/install.sh deploy/agent/hm-agent.service "$release_dir/"
printf '%s\n' "$release_version" > "$release_dir/VERSION"
(
  cd "$release_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum hm-agent-linux-amd64 hm-agent-linux-arm64 hm-agent.service install.sh VERSION > SHA256SUMS
  else
    shasum -a 256 hm-agent-linux-amd64 hm-agent-linux-arm64 hm-agent.service install.sh VERSION > SHA256SUMS
  fi
)
