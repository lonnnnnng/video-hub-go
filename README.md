# Movie Hub Go

Movie Hub Go 是个人使用的剧集管理服务：从 JSON 格式的 MacCMS 资源站搜索剧集，保存追更列表，在服务内并发下载 HLS 分片并整理媒体文件，最后请求 Emby 扫描媒体库。

## 当前功能

- 资源站清单来自 `https://raw.githubusercontent.com/lonnnnnng/resources/main/dist/video/lite.json`，支持启用/停用；桌面每行 3 个站源，使用图标显示检测结果、默认状态和操作，不再提供请求头设置。
- 站源页可一键检测接口连通性与 MacCMS 搜索响应；失败源自动停用，成功源按搜索响应延迟排序，最快的已启用源设为默认源。检测结果和默认源保存在 JSON 状态中。
- 首页展示默认源的影片列表与分类，支持翻页；切换默认源后首页重新加载，搜索框默认选中该源。
- 支持 MacCMS `videolist` 搜索、`detail` / `videolist` 详情，以及常见 `vod_play_from` / `vod_play_url` 分集格式。
- 首页、搜索结果和追更详情可预览 HLS 视频：点击集名播放、切换上一集/下一集，勾选分集批量下载。自动选择 HLS 线路，已有追更沿用原季号，新剧从标题识别季号（默认第 1 季）。
- 可按集入队、暂停、取消和重试；任务进度与失败状态保存在管理页面。可只删除任务记录并保留文件，或同时删除本集 MP4、分集 NFO、入库收据及任务临时分片；删除前停止正在进行的下载，删除文件后通知 Emby 扫描，保留其他集与共享剧集 NFO。删除失败保留任务记录供重试。
- 已完成任务可在弹框中用原生播放器通过 HTTP 播放本地 MP4，支持 Range 拖动进度；不转码，不重新读取站源。只有已完成任务可通过任务 ID 访问对应文件，已删除的记录不再提供播放入口。
- 可按资源站分集身份追更，定时检查新集，并可自动入队和回补已有分集。
- 多集由服务并发调度，每集的 HLS 分片由 Go 并发下载；FFmpeg 把本地片单封装为 MP4，FFprobe 检查视频轨道和时长。
- 媒体按 `剧名 [来源身份]/Season 01/剧名 S01E01 [分集身份].mp4` 归档，生成 Emby/Kodi 可读的 NFO。
- MP4 与剧集/分集 NFO 完整落盘、下载任务标记完成后，向 Emby 发送媒体库刷新请求；失败保留待办，每分钟重试。首次启用或更换 Emby 服务器/密钥也会扫描现有媒体。
- 服务状态只使用分类 JSON 文件；写入通过同目录临时文件、`fsync`、原子 rename 和目录同步完成。跨文件更新通过临时事务记录在重启时补齐。数据目录用文件锁阻止多个服务实例同时写入，不使用 SQLite。

## 运行

需要 Go 1.25 或更新版本，以及 FFmpeg、FFprobe。构建脚本只构建当前 Go 服务。

```zsh
./scripts/build.sh
./scripts/run.sh --listen 127.0.0.1:4444 --data-dir ./data --media-dir ./media
```

Linux 部署可用 [`scripts/build-linux.sh`](scripts/build-linux.sh) 构建 `amd64`/`arm64` 程序，使用 [`scripts/render-systemd.sh`](scripts/render-systemd.sh) 生成服务文件，再用 [`scripts/moviehub-service.sh`](scripts/moviehub-service.sh) 管理状态、日志和健康检查。具体步骤见 [Linux 部署说明](docs/linux-deployment.md)，命令与在线升级用法见 [服务管理脚本说明](docs/service-management.md)。应用关键事件输出到标准错误流；在 systemd 下通过 journal 查看，不记录令牌、播放 URL 或资源站请求头。

打开 `http://127.0.0.1:4444`。首次启动会导入资源站清单。首次使用时在“设置”检查媒体目录、FFmpeg 和 FFprobe 路径；如需 Emby 同步，让 Emby 的“电视节目”媒体库读取同一媒体目录（跨容器时挂载同一宿主目录），再配置 Emby 服务器 URL 和管理员 API Key 后启用。服务会请求 `POST /Library/Refresh`，以 `X-Emby-Token` 传递密钥；扫描完成时间由 Emby 决定，页面中的时间表示刷新请求已被接受。

管理台按菜单使用独立页面：首页 `/`、追更 `/follows.html`、下载 `/jobs.html`、资源站 `/sources.html`、设置 `/settings.html`。各页共用 `/app.js` 和 `/style.css`，在手机宽度下会调整导航和内容布局。

默认只监听本机。局域网使用时显式设置 `--listen 192.168.1.2:4444` 等服务器内网地址。管理 API 不再要求访问令牌，因此只应绑定可信局域网接口，不要映射到公网。

Linux 用户服务可用 `scripts/moviehub-service.sh check-update` 检查公开 GitHub Release，用 `scripts/moviehub-service.sh update` 在线下载、SHA256 校验并升级。升级脚本原子替换程序、重启服务并核对 `/healthz` 的版本；启动失败会恢复旧程序。

## 数据和媒体

- `data/settings.json` 保存服务设置。
- `data/sources.json` 保存资源站及清单更新时间。
- `data/follows.json` 保存追更记录。
- `data/jobs.json` 保存下载队列与任务状态。
- `data/emby.json` 保存 Emby 同步状态。
- 更新过的分类文件会留下同名 `.bak` 作为该项最近一次更新前的快照；损坏或缺失分类文件时服务会停止启动，不会自动覆盖原文件。
- 旧版 `data/state.json` 会在首次启动时迁移到上述文件，并原样保留。跨文件提交期间的 `data/transaction.json` 会在完成或重启恢复后移除。
- 下载过程文件写入媒体目录下的 `.moviehub/`，成功媒体和 NFO 放在剧集目录中。
- 媒体文件名含稳定身份后缀，避免不同来源或同名剧集意外覆盖。

## 首期边界

资源站 JSON 清单虽然可导入，但清单中的 API 结构、站点可用性和鉴权要求各不相同。当前搜索适配 MacCMS JSON API；不兼容的站点会单独显示请求错误，不能保证清单内每个站点都可搜索或播放。

详情播放器使用内置的 HLS.js 1.7.3（Apache 2.0，版权文件随程序提供），不依赖外部 CDN；不支持 HLS.js 时尝试浏览器原生 HLS。播放器直接使用详情中的 `episode.url`，片单、分片和密钥均由浏览器直连资源站获取，不经过 Go 服务，也没有服务端转发兜底。切集或关闭详情时取消浏览器播放请求。浏览器直连受到站源跨域策略、请求头限制和客户端网络影响，失败时可重试或更换站源；站源为服务端配置的请求头不注入浏览器。

预览成功能帮助判断片源是否可用，但不能保证完整下载成功：后续分片仍可能失效，浏览器与下载器支持的编码和 HLS 特性也有差异。本服务不处理 DRM，也不抓取站点 HTML。下载结果发布时采用不覆盖策略。本地 HLS 样本已验证普通 TS、AES-128、fMP4 字节范围及多链接并发下载；具体站点和家庭网络 Emby 连通性仍需按实际配置验收。

内嵌下载器支持结束的 HLS VOD 媒体片单、单视频轨主片单、相对分片地址、普通 TS/fMP4、AES-128 和字节范围分片。独立音轨、直播、低延迟 HLS、多初始化文件及其他加密方式会明确报错。网络连接沿机器环境，服务不设置代理地址。
