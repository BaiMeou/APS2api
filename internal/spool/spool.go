// Package spool 提供"先写后读"的字节缓冲，用于承载请求/媒体体的序列化输出。
//
// 缓冲驻留内存。SetMaxSpillBytes / SpilledBytes 是为磁盘溢出策略预留的接口位，
// 当前不启用磁盘配额，调用方无需感知缓冲介质。
package spool

import (
	"bytes"
	"encoding/json"
	"io"
)

// SetMaxSpillBytes 设置磁盘溢出配额上限；当前不溢出到磁盘，故为空操作。
func SetMaxSpillBytes(int64) {}

// SpilledBytes 返回已溢出到磁盘的字节数；当前恒为 0。
func SpilledBytes() int64 { return 0 }

// Buffer 是纯内存的先写后读字节缓冲。
//
// 非并发安全——约定单个逻辑请求内串行 Write→Reader→Close 使用。
type Buffer struct {
	mem []byte
}

// New 构造一个空 Buffer。
func New() *Buffer { return &Buffer{} }

// Write 实现 io.Writer：累加到内存缓冲。
func (b *Buffer) Write(p []byte) (int, error) {
	b.mem = append(b.mem, p...)
	return len(p), nil
}

// Len 返回当前已写入字节数。
func (b *Buffer) Len() int64 { return int64(len(b.mem)) }

// Reader 返回从头读取已写内容的 io.Reader。
func (b *Buffer) Reader() (io.Reader, error) { return bytes.NewReader(b.mem), nil }

// Close 释放内存（幂等）。
func (b *Buffer) Close() error {
	b.mem = nil
	return nil
}

// trimTrailingNewline 去掉末尾一个换行符（与 jsonx.Marshal 去尾换行对齐，
// 保证发往上游的请求体逐字节一致）。
func (b *Buffer) trimTrailingNewline() {
	if n := len(b.mem); n > 0 && b.mem[n-1] == '\n' {
		b.mem = b.mem[:n-1]
	}
}

// EncodeJSON 把 v 关闭 HTML 转义地序列化进一个 Buffer 并去掉尾换行，行为对齐 jsonx.Marshal。
// 返回的 Buffer 用完必须 Close。出错时已自行 Close 并返回 nil。
func EncodeJSON(v any) (*Buffer, error) {
	b := New()
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		_ = b.Close()
		return nil, err
	}
	b.trimTrailingNewline()
	return b, nil
}
