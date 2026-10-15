#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
service_user="${MOVIEHUB_USER:-$(id -un)}"
install_dir="${MOVIEHUB_INSTALL_DIR:-$project_root}"
data_dir="${MOVIEHUB_DATA_DIR:-${install_dir}/data}"
media_dir="${MOVIEHUB_MEDIA_DIR:-${install_dir}/media}"
listen="${MOVIEHUB_LISTEN:-127.0.0.1:4488}"
service_name="${MOVIEHUB_SERVICE_NAME:-video-hub-go}"

if [[ ! "$service_user" =~ ^[a-z_][a-z0-9_-]*$ || ! "$service_name" =~ ^[a-z][a-z0-9_-]*$ ]]; then
  printf '服务用户或服务名格式无效\n' >&2
  exit 2
fi
for path in "$install_dir" "$data_dir" "$media_dir"; do
  if [[ ! "$path" =~ ^/[A-Za-z0-9_./-]+$ || "$path" == *"/../"* || "$path" == */.. || "$path" == *"/./"* ]]; then
    printf '服务目录必须是无空格、无父目录跳转的绝对路径：%s\n' "$path" >&2
    exit 2
  fi
done
if [[ ! "$listen" =~ ^[A-Za-z0-9.:-]+:[0-9]+$ ]]; then
  printf '监听地址格式无效：%s\n' "$listen" >&2
  exit 2
fi
port="${listen##*:}"
if (( ${#port} > 5 )) || (( 10#$port < 1 || 10#$port > 65535 )); then
  printf '监听端口范围应为 1–65535\n' >&2
  exit 2
fi

if (( $# > 1 )); then
  printf '用法：%s [输出.service路径]\n' "$0" >&2
  exit 2
fi
if (( $# == 1 )); then
  output="$1"
  if [[ -e "$output" ]]; then
    printf '服务文件已存在，未覆盖：%s\n' "$output" >&2
    exit 1
  fi
  if [[ ! -d "$(dirname "$output")" ]]; then
    printf '输出目录不存在：%s\n' "$(dirname "$output")" >&2
    exit 1
  fi
else
  output="$(mktemp "${TMPDIR:-/tmp}/${service_name}.XXXXXXXX.service")"
fi

template="$(<"${project_root}/deploy/systemd/moviehub.service.in")"
# long: 只替换受限的用户、路径和监听值，先生成文件供管理员审阅，不直接写入 /etc 或启用服务。
template="${template//@SERVICE_USER@/$service_user}"
template="${template//@INSTALL_DIR@/$install_dir}"
template="${template//@DATA_DIR@/$data_dir}"
template="${template//@MEDIA_DIR@/$media_dir}"
template="${template//@LISTEN@/$listen}"
printf '%s\n' "$template" > "$output"
printf '已生成：%s\n' "$output"
printf '请检查 User、ExecStart、ReadWritePaths 与 .env 后再安装为 %s.service\n' "$service_name"
