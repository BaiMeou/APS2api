package nodes

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGetAverageLatencyUsesHealthySamples(t *testing.T) {
	resetState()
	defer resetState()
	if got := GetAverageLatency(); got != 500 {
		t.Fatalf("无样本默认 500, got %v", got)
	}
	MergeNodes([]Node{
		{RawURI: "http://fast", Name: "fast"},
		{RawURI: "http://slow", Name: "slow"},
		{RawURI: "http://off", Name: "off", Disabled: true},
		{RawURI: "http://cool", Name: "cool"},
	})
	RecordTest("http://fast", true, 100, "")
	RecordTest("http://slow", true, 300, "")
	RecordTest("http://off", true, 10, "")
	RecordTest("http://cool", true, 20, "")
	RecordRateLimit("http://cool", 60)
	if got := GetAverageLatency(); got != 200 {
		t.Fatalf("应只平均未冷却的健康样本 (100+300)/2=200, got %v", got)
	}
}

func TestGetNodeNameKnownAndUnknown(t *testing.T) {
	resetState()
	defer resetState()
	MergeNodes([]Node{{RawURI: "http://alpha:1", Name: "alpha"}})
	if got := GetNodeName("http://alpha:1"); got != "alpha" {
		t.Fatalf("GetNodeName=%q, want alpha", got)
	}
	if got := GetNodeName("http://missing:1"); got != "Unknown" {
		t.Fatalf("缺失节点应返回 Unknown, got %q", got)
	}
}

func TestSelectForParallelPrefersLowerInFlight(t *testing.T) {
	resetState()
	defer resetState()
	MergeNodes([]Node{
		{RawURI: "http://a", Name: "a"},
		{RawURI: "http://b", Name: "b"},
		{RawURI: "http://c", Name: "c"},
	})
	RecordTest("http://a", true, 40, "")
	RecordTest("http://b", true, 40, "")
	RecordTest("http://c", true, 40, "")
	mu.Lock()
	healthMap["http://a"].InFlight = 2
	healthMap["http://b"].InFlight = 0
	healthMap["http://c"].InFlight = 1
	healthMap["http://a"].LastSelectedAt = 0
	healthMap["http://b"].LastSelectedAt = 0
	healthMap["http://c"].LastSelectedAt = 0
	mu.Unlock()

	selected := SelectForParallel(2, 80, false, false)
	if len(selected) != 2 {
		t.Fatalf("want 2 nodes, got %+v", selected)
	}
	if selected[0].RawURI != "http://b" || selected[1].RawURI != "http://c" {
		t.Fatalf("应先选低 InFlight: got %+v", selected)
	}
}

func TestDecInFlightDoesNotGoNegative(t *testing.T) {
	resetState()
	defer resetState()
	MergeNodes([]Node{{RawURI: "http://n", Name: "n"}})
	RecordTest("http://n", true, 10, "")

	DecInFlight("http://n")
	DecInFlight("http://n")
	h := LoadHealth()["http://n"]
	if h == nil || atomic.LoadInt32(&h.InFlight) != 0 {
		t.Fatalf("超额 Dec 后 InFlight 应为 0, got %+v", h)
	}
	IncInFlight("http://n")
	if atomic.LoadInt32(&h.InFlight) != 1 {
		t.Fatalf("触底后 Inc 应为 1, got %+v", h)
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			DecInFlight("http://n")
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&h.InFlight) != 0 {
		t.Fatalf("并发超额 Dec 后 InFlight 应为 0, got %d", h.InFlight)
	}
}

func TestIncDecInFlightUnknownURINoOp(t *testing.T) {
	resetState()
	defer resetState()
	IncInFlight("http://missing")
	DecInFlight("http://missing")
}

func TestGetAverageLatencyConcurrentWithRecordTest(t *testing.T) {
	resetState()
	defer resetState()
	MergeNodes([]Node{{RawURI: "http://n", Name: "n"}})
	RecordTest("http://n", true, 50, "")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				if got := GetAverageLatency(); got != 50 {
					t.Errorf("平均延迟应为 50, got %v", got)
					return
				}
				RecordTest("http://n", true, 50, "")
			}
		}()
	}
	wg.Wait()
	if got := GetAverageLatency(); got != 50 {
		t.Fatalf("并发读写后平均延迟应为 50, got %v", got)
	}
}

func TestStickyPoolAddEvictCloneRestore(t *testing.T) {
	p := NewStickyNodePool()
	p.Add("http://a")
	p.Add("http://b")
	if !p.IsSticky("http://a") || p.AvailableCount() != 2 {
		t.Fatalf("Add 后应 sticky, count=%d list=%v", p.AvailableCount(), p.List())
	}
	p.Evict("http://a")
	if p.IsSticky("http://a") || !p.IsSticky("http://b") {
		t.Fatalf("Evict 只应去掉 a, list=%v", p.List())
	}
	if got := p.List(); len(got) != 1 || got[0] != "http://b" {
		t.Fatalf("List=%v, want [http://b]", got)
	}

	resetState()
	defer resetState()
	globalStickyPool.Add("http://x")
	globalStickyPool.Add("http://y")
	snap := cloneStickyPoolUnsafe()
	globalStickyPool.Evict("http://x")
	if globalStickyPool.IsSticky("http://x") {
		t.Fatal("Evict 后 x 不应仍 sticky")
	}
	restoreStickyPoolUnsafe(snap)
	if !globalStickyPool.IsSticky("http://x") || !globalStickyPool.IsSticky("http://y") {
		t.Fatalf("restore 应还原 sticky 集合, list=%v", globalStickyPool.List())
	}
}

func TestStickyPoolConcurrentAddEvict(t *testing.T) {
	p := NewStickyNodePool()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uri := fmt.Sprintf("http://s-%d", i)
			p.Add(uri)
			_ = p.IsSticky(uri)
			_ = p.List()
			_ = p.AvailableCount()
			p.Evict(uri)
		}(i)
	}
	wg.Wait()
	if p.AvailableCount() != 0 {
		t.Fatalf("成对 Add/Evict 后应为空, list=%v", p.List())
	}
}

func TestInFlightIncDecBalances(t *testing.T) {
	resetState()
	defer resetState()
	MergeNodes([]Node{{RawURI: "http://n", Name: "n"}})
	RecordTest("http://n", true, 10, "")

	const workers = 32
	const loops = 80
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range loops {
				IncInFlight("http://n")
				DecInFlight("http://n")
			}
		}()
	}
	wg.Wait()
	h := LoadHealth()["http://n"]
	if h == nil || h.InFlight != 0 {
		t.Fatalf("成对 Inc/Dec 后 InFlight 应为 0, got %+v", h)
	}
}

func TestHotspotReadersWritersNoRaceOrNegativeInFlight(t *testing.T) {
	resetState()
	defer resetState()
	nodes := make([]Node, 12)
	for i := range nodes {
		uri := fmt.Sprintf("http://hot-%d", i)
		nodes[i] = Node{RawURI: uri, Name: fmt.Sprintf("hot-%d", i)}
	}
	MergeNodes(nodes)
	for _, n := range nodes {
		RecordTest(n.RawURI, true, 25, "")
	}

	var wg sync.WaitGroup
	var selects atomic.Int32
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				_ = GetNodeName(nodes[selects.Add(1)%int32(len(nodes))].RawURI)
				_ = GetAverageLatency()
				got := SelectForParallel(3, 80, false, true)
				if len(got) == 0 || len(got) > 3 {
					t.Errorf("SelectForParallel 数量异常: %d", len(got))
					return
				}
				IncInFlight(got[0].RawURI)
				DecInFlight(got[0].RawURI)
			}
		}()
	}
	wg.Wait()
	for _, n := range nodes {
		if h := LoadHealth()[n.RawURI]; h != nil && h.InFlight < 0 {
			t.Fatalf("%s InFlight 为负: %d", n.RawURI, h.InFlight)
		}
	}
}

func BenchmarkSelectForParallel(b *testing.B) {
	resetState()
	defer resetState()
	nodes := make([]Node, 40)
	for i := range nodes {
		nodes[i] = Node{RawURI: fmt.Sprintf("http://bench-%d", i), Name: fmt.Sprintf("b%d", i)}
	}
	MergeNodes(nodes)
	for _, n := range nodes {
		RecordTest(n.RawURI, true, 40, "")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SelectForParallel(3, 80, false, false)
	}
}

func BenchmarkGetNodeName(b *testing.B) {
	resetState()
	defer resetState()
	nodes := make([]Node, 40)
	for i := range nodes {
		nodes[i] = Node{RawURI: fmt.Sprintf("http://name-%d", i), Name: fmt.Sprintf("n%d", i)}
	}
	MergeNodes(nodes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetNodeName(nodes[i%len(nodes)].RawURI)
	}
}
