// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
	"github.com/bsfdsagfadg/vertex/internal/transport"
)

func (s *Server) adminGetNodes(w http.ResponseWriter, _ *http.Request) {
	list := nodes.LoadNodes()
	var enabledCount, disabledCount int
	for _, n := range list {
		if n.Disabled {
			disabledCount++
		} else {
			enabledCount++
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"nodes":          list,
		"health":         nodes.LoadHealth(),
		"total":          len(list),
		"enabled_count":  enabledCount,
		"disabled_count": disabledCount,
	})
}

func (s *Server) adminFetchSub(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	log.Printf("[Admin] [FetchSub] 开始拉取订阅 URL: %s", body.URL)
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", body.URL, nil)
	if err != nil {
		log.Printf("[Admin] [FetchSub] 创建拉取请求失败: %v", err)
		s.writeJSON(w, http.StatusBadRequest, adminErr("创建请求失败: "+err.Error()))
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Admin] [FetchSub] 发送拉取请求失败: %v", err)
		s.writeJSON(w, http.StatusBadRequest, adminErr("拉取失败: "+err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[Admin] [FetchSub] 服务器返回非200状态码: %d", resp.StatusCode)
		s.writeJSON(w, http.StatusBadRequest, adminErr("拉取失败: 服务器返回状态码 "+strconv.Itoa(resp.StatusCode)))
		return
	}
	data, _ := io.ReadAll(resp.Body)
	text := strings.TrimSpace(string(data))

	var lines []string
	if strings.Contains(text, "proxies:") {
		log.Printf("[Admin] [FetchSub] 识别为 Clash YAML 格式")
		lines = parseClashYamlToURIs(text)
	} else {
		if b, err := decodeSubBase64(text); err == nil {
			log.Printf("[Admin] [FetchSub] 识别并解密了 Base64 订阅内容")
			text = string(b)
		} else {
			log.Printf("[Admin] [FetchSub] 内容未采用 Base64 或解密失败，尝试直接按行解析")
		}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				lines = append(lines, line)
			}
		}
	}

	var newNodes []nodes.Node
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if out, err := transport.ParseURI(line); err == nil {
			t, _ := out["type"].(string)
			// 提取节点名
			nodeName := ""
			if strings.HasPrefix(line, "vmess://") {
				// vmess 解密出来的 json 中有 ps 字段，也就是节点名
				b64Str := line[8:]
				if idx := strings.Index(b64Str, "?"); idx != -1 {
					b64Str = b64Str[:idx]
				}
				if idx := strings.Index(b64Str, "#"); idx != -1 {
					b64Str = b64Str[:idx]
				}
				b64Str = strings.ReplaceAll(strings.ReplaceAll(b64Str, "-", "+"), "_", "/")
				if pad := len(b64Str) % 4; pad != 0 {
					b64Str += strings.Repeat("=", 4-pad)
				}
				if b, err := base64.StdEncoding.DecodeString(b64Str); err == nil {
					var d map[string]any
					if err := json.Unmarshal(b, &d); err == nil {
						if ps, ok := d["ps"].(string); ok && ps != "" {
							nodeName = ps
						}
					}
				}
			} else {
				// ss, vless, trojan, hysteria2, tuic 等节点，通过 # 分割后面的节点名
				if idx := strings.Index(line, "#"); idx != -1 {
					escapedName := line[idx+1:]
					if dec, err := url.QueryUnescape(escapedName); err == nil {
						nodeName = dec
					} else {
						nodeName = escapedName
					}
				}
			}

			if nodeName == "" {
				if n, ok := out["name"].(string); ok {
					nodeName = n
				}
			}
			if nodeName == "" {
				nodeName = line[:min(len(line), 40)]
			}
			out["name"] = nodeName
			newNodes = append(newNodes, nodes.Node{Type: t, Name: nodeName, RawURI: line})
		}
	}
	nodes.MergeNodes(newNodes)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(newNodes)})
}

func (s *Server) adminTestAll(w http.ResponseWriter, _ *http.Request) {
	log.Printf("[Admin] [TestAll] 开始触发全局并发测速（基于 recaptchaToken 耗时）")
	go func() {
		// 使用后台独立 Context 避免 http handler 返回后 context 被取消
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		list := nodes.LoadNodes()
		log.Printf("[Admin] [TestAll] 加载到节点总数: %d", len(list))

		var wg sync.WaitGroup
		sem := make(chan struct{}, 10) // 并发度限制为 10，保护网络不瞬间过载

		for _, n := range list {
			if n.Disabled {
				log.Printf("[Admin] [TestAll] 节点已被禁用，跳过测试: %s", n.Name)
				continue
			}
			wg.Add(1)
			go func(node nodes.Node) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()

				start := time.Now()
				log.Printf("[Admin] [TestAll] 开始测试节点: %s (%s)", node.Name, node.Type)

				// 1. 创建节点专用的 Session
				sess, err := s.vc.Net().CreateSession(15, node.RawURI, "admin-test-all")
				var testErr error
				if err == nil {
					// 2. 模拟 recaptcha 完整的 token 获取流程，将其作为获取速度的实际指标
					testErr = fetchRecaptchaTokenWithSess(ctx, sess)
					sess.Close()
				} else {
					testErr = err
				}

				duration := float64(time.Since(start).Milliseconds())
				if testErr != nil {
					log.Printf("[Admin] [TestAll] 节点 %s 测试失败: %v, 耗时: %.0fms", node.Name, testErr, duration)
				} else {
					log.Printf("[Admin] [TestAll] 节点 %s 测试成功, recaptcha 耗时: %.0fms", node.Name, duration)
				}
				success := testErr == nil
				nodes.RecordTest(node.RawURI, success, duration, errToStr(testErr))
				if !success {
					nodes.BatchUpdateNodesDisabled([]string{node.RawURI}, true)
				}
			}(n)
		}
		wg.Wait()
		log.Printf("[Admin] [TestAll] 全局节点测试全部结束")
	}()
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminTestNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RawURI         string  `json:"raw_uri"`
		AutoDisable    bool    `json:"auto_disable"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	if body.TimeoutSeconds <= 0 {
		body.TimeoutSeconds = 25
	}
	timeout := time.Duration(body.TimeoutSeconds * float64(time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	start := time.Now()
	sess, err := s.vc.Net().CreateSession(15, body.RawURI, "admin-test-node")
	var testErr error
	if err == nil {
		testErr = fetchRecaptchaTokenWithSess(ctx, sess)
		sess.Close()
	} else {
		testErr = err
	}
	elapsed := float64(time.Since(start).Milliseconds())

	errStr := ""
	ok := testErr == nil
	if testErr != nil {
		if ctx.Err() != nil || errors.Is(testErr, context.DeadlineExceeded) {
			errStr = "timeout"
		} else {
			errStr = testErr.Error()
		}
	}

	disabled := false
	if body.AutoDisable {
		nodes.UpdateNodeTestResult(body.RawURI, ok, elapsed, errStr)
		disabled = !ok
		if !ok {
			nodes.BatchUpdateNodesDisabled([]string{body.RawURI}, true)
		}
	}

	log.Printf("[Admin] [TestNode] 节点测试 %s: ok=%v elapsed=%.0fms error=%q disabled=%v", nodes.GetNodeName(body.RawURI), ok, elapsed, errStr, disabled)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":         ok,
		"elapsed_ms": elapsed,
		"error":      errStr,
		"disabled":   disabled,
	})
}

func (s *Server) adminEnableNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RawURI string `json:"raw_uri"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	ok := nodes.EnableNode(body.RawURI)
	log.Printf("[Admin] [EnableNode] 启用节点 %s: %v", nodes.GetNodeName(body.RawURI), ok)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}

// 模拟实测 recaptchaToken
func fetchRecaptchaTokenWithSess(ctx context.Context, sess *transport.Session) error {
	const (
		recaptchaBase = "https://www.google.com"
		siteKey       = "6LdCjtspAAAAAMcV4TGdWLJqRTEk1TfpdLqEnKdj"
		recaptchaCo   = "aHR0cHM6Ly9jb25zb2xlLmNsb3VkLmdvb2dsZS5jb206NDQz"
		recaptchaHl   = "zh-CN"
		recaptchaV    = "jdMmXeCQEkPbnFDy9T04NbgJ"
		recaptchaVh   = "6581054572"
		randomCharset = "abcdefghijklmnopqrstuvwxyz0123456789"
	)
	var (
		tokenRe = regexp.MustCompile(`id="recaptcha-token"[^>]*value="([^"]+)"`)
		rrespRe = regexp.MustCompile(`rresp","(.*?)"`)
	)

	// 随机生成 10 位回调参数
	b := make([]byte, 10)
	for i := range b {
		b[i] = randomCharset[time.Now().UnixNano()%int64(len(randomCharset))]
	}
	cb := string(b)

	anchorURL := fmt.Sprintf(
		"%s/recaptcha/enterprise/anchor?ar=1&k=%s&co=%s&hl=%s&v=%s&size=invisible&anchor-ms=20000&execute-ms=15000&cb=%s",
		recaptchaBase, siteKey, recaptchaCo, recaptchaHl, recaptchaV, cb,
	)

	_, anchorBody, err := sess.DoAndRead(ctx, "GET", anchorURL, transport.AnchorHeaders(), nil)
	if err != nil {
		return fmt.Errorf("GET anchor 失败: %w", err)
	}
	m := tokenRe.FindSubmatch(anchorBody)
	if m == nil {
		return fmt.Errorf("从 anchor HTML 解析 recaptcha-token 失败")
	}
	baseToken := string(m[1])

	form := url.Values{
		"v":      {recaptchaV},
		"reason": {"q"},
		"k":      {siteKey},
		"c":      {baseToken},
		"co":     {recaptchaCo},
		"hl":     {recaptchaHl},
		"size":   {"invisible"},
		"vh":     {recaptchaVh},
		"chr":    {""},
		"bg":     {""},
	}
	reloadURL := recaptchaBase + "/recaptcha/enterprise/reload?k=" + siteKey
	header := transport.XHRHeaders(
		"application/x-www-form-urlencoded;charset=UTF-8", "*/*",
		recaptchaBase, anchorURL, "same-origin",
	)

	_, reloadBody, err := sess.DoAndRead(ctx, "POST", reloadURL, header, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("POST reload 失败: %w", err)
	}
	rm := rrespRe.FindSubmatch(reloadBody)
	if rm == nil {
		return fmt.Errorf("从 reload 响应解析 rresp 失败")
	}
	return nil
}

func (s *Server) adminDedupNodes(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed_count": nodes.DedupNodes()})
}

func (s *Server) adminDeleteDisabledNodes(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted_count": nodes.DeleteDisabled()})
}

func (s *Server) adminImportNodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text    string `json:"text"`
		Replace bool   `json:"replace"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	log.Printf("[Admin] [ImportNodes] 收到优选节点文件导入请求, 替换模式: %v", body.Replace)

	text := strings.TrimSpace(body.Text)
	var lines []string
	if strings.Contains(text, "proxies:") {
		lines = parseClashYamlToURIs(text)
	} else {
		if b, err := decodeSubBase64(text); err == nil {
			text = string(b)
		}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				lines = append(lines, line)
			}
		}
	}

	var newNodes []nodes.Node
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if out, err := transport.ParseURI(line); err == nil {
			t, _ := out["type"].(string)
			nodeName := ""
			if strings.HasPrefix(line, "vmess://") {
				b64Str := line[8:]
				if idx := strings.Index(b64Str, "?"); idx != -1 {
					b64Str = b64Str[:idx]
				}
				if idx := strings.Index(b64Str, "#"); idx != -1 {
					b64Str = b64Str[:idx]
				}
				b64Str = strings.ReplaceAll(strings.ReplaceAll(b64Str, "-", "+"), "_", "/")
				if pad := len(b64Str) % 4; pad != 0 {
					b64Str += strings.Repeat("=", 4-pad)
				}
				if b, err := base64.StdEncoding.DecodeString(b64Str); err == nil {
					var d map[string]any
					if err := json.Unmarshal(b, &d); err == nil {
						if ps, ok := d["ps"].(string); ok && ps != "" {
							nodeName = ps
						}
					}
				}
			} else {
				if idx := strings.Index(line, "#"); idx != -1 {
					escapedName := line[idx+1:]
					if dec, err := url.QueryUnescape(escapedName); err == nil {
						nodeName = dec
					} else {
						nodeName = escapedName
					}
				}
			}

			if nodeName == "" {
				nodeName = line[:min(len(line), 40)]
			}
			newNodes = append(newNodes, nodes.Node{Type: t, Name: nodeName, RawURI: line})
		}
	}

	if body.Replace {
		// 先清除所有节点
		log.Printf("[Admin] [ImportNodes] 替换模式，正在清除全部已有候选节点")
		allCurrent := nodes.LoadNodes()
		for _, cn := range allCurrent {
			nodes.DeleteNode(cn.RawURI)
		}
	}

	log.Printf("[Admin] [ImportNodes] 正在合并导入的新节点数量: %d", len(newNodes))
	nodes.MergeNodes(newNodes)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(newNodes)})
}

func (s *Server) adminUseNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RawURI string `json:"raw_uri"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	_ = config.WriteSettings(map[string]any{"active_node_uri": body.RawURI, "parallel_pool_enabled": false})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminDeleteNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RawURI string `json:"raw_uri"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	nodes.DeleteNode(body.RawURI)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminBatchDisableNodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URIs []string `json:"uris"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	log.Printf("[Admin] [BatchDisable] 批量禁用 %d 个节点", len(body.URIs))
	nodes.BatchUpdateNodesDisabled(body.URIs, true)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminBatchEnableNodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URIs []string `json:"uris"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	log.Printf("[Admin] [BatchEnable] 批量启用 %d 个节点", len(body.URIs))
	nodes.BatchUpdateNodesDisabled(body.URIs, false)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminBatchDeleteNodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URIs []string `json:"uris"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	log.Printf("[Admin] [BatchDelete] 批量删除 %d 个节点", len(body.URIs))
	nodes.BatchDeleteNodes(body.URIs)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func errToStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// decodeSubBase64 宽容解码订阅的 Base64 文本，兼容各种换行、空格及 URL 安全格式
func decodeSubBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, " ", "")
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	t := strings.ReplaceAll(strings.ReplaceAll(s, "-", "+"), "_", "/")
	if pad := len(t) % 4; pad != 0 {
		t += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(t)
}

// parseClashYamlToURIs 解析极简 yaml 格式，提取 proxies 列表并转换为 URI 节点
func parseClashYamlToURIs(yamlText string) []string {
	var uris []string

	// 我们按行流式扫描 proxies 字段
	lines := strings.Split(yamlText, "\n")
	inProxies := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// 判断 proxies 的开始和结束
		if strings.HasPrefix(trimmed, "proxies:") {
			inProxies = true
			continue
		}

		// 如果遇到了其他顶层字段（非缩进），则 proxies 块结束
		if inProxies && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.Contains(trimmed, ":") {
			inProxies = false
		}

		if !inProxies {
			continue
		}

		// 支持单行简写形式：
		// - {name: "xxx", type: ss, server: "xxx", port: 123, cipher: "aes-256-gcm", password: "xxx"}
		// - {name: xxx, server: xxx, port: xxx, type: vmess, uuid: xxx, ...}
		if strings.HasPrefix(trimmed, "- {") && strings.HasSuffix(trimmed, "}") {
			cleaned := trimmed[3 : len(trimmed)-1]
			// 我们写一个健壮的 key-value 解析器
			attrs := parseInlineYamlAttrs(cleaned)
			if uri := clashProxyToURI(attrs); uri != "" {
				uris = append(uris, uri)
			}
			continue
		}

		// 支持标准缩进列表形式（暂且不用处理复杂的 yaml 嵌套，dev 内全部是单行简写形式，
		// 但为了健壮性，我们可以只处理 {} 单行形式）
	}
	return uris
}

func parseInlineYamlAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	var currentKey, currentValue strings.Builder
	inQuotes := false
	var quoteChar rune
	isKey := true
	braceDepth := 0
	bracketDepth := 0

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inQuotes {
			if r == quoteChar {
				inQuotes = false
			} else if r == '\\' && i+1 < len(runes) {
				if isKey {
					currentKey.WriteRune(runes[i+1])
				} else {
					currentValue.WriteRune(runes[i+1])
				}
				i++
			} else {
				if isKey {
					currentKey.WriteRune(r)
				} else {
					currentValue.WriteRune(r)
				}
			}
			continue
		}

		if r == '"' || r == '\'' {
			inQuotes = true
			quoteChar = r
			continue
		}

		if isKey {
			if r == ':' {
				isKey = false
				// 略过冒号后的空格
				if i+1 < len(runes) && runes[i+1] == ' ' {
					i++
				}
			} else if r != ' ' && r != '\t' {
				currentKey.WriteRune(r)
			}
		} else {
			switch r {
			case '{':
				braceDepth++
				currentValue.WriteRune(r)
			case '}':
				if braceDepth > 0 {
					braceDepth--
				}
				currentValue.WriteRune(r)
			case '[':
				bracketDepth++
				currentValue.WriteRune(r)
			case ']':
				if bracketDepth > 0 {
					bracketDepth--
				}
				currentValue.WriteRune(r)
			case ',':
				if braceDepth > 0 || bracketDepth > 0 {
					currentValue.WriteRune(r)
					continue
				}
				key := strings.TrimSpace(currentKey.String())
				val := strings.TrimSpace(currentValue.String())
				if key != "" {
					attrs[key] = val
				}
				currentKey.Reset()
				currentValue.Reset()
				isKey = true
			default:
				currentValue.WriteRune(r)
			}
		}
	}

	// 最后一个 key-value
	key := strings.TrimSpace(currentKey.String())
	val := strings.TrimSpace(currentValue.String())
	if key != "" {
		attrs[key] = val
	}

	return attrs
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseInlineYamlObject(s string) map[string]string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") && len(trimmed) >= 2 {
		trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}
	if trimmed == "" {
		return map[string]string{}
	}
	return parseInlineYamlAttrs(trimmed)
}

func buildProxyURI(scheme, credential, server, port, name string, query url.Values) string {
	u := &url.URL{
		Scheme:   scheme,
		User:     url.User(credential),
		Host:     net.JoinHostPort(server, port),
		Fragment: name,
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func clashProxyToURI(attrs map[string]string) string {
	typ := strings.ToLower(strings.TrimSpace(attrs["type"]))
	name := attrs["name"]
	server := attrs["server"]
	port := attrs["port"]

	if server == "" || port == "" {
		return ""
	}

	switch typ {
	case "ss":
		cipher := attrs["cipher"]
		password := attrs["password"]
		if cipher == "" || password == "" {
			return ""
		}
		// ss 格式: ss://base64(cipher:password)@server:port#name
		userinfo := base64.StdEncoding.EncodeToString([]byte(cipher + ":" + password))
		return "ss://" + userinfo + "@" + server + ":" + port + "#" + url.QueryEscape(name)

	case "vmess":
		uuid := attrs["uuid"]
		alterIdStr := attrs["alterId"]
		if alterIdStr == "" {
			alterIdStr = "0"
		}
		alterId, _ := strconv.Atoi(alterIdStr)

		tlsEnabled := false
		if attrs["tls"] == "true" {
			tlsEnabled = true
		}

		// 构造 vmess json 结构
		vmessJSON := map[string]any{
			"v":    "2",
			"ps":   name,
			"add":  server,
			"port": port,
			"id":   uuid,
			"aid":  alterId,
			"net":  "tcp",
			"type": "none",
			"host": "",
			"path": "",
			"tls":  "",
		}

		if attrs["network"] == "ws" {
			vmessJSON["net"] = "ws"
			if wsOpts, ok := attrs["ws-opts"]; ok {
				// 提取 path 和 Host
				// 极简提取 ws-opts 中的 path 和 headers
				path := "/"
				if idx := strings.Index(wsOpts, "path:"); idx != -1 {
					sub := wsOpts[idx+5:]
					if commaIdx := strings.Index(sub, ","); commaIdx != -1 {
						sub = sub[:commaIdx]
					}
					path = strings.Trim(strings.TrimSpace(sub), "\"'{}")
				}
				vmessJSON["path"] = path

				host := ""
				if idx := strings.Index(wsOpts, "Host:"); idx != -1 {
					sub := wsOpts[idx+5:]
					if commaIdx := strings.Index(sub, ","); commaIdx != -1 {
						sub = sub[:commaIdx]
					}
					if braceIdx := strings.Index(sub, "}"); braceIdx != -1 {
						sub = sub[:braceIdx]
					}
					host = strings.Trim(strings.TrimSpace(sub), "\"'{}")
				}
				vmessJSON["host"] = host
			}
		}

		if tlsEnabled {
			vmessJSON["tls"] = "tls"
		}

		jsonBytes, _ := json.Marshal(vmessJSON)
		b64Str := base64.StdEncoding.EncodeToString(jsonBytes)
		return "vmess://" + b64Str

	case "vless":
		uuid := attrs["uuid"]
		if uuid == "" {
			return ""
		}

		query := url.Values{}
		serverName := firstNonEmpty(attrs["servername"], attrs["sni"], server)
		realityOpts := parseInlineYamlObject(attrs["reality-opts"])
		if len(realityOpts) > 0 {
			query.Set("security", "reality")
			if publicKey := realityOpts["public-key"]; publicKey != "" {
				query.Set("pbk", publicKey)
			}
			if shortID := realityOpts["short-id"]; shortID != "" {
				query.Set("sid", shortID)
			}
		} else if isTruthy(attrs["tls"]) {
			query.Set("security", "tls")
		}
		if serverName != "" {
			query.Set("sni", serverName)
		}
		if isTruthy(attrs["skip-cert-verify"]) {
			query.Set("allowInsecure", "1")
		}
		if flow := attrs["flow"]; flow != "" {
			query.Set("flow", flow)
		}
		if fp := attrs["client-fingerprint"]; fp != "" {
			query.Set("fp", fp)
		}
		if network := strings.ToLower(strings.TrimSpace(attrs["network"])); network != "" {
			query.Set("type", network)
			switch network {
			case "ws":
				wsOpts := parseInlineYamlObject(attrs["ws-opts"])
				if path := wsOpts["path"]; path != "" {
					query.Set("path", path)
				}
				headers := parseInlineYamlObject(wsOpts["headers"])
				if host := firstNonEmpty(headers["Host"], headers["host"]); host != "" {
					query.Set("host", host)
				}
			case "grpc":
				grpcOpts := parseInlineYamlObject(attrs["grpc-opts"])
				if serviceName := firstNonEmpty(grpcOpts["grpc-service-name"], grpcOpts["serviceName"]); serviceName != "" {
					query.Set("serviceName", serviceName)
				}
			}
		}
		return buildProxyURI("vless", uuid, server, port, name, query)

	case "trojan":
		password := attrs["password"]
		if password == "" {
			return ""
		}

		query := url.Values{}
		if sni := firstNonEmpty(attrs["sni"], attrs["servername"], server); sni != "" {
			query.Set("sni", sni)
		}
		if isTruthy(attrs["skip-cert-verify"]) {
			query.Set("allowInsecure", "1")
		}
		if fp := attrs["client-fingerprint"]; fp != "" {
			query.Set("fp", fp)
		}
		if network := strings.ToLower(strings.TrimSpace(attrs["network"])); network != "" {
			query.Set("type", network)
			switch network {
			case "ws":
				wsOpts := parseInlineYamlObject(attrs["ws-opts"])
				if path := wsOpts["path"]; path != "" {
					query.Set("path", path)
				}
				headers := parseInlineYamlObject(wsOpts["headers"])
				if host := firstNonEmpty(headers["Host"], headers["host"]); host != "" {
					query.Set("host", host)
				}
			case "grpc":
				grpcOpts := parseInlineYamlObject(attrs["grpc-opts"])
				if serviceName := firstNonEmpty(grpcOpts["grpc-service-name"], grpcOpts["serviceName"]); serviceName != "" {
					query.Set("serviceName", serviceName)
				}
			}
		}
		return buildProxyURI("trojan", password, server, port, name, query)

	case "hysteria2", "hy2":
		password := attrs["password"]
		if password == "" {
			return ""
		}

		query := url.Values{}
		if sni := firstNonEmpty(attrs["sni"], attrs["servername"], server); sni != "" {
			query.Set("sni", sni)
		}
		if isTruthy(attrs["skip-cert-verify"]) {
			query.Set("insecure", "1")
		}
		if ports := firstNonEmpty(attrs["ports"], attrs["mport"]); ports != "" {
			query.Set("ports", ports)
		}
		if obfs := attrs["obfs"]; obfs != "" {
			query.Set("obfs", obfs)
		}
		if obfsPassword := attrs["obfs-password"]; obfsPassword != "" {
			query.Set("obfs-password", obfsPassword)
		}
		if fp := firstNonEmpty(attrs["client-fingerprint"], attrs["fingerprint"]); fp != "" {
			query.Set("fp", fp)
		}
		return buildProxyURI("hy2", password, server, port, name, query)
	}

	return ""
}
