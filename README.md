# Movie Hub Go

Movie Hub Go 是个人使用的剧集管理服务：从 JSON 格式的 MacCMS 资源站搜索剧集，保存追更列表，在服务内并发下载 HLS 分片并整理媒体文件，最后请求 Emby 扫描媒体库。

## 当前功能

- 资源站清单来自 `https://raw.githubusercontent.com/lonnnnnng/resources/main/dist/video/lite.json`，支持启用/停用和请求头配置。
- 站源页可一键检测接口连通性与 MacCMS 搜索响应；失败源自动停用，成功源按搜索响应延迟排序，最快的已启用源设为默认源。检测结果和默认源保存在 JSON 状态中。
- 首页展示默认源的影片列表与分类，支持翻页；切换默认源后首页重新加载，搜索框默认选中该源。
- 支持 MacCMS `videolist` 搜索、`detail` / `videolist` 详情，以及常见 `vod_play_from` / `vod_play_url` 分集格式。
- 可按集入队、暂停、取消和重试；任务进度与失败状态保存在管理页面。
- 可按资源站分集身份追更，定时检查新集，并可自动入队和回补已有分集。
- 多集由服务并发调度，每集的 HLS 分片由 Go 并发下载；FFmpeg 把本地片单封装为 MP4，FFprobe 检查视频轨道和时长。
- 媒体按 `剧名 [来源身份]/Season 01/剧名 S01E01 [分集身份].mp4` 归档，生成 Emby/Kodi 可读的 NFO。
- MP4 与剧集/分集 NFO 完整落盘、下载任务标记完成后，向 Emby 发送媒体库刷新请求；失败保留待办，每分钟重试。首次启用或更换 Emby 服务器/密钥也会扫描现有媒体。
- 服务状态只使用分类 JSON 文件；写入通过同目录临时文件、`fsync`、原子 rename 和目录同步完成。跨文件更新通过临时事务记录在重启时补齐。数据目录用文件锁阻止多个服务实例同时写入，不使用 SQLite。

## 运行

需要 Go 1.25 或更新版本，以及 FFmpeg、FFprobe。构建脚本只构建当前 Go 服务。

```zsh
./scripts/build.sh
./scripts/run.sh --listen 127.0.0.1:4488 --data-dir ./data --media-dir ./media
```

Linux 部署可用 [`scripts/build-linux.sh`](scripts/build-linux.sh) 构建 `amd64`/`arm64` 程序，使用 [`scripts/render-systemd.sh`](scripts/render-systemd.sh) 生成服务文件，再用 [`scripts/moviehub-service.sh`](scripts/moviehub-service.sh) 管理状态、日志和健康检查。具体步骤见 [`docs/linux-deployment.md`](docs/linux-deployment.md)。应用关键事件输出到标准错误流；在 systemd 下通过 journal 查看，不记录令牌、播放 URL 或资源站请求头。

打开 `http://127.0.0.1:4488`。首次启动会导入资源站清单。首次使用时在“设置”检查媒体目录、FFmpeg 和 FFprobe 路径；如需 Emby 同步，让 Emby 的“电视节目”媒体库读取同一媒体目录（跨容器时挂载同一宿主目录），再配置 Emby 服务器 URL 和管理员 API Key 后启用。服务会请求 `POST /Library/Refresh`，以 `X-Emby-Token` 传递密钥；扫描完成时间由 Emby 决定，页面中的时间表示刷新请求已被接受。

管理台按菜单使用独立页面：首页 `/`、追更 `/follows.html`、下载 `/jobs.html`、资源站 `/sources.html`、设置 `/settings.html`。各页共用 `/app.js` 和 `/style.css`，在手机宽度下会调整导航和内容布局。

默认只监听本机。要让局域网访问，显式设置 `--listen 0.0.0.0:4488`，并通过环境变量 `MOVIEHUB_TOKEN` 提供至少 24 字符的随机令牌；请求 API 时使用 `Authorization: Bearer <令牌>`。不要把该服务直接暴露到公网。

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

播放器由 Emby 提供；本服务不代理在线视频播放、不处理 DRM，也不抓取站点 HTML。下载结果发布时采用不覆盖策略。本地 HLS 样本已验证普通 TS、AES-128、fMP4 字节范围及多链接并发下载；具体站点和家庭网络 Emby 连通性仍需按实际配置验收。

内嵌下载器支持结束的 HLS VOD 媒体片单、单视频轨主片单、相对分片地址、普通 TS/fMP4、AES-128 和字节范围分片。独立音轨、直播、低延迟 HLS、多初始化文件及其他加密方式会明确报错。网络连接沿机器环境，服务不设置代理地址。
