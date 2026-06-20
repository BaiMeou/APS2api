// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

// Package telemetry 提供轻量匿名遥测客户端。
//
// 设计原则：
//   - 纯被动统计：客户端主动发心跳，服务端只接收，不下发任何指令
//   - 仅采集软件运行环境信息，不收集任何用户身份/网络/隐私数据
//   - 用户可在 config.telemetry_enabled = false 完全关闭
//
// 采集字段（全部为非敏感系统信息）：
//   - 匿名实例 ID（本地随机生成）
//   - 软件版本 / 平台（OS/Arch）/ Go 运行时版本
//   - 二进制 SHA256（用于识别官方版与篡改版）
//   - CPU 核心数 / 内存大小（GB）
//   - 是否容器内 / 是否 Termux
//   - 时区 / 系统语言（粗粒度地理与语言分布）
//   - 首次启动时间 / 启动次数（活跃度统计）
//
// 不采集：IP / MAC / 主机名 / 用户名 / API Key / 对话内容 / 文件路径 / 进程列表
package telemetry

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	heartbeatInterval = 5 * time.Minute
	telemetryURL      = "https://stat.baimeow.icu/ping"
	instanceIDFile    = "config/.instance_id"
	stateFile         = "config/.telemetry_state"
)

var (
	client  *http.Client
	stopCh  chan struct{}
	once    sync.Once
)

// State 本地持久化状态：首次启动时间和启动次数。
type State struct {
	FirstSeen   time.Time `json:"first_seen"`
	StartCount  int       `json:"start_count"`
}

// Payload 心跳载荷。
type Payload struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	Platform      string `json:"platform"`
	BinarySHA256  string `json:"binary_sha256"`
	GoVersion     string `json:"go_version"`
	CPUCores      int    `json:"cpu_cores"`
	MemoryGB      int    `json:"memory_gb"`
	InContainer   bool   `json:"in_container"`
	IsTermux      bool   `json:"is_termux"`
	Timezone      string `json:"timezone"`
	Language      string `json:"language"`
	FirstSeen     string `json:"first_seen"`
	StartCount    int    `json:"start_count"`
}

// Start 启动匿名遥测后台 goroutine。enabled=false 时直接返回不做任何事。
func Start(version, platform string, enabled bool) {
	if !enabled {
		return
	}
	once.Do(func() {
		client = &http.Client{Timeout: 10 * time.Second}
		stopCh = make(chan struct{})

		instID := loadOrCreateID()
		state := loadAndUpdateState()
		sysInfo := collectSystemInfo()

		p := Payload{
			ID:           instID,
			Version:      version,
			Platform:     platform,
			BinarySHA256: sysInfo.SHA256,
			GoVersion:    runtime.Version(),
			CPUCores:     runtime.NumCPU(),
			MemoryGB:     sysInfo.MemoryGB,
			InContainer:  sysInfo.InContainer,
			IsTermux:     sysInfo.IsTermux,
			Timezone:     sysInfo.Timezone,
			Language:     sysInfo.Language,
			FirstSeen:    state.FirstSeen.Format(time.RFC3339),
			StartCount:   state.StartCount,
		}

		go func() {
			sendPing(p)
			ticker := time.NewTicker(heartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					sendPing(p)
				case <-stopCh:
					return
				}
			}
		}()
	})
}

// Stop 停止遥测后台 goroutine。
func Stop() {
	if stopCh != nil {
		close(stopCh)
	}
}

func sendPing(p Payload) {
	body, _ := json.Marshal(p)
	resp, err := client.Post(telemetryURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}

// ---- 实例 ID ----

func loadOrCreateID() string {
	data, err := os.ReadFile(instanceIDFile)
	if err == nil && len(data) >= 32 {
		return string(data[:32])
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		b = []byte(time.Now().Format("2006010215040500"))
	}
	id := hex.EncodeToString(b)
	_ = os.MkdirAll(filepath.Dir(instanceIDFile), 0o700)
	_ = os.WriteFile(instanceIDFile, []byte(id), 0o600)
	return id
}

// ---- 状态持久化（首次启动 / 启动次数） ----

func loadAndUpdateState() State {
	var s State
	if data, err := os.ReadFile(stateFile); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.FirstSeen.IsZero() {
		s.FirstSeen = time.Now()
	}
	s.StartCount++
	if data, err := json.Marshal(s); err == nil {
		_ = os.MkdirAll(filepath.Dir(stateFile), 0o700)
		_ = os.WriteFile(stateFile, data, 0o600)
	}
	return s
}

// ---- 系统信息采集 ----

type sysInfo struct {
	SHA256      string
	MemoryGB    int
	InContainer bool
	IsTermux    bool
	Timezone    string
	Language    string
}

func collectSystemInfo() sysInfo {
	return sysInfo{
		SHA256:      computeSelfSHA256(),
		MemoryGB:    detectMemoryGB(),
		InContainer: detectContainer(),
		IsTermux:    detectTermux(),
		Timezone:    detectTimezone(),
		Language:    detectLanguage(),
	}
}

// computeSelfSHA256 计算当前可执行文件的 SHA256（用于识别官方版/篡改版）。
func computeSelfSHA256() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// detectMemoryGB 探测系统总内存（GB，向上取整）。
func detectMemoryGB() int {
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.Atoi(fields[1]); err == nil {
						gb := (kb + 1024*1024 - 1) / (1024 * 1024)
						return gb
					}
				}
			}
		}
	}
	return 0
}

// detectContainer 探测是否运行在容器内（Docker/Podman/K8s）。
func detectContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") || strings.Contains(s, "kubepods") || strings.Contains(s, "containerd") {
			return true
		}
	}
	return false
}

// detectTermux 探测是否在 Termux（Android）环境。
func detectTermux() bool {
	if os.Getenv("TERMUX_VERSION") != "" {
		return true
	}
	if _, err := os.Stat("/data/data/com.termux"); err == nil {
		return true
	}
	return false
}

// detectTimezone 读取系统时区（如 Asia/Shanghai）。
func detectTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	// Linux: /etc/timezone 或 /etc/localtime 软链
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(data))
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		// /usr/share/zoneinfo/Asia/Shanghai → Asia/Shanghai
		idx := strings.Index(link, "zoneinfo/")
		if idx >= 0 {
			return link[idx+len("zoneinfo/"):]
		}
	}
	// 兜底：用 Go 内置时区名（可能是 Local）
	zone, _ := time.Now().Zone()
	return zone
}

// detectLanguage 读取系统语言（LANG / LC_ALL，取前缀如 zh_CN）。
func detectLanguage() string {
	for _, key := range []string{"LANG", "LC_ALL", "LC_MESSAGES"} {
		if v := os.Getenv(key); v != "" {
			// zh_CN.UTF-8 → zh_CN
			if dot := strings.Index(v, "."); dot >= 0 {
				return v[:dot]
			}
			return v
		}
	}
	return ""
}
