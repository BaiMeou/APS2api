package api

import (
	"net/url"
	"testing"
)

func TestParseInlineYamlAttrsKeepsNestedObjects(t *testing.T) {
	attrs := parseInlineYamlAttrs("name: demo, type: vless, ws-opts: { path: /ws, headers: { Host: edge.example.com } }, reality-opts: { public-key: pubkey, short-id: abcd }")

	if got := attrs["ws-opts"]; got != "{ path: /ws, headers: { Host: edge.example.com } }" {
		t.Fatalf("ws-opts was split unexpectedly: %q", got)
	}
	if got := attrs["reality-opts"]; got != "{ public-key: pubkey, short-id: abcd }" {
		t.Fatalf("reality-opts was split unexpectedly: %q", got)
	}
}

func TestClashProxyToURIPreservesVlessWSAndReality(t *testing.T) {
	raw := clashProxyToURI(map[string]string{
		"type":               "vless",
		"name":               "demo",
		"server":             "cf.example.com",
		"port":               "443",
		"uuid":               "12345678-1234-1234-1234-123456789012",
		"tls":                "true",
		"servername":         "edge.example.com",
		"client-fingerprint": "chrome",
		"flow":               "xtls-rprx-vision",
		"network":            "ws",
		"ws-opts":            "{ path: /ws, headers: { Host: edge.example.com } }",
		"reality-opts":       "{ public-key: pubkey, short-id: abcd }",
	})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	q := u.Query()

	if u.Scheme != "vless" {
		t.Fatalf("unexpected scheme: %s", u.Scheme)
	}
	if q.Get("security") != "reality" {
		t.Fatalf("security not preserved: %q", q.Get("security"))
	}
	if q.Get("pbk") != "pubkey" || q.Get("sid") != "abcd" {
		t.Fatalf("reality opts not preserved: pbk=%q sid=%q", q.Get("pbk"), q.Get("sid"))
	}
	if q.Get("type") != "ws" || q.Get("path") != "/ws" || q.Get("host") != "edge.example.com" {
		t.Fatalf("ws params not preserved: type=%q path=%q host=%q", q.Get("type"), q.Get("path"), q.Get("host"))
	}
	if q.Get("sni") != "edge.example.com" || q.Get("fp") != "chrome" || q.Get("flow") != "xtls-rprx-vision" {
		t.Fatalf("tls params not preserved: sni=%q fp=%q flow=%q", q.Get("sni"), q.Get("fp"), q.Get("flow"))
	}
}

func TestClashProxyToURIBuildsHy2WithPortRange(t *testing.T) {
	raw := clashProxyToURI(map[string]string{
		"type":             "hysteria2",
		"name":             "demo",
		"server":           "203.10.99.51",
		"port":             "20000",
		"ports":            "20000-55000",
		"password":         "secret",
		"sni":              "www.bing.com",
		"skip-cert-verify": "true",
	})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	q := u.Query()

	if u.Scheme != "hy2" {
		t.Fatalf("unexpected scheme: %s", u.Scheme)
	}
	if q.Get("ports") != "20000-55000" {
		t.Fatalf("ports not preserved: %q", q.Get("ports"))
	}
	if q.Get("sni") != "www.bing.com" || q.Get("insecure") != "1" {
		t.Fatalf("hy2 tls params not preserved: sni=%q insecure=%q", q.Get("sni"), q.Get("insecure"))
	}
}
