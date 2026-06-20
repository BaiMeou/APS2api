// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package transport

import http "github.com/bogdanfinn/fhttp"

// Chrome 131 的 UA 与 sec-ch-ua（与 transport 的 TLS 指纹保持一致）。
const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	chUA      = `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`
)

// XHRHeaders 构造 XHR/fetch 风格的完整 Chrome 请求头（含 H2 头顺序 HeaderOrderKey）。
//
// tls-client 不像 curl_cffi 那样自动补 sec-ch-ua / sec-fetch-* 等头，必须手动设全，
// 否则 Google 匿名端点的指纹校验不通过。逐字节抄自经实测的 PoC（recaptcha reload
// 与 batchGraphql 都用此头）。
func XHRHeaders(contentType, accept, origin, referer, site string) http.Header {
	h := http.Header{
		"sec-ch-ua":          {chUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"Windows"`},
		"user-agent":         {userAgent},
		"accept":             {accept},
		"origin":             {origin},
		"sec-fetch-site":     {site},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"referer":            {referer},
		"accept-encoding":    {"gzip, deflate, br"},
		"accept-language":    {"en-US,en;q=0.9"},
		"priority":           {"u=1, i"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "user-agent",
			"content-type", "accept", "origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language", "priority",
		},
	}
	if contentType != "" {
		h["content-type"] = []string{contentType}
	}
	return h
}

// AnchorHeaders 构造 recaptcha anchor iframe 导航请求头。逐字节抄自 PoC。
func AnchorHeaders() http.Header {
	return http.Header{
		"sec-ch-ua":                 {chUA},
		"sec-ch-ua-mobile":          {"?0"},
		"sec-ch-ua-platform":        {`"Windows"`},
		"upgrade-insecure-requests": {"1"},
		"user-agent":                {userAgent},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"sec-fetch-site":            {"cross-site"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-dest":            {"iframe"},
		"accept-encoding":           {"gzip, deflate, br"},
		"accept-language":           {"en-US,en;q=0.9"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests",
			"user-agent", "accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"accept-encoding", "accept-language",
		},
	}
}
