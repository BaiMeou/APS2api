package transform

import "testing"

func BenchmarkConvertRealtimeChunkContent(b *testing.B) {
	chunk := map[string]any{"candidates": []any{
		map[string]any{
			"content":      map[string]any{"parts": []any{map[string]any{"text": "Hello"}}, "role": "model"},
			"finishReason": "FINISH_REASON_UNSPECIFIED",
		},
	}}
	b.ReportAllocs()
	for b.Loop() {
		_ = ConvertRealtimeChunk(chunk, "gemini-3.1-flash", "req123", false)
	}
}

func BenchmarkConvertRealtimeChunkFirstAndContent(b *testing.B) {
	chunk := map[string]any{"candidates": []any{
		map[string]any{
			"content":      map[string]any{"parts": []any{map[string]any{"text": "Hello"}}, "role": "model"},
			"finishReason": "FINISH_REASON_UNSPECIFIED",
		},
	}}
	b.ReportAllocs()
	for b.Loop() {
		_ = ConvertRealtimeChunk(chunk, "gemini-3.1-flash", "req123", true)
	}
}
