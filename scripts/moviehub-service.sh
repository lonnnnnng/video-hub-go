#!/usr/bin/env bash
set -euo pipefail

service_name="${MOVIEHUB_SERVICE_NAME:-moviehub}"
health_url="${MOVIEHUB_HEALTH_URL:-http://127.0.0.1:4488/healthz}"
action="${1:-help}"
if (( $# > 0 )); then shift; fi

if [[ ! "$service_name" =~ ^[a-z][a-z0-9_-]*$ ]]; then
  printf '服务名格式无效\n' >&2
  exit 2
fi
case "$action" in
  help|-h|--help)
    printf '用法：%s {status|start|stop|restart|logs [-f]|health|help}\n' "$0"
    exit 0
    ;;
  status|start|stop|restart|logs|health) ;;
  *) printf '未知命令：%s\n' "$action" >&2; exit 2 ;;
esac

if [[ "$(uname -s)" != Linux || ! -d /run/systemd/system ]]; then
  printf '管理命令仅支持运行 systemd 的 Linux；本机可用 scripts/run.sh 启动\n' >&2
  exit 1
fi

case "$action" in
  status|start|stop|restart)
    if (( $# > 0 )); then printf '%s 不接受额外参数\n' "$action" >&2; exit 2; fi
    exec systemctl "$action" "${service_name}.service"
    ;;
  logs)
    if (( $# == 0 )); then exec journalctl -u "${service_name}.service" -n 100 --no-pager; fi
    if (( $# == 1 )) && [[ "$1" == -f ]]; then exec journalctl -u "${service_name}.service" -n 100 -f --no-pager; fi
    printf 'logs 仅接受 -f\n' >&2
    exit 2
    ;;
  health)
    if (( $# > 0 )); then printf 'health 不接受额外参数\n' >&2; exit 2; fi
    # long: 健康检查只访问本机公开端点，不携带访问令牌，也不向其他主机发送运行信息。
    if [[ ! "$health_url" =~ ^http://(127\.0\.0\.1|localhost|\[::1\]):[0-9]+/healthz$ ]]; then
      printf 'MOVIEHUB_HEALTH_URL 必须是本机 /healthz 地址\n' >&2
      exit 2
    fi
    response="$(curl --fail --silent --show-error --max-time 5 "$health_url")"
    if [[ "$response" != *'"status":"ok"'* ]]; then
      printf '服务健康响应无效\n' >&2
      exit 1
    fi
    printf '服务健康：%s\n' "$response"
    ;;
esac
