package jsonx

import "testing"

func BenchmarkMarshalSmallMap(b *testing.B) {
	v := map[string]any{
		"id":      "chatcmpl-req123",
		"object":  "chat.completion.chunk",
		"created": int64(1700000000),
		"model":   "gemini-3.1-flash",
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"content": "Hi"}, "finish_reason": nil,
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Marshal(v); err != nil {
			b.Fatal(err)
		}
	}
}
