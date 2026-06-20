// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package recaptcha

import (
	"errors"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
	"github.com/bsfdsagfadg/vertex/internal/transport"
)

type ActiveToken struct {
	Value    string
	ExpireAt time.Time
}

type TokenPool struct {
	net            *transport.NetworkClient
	maxSize        int
	tokenChan      chan ActiveToken
	stopCh         chan struct{}
	onceStart      sync.Once
	onceStop       sync.Once
	fetch          func(proxyURI string) (string, error)
	activeFetchers int32 // 记录当前正在后台执行获取任务的协程数
}

func NewTokenPoolSize(net *transport.NetworkClient, poolSize int) *TokenPool {
	if poolSize <= 0 {
		poolSize = 8
	}
	return &TokenPool{
		net:       net,
		maxSize:   poolSize,
		tokenChan: make(chan ActiveToken, poolSize),
		stopCh:    make(chan struct{}),
		fetch:     func(proxyURI string) (string, error) { return FetchRecaptchaToken(net, proxyURI) },
	}
}

func (p *TokenPool) Start() {
	p.onceStart.Do(func() {
		go p.workerLoop()
	})
}

func (p *TokenPool) Stop() {
	p.onceStop.Do(func() {
		if p.stopCh != nil {
			close(p.stopCh)
		}
		if p.tokenChan != nil {
			for len(p.tokenChan) > 0 {
				<-p.tokenChan
			}
		}
	})
}

func (p *TokenPool) Stats() (size, fill int) {
	return p.maxSize, len(p.tokenChan)
}

func (p *TokenPool) workerLoop() {
	// 使用更温和的 1 秒轮询，消除 select 内部的 time.Sleep 挂起
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			currentCount := len(p.tokenChan)
			active := atomic.LoadInt32(&p.activeFetchers)
			needed := p.maxSize - (currentCount + int(active))

			if needed > 0 {
				for i := 0; i < needed; i++ {
					atomic.AddInt32(&p.activeFetchers, 1)
					go func() {
						defer atomic.AddInt32(&p.activeFetchers, -1)

						// 智能多节点分摊：
						// 1. 优先使用健康的候选并行节点分摊 reCAPTCHA 请求，避免单一 IP 被封
						// 2. 其次使用锁定节点或全局代理
						proxyURI := ""
						cfg := config.Load()
						if cfg.ParallelPoolEnabled {
							cands := nodes.SelectForParallel(cfg.ParallelPoolSize)
							if len(cands) > 0 {
								proxyURI = cands[rand.Intn(len(cands))].RawURI
							}
						}
						if proxyURI == "" {
							proxyURI = cfg.ActiveNodeURI
						}
						if proxyURI == "" {
							proxyURI = cfg.ProxyURL
						}

						val, err := p.fetch(proxyURI)
						if err != nil || val == "" {
							return
						}

						expireSec := cfg.RecaptchaExpireSeconds
						if expireSec <= 0 {
							expireSec = 60
						}
						tok := ActiveToken{
							Value:    val,
							ExpireAt: time.Now().Add(time.Duration(expireSec) * time.Second),
						}

						select {
						case p.tokenChan <- tok:
						case <-p.stopCh:
							return
						}
					}()
				}
			}
		}
	}
}

func (p *TokenPool) GetToken() (string, error) {
	return p.GetTokenWithProxy("")
}

func (p *TokenPool) GetTokenWithProxy(proxyURI string) (string, error) {
	// 总是优先从缓存池中获取 Token。只有在池完全枯竭时，才使用传入的 proxyURI 同步获取。
	// 这能确保在并行池和单节点代理下均能享受到 0ms 的 reCAPTCHA 延迟。
	for {
		select {
		case tok, ok := <-p.tokenChan:
			if !ok {
				return p.fetch(proxyURI)
			}
			if time.Now().After(tok.ExpireAt) {
				continue // 自动跳过已过期的 Token
			}
			return tok.Value, nil
		case <-p.stopCh:
			return "", errors.New("pool stopped")
		default:
			log.Printf("[Recaptcha] 池已枯竭，降级为同步阻塞式抓取")
			return p.fetch(proxyURI)
		}
	}
}
