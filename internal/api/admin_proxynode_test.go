//go:build proxynode

package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsfdsagfadg/vertex/internal/config"
)

// newProxyTestEnv 隔离配置到临时文件，返回一个可调用代理端点的 Server。
func newProxyTestEnv(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("VPROXY_CONFIG", cfgPath)
	t.Setenv("PROXY_URL", "")
	config.InvalidateCache()
	return &Server{}
}

// TestAdminProxyCRUD 验证 proxynode 代理池端点：列表 → 新增 → 拒绝重复/非法 → 删除 → 越界 404。
func TestAdminProxyCRUD(t *testing.T) {
	s := newProxyTestEnv(t)

	// 初始为空。
	rec := httptest.NewRecorder()
	s.adminGetProxies(rec, httptest.NewRequest("GET", "/api/admin/proxies", nil))
	if body := rec.Body.String(); !strings.Contains(body, `"proxies":[]`) {
		t.Fatalf("初始代理池应为空，got %s", body)
	}

	// 新增两个合法代理。
	for _, u := range []string{"http://p1:1080", "socks5://p2:1081"} {
		rec := httptest.NewRecorder()
		s.adminAddProxy(rec, httptest.NewRequest("POST", "/api/admin/proxies", strings.NewReader(`{"url":"`+u+`"}`)))
		if rec.Code != 200 {
			t.Fatalf("新增 %s 应成功，got %d body=%s", u, rec.Code, rec.Body.String())
		}
	}
	if got := config.ProxyPoolList(); len(got) != 2 {
		t.Fatalf("应有 2 个代理，got %v", got)
	}

	// 重复新增被拒。
	rec = httptest.NewRecorder()
	s.adminAddProxy(rec, httptest.NewRequest("POST", "/api/admin/proxies", strings.NewReader(`{"url":"http://p1:1080"}`)))
	if rec.Code != 400 {
		t.Fatalf("重复代理应 400，got %d", rec.Code)
	}

	// 非法地址（无 scheme）被拒。
	rec = httptest.NewRecorder()
	s.adminAddProxy(rec, httptest.NewRequest("POST", "/api/admin/proxies", strings.NewReader(`{"url":"justhost:1080"}`)))
	if rec.Code != 400 {
		t.Fatalf("非法代理地址应 400，got %d", rec.Code)
	}

	// 删除下标 0 → 剩 1 个。
	rec = httptest.NewRecorder()
	s.adminDeleteProxy(rec, httptest.NewRequest("DELETE", "/api/admin/proxies/0", nil), "0")
	if rec.Code != 200 {
		t.Fatalf("删除下标 0 应成功，got %d body=%s", rec.Code, rec.Body.String())
	}
	got := config.ProxyPoolList()
	if len(got) != 1 || got[0] != "socks5://p2:1081" {
		t.Fatalf("删除后应剩 socks5://p2:1081，got %v", got)
	}

	// 删除越界下标 → 404。
	rec = httptest.NewRecorder()
	s.adminDeleteProxy(rec, httptest.NewRequest("DELETE", "/api/admin/proxies/5", nil), "5")
	if rec.Code != 404 {
		t.Fatalf("越界下标应 404，got %d", rec.Code)
	}
}

// TestAdminProxyRouteDispatch 验证 adminProxyRoute 仅命中 /proxies 与 /proxies/{idx}，其余放行（返回 false）。
func TestAdminProxyRouteDispatch(t *testing.T) {
	s := newProxyTestEnv(t)

	if !s.adminProxyRoute(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/admin/proxies", nil), "/proxies") {
		t.Fatal("/proxies 应被代理路由命中")
	}
	if !s.adminProxyRoute(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/api/admin/proxies/0", nil), "/proxies/0") {
		t.Fatal("/proxies/0 应被代理路由命中")
	}
	if s.adminProxyRoute(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/admin/settings", nil), "/settings") {
		t.Fatal("/settings 不应被代理路由命中（应返回 false 放行给主分发器）")
	}
}

// TestValidProxyURL 验证代理地址校验：支持的 scheme 通过、其它拒绝。
func TestValidProxyURL(t *testing.T) {
	ok := []string{"http://h:1", "https://h:1", "socks5://h:1", "socks5h://h:1"}
	bad := []string{"", "host:1080", "ftp://h:1", "://h", "http://"}
	for _, u := range ok {
		if !validProxyURL(u) {
			t.Errorf("%q 应为合法代理地址", u)
		}
	}
	for _, u := range bad {
		if validProxyURL(u) {
			t.Errorf("%q 应为非法代理地址", u)
		}
	}
}
