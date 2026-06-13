// Package transport 封装 bogdanfinn/tls-client，提供带 Chrome TLS 指纹的 HTTP 会话。
//
// 这是 vproxy 的命脉：Google 匿名端点会校验 TLS ClientHello 指纹（JA3/JA4/Akamai-H2），
// 必须用 tls-client 的 Chrome profile 伪装。PoC 已实测 Chrome_131 的指纹与 curl_cffi
// (chrome131) 完全一致、端到端真调通过，故沿用 bogdanfinn/tls-client（而非 utls 裸方案）。
package transport

import (
	"context"
	"io"
	"math/rand"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// Header 是 fhttp.Header 的别名，让 recaptcha/vertex 能构造请求头而不直接 import fhttp。
type Header = http.Header

// Response 是 fhttp.Response 的别名。
type Response = http.Response

// Session 封装一个独立的 tls-client，服务于单次逻辑请求。
//
// 每个 Session 持有自己的连接池，请求结束即 Close —— 即用即毁
// + FRESH_CONNECT 防串流语义：
// 不同逻辑请求绝不共享连接，杜绝 HTTP/2 多路复用把一个请求的回复串到另一个。
type Session struct {
	client tls_client.HttpClient
}

// Do 发送一次请求。header 用 XHRHeaders / AnchorHeaders 构造（含 H2 头顺序）。
//
// ctx 绑定到请求：客户端断开 / 优雅关闭使 ctx 取消时，底层 HTTP 请求随之中断
// （req.WithContext），不再空等上游响应。
func (s *Session) Do(ctx context.Context, method, url string, header http.Header, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	if header != nil {
		req.Header = header
	}
	return s.client.Do(req)
}

// DoAndRead 发送请求、读完整响应体后关闭。读完再关 = 排干在前，防止上游半截 body
// 串到下一个请求（红线：排干 → close）。返回状态码与已解压的响应体。
func (s *Session) DoAndRead(ctx context.Context, method, url string, header http.Header, body io.Reader) (int, []byte, error) {
	resp, err := s.Do(ctx, method, url, header, body)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, readErr
	}
	return resp.StatusCode, data, nil
}

// StreamResponse 是流式请求返回的响应句柄：状态码 + 未读完的 Body。
//
// 调用方边读 Body 边处理增量数据，处理完（无论正常或异常）必须 Close —— Close 会
// 先排干未读完的数据再关闭连接（aiter_content 排干 → aclose）。排干在前防止上游半截
// body 串到下一个请求（FRESH_CONNECT 语义）。
type StreamResponse struct {
	StatusCode int
	Body       io.ReadCloser
}

// Close 排干未读完的数据再关闭 Body（排干 → close，红线：防串流）。
func (sr *StreamResponse) Close() {
	if sr.Body == nil {
		return
	}
	// 排干：把剩余字节读丢，防止半截 body 串到复用连接（即便本 Session 即用即毁，
	// 排干仍是防御，且让底层连接干净关闭）。
	_, _ = io.Copy(io.Discard, sr.Body)
	_ = sr.Body.Close()
}

// DoStream 发送请求并返回流式句柄（不读完 Body）。
// 调用方读 Body 增量，用完 Close 排干+关闭。
//
// ctx 绑定到请求：客户端断开 / 优雅关闭使 ctx 取消时，底层流连接随之中断，
// 后续 Body.Read 返回错误，调用方扫描循环干净结束。
func (s *Session) DoStream(ctx context.Context, method, url string, header http.Header, body io.Reader) (*StreamResponse, error) {
	resp, err := s.Do(ctx, method, url, header, body)
	if err != nil {
		return nil, err
	}
	return &StreamResponse{StatusCode: resp.StatusCode, Body: resp.Body}, nil
}

// Close 释放 Session 的连接（即用即毁）。
func (s *Session) Close() {
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
}

// NetworkClient 是 Session 工厂。
type NetworkClient struct{}

// NewNetworkClient 构造 NetworkClient。
func NewNetworkClient() *NetworkClient { return &NetworkClient{} }

// browserProfiles 浏览器指纹目标 = ["chrome124", "chrome131"]。
var browserProfiles = []profiles.ClientProfile{profiles.Chrome_124, profiles.Chrome_131}

func pickProfile() profiles.ClientProfile {
	return browserProfiles[rand.Intn(len(browserProfiles))]
}

// CreateSession 创建一个新 Session：随机 Chrome 指纹 + 可选代理 + 独立 cookie jar。
//
// timeoutSec 为该会话所有请求的超时（tls-client 是 client 级超时，非 per-request）。
// recaptcha 取 token 用较短超时（15s），batchGraphql 用 180s。
func (c *NetworkClient) CreateSession(timeoutSec int) (*Session, error) {
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(timeoutSec),
		tls_client.WithClientProfile(pickProfile()),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
	}
	if proxy := pickProxy(); proxy != "" {
		opts = append(opts, tls_client.WithProxyUrl(proxy))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, err
	}
	return &Session{client: client}, nil
}
