// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package vertex

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
)

func RunParallel[T any](ctx context.Context, cfg config.AppConfig, op func(ctx context.Context, proxyURI string) (T, error)) (T, error) {
	cands := nodes.SelectForParallel(cfg.ParallelPoolSize)
	if !cfg.ParallelPoolEnabled || len(cands) == 0 {
		proxy := cfg.ActiveNodeURI
		if proxy == "" {
			proxy = cfg.ProxyURL
		}
		log.Printf("[Vertex] [RunParallel] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		return op(ctx, proxy)
	}

	log.Printf("[Vertex] [RunParallel] 开启并发测速, %d 个节点参与", len(cands))
	for _, c := range cands {
		log.Printf("[Vertex] [RunParallel] 参与节点: %s", c.Name)
	}

	ctxRace, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		uri string
		val T
		err error
	}

	resCh := make(chan result, cfg.ParallelPoolSize)
	var active int32
	var mu sync.Mutex
	activeKeys := make(map[string]bool)
	round := 0

	startNext := func() {
		mu.Lock()
		defer mu.Unlock()
		if cfg.ParallelPoolMaxRounds > 0 && round >= cfg.ParallelPoolMaxRounds {
			return
		}
		roundCands := nodes.SelectForParallel(1)
		for _, c := range roundCands {
			if !activeKeys[c.RawURI] {
				activeKeys[c.RawURI] = true
				atomic.AddInt32(&active, 1)
				go func(u string) {
					v, err := op(ctxRace, u)
					select {
					case resCh <- result{u, v, err}:
					case <-ctxRace.Done():
					}
				}(c.RawURI)
				return
			}
		}
		round++
	}

	for i := 0; i < cfg.ParallelPoolSize && i < len(cands); i++ {
		mu.Lock()
		activeKeys[cands[i].RawURI] = true
		atomic.AddInt32(&active, 1)
		mu.Unlock()
		go func(u string) {
			v, err := op(ctxRace, u)
			select {
			case resCh <- result{u, v, err}:
			case <-ctxRace.Done():
			}
		}(cands[i].RawURI)
	}

	var lastErr error
	var zero T
	for atomic.LoadInt32(&active) > 0 {
		select {
		case res := <-resCh:
			atomic.AddInt32(&active, -1)
			name := res.uri
			for _, c := range cands {
				if c.RawURI == res.uri {
					name = c.Name
					break
				}
			}
			if res.err == nil {
				log.Printf("[Racing] 节点 %s 成功", name)
				nodes.RecordTest(res.uri, true, 50, "")
				if ctx.Err() == nil {
					return res.val, nil
				}
			} else if res.err != context.Canceled && !errors.Is(res.err, context.Canceled) {
				log.Printf("[Racing] 节点 %s 失败: %s", name, res.err.Error())
				nodes.RecordTest(res.uri, false, 0, res.err.Error())
			} else {
				log.Printf("[Racing] 节点 %s 被取消", name)
			}
			lastErr = res.err
			startNext()
		case <-ctx.Done():
			log.Printf("[Racing] 客户端断开，停止并行竞争")
			return zero, ctx.Err()
		}
	}

	if lastErr != nil {
		return zero, lastErr
	}
	return zero, fmt.Errorf("all nodes failed")
}

func StreamParallel(ctx context.Context, cfg config.AppConfig, op func(ctx context.Context, proxyURI string) <-chan StreamChunk, yield func(StreamChunk) bool) {
	cands := nodes.SelectForParallel(cfg.ParallelPoolSize)
	if !cfg.ParallelPoolEnabled || len(cands) == 0 {
		proxy := cfg.ActiveNodeURI
		if proxy == "" {
			proxy = cfg.ProxyURL
		}
		log.Printf("[Vertex] [StreamParallel] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		for chunk := range op(ctx, proxy) {
			if !yield(chunk) {
				return
			}
		}
		return
	}
	log.Printf("[Vertex] [StreamParallel] 开启并发测速, %d 个节点参与", len(cands))
	for _, c := range cands {
		log.Printf("[Vertex] [StreamParallel] 参与节点: %s", c.Name)
	}
	ctxRace, cancel := context.WithCancel(ctx)
	defer cancel()
	type res struct {
		uri   string
		ch    <-chan StreamChunk
		first StreamChunk
		err   error
	}
	resCh := make(chan res, len(cands))
	var active int32
	for _, cand := range cands {
		atomic.AddInt32(&active, 1)
		go func(u string) {
			ch := op(ctxRace, u)
			select {
			case first, ok := <-ch:
				if !ok {
					select {
					case resCh <- res{u, nil, StreamChunk{}, fmt.Errorf("stream closed")}:
					case <-ctxRace.Done():
					}
				} else if first.Err != nil {
					select {
					case resCh <- res{u, nil, StreamChunk{}, first.Err}:
					case <-ctxRace.Done():
					}
				} else {
					select {
					case resCh <- res{u, ch, first, nil}:
					case <-ctxRace.Done():
					}
				}
			case <-ctxRace.Done():
			}
		}(cand.RawURI)
	}
	var winner *res
loop:
	for atomic.LoadInt32(&active) > 0 {
		select {
		case r := <-resCh:
			atomic.AddInt32(&active, -1)
			name := r.uri
			for _, c := range cands {
				if c.RawURI == r.uri {
					name = c.Name
					break
				}
			}
			if r.err == nil {
				if winner == nil {
					winner = &r
					log.Printf("[Vertex] [StreamParallel] 节点胜出: %s", name)
					nodes.RecordTest(r.uri, true, 50, "")
					break loop
				}
				log.Printf("[Vertex] [StreamParallel] 节点备用成功: %s", name)
			} else if ctx.Err() == nil && r.err != context.Canceled && !errors.Is(r.err, context.Canceled) {
				log.Printf("[Racing] 节点 %s 失败: %s", name, r.err.Error())
				nodes.RecordTest(r.uri, false, 0, r.err.Error())
			}
		case <-ctx.Done():
			log.Printf("[Racing] 客户端断开，停止并行竞争")
			return
		}
	}
	if winner != nil {
		if !yield(winner.first) {
			return
		}
		for chunk := range winner.ch {
			if !yield(chunk) {
				return
			}
		}
	} else {
		yield(StreamChunk{Err: NewInternalError("all nodes failed to stream")})
	}
}
