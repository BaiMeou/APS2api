//go:build proxynode

package transport

import (
	"sync/atomic"

	"github.com/bsfdsagfadg/vertex/internal/config"
)

// proxyRR 是代理池轮询游标，保证多个会话在池内代理间均匀轮转（而非每次随机可能扎堆）。
var proxyRR atomic.Uint64

// pickProxy 从出站代理池轮询选一个代理给本次会话。
//
// 池非空时按轮询游标取下一个，使并发会话均匀分散到各代理节点；池为空时回退到
// 单个 ProxyURL（或直连）。这样默认配置（无池、无单代理）即直连，配了池就自动负载分散。
func pickProxy() string {
	pool := config.ProxyPoolList()
	if len(pool) == 0 {
		return config.ProxyURL()
	}
	// 轮询：fetch-and-add 后在 uint64 空间取模（恒非负，避免溢出转 int 时变负），O(1) 且并发安全。
	idx := (proxyRR.Add(1) - 1) % uint64(len(pool))
	return pool[idx]
}
