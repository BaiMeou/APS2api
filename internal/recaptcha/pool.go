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
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			currentCount := len(p.tokenChan)
			active := atomic.LoadInt32(&p.activeFetchers)

			// 动态读取配置，允许不重启的情况下缩小池大小限制（扩大需要重启）
			targetSize := config.Load().TokenPoolSize
			if targetSize <= 0 {
				targetSize = 8
			}
			if targetSize > p.maxSize {
				targetSize = p.maxSize // 受限于初始化的 channel 容量
			}

			needed := targetSize - (currentCount + int(active))

			if needed > 0 {
				for i := 0; i < needed; i++ {
					atomic.AddInt32(&p.activeFetchers, 1)
					go func() {
						defer atomic.AddInt32(&p.activeFetchers, -1)

						// 急救模式：如果池子彻底干涸（currentCount == 0 / len(tokenChan) == 0）
						// 则说明聊天处于大并发对冲启动阶段。此时跳过随机 jitter 错开，不睡眠，全力秒级极速拉取重填。
						if len(p.tokenChan) > 0 {
							time.Sleep(time.Duration(rand.Intn(1000)) * time.Millisecond)
						} else {
							log.Printf("[Recaptcha] Token 池空，启动急救无缝填充机制...")
						}

						// 智能多节点分摊
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
						default:
							// 满了直接退出，防止 goroutine 泄露
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
	for {
		select {
		case tok, ok := <-p.tokenChan:
			if !ok {
				return p.fetch(proxyURI)
			}
			// 即将过期的 Token 提前过滤掉（留出 5 秒网络传输冗余），提升请求成功率
			if time.Now().Add(5 * time.Second).After(tok.ExpireAt) {
				continue // 自动跳过即将过期的 Token
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