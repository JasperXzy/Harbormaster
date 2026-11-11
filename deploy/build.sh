#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
go run ./internal/ui/prepare
mkdir -p deploy/bin
CGO_ENABLED=0 go build -trimpath -o deploy/bin/hm-server ./cmd/hm-server
CGO_ENABLED=0 go build -trimpath -o deploy/bin/hm-agent ./cmd/hm-agent
