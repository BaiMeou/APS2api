package nodes

import (
	"encoding/json"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Node struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	RawURI   string `json:"raw_uri"`
	Disabled bool   `json:"disabled"`
}

type NodeHealth struct {
	SuccessCount        int     `json:"success_count"`
	FailCount           int     `json:"fail_count"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	LastTestMs          float64 `json:"last_test_ms"`
	LastTestError       string  `json:"last_test_error"`
	LastSuccessAt       int64   `json:"last_success_at"`
	LastFailAt          int64   `json:"last_fail_at"`
	CooldownUntil       int64   `json:"cooldown_until"`
}

var (
	mu        sync.Mutex
	nodeList  []Node
	healthMap = make(map[string]*NodeHealth)
	loaded    bool
)

func fileDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "config")
	}
	return "config"
}

func ensureLoaded() {
	if loaded {
		return
	}
	loaded = true
	if b, err := os.ReadFile(filepath.Join(fileDir(), "nodes.json")); err == nil {
		var d struct {
			Nodes []Node `json:"nodes"`
		}
		json.Unmarshal(b, &d)
		nodeList = d.Nodes
	}
	if b, err := os.ReadFile(filepath.Join(fileDir(), "node_health.json")); err == nil {
		json.Unmarshal(b, &healthMap)
	}
}

func LoadNodes() []Node {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	log.Printf("[Nodes] 获取所有节点 (数量: %d)", len(nodeList))
	return nodeList
}

func LoadHealth() map[string]*NodeHealth {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	return healthMap
}

func saveNodesUnsafe() {
	d := map[string]any{"nodes": nodeList}
	b, _ := json.MarshalIndent(d, "", "  ")
	os.WriteFile(filepath.Join(fileDir(), "nodes.json"), b, 0644)
}

func saveHealthUnsafe() {
	b, _ := json.MarshalIndent(healthMap, "", "  ")
	os.WriteFile(filepath.Join(fileDir(), "node_health.json"), b, 0644)
}

func MergeNodes(newNodes []Node) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	existing := make(map[string]bool)
	for _, n := range nodeList {
		existing[n.RawURI] = true
	}
	for _, n := range newNodes {
		if !existing[n.RawURI] {
			nodeList = append(nodeList, n)
			existing[n.RawURI] = true
		}
	}
	saveNodesUnsafe()
}

func DeleteNode(uri string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	var kept []Node
	for _, n := range nodeList {
		if n.RawURI != uri {
			kept = append(kept, n)
		}
	}
	nodeList = kept
	delete(healthMap, uri)
	saveNodesUnsafe()
	saveHealthUnsafe()
}

func BatchUpdateNodesDisabled(uris []string, disabled bool) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	targets := make(map[string]bool)
	for _, u := range uris {
		targets[u] = true
	}
	for i, n := range nodeList {
		if targets[n.RawURI] {
			nodeList[i].Disabled = disabled
		}
	}
	saveNodesUnsafe()
}

func BatchDeleteNodes(uris []string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	targets := make(map[string]bool)
	for _, u := range uris {
		targets[u] = true
		delete(healthMap, u)
	}
	var kept []Node
	for _, n := range nodeList {
		if !targets[n.RawURI] {
			kept = append(kept, n)
		}
	}
	nodeList = kept
	saveNodesUnsafe()
	saveHealthUnsafe()
}

func DedupNodes() int {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	seen := make(map[string]bool)
	var kept []Node
	removed := 0
	for _, n := range nodeList {
		if !seen[n.RawURI] {
			kept = append(kept, n)
			seen[n.RawURI] = true
		} else {
			removed++
		}
	}
	nodeList = kept
	saveNodesUnsafe()
	return removed
}

func DeleteDisabled() int {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	var kept []Node
	removed := 0
	for _, n := range nodeList {
		if !n.Disabled {
			kept = append(kept, n)
		} else {
			removed++
			delete(healthMap, n.RawURI)
		}
	}
	nodeList = kept
	saveNodesUnsafe()
	saveHealthUnsafe()
	return removed
}

func RecordTest(uri string, ok bool, ms float64, errStr string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	h, exists := healthMap[uri]
	if !exists {
		h = &NodeHealth{}
		healthMap[uri] = h
	}
	h.LastTestMs = ms
	h.LastTestError = errStr
	if ok {
		h.SuccessCount++
		h.ConsecutiveFailures = 0
		h.LastSuccessAt = time.Now().Unix()
		h.CooldownUntil = 0
	} else {
		h.FailCount++
		h.ConsecutiveFailures++
		h.LastFailAt = time.Now().Unix()
		h.CooldownUntil = time.Now().Unix() + 300
	}
	saveNodesUnsafe()
	saveHealthUnsafe()
}

type scoredNode struct {
	node  Node
	score float64
}

func SelectForParallel(k int) []Node {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	now := time.Now().Unix()
	var scored []scoredNode
	for _, n := range nodeList {
		if n.Disabled {
			continue
		}
		h := healthMap[n.RawURI]
		if h != nil && h.CooldownUntil > now {
			continue
		}
		score := 100.0
		if h != nil {
			score += math.Min(float64(h.SuccessCount), 100) * 3
			score -= math.Min(float64(h.FailCount), 100) * 4
			score -= float64(h.ConsecutiveFailures) * 25
			if h.LastTestMs > 0 {
				score -= math.Min(h.LastTestMs/1000.0, 30.0)
			}
			if h.LastSuccessAt == 0 {
				score += 20
			}
		} else {
			score += 20
		}
		scored = append(scored, scoredNode{n, math.Max(1.0, score)})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if len(scored) > k*3 {
		scored = scored[:k*3]
	}
	var selected []Node
	for i := 0; i < k && len(scored) > 0; i++ {
		idx := rand.Intn(len(scored)) // 简单随机回退
		selected = append(selected, scored[idx].node)
		scored = append(scored[:idx], scored[idx+1:]...)
	}
	log.Printf("[Nodes] 选择并行节点 (需求: %d, 实际: %d)", k, len(selected))
	return selected
}
