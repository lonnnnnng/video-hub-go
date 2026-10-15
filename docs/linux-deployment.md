# Linux 与 systemd 部署

当前项目提供 Linux `amd64`、`arm64` 构建脚本、systemd unit 模板和管理脚本。模板生成脚本只写出待审阅的文件，不会修改 `/etc`、执行 `sudo` 或启动服务。仓库尚未发布带校验和的 Release，因此这里使用本地构建产物部署。

## 构建与准备

在有 Go 1.25+ 的构建机执行：

```bash
./scripts/build-linux.sh
(cd dist && shasum -a 256 -c SHA256SUMS)
```

Linux 上也可用 `sha256sum -c SHA256SUMS`。把对应架构的 `moviehub-linux-amd64` 或 `moviehub-linux-arm64` 复制为 `/opt/moviehub/bin/moviehub`，保留可执行权限。以下是 Ubuntu/Debian 上首次建立专用用户和目录的示例；已有用户或目录应先检查再调整，不要覆盖已有媒体：

```bash
sudo useradd --system --home /opt/moviehub --shell /usr/sbin/nologin moviehub
sudo install -d -m 0755 /opt/moviehub/bin
sudo install -d -o moviehub -g moviehub -m 0700 /var/lib/moviehub
sudo install -d -o moviehub -g moviehub -m 2750 /srv/moviehub/media
sudo install -m 0755 dist/moviehub-linux-amd64 /opt/moviehub/bin/moviehub
```

ARM64 主机将最后一行的源文件换成 `moviehub-linux-arm64`。运行机需要 FFmpeg 和 FFprobe；若不在服务默认 `PATH` 中，在管理台设置它们的绝对路径。

示例部署目录：程序 `/opt/moviehub`，状态 `/var/lib/moviehub`，媒体 `/srv/moviehub/media`。请先建立非 root 服务用户，并让该用户能读写状态和媒体目录。unit 的 `UMask=0027` 会使新建媒体目录默认只向属主和属组开放；将 Emby 进程用户加入媒体目录的共享组，或为其配置等效 ACL，确保它能遍历剧集及 Season 目录。已有媒体目录的所有权和 ACL 应按实际共享方式检查，不要递归改写。Emby “电视节目”媒体库必须能读取同一媒体目录；容器部署时要挂载同一宿主目录。

## 生成并安装 unit

在 Linux 上从本仓库目录执行；变量按真实用户和目录修改：

```bash
MOVIEHUB_USER=moviehub \
MOVIEHUB_INSTALL_DIR=/opt/moviehub \
MOVIEHUB_DATA_DIR=/var/lib/moviehub \
MOVIEHUB_MEDIA_DIR=/srv/moviehub/media \
MOVIEHUB_LISTEN=127.0.0.1:4488 \
./scripts/render-systemd.sh
```

脚本会打印生成的临时 `.service` 路径。核对 `User`、`ExecStart`、`EnvironmentFile`、`ReadWritePaths` 后，由管理员安装和启用：

```bash
sudo install -m 0644 /tmp/moviehub.XXXXXXXX.service /etc/systemd/system/moviehub.service
sudo systemctl daemon-reload
sudo systemctl enable --now moviehub.service
systemctl status moviehub.service --no-pager
curl --fail http://127.0.0.1:4488/healthz
sudo install -m 0755 scripts/moviehub-service.sh /usr/local/bin/moviehub-service
```

把示例中的 `/tmp/moviehub.XXXXXXXX.service` 换成脚本打印的实际路径。unit 的可选环境文件为 `/opt/moviehub/.env`；需要局域网监听时在该文件设置 `MOVIEHUB_TOKEN`（至少 24 字符），将文件限制为仅管理员可读，并在 unit 中使用 `0.0.0.0:4488` 等实际监听地址。服务启动时会校验非回环监听的令牌。令牌不要写入 unit、日志或命令行。

如果在管理台把媒体目录改到 unit `ReadWritePaths` 以外，还需同步更新 unit、执行 `daemon-reload` 并重启；否则 systemd 沙箱可能阻止写入。状态目录和媒体目录也需要足够磁盘空间与共享权限。

## 管理与日志

```bash
moviehub-service status
moviehub-service logs
moviehub-service logs -f
moviehub-service health
sudo moviehub-service restart
```

脚本自身不调用 `sudo`。应用把结构化关键事件写到标准错误输出，由 `journalctl -u moviehub.service` 收集；不另建包含站源地址或密钥的应用日志文件。HTTP 日志只记录路由模板、状态码和耗时，不包含搜索词、播放 URL、请求头、请求体或 API Key。

若监听端口不是 4488，执行 `health` 前设置 `MOVIEHUB_HEALTH_URL=http://127.0.0.1:实际端口/healthz`；管理脚本只接受本机地址。

升级前备份整个状态目录和媒体目录，尤其是 `settings.json`、`sources.json`、`follows.json`、`jobs.json`、`emby.json`。更新二进制时先停止服务，保留旧程序以便回退，再替换、启动并检查 `/healthz` 与 journal 日志。不要只凭 `systemctl active` 判断站源下载和 Emby 扫描可用。
