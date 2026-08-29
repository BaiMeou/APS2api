package vertex

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestPickBestErrorPrefersRetryableRateLimit(t *testing.T) {
	got := pickBestError([]error{
		NewPermissionDeniedError("perm"),
		NewRateLimitError("quota", 0),
		NewUnavailableError("down"),
		fmt.Errorf("plain"),
	})
	var ve *VertexError
	if !asVertexErrorOK(got, &ve) || ve.Kind != "ratelimit" {
		t.Fatalf("应优先返回 429, got %v", got)
	}
}

func TestPickBestErrorPrefersRetryableOverHardAndGeneric(t *testing.T) {
	got := pickBestError([]error{
		fmt.Errorf("plain"),
		NewInvalidArgumentError("bad"),
		NewUnavailableError("down"),
	})
	var ve *VertexError
	if !asVertexErrorOK(got, &ve) || ve.Kind != "unavailable" {
		t.Fatalf("无可重试 429 时应优先可重试 503, got %v", got)
	}
}

func TestPickBestErrorEmpty(t *testing.T) {
	err := pickBestError(nil)
	if err == nil || !strings.Contains(err.Error(), "all nodes failed") {
		t.Fatalf("空列表应返回 all nodes failed, got %v", err)
	}
}

func TestDeepCopyAnyIsolatesNestedMapsAndSlices(t *testing.T) {
	orig := map[string]any{
		"model": "gemini",
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}},
		},
		"n": float64(1),
	}
	copied, ok := deepCopyAny(orig).(map[string]any)
	if !ok {
		t.Fatal("deepCopyAny 应返回 map")
	}
	contents := copied["contents"].([]any)
	first := contents[0].(map[string]any)
	first["role"] = "mutated"
	first["parts"].([]any)[0].(map[string]any)["text"] = "changed"
	copied["model"] = "other"

	if orig["model"] != "gemini" {
		t.Fatal("顶层 string 被改写")
	}
	origFirst := orig["contents"].([]any)[0].(map[string]any)
	if origFirst["role"] != "user" {
		t.Fatal("嵌套 map 未隔离")
	}
	if origFirst["parts"].([]any)[0].(map[string]any)["text"] != "hello" {
		t.Fatal("深层 slice/map 未隔离")
	}
}

func TestScanStreamConcurrentPoolReuse(t *testing.T) {
	raw := wrap(`{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}]}`)
	errc := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var emitted int
			err := scanStream(strings.NewReader(raw), func(obj map[string]any) (bool, error) {
				stop, err := processStreamingObject(obj, func(map[string]any) bool {
					emitted++
					return true
				})
				return stop, err
			})
			if err != nil {
				errc <- err
				return
			}
			if emitted != 1 {
				errc <- fmt.Errorf("emitted=%d, want 1", emitted)
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
}

func asVertexErrorOK(err error, dest **VertexError) bool {
	ve := asVertexError(err)
	if ve == nil {
		return false
	}
	*dest = ve
	return true
}

func BenchmarkDeepCopyAnyPayload(b *testing.B) {
	payload := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"text": strings.Repeat("x", 256)},
				map[string]any{"inlineData": map[string]any{"mimeType": "text/plain", "data": strings.Repeat("d", 512)}},
			}},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = deepCopyAny(payload)
	}
}
