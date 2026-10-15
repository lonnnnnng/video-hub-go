#!/bin/zsh
set -euo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$project_root/bin"
go -C "$project_root" build -trimpath -o "$project_root/bin/moviehub" ./cmd/moviehub
print "构建完成：$project_root/bin/moviehub"
