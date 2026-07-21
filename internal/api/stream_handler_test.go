package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/recaptcha"
	"github.com/bsfdsagfadg/vertex/internal/transform"
	"github.com/bsfdsagfadg/vertex/internal/vertex"
)

func TestSSEWriterLazyHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := newSSEWriter(rec, "text/event-stream")

	if sw.hasWritten() {
		t.Fatal("newSSEWriter 初始化后不应立刻标记 hasWritten=true")
	}

	sw.write("data: test\n\n")

	if !sw.hasWritten() {
		t.Fatal("调用 write 后应标记 hasWritten=true")
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Headers 应该被设置，got Content-Type=%q", rec.Header().Get("Content-Type"))
	}
}

func TestStreamChatCompletions_FirstChunkError429(t *testing.T) {
	// Mock upstream returning 429
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer mockUpstream.Close()

	vertex.SetBatchGraphqlURL(mockUpstream.URL + "/batchGraphql?key=test&prettyPrint=false")

	mockPool := recaptcha.NewTokenPoolCustom(func(proxyURI string) (string, error) {
		return "mock-token", nil
	})
	cfg := config.AppConfig{}
	vc := vertex.NewVertexAIClient(config.StaticProvider(cfg))
	vc.SetTokenPool(mockPool)

	handler := &ChatHandler{
		handler:  handler{vc: vc, cfg: config.StaticProvider(cfg)},
		reqConv:  transform.DefaultRequestConverter(),
		respConv: transform.DefaultResponseConverter(),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`))

	handler.handleChatCompletions(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("全部节点 429 后应返回 HTTP 429，实际得到 %d, body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("报错时不应设置 text/event-stream 响应头, got %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"code":429`) || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("响应 Body 应为 JSON 格式的错误信息, got %s", rec.Body.String())
	}
}

func TestGeminiStreamGenerate_FirstChunkError429(t *testing.T) {
	// Mock upstream returning 429
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer mockUpstream.Close()

	vertex.SetBatchGraphqlURL(mockUpstream.URL + "/batchGraphql?key=test&prettyPrint=false")

	mockPool := recaptcha.NewTokenPoolCustom(func(proxyURI string) (string, error) {
		return "mock-token", nil
	})
	cfg := config.AppConfig{}
	vc := vertex.NewVertexAIClient(config.StaticProvider(cfg))
	vc.SetTokenPool(mockPool)

	handler := &GeminiHandler{
		handler: handler{vc: vc, cfg: config.StaticProvider(cfg)},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-2.5-flash:streamGenerateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))

	handler.handleGeminiStreamGenerate(rec, req, "gemini-2.5-flash")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("全部节点 429 后 Gemini 流式应返回 HTTP 429，实际得到 %d, body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("报错时不应设置 text/event-stream 响应头, got %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"code":429`) || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("响应 Body 应为 JSON 格式的 Gemini 错误信息, got %s", rec.Body.String())
	}
}
