// MediaCleaner Pro 服务入口。
//
// 单进程单二进制：HTTP API + 扫描引擎 + 清理执行器 + 静态前端。
// 数据库使用纯 Go 的 SQLite 驱动（modernc.org/sqlite），无需 CGO，
// 因此可以交叉编译出静态二进制，直接塞进极简镜像运行。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mediacleaner/internal/api"
	"mediacleaner/internal/auth"
	"mediacleaner/internal/cleaner"
	"mediacleaner/internal/config"
	"mediacleaner/internal/media"
	"mediacleaner/internal/rules"
	"mediacleaner/internal/scan"
	"mediacleaner/internal/store"
)

func main() {
	var (
		showVersion = flag.Bool("version", false, "打印版本号后退出")
		addr        = flag.String("addr", "", "监听地址，例如 :8080")
	)
	flag.Parse()

	cfg := config.Load()
	if *showVersion {
		fmt.Printf("%s %s\n", cfg.AppName, cfg.Version)
		return
	}
	if *addr != "" {
		cfg.Addr = *addr
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[mediacleaner] ")

	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()
	log.Printf("数据库: %s", cfg.DBPath)

	// 媒体能力探测（决定是否具备转码/关键帧能力）
	media.SetBinaries(cfg.FFmpeg, cfg.FFprobe)
	if cap := media.Capabilities(); cap["ffmpeg"] {
		log.Printf("已检测到 ffmpeg，视频预览转码与相似视频检测可用")
	} else {
		log.Printf("未检测到 ffmpeg：视频将使用浏览器可直放的格式，相似视频检测不可用（其余功能不受影响）")
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 账号初始化
	authSvc := auth.New(st, cfg)
	if pwd, err := authSvc.EnsureAdmin(rootCtx); err != nil {
		log.Fatalf("初始化管理员失败: %v", err)
	} else if pwd != "" {
		log.Printf("已创建管理员账号 admin，初始密码: %s（已写入 %s/initial-admin.txt）",
			pwd, cfg.ConfigDir)
	}
	authSvc.PurgeExpiredSessions(rootCtx)

	// 存储卷
	roots := scan.NewRootService(st, cfg)
	for _, note := range roots.EnsureDefaults(rootCtx) {
		log.Printf("存储卷: %s", note)
	}

	// 内置规则
	if n, err := rules.NewStore(st).SeedDefaults(rootCtx); err != nil {
		log.Printf("写入内置规则失败: %v", err)
	} else if n > 0 {
		log.Printf("已写入 %d 条内置清理规则（默认全部需要人工确认）", n)
	}

	// 扫描引擎
	engine := scan.NewEngine(st, cfg, roots)
	engine.Start(rootCtx)

	// 清理执行器与隔离区
	quar := cleaner.NewQuarantine(st, cfg, func(ctx context.Context, p string) (bool, error) {
		if _, err := roots.Resolve(ctx, p); err != nil {
			return false, nil
		}
		return true, nil
	})
	exec := cleaner.NewExecutor(st, cfg, quar)
	ruleEngine := rules.NewEngine(st, cfg)

	srv := api.New(cfg, st, authSvc, roots, engine, rules.NewStore(st), ruleEngine, exec, quar)
	srv.StartBackground(rootCtx)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// SSE 长连接需要，写超时交给客户端断开处理
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("%s %s 已启动，监听 %s", cfg.AppName, cfg.Version, cfg.Addr)
		log.Printf("数据根目录: %s", cfg.DataRoot)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务启动失败: %v", err)
		}
	}()

	<-rootCtx.Done()
	log.Printf("收到退出信号，正在关闭…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭 HTTP 服务超时: %v", err)
	}
	log.Printf("已退出")
}
