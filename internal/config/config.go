// Package config 加载运行配置（带 60 秒内存缓存）。
//
// 应用配置（AppConfig）。字段默认值见 DefaultConfig。配置来源优先级：
// 环境变量 VPROXY_CONFIG 指定的路径 > 可执行文件同级 config/config.json > 工作目录 config/config.json。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 匿名上游接口的内置默认值。
const (
	defaultAnonAPIKey          = "AIzaSyCI-zsRP85UVOi0DjtiCwWBwQ1djDy741g"
	defaultCountTokensQuerySig = "2/mENOSldfC+HZM+tGhVuJLrl8M6gEyK3HRjUKuA5AM58="
)

// AppConfig 是应用配置。
//
// 注意：里程碑1 用强类型字段，未保活配置文件中的额外字段（如 max_concurrent）。
// 额外字段属服务器并发优化项，里程碑1 不需要；里程碑2 接入并发封顶时再补额外字段保活。
type AppConfig struct {
	PortAPI                   int               `json:"port_api"`
	MaxRetries                int               `json:"max_retries"`
	ErrorDir                  string            `json:"error_dir"`
	Debug                     bool              `json:"debug"`
	LogDir                    string            `json:"log_dir"`
	AdminPassword             string            `json:"admin_password"`
	ProxyURL                  string            `json:"proxy_url"`
	// ProxyPool 是出站代理池（多代理节点）：每次新建会话从中轮询选一个出站代理。
	// 为空时回退到单个 ProxyURL（或直连）。仅 proxynode 构建消费此字段，serveropt 构建忽略它。
	ProxyPool                 []string          `json:"proxy_pool"`
	Anti429Enabled            bool              `json:"anti429_enabled"`
	Anti429Target             string            `json:"anti429_target"`
	ForceNoStream             bool              `json:"force_no_stream"`
	AntiTracking              bool              `json:"anti_tracking"`
	DropMaxTokens             bool              `json:"drop_max_tokens"`
	SafetySettings            map[string]string `json:"safety_settings"`
	VertexAPIKey              string            `json:"vertex_api_key"`
	CountTokensQuerySignature string            `json:"count_tokens_query_signature"`
	MaxN                      int               `json:"max_n"`
	// TokenPoolSize 是 recaptcha token 后台预取池大小（服务器优化）。
	// 默认 8；设 0 关闭预取、永远实时取（单实例自用）。
	TokenPoolSize int `json:"token_pool_size"`
	// MaxSpillMB 是大请求/媒体体落盘的全局磁盘配额（MB，服务器优化）。
	// 默认 2048（2GB，磁盘紧张时可调小）；超配额则降级留内存、不失败请求。
	MaxSpillMB int `json:"max_spill_mb"`
	// MaxRequestMB 是单请求体大小上限（MB）。默认 0 = 不限（避免对合法大媒体取舍）；
	// >0 时给入站 body 套 MaxBytesReader 作为"防绝对失控"安全阀，超限返回 413。
	MaxRequestMB int `json:"max_request_mb"`
}

// DefaultConfig 返回默认配置。
func DefaultConfig() AppConfig {
	return AppConfig{
		PortAPI:                   2156,
		MaxRetries:                2,
		ErrorDir:                  "errors",
		Debug:                     false,
		LogDir:                    "logs",
		AdminPassword:             "",
		ProxyURL:                  "",
		ProxyPool:                 nil,
		Anti429Enabled:            false,
		Anti429Target:             "system",
		ForceNoStream:             false,
		AntiTracking:              true,
		DropMaxTokens:             false,
		SafetySettings:            map[string]string{},
		VertexAPIKey:              defaultAnonAPIKey,
		CountTokensQuerySignature: defaultCountTokensQuerySig,
		MaxN:                      8,
		TokenPoolSize:             8,
		MaxSpillMB:                2048,
	}
}

var (
	mu        sync.Mutex
	cached    *AppConfig
	cacheTime time.Time
)

const cacheTTL = 60 * time.Second

// configPath 解析配置文件路径。
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

// ConfigPath 导出当前 config.json 的解析路径（供 admin 后台读写配置时定位文件）。
func ConfigPath() string { return configPath() }

// WriteSettings 合并若干字段写回 config.json：读现有 JSON（保留未提及字段，包括强类型 AppConfig
// 之外的额外字段如 max_concurrent）、覆盖 updates 中的键、原子写回，并清空配置缓存。
// 读取 JSON + 合并 + 写回 + 失效配置缓存。
func WriteSettings(updates map[string]any) error {
	path := configPath()

	// 以原始 map 形式读现有配置，保留所有未知字段不丢失（admin 只动它认识的几个）。
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &raw) // 解析失败则当空配置重建
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

// writeJSONFile 把 v 以缩进 JSON 原子写到 path（先写 .tmp 再 rename，避免半截文件）。
func writeJSONFile(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load 加载配置，带 60 秒内存缓存。
//
// 加载逻辑：以 DefaultConfig 为基底，把配置文件中出现的字段覆盖上去；
// 文件不存在或解析失败时退回全默认（异常分支同样刷新缓存时间，避免反复读盘）。
func Load() AppConfig {
	mu.Lock()
	defer mu.Unlock()

	if cached != nil && time.Since(cacheTime) < cacheTTL {
		return *cached
	}

	cfg := DefaultConfig()
	if data, err := os.ReadFile(configPath()); err == nil {
		// Unmarshal 到已含默认值的 cfg：文件里出现的字段覆盖默认，未出现的保留默认。
		_ = json.Unmarshal(data, &cfg)
	}

	cached = &cfg
	cacheTime = time.Now()
	return cfg
}

// InvalidateCache 强制清除缓存（admin 修改配置后调用；里程碑1 暂未接入 admin）。
func InvalidateCache() {
	mu.Lock()
	defer mu.Unlock()
	cached = nil
	cacheTime = time.Time{}
}

// ProxyURL 动态读取代理配置：环境变量 PROXY_URL 优先，其次 config.json 的 proxy_url。
func ProxyURL() string {
	if v := os.Getenv("PROXY_URL"); v != "" {
		return v
	}
	return Load().ProxyURL
}

// ProxyPoolList 返回当前出站代理池（config.json 的 proxy_pool）。多代理节点用。
func ProxyPoolList() []string {
	return Load().ProxyPool
}

// WriteProxyPool 把代理池写回 config.json（合并保留其它字段）并清缓存。admin 代理管理用。
func WriteProxyPool(pool []string) error {
	if pool == nil {
		pool = []string{}
	}
	return WriteSettings(map[string]any{"proxy_pool": pool})
}
