// updater 主程序：加载配置并启动 Gin HTTP 服务。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"updater/internal/config"
	"updater/internal/httpapi"
	"updater/internal/jobs"
	"updater/internal/updater"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	store := jobs.NewStore()
	runner := updater.NewRunner(cfg)
	router := httpapi.NewRouter(cfg, store, runner)

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("updater 监听 %s，项目目录 %s", cfg.Addr(), cfg.ComposeProjectDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("优雅关闭超时或失败: %v", err)
	}
	log.Println("updater 已退出")
}
