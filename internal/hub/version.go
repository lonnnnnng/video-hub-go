package hub

// long: 发布构建注入版本号，健康检查据此确认升级后运行的是新进程。
var Version = "dev"

// long: 仅注入明确的发行时间；未注入时查询对应 GitHub Release，不把构建时间冒充发行时间。
var ReleaseTime = ""
