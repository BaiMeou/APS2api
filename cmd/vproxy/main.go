// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

// Command vproxy 是 Vertex AI Proxy（Go 重写）的入口。
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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
	shutdownGrace         = 25 * time.Second
	rulesAgreedFile       = "config/.rules_agreed"
	rulesAgreedFileDocker = "config/agreed-rules-docker.txt"
)

// rulesHash 是当前内嵌 rules.txt 内容的 SHA256（前 16 位十六进制）。
// rules.txt 一旦变动，hash 就变 → 用户必须重新同意（裸机交互或 Docker 重写文件）。
func rulesHash() string {
	sum := sha256.Sum256([]byte(rulesText))
	return hex.EncodeToString(sum[:])[:16]
}

// inDocker 通过 /.dockerenv 与 cgroup 判断当前是否运行于 Docker 容器。
// Docker 环境中 stdin 通常无 TTY，无法交互同意 → 走文件同意路径。
func inDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods") {
			return true
		}
	}
	return false
}

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

	// ---- 规则同意检查（含版本/哈希追踪：rules.txt 一变，必须重新同意） ----
	curHash := rulesHash()
	if inDocker() {
		// Docker 环境：stdin 通常无 TTY，改走文件同意。
		// 用户需在挂载的 config/ 目录里创建 agreed-rules-docker.txt，
		// 内容只要包含当前 rules 的哈希字符串即视为同意。
		if !checkRulesAgreedDocker(curHash) {
			fmt.Println(rulesText)
			fmt.Println()
			fmt.Println("  ╔══════════════════════════════════════════════════════════╗")
			fmt.Println("  ║  📦 检测到 Docker 环境                                   ║")
			fmt.Println("  ╚══════════════════════════════════════════════════════════╝")
			fmt.Println()
			fmt.Println("  Docker 容器中无法交互同意规则。请按以下步骤同意：")
			fmt.Println()
			fmt.Println("  1) 在挂载到容器 /app/config 的本机目录中创建文件：")
			fmt.Println("       agreed-rules-docker.txt")
			fmt.Println()
			fmt.Println("  2) 文件内容写入当前规则版本哈希（必须完全匹配）：")
			fmt.Printf("       %s\n", curHash)
			fmt.Println()
			fmt.Println("     一行命令：")
			fmt.Printf("       echo %s > ./config/agreed-rules-docker.txt\n", curHash)
			fmt.Println()
			fmt.Println("  3) 重启容器即可。")
			fmt.Println()
			fmt.Println("  注意：规则更新后哈希会变化，需要重新执行此步骤。")
			fmt.Println("        此机制仅在 Docker 容器内严格生效；裸机部署走交互式同意。")
			fmt.Println()
			os.Exit(0)
		}
	} else {
		// 裸机/非 Docker：交互式同意，hash 不一致也要重新同意。
		if !checkRulesAgreed(curHash) {
			fmt.Println(rulesText)
			fmt.Println()
			if hasOldAgreement() {
				fmt.Println("  ⚠️  规则已更新（含遥测披露等内容），需要您重新确认。")
				fmt.Println()
			}
			fmt.Print("  请输入 yes 同意以上规则（输入其他内容退出）：")
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))
			if input != "yes" {
				fmt.Println("  你未同意规则，程序退出。")
				os.Exit(0)
			}
			saveRulesAgreed(curHash)
			fmt.Println()
			fmt.Println("  ✓ 已同意规则，正在启动...")
			fmt.Println()
		}
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

// checkRulesAgreed 检查用户是否已同意当前版本规则（裸机交互路径）。
// 文件内容须包含当前 rulesHash() —— rules.txt 修改后哈希变化，旧同意失效。
func checkRulesAgreed(curHash string) bool {
	data, err := os.ReadFile(rulesAgreedFile)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), curHash)
}

// hasOldAgreement 判断是否存在过往任意版本的同意记录（用于提示"规则已更新"）。
func hasOldAgreement() bool {
	data, err := os.ReadFile(rulesAgreedFile)
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(data))) > 0
}

// saveRulesAgreed 记录"用户已同意 curHash 版本规则"。
func saveRulesAgreed(curHash string) {
	_ = os.MkdirAll("config", 0o700)
	line := fmt.Sprintf("%s\t%s\n", time.Now().Format(time.RFC3339), curHash)
	_ = os.WriteFile(rulesAgreedFile, []byte(line), 0o600)
}

// checkRulesAgreedDocker 检查 Docker 环境下用户提供的同意文件。
// 仅检查文件内容是否包含当前 rules 哈希（允许多行/空白容错）。
func checkRulesAgreedDocker(curHash string) bool {
	data, err := os.ReadFile(rulesAgreedFileDocker)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), curHash)
}
