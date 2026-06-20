// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultAnonAPIKey          = "AIzaSyCI-zsRP85UVOi0DjtiCwWBwQ1djDy741g"
	defaultCountTokensQuerySig = "2/mENOSldfC+HZM+tGhVuJLrl8M6gEyK3HRjUKuA5AM58="
)

type AppConfig struct {
	PortAPI                   int               `json:"port_api"`
	MaxRetries                int               `json:"max_retries"`
	AdminPassword             string            `json:"admin_password"`
	ProxyURL                  string            `json:"proxy_url"`
	Anti429Enabled            bool              `json:"anti429_enabled"`
	Anti429Target             string            `json:"anti429_target"`
	ForceNoStream             bool              `json:"force_no_stream"`
	AntiTracking              bool              `json:"anti_tracking"`
	DropMaxTokens             bool              `json:"drop_max_tokens"`
	SafetySettings            map[string]string `json:"safety_settings"`
	VertexAPIKey              string            `json:"vertex_api_key"`
	CountTokensQuerySignature string            `json:"count_tokens_query_signature"`
	MaxN                      int               `json:"max_n"`
	TokenPoolSize             int               `json:"token_pool_size"`
	MaxSpillMB                int               `json:"max_spill_mb"`
	MaxRequestMB              int               `json:"max_request_mb"`

	// 并发池与节点锁定配置
	ActiveNodeURI         string `json:"active_node_uri"`
	ParallelPoolEnabled   bool   `json:"parallel_pool_enabled"`
	ParallelPoolSize      int    `json:"parallel_pool_size"`
	ParallelPoolMaxRounds int    `json:"parallel_pool_max_rounds"`
	DebugPprof            bool   `json:"debug_pprof"`
	ParallelNodeTopK      int    `json:"parallel_node_top_k"`
	DebugMode             bool   `json:"debug_mode"`

	// 匿名遥测：仅发送实例 ID + 版本 + 平台，不含任何用户/网络/隐私数据。
	// 用于了解软件的版本分布和活跃数。指针类型区分"未设置"和"显式 false"，未设置时默认开启。
	TelemetryEnabled *bool `json:"telemetry_enabled,omitempty"`
}

func DefaultConfig() AppConfig {
	return AppConfig{
		PortAPI:                   2156,
		MaxRetries:                2,
		Anti429Target:             "system",
		AntiTracking:              true,
		VertexAPIKey:              defaultAnonAPIKey,
		CountTokensQuerySignature: defaultCountTokensQuerySig,
		MaxN:                      8,
		TokenPoolSize:             8,
		MaxSpillMB:                2048,
		ParallelPoolEnabled:       true,
		ParallelPoolSize:          4,
		ParallelNodeTopK:          80,
	}
}

var (
	mu        sync.Mutex
	cached    *AppConfig
	cacheTime time.Time
)

const cacheTTL = 60 * time.Second

func configPath() string {
	if p := os.Getenv("VPROXY_CONFIG"); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "config", "config.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("config", "config.json")
}

func ConfigPath() string { return configPath() }

func WriteSettings(updates map[string]any) error {
	path := configPath()
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &raw)
	}
	for k, v := range updates {
		raw[k] = v
	}
	if err := writeJSONFile(path, raw); err != nil {
		return err
	}
	InvalidateCache()
	return nil
}

func writeJSONFile(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	data, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Load() AppConfig {
	mu.Lock()
	defer mu.Unlock()
	if cached != nil && time.Since(cacheTime) < cacheTTL {
		return *cached
	}
	cfg := DefaultConfig()
	if data, err := os.ReadFile(configPath()); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Printf("[Config] 解析 config.json 失败: %v", err)
		} else {
			log.Printf("[Config] 成功加载配置文件 config.json")
		}
	} else if !os.IsNotExist(err) {
		log.Printf("[Config] 读取 config.json 失败: %v", err)
	}
	cached = &cfg
	cacheTime = time.Now()
	return cfg
}

func InvalidateCache() {
	mu.Lock()
	defer mu.Unlock()
	cached = nil
}
