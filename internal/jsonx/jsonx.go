// Package jsonx 提供关闭 HTML 转义的 JSON 序列化。
//
// Go 标准库 json.Marshal 默认会把 < > & 转义成 < > &，
// 而我们不做这种转义。为了逐字节稳定（既用于发往上游的请求体，也用于返回给客户端的响应体），
// 这里统一用关闭 HTML 转义的编码器。这是里程碑红线之一。
package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
)

type marshalBuf struct {
	buf bytes.Buffer
	enc *json.Encoder
}

func newMarshalBuf() *marshalBuf {
	m := &marshalBuf{}
	m.enc = json.NewEncoder(&m.buf)
	m.enc.SetEscapeHTML(false)
	return m
}

var marshalPool = sync.Pool{New: func() any { return newMarshalBuf() }} //nolint:gochecknoglobals

func encode(v any) (*marshalBuf, []byte, error) {
	m := marshalPool.Get().(*marshalBuf)
	m.buf.Reset()
	if err := m.enc.Encode(v); err != nil {
		marshalPool.Put(m)
		return nil, nil, fmt.Errorf("error: %w", err)
	}
	b := m.buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return m, b, nil
}

// Marshal 序列化为 JSON，不做 HTML 转义、不转义非 ASCII。
func Marshal(v any) ([]byte, error) {
	m, b, err := encode(v)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(b))
	copy(out, b)
	marshalPool.Put(m)
	return out, nil
}

// Append 把 v 序列化后追加到 dst，语义与 Marshal 相同。
func Append(dst []byte, v any) ([]byte, error) {
	m, b, err := encode(v)
	if err != nil {
		return dst, err
	}
	dst = append(dst, b...)
	marshalPool.Put(m)
	return dst, nil
}

// Truthy 复刻动态语言常见的真值语义，用于判断解析出的 JSON 值是否"为真"
// （nil/false/空串/0/空数组/空对象为假，其余为真）。集中一处，避免各包重复实现导致语义漂移。
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}
