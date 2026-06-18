package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

type boxInfo struct {
	instance   *box.Box
	port       int
	closed     bool
	refCount   int32
	lastUsedAt time.Time
}

var (
	boxMap   = make(map[string]*boxInfo)
	boxMutex sync.RWMutex
)

func buildOutbound(uri string) (option.Outbound, error) {
	outMap, err := ParseURI(uri)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("parse URI: %w", err)
	}

	typ, _ := outMap["type"].(string)
	outMap["tag"] = "proxy"

	b, err := json.Marshal(outMap)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("marshal outbound: %w", err)
	}

	var opts any
	switch typ {
	case "vless":
		o := new(option.VLESSOutboundOptions)
		if err := json.Unmarshal(b, o); err != nil {
			return option.Outbound{}, fmt.Errorf("unmarshal vless: %w", err)
		}
		opts = o
	case "vmess":
		o := new(option.VMessOutboundOptions)
		if err := json.Unmarshal(b, o); err != nil {
			return option.Outbound{}, fmt.Errorf("unmarshal vmess: %w", err)
		}
		opts = o
	case "trojan":
		o := new(option.TrojanOutboundOptions)
		if err := json.Unmarshal(b, o); err != nil {
			return option.Outbound{}, fmt.Errorf("unmarshal trojan: %w", err)
		}
		opts = o
	case "shadowsocks":
		o := new(option.ShadowsocksOutboundOptions)
		if err := json.Unmarshal(b, o); err != nil {
			return option.Outbound{}, fmt.Errorf("unmarshal shadowsocks: %w", err)
		}
		opts = o
	case "hysteria2":
		o := new(option.Hysteria2OutboundOptions)
		if err := json.Unmarshal(b, o); err != nil {
			return option.Outbound{}, fmt.Errorf("unmarshal hysteria2: %w", err)
		}
		opts = o
	case "tuic":
		o := new(option.TUICOutboundOptions)
		if err := json.Unmarshal(b, o); err != nil {
			return option.Outbound{}, fmt.Errorf("unmarshal tuic: %w", err)
		}
		opts = o
	default:
		return option.Outbound{}, fmt.Errorf("unsupported outbound type: %s", typ)
	}

	return option.Outbound{Type: typ, Tag: "proxy", Options: opts}, nil
}

func safeNewBox(opts option.Options) (instance *box.Box, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("box.New panic: %v\n%s", r, debug.Stack())
		}
	}()
	return box.New(box.Options{Context: include.Context(context.Background()), Options: opts})
}

func BuildSOCKS5Proxy(uri string) (socksAddr string, cleanup func(), err error) {
	boxMutex.Lock()
	if existing, ok := boxMap[uri]; ok {
		existing.lastUsedAt = time.Now()
		boxMutex.Unlock()
		return fmt.Sprintf("socks5://127.0.0.1:%d", existing.port), nil, nil
	}
	boxMutex.Unlock()

	lease, err := DefaultPortAllocator.Acquire()
	if err != nil {
		return "", nil, fmt.Errorf("acquire port: %w", err)
	}

	port := lease.Port
	cleanup = func() {
		DefaultPortAllocator.Release(lease)
	}

	proxyOutbound, err := buildOutbound(uri)
	if err != nil {
		cleanup()
		return "", nil, err
	}

	addr := badoption.Addr(netip.MustParseAddr("127.0.0.1"))
	opts := option.Options{
		Log: &option.LogOptions{Level: "warn", Timestamp: true},
		Inbounds: []option.Inbound{{
			Type: "socks",
			Tag:  "socks-in",
			Options: &option.SocksInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     &addr,
					ListenPort: uint16(port),
				},
			},
		}},
		Outbounds: []option.Outbound{
			proxyOutbound,
			{Type: "direct", Tag: "direct"},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{{
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: badoption.Listable[string]{"socks-in"},
					},
				RuleAction: option.RuleAction{
					Action: "route",
					RouteOptions: option.RouteActionOptions{
							Outbound: "proxy",
						},
					},
				},
			}},
			Final: "proxy",
		},
	}

	instance, err := safeNewBox(opts)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create sing-box: %w", err)
	}

	if err := instance.Start(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("start sing-box: %w", err)
	}

	socksAddr = fmt.Sprintf("socks5://127.0.0.1:%d", port)

	boxMutex.Lock()
	if old, ok := boxMap[uri]; ok {
		old.instance.Close()
	}
	boxMap[uri] = &boxInfo{instance: instance, port: port, refCount: 1, lastUsedAt: time.Now()}
	boxMutex.Unlock()

	return socksAddr, cleanup, nil
}

func resolveProxyURI(uri string) (proxy string, cleanup func(), err error) {
	if uri == "" {
		return "", nil, nil
	}
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "socks5://") {
		return uri, nil, nil
	}
	return BuildSOCKS5Proxy(uri)
}

// RemoveProxy decrements the ref count for the proxy identified by uri.
// When the ref count reaches zero, the sing-box instance is closed and removed.
func RemoveProxy(uri string) {
	boxMutex.Lock()
	defer boxMutex.Unlock()
	info, ok := boxMap[uri]
	if !ok {
		return
	}
	info.refCount--
	if info.refCount <= 0 {
		if !info.closed {
			info.closed = true
			info.instance.Close()
		}
		DefaultPortAllocator.Release(&PortLease{Port: info.port})
		delete(boxMap, uri)
		log.Printf("[Transport] 代理已清理并释放端口: %s (port %d)", uri, info.port)
	}
}

// StartProxyGC starts a background goroutine that periodically closes proxies
// that have been idle (lastUsedAt older than maxIdle) and have no refs.
func StartProxyGC(interval, maxIdle time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
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
		if info.refCount <= 0 && now.Sub(info.lastUsedAt) > maxIdle {
			if !info.closed {
				info.closed = true
				info.instance.Close()
			}
			DefaultPortAllocator.Release(&PortLease{Port: info.port})
			delete(boxMap, uri)
			log.Printf("[Transport] 空闲代理已清理: %s (port %d)", uri, info.port)
		}
	}
}

func StopAllProxies() {
	boxMutex.Lock()
	defer boxMutex.Unlock()

	for _, info := range boxMap {
		if !info.closed {
			info.closed = true
			info.instance.Close()
		}
		DefaultPortAllocator.Release(&PortLease{Port: info.port})
	}
	boxMap = make(map[string]*boxInfo)
}
