#!/usr/bin/env bash
set -euo pipefail

service_name="${MOVIEHUB_SERVICE_NAME:-video-hub-go}"
service_scope="${MOVIEHUB_SYSTEMD_SCOPE:-user}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
action="${1:-help}"
if (( $# > 0 )); then shift; fi

if [[ ! "$service_name" =~ ^[a-z][a-z0-9_-]*$ ]]; then
  printf '服务名格式无效\n' >&2
  exit 2
fi
if [[ "$service_scope" == user ]]; then
  systemctl_cmd=(systemctl --user)
  journalctl_cmd=(journalctl --user)
elif [[ "$service_scope" == system ]]; then
  systemctl_cmd=(systemctl)
  journalctl_cmd=(journalctl)
else
  printf 'MOVIEHUB_SYSTEMD_SCOPE 必须是 user 或 system\n' >&2
  exit 2
fi
case "$action" in
  help|-h|--help)
    printf '用法：%s {status|start|stop|restart|logs [-f]|health|check-update|update|help}\n' "$0"
    exit 0
    ;;
  status|start|stop|restart|logs|health|check-update|update) ;;
  *) printf '未知命令：%s\n' "$action" >&2; exit 2 ;;
esac

if [[ "$(uname -s)" != Linux || ! -d /run/systemd/system ]]; then
  printf '管理命令仅支持运行 systemd 的 Linux；本机可用 scripts/run.sh 启动\n' >&2
  exit 1
fi

case "$action" in
  check-update|update)
    if (( $# > 0 )); then printf '%s 不接受额外参数\n' "$action" >&2; exit 2; fi
    exec python3 "${script_dir}/moviehub-update.py" "$action"
    ;;
  status|start|stop|restart)
    if (( $# > 0 )); then printf '%s 不接受额外参数\n' "$action" >&2; exit 2; fi
    exec "${systemctl_cmd[@]}" "$action" "${service_name}.service"
    ;;
  logs)
    if (( $# == 0 )); then exec "${journalctl_cmd[@]}" -u "${service_name}.service" -n 100 --no-pager; fi
    if (( $# == 1 )) && [[ "$1" == -f ]]; then exec "${journalctl_cmd[@]}" -u "${service_name}.service" -n 100 -f --no-pager; fi
    printf 'logs 仅接受 -f\n' >&2
    exit 2
    ;;
  health)
    if (( $# > 0 )); then printf 'health 不接受额外参数\n' >&2; exit 2; fi
    # long: 与升级器共用实际 unit 的监听地址，避免只监听局域网 IP 时误查 127.0.0.1。
    exec python3 "${script_dir}/moviehub-update.py" health
    ;;
esac
