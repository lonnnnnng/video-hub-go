# Video Hub Go

**从资源站搜索、追更和下载，到整理媒体并同步 Emby。**

Video Hub Go 是面向个人与家庭局域网的剧集管理服务。它聚合 JSON 资源站，提供搜索、预览、自动追更和并发下载；下载完成后将视频封装为 MP4，整理剧集目录、NFO 与海报，并请求 Emby 扫描共享媒体库。

[最新版本](https://github.com/lonnnnnng/video-hub-go/releases/latest) · [Linux 部署](docs/linux-deployment.md) · [服务管理与升级](docs/service-management.md) · [发版流程](docs/release.md) · [问题反馈](https://github.com/lonnnnnng/video-hub-go/issues)

> 管理台没有登录或访问令牌。请只在可信局域网使用，不要将服务端口暴露到公网。程序、命令和部署脚本沿用 `moviehub` 命名。

## 能做什么

- **找剧与追更**：从 JSON 资源站导入剧集，搜索详情、查看分集，并按计划检查新集。
- **并发下载**：同时下载多集及其 HLS 分片；使用 FFmpeg 将媒体流复制封装为 MP4，并由 FFprobe 检查成品，不进行转码。
- **整理媒体库**：按剧名和季度归档 MP4，生成 NFO 和海报；失败的下载与同步任务可重试。
- **同步 Emby**：文件发布后通知 Emby 刷新媒体库，并核对 Emby 是否已按本地 `File` 媒体源识别该集。
- **持续运维**：JSON 文件保存设置和队列，支持 systemd 管理、日志查看、版本检查与校验后升级。
- **浏览器预览**：可预览资源站提供的 HLS；媒体分片由浏览器直接访问资源站，受站源跨域策略和客户端网络影响。

下载和 Emby 的文件路径关系如下：

```text
资源站 HLS -> Video Hub Go -> MP4 / NFO / 海报 -> Emby 扫描共享目录
```

Emby 直接读取媒体目录中的文件。Emby 服务器与 Video Hub Go 必须能访问同一批媒体文件，且 Emby 运行用户具有目录遍历和文件读取权限。

## 快速运行

### Linux Release

在 Linux `amd64` 或 `arm64` 主机上下载当前正式版。请在一个新的空目录中执行，不要在已有服务目录里下载覆盖程序。将 `version` 设置为 [Release 页面](https://github.com/lonnnnnng/video-hub-go/releases)中的版本标签：

```bash
mkdir moviehub-quickstart
cd moviehub-quickstart
set -euo pipefail

version=v0.4.3
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "不支持的 CPU 架构"; exit 1 ;;
esac

release_url="https://github.com/lonnnnnng/video-hub-go/releases/download/$version"
curl --fail --location "$release_url/moviehub-linux-$arch" -o moviehub
curl --fail --location "$release_url/SHA256SUMS" -o SHA256SUMS
awk -v asset="moviehub-linux-$arch" '$2 == asset {print $1 "  moviehub"}' SHA256SUMS | sha256sum -c -
chmod 0755 moviehub
mkdir -p data media
./moviehub --listen 127.0.0.1:4444 --data-dir ./data --media-dir ./media
```

运行用户需要读写 `data/` 和 `media/`。服务器需安装 FFmpeg 与 FFprobe。首次启动后访问 `http://127.0.0.1:4444/`；要从局域网其他设备访问，将 `--listen` 改为服务器的内网地址。

### 从源码运行

需要 Go 1.25+、FFmpeg 和 FFprobe。前端资源已嵌入 Go 程序，不需要单独构建 Node.js 项目。

```bash
mkdir -p bin
go build -trimpath -o ./bin/moviehub ./cmd/moviehub
./bin/moviehub --listen 127.0.0.1:4444 --data-dir ./data --media-dir ./media
```

## Emby 设置

1. 在 Emby 创建「电视节目」媒体库，并将其指向 Video Hub Go 的媒体目录。
2. 确认 Emby 服务用户可以遍历剧集与季度目录，并读取 MP4、NFO 和图片。
3. 在 Video Hub Go 的「设置」中填写 Emby 地址和 API Key，启用自动扫描。
4. 下载完成后，检查队列中的「Emby 已识别」状态，并在 Emby 中确认该集可播放。

自动扫描表示 Emby 接受了刷新请求；扫描完成时间由 Emby 决定。Video Hub Go 会继续检查目录和媒体源，识别失败时可从管理台再次请求扫描。

## 服务器目录

当前服务器采用平铺的运行目录。主程序、管理脚本与持久化目录处于同一级；部署目录不包含源码仓库中的文档、测试或构建产物。

```text
/home/lee/video-hub-go/
├── moviehub                 # 正在运行的 Linux 程序
├── moviehub-service.sh      # systemd 管理与升级入口
├── moviehub-update.py       # 校验并替换正式版程序
├── data/                    # 设置、资源站、追更、任务和 Emby 状态
└── media/                   # MP4、NFO、海报及下载临时文件
```

管理脚本与更新脚本必须放在一起；程序与两个脚本平级，供服务启动和在线更新使用。服务器上的管理命令示例：

```bash
cd /home/lee/video-hub-go
./moviehub-service.sh status
./moviehub-service.sh health
./moviehub-service.sh logs
./moviehub-service.sh check-update
./moviehub-service.sh update
```

上述是当前服务器的实际布局。新建 Linux systemd 部署的通用安装示例采用 `/opt/moviehub/bin/moviehub`，详见 [Linux 部署说明](docs/linux-deployment.md)；两种目录布局不要混用。服务配置、升级回滚和日志命令见[服务管理说明](docs/service-management.md)。

## 配置与数据

启动参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--listen` | `127.0.0.1:4444` | HTTP 监听地址；局域网访问时使用服务器内网地址。 |
| `--data-dir` | `./data` | 持久化设置和队列状态，建议使用固定路径。 |
| `--media-dir` | `./media` | 首次启动时的媒体目录；已有配置以保存的设置为准。 |

FFmpeg、FFprobe、下载并发、追更间隔和 Emby 连接参数可在管理台设置。JSON 状态文件位于数据目录；下载的正式媒体位于媒体目录。升级程序不会替代这两个目录，仍应按自己的备份策略保护它们。

## 开发与验证

```bash
go test ./...
go test -race ./...
go vet ./...
```

构建 Linux `amd64`、`arm64` 发布程序及校验和：

```bash
MOVIEHUB_VERSION=v0.4.3 ./scripts/build-linux.sh
```

更多兼容范围、故障排查和发布验收步骤见 [部署与运维文档](docs/linux-deployment.md)、[服务管理说明](docs/service-management.md) 和 [发版流程](docs/release.md)。
