# Linux 与 systemd 部署

当前项目提供 Linux `amd64`、`arm64` 构建脚本、systemd unit 模板和管理脚本。模板生成脚本只写出待审阅的文件，不会修改 `/etc`、执行 `sudo` 或启动服务。公开 GitHub Release 提供带校验和的构建产物。

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
sudo install -m 0644 /tmp/video-hub-go.XXXXXXXX.service /etc/systemd/system/video-hub-go.service
sudo systemctl daemon-reload
sudo systemctl enable --now video-hub-go.service
systemctl status video-hub-go.service --no-pager
curl --fail http://127.0.0.1:4488/healthz
sudo install -m 0755 scripts/moviehub-service.sh /usr/local/bin/moviehub-service
sudo install -m 0644 scripts/moviehub-update.py /usr/local/bin/moviehub-update.py
```

把示例中的 `/tmp/moviehub.XXXXXXXX.service` 换成脚本打印的实际路径。局域网使用时将 `MOVIEHUB_LISTEN` 指向服务器内网地址，例如 `192.168.1.2:4488`。管理接口无需访问令牌，不要将端口映射到公网。

如果在管理台把媒体目录改到 unit `ReadWritePaths` 以外，还需同步更新 unit、执行 `daemon-reload` 并重启；否则 systemd 沙箱可能阻止写入。状态目录和媒体目录也需要足够磁盘空间与共享权限。

## 管理与日志

当前服务器的用户服务可以直接运行 `scripts/moviehub-service.sh status`、`logs`、`health`、`check-update`、`update`。上述 `/etc/systemd/system` 示例属于系统服务，使用 `/usr/local/bin/moviehub-service` 前需设置：

```bash
export MOVIEHUB_SYSTEMD_SCOPE=system
export MOVIEHUB_BINARY=/opt/moviehub/bin/moviehub
moviehub-service status
moviehub-service check-update
sudo --preserve-env=MOVIEHUB_SYSTEMD_SCOPE,MOVIEHUB_BINARY moviehub-service update
```

脚本自身不调用 `sudo`。应用把结构化关键事件写到标准错误输出，由 `journalctl -u video-hub-go.service` 收集；不另建包含站源地址或密钥的应用日志文件。HTTP 日志只记录路由模板、状态码和耗时，不包含搜索词、播放 URL、请求头、请求体或 API Key。

在线升级脚本 `scripts/moviehub-update.py` 须与管理脚本放在同一目录，且由能替换程序文件、重启对应 systemd 服务的用户执行。默认针对 `video-hub-go` 用户服务与脚本上级目录的 `bin/moviehub`，健康地址从 systemd unit 的 `--listen` 参数读取；其他部署设置 `MOVIEHUB_SYSTEMD_SCOPE=system`、`MOVIEHUB_SERVICE_NAME`、`MOVIEHUB_BINARY`、`MOVIEHUB_HEALTH_URL`。更新会保留带时间戳的旧程序备份；升级失败自动恢复并重启。更新后应检查服务状态和 Emby 扫描。

若服务不是通过 `--listen` 参数指定监听地址，执行 `health`、`check-update`、`update` 前设置 `MOVIEHUB_HEALTH_URL=http://实际监听地址/healthz`。

升级前备份整个状态目录和媒体目录，尤其是 `settings.json`、`sources.json`、`follows.json`、`jobs.json`、`emby.json`。`update` 会自动保留旧程序并重启；升级后还需检查 journal 日志。不要只凭 `systemctl active` 判断站源下载和 Emby 扫描可用。
