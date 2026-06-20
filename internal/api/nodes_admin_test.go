// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsfdsagfadg/vertex/internal/nodes"
)

func TestParseClashYamlToURIs(t *testing.T) {
	yamlContent := `proxies:
  - {name: "🇭🇰 香港Y01", type: ss, server: "hk01-ae5", port: 443, cipher: aes-256-gcm, password: "password"}
`

	uris := parseClashYamlToURIs(string(yamlContent))
	if len(uris) == 0 {
		t.Fatalf("No URIs parsed")
	}

	// 验证所有节点都成功转换成了 URI
	if len(uris) != 1 {
		t.Errorf("Expected 1 URIs, but got %d", len(uris))
	}

	// 验证特定节点及其名称被正确转换且保留
	hasHK01 := false
	for _, u := range uris {
		if strings.Contains(u, "hk01-ae5") && strings.Contains(u, "%E9%A6%99%E6%B8%AFY01") {
			hasHK01 = true
			break
		}
	}
	if !hasHK01 {
		t.Errorf("Expected to find 香港Y01 node in parsed URIs: %v", uris)
	}
}

func TestAdminFetchSub_Yaml(t *testing.T) {
	yamlContent := []byte(`proxies:
  - {name: "🇭🇰 香港Y01", type: ss, server: "hk01-ae5", port: 443, cipher: aes-256-gcm, password: "password"}
`)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(yamlContent)
	}))
	defer ts.Close()

	nodes.DeleteDisabled()
	originalNodes := nodes.LoadNodes()

	s := &Server{}
	reqBody := `{"url": "` + ts.URL + `"}`
	req := httptest.NewRequest("POST", "/api/admin/subscriptions/fetch", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.adminFetchSub(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d, body: %s", resp.StatusCode, string(body))
	}

	var res map[string]any
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if res["ok"] != true {
		t.Errorf("Expected ok: true, got %v", res["ok"])
	}

	countVal, ok := res["count"].(float64)
	if !ok || countVal != 1 {
		t.Errorf("Expected count 1, got %v", res["count"])
	}

	loaded := nodes.LoadNodes()
	foundOriginal := len(originalNodes)
	if len(loaded) <= foundOriginal {
		t.Errorf("Expected nodes count to increase from %d, but got %d", foundOriginal, len(loaded))
	}

	// 校验新导入的节点，其名称必须是 Clash YAML 里的节点名
	hk01Found := false
	for _, node := range loaded {
		if strings.Contains(node.RawURI, "hk01-ae5") {
			if node.Name == "🇭🇰 香港Y01" {
				hk01Found = true
			} else {
				t.Errorf("HK01 node name is incorrect: %s, expected: 🇭🇰 香港Y01", node.Name)
			}
		}
	}
	if !hk01Found {
		t.Errorf("HK01 node not found in merged nodes list")
	}
}

func TestAdminFetchSub_Base64NodeTxt(t *testing.T) {
	// Base64 encoded: vmess://eyJhZGQiOiJqcDA1LXZtNSIsImFpZCI6IjAiLCJhbHBuIjoiIiwiaG9zdCI6IiIsImlkIjoiM2MzZjBiMmMtMzQ2YS00YjNiLTkxMWQtOGUyYjk1ZWVhZDdlIiwibmV0IjoidGNwIiwicGF0aCI6IiIsInBvcnQiOiI0NDMiLCJwcyI6IuaXpeacrFkwNSB8IOS4i+i9veS4k+eUqCB8IHgwLjAxIiwic2N5IjoiYXV0byIsInNuaSI6IiIsInRscyI6IiIsInR5cGUiOiJub25lIiwidiI6IjIifQ==
	content := []byte(`dm1lc3M6Ly9leUpoWkdRaU9pSnFjREExTFhadE5TSXNJbUZwWkNJNklqQWlMQ0poYkhCdUlqb2lJaXdpYUc5emRDSTZJaUlzSW1sa0lqb2lNMk16WmpCaU1tTXRNelEyWVMwMFlqTmlMVGt4TVdRdE9HVXlZamsxWldWaFpEZGxJaXdpYm1WMElqb2lkR053SWl3aWNHRjBhQ0k2SWlJc0luQnZjblFpT2lJME5ETWlMQ0p3Y3lJNkl1YVhwZWFjckZrd05TQjhJT1M0aStpOXZlUzRrK2VVcUNCOElIZ3dMakF4SWl3aWMyTjVJam9pWVhWMGJ5SXNJbk51YVNJNklpSXNJblJzY3lJNklpSXNJblI1Y0dVaU9pSnViMjVsSWl3aWRpSTZJaklpZlE9PQ==`)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer ts.Close()

	nodes.DeleteDisabled()
	originalNodes := nodes.LoadNodes()

	s := &Server{}
	reqBody := `{"url": "` + ts.URL + `"}`
	req := httptest.NewRequest("POST", "/api/admin/subscriptions/fetch", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.adminFetchSub(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d, body: %s", resp.StatusCode, string(body))
	}

	loaded := nodes.LoadNodes()
	foundOriginal := len(originalNodes)
	if len(loaded) <= foundOriginal {
		t.Errorf("Expected nodes count to increase from %d, but got %d", foundOriginal, len(loaded))
	}

	// 校验其中一个解密后有 ps 字段的 VMess 节点，验证导入后的节点名确实为文件里的节点名
	vmess05Found := false
	for _, node := range loaded {
		if strings.Contains(node.RawURI, "jp05-vm5") || strings.Contains(node.Name, "日本Y05") {
			if strings.Contains(node.Name, "日本Y05 | 下载专用 | x0.01") {
				vmess05Found = true
			} else {
				t.Errorf("VMess JP05 node name is incorrect: %s, expected: 日本Y05 | 下载专用 | x0.01", node.Name)
			}
		}
	}
	if !vmess05Found {
		t.Errorf("VMess JP05 node not found in merged nodes list")
	}
}

func TestAdminImportNodes(t *testing.T) {
	s := &Server{}

	// 准备测试用的 Clash YAML 内容
	yamlContent := `
proxies:
	 - {name: "🇺🇸 独享美西 [VMess] - Import", type: vmess, server: "us-import.xyz", port: 443, uuid: "uuid-12345"}
	 - {name: "🇯🇵 独享东京 [VLESS] - Import", type: vless, server: "jp-import.xyz", port: 443, uuid: "uuid-67890"}
`

	// 1. 追加模式测试 (replace = false)
	reqBody := map[string]any{
		"text":    yamlContent,
		"replace": false,
	}
	b, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/admin/nodes/import", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.adminImportNodes(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d, body: %s", resp.StatusCode, string(body))
	}

	var res map[string]any
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	if res["count"].(float64) != 2 {
		t.Errorf("Expected count 2, got %v", res["count"])
	}

	// 验证节点已导入
	loaded := nodes.LoadNodes()
	foundUS := false
	for _, n := range loaded {
		if strings.Contains(n.Name, "独享美西") {
			foundUS = true
			break
		}
	}
	if !foundUS {
		t.Errorf("Expected to find imported US node")
	}

	// 2. 覆盖模式测试 (replace = true)
	reqBodyReplace := map[string]any{
		"text":    yamlContent,
		"replace": true,
	}
	br, _ := json.Marshal(reqBodyReplace)
	reqR := httptest.NewRequest("POST", "/api/admin/nodes/import", strings.NewReader(string(br)))
	reqR.Header.Set("Content-Type", "application/json")
	wR := httptest.NewRecorder()

	s.adminImportNodes(wR, reqR)

	respR := wR.Result()
	defer func() { _ = respR.Body.Close() }()
	bodyR, _ := io.ReadAll(respR.Body)

	if respR.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d, body: %s", respR.StatusCode, string(bodyR))
	}

	// 在覆盖模式下，所有节点数必须被清空后重新替换，当前只有 2 个新导入的节点
	loadedR := nodes.LoadNodes()
	if len(loadedR) != 2 {
		t.Errorf("Expected exactly 2 nodes after replace import, but got %d", len(loadedR))
	}
}
