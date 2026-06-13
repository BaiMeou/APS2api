//go:build proxynode

package transport

import (
	"path/filepath"
	"testing"

	"github.com/bsfdsagfadg/vertex/internal/config"
)

// TestPickProxyRoundRobin 验证 proxynode 构建下 pickProxy 在代理池内轮询、均匀分散。
func TestPickProxyRoundRobin(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("VPROXY_CONFIG", cfgPath)
	t.Setenv("PROXY_URL", "") // 避免环境变量覆盖
	config.InvalidateCache()

	pool := []string{"http://p1:1", "http://p2:2", "http://p3:3"}
	if err := config.WriteProxyPool(pool); err != nil {
		t.Fatalf("写代理池: %v", err)
	}

	// 连续取 3*len 次，每个代理应被命中相同次数（轮询均匀）。
	counts := map[string]int{}
	rounds := 3
	for i := 0; i < rounds*len(pool); i++ {
		counts[pickProxy()]++
	}
	for _, p := range pool {
		if counts[p] != rounds {
			t.Fatalf("代理 %s 应被命中 %d 次，实际 %d（分布=%v）", p, rounds, counts[p], counts)
		}
	}
}

// TestPickProxyEmptyPoolFallback 验证池为空时回退到单个 ProxyURL（或直连）。
func TestPickProxyEmptyPoolFallback(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("VPROXY_CONFIG", cfgPath)
	t.Setenv("PROXY_URL", "")
	config.InvalidateCache()

	// 空池 + 无单代理 → 直连（空串）。
	if got := pickProxy(); got != "" {
		t.Fatalf("空池且无单代理应直连（空串），got %q", got)
	}

	// 空池 + 配了单代理 → 回退到单代理。
	t.Setenv("PROXY_URL", "http://single:9")
	config.InvalidateCache()
	if got := pickProxy(); got != "http://single:9" {
		t.Fatalf("空池应回退到单代理，got %q", got)
	}
}
