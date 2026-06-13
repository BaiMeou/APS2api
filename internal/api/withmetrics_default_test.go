//go:build !serveropt

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bsfdsagfadg/vertex/internal/metrics"
)

// TestWithMetricsLite 验证 withMetrics 中间件在精简（空指标）构建下的与指标无关行为：
// 设 X-Request-Id、注入 context、跳过 /health。不断言计数（空采集器恒为零值）。
func TestWithMetricsLite(t *testing.T) {
	s := &Server{metrics: metrics.New(10)}

	var seenReqID string
	ok := s.withMetrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenReqID = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	ok.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("应设置 X-Request-Id 响应头")
	}
	if seenReqID == "" || seenReqID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("context 里的 request-id 应与响应头一致，got ctx=%q header=%q", seenReqID, rec.Header().Get("X-Request-Id"))
	}

	// /health 应被跳过，不设 request-id。
	rec2 := httptest.NewRecorder()
	s.withMetrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec2, httptest.NewRequest("GET", "/health", nil))
	if rec2.Header().Get("X-Request-Id") != "" {
		t.Fatal("/health 不应设 X-Request-Id")
	}

	// 空采集器 Snapshot 恒为零值。
	if s.metrics.Snapshot().Total != 0 {
		t.Fatalf("精简构建下空采集器 total 应恒为 0，got %d", s.metrics.Snapshot().Total)
	}
}
