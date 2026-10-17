# Video Hub Go

**从找剧、追更到下载入库，一处管理你的家庭剧集库。**

Video Hub Go 是面向个人与家庭局域网的剧集管理服务。聚合 JSON 资源站，提供搜索、在线播放预览、自动追更和并发下载，将视频整理为 MP4 与 NFO，并通知 Emby 扫描入库。

Go 服务内置网页管理台与 HLS 下载器，部署时无需单独运行下载器；媒体合并和校验使用 FFmpeg、FFprobe。配置与任务采用 JSON 文件持久化，无需数据库。

[下载最新版](https://github.com/lonnnnnng/video-hub-go/releases/latest) · [Linux 部署](docs/linux-deployment.md) · [服务管理与升级](docs/service-management.md) · [反馈问题](https://github.com/lonnnnnng/video-hub-go/issues)

> 项目名称为 Video Hub Go，程序文件及现有脚本沿用 `moviehub` 命名。默认端口为 **4444**。管理台无需登录或访问令牌，请部署在可信局域网，避免将端口开放到公网。

## 目录

- [功能概览](#功能概览)
- [快速开始](#快速开始)
- [首次使用](#首次使用)
- [接入 Emby](#接入-emby)
- [配置说明](#配置说明)
- [服务管理与在线升级](#服务管理与在线升级)
- [数据、媒体与备份](#数据媒体与备份)
- [常见问题](#常见问题)
- [兼容范围](#兼容范围)
- [开发与构建](#开发与构建)

## 功能概览

| 能力 | 使用体验 |
| --- | --- |
| 资源站管理 | 自动导入站源清单，一键检测连通性与搜索能力，失败自动禁用、按延迟排序；也可手动选择已启用的默认站源。 |
| 找剧与搜索 | 首页展示默认站源的分类和片单，支持翻页；可选择站源搜索，查看剧集详情与分集。 |
| 在线预览 | 详情弹框内播放 HLS，支持上一集、下一集；浏览器直接访问站源媒体地址。 |
| 自动追更 | 定时检查新集，可开启自动下载，也可在添加追更时回补已有分集。 |
| 并发下载 | 同时下载多集，每集并发获取 HLS 分片，经 FFmpeg 封装为 MP4，再由 FFprobe 校验。 |
| 队列管理 | 桌面三列紧凑队列与固定筛选栏，按剧名、分集和状态筛选；下载中显示速度，完成后显示文件大小与 MP4 合成时间。支持暂停、继续、取消、重试和删除任务或文件。 |
| 本地播放 | 已完成任务通过 HTTP 播放本地 MP4，支持 Range 请求和拖动进度，无需转码。 |
| Emby 入库 | 自动整理剧集与季度目录、生成 NFO，文件发布完成后通知 Emby 扫描，失败保留待办重试。 |
| 轻量运维 | 分类 JSON 存储、原子写入、关键事件日志、systemd 管理；导航底部显示版本与发行时间，可检测并安装新版。 |

## 快速开始

### 环境要求

| 使用方式 | 所需环境 |
| --- | --- |
| Linux Release | Linux `amd64` 或 `arm64`，FFmpeg、FFprobe；无需安装 Go。 |
| 从源码运行 | Go **1.25+**，FFmpeg、FFprobe；可在 macOS、Linux 使用。 |
| 后台服务与在线升级 | Linux、systemd，以及访问 GitHub Release 的网络；管理脚本另需 Bash，脚本升级另需 Python 3。 |
| 网页管理台 | 现代浏览器；在线预览还需要浏览器能够访问资源站的媒体域名。 |

运行服务的用户需要读写数据目录与媒体目录。首次使用前，可通过 `ffmpeg -version` 和 `ffprobe -version` 确认工具可用。

### 方式一：使用 Linux Release

以下命令在 **Linux 服务器**执行，示例安装 v0.4.2。安装其他版本时，将 `version` 改为 [Release 页面](https://github.com/lonnnnnng/video-hub-go/releases)中对应的标签。需要 Git、curl 和 `sha256sum`；首次安装使用尚不存在的目标目录。

```bash
version=v0.4.2
git clone --depth 1 --branch "$version" https://github.com/lonnnnnng/video-hub-go.git
cd video-hub-go

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "不支持的 CPU 架构"; exit 1 ;;
esac

mkdir -p bin
release_url="https://github.com/lonnnnnng/video-hub-go/releases/download/$version"
curl --fail --location "$release_url/moviehub-linux-$arch" -o "bin/moviehub-linux-$arch"
curl --fail --location "$release_url/SHA256SUMS" -o bin/SHA256SUMS

# long: 校验当前架构的程序后再安装，避免运行下载不完整或内容不匹配的文件。
(
  cd bin
  awk -v asset="moviehub-linux-$arch" '$2 == asset' SHA256SUMS | sha256sum -c -
) && install -m 0755 "bin/moviehub-linux-$arch" bin/moviehub
```

确认校验输出 `OK` 且安装命令成功后，先在前台启动：

```bash
./bin/moviehub --listen 127.0.0.1:4444 --data-dir ./data --media-dir ./media
```

本机访问 `http://127.0.0.1:4444/`。如需在局域网其他设备上访问，将监听地址换成服务器的内网 IP，例如：

```bash
./bin/moviehub --listen 192.168.1.2:4444 --data-dir ./data --media-dir ./media
```

此时浏览器打开 `http://192.168.1.2:4444/`。示例 IP 请替换为自己的服务器地址。

长期运行请按 [Linux 部署说明](docs/linux-deployment.md)安装 systemd 服务。仓库提供 unit 模板与生成脚本；前台运行不会自动配置开机启动。

### 方式二：从源码运行

```bash
git clone https://github.com/lonnnnnng/video-hub-go.git
cd video-hub-go
go build -trimpath -o ./bin/moviehub ./cmd/moviehub
./bin/moviehub --listen 127.0.0.1:4444 --data-dir ./data --media-dir ./media
```

macOS 开发机也可使用 `./scripts/build.sh` 和 `./scripts/run.sh`，两者依赖 zsh。Linux 无需安装 zsh，直接使用上述 Go 命令或 Release 程序即可。

## 首次使用

### 1. 检查设置

进入「设置」，确认媒体目录与 FFmpeg、FFprobe 路径。工具在服务的 `PATH` 中时，可保留 `ffmpeg`、`ffprobe`；否则填写绝对路径。

下载并发默认是 **2 个任务，每个任务 8 个分片线程**。可按带宽、磁盘性能和站源限制调整。

### 2. 检测资源站

首次启动会尝试导入 [站源清单](https://raw.githubusercontent.com/lonnnnnng/resources/main/dist/video/lite.json)。进入「资源站」点击一键检测，检查接口连通性与搜索能力；检测失败的站源会被自动禁用，延迟最低的可用已启用站源成为默认源。也可手动调整启用状态。

首页由默认站源提供分类与片单。站源清单可下载，不代表每个站点当前都能搜索或提供可下载片源；检测后以页面结果为准。

### 3. 搜索、预览与下载

在首页搜索剧名，打开详情后：

- **点击集名**：在弹框中预览；使用上一集、下一集切换。
- **勾选分集**：选择要下载的内容，再加入下载队列。
- **添加追更**：保存剧集，后续定期检查更新。

系统自动选择 HLS 线路。已有追更沿用原季号，新剧尝试从标题识别季号，无法识别时使用第 1 季。

在线预览时，片单、分片和密钥由浏览器直接向资源站获取，媒体流量不经过 Video Hub Go。播放受站源跨域策略及客户端网络影响；预览成功也不能保证后续所有分片均能下载。

### 4. 开启自动追更

首次添加追更默认记录当前已有分集，后续检查新集。希望下载已有集时，勾选回补；希望新集自动进入队列时，开启自动下载。

在「追更」页面可立即检查、暂停或恢复追更、调整自动下载开关。新集以资源站的分集身份识别，站源修改集名或线路可能影响识别结果。

### 5. 管理下载结果

在「下载」页面查看任务进度与失败原因。失败任务会在尝试次数范围内退避重试，达到上限后可手动重试。服务重启后，原本正在下载的任务会重新排队；暂停、取消和失败任务保留原状态。

下载前与分片写入时检查目标磁盘，默认预留 **2 GiB** 安全余量；封装 MP4 前再按分片总大小预留成品空间，FFmpeg 运行期间继续检查。空间不足显示“等待磁盘空间”，不占下载名额、不消耗尝试次数，每 30 秒重新检查。空间恢复后重新解析并下载本次分集；本轮未发布的分片会先安全回收，避免重复重试堆积。

正式 MP4、NFO 和入库收据完整发布且视频哈希匹配后，后台自动回收该任务的临时分片，也会处理旧版本遗留的已完成任务。下载页的“清理临时分片”可立即安排后台检查；视频、NFO、海报和任务记录保留。校验异常时保留临时文件，并在任务中显示原因。

已完成任务可点击播放，通过服务提供的本地 MP4 地址观看。删除时有两种选择：

| 操作 | 结果 |
| --- | --- |
| 仅删除任务 | 移除队列记录，保留媒体与临时文件；该任务的网页播放入口随之移除。 |
| 删除任务及文件 | 先停止下载并等待退出，再清理本集 MP4、分集 NFO、入库收据及该任务的临时分片；保留其他集、共享的 `tvshow.nfo` 和海报。 |

文件删除失败时保留任务及错误，供排查后重试。删除已发布的媒体文件会安排 Emby 刷新。

## 接入 Emby

Emby 是可选集成。未配置 Emby 时，仍可使用搜索、追更、下载与网页播放。

1. 在 Emby 中创建或配置「电视节目」媒体库，指向 Video Hub Go 的媒体目录。
2. 确保 Emby 运行用户能够读取媒体文件、遍历剧集及季度目录。若两者处于不同容器，为它们挂载同一宿主媒体目录；容器内路径可以不同。
3. 在 Video Hub Go「设置」中填写 **Emby 服务器地址**和管理员 **API Key**，开启自动扫描。例如 `http://192.168.1.2:8096`。
4. 保存后可点击「请求扫描媒体库」，并在 Emby 中确认入库结果。

下载完成后，服务将 MP4 和 NFO 发布到媒体目录，保存完成状态，再请求 Emby 刷新媒体库。扫描请求失败时保留待办，每分钟重试；首次启用或更换 Emby 地址、密钥时也会安排扫描。

服务通过 `POST /Library/Refresh` 通知 Emby 读取共享媒体目录。Emby 服务器地址填写 Emby 自身的地址和端口；设置页中的扫描时间表示 Emby 已接受刷新请求，实际扫描完成时间由 Emby 决定。

队列分别显示“已下载”和“Emby 已识别”。后台查询 Emby 分集，只有路径匹配且媒体源协议为 `File` 的 MP4 才确认入库；HTTP STRM、重复条目及不完整查询会提示异常。扫描期间自动等待，超过 10 分钟仍未发现文件会提示检查媒体库和权限，可点击“请求扫描媒体库”补扫。已识别条目每 5 分钟复查；此状态表示 Emby 已识别本地文件，实际播放权限和编码兼容性仍由 Emby 决定。

**海报自动入库：** 下载任务会保留资源站提供的海报地址。后台按剧集补齐剧目录和季目录中的 `poster.jpg` 或 `poster.png`，同剧多集共用一次图片下载；文件完整落盘后再安排 Emby 刷新。旧版本的已完成任务会自动从原站源详情补查海报，无需重新下载视频。

图片失败不会改变视频完成状态，每五分钟重试；已有本地海报（包括 `folder.jpg`、`seasonXX-poster.jpg` 等受支持命名）会保留。支持 JPEG/PNG 图片，最多 8 MiB、1600 万像素，网络跟随机器设置。原站源失效或未提供受支持图片时无法自动补图，可自行放置本地海报。Emby 的媒体库入口拼图由 Emby 根据剧集主海报生成，生成结果与时间由 Emby 决定。

## 配置说明

### 启动参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--listen` | `127.0.0.1:4444` | HTTP 监听地址；局域网使用时指定服务器内网 IP。 |
| `--data-dir` | `./data` | JSON 状态目录，建议长期使用固定的绝对路径。 |
| `--media-dir` | `./media` | **首次初始化**使用的媒体目录；已有数据以保存的设置为准。 |

### 网页设置

| 配置项 | 默认值 | 可用范围或说明 |
| --- | --- | --- |
| 媒体目录 | 首次启动参数对应的绝对路径 | 修改后用于新建任务；已有任务保留自身的媒体目录。 |
| FFmpeg / FFprobe | `ffmpeg` / `ffprobe` | 可填写服务用户可执行的绝对路径。 |
| 同时下载任务数 | `2` | `1–6`。 |
| 每集分片线程数 | `8` | `1–32`。 |
| 最大尝试次数 | `3` | `1–10`，包含首次下载尝试。 |
| 追更检查间隔 | `30` 分钟 | `1–1440` 分钟。 |
| Emby 自动扫描 | 关闭 | 启用时须填写服务器地址与 API Key。 |

站源清单地址由服务固定使用上述 GitHub JSON 清单；管理台不提供自定义清单地址或请求头配置。程序没有代理设置，联网沿用所在机器与进程的网络环境。

若 systemd unit 限制了可写目录，在网页中更换媒体目录后，还需同步检查 `ReadWritePaths` 与目录权限，重新加载 unit 并重启服务。具体见 [Linux 部署说明](docs/linux-deployment.md)。

## 服务管理与在线升级

管理台导航底部显示当前版本与发行时间（北京时间）。点击 **检测更新** 查询 GitHub 最新正式 Release；发现新版后点击 **更新** 并确认。更新期间页面显示进度，服务重启后自动重新连接并刷新页面。开发版本显示“尚未发行”，不执行正式版升级。

网页更新支持 Linux `amd64`、`arm64` 的 systemd 用户服务，默认服务名为 `video-hub-go`，程序目录须可写，unit 启动参数须包含 `--listen`。不满足条件时会显示原因；系统服务由非 root 用户运行的部署请使用下方管理脚本升级。自定义服务名等配置见 [服务管理说明](docs/service-management.md#网页更新)。

以下命令适用于默认名为 `video-hub-go` 的 **systemd 用户服务**，以服务所属用户在安装目录执行：

```bash
./scripts/moviehub-service.sh status        # 查看状态
./scripts/moviehub-service.sh health        # 检查健康与运行版本
./scripts/moviehub-service.sh logs          # 最近 100 条日志
./scripts/moviehub-service.sh logs -f       # 持续查看日志
./scripts/moviehub-service.sh restart       # 重启服务
./scripts/moviehub-service.sh check-update  # 检查最新正式版
./scripts/moviehub-service.sh update        # 下载、校验并升级
```

还支持 `start`、`stop`、`help`。系统服务、自定义服务名或目录需要显式设置管理脚本环境变量，详见 [服务管理脚本说明](docs/service-management.md)。

在线升级下载对应架构的 GitHub Release 程序与 `SHA256SUMS`，校验后原子替换二进制、重启服务，并在 30 秒内核对健康接口的目标版本。旧程序保留为 `bin/moviehub.previous-时间戳`；升级失败时尝试恢复旧程序并重启，若回滚也失败，需根据错误和日志人工处理。

升级前请备份数据与媒体。升级器要求当前服务健康、GitHub 可访问，且执行用户有权替换程序并重启服务；它不会自动备份整个数据和媒体目录。

健康检查入口为 `GET /healthz`，例如：

```json
{"status":"ok","version":"v0.4.2"}
```

服务关键事件输出到标准错误流，systemd 部署通过 journal 查看，包括站源检测、追更、下载、合并、入库和 Emby 通知。HTTP 日志不记录搜索词、播放 URL、请求头、请求体或 API Key。

## 数据、媒体与备份

### 状态文件

| 文件 | 保存内容 |
| --- | --- |
| `data/settings.json` | 下载、媒体目录和 Emby 等设置。 |
| `data/sources.json` | 资源站、检测结果与清单更新时间。 |
| `data/follows.json` | 追更记录与已知分集。 |
| `data/jobs.json` | 下载队列、状态和输出位置。 |
| `data/emby.json` | Emby 待扫描与通知状态。 |
| `data/web-update.json` | 网页更新版本、进度与结果记录，首次执行更新时生成；旁边的 `.lock` 防止重复执行更新。 |

状态以同目录临时文件、`fsync`、原子重命名及目录同步提交，跨文件更新通过事务记录恢复；文件锁阻止多个实例同时写入一个数据目录。

更新过的分类文件保留同名 `.bak`，作为该文件最近一次更新前的快照。分类文件损坏或不完整时，服务停止启动并保留原文件，不会用空数据覆盖。旧版 `state.json` 首次加载时迁移，原文件保留；`transaction.json` 是临时恢复记录，请勿在运行时手动删除。

### 媒体目录

```text
media/
├── .moviehub/                        # 下载与合并临时文件
└── 剧名 [来源身份]/
    ├── tvshow.nfo                    # 剧集元数据
    ├── poster.jpg                   # 剧集海报，PNG 来源保存为 poster.png
    └── Season 01/
        ├── poster.jpg               # 季海报，与同剧分集共用
        ├── 剧名 S01E01 [分集身份].mp4
        ├── 剧名 S01E01 [分集身份].nfo
        └── 剧名 S01E01 [分集身份].mp4.moviehub.json
```

来源与分集身份用于区分同名内容，发布 MP4 时避免覆盖已有文件。`.moviehub.json` 是识别本程序产物的入库收据，删除文件等操作会使用它，请与媒体文件一同保留。

### 备份建议

- 备份整个状态目录和媒体目录，保留 NFO、收据与必要的临时文件。
- 获取一致快照前先停止服务，或使用文件系统的一致性快照；完成后再启动。
- `.bak` 仅是单文件的近期快照，不能替代完整备份。设置文件包含 Emby API Key，应限制备份的读取权限。
- 迁移服务器时同时检查已保存的媒体绝对路径、任务输出路径、工具路径以及 Emby 的挂载和权限；仅复制文件不会自动重写这些路径。

## 常见问题

| 现象 | 排查方向 |
| --- | --- |
| 其他设备打不开管理台 | 默认只监听 `127.0.0.1`。检查 `--listen` 是否为服务器内网 IP、端口是否为 `4444`，以及主机防火墙。 |
| 清单能导入，站点却不能搜索 | 清单下载与站点接口是不同请求。运行站源检测，查看接口可用性、兼容性及服务器网络。 |
| 有搜索结果，却没有可播放分集 | 站源可能没有返回兼容的 HLS 线路，或详情接口暂时失效；可尝试其他站源。 |
| 可以下载，但浏览器播放失败 | 浏览器直连受 CORS、媒体编码、HTTPS 页面混合内容限制和客户端网络影响；下载器使用的是服务器网络。 |
| 预览成功，下载仍失败 | 后续分片、密钥或签名地址可能失效，也可能遇到下载器不支持的 HLS 特性；查看任务错误和服务日志。 |
| FFmpeg / FFprobe 显示不可用 | 检查安装情况、服务用户的 `PATH` 与执行权限；必要时在设置中填写绝对路径。 |
| 文件已完成，Emby 没有入库 | 检查 Emby 是否读取同一媒体目录、目录遍历权限、URL/API Key 与扫描待办；刷新请求被接受不代表扫描已经完成。 |
| 本地 MP4 在浏览器中没有画面或声音 | 封装为 MP4 不会改变原始编码。浏览器需支持文件内的视频与音频编码，可尝试兼容的播放器或 Emby。 |
| 数据目录被另一个进程占用 | 检查是否重复启动了相同数据目录的实例；不要通过删除状态文件解决。 |
| 在线升级失败 | 检查当前服务健康、GitHub 连通性、Python 3、程序目录权限和 systemd 服务范围，按日志确认是否完成回滚。 |

## 兼容范围

| 项目 | 当前范围 |
| --- | --- |
| 资源站 | JSON 格式的 MacCMS API，支持 `videolist` 搜索、详情及常见 `vod_play_from` / `vod_play_url` 分集结构；不解析站点 HTML。 |
| 下载 | 已结束的 HLS VOD、单视频轨主片单、相对地址、普通 TS/fMP4、AES-128 及受支持的字节范围分片。 |
| 暂不支持 | 直播、低延迟 HLS、独立音轨、多初始化文件、DRM 及 AES-128 之外的加密方式；部分特性组合也会明确报错。 |
| 媒体处理 | FFmpeg 使用流复制封装为 MP4，FFprobe 校验视频轨道和时长；不转码。 |
| 在线播放 | 内置 HLS.js，支持时使用浏览器原生 HLS；无服务端媒体转发兜底。 |
| 本地播放 | 服务通过 HTTP 提供已完成任务对应的 MP4，支持 Range/HEAD；编码兼容性取决于浏览器。 |

站源接口与媒体链接可能随时变化，导入、搜索、预览和完整下载是不同环节，分别以实际结果为准。

## 开发与构建

前端 HTML、CSS、JavaScript 随 Go 程序嵌入，无需 Node 构建步骤。主要目录：

```text
cmd/moviehub/           # 程序入口
internal/hub/          # 站源、追更、下载、存储、HTTP 与 Emby 集成
internal/hub/web/      # 按菜单拆分的管理台页面与静态资源
scripts/               # 构建、服务管理与在线升级脚本
deploy/systemd/        # systemd unit 模板
docs/                  # 部署、运维与界面说明
```

常用验证命令：

```bash
go test ./...
go test -race ./...
go vet ./...
```

生成 Linux 双架构发布程序，并注入版本号：

```bash
MOVIEHUB_VERSION=v0.4.2 ./scripts/build-linux.sh
```

产物位于 `dist/`：`moviehub-linux-amd64`、`moviehub-linux-arm64`、`SHA256SUMS`。普通源码构建的健康版本默认为 `dev`。

发行时间默认读取对应 GitHub Release 的 `published_at`。若构建时已知真实发行时间，可通过 `MOVIEHUB_RELEASE_TIME` 注入 RFC 3339 时间；构建时间不会作为发行时间展示。

### GitHub Actions 发版流水线

[Release 流水线](.github/workflows/release.yml) 仅在推送 `v主版本.次版本.修订版本` 标签时执行。**普通代码推送不触发流水线发版；只有明确要求发版时，才在推送代码后创建并推送版本标签。**

流水线先运行 Go 竞态测试、`go vet` 与 JavaScript 语法检查，再调用已有脚本构建 Linux 双架构程序及 `SHA256SUMS`。资产上传到草稿 Release 后重新下载校验，全部通过才公开。使用仓库内置 `GITHUB_TOKEN`，无需额外配置发布令牌。

发版步骤、失败重跑和部署验证见 [发版说明](docs/release.md)。

内置 HLS.js 1.7.3，采用 Apache 2.0 许可证；上游版权与许可文本见 [hls.LICENSE.txt](internal/hub/web/hls.LICENSE.txt)。
