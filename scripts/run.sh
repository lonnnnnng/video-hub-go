#!/bin/zsh
set -euo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_root"
if [[ ! -x "$project_root/bin/moviehub" ]]; then
  zsh "$project_root/scripts/build.sh"
fi
exec "$project_root/bin/moviehub" "$@"
