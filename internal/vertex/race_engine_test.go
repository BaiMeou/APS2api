package vertex

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
)

// stuckRun 模拟"网络延迟一直连不上"的卡死节点：阻塞直到 ctx 取消，返回 ctx.Err()。
// 与真实 run（会话层有 180s RequestTimeout 兜底）的区别：这里在 RaceTimeout 到点就返回。
func stuckRun(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func raceTestConfig(raceTimeout int) config.ConfigProvider {
	return config.StaticProvider(config.AppConfig{ //nolint:exhaustruct
		ParallelPoolEnabled: true,
		ParallelPoolSize:    3,
		ParallelNodeTopK:    80,
		StickyNodePriority:  false,
		RaceTimeout:         raceTimeout,
	})
}

func raceTestNodes(t *testing.T) {
	t.Helper()
	nodes.MergeNodes([]nodes.Node{
		{Type: "http", Name: "n1", RawURI: "http://node1:8080"},
		{Type: "http", Name: "n2", RawURI: "http://node2:8080"},
		{Type: "http", Name: "n3", RawURI: "http://node3:8080"},
	})
	t.Cleanup(func() {
		nodes.DeleteNode("http://node1:8080")
		nodes.DeleteNode("http://node2:8080")
		nodes.DeleteNode("http://node3:8080")
	})
}

// TestRunRace_StuckNodeEliminatedByTimeout 验证核心修复：
// 全部节点都"卡死"时，RaceTimeout 到点后每个节点被单独淘汰（返回 503），
// 竞速正常结束，而不是被卡死节点拖住挂到 180s。
func TestRunRace_StuckNodeEliminatedByTimeout(t *testing.T) {
	raceTestNodes(t)

	start := time.Now()
	_, err := RunRace(context.Background(), raceTestConfig(1), stuckRun)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("全节点卡死时应在超时后返回错误")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("竞速被卡死节点拖住: 耗时 %v，应约 1 秒后全部淘汰", elapsed)
	}
	ve, ok := err.(*VertexError)
	if !ok || ve.Kind != "unavailable" {
		t.Fatalf("应返回 503 超时错误（可重试），got: %v", err)
	}
}

// TestRunRace_FastNodeWinsDespiteStuck 验证不影响正常节点：
// 一个节点立即胜出时，其余卡死节点不应拖慢胜出（RaceTimeout 不误伤快节点）。
func TestRunRace_FastNodeWinsDespiteStuck(t *testing.T) {
	raceTestNodes(t)

	var winner atomic.Bool
	run := func(ctx context.Context, uri string) (string, error) {
		if uri == "http://node1:8080" {
			winner.Store(true)
			return "ok", nil
		}
		return stuckRun(ctx, uri)
	}

	start := time.Now()
	val, err := RunRace(context.Background(), raceTestConfig(1), run)
	if err != nil || val != "ok" {
		t.Fatalf("快节点应立即胜出, val=%v err=%v", val, err)
	}
	if !winner.Load() {
		t.Fatal("node1 应被选中并胜出")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("胜出不应被卡死节点拖慢, 耗时 %v", elapsed)
	}
}

// TestRunRace_NoTimeoutKeepsLegacyBehavior 验证 RaceTimeout=0（默认）时行为不变：
// 卡死节点不会提前被淘汰（保留原有等待语义，由上层 ctx 控制）。
func TestRunRace_NoTimeoutKeepsLegacyBehavior(t *testing.T) {
	raceTestNodes(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := RunRace(ctx, raceTestConfig(0), stuckRun)
	elapsed := time.Since(start)

	// 无 RaceTimeout：等 ctx（300ms）取消后整体退出，返回 ctx.Err()。
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 ctx 超时/取消错误, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("应随 ctx 在 ~300ms 退出, 实际 %v", elapsed)
	}
}

// TestRunRace_RoundRelaySwitchesToFreshNodes 验证关单节点重试时的轮次换批：
// 并发 2、重试 1（roundBudget=1）：第一轮 2 个节点失败后，换一批从未用过的 2 个节点再试，
// 且每个节点最多被尝试一次（不重复使用节点）。
func TestRunRace_RoundRelaySwitchesToFreshNodes(t *testing.T) {
	nodes.MergeNodes([]nodes.Node{
		{Type: "http", Name: "n1", RawURI: "http://node1:8080"},
		{Type: "http", Name: "n2", RawURI: "http://node2:8080"},
		{Type: "http", Name: "n3", RawURI: "http://node3:8080"},
		{Type: "http", Name: "n4", RawURI: "http://node4:8080"},
	})
	t.Cleanup(func() {
		for i := 1; i <= 4; i++ {
			nodes.DeleteNode(fmt.Sprintf("http://node%d:8080", i))
		}
	})

	cfg := config.StaticProvider(config.AppConfig{ //nolint:exhaustruct
		ParallelPoolEnabled:      true,
		ParallelPoolSize:         2,
		ParallelNodeTopK:         80,
		StickyNodePriority:       false,
		ParallelPoolRetryEnabled: false, // 关单节点重试 → 竞速轮次换批
		MaxRetries:               1,     // 换 1 批新节点
	})

	var mu sync.Mutex
	attempted := map[string]int{}
	run := func(_ context.Context, uri string) (string, error) {
		mu.Lock()
		attempted[uri]++
		total := len(attempted)
		count := attempted[uri]
		mu.Unlock()
		if count > 1 {
			return "", errors.New("node reused") // 同一节点不应被再次尝试
		}
		// 前两个尝试的节点（即第一轮）全部失败；后续轮次成功。
		// 这不依赖于节点随机选择的顺序。
		if total <= 2 {
			return "", NewRateLimitError("quota", 0)
		}
		return "ok", nil
	}

	val, err := RunRace(context.Background(), cfg, run)
	if err != nil || val != "ok" {
		t.Fatalf("第二轮应胜出, val=%v err=%v", val, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempted) < 3 {
		t.Fatalf("应至少尝试 3 个不同节点(跨两批), got %v", attempted)
	}
	for uri, c := range attempted {
		if c != 1 {
			t.Fatalf("节点 %s 被尝试 %d 次, 应只 1 次", uri, c)
		}
	}
}
