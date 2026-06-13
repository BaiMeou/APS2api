package transport

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"

	tls_client "github.com/bogdanfinn/tls-client"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

var (
	boxMap   = make(map[string]*box.Box)
	boxMutex sync.Mutex
)

func getOrStartBox(uri string) (*box.Box, error) {
	boxMutex.Lock()
	defer boxMutex.Unlock()

	if b, ok := boxMap[uri]; ok {
		return b, nil
	}

	outMap, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}
	outMap["tag"] = "proxy"

	bBytes, _ := json.Marshal(outMap)
	var outbound option.Outbound
	json.Unmarshal(bBytes, &outbound)

	opts := option.Options{
		Outbounds: []option.Outbound{outbound},
	}

	instance, err := box.New(box.Options{Context: context.Background(), Options: opts})
	if err != nil {
		return nil, err
	}
	if err := instance.Start(); err != nil {
		return nil, err
	}

	boxMap[uri] = instance
	return instance, nil
}

type singboxDialer struct{ b *box.Box }

func (d *singboxDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *singboxDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dest := M.ParseSocksaddr(addr)
	routerCtx := service.ContextWith[adapter.Router](ctx, d.b.Router())
	routerDialer, err := dialer.New(routerCtx, option.DialerOptions{}, false)
	if err != nil {
		return nil, err
	}
	return routerDialer.DialContext(ctx, network, dest)
}

func injectProxy(opts []tls_client.HttpClientOption, proxyURI string) []tls_client.HttpClientOption {
	if proxyURI == "" {
		return opts
	}
	if strings.HasPrefix(proxyURI, "http://") || strings.HasPrefix(proxyURI, "https://") || strings.HasPrefix(proxyURI, "socks5://") {
		return append(opts, tls_client.WithProxyUrl(proxyURI))
	}
	b, err := getOrStartBox(proxyURI)
	if err == nil {
		d := &singboxDialer{b: b}
		opts = append(opts, tls_client.WithDialContext(d.DialContext))
	}
	return opts
}
