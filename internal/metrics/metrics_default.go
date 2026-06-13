//go:build !serveropt

// Package metrics 是自身可观测采集器的精简实现：所有方法空操作、Snapshot 返回零值。
//
// 内部健康采集（成败率、延迟分位、最近请求历史）是面向多机部署的服务器优化，
// 单实例自用无需统计设施，故精简实现退化为空采集器：保留完整的类型与方法签名，
// 使调用方（热路径里的 IncUpstream*/StartRequest/EndRequest/RecordRequest 等）
// 无需任何 if 分支即自动失效，零开销。
package metrics

// Collector 是空采集器。无字段——所有方法均为空操作。
type Collector struct{}

// RequestRecord 与完整实现保持相同字段（精简实现从不产生记录）。
type RequestRecord struct {
	Time    string  `json:"time"`
	Path    string  `json:"path"`
	Success bool    `json:"success"`
	Latency float64 `json:"latency"`
}

// Snapshot 与完整实现保持相同字段（精简实现恒为零值）。
type Snapshot struct {
	StartUnix      int64   `json:"start_unix"`
	Total          int64   `json:"total"`
	Success        int64   `json:"success"`
	Fail           int64   `json:"fail"`
	Active         int64   `json:"active"`
	SuccessRate    float64 `json:"success_rate"`
	Upstream429    int64   `json:"upstream_429"`
	UpstreamEmpty  int64   `json:"upstream_empty"`
	UpstreamAuth   int64   `json:"upstream_auth_fail"`
	LatencyP50     float64 `json:"latency_p50_sec"`
	LatencyP95     float64 `json:"latency_p95_sec"`
	LatencyP99     float64 `json:"latency_p99_sec"`
	LatencySamples int     `json:"latency_samples"`
}

// Default 是进程级全局采集器（空实现）。
var Default = New(0)

// New 构造空采集器；maxLatency 被忽略，仅保留签名兼容。
func New(maxLatency int) *Collector { return &Collector{} }

// 以下方法全为空操作 / 零值返回（精简实现不采集任何指标）。

func (c *Collector) SetStart(unix int64)                                    {}
func (c *Collector) StartRequest()                                          {}
func (c *Collector) EndRequest(success bool, latencySec float64)            {}
func (c *Collector) IncUpstream429()                                        {}
func (c *Collector) IncUpstreamEmpty()                                      {}
func (c *Collector) IncUpstreamAuth()                                       {}
func (c *Collector) Snapshot() Snapshot                                     { return Snapshot{} }
func (c *Collector) Reset()                                                 {}
func (c *Collector) RecordRequest(path string, success bool, lat float64, at string) {}
func (c *Collector) RecentRequests() []RequestRecord                        { return nil }
