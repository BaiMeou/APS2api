// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package vertex

import (
	"strings"
	"testing"

	"github.com/bsfdsagfadg/vertex/internal/config"
)

func TestParseUpstreamData_Basic(t *testing.T) {
	// 模拟匿名 batchGraphql 响应：Gemini 载荷包在 data.ui.streamGenerateContentAnonymous 下。
	raw := `[{"results":[{"data":{"ui":{"streamGenerateContentAnonymous":{` +
		`"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}],` +
		`"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}}}}]}]`
	r := ParseUpstreamData(raw)
	if r.HasError {
		t.Fatalf("unexpected error: %s", r.ErrorMessage)
	}
	if len(r.Parts) != 1 {
		t.Fatalf("parts len=%d, want 1", len(r.Parts))
	}
	if r.Parts[0]["text"] != "Hello" {
		t.Errorf("text=%v", r.Parts[0]["text"])
	}
	if r.FinishReason != "STOP" {
		t.Errorf("finishReason=%q", r.FinishReason)
	}
	if len(r.UsageMetadata) == 0 {
		t.Error("usageMetadata 未提取（unwrap 失败？）")
	}
}

func TestParseUpstreamData_FailedVerify(t *testing.T) {
	raw := `[{"error":{"code":400,"message":"Failed to verify action","status":"INVALID_ARGUMENT"}}]`
	r := ParseUpstreamData(raw)
	// parseErrorResponse 命中 "Failed to verify action" 时不设结构化 ErrorObj，
	// 但 extractErrorMessage 兜底会设 ErrorMessage。上层 executeCompleteRequest 据 ErrorMessage
	// 含该串识别为 AuthenticationError，触发首次认证重试（同 token 再打一次），而不走 ErrorObj。
	if r.ErrorObj != nil {
		t.Error(`"Failed to verify action" 不应设结构化 ErrorObj（parseErrorResponse 忽略它）`)
	}
	if !strings.Contains(r.ErrorMessage, "Failed to verify action") {
		t.Errorf("ErrorMessage 应含 Failed to verify action, got %q", r.ErrorMessage)
	}
}

func TestParseUpstreamData_RealError(t *testing.T) {
	raw := `[{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}]`
	r := ParseUpstreamData(raw)
	if !r.HasError {
		t.Fatal("expected HasError")
	}
	if r.ErrorObj == nil || r.ErrorObj.Kind != "ratelimit" {
		t.Errorf("errObj=%v", r.ErrorObj)
	}
}

func TestCleanJSONString(t *testing.T) {
	if got := cleanJSONString(`{"a":1},`); got != `[{"a":1}]` {
		t.Errorf("尾逗号处理: %q", got)
	}
	if got := cleanJSONString(``); got != "[]" {
		t.Errorf("空串: %q", got)
	}
}

func TestExtractPathIndex(t *testing.T) {
	if got := extractPathIndex(map[string]any{"path": []any{"a", float64(2)}}); got != 2 {
		t.Errorf("path index=%d, want 2", got)
	}
	if got := extractPathIndex(map[string]any{}); got != -1 {
		t.Errorf("no path=%d, want -1", got)
	}
}

func TestParseErrorResponse(t *testing.T) {
	e := parseErrorResponse(map[string]any{"error": map[string]any{
		"code": float64(404), "message": "not found", "status": "NOT_FOUND",
	}})
	if e == nil || e.Kind != "notfound" {
		t.Errorf("got %v", e)
	}
	// GraphQL errors 数组
	e2 := parseErrorResponse(map[string]any{"errors": []any{
		map[string]any{"message": "boom", "code": float64(500)},
	}})
	if e2 == nil {
		t.Error("errors 数组未解析")
	}
}

func TestAuthError502(t *testing.T) {
	e := NewAuthenticationError("x")
	if e.Code != 502 {
		t.Errorf("auth code=%d, want 502（红线：避免网关误判禁用渠道）", e.Code)
	}
	if !e.IsRetryable() {
		t.Error("auth 应可重试")
	}
}

func TestRaiseForStatus(t *testing.T) {
	if raiseForStatus(429, "", "x", nil, "").Kind != "ratelimit" {
		t.Error("429 → ratelimit")
	}
	if raiseForStatus(401, "", "x", nil, "").Code != 502 {
		t.Error("401 → auth(502)")
	}
	if raiseForStatus(400, "", "x", nil, "").Kind != "invalid" {
		t.Error("400 → invalid")
	}
}

func TestBuildRequestPayload(t *testing.T) {
	cfg := config.DefaultConfig()
	payload := map[string]any{"contents": []any{
		map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}},
	}}
	body := buildRequestPayload("gemini-3.1-flash", payload, "TOKEN123", cfg)
	if body["querySignature"] != querySignature {
		t.Error("querySignature 不匹配")
	}
	if body["operationName"] != "StreamGenerateContentAnonymous" {
		t.Error("operationName 不匹配")
	}
	vars := body["variables"].(map[string]any)
	if vars["region"] != "global" {
		t.Errorf("region=%v, want global", vars["region"])
	}
	if vars["recaptchaToken"] != "TOKEN123" {
		t.Errorf("recaptchaToken=%v", vars["recaptchaToken"])
	}
	if vars["model"] != "gemini-3.1-flash" {
		t.Errorf("model=%v", vars["model"])
	}
}

func TestBuildCompleteResponse_Empty(t *testing.T) {
	c := &VertexAIClient{}
	// 无 parts、无 error、无 promptFeedback → EmptyResponseError
	_, err := c.buildCompleteResponse(&ParseResult{PromptFeedback: map[string]any{}})
	if err == nil {
		t.Error("空响应应返回 EmptyResponseError")
	}
	if ve := asVertexError(err); ve == nil || ve.Kind != "empty" {
		t.Errorf("err=%v, want empty", err)
	}
}
