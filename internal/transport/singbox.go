package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"runtime/debug"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/include" // 用于构建必需的上下文注册表
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	sjson "github.com/sagernet/sing/common/json" // 命名为 sjson 以区分系统的 json
)

type boxInfo struct {
	instance   *box.Box
	lastUsedAt time.Time
	closed     bool
}

var (
	boxMap   = make(map[string]*boxInfo)
	boxMutex sync.RWMutex
)

func safeNewBox(opts option.Options) (instance *box.Box, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("box.New panic: %v\n%s", r, debug.Stack())
		}
	}()
	return box.New(box.Options{Context: include.Context(context.Background()), Options: opts})
}

// getOrStartBoxDialer 获取或启动节点的内部 Dialer
func getOrStartBoxDialer(uri string) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	boxMutex.Lock()
	if info, ok := boxMap[uri]; ok && !info.closed {
		info.lastUsedAt = time.Now()
		b := info.instance
		boxMutex.Unlock()
		d := &singboxDialer{b: b}
		return d.DialContext, nil
	}
	boxMutex.Unlock()

	outMap, err := ParseURI(uri)
	if err != nil {
		return nil, fmt.Errorf("parse URI: %w", err)
	}
	outMap["tag"] = "proxy"
	outMap["domain_resolver"] = "dns-direct"

	proxyOutboundBytes, err := json.Marshal(outMap)
	if err != nil {
		return nil, fmt.Errorf("marshal proxy outbound: %w", err)
	}

	configJSON := fmt.Sprintf(`{
		"dns": {
			"servers": [
				{
					"tag": "dns-direct",
					"type": "udp",
					"server": "223.5.5.5",
					"detour": "direct"
				},
				{
					"tag": "dns-proxy",
					"type": "udp",
					"server": "8.8.8.8"
				}
			]
		},
		"outbounds": [
			%s,
			{
				"type": "direct",
				"tag": "direct"
			}
		]
	}`, string(proxyOutboundBytes))

	ctx := include.Context(context.Background())
	opts, err := sjson.UnmarshalExtendedContext[option.Options](ctx, []byte(configJSON))
	if err != nil {
		return nil, fmt.Errorf("unmarshal sing-box config: %w", err)
	}

	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return nil, err
	}
	if err := instance.Start(); err != nil {
		return nil, err
	}

	boxMutex.Lock()
	if old, ok := boxMap[uri]; ok && !old.closed {
		old.closed = true
		old.instance.Close()
	}
	boxMap[uri] = &boxInfo{instance: instance, lastUsedAt: time.Now()}
	boxMutex.Unlock()

	d := &singboxDialer{b: instance}
	return d.DialContext, nil
}

type singboxDialer struct {
	b *box.Box
}

func (d *singboxDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *singboxDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dest := M.ParseSocksaddr(addr)
	routerCtx := service.ContextWith(ctx, d.b.Router())
	
	routerDialer, err := dialer.New(routerCtx, option.DialerOptions{}, false)
	if err != nil {
		return nil, err
	}
	return routerDialer.DialContext(ctx, network, dest)
}

// RemoveProxy 主动清理代理实例 (响应面板删除节点)
func RemoveProxy(uri string) {
	boxMutex.Lock()
	if info, ok := boxMap[uri]; ok {
		if !info.closed {
			info.closed = true
			info.instance.Close()
			log.Printf("[Transport] 代理节点已清理释放: %s", uri)
		}
		delete(boxMap, uri)
	}
	boxMutex.Unlock()
}

// StartProxyGC 启动后台空闲实例垃圾回收 (每隔 interval 扫描，超时 maxIdle 回收)
func StartProxyGC(interval, maxIdle time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			cleanupIdleProxies(maxIdle)
		}
	}()
}

func cleanupIdleProxies(maxIdle time.Duration) {
	boxMutex.Lock()
	defer boxMutex.Unlock()
	now := time.Now()
	for uri, info := range boxMap {
		if now.Sub(info.lastUsedAt) > maxIdle {
			if !info.closed {
				info.closed = true
				info.instance.Close()
				log.Printf("[Transport] 空闲代理已清理释放: %s", uri)
			}
			delete(boxMap, uri)
		}
	}
}

// StopAllProxies 程序优雅退出时清理全部实例
func StopAllProxies() {
	boxMutex.Lock()
	defer boxMutex.Unlock()
	for _, info := range boxMap {
		if !info.closed {
			info.closed = true
			info.instance.Close()
		}
	}
	boxMap = make(map[string]*boxInfo)
}