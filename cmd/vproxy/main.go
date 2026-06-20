// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

// Command vproxy 是 Vertex AI Proxy（Go 重写）的入口。
package main

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/api"
	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/metrics"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
	"github.com/bsfdsagfadg/vertex/internal/spool"
	"github.com/bsfdsagfadg/vertex/internal/telemetry"
	"github.com/bsfdsagfadg/vertex/internal/transport"
	"github.com/bsfdsagfadg/vertex/internal/vertex"
)

// version / buildCommit / buildTime 由构建脚本通过 -ldflags 注入。
var (
	version     = "dev"
	buildCommit = "unknown"
	buildTime   = "unknown"
)

//go:embed rules.txt
var rulesText string

const (
	shutdownGrace   = 25 * time.Second
	rulesAgreedFile = "config/.rules_agreed"
)

func main() {
	// ---- 启动版权横幅 ----
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Printf("║  Vertex AI Proxy  %-42s ║\n", version)
	fmt.Println("║  Copyright (c) 2026 BaiMeow. All rights reserved.          ║")
	fmt.Println("║  PolyForm Noncommercial License 1.0.0                      ║")
	fmt.Printf("║  Build: %s / %s                                  ║\n", buildCommit, buildTime)
	fmt.Printf("║  Platform: %s/%s                                       ║\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")

	// ---- 反诈播报（每次启动必显示） ----
	fmt.Println()
	fmt.Println("  ╔══════════════════════════════════════════════════════════╗")
	fmt.Println("  ║                                                          ║")
	fmt.Println("  ║   ⚠️  本软件完全免费，如果你花钱购买了这个软件，         ║")
	fmt.Println("  ║       你被骗了。请立即联系卖家退款。                     ║")
	fmt.Println("  ║                                                          ║")
	fmt.Println("  ║   获取正版：https://discord.gg/odysseia                  ║")
	fmt.Println("  ║                                                          ║")
	fmt.Println("  ╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	// ---- 规则同意检查 ----
	if !checkRulesAgreed() {
		fmt.Println(rulesText)
		fmt.Println()
		fmt.Print("  请输入 yes 同意以上规则（输入其他内容退出）：")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))
		if input != "yes" {
			fmt.Println("  你未同意规则，程序退出。")
			os.Exit(0)
		}
		saveRulesAgreed()
		fmt.Println()
		fmt.Println("  ✓ 已同意规则，正在启动...")
		fmt.Println()
	}

	cfg := config.Load()
	metrics.Default.SetStart(time.Now().Unix())
	spool.SetMaxSpillBytes(int64(cfg.MaxSpillMB) << 20)

	nodes.DeleteNodeCallback = transport.RemoveProxy
	transport.StartProxyGC(5*time.Minute, 30*time.Minute)

	keys := api.NewAPIKeyManager()
	keys.LoadKeys()

	api.EnsureAdminPassword()
	api.StartAdminSessionCleanup(time.Hour)

	vc := vertex.NewVertexAIClient()
	vc.StartTokenPool()

	// 启动匿名遥测（默认开启，可在 config.json 设置 telemetry_enabled=false 关闭）。
	// 仅采集软件版本/平台/Go运行时/CPU/内存/容器/时区/语言/启动次数等非敏感信息。
	telemetryEnabled := true
	if cfg.TelemetryEnabled != nil {
		telemetryEnabled = *cfg.TelemetryEnabled
	}
	telemetry.Start(version, runtime.GOOS+"/"+runtime.GOARCH, telemetryEnabled)

	srv := api.NewServer(vc, keys, cfg)
	httpServer := &http.Server{
		Addr:              "0.0.0.0:" + strconv.Itoa(cfg.PortAPI),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

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
			transport.StopAllProxies()
			vc.StopTokenPool()
			telemetry.Stop()
			close(shutdownDone)
			return
		}
	}()

	log.Printf("[vproxy] 监听 %s（API 密钥 %d 个，max_retries=%d，token_pool=%d）",
		httpServer.Addr, keys.Count(), cfg.MaxRetries, cfg.TokenPoolSize)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[vproxy] server error: %v", err)
	}
	<-shutdownDone
	log.Printf("[vproxy] 优雅关闭完成，vproxy 退出")
}

// checkRulesAgreed 检查用户是否已同意规则。
func checkRulesAgreed() bool {
	data, err := os.ReadFile(rulesAgreedFile)
	if err != nil {
		return false
	}
	// 文件内容是同意时的时间戳，只要文件存在就算同意过
	return len(data) > 0
}

// saveRulesAgreed 保存规则同意记录。
func saveRulesAgreed() {
	_ = os.MkdirAll("config", 0o700)
	_ = os.WriteFile(rulesAgreedFile, []byte(time.Now().Format(time.RFC3339)), 0o600)
}
