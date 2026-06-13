//go:build !serveropt

package recaptcha

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// TestTokenPoolLiteAlwaysRealtime 验证精简实现每次 GetToken 都实时获取，
// 且 Start/Stop 为空操作、Stats 恒为 0,0。
func TestTokenPoolLiteAlwaysRealtime(t *testing.T) {
	var calls int32
	p := &TokenPool{fetch: func() (string, error) {
		n := atomic.AddInt32(&calls, 1)
		return fmt.Sprintf("tok-%d", n), nil
	}}

	p.Start() // 空操作
	if size, fill := p.Stats(); size != 0 || fill != 0 {
		t.Fatalf("精简实现 Stats 应恒为 0,0，got %d,%d", size, fill)
	}

	for i := 1; i <= 3; i++ {
		tok, err := p.GetToken()
		if err != nil || tok == "" {
			t.Fatalf("第 %d 次 GetToken 失败：tok=%q err=%v", i, tok, err)
		}
		if int(atomic.LoadInt32(&calls)) != i {
			t.Fatalf("精简实现应每次实时获取，期望 %d 次，实际 %d", i, calls)
		}
	}

	p.Stop() // 空操作，不应阻塞
}
