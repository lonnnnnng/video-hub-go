# 发版与部署

## 触发规则

- 普通提交和 `git push origin main` 仅同步代码，不触发 Release 流水线。
- 只有用户明确要求“发版”时，才在代码推送后创建并推送正式版本标签，例如 `v0.4.2`。
- 流水线仅接受 `v数字.数字.数字` 格式，不提供定时发版或分支推送发版。
- 自动构建与发布由 GitHub Actions 完成；Linux 部署是独立步骤，需在明确要求更新部署时执行。

## 发布新版本

1. 确定未使用的版本号，更新 README 示例和 `docs/releases/<版本>.md` 更新说明。
2. 检查本次差异，使用中文提交说明提交代码，先推送 `main`。
3. 明确发版后再推送标签，例如：

   ```bash
   git push origin main
   git tag -a v0.4.2 -m '发布v0.4.2'
   git push origin v0.4.2
   ```

4. 在 [Actions](https://github.com/lonnnnnng/video-hub-go/actions/workflows/release.yml) 确认“发布 Linux Release”运行成功。
5. 确认公开 Release 的版本、双架构资产、`SHA256SUMS`，必要时重新下载核验。

流水线使用 `go.mod` 声明的 Go 版本，执行竞态测试、静态检查和 JavaScript 语法检查。构建通过后创建草稿，上传 `moviehub-linux-amd64`、`moviehub-linux-arm64` 与 `SHA256SUMS`，重新下载并逐字节比较后才公开为最新版本。发行时间读取 GitHub Release 的真实发布时间。

## 失败与重跑

先查看失败步骤和错误日志，再针对原因修复。临时网络或 Runner 错误可对失败运行选择 Re-run；未公开的同版本草稿允许重传资产。已公开 Release 的同名资产不会被覆盖；代码修复应使用新版本标签，不移动已发布标签。

## 更新 Linux 部署

确认已发布目标版本后，按 [服务管理说明](service-management.md) 备份数据和旧程序，通过管理页面更新或管理脚本安装 Release。部署后核对：

- `/healthz` 返回目标版本，systemd 为 `active/running`。
- 服务器程序的 SHA256 与对应架构资产一致。
- 原有任务和站源保留；新增完成时间已从 MP4 补齐。
- 网页能加载新资源，检测更新结果符合最新 Release。
- 已完成任务的缺失海报由后台补查；Emby 能读取剧集和季海报，媒体库入口封面由 Emby 生成。
