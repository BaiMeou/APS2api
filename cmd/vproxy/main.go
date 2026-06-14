// Command vproxy 是 Vertex AI Proxy（Go 重写）的入口。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/api"
	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/metrics"
	"github.com/bsfdsagfadg/vertex/internal/spool"
	"github.com/bsfdsagfadg/vertex/internal/vertex"
)

// shutdownGrace 是优雅关闭时排干在途请求的最长等待。超时则强制结束（避免卡死部署）。
const shutdownGrace = 25 * time.Second

func main() {
	cfg := config.Load()
	metrics.Default.SetStart(time.Now().Unix())
	spool.SetMaxSpillBytes(int64(cfg.MaxSpillMB) << 20) // 大请求/媒体序列化的磁盘溢出配额上限

	keys := api.NewAPIKeyManager()
	keys.LoadKeys()

	// 管理后台：确保有管理员密码（首次启动自动生成并写回 config.json），启动会话清理后台任务。
	api.EnsureAdminPassword()
	api.StartAdminSessionCleanup(time.Hour)

	vc := vertex.NewVertexAIClient()
	vc.StartTokenPool() // 启动 recaptcha token 获取器

	srv := api.NewServer(vc, keys, cfg)
	httpServer := &http.Server{
		Addr:    "0.0.0.0:" + strconv.Itoa(cfg.PortAPI),
		Handler: srv.Handler(),
		// 只设安全的超时：ReadHeaderTimeout 防 slowloris 慢速发头、IdleTimeout 回收空闲 keep-alive。
		// 刻意不设 ReadTimeout/WriteTimeout——它们会砍断长 SSE 流式与大媒体上传/下载（大文件红线）。
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 信号处理：
	//   SIGINT/SIGTERM → 优雅关闭（停止接新连接、排干在途请求，再停 token 池）。
	//   SIGHUP         → 立即清配置/模型缓存，下次读取即热重载（省去 ≤60s TTL 等待）。
	// 配置热重载本身不掉在途请求：每请求各自 config.Load() 取值拷贝，改配置不影响已在途的。
	shutdownDone := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		for s := range sig {
			if s == syscall.SIGHUP {
				config.InvalidateCache()
				config.InvalidateModelsCache()
				log.Printf("[vproxy] 收到 SIGHUP：已清配置/模型缓存，下次读取即热重载")
				continue
			}
			log.Printf("[vproxy] 收到 %v：开始优雅关闭，排干在途请求（最长 %s）…", s, shutdownGrace)
			ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			if err := httpServer.Shutdown(ctx); err != nil {
				log.Printf("[vproxy] 优雅关闭超时/出错：%v（强制结束）", err)
			}
			cancel()
			vc.StopTokenPool()
			close(shutdownDone)
			return
		}
	}()

	log.Printf("[vproxy] 监听 %s（API 密钥 %d 个，max_retries=%d，token_pool=%d）",
		httpServer.Addr, keys.Count(), cfg.MaxRetries, cfg.TokenPoolSize)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[vproxy] server error: %v", err)
	}
	<-shutdownDone // 等优雅关闭流程跑完（Shutdown 返回后 ListenAndServe 才返回 ErrServerClosed）
	log.Printf("[vproxy] 优雅关闭完成，vproxy 退出")
}
