package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"movie-hub-go/internal/hub"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("Movie Hub Go 运行失败", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:4488", "HTTP 监听地址")
	data := flag.String("data-dir", "./data", "JSON 状态目录")
	media := flag.String("media-dir", "./media", "首次启动的媒体目录")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return errors.New("监听地址格式无效")
	}
	token := os.Getenv("MOVIEHUB_TOKEN")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) && len(token) < 24 {
		return errors.New("非本机监听需要至少 24 字符的 MOVIEHUB_TOKEN")
	}
	if token != "" && len(token) < 24 {
		return errors.New("MOVIEHUB_TOKEN 至少需要 24 字符")
	}
	abs, err := filepath.Abs(*media)
	if err != nil {
		return err
	}
	store, err := hub.OpenStore(*data, abs)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Error("关闭状态文件锁失败", "error", err)
		}
	}()
	service := hub.NewService(store)
	if err = service.Start(); err != nil {
		return err
	}
	defer func() {
		service.Close()
		slog.Info("后台任务已退出")
	}()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: service.Handler(token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 60 * time.Second}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(done)
	go func() {
		sig := <-done
		slog.Info("收到停止信号，开始关闭 HTTP 服务", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("HTTP 服务关闭失败", "error", err)
		}
	}()
	// long: 启动日志只记录监听和目录，不输出访问令牌或 JSON 状态内容。
	slog.Info("Movie Hub Go 已启动", "listen", listener.Addr().String(), "data_dir", *data, "media_dir", abs)
	if err = server.Serve(listener); err != nil && err != http.ErrServerClosed {
		// long: 监听或服务异常必须返回失败状态，systemd 才能按 Restart=on-failure 拉起服务。
		return err
	}
	return nil
}
