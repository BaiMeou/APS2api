// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

// Package telemetry 提供轻量匿名统计客户端 + 远程熔断查询。
// 心跳：每 5 分钟向统计服务器发送一次，仅含匿名实例 ID + 版本 + 平台，不含用户隐私。
// 熔断：每 1 分钟查询中央服务器的 /killswitch 状态，IsKilled() 供上层中间件调用。
package telemetry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	heartbeatInterval  = 5 * time.Minute
	killswitchInterval = 1 * time.Minute
	telemetryURL       = "http://34.83.159.156:8090/ping"
	killswitchURL      = "http://34.83.159.156:8090/killswitch"
	instanceIDFile     = "config/.instance_id"
)

var (
	client *http.Client
	stopCh chan struct{}
	once   sync.Once
	idFile string

	killed atomic.Bool // 远程熔断状态，true = 所有请求返回 503
)

// Start 启动匿名统计 + 熔断查询后台 goroutine。
func Start(version, platform string) {
	once.Do(func() {
		client = &http.Client{Timeout: 10 * time.Second}
		stopCh = make(chan struct{})
		idFile = instanceIDFile
		instID := loadOrCreateID()

		// 心跳 goroutine
		go func() {
			sendPing(instID, version, platform)
			ticker := time.NewTicker(heartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					sendPing(instID, version, platform)
				case <-stopCh:
					return
				}
			}
		}()

		// 熔断查询 goroutine
		go func() {
			checkKillSwitch()
			ticker := time.NewTicker(killswitchInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					checkKillSwitch()
				case <-stopCh:
					return
				}
			}
		}()
	})
}

// Stop 停止所有后台 goroutine。
func Stop() {
	if stopCh != nil {
		close(stopCh)
	}
}

// IsKilled 返回远程熔断状态。true 时上层应拒绝所有用户请求。
func IsKilled() bool {
	return killed.Load()
}

// checkKillSwitch 查询中央服务器的熔断状态。
func checkKillSwitch() {
	resp, err := client.Get(killswitchURL)
	if err != nil {
		return // 网络故障时保持上次状态（不因断网误开熔断）
	}
	defer resp.Body.Close()
	var result struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}
	prev := killed.Swap(result.Enabled)
	if prev != result.Enabled {
		if result.Enabled {
			log.Printf("[telemetry] ⚠️  远程熔断已开启 — 所有用户请求将返回 503")
		} else {
			log.Printf("[telemetry] ✅ 远程熔断已关闭 — 服务恢复正常")
		}
	}
}

// ---- 内部工具 ----

func loadOrCreateID() string {
	data, err := os.ReadFile(idFile)
	if err == nil && len(data) >= 32 {
		return string(data[:32])
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		b = []byte(time.Now().Format("2006010215040500"))
	}
	id := hex.EncodeToString(b)
	_ = os.MkdirAll(filepath.Dir(idFile), 0o700)
	_ = os.WriteFile(idFile, []byte(id), 0o600)
	return id
}

type pingPayload struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

func sendPing(id, version, platform string) {
	body, _ := json.Marshal(pingPayload{ID: id, Version: version, Platform: platform})
	resp, err := client.Post(telemetryURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	// 心跳响应也携带 killswitch 状态（双重保障）
	var result struct {
		OK         bool `json:"ok"`
		Killswitch bool `json:"killswitch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
		killed.Store(result.Killswitch)
	}
	if resp.StatusCode == 200 {
		log.Printf("[telemetry] 心跳已发送 (id=%s, ver=%s)", id[:8], version)
	}
}
