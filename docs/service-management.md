# Linux 服务管理脚本

`scripts/moviehub-service.sh` 管理 systemd 服务；`health`、`check-update` 和 `update` 会调用同目录的 `moviehub-update.py`。两份脚本须放在同一目录。管理脚本仅支持运行 systemd 的 Linux；在 macOS 开发机上请用 `scripts/run.sh` 启动程序。

## 当前服务器：用户服务

通过 SSH 登录服务器后，以 `lee` 用户执行，不要加 `sudo`：

```bash
ssh -i ~/.ssh/id_ed25519 lee@192.168.1.2
cd /home/lee/video-hub-go
./scripts/moviehub-service.sh help
./scripts/moviehub-service.sh status
./scripts/moviehub-service.sh health
```

当前服务名为 `video-hub-go.service`，监听 `192.168.1.2:4444`。管理页面为 `http://192.168.1.2:4444/`。`health` 会读取 unit 中的 `--listen` 参数并查询 `/healthz`；正常时输出运行版本，例如 `服务健康：v0.2.0`。

| 命令 | 用途 |
| --- | --- |
| `help` | 显示支持的命令。 |
| `status` | 查看 systemd 服务状态。 |
| `start`、`stop`、`restart` | 启动、停止或重启服务。运行中的下载会随服务停止而中断，重启后由队列状态恢复。 |
| `logs` | 查看最近 100 条服务日志。 |
| `logs -f` | 持续查看服务日志；按 `Ctrl+C` 退出日志查看，不会停止服务。 |
| `health` | 检查运行中服务的健康接口并显示版本。 |
| `check-update` | 查询公开 GitHub 最新正式 Release，显示当前版本和最新版本；不会安装。 |
| `update` | 若有更新，则下载对应架构的程序并升级；已是最新版时不替换程序。 |

除 `logs -f` 外，命令不接受额外参数。`status`、`start`、`stop`、`restart` 调用 `systemctl --user`；`logs` 调用 `journalctl --user`。

## 在线升级

```bash
cd /home/lee/video-hub-go
./scripts/moviehub-service.sh check-update
./scripts/moviehub-service.sh update
./scripts/moviehub-service.sh health
./scripts/moviehub-service.sh status
./scripts/moviehub-service.sh logs
```

`check-update` 只查询 Release 元数据，旧版服务的健康接口没有版本字段时，当前版本可能显示 `unknown`。`update` 支持 Linux `amd64`、`arm64`：下载 GitHub Release 中的对应程序及 `SHA256SUMS`，校验 SHA256 后原子替换程序、重启服务，并在 30 秒内检查 `/healthz` 是否报告目标版本。成功时打印旧程序备份路径（`bin/moviehub.previous-时间戳`）。升级失败时脚本会尝试恢复旧程序并重启；若恢复或重启也失败，应查看错误输出和 `logs`，人工检查备份及服务状态。

升级器使用机器本身的网络环境访问 GitHub，没有内置代理配置。`check-update` 和 `update` 都要求当前服务健康且可访问；服务已经停止时，先用 `status`、`logs` 排查并启动服务。升级前建议备份 `data/` 和 `media/`，尤其是 JSON 状态文件。升级后除 `health` 外，还应按需验证下载及 Emby 扫描。

## 其他部署目录或服务名

| 环境变量 | 默认值 | 作用 |
| --- | --- | --- |
| `MOVIEHUB_SYSTEMD_SCOPE` | `user` | `user` 调用 `systemctl --user`；`system` 调用系统级 `systemctl`。 |
| `MOVIEHUB_SERVICE_NAME` | `video-hub-go` | systemd unit 名，不带 `.service`；升级命令使用小写字母、数字和连字符。 |
| `MOVIEHUB_BINARY` | 管理脚本上级目录的 `bin/moviehub` | `update` 要替换的程序文件；必须是普通文件。 |
| `MOVIEHUB_HEALTH_URL` | 从 unit 的 `ExecStart --listen` 推导 | 健康检查 URL；仅在 unit 没有 `--listen` 或需覆盖地址时设置，例如 `http://192.168.1.2:4444/healthz`。 |

环境变量只影响**当前命令**。例如管理其他用户服务：

```bash
MOVIEHUB_SERVICE_NAME=my-video-hub \
MOVIEHUB_BINARY=/home/lee/my-video-hub/bin/moviehub \
./scripts/moviehub-service.sh health
```

若按 [Linux 部署说明](linux-deployment.md) 把脚本安装到 `/usr/local/bin`、把程序装到 `/opt/moviehub/bin/moviehub`，则为系统服务显式指定范围与程序路径：

```bash
MOVIEHUB_SYSTEMD_SCOPE=system MOVIEHUB_BINARY=/opt/moviehub/bin/moviehub \
  /usr/local/bin/moviehub-service check-update
sudo env MOVIEHUB_SYSTEMD_SCOPE=system MOVIEHUB_BINARY=/opt/moviehub/bin/moviehub \
  /usr/local/bin/moviehub-service update
```

系统服务的 `update` 需要有权替换程序并重启该 unit；用户服务则以其属主执行。若 `health` 无法从 unit 解析监听地址，可以为单条命令设置 `MOVIEHUB_HEALTH_URL`。端口改为 `4444` 后，旧版 `4488` 地址不应再用于健康检查。

## 常见问题

| 现象 | 先检查 |
| --- | --- |
| `管理命令仅支持运行 systemd 的 Linux` | 是否在 Linux 服务器执行，而非 macOS 本机。 |
| `无法从服务配置读取监听地址` | unit 的 `ExecStart` 是否带 `--listen`；否则设置 `MOVIEHUB_HEALTH_URL`。 |
| `无法读取运行中服务的健康接口` | `status`、`logs`、unit 监听地址、服务器本机对 `/healthz` 的访问。 |
| `在线升级失败` | 错误输出、GitHub 连通性、Release 资产、程序目录写权限与 `logs`。 |
| `check-update` 显示 `unknown` | 旧程序可能没有在健康接口返回版本；升级成功后再用 `health` 核对。 |
