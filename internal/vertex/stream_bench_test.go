package vertex

import (
	"strings"
	"testing"
)

func BenchmarkScanStreamTypical(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 32; i++ {
		sb.WriteString(wrap(`{"candidates":[{"content":{"parts":[{"text":"x"}],"role":"model"},"finishReason":"FINISH_REASON_UNSPECIFIED"}]}`))
	}
	sb.WriteString(wrap(`{"candidates":[{"content":{"parts":[{"text":"end"}],"role":"model"},"finishReason":"STOP"}]}`))
	raw := sb.String()
	b.ReportAllocs()
	for b.Loop() {
		_ = scanStream(strings.NewReader(raw), func(obj map[string]any) (bool, error) {
			return processStreamingObject(obj, func(map[string]any) bool { return true })
		})
	}
}
