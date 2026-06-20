// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package nodes

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
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
	mu                 sync.Mutex
	nodeList           []Node
	healthMap          = make(map[string]*NodeHealth)
	loaded             bool
	DeleteNodeCallback func(uri string)
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
		_ = json.Unmarshal(b, &d)
		nodeList = d.Nodes
	}
	if b, err := os.ReadFile(filepath.Join(fileDir(), "node_health.json")); err == nil {
		_ = json.Unmarshal(b, &healthMap)
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
	_ = os.WriteFile(filepath.Join(fileDir(), "nodes.json"), b, 0644)
}

func saveHealthUnsafe() {
	b, _ := json.MarshalIndent(healthMap, "", "  ")
	_ = os.WriteFile(filepath.Join(fileDir(), "node_health.json"), b, 0644)
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
	if DeleteNodeCallback != nil {
		DeleteNodeCallback(uri)
	}
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
	if DeleteNodeCallback != nil {
		for _, u := range uris {
			DeleteNodeCallback(u)
		}
	}
}

func DedupNodes() int {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	keepMap := make(map[string]bool)
	var kept []Node
	removed := 0
	for _, n := range nodeList {
		key := n.RawURI
		if scheme, userinfo, host, port, ok := parseNodeIdentity(n.RawURI); ok {
			key = scheme + "://" + userinfo + "@" + host + ":" + strconv.Itoa(port)
		}
		if !keepMap[key] {
			keepMap[key] = true
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

func GetNodeName(uri string) string {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	for _, n := range nodeList {
		if n.RawURI == uri {
			return n.Name
		}
	}
	return "Unknown"
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

func padB64(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "-", "+"), "_", "/")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return s
}

func parseNodeIdentity(rawURI string) (scheme, userinfo, host string, port int, ok bool) {
	if strings.HasPrefix(rawURI, "vmess://") {
		b64Str := rawURI[8:]
		if idx := strings.Index(b64Str, "?"); idx != -1 {
			b64Str = b64Str[:idx]
		}
		if idx := strings.Index(b64Str, "#"); idx != -1 {
			b64Str = b64Str[:idx]
		}
		b64Str = padB64(b64Str)
		if b, err := base64.StdEncoding.DecodeString(b64Str); err == nil {
			var d map[string]any
			if err := json.Unmarshal(b, &d); err == nil {
				id, _ := d["id"].(string)
				add, _ := d["add"].(string)
				portStr := fmt.Sprintf("%v", d["port"])
				p, _ := strconv.Atoi(portStr)
				return "vmess", id, add, p, true
			}
		}
		return "", "", "", 0, false
	}
	if strings.HasPrefix(rawURI, "ss://") {
		body := rawURI[5:]
		if idx := strings.Index(body, "#"); idx != -1 {
			body = body[:idx]
		}
		if idx := strings.Index(body, "@"); idx != -1 {
			b, err := base64.StdEncoding.DecodeString(padB64(body[:idx]))
			if err == nil {
				parts := strings.SplitN(string(b), ":", 2)
				hp := strings.Split(body[idx+1:], ":")
				p, _ := strconv.Atoi(hp[1])
				return "ss", parts[0] + ":" + parts[1], hp[0], p, true
			}
		}
		return "", "", "", 0, false
	}
	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", "", 0, false
	}
	scheme = u.Scheme
	userinfo = u.User.Username()
	host = u.Hostname()
	port, _ = strconv.Atoi(u.Port())
	if port == 0 {
		port = 443
	}
	return scheme, userinfo, host, port, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
		failures := maxInt(1, h.ConsecutiveFailures)
		cooldown := minInt(1800, 30*(1<<minInt(failures-1, 6)))
		h.CooldownUntil = time.Now().Unix() + int64(cooldown)
	}
	saveNodesUnsafe()
	saveHealthUnsafe()
}

func UpdateNodeTestResult(uri string, ok bool, ms float64, errStr string) {
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
		failures := maxInt(1, h.ConsecutiveFailures)
		cooldown := minInt(1800, 30*(1<<minInt(failures-1, 6)))
		h.CooldownUntil = time.Now().Unix() + int64(cooldown)
	}
	saveNodesUnsafe()
	saveHealthUnsafe()
}

func EnableNode(uri string) bool {
	mu.Lock()
	defer mu.Unlock()
	ensureLoaded()
	found := false
	for i, n := range nodeList {
		if n.RawURI == uri {
			nodeList[i].Disabled = false
			if h, exists := healthMap[uri]; exists {
				h.CooldownUntil = 0
			}
			found = true
			break
		}
	}
	if found {
		saveNodesUnsafe()
		saveHealthUnsafe()
	}
	return found
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
	var cooldownNodes []scoredNode
	for _, n := range nodeList {
		if n.Disabled {
			continue
		}
		h := healthMap[n.RawURI]
		if h != nil && h.CooldownUntil > now {
			cooldownNodes = append(cooldownNodes, scoredNode{n, float64(h.CooldownUntil)})
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
			lastSeen := maxInt64(h.LastSuccessAt, h.LastFailAt)
			if lastSeen == 0 {
				score += 20
			} else if now-lastSeen > 3600 {
				score += 10
			}
		} else {
			score += 20
		}
		scored = append(scored, scoredNode{n, math.Max(1.0, score)})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if len(scored) < k && len(cooldownNodes) > 0 {
		sort.Slice(cooldownNodes, func(i, j int) bool { return cooldownNodes[i].score < cooldownNodes[j].score })
		needed := k - len(scored)
		if needed > len(cooldownNodes) {
			needed = len(cooldownNodes)
		}
		scored = append(scored, cooldownNodes[:needed]...)
	}
	topK := config.Load().ParallelNodeTopK
	if topK <= 0 {
		topK = 80
	}
	if len(scored) > topK {
		scored = scored[:topK]
	}
	weights := make([]float64, len(scored))
	totalWeight := 0.0
	for i, s := range scored {
		w := s.score + 120.0
		if w < 1 {
			w = 1
		}
		weights[i] = w
		totalWeight += w
	}
	var selected []Node
	for i := 0; i < k && len(scored) > 0; i++ {
		r := rand.Float64() * totalWeight
		idx := len(weights) - 1
		for j, w := range weights {
			r -= w
			if r <= 0 {
				idx = j
				break
			}
		}
		selected = append(selected, scored[idx].node)
		totalWeight -= weights[idx]
		weights = append(weights[:idx], weights[idx+1:]...)
		scored = append(scored[:idx], scored[idx+1:]...)
	}
	log.Printf("[Nodes] 选择并行节点 (需求: %d, 实际: %d)", k, len(selected))
	return selected
}
