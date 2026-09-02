package spool

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bsfdsagfadg/vertex/internal/jsonx"
)

// TestEncodeJSONMatchesJsonx 验证 EncodeJSON 与 jsonx.Marshal 逐字节一致
// （关 HTML 转义 + 去尾换行），保证发往上游的请求体不变。
func TestEncodeJSONMatchesJsonx(t *testing.T) {
	cases := []any{
		map[string]any{"a": float64(1), "b": "x<y>&z"}, // 含 < > & 验证不转义
		map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "你好"}}}}},
		"plain string",
		[]any{float64(1), float64(2), float64(3)},
	}
	for i, v := range cases {
		buf, err := EncodeJSON(v)
		if err != nil {
			t.Fatalf("case %d EncodeJSON: %v", i, err)
		}
		r, _ := buf.Reader()
		got, _ := io.ReadAll(r)
		want, _ := jsonx.Marshal(v)
		if string(got) != string(want) {
			t.Fatalf("case %d 不一致:\n got=%q\nwant=%q", i, got, want)
		}
		_ = buf.Close()
	}
}

// TestBufferMemOnly 验证内存缓冲：写入、读回完整、Len 正确、不落盘。
func TestBufferMemOnly(t *testing.T) {
	SetMaxSpillBytes(123)
	t.Cleanup(func() { SetMaxSpillBytes(0) })
	before := SpilledBytes()

	b := New()
	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 15 {
		t.Fatalf("Len 应为 15，got %d", b.Len())
	}
	r, err := b.Reader()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	if string(got) != "hello0123456789" {
		t.Fatalf("读回内容错: %q", got)
	}
	if SpilledBytes() != before {
		t.Fatal("阈值内写入不应落盘")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close 应幂等: %v", err)
	}
}

func TestBufferSpillsToDiskAndReadsBack(t *testing.T) {
	SetMaxSpillBytes(8)
	t.Cleanup(func() { SetMaxSpillBytes(0) })
	before := SpilledBytes()

	payload := []byte("0123456789abcdef") // 16 > 8
	b := New()
	if _, err := b.Write(payload[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(payload[6:]); err != nil {
		t.Fatal(err)
	}
	if b.Len() != int64(len(payload)) {
		t.Fatalf("Len=%d, want %d", b.Len(), len(payload))
	}
	if SpilledBytes() <= before {
		t.Fatal("超阈值写入应落盘并累计 SpilledBytes")
	}
	r, err := b.Reader()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("落盘读回错: %q", got)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("落盘 Close 应幂等: %v", err)
	}
}

func TestEncodeJSONSpillMatchesJsonx(t *testing.T) {
	SetMaxSpillBytes(16)
	t.Cleanup(func() { SetMaxSpillBytes(0) })

	v := map[string]any{"text": strings.Repeat("你好", 40), "html": "a<b>&c"}
	buf, err := EncodeJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	r, err := buf.Reader()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want, err := jsonx.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("落盘 EncodeJSON 与 jsonx 不一致:\n got=%q\nwant=%q", got, want)
	}
	if err := buf.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBufferSpillTempFileRemovedOnClose(t *testing.T) {
	SetMaxSpillBytes(4)
	t.Cleanup(func() { SetMaxSpillBytes(0) })
	b := New()
	if _, err := b.Write([]byte("spill-me-please")); err != nil {
		t.Fatal(err)
	}
	path := b.filePath
	if path == "" {
		t.Fatal("落盘后应有临时文件路径")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("落盘临时文件应存在: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Close 后临时文件应删除, err=%v", err)
	}
}
