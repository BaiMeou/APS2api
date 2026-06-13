package api

import (
	"context"
	"net/http"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/transform"
)

// 本文件实现假流式：模型名带 "假流式-"/"fake-" 前缀时，先完整非流式生成、再切片按 SSE 推。
// OpenAI 端点与 Gemini 端点（use_fake 分支）共用此机制。

// sseWriter 是一个带 flush 的 SSE 行写出器；write 返回 false 表示客户端断开。
type sseWriter struct {
	w     http.ResponseWriter
	flush func()
}

// newSSEWriter 写出标准 SSE 响应头并返回写出器。
// contentType 通常是 text/event-stream（OAI）或 application/json（Gemini 流，沿用既定 media_type）。
func (s *Server) newSSEWriter(w http.ResponseWriter, contentType string) *sseWriter {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w}
	if flusher != nil {
		sw.flush = flusher.Flush
	}
	return sw
}

// write 写一条原始字符串并 flush。返回 false 表示客户端断开。
func (sw *sseWriter) write(line string) bool {
	if _, err := sw.w.Write([]byte(line)); err != nil {
		return false
	}
	if sw.flush != nil {
		sw.flush()
	}
	return true
}

// oaiFakeStream 处理 OpenAI 假流式：完整非流式生成 → 转 OAI → 把 content 文本切片按 chunk 推。
// 切片大小 = max(1, len/8)。
func (s *Server) oaiFakeStream(ctx context.Context, w http.ResponseWriter, model string, geminiPayload map[string]any) {
	requestID := reqID24()
	sw := s.newSSEWriter(w, "text/event-stream")

	resp, vErr := s.vc.CompleteChat(ctx, model, geminiPayload)
	if vErr != nil {
		ve := toVertexError(vErr)
		s.writeStreamError(sw.write, ve, requestID)
		return
	}

	oai := transform.GeminiJSONToOAIJSON(resp, model)
	contentText := firstChoiceContent(oai)

	createdTS := time.Now().Unix()
	chunkSize := 1
	if len(contentText) > 0 {
		if cs := len(contentText) / 8; cs > 1 {
			chunkSize = cs
		}
	}

	// 按字节切片推 delta（len/8 的字节切片；非 ASCII 可能切在多字节边界，
	// 但客户端按字节流重组无碍——逐字节保持既定行为，不做 rune 边界优化）。
	for i := 0; i < len(contentText); i += chunkSize {
		end := i + chunkSize
		if end > len(contentText) {
			end = len(contentText)
		}
		piece := contentText[i:end]
		base := s.streamChunkBase(model, requestID)
		base["created"] = createdTS
		var delta map[string]any
		if i == 0 {
			delta = map[string]any{"role": "assistant", "content": piece}
		} else {
			delta = map[string]any{"content": piece}
		}
		choice := map[string]any{"index": 0, "delta": delta}
		if end >= len(contentText) {
			choice["finish_reason"] = "stop"
		}
		base["choices"] = []any{choice}
		if !sw.write(s.sseEvent(base)) {
			return
		}
	}
	_ = sw.write("data: [DONE]\n\n")
}

// firstChoiceContent 取 OAI 响应 choices[0].message.content（非字符串/缺失返 ""）。
func firstChoiceContent(oai map[string]any) string {
	choices, ok := oai["choices"].([]any)
	if !ok || len(choices) == 0 {
		return ""
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return ""
	}
	msg, ok := choice["message"].(map[string]any)
	if !ok {
		return ""
	}
	if c, ok := msg["content"].(string); ok {
		return c
	}
	return ""
}
